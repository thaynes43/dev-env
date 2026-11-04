package agentd

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// OpenProject #397, from the advisory on PR #900: a refused held launch
// (DESIGN-054) ends the login window, and a ceremony opener (agentd ctl login
// url, LoginURL) checks for and creates that window under the pod's login
// lock. The refusal's stop and the opener's window creation must agree on
// that lock, or a window the opener creates after the stop outlives the
// refused session. These tests run the daemon's poll by hand on a fake clock,
// and the opener is LoginURL itself, so every step is ordered by the test.

// loginRaceTmux is one pod's tmux as two processes see it: the daemon and a
// ceremony opener each run their commands through their own Runner over the
// same login window. The opener's first look at the login window waits on
// gate: LoginURL makes it holding the login lock, after its checks and before
// it creates the window, so a test can act while an opener holds the lock.
type loginRaceTmux struct {
	mu   sync.Mutex
	tm   loginTmux
	once sync.Once
	// reached closes when the opener is at the gate, holding the lock.
	reached, gate chan struct{}
}

func newLoginRaceTmux() *loginRaceTmux {
	return &loginRaceTmux{reached: make(chan struct{}), gate: make(chan struct{})}
}

func (f *loginRaceTmux) handle(c Cmd) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tm.handle(c)
}

// runner is the daemon's view.
func (f *loginRaceTmux) runner() *fakeRunner { return &fakeRunner{handle: f.handle} }

// openerRunner is the ceremony opener's view, held at the gate once.
func (f *loginRaceTmux) openerRunner() *fakeRunner {
	return &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Args[0] == "has-session" && len(c.Args) > 2 && c.Args[2] == "="+LoginTmuxSession {
			f.once.Do(func() { close(f.reached); <-f.gate })
		}
		return f.handle(c)
	}}
}

func (f *loginRaceTmux) window() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tm.window
}

// counts is how many login windows were created and stopped.
func (f *loginRaceTmux) counts() (started, stopped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tm.started, f.tm.killed
}

// finishCeremony is the login CLI ending in its window, well.
func (f *loginRaceTmux) finishCeremony() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tm.pane += "Login successful.\nLOGIN_EXIT=0\n"
}

// openLogin runs LoginURL, as agentd ctl login url does in its own process,
// until it holds the login lock at the gate. The returned function lets it go
// on, creating its window, and returns once it has released the lock.
func openLogin(t *testing.T, f *loginRaceTmux, d *Daemon) (finish func()) {
	t.Helper()
	done, exited := make(chan error, 1), make(chan struct{})
	r := f.openerRunner()
	go func() {
		defer close(exited)
		_, err := LoginURL(context.Background(), r, d.S, d.Session, d.now)
		done <- err
	}()
	var once sync.Once
	letGo := func() { once.Do(func() { close(f.gate) }) }
	// A test that fails before finish still lets the opener go and waits for
	// it, so no goroutine is left holding the lock.
	t.Cleanup(func() {
		letGo()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
		}
	})
	select {
	case <-f.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the ceremony opener never reached the login window")
	}
	return func() {
		t.Helper()
		letGo()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("the ceremony opener: ", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the ceremony opener did not return")
		}
		if !f.window() {
			t.Fatal("the ceremony opener created no window, so this test proves nothing")
		}
	}
}

// loginRaceDaemon is a remote Claude session's daemon before its first launch,
// with a start deadline a minute after the fake clock's start.
func loginRaceDaemon(t *testing.T, r Runner) (*Daemon, *testClock, time.Time) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin = "tmux", "claude"
	s.Getenv = envOf(nil)
	deadline := deadlineNow.Add(time.Minute)
	s.StartDeadline = deadline.Format(time.RFC3339)
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady, Steps: []Step{{Name: "agent", State: StepOK}}}); err != nil {
		t.Fatal(err)
	}
	clock := &testClock{now: deadlineNow}
	d := &Daemon{S: s, R: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Session: remoteSession(), Self: "/agentd", Now: clock.Now}
	return d, clock, deadline
}

