package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// fakeCodexWorld stands in for the codex binary and the daemon it leaves
// behind. Every value in it is synthetic.
type fakeCodexWorld struct {
	t  *testing.T
	s  Settings
	mu sync.Mutex

	live        bool
	pid         int
	staleSocket bool
	unknown     bool
	startErr    error
	connection  string
	remoteErr   error
	pairFails   int
	pairOut     string

	calls           []string
	authAtStart     []bool
	pidFilesAtStart []bool
	sleeps          int
}

const fakePairCode = "SYNTH-PAIR-0001"

func newFakeCodexWorld(t *testing.T) *fakeCodexWorld {
	s, _ := hostFixture(t)
	return &fakeCodexWorld{t: t, s: s, pid: 4242, connection: "connected", pairOut: `{"manualPairingCode":"` + fakePairCode + `","expiresAt":1760000000}`}
}

func (w *fakeCodexWorld) deps() codexRemoteDeps {
	return codexRemoteDeps{
		sync: SyncCodexAccess,
		native: func(_ context.Context, s Settings, bin string, args []string, caller io.Writer) ([]byte, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if caller != nil {
				w.t.Error("native output must not stream to a caller")
			}
			w.calls = append(w.calls, bin+" "+strings.Join(args, " "))
			switch strings.Join(args, " ") {
			case "remote-control start --json":
				_, err := os.Lstat(filepath.Join(s.CodexHome, "auth.json"))
				w.authAtStart = append(w.authAtStart, err == nil)
				_, err = os.Lstat(filepath.Join(s.CodexHome, "app-server-daemon", "app-server.pid"))
				w.pidFilesAtStart = append(w.pidFilesAtStart, err == nil)
				if w.startErr != nil {
					return nil, w.startErr
				}
				w.live, w.staleSocket = true, false
				return []byte(`{"status":"started"}`), nil
			case "remote-control pair --json":
				if w.pairFails > 0 {
					w.pairFails--
					return nil, errors.New("not ready")
				}
				return []byte(w.pairOut), nil
			}
			w.t.Errorf("unexpected codex call %v", args)
			return nil, errors.New("unexpected")
		},
		remote: func(context.Context, Settings, bool, int) (codexHostRemote, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.remoteErr != nil {
				return codexHostRemote{}, w.remoteErr
			}
			return codexHostRemote{Version: managedCodexVersion, Connection: w.connection}, nil
		},
		process: func(_ Settings, hideSocket bool) (codexHostProcess, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			switch {
			case w.unknown:
				return codexHostProcess{}, errCodexHostUnknown
			case w.live:
				return codexHostProcess{State: "Live", PID: w.pid}, nil
			case w.staleSocket && !hideSocket:
				return codexHostProcess{}, errCodexHostUnknown
			}
			return codexHostProcess{State: "Absent"}, nil
		},
		sleep: func(context.Context, time.Duration) bool {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.sleeps++
			return true
		},
	}
}

func (w *fakeCodexWorld) starts() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, c := range w.calls {
		if strings.HasSuffix(c, "remote-control start --json") {
			n++
		}
	}
	return n
}

func (w *fakeCodexWorld) setLive(v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.live = v
}

var codexRemoteSess = protocol.Session{Name: "codex-1", Repo: "haynes-ops", Agent: protocol.AgentCodex, Mode: protocol.ModeRemote, Model: "gpt-6-luna"}

func TestCodexRemoteSessionSelector(t *testing.T) {
	if !codexRemoteSession(codexRemoteSess) {
		t.Fatal("codex remote not recognised")
	}
	for _, other := range []protocol.Session{
		{Agent: protocol.AgentClaude, Mode: protocol.ModeRemote},
		{Agent: protocol.AgentCodex, Mode: protocol.ModeTask},
		{Agent: protocol.AgentCodex, Mode: protocol.ModeLocal},
	} {
		if codexRemoteSession(other) {
			t.Fatalf("%s %s taken for a codex remote session", other.Agent, other.Mode)
		}
	}
}

