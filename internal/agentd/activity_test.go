package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	writeFile(t, path, "x")
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func writeClaudeSession(t *testing.T, s Settings, pid int, status string, at time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(pid)+".json"),
		`{"pid":`+strconv.Itoa(pid)+`,"status":"`+status+`","updatedAt":`+strconv.FormatInt(at.UnixMilli(), 10)+`,"kind":"interactive"}`)
}

// The worktree's activity is v1's wt_busy signals: its git files and every
// file outside .git, node_modules and .claude (D-59).
func TestScanWorktree(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	wt := t.TempDir()
	gd := t.TempDir()
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+gd+"\n")
	touch(t, filepath.Join(wt, "README.md"), base)
	touch(t, filepath.Join(wt, "src", "a.go"), base.Add(time.Hour))
	touch(t, filepath.Join(wt, "node_modules", "x.js"), base.Add(9*time.Hour))
	touch(t, filepath.Join(wt, ".claude", "settings.local.json"), base.Add(9*time.Hour))
	touch(t, filepath.Join(gd, "index"), base.Add(9*time.Hour))
	if got, err := scanWorktree(context.Background(), wt); err != nil || !got.Equal(base.Add(time.Hour)) {
		t.Errorf("newest %s %v, want the source file's", got, err)
	}
	touch(t, filepath.Join(gd, "FETCH_HEAD"), base.Add(2*time.Hour))
	if got, _ := scanWorktree(context.Background(), wt); !got.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("newest %s, want FETCH_HEAD's", got)
	}
	if got, err := scanWorktree(context.Background(), filepath.Join(wt, "gone")); err != nil || !got.IsZero() {
		t.Errorf("a missing worktree: %s %v", got, err)
	}

	// Over the cap, the walk fails, and the activity is now: never idle by
	// mistake.
	old := worktreeScanCap
	worktreeScanCap = 1
	t.Cleanup(func() { worktreeScanCap = old })
	now := time.Now().UTC()
	if got := worktreeActivity(context.Background(), wt, now); !got.Equal(now) {
		t.Errorf("over the cap: %s, want now", got)
	}
	worktreeScanCap = old

	// A walk that is cancelled, or runs out of time, answers now too.
	many := t.TempDir()
	for i := range 2500 {
		touch(t, filepath.Join(many, strconv.Itoa(i)), base)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanWorktree(ctx, many); err == nil {
		t.Error("a cancelled walk finished")
	}
	if got := worktreeActivity(ctx, many, now); !got.Equal(now) {
		t.Errorf("a cancelled walk: %s, want now", got)
	}
}

// A walk's answer stands for worktreeScanEvery.
func TestWorktreeActivityIsCached(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	wt := t.TempDir()
	touch(t, filepath.Join(wt, "a"), base)
	now := time.Now().UTC()
	if got := worktreeActivity(context.Background(), wt, now); !got.Equal(base) {
		t.Fatalf("first walk %s", got)
	}
	touch(t, filepath.Join(wt, "b"), base.Add(time.Hour))
	if got := worktreeActivity(context.Background(), wt, now.Add(time.Minute)); !got.Equal(base) {
		t.Errorf("within the interval: %s, want the cached answer", got)
	}
	if got := worktreeActivity(context.Background(), wt, now.Add(worktreeScanEvery)); !got.Equal(base.Add(time.Hour)) {
		t.Errorf("after the interval: %s, want a new walk", got)
	}
}

// A TUI's state comes from Claude's own status; an attached client and a busy
// CLI are activity now; a task is busy until it ends (D-59).
func TestApplyActivity(t *testing.T) {
	now := time.Date(2026, 10, 7, 22, 0, 0, 0, time.UTC)
	idleSince := now.Add(-2 * time.Hour)
	s := testSettings(t, t.TempDir())
	clients := ""
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "tmux" && c.Args[0] == "list-clients" {
			return Result{Stdout: []byte(clients)}, nil
		}
		return Result{}, nil
	}}
	run := func(state string, tui bool) protocol.Status {
		st := protocol.Status{Agent: protocol.AgentState{State: state}}
		applyActivity(context.Background(), f, s, &st, 4242, tui, now)
		return st
	}

	writeClaudeSession(t, s, 4242, "idle", idleSince)
	if st := run(protocol.AgentBusy, true); st.Agent.State != protocol.AgentIdle || !st.Agent.LastActivity.Equal(idleSince) || st.Agent.Attached != 0 {
		t.Errorf("idle TUI: %+v", st.Agent)
	}
	writeClaudeSession(t, s, 4242, "waiting", idleSince)
	if st := run(protocol.AgentBusy, true); st.Agent.State != protocol.AgentWaiting || !st.Agent.LastActivity.Equal(idleSince) {
		t.Errorf("waiting TUI: %+v", st.Agent)
	}
	writeClaudeSession(t, s, 4242, "busy", idleSince)
	if st := run(protocol.AgentBusy, true); st.Agent.State != protocol.AgentBusy || !st.Agent.LastActivity.Equal(now) {
		t.Errorf("busy TUI: %+v", st.Agent)
	}
	// A task's record is not read: it is busy until it ends.
	writeClaudeSession(t, s, 4242, "idle", idleSince)
	if st := run(protocol.AgentBusy, false); st.Agent.State != protocol.AgentBusy || !st.Agent.LastActivity.Equal(now) {
		t.Errorf("task: %+v", st.Agent)
	}
	// An attached client is activity now, whatever Claude says.
	clients = "/dev/pts/3\n/dev/pts/4\n"
	if st := run(protocol.AgentBusy, true); st.Agent.State != protocol.AgentIdle || st.Agent.Attached != 2 || !st.Agent.LastActivity.Equal(now) {
		t.Errorf("attached: %+v", st.Agent)
	}
	clients = ""
	// An exited agent keeps its last activity; the worktree adds its own.
	wt := t.TempDir()
	touch(t, filepath.Join(wt, "f"), now.Add(-30*time.Minute))
	last := now.Add(-3 * time.Hour)
	st := protocol.Status{Agent: protocol.AgentState{State: protocol.AgentExited, LastActivity: &last}, Workspace: &protocol.Workspace{Worktree: wt}}
	applyActivity(context.Background(), f, s, &st, 0, false, now)
	if st.Agent.State != protocol.AgentExited || !st.Agent.LastActivity.Equal(now.Add(-30*time.Minute)) {
		t.Errorf("exited with a newer file: %+v", st.Agent)
	}
}
