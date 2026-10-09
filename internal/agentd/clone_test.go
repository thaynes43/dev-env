package agentd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func cloneSession(name string) protocol.Session {
	return protocol.Session{Name: name, Repo: "demo", Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: "claude-opus-5-5", Prompt: "p"}
}

// failOriginFetch keeps local git operations real while origin is unavailable.
func failOriginFetch(r ExecRunner) *fakeRunner {
	return &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "git" && len(c.Args) > 2 && slices.Equal(c.Args[2:], []string{"fetch", "--prune", "origin"}) {
			return Result{}, &CmdError{Name: "git", Sub: "fetch", ExitCode: 128, Stderr: "fatal: origin unavailable"}
		}
		return r.Run(context.Background(), c)
	}}
}

func TestPrepareRepoRefusesNewBranchAfterFailedFetch(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1009-100000")
	// First boot stopped after cloning, before either the branch or worktree
	// existed. The next boot must not use that clone's stale origin/main.
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	f := failOriginFetch(r)
	for _, base := range []string{"", "origin/main", "refs/remotes/origin/main"} {
		sess.Base = base
		ws, step := PrepareRepo(context.Background(), f, s, sess)
		if step.State != StepFail || !strings.Contains(strings.Join(step.Notes, "\n"), "refusing a new session branch from") {
			t.Fatalf("base %q: %s %q", base, step.State, step.Notes)
		}
		if exists(ws.Worktree) {
			t.Error("failed fetch created a worktree")
		}
		if refs := gitRun(t, g.env, ws.Clone, "for-each-ref", "--format=%(refname)", "refs/heads/"+ws.Branch); refs != "" {
			t.Errorf("failed fetch created a branch: %s", refs)
		}
	}
	for _, c := range f.calls {
		if len(c.Args) > 3 && c.Args[2] == "worktree" {
			t.Errorf("failed fresh boot changed worktree metadata: %v", c.Args)
		}
	}
}

func TestPrepareRepoFreshReusedCloneFetchesAndPinsBase(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1009-100001")
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	old := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(g.seed, "new.txt"), "new\n")
	gitRun(t, g.env, g.seed, "add", "new.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "new origin tip")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	tip := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	added := false
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "git" && len(c.Args) > 4 && c.Args[2] == "worktree" && c.Args[3] == "add" {
			added = true
			if base := c.Args[len(c.Args)-1]; base != tip {
				t.Errorf("worktree add base = %q, want fetched commit %s", base, tip)
			}
			// A moving ref cannot change the commit selected for this task.
			gitRun(t, g.env, s.ClonePath(sess.Repo), "update-ref", "refs/remotes/origin/main", old)
		}
		return r.Run(context.Background(), c)
	}}
	ws, step := PrepareRepo(context.Background(), f, s, sess)
	if step.State != StepOK || ws.Head != tip || !added {
		t.Fatalf("head %s, want %s; added %v, %s %q", ws.Head, tip, added, step.State, step.Notes)
	}
}

func TestPrepareRepoOfflineWorktreePreservesWIP(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1009-100002")
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK {
		t.Fatal(step.Notes)
	}
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "staged\n")
	gitRun(t, g.env, ws.Worktree, "add", "README.md")
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "unstaged\n")
	writeFile(t, filepath.Join(ws.Worktree, "untracked.txt"), "untracked\n")
	before := gitRun(t, g.env, ws.Worktree, "status", "--porcelain=v1")
	index := gitRun(t, g.env, ws.Worktree, "show", ":README.md")
	// A resumed workspace never needs its original base or rescue again.
	sess.Base = "origin/no-longer-present"
	sess.Restore = "old-session/20261008-0024"
	ws2, step := PrepareRepo(context.Background(), failOriginFetch(r), s, sess)
	if step.State != StepWarn || ws2.Head != ws.Head || !strings.Contains(strings.Join(step.Notes, "\n"), "WARN fetch failed; preserving the existing worktree") {
		t.Fatalf("head %s, want %s; %s %q", ws2.Head, ws.Head, step.State, step.Notes)
	}
	if got := gitRun(t, g.env, ws.Worktree, "status", "--porcelain=v1"); got != before {
		t.Errorf("WIP changed: %q, want %q", got, before)
	}
	if got := gitRun(t, g.env, ws.Worktree, "show", ":README.md"); got != index {
		t.Errorf("index changed: %q, want %q", got, index)
	}
	for file, want := range map[string]string{"README.md": "unstaged\n", "untracked.txt": "untracked\n"} {
		if data, err := os.ReadFile(filepath.Join(ws.Worktree, file)); err != nil || string(data) != want {
			t.Errorf("%s = %q, %v; want %q", file, data, err, want)
		}
	}
}

