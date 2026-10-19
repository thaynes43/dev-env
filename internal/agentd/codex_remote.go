package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Codex remote on a session pod (Story110 T17, option A of the T15 plan): the
// phone-pairing daemon of v1's `agent-run codex-remote`, as one remote-mode
// session. This is a slim supervisor, not the codex-host: it has no one-start
// receipt and no host-pod preflight, so a pod roll or a resume is not a dead
// end. It runs `codex remote-control start` in the session's HOME on the
// keeper's access-only auth.json, and restarts the daemon when it is gone, with
// a bounded backoff. The enrollment (codex's state sqlite and installation id)
// lives under CODEX_HOME on the session volume, so a resume is the same
// computer on the phone. The pairing code, the only secret here, is returned to
// the caller of `ctl codex-pair` and nowhere else.
//
// What codex can and cannot tell us (checked against the 0.160.1 state): its
// sqlite holds the computer's enrollment, which exists whether or not a phone
// ever paired, and the relay holds the pairing. So the status below reports the
// daemon's own state and whether this session ever issued a code, and says so.

// Remote Control states for a Codex remote session, in status.remoteControl.state
// (free text to the operator). login-required, starting and offline are the
// protocol's; these two are the Codex daemon's connected states.
const (
	// CodexRemoteUnpaired: the daemon is connected and this session has never
	// issued a pairing code, so no phone can have paired through it.
	CodexRemoteUnpaired = "unpaired"
	// CodexRemoteCodeIssued: the daemon is connected and a pairing code was
	// issued at least once. Codex has no read for the phone's side of the
	// pairing, so this does not prove a phone paired.
	CodexRemoteCodeIssued = "pair-code-issued"
	// CodexRemoteErrored: the daemon runs and codex reports its remote
	// connection as errored (an expired or rejected login, mostly).
	CodexRemoteErrored = "errored"
)

const (
	codexRemoteStateFile = "codex-remote.json"
	codexPairIssuedFile  = "codex-pair-issued.json"

	codexRemoteStartTimeout = 30 * time.Second
	codexRemoteBackoffBase  = 5 * time.Second
	codexRemoteBackoffMax   = 5 * time.Minute
	// codexRemoteStable is how long a daemon must stay up before an earlier
	// run of failed starts stops counting against the backoff.
	codexRemoteStable = time.Minute

	codexPairAttempts = 6
	codexPairWait     = 3 * time.Second
)

// codexRemoteState is ~/.agentd/codex-remote.json: the last start attempt, so
// status can tell "starting" from "offline" across the stateless collector.
type codexRemoteState struct {
	BootID    string    `json:"bootId"`
	StartedAt time.Time `json:"startedAt"`
	Attempts  int       `json:"attempts"`
	// Error is a fixed phrase, never native output.
	Error string `json:"error,omitempty"`
}

// codexPairIssued records that a pairing code was issued. It holds no code.
type codexPairIssued struct {
	At time.Time `json:"at"`
}

// codexRemoteDeps are the supervisor's seams. Production wires the codex-host
// primitives (the scrubbed native runner, the passive process inspector and the
// RPC status probe); tests fake the codex binary.
type codexRemoteDeps struct {
	native func(context.Context, Settings, string, []string, io.Writer) ([]byte, error)
	remote func(context.Context, Settings, bool, int) (codexHostRemote, error)
	sync   func(Settings, time.Time) error
	// process returns the passive inspection; hideSocket makes the inspector
	// ignore the rendezvous socket, to tell a stale socket from a live daemon.
	process func(s Settings, hideSocket bool) (codexHostProcess, error)
	sleep   func(context.Context, time.Duration) bool
}

func realCodexRemoteDeps() codexRemoteDeps {
	return codexRemoteDeps{
		native: runCodexHostNative, remote: probeCodexHostRemote, sync: SyncCodexAccess,
		process: func(s Settings, hideSocket bool) (codexHostProcess, error) {
			fs := realCodexHostFS()
			if hideSocket {
				socket := filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock")
				real := fs.lstat
				fs.lstat = func(p string) (os.FileInfo, error) {
					if p == socket {
						return nil, os.ErrNotExist
					}
					return real(p)
				}
			}
			return inspectCodexHostProcess(s, fs)
		},
		sleep: func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(d):
				return true
			}
		},
	}
}

// codexRemoteDepsFor builds the dependencies; tests replace it.
var codexRemoteDepsFor = realCodexRemoteDeps

