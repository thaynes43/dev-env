package agentd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
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
	// ProjectCatalog is constructed from trusted pod configuration by the
	// command. Disabled private bootstrap never invokes its reader.
	ProjectCatalog *ProjectCatalogSource
	// grants is a focused test seam for the boot and maintenance refresh hook.
	// Production always derives grants from the pod environment.
	grants func(func(string) string) Grants

	failures int
	// copying is set while a copy of the log to the shared volume runs, so a
	// hung CephFS write never stacks copies (D-65).
	copying atomic.Bool
	// writerRefused keeps an uncertain shared writer observational only.
	writerRefused bool
	// remote is a remote session's launch context (Story110 T03), kept for the
	// boot so a login that lands later can start the agent; waiting is true
	// while the boot found no usable login.
	remote *remoteWait
	// loginLocked is set while this daemon holds the pod's login lock, so the
	// agent start inside pollRemoteLogin does not take it a second time.
	loginLocked bool
	// claudeSyncErr is the last projection outcome logged (nil: applied).
	claudeSyncErr error
	// crm supervises a Codex remote session's phone daemon (Story110 T17).
	crm *codexRemote

	// measure is the conversation usage measurement; nil means
	// MeasureConversationUsage. It is a test seam.
	measure  func(Settings, protocol.Session, string, string, time.Time) protocol.ConversationUsage
	usage    conversationUsageCache
	tmpUsage tmpUsageMonitor
}

// conversationUsageEvery is how often an interactive session's conversation
// usage is measured: slower than the heartbeat, because a measurement scans the
// provider's record. The newest figure rides every heartbeat between them.
const conversationUsageEvery = 5 * time.Minute

// conversationUsageCache holds the newest published figure. A measurement runs
// in the background so a large record never delays a heartbeat, and one at a
// time.
type conversationUsageCache struct {
	mu      sync.Mutex
	figure  *protocol.ConversationUsage
	started time.Time
	running bool
}

// conversationUsage returns the newest measured figure of an interactive
// session, or nil: for a task session, before the first measurement finishes,
// and whenever the newest measurement was Unknown or not valid. It starts a new
// measurement in the background when the last one is older than
// conversationUsageEvery.
func (d *Daemon) conversationUsage() *protocol.ConversationUsage {
	if d.Session.Mode != protocol.ModeLocal && d.Session.Mode != protocol.ModeRemote {
		return nil
	}
	now := d.now()
	d.usage.mu.Lock()
	defer d.usage.mu.Unlock()
	if !d.usage.running && (d.usage.started.IsZero() || now.Sub(d.usage.started) >= conversationUsageEvery) {
		d.usage.running, d.usage.started = true, now
		measure := d.measure
		if measure == nil {
			measure = MeasureConversationUsage
		}
		session, settings := d.Session, d.S
		go func() {
			u := measure(settings, session, session.SessionUID, settings.PodUID, now)
			d.usage.mu.Lock()
			defer d.usage.mu.Unlock()
			d.usage.running = false
			if u.Valid() && u.ResultState != protocol.ConversationUnknown {
				d.usage.figure = &u
			} else {
				d.usage.figure = nil
			}
		}()
	}
	if d.usage.figure == nil {
		return nil
	}
	figure := *d.usage.figure
	return &figure
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
	if err := ValidatePrivateProjectBootstrap(d.S); err != nil {
		return err
	}
	stopDeadline, err := d.armTaskBudgetDeadline()
	if err != nil {
		return fmt.Errorf("task budget: %w", err)
	}
	defer stopDeadline()
	stopBudgetWatch, err := d.armPrivateTaskBudgetWatch(ctx)
	if err != nil {
		return fmt.Errorf("private task budget: %w", err)
	}
	defer stopBudgetWatch()
	d.expireCredentials()
	if err := os.MkdirAll(d.S.StateDir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	rec := bootRecord{BootID: newBootID(), BootedAt: d.now().UTC(), Boot: protocol.BootBooting, CurrentStep: "admitting workspace"}
	if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
		return fmt.Errorf("boot record: %w", err)
	}
	d.Log.Info("boot", "session", d.Session.Name, "boot", rec.BootID, "repo", d.Session.Repo, "agent", d.Session.Agent, "mode", d.Session.Mode, "model", d.Session.Model, "promptBytes", len(d.Session.Prompt))
	stopBootHeartbeat := d.bootHeartbeat(ctx)
	defer stopBootHeartbeat()
	progress := func(step string) {
		rec.CurrentStep = step
		if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
			d.Log.Warn("boot progress record", "err", err)
		}
	}
	writer, writerErr := retryWorkspaceWriterAdmission(ctx, func() (*writerLease, error) {
		return acquireWorkspaceWriter(ctx, d.R, d.S, d.Session, d.now())
	}, pauseWorkspaceAdmission)
	if writer != nil {
		d.S.writer = writer
		defer writer.unlock()
	}
	if writerErr != nil {
		stopBootHeartbeat()
		d.writerRefused = true
		rec.Boot, rec.AgentError = protocol.BootFailed, "workspace writer admission: "+writerErr.Error()
		rec.Steps = []Step{newStep("workspace", nil, writerErr)}
		if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
			return fmt.Errorf("failed admission boot record: %w", err)
		}
		d.beat(ctx)
		return d.supervise(ctx)
	}

	progress("admitting project")
	if err := prepareProjectTask(&d.S, d.Session); err != nil {
		stopBootHeartbeat()
		d.writerRefused = true
		rec.Boot, rec.AgentError = protocol.BootFailed, "project admission: "+err.Error()
		rec.Steps = []Step{newStep("project", nil, err)}
		if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
			return fmt.Errorf("failed project boot record: %w", err)
		}
		d.beat(ctx)
		return d.supervise(ctx)
	}
	progress("rendering configuration")
	steps := Render(ctx, d.R, d.S, d.Session)
	if d.S.PrivateProjectBootstrap {
		progress("bootstrapping projects: reading catalog")
		report, err := refreshPrivateProjects(ctx, d.R, d.S, d.ProjectCatalog, progress)
		// The first note is the code summary status.agent.problems carries;
		// the codes themselves go to status.agent.projectBootstrap.
		steps = append(steps, newStep("project-bootstrap", []string{report.summary()}, err))
		rec.ProjectBootstrap = report.published()
	}
	progress("preparing repo")
	ws, repoStep := PrepareRepo(ctx, d.R, d.S, d.Session)
	steps = append(steps, repoStep)
	if stop, err := workspaceStopRequested(d.S, d.Session); err != nil || stop {
		stopBootHeartbeat()
		d.shutdown()
		return err
	}
	progress("starting agent")
	agentStep, agentErr := d.startAgent(ctx, ws, repoStep, rec.BootID)
	steps = append(steps, agentStep)
	LogSteps(d.Log, steps)
	stopBootHeartbeat()

	rec.Steps, rec.Workspace, rec.AgentError = steps, &ws, agentErr
	rec.CurrentStep = ""
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