// The keeper's login is applied and on disk before codex starts, the stale
// files a dead daemon left are gone, and the settings carry remote control on
// and auto-update off, merged over what was there.
func TestCodexRemoteStartsAfterAuthWithCleanStateAndSettings(t *testing.T) {
	w := newFakeCodexWorld(t)
	dir := filepath.Join(w.s.CodexHome, "app-server-daemon")
	writeHostFixture(t, filepath.Join(dir, "app-server.pid"), `{"pid":99999999}`)
	writeHostFixture(t, filepath.Join(w.s.CodexHome, "app-server-control", "app-server-control.sock"), "stale")
	writeHostFixture(t, filepath.Join(dir, "settings.json"), `{"shutdownGraceSeconds":30,"updater":{"updateIntervalMinutes":60}}`)
	if _, err := os.Lstat(filepath.Join(w.s.CodexHome, "auth.json")); err == nil {
		t.Fatal("fixture already has an auth file")
	}
	c := &codexRemote{deps: w.deps(), boot: "boot-1"}
	if err := c.ensure(context.Background(), w.s, time.Now()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if w.starts() != 1 || w.calls[0] != "fake-codex remote-control start --json" {
		t.Fatalf("calls %v", w.calls)
	}
	if len(w.authAtStart) != 1 || !w.authAtStart[0] {
		t.Fatal("codex started before the auth file was installed")
	}
	if w.pidFilesAtStart[0] {
		t.Fatal("a dead daemon's pid file was left for codex to trip on")
	}
	if _, err := os.Lstat(filepath.Join(w.s.CodexHome, "app-server-control", "app-server-control.sock")); err == nil {
		t.Fatal("the stale socket was left")
	}
	var settings map[string]any
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil || json.Unmarshal(data, &settings) != nil {
		t.Fatalf("settings: %v", err)
	}
	updater, _ := settings["updater"].(map[string]any)
	if settings["remoteControlEnabled"] != true || updater["autoUpdateEnabled"] != false || updater["updateIntervalMinutes"] != float64(60) || settings["shutdownGraceSeconds"] != float64(30) {
		t.Fatalf("settings %v", settings)
	}
	if fi, err := os.Stat(filepath.Join(dir, "settings.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode %v %v", fi, err)
	}
	// Up: another look starts nothing.
	if err := c.ensure(context.Background(), w.s, time.Now().Add(time.Minute)); err != nil || w.starts() != 1 {
		t.Fatalf("a live daemon was started again: %v %d", err, w.starts())
	}
}

func TestCodexRemoteNeverStartsWithoutAuth(t *testing.T) {
	for name, mutate := range map[string]func(*Settings){
		"no projection configured": func(s *Settings) { s.CodexAccessFile = "" },
		"projection file missing":  func(s *Settings) { _ = os.Remove(s.CodexAccessFile) },
		"projection unreadable":    func(s *Settings) { _ = os.WriteFile(s.CodexAccessFile, []byte("not json"), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			w := newFakeCodexWorld(t)
			mutate(&w.s)
			c := &codexRemote{deps: w.deps(), boot: "b"}
			if err := c.ensure(context.Background(), w.s, time.Now()); !errors.Is(err, ErrCodexAccess) {
				t.Fatalf("ensure: %v", err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("codex was called without a login: %v", w.calls)
			}
		})
	}
}

// A gone daemon is restarted, but never in a tight loop: each attempt doubles
// the wait up to the cap, and a daemon that stays up resets it.
func TestCodexRemoteRestartsWithBoundedBackoff(t *testing.T) {
	w := newFakeCodexWorld(t)
	c := &codexRemote{deps: w.deps(), boot: "b"}
	ctx := context.Background()
	now := time.Now()
	if err := c.ensure(ctx, w.s, now); err != nil || w.starts() != 1 {
		t.Fatalf("first start: %v", err)
	}
	// The daemon dies. Before the first wait is over nothing starts.
	w.setLive(false)
	if err := c.ensure(ctx, w.s, now.Add(codexRemoteBackoffBase-time.Second)); !errors.Is(err, errCodexRemoteDown) || w.starts() != 1 {
		t.Fatalf("restarted inside the backoff, or reported it as up: %v %d", err, w.starts())
	}
	// Starts keep failing: the wait doubles, then holds at the cap.
	w.mu.Lock()
	w.startErr = errors.New("synthetic native failure with SECRET-TEXT")
	w.mu.Unlock()
	at := now.Add(codexRemoteBackoffBase)
	var waits []time.Duration
	for i := 0; i < 12; i++ {
		err := c.ensure(ctx, w.s, at)
		if !errors.Is(err, errCodexRemoteStart) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Fatal("native error text leaked into the error")
		}
		waits = append(waits, c.next.Sub(at))
		at = c.next
	}
	if waits[0] != 2*codexRemoteBackoffBase || waits[1] != 4*codexRemoteBackoffBase || waits[len(waits)-1] != codexRemoteBackoffMax {
		t.Fatalf("waits %v", waits)
	}
	for _, d := range waits {
		if d > codexRemoteBackoffMax {
			t.Fatalf("a wait of %v is past the cap", d)
		}
	}
	// It comes back, stays up past the stable window, and the count resets.
	w.mu.Lock()
	w.startErr = nil
	w.mu.Unlock()
	if err := c.ensure(ctx, w.s, at); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if err := c.ensure(ctx, w.s, at.Add(time.Second)); err != nil || c.failures == 0 {
		t.Fatalf("early reset: %v %d", err, c.failures)
	}
	if err := c.ensure(ctx, w.s, at.Add(codexRemoteStable+2*time.Second)); err != nil || c.failures != 0 {
		t.Fatalf("no reset after a stable run: %v %d", err, c.failures)
	}
	// The record status reads names the boot and carries only a fixed phrase.
	var rec codexRemoteState
	if readJSONFile(w.s.statePath(codexRemoteStateFile), &rec) != nil || rec.BootID != "b" || rec.Attempts < 13 {
		t.Fatalf("state %+v", rec)
	}
	raw, _ := os.ReadFile(w.s.statePath(codexRemoteStateFile))
	if bytes.Contains(raw, []byte("SECRET")) {
		t.Fatal("native error text reached the state file")
	}
}

func TestCodexRemoteTouchesNothingWhenStateIsUnknown(t *testing.T) {
	w := newFakeCodexWorld(t)
	w.unknown = true
	pidFile := filepath.Join(w.s.CodexHome, "app-server-daemon", "app-server.pid")
	writeHostFixture(t, pidFile, `{"pid":1}`)
	c := &codexRemote{deps: w.deps(), boot: "b"}
	if err := c.ensure(context.Background(), w.s, time.Now()); !errors.Is(err, errCodexRemoteState) {
		t.Fatalf("ensure: %v", err)
	}
	if len(w.calls) != 0 {
		t.Fatalf("started on an unknown state: %v", w.calls)
	}
	if _, err := os.Lstat(pidFile); err != nil {
		t.Fatal("a pid file was removed on an unknown state")
	}
}

// After a pod roll the volume holds the old pod's socket: it must not block the
// restart, which is what makes a resume work.
func TestCodexRemoteRestartsOverAStaleSocket(t *testing.T) {
	w := newFakeCodexWorld(t)
	w.staleSocket = true
	sock := filepath.Join(w.s.CodexHome, "app-server-control", "app-server-control.sock")
	writeHostFixture(t, sock, "stale")
	c := &codexRemote{deps: w.deps(), boot: "b"}
	if err := c.ensure(context.Background(), w.s, time.Now()); err != nil || w.starts() != 1 {
		t.Fatalf("ensure: %v %v", err, w.calls)
	}
	if _, err := os.Lstat(sock); err == nil {
		t.Fatal("stale socket kept")
	}
}

func TestCodexDaemonStateKeepsALiveDaemonWithoutASocket(t *testing.T) {
	w := newFakeCodexWorld(t)
	d := w.deps()
	// The plain look is unknown and the socket-blind look says Live: not Absent.
	d.process = func(_ Settings, hide bool) (codexHostProcess, error) {
		if !hide {
			return codexHostProcess{}, errCodexHostUnknown
		}
		return codexHostProcess{State: "Live", PID: 7}, nil
	}
	if _, err := d.daemonState(w.s); err == nil {
		t.Fatal("a live daemon with no socket was taken for absent")
	}
}

func TestCodexPairPrintsOnlyTheCodeAndRecordsNoCode(t *testing.T) {
	w := newFakeCodexWorld(t)
	w.live = true
	w.pairFails = 2
	var out bytes.Buffer
	now := time.Now()
	if err := codexPair(context.Background(), w.s, codexRemoteSess, &out, func() time.Time { return now }, w.deps()); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if out.String() != fakePairCode+"\n" {
		t.Fatalf("output %q", out.String())
	}
	if w.sleeps != 2 {
		t.Fatalf("retried %d times", w.sleeps)
	}
	for _, c := range w.calls {
		if strings.Contains(c, fakePairCode) {
			t.Fatalf("the code reached an argument: %q", c)
		}
	}
	// The marker holds a time, never the code, and flips the status state.
	raw, err := os.ReadFile(w.s.statePath(codexPairIssuedFile))
	if err != nil || strings.Contains(string(raw), fakePairCode) {
		t.Fatalf("marker %q %v", raw, err)
	}
	if got := dirHoldsText(t, w.s.Home, fakePairCode); got != "" {
		t.Fatalf("the pairing code was written to %s", got)
	}
}

// dirHoldsText returns the first file under root that contains text.
func dirHoldsText(t *testing.T, root, text string) string {
	t.Helper()
	found := ""
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && bytes.Contains(b, []byte(text)) {
			found = path
		}
		return nil
	})
	return found
}

