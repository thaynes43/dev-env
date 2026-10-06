package agentd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// rescueRig is a session volume with a clone and its worktree, as boot step 2
// leaves them.
type rescueRig struct {
	g  gitFixture
	s  Settings
	r  ExecRunner
	ws protocol.Workspace
}

func newRescueRig(t *testing.T) rescueRig {
	t.Helper()
	g := newGitFixture(t, "demo")
	writeFile(t, filepath.Join(g.seed, ".gitignore"), "node_modules/\n*.log\n")
	gitRun(t, g.env, g.seed, "add", ".gitignore")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "ignore")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	s, r := g.settings(t)
	ws, step := PrepareRepo(context.Background(), r, s, cloneSession("demo-1006-170000"))
	if step.State != StepOK {
		t.Fatalf("prepare: %q", step.Notes)
	}
	return rescueRig{g: g, s: s, r: r, ws: ws}
}

var rescueNow = time.Date(2026, 10, 6, 17, 30, 0, 0, time.UTC)

func (rig rescueRig) rescue(t *testing.T) protocol.RescueReport {
	t.Helper()
	rep, err := Rescue(context.Background(), rig.r, rig.s, "demo-1006-170000", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func (rig rescueRig) worktree(t *testing.T, rep protocol.RescueReport, path string) protocol.WorktreeRescue {
	t.Helper()
	for _, repo := range rep.Repos {
		for _, w := range repo.Worktrees {
			if w.Path == path {
				return w
			}
		}
	}
	t.Fatalf("no worktree %s in %+v", path, rep)
	return protocol.WorktreeRescue{}
}

func refNames(refs []protocol.Ref) string {
	var out []string
	for _, r := range refs {
		out = append(out, r.Name)
	}
	return strings.Join(out, " ")
}

func TestRescueCleanAndPushed(t *testing.T) {
	rig := newRescueRig(t)
	// Ignored files never count.
	writeFile(t, filepath.Join(rig.ws.Worktree, "node_modules", "x.js"), "x")
	rep := rig.rescue(t)
	if !rep.OK || !rep.CleanAndPushed || rep.Stamp != "20261006-1730" || rep.Session != "demo-1006-170000" {
		t.Fatalf("report %+v", rep)
	}
	if len(rep.Repos) != 1 || len(rep.Repos[0].Worktrees) != 2 || len(rep.Repos[0].UnpushedRefs) != 0 || !rep.Repos[0].Fetched {
		t.Errorf("repo %+v", rep.Repos[0])
	}
	if w := rig.worktree(t, rep, rig.ws.Worktree); w.Dirty || w.RescueBranch != "" || w.Branch != rig.ws.Branch {
		t.Errorf("worktree %+v", w)
	}
}

func TestRescueDirtyWorktreeIsCommittedAndLeftAlone(t *testing.T) {
	rig := newRescueRig(t)
	wt := rig.ws.Worktree
	env := rig.g.env
	writeFile(t, filepath.Join(wt, "README.md"), "edited\n")  // tracked, unstaged
	writeFile(t, filepath.Join(wt, "staged.txt"), "staged\n") // new, staged
	gitRun(t, env, wt, "add", "staged.txt")
	writeFile(t, filepath.Join(wt, "scratch", "new.sh"), "echo\n") // untracked
	writeFile(t, filepath.Join(wt, "build.log"), "ignored\n")      // ignored
	before := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all")
	head := gitRun(t, env, wt, "rev-parse", "HEAD")

	rep := rig.rescue(t)
	w := rig.worktree(t, rep, wt)
	if !rep.OK || rep.CleanAndPushed || !w.Dirty || w.RescueBranch != "rescue/demo-1006-170000-20261006-1730" {
		t.Fatalf("report %+v, worktree %+v", rep, w)
	}
	// The worktree, its index and its branch are as they were.
	if after := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Errorf("status changed:\n%s\nwas:\n%s", after, before)
	}
	if b := gitRun(t, env, wt, "rev-parse", "--abbrev-ref", "HEAD"); b != rig.ws.Branch {
		t.Errorf("branch changed to %s", b)
	}
	// The rescue commit holds the work, on top of HEAD, without the ignored file.
	ref := "refs/heads/" + w.RescueBranch
	if p := gitRun(t, env, wt, "rev-parse", ref+"^"); p != head {
		t.Errorf("rescue parent %s, want %s", p, head)
	}
	files := gitRun(t, env, wt, "ls-tree", "-r", "--name-only", ref)
	for _, want := range []string{"README.md", "staged.txt", "scratch/new.sh"} {
		if !strings.Contains(files, want) {
			t.Errorf("rescue commit lacks %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "build.log") {
		t.Error("an ignored file was rescued")
	}
	if got := gitRun(t, env, wt, "show", ref+":README.md"); got != "edited" {
		t.Errorf("README.md in the rescue = %q", got)
	}
	if !strings.Contains(gitRun(t, env, wt, "log", "-1", "--format=%s", ref), "wip: rescued by agentd from "+wt+" (was on "+rig.ws.Branch) {
		t.Error("rescue commit message")
	}
	if !strings.Contains(refNames(rep.Repos[0].UnpushedRefs), "refs/heads/"+w.RescueBranch) {
		t.Errorf("unpushed refs lack the rescue branch: %+v", rep.Repos[0].UnpushedRefs)
	}
	if left, _ := filepath.Glob(filepath.Join(rig.s.StateDir, "rescue-index-*")); len(left) != 0 {
		t.Errorf("temporary index left: %q", left)
	}

	// A second rescue in the same minute gets its own branch.
	rep2 := rig.rescue(t)
	if w2 := rig.worktree(t, rep2, wt); w2.RescueBranch != w.RescueBranch+"-2" {
		t.Errorf("second rescue branch %q", w2.RescueBranch)
	}
}

func TestRescueReportsUnpushedCommitsAndStash(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "done.txt"), "done\n")
	gitRun(t, env, wt, "add", "done.txt")
	gitRun(t, env, wt, "commit", "-q", "-m", "work")
	writeFile(t, filepath.Join(wt, "README.md"), "stashed\n")
	gitRun(t, env, wt, "stash", "push", "-q", "-m", "older")
	writeFile(t, filepath.Join(wt, "README.md"), "stashed again\n")
	gitRun(t, env, wt, "stash", "push", "-q", "-m", "newer")
	older := gitRun(t, env, wt, "rev-parse", "stash@{1}")

	rep := rig.rescue(t)
	if !rep.OK || rep.CleanAndPushed {
		t.Fatalf("report %+v", rep)
	}
	refs := rep.Repos[0].UnpushedRefs
	if got := refNames(refs); got != "refs/heads/"+rig.ws.Branch+" stash@{0} stash@{1}" {
		t.Errorf("unpushed = %q", got)
	}
	// The older entry has no ref of its own; the report still names it.
	if len(refs) == 3 && refs[2].Commit != older {
		t.Errorf("stash@{1} = %s, want %s", refs[2].Commit, older)
	}
	if w := rig.worktree(t, rep, wt); w.Dirty || w.RescueBranch != "" {
		t.Errorf("worktree %+v", w)
	}

	// Once pushed, the branch is no longer listed.
	gitRun(t, env, wt, "stash", "clear")
	gitRun(t, env, wt, "push", "-q", "origin", rig.ws.Branch)
	rep = rig.rescue(t)
	if !rep.CleanAndPushed || len(rep.Repos[0].UnpushedRefs) != 0 {
		t.Errorf("after push: %+v", rep.Repos[0])
	}
}

func TestRescueWithoutAnIndexFile(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "new.txt"), "new\n")
	gd := gitRun(t, env, wt, "rev-parse", "--absolute-git-dir")
	if err := os.Remove(filepath.Join(gd, "index")); err != nil {
		t.Fatal(err)
	}
	rep := rig.rescue(t)
	w := rig.worktree(t, rep, wt)
	if !rep.OK || w.Refused != "" || w.RescueBranch == "" {
		t.Fatalf("worktree %+v", w)
	}
	files := gitRun(t, env, wt, "ls-tree", "-r", "--name-only", "refs/heads/"+w.RescueBranch)
	if !strings.Contains(files, "new.txt") || !strings.Contains(files, "README.md") {
		t.Errorf("rescue tree:\n%s", files)
	}
	if exists(filepath.Join(gd, "index")) {
		t.Error("the rescue wrote the worktree's own index")
	}
}