const bootHeartbeatEvery = 10 * time.Second

// bootHeartbeat observes admission, rendering and Git preparation waits. It
// never reads the shared worktree, runs another command, or claims a writer.
// Its sender is joined before the final boot status and normal supervision,
// so heartbeat state has one owner and cancellation leaves no boot goroutine.
func (d *Daemon) bootHeartbeat(ctx context.Context) func() {
	if d.Send == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	// Project admission may update d.S while the boot proceeds. The boot
	// status needs only these stable local paths, never the mutable writer.
	s, sess, r := d.S, d.Session, d.R
	beat := func() {
		d.sendHeartbeat(ctx, CollectStatus(ctx, r, s, sess, d.now()))
	}
	beat()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTimer(bootHeartbeatEvery)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				beat()
				// Start the next interval after sending: a slow request must
				// not leave a queued tick that sends again immediately.
				tick.Reset(bootHeartbeatEvery)
			}
		}
	}()
	return func() { cancel(); <-done }
}

// startAgent starts the session's agent. On the volume's first boot that is
// the task (D-42) or a local session's TUI. Later interactive/shared boots
// resume the TUI (D-58); retained private tasks never automatically resume
// (DESIGN-025 24). It returns the boot step and,
// when the agent cannot run, why.
func (d *Daemon) startAgent(ctx context.Context, ws protocol.Workspace, repo Step, bootID string) (Step, string) {
	if repo.State != StepFail && retainedPrivateTask(d.S, d.Session) {
		return newStep("agent", []string{"private task launch/result retained; no automatic provider resume"}, nil), ""
	}
	if codexRemoteSession(d.Session) {
		// Story110 T17: no CLI launch and no conversation to resume; the
		// agent is the phone daemon, which the poll loop keeps up.
		return d.startCodexRemote(ctx, bootID)
	}
	const name = "agent"
	if d.Session.Agent == protocol.AgentCodex && (d.S.CodexAccessFile == "" || SyncCodexAccess(d.S, d.now()) != nil) {
		return newStep(name, nil, ErrCodexAccess), ErrCodexAccess.Error()
	}
	if repo.State == StepFail {
		why := "the repo step failed, so the agent was not started"
		return newStep(name, nil, errors.New(why)), why
	}
	if d.projectsClaude() {
		// Projection mode: apply the keeper's login before anything starts.
		// A pod-owned login on this HOME is a conflict, not a wait.
		if err := SyncClaudeAccess(d.S, d.now()); errors.Is(err, ErrClaudeLoginConflict) {
			return newStep(name, nil, err), err.Error()
		}
	}
	if d.Session.Mode == protocol.ModeRemote {
		d.remote = &remoteWait{ws: ws, repo: repo, boot: bootID}
		// Starting the agent's CLI and opening a login ceremony exclude each
		// other (agentd ctl login url holds the same lock). If a ceremony is
		// being opened right now, hold the agent: the poll starts it once that
		// ceremony's window has ended. This is not the no-login wait, so the
		// loginSettle override never cuts a ceremony short.
		release, ok := d.lockLogin()
		if !ok {
			if !remoteLoginUsable(d.S, d.Session, d.now()) {
				return d.waitForLogin()
			}
			d.remote.deferred = true
			return newStep(name, []string{"held: a login ceremony is opening; the agent starts when its window ends"}, nil), ""
		}
		defer release()
	}
	if !remoteLoginUsable(d.S, d.Session, d.now()) {
		return d.waitForLogin()
	}
	var first Launch
	firstErr := readJSONFile(d.S.statePath(launchFile), &first)
	if firstErr != nil && !errors.Is(firstErr, os.ErrNotExist) {
		why := "saved launch is unreadable or invalid; initial prompt cannot be replayed"
		return newStep(name, nil, errors.New(why)), why
	}
	if firstErr == nil {
		l, err := BuildResume(d.S, d.Session, ws, first, bootID, d.now())
		if err != nil {
			return newStep(name, nil, err), err.Error()
		}
		if err := admitWorkspaceLaunch(ctx, d.S, d.Session, &l); err != nil {
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
	if err := admitWorkspaceLaunch(ctx, d.S, d.Session, &launch); err != nil {
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

// waitForLogin holds a remote session's agent until the pod has a usable
// Claude login. It starts nothing: the login ceremony begins only when someone
// asks for it (agentd ctl login url, from agent-run login). The boot stays
// ready; status reports login-required.
func (d *Daemon) waitForLogin() (Step, string) {
	d.remote.waiting = true
	notes := []string{"login-required: no usable Claude login in this pod; no login is started until agent-run login asks for it"}
	if d.projectsClaude() {
		notes = []string{"login-required: waiting for the keeper's Claude projection; no login is run in this pod"}
	}
	return newStep("agent", notes, nil), ""
}

// lockLogin takes the pod's login lock for this daemon unless it already holds
// it; ok is false only while another process holds it. A lock that cannot be
// taken at all (an unusable state directory) never keeps the agent down: the
// daemon logs it and goes on, since `login url` fails closed on the same error.
func (d *Daemon) lockLogin() (release func(), ok bool) {
	if d.loginLocked {
		return func() {}, true
	}
	rel, err := acquireLoginLock(d.S)
	if err != nil {
		if errors.Is(err, ErrLoginBusy) {
			return nil, false
		}
		d.Log.Warn("login lock unavailable; going on without it", "err", err)
		return func() {}, true
	}
	d.loginLocked = true
	return func() { d.loginLocked = false; rel() }, true
}

// loginSettle is how long a finished login may leave its window open before
// agentd starts the agent anyway.
const loginSettle = 30 * time.Second

// pollRemoteLogin starts a remote session's agent when a login lands and no
// agent CLI runs: at boot with no login (waiting), or later after a requested
// login while the agent was not running (a renewal or a recovery). It acts only
// once the login window has ended, so the CLI has written the whole login, and
// never while the agent's CLI is alive (one owner for the login).
func (d *Daemon) pollRemoteLogin(ctx context.Context) {
	w := d.remote
	if w == nil || d.writerRefused || !remoteLoginUsable(d.S, d.Session, d.now()) {
		return
	}
	// The pid check, the window's end and the agent start are one critical
	// section against `login url`; a busy lock means a ceremony is opening, so
	// this poll leaves everything as it is.
	release, ok := d.lockLogin()
	if !ok {
		return
	}
	defer release()
	var p agentPid
	if readJSONFile(d.S.statePath(pidFile), &p) == nil && pidAlive(p) {
		w.since = time.Time{}
		return
	}
	if !w.waiting && !w.deferred && !hasLoginWindow(ctx, d.R, d.S) {
		return
	}
	if w.since.IsZero() {
		w.since = d.now()
	}
	// A renewal's old login already reads as usable, so only the window's end
	// (the CLI finished, well or not) says the ceremony is over. A boot that
	// found no login also starts once the new one has sat loginSettle.
	if !loginWindowEnded(ctx, d.R, d.S) && (!w.waiting || d.now().Sub(w.since) < loginSettle) {
		return
	}
	w.waiting, w.deferred, w.since = false, false, time.Time{}
	if !d.projectsClaude() {
		StopLoginWindow(ctx, d.R, d.S)
	}
	step, agentErr := d.startAgent(ctx, w.ws, w.repo, w.boot)
	LogSteps(d.Log, []Step{step})
	var rec bootRecord
	if readJSONFile(d.S.statePath(bootFile), &rec) == nil && rec.BootID == w.boot {
		for i := range rec.Steps {
			if rec.Steps[i].Name == step.Name {
				rec.Steps[i] = step
			}
		}
		rec.AgentError = agentErr
		rec.Boot = protocol.BootReady
		if agentErr != "" {
			rec.Boot = protocol.BootFailed
		}
		if err := writeJSONFile(d.S.statePath(bootFile), rec); err != nil {
			d.Log.Error("boot record", "err", err)
		}
	}
	d.beat(ctx)
}

func (d *Daemon) supervise(ctx context.Context) error {
	d.tmpUsage.check(d.now(), d.Log)
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
			if !d.writerRefused {
				d.shutdown()
			}
			return nil
		case <-tick.C:
			d.tmpUsage.check(d.now(), d.Log)
			d.beat(ctx)
		case <-logs.C:
			if !d.writerRefused {
				d.copyLog()
			}
		case <-poll.C:
			if stop, err := workspaceStopRequested(d.S, d.Session); err != nil || stop {
				if !d.writerRefused {
					d.shutdown()
				}
				return err
			}
			d.expireCredentials()
			if d.writerRefused {
				continue
			}
			if err := SyncCodexAccess(d.S, d.now()); err != nil {
				d.Log.Warn("Codex authentication requires renewal or publication")
			}
			if err := d.dispatchDecision(ctx); err != nil {
				d.Log.Warn("managed child decision continuation is unavailable or uncertain")
			}
			d.syncClaudeAccess()
			d.pollRemoteLogin(ctx)
			d.pollCodexRemote(ctx)
			d.continueAfterRenewal(ctx)
			if m := newestMtime(d.S.statePath(resultFile), d.S.statePath(tuiExitFile)); !m.Equal(last) {
				last = m
				d.Log.Info("the agent exited")
				d.beat(ctx)
				d.copyLog()
			}
		}
	}
}

// projectsClaude: this session follows the keeper's Claude login projection.
func (d *Daemon) projectsClaude() bool {
	return d.S.ClaudeLiveFile != "" && d.Session.Agent == protocol.AgentClaude
}

// syncClaudeAccess follows the keeper's newest generation on each poll. It
// logs a fixed line when the outcome changes, not every poll.
func (d *Daemon) syncClaudeAccess() {
	if !d.projectsClaude() {
		return
	}
	err := SyncClaudeAccess(d.S, d.now())
	if errors.Is(err, d.claudeSyncErr) || (err == nil && d.claudeSyncErr == nil) {
		return
	}
	d.claudeSyncErr = err
	switch {
	case err == nil:
		d.Log.Info("Claude projection applied")
	case errors.Is(err, ErrClaudeLoginConflict):
		d.Log.Warn("Claude projection not applied: the pod holds its own login")
	default:
		d.Log.Warn("Claude authentication requires a current keeper projection")
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
	g := c.Grants
	if d.grants != nil {
		g = d.grants(d.S.Getenv)
	}
	g.Now = d.now
	if err := g.Refresh(); err != nil && !errors.Is(err, ErrNoGrantsDir) {
		d.Log.Warn("grant kubeconfig refresh failed")
	}
}

// shutdown forwards the pod's SIGTERM to the agent CLI and waits for it to
// exit, up to StopGrace. tini signals only agentd, and the CLI runs under
// tmux, so nothing else would reach it; a CLI that is SIGKILLed instead
// leaves its Remote Control entry offline and unarchived (S-6, S-15).
func (d *Daemon) shutdown() {
	d.stopCodexRemote()
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
	st := CollectStatus(ctx, d.R, d.S, d.Session, d.now())
	st.ConversationUsage = d.conversationUsage()
	d.sendHeartbeat(ctx, st)
}

func (d *Daemon) sendHeartbeat(ctx context.Context, st protocol.Status) {
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
