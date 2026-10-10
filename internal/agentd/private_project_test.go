package agentd

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func privateProjectFixture(t *testing.T, declaration string) (gitFixture, Settings, *projectFixtureRunner, protocol.Session) {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, runner := g.settings(t)
	s.RemoteBase = "https://github.com/fixture"
	s.Getenv = envOf(map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "synthetic-static-token"})
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog, err := projectcatalog.Parse([]byte(declaration))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "")
	if err != nil {
		t.Fatal(err)
	}
	sess := cloneSession("private-project-task")
	sess.SessionUID, sess.Repo, sess.Base = "private-session-uid", snapshot.Selected().Name, snapshot.Selected().DefaultBranch
	sess.ProjectSnapshot, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return g, s, &projectFixtureRunner{ExecRunner: runner, remote: g.remote}, sess
}

func TestPrivateProjectBootstrapUsesDeclaredAliasAndBranch(t *testing.T) {
	declaration := strings.ReplaceAll(projectTestCatalog, `"demo"`, `"local-alias"`)
	declaration = strings.Replace(declaration, `"defaultBranch":"main"`, `"defaultBranch":"stable"`, 1)
	g, s, r, sess := privateProjectFixture(t, declaration)
	gitRun(t, g.env, g.seed, "checkout", "-q", "-b", "stable")
	writeFile(t, filepath.Join(g.seed, "AGENTS.md"), "REPOSITORY_RULE_IDENTIFIER\n")
	gitRun(t, g.env, g.seed, "add", "AGENTS.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "stable repository rules")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "stable")
	tip := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	cloneIdentity := ""
	wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if slices.Contains(c.Args, "clone") {
			cloneIdentity = c.Args[len(c.Args)-2]
		}
		return r.Run(context.Background(), c)
	}}
	ws, step := PrepareRepo(context.Background(), wrapped, s, sess)
	if step.State != StepOK || ws.Head != tip || cloneIdentity != "https://github.com/fixture/demo" {
		t.Fatalf("private declared source: head %s, identity %s, %s %q", ws.Head, cloneIdentity, step.State, step.Notes)
	}
	if ws.Clone != s.ClonePath("local-alias") || s.WorkspaceID != "" || sess.Workspace != nil || exists(s.workspaceDir()) {
		t.Fatal("private project bootstrap required or created shared workspace state")
	}
	if gitRun(t, g.env, ws.Clone, "symbolic-ref", "--short", "HEAD") != "main" {
		t.Fatal("task preparation moved the reference checkout to the project branch")
	}
	rules, err := os.ReadFile(filepath.Join(ws.Worktree, "AGENTS.md"))
	if err != nil || string(rules) != "REPOSITORY_RULE_IDENTIFIER\n" {
		t.Fatal("task preparation rewrote repository instructions")
	}
	launch, err := BuildLaunch(s, sess, ws, "boot", rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if launch.Dir != ws.Worktree || !slices.Contains(launch.Argv, "--append-system-prompt-file") || !strings.Contains(strings.Join(launch.Argv, " "), "isolated git worktree") {
		t.Fatal("private Claude project launch lost project rules or its worktree guard")
	}
	stored, err := os.ReadFile(s.statePath("project-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := projectcatalog.ParseSnapshot(stored)
	if err != nil || snapshot.Selected().GitHub != "fixture/demo" || snapshot.Selected().DefaultBranch != "stable" || snapshot.RulesRevision() == "" || snapshot.CatalogRevision() == "" {
		t.Fatal("private preparation lost accepted source or rule provenance")
	}
}

func TestPrivateProjectFreshReusedClonePinsFetchedBranch(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	old := gitRun(t, g.env, s.ClonePath(sess.Repo), "rev-parse", "main")
	// An accepted task fetch must not depend on the clone's configured refspec.
	gitRun(t, g.env, s.ClonePath(sess.Repo), "config", "remote.origin.fetch", "+refs/heads/unrelated:refs/remotes/origin/unrelated")
	writeFile(t, filepath.Join(g.seed, "new.txt"), "new\n")
	gitRun(t, g.env, g.seed, "add", "new.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "new accepted source tip")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	tip := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	added := false
	wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if i := slices.Index(c.Args, "worktree"); i >= 0 && len(c.Args) > i+1 && c.Args[i+1] == "add" {
			added = true
			if c.Args[len(c.Args)-1] != tip {
				t.Fatalf("task worktree was not pinned to freshly fetched source: %v", c.Args)
			}
			gitRun(t, g.env, s.ClonePath(sess.Repo), "update-ref", "refs/remotes/origin/main", old)
		}
		return r.Run(context.Background(), c)
	}}
	ws, step := PrepareRepo(context.Background(), wrapped, s, sess)
	if step.State != StepOK || ws.Head != tip || !added {
		t.Fatalf("fresh private project task: head %s, want %s; %s %q", ws.Head, tip, step.State, step.Notes)
	}
	if gitRun(t, g.env, ws.Clone, "rev-parse", "main") != old {
		t.Fatal("task preparation moved the stale local reference branch")
	}
}

func TestPrivateProjectMaterializesFetchedTreeWithoutReplacementRefs(t *testing.T) {
	g, s, runner, sess := privateProjectFixture(t, projectTestCatalog)
	writeFile(t, filepath.Join(g.seed, "AGENTS.md"), "ORIGINAL_REPOSITORY_RULES\n")
	gitRun(t, g.env, g.seed, "add", "AGENTS.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "original accepted instructions")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	original := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	clone := s.ClonePath(sess.Repo)
	if _, err := cloneRepo(context.Background(), runner, s, sess.Repo, clone); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(clone, "AGENTS.md"), "REPLACEMENT_REPOSITORY_RULES\n")
	gitRun(t, g.env, clone, "add", "AGENTS.md")
	gitRun(t, g.env, clone, "commit", "-q", "-m", "local replacement instructions")
	replacement := gitRun(t, g.env, clone, "rev-parse", "HEAD")
	gitRun(t, g.env, clone, "replace", original, replacement)
	if got := gitRun(t, g.env, clone, "show", original+":AGENTS.md"); got != "REPLACEMENT_REPOSITORY_RULES" {
		t.Fatal("fixture did not install an effective commit replacement")
	}
	ws, step := PrepareRepo(context.Background(), runner, s, sess)
	if step.State != StepOK || ws.Head != original {
		t.Fatalf("accepted source did not retain its fetched identity: head %s; %s %q", ws.Head, step.State, step.Notes)
	}
	rules, err := os.ReadFile(filepath.Join(ws.Worktree, "AGENTS.md"))
	if err != nil || string(rules) != "ORIGINAL_REPOSITORY_RULES\n" {
		t.Fatal("replacement refs substituted the fetched task's repository instructions")
	}
	if gitRun(t, g.env, clone, "rev-parse", "refs/replace/"+original) != replacement {
		t.Fatal("private task preparation deleted local replacement state")
	}
}

func TestPrivateProjectRejectsWrongSnapshotBindingBeforeGit(t *testing.T) {
	for _, mismatch := range []string{"repo", "base", "owner", "snapshot"} {
		t.Run(mismatch, func(t *testing.T) {
			_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
			switch mismatch {
			case "repo":
				sess.Repo = "foreign"
			case "base":
				sess.Base = "foreign"
			case "owner":
				s.RemoteBase = "https://github.com/foreign"
			case "snapshot":
				sess.ProjectSnapshot = json.RawMessage(`{"unknown":true}`)
			}
			r := &fakeRunner{}
			ws, step := PrepareRepo(context.Background(), r, s, sess)
			if step.State != StepFail || len(r.calls) != 0 || exists(ws.Clone) || exists(ws.Worktree) {
				t.Fatal("wrong accepted identity reached private Git preparation")
			}
		})
	}
}

func TestPrivateProjectRejectsForeignCloneBeforeFetch(t *testing.T) {
	for _, mismatch := range []string{"origin", "push-origin", "multiple-origins", "common-dir", "root", "symlink"} {
		t.Run(mismatch, func(t *testing.T) {
			g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			clone := s.ClonePath(sess.Repo)
			if _, err := cloneRepo(context.Background(), r, s, sess.Repo, clone); err != nil {
				t.Fatal(err)
			}
			switch mismatch {
			case "origin":
				gitRun(t, g.env, clone, "remote", "set-url", "origin", "https://github.com/fixture/foreign")
			case "push-origin":
				gitRun(t, g.env, clone, "remote", "set-url", "--push", "origin", "https://github.com/fixture/foreign")
			case "multiple-origins":
				gitRun(t, g.env, clone, "remote", "set-url", "--add", "origin", "https://github.com/fixture/foreign")
			case "common-dir", "symlink":
				other := filepath.Join(g.root, "preserved-reference")
				if err := os.Rename(clone, other); err != nil {
					t.Fatal(err)
				}
				if mismatch == "symlink" {
					if err := os.Symlink(other, clone); err != nil {
						t.Fatal(err)
					}
				} else {
					gitRun(t, g.env, other, "worktree", "add", "--detach", clone, "HEAD")
				}
			}
			fetched := false
			wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if slices.Contains(c.Args, "fetch") {
					fetched = true
				}
				if mismatch == "root" && slices.Contains(c.Args, "--show-toplevel") {
					return Result{Stdout: []byte(g.seed)}, nil
				}
				return r.Run(context.Background(), c)
			}}
			ws, step := PrepareRepo(context.Background(), wrapped, s, sess)
			if step.State != StepFail || fetched || exists(ws.Worktree) || r.cloneCalls != 1 {
				t.Fatalf("foreign clone was fetched, replaced or used: %s %q", step.State, step.Notes)
			}
			if !exists(filepath.Join(clone, "README.md")) {
				t.Fatal("foreign reference contents were removed")
			}
		})
	}
}