// codexRemoteSession: this session runs the Codex phone daemon.
func codexRemoteSession(sess protocol.Session) bool {
	return sess.Agent == protocol.AgentCodex && sess.Mode == protocol.ModeRemote
}

// daemonState inspects the daemon: Live (with its process), Absent (no process,
// so stale pid files or a stale socket may be removed) or an error for unknown.
// A stale rendezvous socket makes the plain inspection unknown; a second look
// that ignores the socket separates it from a live daemon with no socket.
func (d codexRemoteDeps) daemonState(s Settings) (codexHostProcess, error) {
	if p, err := d.process(s, false); err == nil {
		return p, nil
	}
	p, err := d.process(s, true)
	if err != nil || p.State != "Absent" {
		return codexHostProcess{}, errCodexHostUnknown
	}
	return p, nil
}

// setCodexDaemonSettings asserts remote control on and auto-update off in the
// daemon's settings.json, keeping every other key (v1's cr_settings).
func setCodexDaemonSettings(s Settings) error {
	path := filepath.Join(s.CodexHome, "app-server-daemon", "settings.json")
	settings := map[string]any{}
	data, err := boundedHostFile(path, 64<<10, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errCodexRemoteState
	}
	if err == nil && (json.Unmarshal(data, &settings) != nil || settings == nil) {
		return errCodexRemoteState
	}
	updater, ok := settings["updater"].(map[string]any)
	if !ok {
		updater = map[string]any{}
	}
	updater["autoUpdateEnabled"] = false
	settings["updater"], settings["remoteControlEnabled"] = updater, true
	out, err := json.Marshal(settings)
	if err != nil || writeCodexPrivateAtomic(path, out) != nil {
		return errCodexRemoteState
	}
	return nil
}

var codexDaemonStateFiles = []string{"daemon.pid", "app-server.pid", "daemon-updater.pid", "app-server-updater.pid"}

// cleanStale removes what a dead daemon left: pid files naming dead processes
// and the rendezvous socket. They block codex's own start (v1's cr_clean). It
// is called only after daemonState said Absent.
func cleanCodexStale(s Settings) {
	dir := filepath.Join(s.CodexHome, "app-server-daemon")
	for _, name := range codexDaemonStateFiles {
		_ = os.Remove(filepath.Join(dir, name))
	}
	_ = os.Remove(filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock"))
}

// stopCodexUpdater ends the daemon's own updater and removes its pid file. The
// pin policy is that Codex updates only with the image (Tom, 2026-09-10), and a
// live updater also makes the passive inspection unknown. Only a process that
// still looks like codex is signalled.
func stopCodexUpdater(s Settings) {
	dir := filepath.Join(s.CodexHome, "app-server-daemon")
	for _, name := range []string{"daemon-updater.pid", "app-server-updater.pid"} {
		path := filepath.Join(dir, name)
		data, err := boundedHostFile(path, 16<<10, false)
		if err != nil {
			continue
		}
		var rec codexHostPID
		if json.Unmarshal(data, &rec) == nil && rec.PID > 1 {
			if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", rec.PID)); err == nil && strings.Contains(string(cmdline), "codex") {
				_ = syscall.Kill(rec.PID, syscall.SIGTERM)
			}
		}
		_ = os.Remove(path)
	}
}

// codexRemote is the supervisor's memory for one boot.
type codexRemote struct {
	deps     codexRemoteDeps
	boot     string
	attempts int
	failures int
	next     time.Time
	liveFrom time.Time
	// lastErr is the last fixed outcome logged (nil: up).
	lastErr error
}

var (
	errCodexRemoteStart = errors.New("codex remote-control did not start")
	errCodexRemoteState = errors.New("the Codex daemon state is unknown or incompatible")
)

