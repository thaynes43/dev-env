package agentd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Daemon is `agentd run`, the session pod's supervisor under tini (DESIGN-001
// 3.6): it renders the config, prepares the repo, starts the agent, then
// heartbeats until SIGTERM, when it forwards the signal to the agent CLI and
// waits for it.
type Daemon struct {
	S       Settings
	R       Runner
	Log     *slog.Logger
	Session protocol.Session
	// Self is agentd's own path, which the tmux pane runs as `run-agent`.
	Self string
	// Send posts a heartbeat; nil turns heartbeats off.
	Send func(context.Context, protocol.Status) error
	// Interval is the heartbeat period (60 s, DESIGN-001 3.6); Poll is how
	// often agentd looks for the task's result, to report it at once.
	Interval time.Duration
	Poll     time.Duration
	// StopGrace is how long shutdown waits for the agent CLI after SIGTERM.
	// It must fit in the pod's termination grace period.
	StopGrace time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	failures int
	// copying is set while a copy of the log to the shared volume runs, so a
	// hung CephFS write never stacks copies (D-65).
	copying atomic.Bool
}

// logCopyEvery is how often the session's log is copied to the shared volume,
// besides when the agent exits and at shutdown (D-65).
const logCopyEvery = 5 * time.Minute

// copyLog copies the session's log to the shared volume in the background. A
// CephFS outage can hang a write, so it never blocks the daemon, and a copy
// still running is not started twice.
func (d *Daemon) copyLog() {
	if !d.copying.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer d.copying.Store(false)
		if err := copyLogToShared(d.S, d.Session.Name); err != nil {
			d.Log.Warn("copy the log to the shared volume", "err", err)
		}
	}()
}

func (d *Daemon) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run boots and supervises until ctx ends. Boot steps never stop it: a step
// that fails is reported in the status, and the heartbeat goes on, so the
// operator sees why a session is not working.
func (d *Daemon) Run(ctx context.Context) error {
	d.expireCredentials()
	if err := os.MkdirAll(d.S.StateDir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	rec := bootRecord{BootID: newBootID(), BootedAt: d.now().UTC(), Boot: protocol.BootBooting}
	if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
		return fmt.Errorf("boot record: %w", err)
	}
	d.Log.Info("boot", "session", d.Session.Name, "boot", rec.BootID, "repo", d.Session.Repo, "agent", d.Session.Agent, "mode", d.Session.Mode, "model", d.Session.Model, "promptBytes", len(d.Session.Prompt))
	d.beat(ctx)

	steps := Render(ctx, d.R, d.S, d.Session)
	ws, repoStep := PrepareRepo(ctx, d.R, d.S, d.Session)
	steps = append(steps, repoStep)
	agentStep, agentErr := d.startAgent(ctx, ws, repoStep, rec.BootID)
	steps = append(steps, agentStep)
	LogSteps(d.Log, steps)

	rec.Steps, rec.Workspace, rec.AgentError = steps, &ws, agentErr
	rec.Boot = protocol.BootReady
	if agentErr != "" {
		rec.Boot = protocol.BootFailed
	}
	if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
		d.Log.Error("boot record", "err", err)
	}
	d.beat(ctx)
	return d.supervise(ctx)
}

// startAgent starts the session's agent. On the volume's first boot that is
// the task (D-42) or a local session's TUI; on every later boot it is the TUI,
// resuming the first launch's conversation, so a task's prompt never runs
// twice and resume is the way back in (D-58). It returns the boot step and,
// when the agent cannot run, why.
func (d *Daemon) startAgent(ctx context.Context, ws protocol.Workspace, repo Step, bootID string) (Step, string) {
	const name = "agent"
	if repo.State == StepFail {
		why := "the repo step failed, so the agent was not started"
		return newStep(name, nil, errors.New(why)), why
	}
	var first Launch
	if readJSONFile(d.S.statePath(launchFile), &first) == nil {
		l, err := BuildResume(d.S, d.Session, ws, first, bootID, d.now())
		if err != nil {
			return newStep(name, nil, err), err.Error()
		}
		if err := StartAgent(ctx, d.R, d.S, l, d.Self); err != nil {
			return newStep(name, nil, err), err.Error()
		}
		notes := []string{fmt.Sprintf("resumed conversation %s, first launched %s, in the TUI in tmux session %q", first.ConversationID, first.CreatedAt.Format(time.RFC3339), TmuxSession)}
		if !first.TUI {
			var res taskResult
			ended := "it left no result"
			if readJSONFile(d.S.statePath(resultFile), &res) == nil {
				ended = fmt.Sprintf("it ended with exit %d at %s", res.ExitCode, res.FinishedAt.Format(time.RFC3339))
			}
			notes = append(notes, "the task's prompt is not run again (D-42); "+ended)
		}
		return newStep(name, notes, nil), ""
	}
	launch, err := BuildLaunch(d.S, d.Session, ws, bootID, d.now())
	if err != nil {
		return newStep(name, nil, err), err.Error()
	}
	if err := StartAgent(ctx, d.R, d.S, launch, d.Self); err != nil {
		return newStep(name, nil, err), err.Error()
	}
	what := "claude task"
	if launch.TUI {
		what = "claude TUI"
	}
	return newStep(name, []string{fmt.Sprintf("%s started in tmux session %q, conversation %s", what, TmuxSession, launch.ConversationID)}, nil), ""
}

