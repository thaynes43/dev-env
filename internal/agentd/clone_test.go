package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func cloneSession(name string) protocol.Session {
	return protocol.Session{Name: name, Repo: "demo", Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: "claude-opus-5-5", Prompt: "p"}
}

func TestPrepareRepoFreshThenReuse(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1006-120000")
	ctx := context.Background()

	ws, step := PrepareRepo(ctx, r, s, sess)
	if step.State != StepOK {
		t.Fatalf("first boot: %s %q", step.State, step.Notes)
	}
	if !strings.Contains(strings.Join(step.Notes, "\n"), "no GitHub token") {
		t.Errorf("notes lack the missing-token note: %q", step.Notes)
	}
	if ws.Clone != filepath.Join(s.Home, "repos", "demo") || ws.Worktree != filepath.Join(s.Home, "work", sess.Name) || ws.Branch != "agent/"+sess.Name {
		t.Errorf("workspace = %+v", ws)
	}
	main := gitRun(t, g.env, g.remote, "rev-parse", "main")
	if ws.Head != main {
		t.Errorf("head %s, want origin main %s", ws.Head, main)
	}
	if got := gitRun(t, g.env, ws.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); got != ws.Branch {
		t.Errorf("worktree branch = %s", got)
	}
	if got := gitRun(t, g.env, ws.Clone, "config", "remote.origin.partialclonefilter"); got != "blob:none" {
		t.Errorf("partial clone filter = %q", got)
	}
	if _, err := os.Stat(filepath.Join(s.Home, "repos", ".demo.agentd-clone")); !isNotExist(err) {
		t.Errorf("temporary clone dir left: %v", err)
	}

	// A resume on the same volume: the origin moved on; agentd fetches and
	// reuses the worktree as it is.
	writeFile(t, filepath.Join(g.seed, "b.txt"), "b\n")
	gitRun(t, g.env, g.seed, "add", "b.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "second")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	ws2, step := PrepareRepo(ctx, r, s, sess)
	if step.State != StepOK || !strings.Contains(strings.Join(step.Notes, "\n"), "reused the worktree") {
		t.Fatalf("second boot: %s %q", step.State, step.Notes)
	}
	if ws2.Head != main {
		t.Errorf("the reused worktree moved: %s", ws2.Head)
	}
	if got := gitRun(t, g.env, ws.Clone, "rev-parse", "origin/main"); got == main {
		t.Error("the clone was not fetched")
	}
}

func TestPrepareRepoRecreatesWorktreeOnItsBranch(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1006-130000")
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK {
		t.Fatal(step.Notes)
	}
	writeFile(t, filepath.Join(ws.Worktree, "wip.txt"), "wip\n")
	gitRun(t, g.env, ws.Worktree, "add", "wip.txt")
	gitRun(t, g.env, ws.Worktree, "commit", "-q", "-m", "wip")
	tip := gitRun(t, g.env, ws.Worktree, "rev-parse", "HEAD")
	if err := os.RemoveAll(ws.Worktree); err != nil {
		t.Fatal(err)
	}

	ws2, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || !strings.Contains(strings.Join(step.Notes, "\n"), "existing branch") {
		t.Fatalf("%s %q", step.State, step.Notes)
	}
	if ws2.Head != tip {
		t.Errorf("head %s, want the branch tip %s", ws2.Head, tip)
	}
}

func TestPrepareRepoLeavesAStrangerDirectory(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1006-140000")
	writeFile(t, filepath.Join(s.WorktreePath(sess.Name), "keep.txt"), "mine")
	_, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepFail || !strings.Contains(strings.Join(step.Notes, "\n"), "not a git worktree") {
		t.Fatalf("%s %q", step.State, step.Notes)
	}
	if data, _ := os.ReadFile(filepath.Join(s.WorktreePath(sess.Name), "keep.txt")); string(data) != "mine" {
		t.Error("the directory was touched")
	}
}

func TestPrepareRepoBaseAndDefaultBranch(t *testing.T) {
	g := newGitFixture(t, "demo")
	gitRun(t, g.env, g.seed, "checkout", "-q", "-b", "feature")
	writeFile(t, filepath.Join(g.seed, "f.txt"), "f\n")
	gitRun(t, g.env, g.seed, "add", "f.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "feature")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "feature")
	feature := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	s, r := g.settings(t)

	sess := cloneSession("demo-1006-150000")
	sess.Base = "origin/feature"
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || ws.Head != feature {
		t.Fatalf("base origin/feature: head %s, %s %q", ws.Head, step.State, step.Notes)
	}
	if b, _ := resolveBase(context.Background(), r, s, ws.Clone, ""); b != "origin/main" {
		t.Errorf("default base = %q", b)
	}
}

func TestCloneRetriesAndCleansUp(t *testing.T) {
	noSleep(t)
	home := t.TempDir()
	s := testSettings(t, home)
	s.TokenWait = 0
	attempts := 0
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "git" && c.Args[0] == "clone" {
			attempts++
			// A clone that dies half way leaves its directory behind.
			writeFile(t, filepath.Join(c.Args[len(c.Args)-1], "partial"), "x")
			return Result{}, &CmdError{Name: "git", Sub: "clone", ExitCode: 128, Stderr: "fatal: unable to access"}
		}
		return Result{}, nil
	}}
	_, step := PrepareRepo(context.Background(), f, s, cloneSession("x-1"))
	if step.State != StepFail || attempts != cloneAttempts {
		t.Fatalf("attempts %d, step %s %q", attempts, step.State, step.Notes)
	}
	if !strings.Contains(strings.Join(step.Notes, "\n"), "fatal: unable to access") {
		t.Errorf("notes lack git's reason: %q", step.Notes)
	}
	left, _ := filepath.Glob(filepath.Join(s.ReposDir(), ".*"))
	if len(left) != 0 || exists(s.ClonePath("x")) {
		t.Errorf("left behind: %q", left)
	}
}

func TestWaitForFile(t *testing.T) {
	noSleep(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "tok")
	if waitForFile(context.Background(), p, 0) {
		t.Error("missing file reported present")
	}
	writeFile(t, p, "")
	if waitForFile(context.Background(), p, 0) {
		t.Error("empty file reported present")
	}
	writeFile(t, p, "t")
	if !waitForFile(context.Background(), p, 0) {
		t.Error("present file not seen")
	}
}
