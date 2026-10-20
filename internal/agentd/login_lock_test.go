package agentd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// NB-11 (#443 review nit D1): two entry points must never both start a Claude
// login ceremony (one token family per pod). The race is a check-then-act gap
// between processes, so each test runs the competing call at the exact point
// the first one is between its check and its act, with no sleeps or threads.

// TestConcurrentLoginURLStartsOneCeremony: a used window (its CLI ended) is
// replaced by the next `login url`. A second request that arrives while the
// first is mid-replacement must not replace it again: that would kill the
// first request's fresh window and start a second `claude auth login`.
func TestConcurrentLoginURLStartsOneCeremony(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin = "tmux", "claude"
	tm := &loginTmux{window: true, pane: loginPaneWithURL + "Login successful.\nLOGIN_EXIT=0\n"}
	var second error
	var secondRan bool
	r := &fakeRunner{}
	r.handle = func(c Cmd) (Result, error) {
		// The first request has read the ended window and is about to stop it.
		if c.Args[0] == "kill-session" && !secondRan {
			secondRan = true
			_, second = LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow })
		}
		return tm.handle(c)
	}
	u, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow })
	if err != nil || u == "" {
		t.Fatalf("first request: %q %v", u, err)
	}
	if !secondRan || !errors.Is(second, ErrLoginBusy) {
		t.Errorf("second request: ran %v err %v, want ErrLoginBusy", secondRan, second)
	}
	if tm.started != 1 {
		t.Errorf("login ceremonies started: %d, want exactly 1 (a second one is a second token family)", tm.started)
	}
	// The lock is released: a later request reuses the waiting window.
	if _, err := LoginURL(context.Background(), r, s, remoteSession(), func() time.Time { return loginNow }); err != nil || tm.started != 1 {
		t.Errorf("after the first request: %v started %d", err, tm.started)
	}
}

// TestLoginURLRefusedWhileDaemonStartsAgent: the daemon starts the agent when a
// ceremony ends. A `login url` that arrives while it does so must not open a
// login beside the starting TUI, even though the agent's pid file is not
// written yet.
func TestLoginURLRefusedWhileDaemonStartsAgent(t *testing.T) {
	s := testSettings(t, t.TempDir())
	s.TmuxBin, s.ClaudeBin = "tmux", "claude"
	s.Getenv = envOf(nil)
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCredentials(t, s, loginNow.Add(30*24*time.Hour).UnixMilli())
	sess := remoteSession()
	tm := &loginTmux{window: true, pane: loginPaneWithURL + "Login successful.\nLOGIN_EXIT=0\n"}
	var during error
	var duringRan bool
	r := &fakeRunner{}
	r.handle = func(c Cmd) (Result, error) {
		if c.Args[0] == "new-session" && len(c.Args) > 3 && c.Args[3] == TmuxSession {
			// The agent's session exists; run-agent has not written its pid yet.
			tm.agent = true
			duringRan = true
			_, during = LoginURL(context.Background(), r, s, sess, func() time.Time { return loginNow })
			return Result{}, nil
		}
		return tm.handle(c)
	}
	d := &Daemon{S: s, R: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Session: sess, Self: "/agentd", Now: func() time.Time { return loginNow }}
	ws := protocol.Workspace{Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name), Branch: "agent/" + sess.Name}
	_ = writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1", Boot: protocol.BootReady, Steps: []Step{{Name: "agent", State: StepOK}}})
	d.remote = &remoteWait{ws: ws, repo: Step{Name: "repo", State: StepOK}, boot: "b1", waiting: true}

	d.pollRemoteLogin(context.Background())
	if !duringRan || !tm.agent {
		t.Fatalf("the daemon did not start the agent (ran %v up %v)", duringRan, tm.agent)
	}
	if !errors.Is(during, ErrLoginBusy) {
		t.Errorf("login url while the daemon starts the agent: %v, want ErrLoginBusy", during)
	}
	if tm.started != 0 {
		t.Errorf("a login ceremony started beside the starting agent (%d)", tm.started)
	}

	// The lock is gone once the daemon is done; the live agent session is the
	// reason for the next refusal, not the lock.
	_, err := LoginURL(context.Background(), r, s, sess, func() time.Time { return loginNow })
	if err == nil || errors.Is(err, ErrLoginBusy) || !strings.Contains(err.Error(), "agent") || tm.started != 0 {
		t.Errorf("after the daemon: %v started %d", err, tm.started)
	}

	// And the other way round: the daemon never starts the agent while a login
	// request holds the lock.
	tm.agent = false
	tm.window, tm.pane, tm.started = true, loginPaneWithURL+"Login successful.\nLOGIN_EXIT=0\n", 0
	release, lerr := acquireLoginLock(s)
	if lerr != nil {
		t.Fatal(lerr)
	}
	d.remote.waiting = true
	d.pollRemoteLogin(context.Background())
	if tm.agent {
		t.Error("the daemon started the agent while a login request held the lock")
	}
	release()
	d.pollRemoteLogin(context.Background())
	if !tm.agent {
		t.Error("the daemon did not start the agent once the lock was free")
	}
}