func TestPrepareRepoOfflineBranchRecoveryIgnoresOldBaseAndRescue(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	sess := cloneSession("demo-1009-100003")
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK {
		t.Fatal(step.Notes)
	}
	writeFile(t, filepath.Join(ws.Worktree, "work.txt"), "retained\n")
	gitRun(t, g.env, ws.Worktree, "add", "work.txt")
	gitRun(t, g.env, ws.Worktree, "commit", "-q", "-m", "retained work")
	tip := gitRun(t, g.env, ws.Worktree, "rev-parse", "HEAD")
	if err := os.RemoveAll(ws.Worktree); err != nil {
		t.Fatal(err)
	}
	sess.Base = "origin/no-longer-present"
	sess.Restore = "old-session/20261008-0024"
	ws, step = PrepareRepo(context.Background(), failOriginFetch(r), s, sess)
	if step.State != StepWarn || ws.Head != tip || !strings.Contains(strings.Join(step.Notes, "\n"), "WARN fetch failed; recovering the existing session branch") {
		t.Fatalf("head %s, want %s; %s %q", ws.Head, tip, step.State, step.Notes)
	}
	if got := gitRun(t, g.env, ws.Worktree, "show", "HEAD:work.txt"); got != "retained" {
		t.Errorf("recovered work = %q", got)
	}
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

func TestPrepareRepoRejectsMismatchedWorktrees(t *testing.T) {
	for _, mismatch := range []string{"other clone", "other branch", "detached", "broken git file", "missing branch ref", "symlink"} {
		t.Run(mismatch, func(t *testing.T) {
			g := newGitFixture(t, "demo")
			s, r := g.settings(t)
			sess := cloneSession("demo-1009-100004")
			ws, step := PrepareRepo(context.Background(), r, s, sess)
			if step.State != StepOK {
				t.Fatal(step.Notes)
			}
			want := "not on the session branch"
			switch mismatch {
			case "other clone":
				if err := os.RemoveAll(ws.Worktree); err != nil {
					t.Fatal(err)
				}
				gitRun(t, g.env, g.root, "clone", "-q", g.remote, ws.Worktree)
				gitRun(t, g.env, ws.Worktree, "checkout", "-q", "-b", ws.Branch)
				want = "does not belong to the session's clone"
			case "other branch":
				gitRun(t, g.env, ws.Worktree, "checkout", "-q", "-b", "other")
			case "detached":
				gitRun(t, g.env, ws.Worktree, "checkout", "-q", "--detach")
			case "broken git file":
				writeFile(t, filepath.Join(ws.Worktree, ".git"), "gitdir: /missing-fixture-git\n")
				want = "not a git worktree"
			case "missing branch ref":
				gitRun(t, g.env, ws.Clone, "update-ref", "-d", "refs/heads/"+ws.Branch)
				want = "verify the workspace HEAD"
			case "symlink":
				if err := os.RemoveAll(ws.Worktree); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(ws.Clone, ws.Worktree); err != nil {
					t.Fatal(err)
				}
				want = "not a directory"
			}
			writeFile(t, filepath.Join(ws.Worktree, "keep.txt"), "untouched\n")
			_, step = PrepareRepo(context.Background(), failOriginFetch(r), s, sess)
			if step.State != StepFail || !strings.Contains(strings.Join(step.Notes, "\n"), want) {
				t.Fatalf("%s %q, want %s", step.State, step.Notes, want)
			}
			if data, err := os.ReadFile(filepath.Join(ws.Worktree, "keep.txt")); err != nil || string(data) != "untouched\n" {
				t.Errorf("worktree content changed: %q, %v", data, err)
			}
		})
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
	main := gitRun(t, g.env, g.remote, "rev-parse", "main")
	gitRun(t, g.env, g.seed, "tag", "-a", "fixture-tag", "-m", "fixture", feature)
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "refs/tags/fixture-tag")
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
	for i, tc := range []struct{ base, want string }{
		{"refs/tags/fixture-tag", feature},
		{main, main},
		{"origin/feature~1", main},
		{"main", main},
		{"origin/not-a-ref", ""},
		{gitRun(t, g.env, ws.Clone, "rev-parse", "main:README.md"), ""},
	} {
		t.Run(tc.base, func(t *testing.T) {
			sess := cloneSession(fmt.Sprintf("demo-1009-base-%d", i))
			sess.Base = tc.base
			ws, step := PrepareRepo(context.Background(), r, s, sess)
			if tc.want == "" {
				if step.State != StepFail || exists(ws.Worktree) || !strings.Contains(strings.Join(step.Notes, "\n"), "resolve base") {
					t.Fatalf("non-commit base: head %s, %s %q", ws.Head, step.State, step.Notes)
				}
			} else if step.State != StepOK || ws.Head != tc.want {
				t.Fatalf("head %s, want %s; %s %q", ws.Head, tc.want, step.State, step.Notes)
			}
		})
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