func TestCodexPairRefusals(t *testing.T) {
	now := func() time.Time { return time.Now() }
	t.Run("not a codex remote session", func(t *testing.T) {
		w := newFakeCodexWorld(t)
		w.live = true
		sess := codexRemoteSess
		sess.Mode = protocol.ModeLocal
		if err := codexPair(context.Background(), w.s, sess, io.Discard, now, w.deps()); err == nil || len(w.calls) != 0 {
			t.Fatalf("pair on a local session: %v %v", err, w.calls)
		}
	})
	t.Run("daemon not running", func(t *testing.T) {
		w := newFakeCodexWorld(t)
		var out bytes.Buffer
		if err := codexPair(context.Background(), w.s, codexRemoteSess, &out, now, w.deps()); err == nil || out.Len() != 0 || len(w.calls) != 0 {
			t.Fatalf("pair without a daemon: %v %q %v", err, out.String(), w.calls)
		}
	})
	t.Run("no current login", func(t *testing.T) {
		w := newFakeCodexWorld(t)
		w.live = true
		_ = os.Remove(w.s.CodexAccessFile)
		if err := codexPair(context.Background(), w.s, codexRemoteSess, io.Discard, now, w.deps()); !errors.Is(err, ErrCodexAccess) {
			t.Fatalf("pair without a login: %v", err)
		}
	})
	t.Run("codex never gives a usable code", func(t *testing.T) {
		for name, reply := range map[string]string{"empty": `{}`, "not json": `oops`, "bad shape": `{"manualPairingCode":"has space and\nnewline"}`} {
			w := newFakeCodexWorld(t)
			w.live, w.pairOut = true, reply
			var out bytes.Buffer
			if err := codexPair(context.Background(), w.s, codexRemoteSess, &out, now, w.deps()); err == nil || out.Len() != 0 {
				t.Fatalf("%s: %v %q", name, err, out.String())
			}
			if _, err := os.Lstat(w.s.statePath(codexPairIssuedFile)); err == nil {
				t.Fatalf("%s: a marker was written for a code that was never issued", name)
			}
		}
	})
	t.Run("cancelled while waiting", func(t *testing.T) {
		w := newFakeCodexWorld(t)
		w.live, w.pairFails = true, 100
		d := w.deps()
		d.sleep = func(context.Context, time.Duration) bool { return false }
		if err := codexPair(context.Background(), w.s, codexRemoteSess, io.Discard, now, d); err == nil {
			t.Fatal("pair kept going after the wait was cancelled")
		}
	})
}