// ensure starts the daemon when it is not running: auth first, then the stale
// files out, the settings (remote control on, auto-update off), the start, and
// the settings again, because the start rewrites them. now gates the backoff.
func (c *codexRemote) ensure(ctx context.Context, s Settings, now time.Time) error {
	p, err := c.deps.daemonState(s)
	if err != nil {
		// Unknown (a held lock, an odd pid file): touch nothing.
		return errCodexRemoteState
	}
	if p.State == "Live" {
		if c.liveFrom.IsZero() {
			c.liveFrom = now
		}
		if now.Sub(c.liveFrom) >= codexRemoteStable {
			c.failures = 0
		}
		return nil
	}
	c.liveFrom = time.Time{}
	if now.Before(c.next) {
		return nil
	}
	c.attempts++
	// The next attempt waits whether or not this one works: a start that
	// returns and a daemon that dies at once must not become a tight loop.
	c.failures++
	delay := codexRemoteBackoffBase << min(c.failures-1, 6)
	c.next = now.Add(min(delay, codexRemoteBackoffMax))
	rec := codexRemoteState{BootID: c.boot, StartedAt: now.UTC(), Attempts: c.attempts}
	err = c.start(ctx, s, now)
	if err != nil {
		rec.Error = err.Error()
	}
	_ = writeJSONFile(s.statePath(codexRemoteStateFile), rec)
	return err
}

func (c *codexRemote) start(ctx context.Context, s Settings, now time.Time) error {
	// The keeper's login must be applied and on disk before the daemon starts.
	if s.CodexAccessFile == "" || c.deps.sync(s, now) != nil {
		return ErrCodexAccess
	}
	if fi, err := os.Lstat(filepath.Join(s.CodexHome, "auth.json")); err != nil || !fi.Mode().IsRegular() {
		return ErrCodexAccess
	}
	if privateCodexHome(s) != nil {
		return errCodexRemoteState
	}
	cleanCodexStale(s)
	if err := setCodexDaemonSettings(s); err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, codexRemoteStartTimeout)
	defer cancel()
	if _, err := c.deps.native(sctx, s, s.CodexBin, []string{"remote-control", "start", "--json"}, nil); err != nil {
		return errCodexRemoteStart
	}
	// The start rewrote the settings and spawned an updater: put both right.
	if err := setCodexDaemonSettings(s); err != nil {
		return err
	}
	stopCodexUpdater(s)
	return nil
}

// startCodexRemote is the boot's agent step for a Codex remote session. The
// caller has checked that the keeper's access file is configured and applied. A
// first start that fails is a warning, not a failed boot: the supervisor keeps
// trying with backoff, and status says why.
func (d *Daemon) startCodexRemote(ctx context.Context, bootID string) (Step, string) {
	const name = "agent"
	d.crm = &codexRemote{deps: codexRemoteDepsFor(), boot: bootID}
	err := d.crm.ensure(ctx, d.S, d.now())
	if err != nil {
		d.crm.lastErr = err
		return Step{Name: name, State: StepWarn, Notes: []string{"codex remote-control is not up yet: " + err.Error() + "; agentd retries with backoff"}}, ""
	}
	return newStep(name, []string{"codex remote-control daemon is supervised; pair a phone with agent-run pair " + d.Session.Name}, nil), ""
}

// pollCodexRemote restarts a Codex remote session's daemon when it is gone. It
// logs a fixed line when the outcome changes, never native output.
func (d *Daemon) pollCodexRemote(ctx context.Context) {
	if d.crm == nil || d.writerRefused {
		return
	}
	err := d.crm.ensure(ctx, d.S, d.now())
	if errors.Is(err, d.crm.lastErr) {
		return
	}
	d.crm.lastErr = err
	if err != nil {
		d.Log.Warn("Codex remote daemon is down", "why", err.Error())
		return
	}
	d.Log.Info("Codex remote daemon is supervised")
}