func TestPrivateProjectFetchFailureRefusesFreshTaskAndPreservesResume(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	r.failFetch = true
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepFail || exists(ws.Worktree) || gitRun(t, g.env, ws.Clone, "for-each-ref", "--format=%(refname)", "refs/heads/"+ws.Branch) != "" {
		t.Fatal("failed project fetch created a fresh task branch")
	}
	r.failFetch = false
	ws, step = PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK {
		t.Fatal(step.Notes)
	}
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "uncommitted project work\n")
	writeFile(t, filepath.Join(ws.Worktree, "staged.txt"), "staged project work\n")
	gitRun(t, g.env, ws.Worktree, "add", "staged.txt")
	writeFile(t, filepath.Join(ws.Worktree, "untracked.txt"), "untracked project work\n")
	mergeHead := gitRun(t, g.env, ws.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "MERGE_HEAD")
	writeFile(t, mergeHead, ws.Head+"\n")
	before := gitResumeSnapshot(t, g, ws)
	r.failFetch = true
	resumed, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepWarn || resumed.Head != ws.Head || !maps.Equal(before, gitResumeSnapshot(t, g, resumed)) {
		t.Fatalf("private project resume changed pending work after failed fetch: %s %q", step.State, step.Notes)
	}
	if err := os.Remove(mergeHead); err != nil {
		t.Fatal(err)
	}
	gitRun(t, g.env, ws.Worktree, "add", "README.md")
	gitRun(t, g.env, ws.Worktree, "commit", "-q", "-m", "saved project work")
	tip := gitRun(t, g.env, ws.Worktree, "rev-parse", "HEAD")
	gitRun(t, g.env, ws.Clone, "worktree", "remove", "--force", ws.Worktree)
	recovered, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepWarn || recovered.Head != tip {
		t.Fatalf("offline private project branch recovery lost committed work: %s %q", step.State, step.Notes)
	}
}