func TestCollectCodexRemoteMapsDaemonState(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	setup := func(t *testing.T) (*fakeCodexWorld, func(string) (*protocol.RemoteControl, protocol.AgentState)) {
		w := newFakeCodexWorld(t)
		old := codexRemoteDepsFor
		codexRemoteDepsFor = w.deps
		t.Cleanup(func() { codexRemoteDepsFor = old })
		return w, func(boot string) (*protocol.RemoteControl, protocol.AgentState) {
			return collectCodexRemote(ctx, w.s, codexRemoteSess, boot, now)
		}
	}
	t.Run("no keeper login", func(t *testing.T) {
		w, collect := setup(t)
		_ = os.Remove(w.s.CodexAccessFile)
		rc, agent := collect("b")
		if rc.State != protocol.RemoteLoginRequired || agent.State != protocol.AgentLoginRequired {
			t.Fatalf("%+v %+v", rc, agent)
		}
	})
	t.Run("not started yet", func(t *testing.T) {
		_, collect := setup(t)
		rc, agent := collect("b")
		if rc.State != protocol.RemoteStarting || agent.State != protocol.AgentPending {
			t.Fatalf("%+v %+v", rc, agent)
		}
	})
	t.Run("started this boot and gone", func(t *testing.T) {
		w, collect := setup(t)
		writeHostFixture(t, w.s.statePath(codexRemoteStateFile), `{"bootId":"b","startedAt":"2026-10-19T12:00:00Z","attempts":2,"error":"codex remote-control did not start"}`)
		rc, agent := collect("b")
		if rc.State != protocol.RemoteOffline || agent.State != protocol.AgentInterrupted || !strings.Contains(agent.Error, "did not start") {
			t.Fatalf("%+v %+v", rc, agent)
		}
		// A record from an earlier boot is not this boot's start.
		if rc, _ := collect("other-boot"); rc.State != protocol.RemoteStarting {
			t.Fatalf("an old boot's record counted: %+v", rc)
		}
	})
	t.Run("live", func(t *testing.T) {
		w, collect := setup(t)
		w.live = true
		rc, agent := collect("b")
		if rc.State != CodexRemoteUnpaired || agent.State != protocol.AgentBusy || rc.Name != codexRemoteSess.Name || rc.URL != "" || rc.SessionID != "" {
			t.Fatalf("connected, never paired: %+v %+v", rc, agent)
		}
		writeHostFixture(t, w.s.statePath(codexPairIssuedFile), `{"at":"2026-10-19T12:00:00Z"}`)
		if rc, _ := collect("b"); rc.State != CodexRemoteCodeIssued {
			t.Fatalf("after a code: %+v", rc)
		}
		w.connection = "connecting"
		if rc, _ := collect("b"); rc.State != protocol.RemoteStarting {
			t.Fatalf("connecting: %+v", rc)
		}
		w.connection = "errored"
		if rc, a := collect("b"); rc.State != CodexRemoteErrored || a.State != protocol.AgentBusy {
			t.Fatalf("errored: %+v %+v", rc, a)
		}
		w.remoteErr = errCodexHostUnknown
		if rc, _ := collect("b"); rc.State != protocol.RemoteStarting {
			t.Fatalf("probe failed: %+v", rc)
		}
	})
}