func TestRescueAnchorsADetachedHead(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	gitRun(t, env, wt, "switch", "-q", "--detach")
	writeFile(t, filepath.Join(wt, "lost.txt"), "lost\n")
	gitRun(t, env, wt, "add", "lost.txt")
	gitRun(t, env, wt, "commit", "-q", "-m", "on no branch")
	head := gitRun(t, env, wt, "rev-parse", "HEAD")
	gitRun(t, env, wt, "branch", "-q", "-D", rig.ws.Branch)

	rep := rig.rescue(t)
	w := rig.worktree(t, rep, wt)
	if w.Branch != "" || w.RescueBranch == "" || w.Dirty {
		t.Fatalf("worktree %+v", w)
	}
	if got := gitRun(t, env, wt, "rev-parse", "refs/heads/"+w.RescueBranch); got != head {
		t.Errorf("anchor %s, want %s", got, head)
	}
	if rep.CleanAndPushed {
		t.Error("an anchored HEAD counted as clean and pushed")
	}
}

func TestRescueRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, rig rescueRig)
		want  string
	}{
		{"nested repo", func(t *testing.T, rig rescueRig) {
			gitRun(t, rig.g.env, rig.ws.Worktree, "init", "-q", "inner")
			writeFile(t, filepath.Join(rig.ws.Worktree, "inner", "f"), "f")
		}, "untracked nested git repo"},
		{"too big", func(t *testing.T, rig rescueRig) {
			old := rescueUntrackedCap
			rescueUntrackedCap = 10
			t.Cleanup(func() { rescueUntrackedCap = old })
			writeFile(t, filepath.Join(rig.ws.Worktree, "big.bin"), strings.Repeat("x", 100))
		}, "untracked files (cap"},
		{"merge in progress", func(t *testing.T, rig rescueRig) {
			writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "conflict\n")
			gd := gitRun(t, rig.g.env, rig.ws.Worktree, "rev-parse", "--absolute-git-dir")
			writeFile(t, filepath.Join(gd, "MERGE_HEAD"), gitRun(t, rig.g.env, rig.ws.Worktree, "rev-parse", "HEAD")+"\n")
		}, "merge or rebase is in progress"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRescueRig(t)
			tc.setup(t, rig)
			rep := rig.rescue(t)
			w := rig.worktree(t, rep, rig.ws.Worktree)
			if rep.OK || rep.CleanAndPushed || !strings.Contains(w.Refused, tc.want) || w.RescueBranch != "" {
				t.Errorf("report ok=%v, worktree %+v, want refused %q", rep.OK, w, tc.want)
			}
		})
	}
}

func TestRescueWithoutOrigin(t *testing.T) {
	rig := newRescueRig(t)
	if err := os.RemoveAll(rig.g.remote); err != nil {
		t.Fatal(err)
	}
	rep := rig.rescue(t)
	if !rep.OK || rep.CleanAndPushed || rep.Repos[0].Fetched || rep.Repos[0].FetchError == "" {
		t.Errorf("report %+v", rep.Repos[0])
	}
}

func TestRescueLock(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockFile(s.statePath(rescueLockFile))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Rescue(context.Background(), &fakeRunner{}, s, "s", rescueNow); err == nil || !strings.Contains(err.Error(), "another rescue") {
		t.Errorf("err = %v", err)
	}
	unlock()
	rep, err := Rescue(context.Background(), &fakeRunner{}, s, "s", rescueNow)
	if err != nil || !rep.OK || len(rep.Repos) != 0 {
		t.Errorf("no repos: %+v %v", rep, err)
	}
}