// holdFirstLaunch boots the daemon's agent, which the boot must hold.
func holdFirstLaunch(t *testing.T, d *Daemon) {
	t.Helper()
	ws := protocol.Workspace{Clone: d.S.ClonePath(d.Session.Repo), Worktree: d.S.WorktreePath(d.Session.Name), Branch: "agent/" + d.Session.Name}
	step, agentErr := d.startAgent(context.Background(), ws, Step{Name: "repo", State: StepOK}, "b1")
	if agentErr != "" || step.State == StepFail || d.remote == nil || (!d.remote.waiting && !d.remote.deferred) {
		t.Fatalf("the boot did not hold the agent: step %+v err %q remote %+v", step, agentErr, d.remote)
	}
}

// pollWithin runs one login poll, as the supervise loop does, and fails the
// test if the poll does not return: it must never wait for the login lock.
func pollWithin(t *testing.T, d *Daemon) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); d.pollRemoteLogin(context.Background()) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a login poll waited for the login lock")
	}
}

// assertLaunchRefused checks that the held launch was refused and that no
// agent started.
func assertLaunchRefused(t *testing.T, d *Daemon, daemonTmux *fakeRunner) {
	t.Helper()
	var rec bootRecord
	if err := readJSONFile(d.S.statePath(bootFile), &rec); err != nil || rec.Boot != protocol.BootFailed || rec.AgentError != protocol.StartDeadlinePassed {
		t.Fatalf("boot record %+v %v, want boot failed with %q", rec, err, protocol.StartDeadlinePassed)
	}
	if d.remote != nil || exists(d.S.statePath(launchFile)) || exists(d.S.statePath(pidFile)) {
		t.Fatal("the refused launch can still start, or started, the agent")
	}
	for _, l := range daemonTmux.lines() {
		if strings.HasPrefix(l, "tmux new-session") {
			t.Fatal("the daemon started a tmux session after the refusal: ", l)
		}
	}
}