// CollectStatus reports a codex remote session through the same mapping, and
// does not ask for a Claude login.
func TestCollectStatusForCodexRemote(t *testing.T) {
	w := newFakeCodexWorld(t)
	old := codexRemoteDepsFor
	codexRemoteDepsFor = w.deps
	t.Cleanup(func() { codexRemoteDepsFor = old })
	w.live = true
	writeHostFixture(t, w.s.statePath(bootFile), `{"bootId":"b","bootedAt":"2026-10-19T12:00:00Z","boot":"ready"}`)
	st := CollectStatus(context.Background(), &fakeRunner{}, w.s, codexRemoteSess, time.Now())
	if st.RemoteControl == nil || st.RemoteControl.State != CodexRemoteUnpaired || st.Agent.State != protocol.AgentBusy {
		t.Fatalf("status %+v / %+v", st.RemoteControl, st.Agent)
	}
	if strings.Contains(st.Agent.Error, "Claude") {
		t.Fatalf("a Claude login message on a codex session: %q", st.Agent.Error)
	}
}

func newCodexRemoteDaemon(t *testing.T, w *fakeCodexWorld) (*Daemon, *bytes.Buffer) {
	var logs bytes.Buffer
	d := &Daemon{S: w.s, R: &fakeRunner{}, Log: slog.New(slog.NewTextHandler(&logs, nil)), Session: codexRemoteSess, StopGrace: 3 * time.Second}
	old := codexRemoteDepsFor
	codexRemoteDepsFor = w.deps
	t.Cleanup(func() { codexRemoteDepsFor = old })
	return d, &logs
}