// stopCodexRemote drains the daemon on shutdown: SIGTERM to the live
// app-server, then a wait for it to go, up to grace. It never restarts the
// daemon (the poll loop is over) and the enrollment stays on the volume.
func (d *Daemon) stopCodexRemote() {
	if d.crm == nil {
		return
	}
	p, err := d.crm.deps.daemonState(d.S)
	if err != nil || p.State != "Live" {
		return
	}
	d.Log.Info("SIGTERM: forwarding to the Codex daemon", "grace", d.StopGrace)
	if err := syscall.Kill(p.PID, syscall.SIGTERM); err != nil {
		d.Log.Warn("SIGTERM to the Codex daemon", "err", err)
		return
	}
	deadline := time.Now().Add(d.StopGrace)
	for time.Now().Before(deadline) {
		if cur, err := d.crm.deps.daemonState(d.S); err == nil && cur.State != "Live" {
			d.Log.Info("the Codex daemon exited")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.Log.Warn("the Codex daemon is still running after the grace period")
}

// collectCodexRemote builds status.remoteControl and the agent state for a
// Codex remote session from the daemon's own state. It reads; it starts nothing.
func collectCodexRemote(ctx context.Context, s Settings, sess protocol.Session, boot string, now time.Time) (*protocol.RemoteControl, protocol.AgentState) {
	rc := &protocol.RemoteControl{Name: sess.Name}
	if codexHostAccessReady(s, now) != nil {
		rc.State = protocol.RemoteLoginRequired
		return rc, protocol.AgentState{State: protocol.AgentLoginRequired, Error: "no current Codex projection from the keeper; the daemon cannot start or keep its login"}
	}
	var rec codexRemoteState
	started := readJSONFile(s.statePath(codexRemoteStateFile), &rec) == nil && rec.BootID == boot && boot != ""
	deps := codexRemoteDepsFor()
	p, err := deps.daemonState(s)
	if err != nil || p.State != "Live" {
		rc.State = protocol.RemoteStarting
		agent := protocol.AgentState{State: protocol.AgentPending}
		if started && err == nil {
			rc.State = protocol.RemoteOffline
			agent = protocol.AgentState{State: protocol.AgentInterrupted, Error: "the Codex daemon is not running; agentd restarts it with backoff"}
			if rec.Error != "" {
				agent.Error += " (last start: " + rec.Error + ")"
			}
		}
		return rc, agent
	}
	// A live daemon is a standing service, like v1's: busy, so the idle timer
	// does not suspend it and drop the phone's computer.
	agent := protocol.AgentState{State: protocol.AgentBusy}
	r, err := deps.remote(ctx, s, false, p.PID)
	switch {
	case err != nil:
		rc.State = protocol.RemoteStarting
	case r.Connection == "connected":
		rc.State = CodexRemoteUnpaired
		if readJSONFile(s.statePath(codexPairIssuedFile), &codexPairIssued{}) == nil {
			rc.State = CodexRemoteCodeIssued
		}
	case r.Connection == "errored":
		rc.State = CodexRemoteErrored
	default:
		rc.State = protocol.RemoteStarting
	}
	return rc, agent
}

// codexPairCodeShape bounds what is printed as a code: a short token, no
// whitespace or control characters.
var codexPairCodeShape = regexp.MustCompile(`^[A-Za-z0-9._-]{4,64}$`)

// CodexPair asks the running daemon for a pairing code and writes it, with a
// newline, to w, and nothing else. The code never goes to an argument, an
// environment variable, a file or a log: native output is captured in memory,
// reduced to the code, and dropped. Right after a start the relay enrollment may
// not be ready, so it retries a few times (v1 does the same).
func CodexPair(ctx context.Context, s Settings, sess protocol.Session, w io.Writer, now func() time.Time) error {
	return codexPair(ctx, s, sess, w, now, codexRemoteDepsFor())
}

func codexPair(ctx context.Context, s Settings, sess protocol.Session, w io.Writer, now func() time.Time, d codexRemoteDeps) error {
	if !codexRemoteSession(sess) {
		return errors.New("pair is for a codex --interactive session")
	}
	if codexHostAccessReady(s, now()) != nil {
		return ErrCodexAccess
	}
	for attempt := 0; attempt < codexPairAttempts; attempt++ {
		if attempt > 0 && !d.sleep(ctx, codexPairWait) {
			return errors.New("cancelled while waiting for codex")
		}
		p, err := d.daemonState(s)
		if err != nil || p.State != "Live" {
			if attempt == codexPairAttempts-1 {
				return errors.New("the Codex daemon is not running; agent-run show says why, and agentd restarts it with backoff")
			}
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := d.native(pctx, s, s.CodexBin, []string{"remote-control", "pair", "--json"}, nil)
		cancel()
		if err != nil {
			continue
		}
		var reply struct {
			Code string `json:"manualPairingCode"`
		}
		if json.Unmarshal(out, &reply) != nil || !codexPairCodeShape.MatchString(reply.Code) {
			continue
		}
		if _, err := fmt.Fprintln(w, reply.Code); err != nil {
			return errors.New("the pairing code could not be written")
		}
		// Only the time is kept. Its failure does not take the code back.
		_ = writeJSONFile(s.statePath(codexPairIssuedFile), codexPairIssued{At: now().UTC()})
		return nil
	}
	return errors.New("codex gave no pairing code; try again in a moment (the first pairing right after a start can race the relay)")
}