func countLines(r *fakeRunner, prefix string) int {
	n := 0
	for _, l := range r.lines() {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// The regression: a ceremony opener holds the login lock when the held
// launch's deadline passes. The refusal does not wait for the lock, and the
// window the opener creates afterwards does not outlive the refusal. The
// ceremony finishing after the refusal starts no agent.
func TestRefusedHeldLaunchStopsTheWindowOfAnOpenerHoldingTheLock(t *testing.T) {
	for name, loggedIn := range map[string]bool{"login-required": false, "ceremony-opening": true} {
		t.Run(name, func(t *testing.T) {
			f := newLoginRaceTmux()
			daemonTmux := f.runner()
			d, clock, deadline := loginRaceDaemon(t, daemonTmux)
			if loggedIn {
				writeCredentials(t, d.S, 0)
			}
			finishOpener := openLogin(t, f, d)
			holdFirstLaunch(t, d)
			if d.remote.deferred != loggedIn || d.remote.waiting == loggedIn {
				t.Fatalf("held as %+v, want deferred %v", d.remote, loggedIn)
			}

			clock.Set(deadline.Add(time.Second))
			pollWithin(t, d)
			assertLaunchRefused(t, d, daemonTmux)
			stopsWhileHeld := countLines(daemonTmux, "tmux kill-session")

			// The opener goes on under its lock, creates its window and
			// releases the lock; then the ceremony finishes in that window.
			finishOpener()
			if !loggedIn {
				writeCredentials(t, d.S, 0)
			}
			f.finishCeremony()
			pollWithin(t, d)
			pollWithin(t, d)

			if f.window() {
				started, stopped := f.counts()
				t.Fatalf("a login window outlived the refused launch (windows started %d, stop calls %d)", started, stopped)
			}
			assertLaunchRefused(t, d, daemonTmux)
			// The one stop came after the opener released the lock.
			if n := countLines(daemonTmux, "tmux kill-session -t ="+LoginTmuxSession); stopsWhileHeld != 0 || n != 1 {
				t.Fatalf("daemon stops: %d while the opener held the lock, %d in all; want 0 and 1", stopsWhileHeld, n)
			}

			// A later request for a login (the user retries login url) opens
			// no window: the session will never launch.
			if _, err := LoginURL(context.Background(), f.runner(), d.S, d.Session, d.now); err == nil || !strings.Contains(err.Error(), protocol.StartDeadlinePassed) || f.window() {
				t.Fatalf("login url after the refusal: %v, window %v", err, f.window())
			}
		})
	}
}

// The control: with no opener holding the lock, the refusal stops the login
// window in the same poll, as before, and later polls stop nothing again.
func TestRefusedHeldLaunchStopsAnIdleLoginWindowAtOnce(t *testing.T) {
	f := newLoginRaceTmux()
	daemonTmux := f.runner()
	d, clock, deadline := loginRaceDaemon(t, daemonTmux)
	holdFirstLaunch(t, d)
	// A ceremony was opened earlier and its opener has returned: the window
	// waits for a code, and nobody holds the lock.
	openLogin(t, f, d)()

	clock.Set(deadline.Add(time.Second))
	pollWithin(t, d)
	if f.window() || countLines(daemonTmux, "tmux kill-session -t ="+LoginTmuxSession) != 1 {
		t.Fatalf("the refusal did not stop the idle login window at once (window %v, daemon stops %d)", f.window(), countLines(daemonTmux, "tmux kill-session"))
	}
	assertLaunchRefused(t, d, daemonTmux)
	pollWithin(t, d)
	pollWithin(t, d)
	if n := countLines(daemonTmux, "tmux kill-session"); n != 1 {
		t.Fatalf("later polls stopped the login window again: %d stops", n)
	}
}

// Projection mode keeps its behaviour: the keeper owns the login, no login
// window is the daemon's to stop, and a refusal stops none, whether or not
// the login lock is held.
func TestRefusedHeldLaunchInProjectionModeStopsNoLoginWindow(t *testing.T) {
	for name, locked := range map[string]bool{"lock-free": false, "lock-held": true} {
		t.Run(name, func(t *testing.T) {
			daemonTmux := newLoginRaceTmux().runner()
			d, clock, deadline := loginRaceDaemon(t, daemonTmux)
			d.S.ClaudeLiveFile = filepath.Join(t.TempDir(), "absent", "live.json")
			if !d.projectsClaude() {
				t.Fatal("not a projection-mode session")
			}
			release := func() {}
			if locked {
				rel, err := acquireLoginLock(d.S)
				if err != nil {
					t.Fatal(err)
				}
				release = rel
			}
			holdFirstLaunch(t, d)
			clock.Set(deadline.Add(time.Second))
			pollWithin(t, d)
			release()
			pollWithin(t, d)
			pollWithin(t, d)
			assertLaunchRefused(t, d, daemonTmux)
			if n := countLines(daemonTmux, "tmux kill-session"); n != 0 {
				t.Fatalf("a projection-mode refusal stopped a login window: %d stops", n)
			}
		})
	}
}

// agentd ctl login url opens no login window for a session whose first launch
// is refused: past or invalid start deadline, no launch record. A session
// that launched, one still before its deadline and a Codex remote session
// (exempt, as at boot) are not affected.
func TestLoginURLRefusesASessionWhoseFirstLaunchIsRefused(t *testing.T) {
	passed := deadlineNow.Add(-time.Second).Format(time.RFC3339)
	for _, c := range []struct {
		name, deadline string
		launched       bool
		codex          bool
		refused        string
	}{
		{name: "no-deadline"},
		{name: "before-deadline", deadline: deadlineNow.Add(time.Minute).Format(time.RFC3339)},
		{name: "deadline-passed", deadline: passed, refused: protocol.StartDeadlinePassed},
		{name: "deadline-at-now", deadline: deadlineNow.Format(time.RFC3339), refused: protocol.StartDeadlinePassed},
		{name: "deadline-invalid", deadline: "tomorrow", refused: protocol.StartDeadlineInvalid},
		{name: "launched", deadline: passed, launched: true},
		{name: "codex-remote", deadline: passed, codex: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newLoginRaceTmux()
			opener := f.runner()
			d, _, _ := loginRaceDaemon(t, opener)
			d.S.StartDeadline = c.deadline
			if c.launched {
				writeFile(t, d.S.statePath(launchFile), "{}")
			}
			if c.codex {
				d.Session.Agent, d.Session.Model = protocol.AgentCodex, "gpt-6-luna"
			}
			u, err := LoginURL(context.Background(), opener, d.S, d.Session, d.now)
			if c.refused != "" {
				if err == nil || !strings.Contains(err.Error(), c.refused) || u != "" {
					t.Fatalf("login url: link returned %v, error %v, want a refusal naming %q", u != "", err, c.refused)
				}
				if f.window() || countLines(opener, "tmux") != 0 {
					t.Fatalf("a refused login ran tmux: %q", opener.lines())
				}
				return
			}
			if err != nil || u == "" || !f.window() {
				t.Fatalf("login url: link returned %v, error %v, window %v", u != "", err, f.window())
			}
		})
	}
}