// The boot's agent step starts the daemon; a failing first start is a warning
// the poll loop recovers from, not a failed boot that never would.
func TestStartCodexRemoteStepAndPollRecovery(t *testing.T) {
	w := newFakeCodexWorld(t)
	d, logs := newCodexRemoteDaemon(t, w)
	w.startErr = errors.New("synthetic")
	step, agentErr := d.startAgent(context.Background(), protocol.Workspace{}, Step{State: StepOK}, "boot-1")
	if agentErr != "" || step.State != StepWarn || !strings.Contains(strings.Join(step.Notes, " "), "retries with backoff") {
		t.Fatalf("step %+v %q", step, agentErr)
	}
	// It recovers once codex can start, when the backoff is over.
	w.mu.Lock()
	w.startErr = nil
	w.mu.Unlock()
	d.crm.next = time.Time{}
	d.pollCodexRemote(context.Background())
	if w.starts() != 2 {
		t.Fatalf("the poll did not restart the daemon: %v", w.calls)
	}
	if !strings.Contains(logs.String(), "supervised") {
		t.Fatalf("no recovery line: %s", logs.String())
	}
	// A claude remote session and a task are left alone by the poll.
	d2, _ := newCodexRemoteDaemon(t, w)
	d2.pollCodexRemote(context.Background())
}

func TestStartCodexRemoteWithoutAuthIsNotStarted(t *testing.T) {
	w := newFakeCodexWorld(t)
	_ = os.Remove(w.s.CodexAccessFile)
	d, _ := newCodexRemoteDaemon(t, w)
	step, _ := d.startAgent(context.Background(), protocol.Workspace{}, Step{State: StepOK}, "boot-1")
	if step.State != StepWarn || len(w.calls) != 0 {
		t.Fatalf("step %+v calls %v", step, w.calls)
	}
}

// SIGTERM drains the daemon: it is signalled, awaited up to the grace, and
// nothing restarts it afterwards.
func TestCodexRemoteDrainOnShutdown(t *testing.T) {
	w := newFakeCodexWorld(t)
	d, logs := newCodexRemoteDaemon(t, w)
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	exited := make(chan struct{})
	go func() { _ = child.Wait(); w.setLive(false); close(exited) }()
	w.pid, w.live = child.Process.Pid, true
	d.crm = &codexRemote{deps: w.deps(), boot: "b"}
	d.shutdown()
	select {
	case <-exited:
	default:
		t.Fatalf("the daemon was not signalled; logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "the Codex daemon exited") {
		t.Fatalf("logs: %s", logs.String())
	}
	if w.starts() != 0 {
		t.Fatal("shutdown started a daemon")
	}
}

func TestCodexRemoteDrainIsBoundedByTheGrace(t *testing.T) {
	w := newFakeCodexWorld(t)
	d, logs := newCodexRemoteDaemon(t, w)
	d.StopGrace = 300 * time.Millisecond
	child := exec.Command("sh", "-c", "trap '' TERM; sleep 60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	w.pid, w.live = child.Process.Pid, true
	d.crm = &codexRemote{deps: w.deps(), boot: "b"}
	began := time.Now()
	d.stopCodexRemote()
	if time.Since(began) > 3*time.Second || !strings.Contains(logs.String(), "still running after the grace") {
		t.Fatalf("drain not bounded: %v %s", time.Since(began), logs.String())
	}
}

// While the daemon is down and the supervisor waits out the backoff, the log
// says it is down once, never that it is supervised (a review finding).
func TestPollCodexRemoteNeverLogsUpWhileDown(t *testing.T) {
	w := newFakeCodexWorld(t)
	d, logs := newCodexRemoteDaemon(t, w)
	w.startErr = errors.New("synthetic")
	now := time.Now()
	d.Now = func() time.Time { return now }
	if step, _ := d.startAgent(context.Background(), protocol.Workspace{}, Step{State: StepOK}, "boot-1"); step.State != StepWarn {
		t.Fatalf("step %+v", step)
	}
	for i := 0; i < 3; i++ {
		now = now.Add(time.Second) // still inside the 5 s wait
		d.pollCodexRemote(context.Background())
	}
	if strings.Contains(logs.String(), "is supervised") {
		t.Fatalf("a down daemon was logged as up:\n%s", logs.String())
	}
	// The boot step already carried the failed start, so the polls add at most
	// one line of their own and do not repeat it.
	if n := strings.Count(logs.String(), "Codex remote daemon is down"); n > 1 {
		t.Fatalf("%d down lines, want at most 1:\n%s", n, logs.String())
	}
	// It comes up: one recovery line.
	w.mu.Lock()
	w.startErr = nil
	w.mu.Unlock()
	now = now.Add(codexRemoteBackoffBase)
	d.pollCodexRemote(context.Background())
	if !strings.Contains(logs.String(), "supervised") {
		t.Fatalf("no recovery line:\n%s", logs.String())
	}
}