func (d *Daemon) supervise(ctx context.Context) error {
	tick := time.NewTicker(d.Interval)
	defer tick.Stop()
	poll := time.NewTicker(d.Poll)
	defer poll.Stop()
	logs := time.NewTicker(logCopyEvery)
	defer logs.Stop()
	last := newestMtime(d.S.statePath(resultFile), d.S.statePath(tuiExitFile))
	for {
		select {
		case <-ctx.Done():
			d.shutdown()
			return nil
		case <-tick.C:
			d.beat(ctx)
		case <-logs.C:
			d.copyLog()
		case <-poll.C:
			d.expireCredentials()
			if m := newestMtime(d.S.statePath(resultFile), d.S.statePath(tuiExitFile)); !m.Equal(last) {
				last = m
				d.Log.Info("the agent exited")
				d.beat(ctx)
				d.copyLog()
			}
		}
	}
}

// The daemon removes private credential files even while the keeper or broker
// is unavailable. Missing grants volumes are normal for older session pods.
func (d *Daemon) expireCredentials() {
	if d.S.Getenv == nil {
		return
	}
	c := CredentialsFromEnv(d.S.Getenv)
	c.Grants.Now = d.now
	if err := c.Expire(); err != nil && !errors.Is(err, ErrNoGrantsDir) {
		d.Log.Warn("credential expiry cleanup failed")
	}
}

// shutdown forwards the pod's SIGTERM to the agent CLI and waits for it to
// exit, up to StopGrace. tini signals only agentd, and the CLI runs under
// tmux, so nothing else would reach it; a CLI that is SIGKILLed instead
// leaves its Remote Control entry offline and unarchived (S-6, S-15).
func (d *Daemon) shutdown() {
	var p agentPid
	if readJSONFile(d.S.statePath(pidFile), &p) == nil && pidAlive(p) {
		d.Log.Info("SIGTERM: forwarding to the agent", "pid", p.Pid, "grace", d.StopGrace)
		if err := syscall.Kill(p.Pid, syscall.SIGTERM); err != nil {
			d.Log.Warn("SIGTERM to the agent", "err", err)
		}
		deadline := time.Now().Add(d.StopGrace)
		for pidAlive(p) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if pidAlive(p) {
			d.Log.Warn("the agent is still running after the grace period", "pid", p.Pid)
		} else {
			d.Log.Info("the agent exited")
		}
		// Let run-agent write the result before the last heartbeat.
		for i := 0; i < 20 && !exists(d.S.statePath(resultFile)); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	// A last copy of the log, given at most a few seconds of the grace. A
	// periodic copy still running holds an older log, so once it is done the
	// last one starts.
	deadline := time.Now().Add(3 * time.Second)
	for started := false; time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !started {
			started = d.copying.CompareAndSwap(false, true)
			if started {
				go func() {
					defer d.copying.Store(false)
					if err := copyLogToShared(d.S, d.Session.Name); err != nil {
						d.Log.Warn("copy the log to the shared volume", "err", err)
					}
				}()
			}
			continue
		}
		if !d.copying.Load() {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d.beat(ctx)
}

// beat sends one heartbeat. A failure never touches the agent (D-01: running
// sessions do not notice an operator outage); it is logged on the first
// failure and every tenth after it, and the recovery is logged too.
func (d *Daemon) beat(ctx context.Context) {
	if d.Send == nil {
		return
	}
	st := CollectStatus(ctx, d.R, d.S, d.Session.Name, d.now())
	err := d.Send(ctx, st)
	switch {
	case err == nil && d.failures > 0:
		d.Log.Info("heartbeat recovered", "after", d.failures)
		d.failures = 0
	case err != nil:
		d.failures++
		if d.failures == 1 || d.failures%10 == 0 {
			d.Log.Warn("heartbeat failed", "err", err, "failures", d.failures)
		}
	}
}
