package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func pushPrivateProjectChange(t *testing.T, g gitFixture, file string) string {
	t.Helper()
	writeFile(t, filepath.Join(g.seed, file), file+"\n")
	gitRun(t, g.env, g.seed, "add", file)
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "remote "+file)
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	return gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
}

func readTaskSourceRecord(t *testing.T, s Settings) (protocol.TaskSource, []byte) {
	t.Helper()
	path := s.statePath(taskSourceFile)
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("task source record is missing or not a private regular file: %v", err)
	}
	var record protocol.TaskSource
	if err := readWorkspaceJSON(path, &record); err != nil {
		t.Fatal(err)
	}
	return record, mustRead(t, path)
}

func taskSourceJSON(t *testing.T, source *protocol.TaskSource) string {
	t.Helper()
	if source == nil {
		return "null"
	}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// roots-2: the new task's pin is recorded once and survives a resume that
// fetches a newer tip. Status reports the recorded pin, not the fresh fetch.
func TestPrivateProjectTaskSourceRecordSurvivesResume(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	pinned := pushPrivateProjectChange(t, g, "first.txt")
	before := time.Now().UTC()
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || ws.Head != pinned {
		t.Fatalf("new task: head %s, want %s; %s %q", ws.Head, pinned, step.State, step.Notes)
	}
	record, saved := readTaskSourceRecord(t, s)
	want := protocol.TaskSource{Version: 1, Session: sess.Name, SessionUID: sess.SessionUID, Repo: "demo", GitHub: "fixture/demo", Branch: "main",
		Commit: pinned, FetchedAt: record.FetchedAt, CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision()}
	if taskSourceJSON(t, &record) != taskSourceJSON(t, &want) || record.FetchedAt.Before(before.Add(-time.Second)) || record.CatalogRevision == "" || record.RulesRevision == "" {
		t.Fatalf("task source record %+v, want %+v", record, want)
	}
	if taskSourceJSON(t, ws.Source) != taskSourceJSON(t, &record) {
		t.Fatalf("prepared workspace does not report its source: %+v", ws.Source)
	}

	newer := pushPrivateProjectChange(t, g, "second.txt")
	resumed, step := PrepareRepo(context.Background(), r, s, sess)
	notes := strings.Join(step.Notes, "\n")
	if step.State != StepOK || resumed.Head != pinned || !strings.Contains(notes, "reused the worktree") {
		t.Fatalf("resume: head %s; %s %q", resumed.Head, step.State, step.Notes)
	}
	if _, again := readTaskSourceRecord(t, s); !bytes.Equal(again, saved) {
		t.Fatal("resume rewrote the task source record")
	}
	if resumed.Source == nil || resumed.Source.Commit != pinned {
		t.Fatalf("resume reported source %+v, want %s", resumed.Source, pinned)
	}
	if strings.Contains(notes, "fetched accepted repository") || !strings.Contains(notes, "refreshed origin/main") ||
		!strings.Contains(notes, newer) || !strings.Contains(notes, "not the task's source") || !strings.Contains(notes, pinned) {
		t.Fatalf("resume fetch note does not separate the refresh from the task's source: %q", step.Notes)
	}

	// Status reads the durable record, not boot.json's copy of a workspace.
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b2", Boot: protocol.BootReady, Workspace: &protocol.Workspace{Clone: resumed.Clone, Worktree: resumed.Worktree}}); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), r.ExecRunner, s, sess.Name, time.Now())
	if st.Workspace == nil || taskSourceJSON(t, st.Workspace.Source) != taskSourceJSON(t, &record) || st.Workspace.Head != pinned {
		t.Fatalf("status workspace %+v does not report the recorded source", st.Workspace)
	}
}

// roots-2: a retry after a failed worktree creation creates the task from its
// recorded pin; the record is never replaced by a newer fetch.
func TestPrivateProjectTaskSourceRetryUsesRecordedPin(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	pinned := pushPrivateProjectChange(t, g, "first.txt")
	failing := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if i := slices.Index(c.Args, "worktree"); i >= 0 && len(c.Args) > i+1 && c.Args[i+1] == "add" {
			return Result{}, &CmdError{Name: "git", Sub: "worktree", ExitCode: 128, Stderr: "fatal: synthetic worktree failure"}
		}
		return r.Run(context.Background(), c)
	}}
	if _, step := PrepareRepo(context.Background(), failing, s, sess); step.State != StepFail || exists(s.WorktreePath(sess.Name)) {
		t.Fatalf("synthetic worktree failure: %s %q", step.State, step.Notes)
	}
	record, saved := readTaskSourceRecord(t, s)
	if record.Commit != pinned {
		t.Fatalf("recorded pin %s, want %s", record.Commit, pinned)
	}
	pushPrivateProjectChange(t, g, "second.txt")
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || ws.Head != pinned || ws.Source == nil || ws.Source.Commit != pinned {
		t.Fatalf("retry did not use the recorded pin: head %s; %s %q", ws.Head, step.State, step.Notes)
	}
	if _, again := readTaskSourceRecord(t, s); !bytes.Equal(again, saved) {
		t.Fatal("retry replaced the task source record")
	}
}

// roots-2: a record that binds another session or source is never adopted or
// replaced; the new task is refused before any worktree exists.
func TestPrivateProjectTaskSourceRefusesForeignRecord(t *testing.T) {
	_, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	foreign := protocol.TaskSource{Version: 1, Session: "another-task", Repo: "demo", GitHub: "fixture/demo", Branch: "main",
		Commit: strings.Repeat("a", 40), FetchedAt: time.Now().UTC()}
	if err := writeWorkspaceJSON(s.statePath(taskSourceFile), foreign); err != nil {
		t.Fatal(err)
	}
	saved := mustRead(t, s.statePath(taskSourceFile))
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepFail || exists(ws.Worktree) || !bytes.Equal(saved, mustRead(t, s.statePath(taskSourceFile))) {
		t.Fatalf("foreign task source record was adopted or replaced: %s %q", step.State, step.Notes)
	}
}

// roots-3: the selected-branch fetch writes only its explicit destination,
// even when a configured refspec would map into local branches.
func TestFetchProjectTargetIgnoresConfiguredRefspecs(t *testing.T) {
	for _, checkout := range []string{"owner-branch", "default-branch"} {
		t.Run(checkout, func(t *testing.T) {
			g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			snapshot, err := acceptedProjectTask(s, sess)
			if err != nil {
				t.Fatal(err)
			}
			clone := s.ClonePath(sess.Repo)
			if _, err := cloneRepo(context.Background(), r, s, sess.Repo, clone); err != nil {
				t.Fatal(err)
			}
			gitRun(t, g.env, clone, "commit", "-q", "--allow-empty", "-m", "local-only owner commit")
			local := gitRun(t, g.env, clone, "rev-parse", "refs/heads/main")
			if checkout == "owner-branch" {
				gitRun(t, g.env, clone, "checkout", "-q", "-b", "owner-work")
			}
			gitRun(t, g.env, clone, "config", "--replace-all", "remote.origin.fetch", "+refs/heads/*:refs/heads/*")
			tip := pushPrivateProjectChange(t, g, "remote.txt")
			s.privateProjectGit = true
			r.namedRemote = true
			var fetchArgs []string
			wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if slices.Contains(c.Args, "fetch") {
					fetchArgs = c.Args
				}
				return r.Run(context.Background(), c)
			}}
			target, _, err := fetchProjectTarget(context.Background(), wrapped, s, snapshot.Selected())
			if err != nil || target != tip {
				t.Fatalf("fetch: %v, target %s, want %s", err, target, tip)
			}
			if !slices.Contains(fetchArgs, "--refmap=") {
				t.Fatalf("fetch applied configured refspecs: %q", fetchArgs)
			}
			if got := gitRun(t, g.env, clone, "rev-parse", "refs/heads/main"); got != local {
				t.Fatalf("configured refspec moved local main from %s to %s", local, got)
			}
			if got := gitRun(t, g.env, clone, "rev-parse", "refs/remotes/origin/main"); got != tip {
				t.Fatalf("explicit destination %s, want %s", got, tip)
			}
		})
	}
}

// roots-4: a resume needs no fetch, so drift in the shared origin
// configuration warns and preserves the task instead of refusing it.
func TestPrivateProjectResumeToleratesOriginIdentityDrift(t *testing.T) {
	for _, drift := range []string{"push-origin", "origin", "multiple-origins"} {
		t.Run(drift, func(t *testing.T) {
			g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			ws, step := PrepareRepo(context.Background(), r, s, sess)
			if step.State != StepOK {
				t.Fatalf("new task: %s %q", step.State, step.Notes)
			}
			writeFile(t, filepath.Join(ws.Worktree, "README.md"), "uncommitted project work\n")
			writeFile(t, filepath.Join(ws.Worktree, "staged.txt"), "staged project work\n")
			gitRun(t, g.env, ws.Worktree, "add", "staged.txt")
			writeFile(t, filepath.Join(ws.Worktree, "untracked.txt"), "untracked project work\n")
			before := gitResumeSnapshot(t, g, ws)
			switch drift {
			case "push-origin":
				gitRun(t, g.env, ws.Worktree, "remote", "set-url", "--push", "origin", "https://github.com/fixture/fork")
			case "origin":
				gitRun(t, g.env, ws.Worktree, "remote", "set-url", "origin", "https://github.com/fixture/fork")
			case "multiple-origins":
				gitRun(t, g.env, ws.Worktree, "remote", "set-url", "--add", "origin", "https://github.com/fixture/fork")
			}
			pushPrivateProjectChange(t, g, "remote.txt")
			fetched := false
			wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if slices.Contains(c.Args, "fetch") {
					fetched = true
				}
				return r.Run(context.Background(), c)
			}}
			resumed, step := PrepareRepo(context.Background(), wrapped, s, sess)
			notes := strings.Join(step.Notes, "\n")
			if step.State != StepWarn || fetched || resumed.Head != ws.Head || !strings.Contains(notes, "reused the worktree") ||
				!strings.Contains(notes, "identity does not match the catalog") || !maps.Equal(before, gitResumeSnapshot(t, g, resumed)) {
				t.Fatalf("resume after identity drift: fetched %v, %s %q", fetched, step.State, step.Notes)
			}
			if resumed.Source == nil || resumed.Source.Commit != ws.Head {
				t.Fatalf("resume after identity drift lost its recorded source: %+v", resumed.Source)
			}

			// The session branch alone is also an existing task.
			gitRun(t, g.env, ws.Worktree, "add", "README.md", "untracked.txt")
			gitRun(t, g.env, ws.Worktree, "commit", "-q", "-m", "saved project work")
			tip := gitRun(t, g.env, ws.Worktree, "rev-parse", "HEAD")
			gitRun(t, g.env, ws.Clone, "worktree", "remove", "--force", ws.Worktree)
			recovered, step := PrepareRepo(context.Background(), wrapped, s, sess)
			if step.State != StepWarn || fetched || recovered.Head != tip || !strings.Contains(strings.Join(step.Notes, "\n"), "recreated the worktree on the existing branch") {
				t.Fatalf("branch recovery after identity drift: fetched %v, head %s; %s %q", fetched, recovered.Head, step.State, step.Notes)
			}

			// A new task on the drifted reference still requires the full proof.
			other := sess
			other.Name = "private-project-other"
			if _, step := PrepareRepo(context.Background(), wrapped, s, other); step.State != StepFail || fetched || exists(s.WorktreePath(other.Name)) {
				t.Fatalf("new task on a drifted reference: fetched %v, %s %q", fetched, step.State, step.Notes)
			}
		})
	}
}

// roots-5: the task-path clone is the staged reference clone. Preserved
// staging is never deleted, and a fresh reference checks out the declared
// branch, so a later bootstrap refresh can advance it.
func TestPrivateProjectTaskCloneUsesStagedReference(t *testing.T) {
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"`, `"defaultBranch":"stable"`, 1)
	g, s, r, sess := privateProjectFixture(t, declaration)
	gitRun(t, g.env, g.seed, "checkout", "-q", "-b", "stable")
	writeFile(t, filepath.Join(g.seed, "stable.txt"), "stable\n")
	gitRun(t, g.env, g.seed, "add", "stable.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "stable branch")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "stable")
	stable := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(s.ReposDir(), ".demo.agentd-clone")
	writeFile(t, filepath.Join(staging, "keep"), "preserved bootstrap staging\n")
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepFail || r.cloneCalls != 0 || exists(ws.Clone) || exists(ws.Worktree) || !exists(filepath.Join(staging, "keep")) ||
		!strings.Contains(strings.Join(step.Notes, "\n"), "staging preserved") {
		t.Fatalf("task clone bypassed preserved staging: clones %d, %s %q", r.cloneCalls, step.State, step.Notes)
	}
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	ws, step = PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || ws.Head != stable || r.cloneCalls != 1 {
		t.Fatalf("staged task clone: head %s, want %s; %s %q", ws.Head, stable, step.State, step.Notes)
	}
	if got := gitRun(t, g.env, ws.Clone, "symbolic-ref", "HEAD"); got != "refs/heads/stable" {
		t.Fatalf("task clone checked out %s, not the declared branch", got)
	}
	s.PrivateProjectBootstrap, s.ProjectCatalog, s.ProjectCloneOwner = true, "dev-env-system/dev-env-project-catalog", "fixture"
	report := requirePrivateBootstrapReady(t, s, r, projectSyncFixtureOptions(declaration).AcceptedCatalog)
	if len(report.Repositories) != 1 || report.Repositories[0].State != "Ready" {
		t.Fatalf("bootstrap after a task clone: %+v", report)
	}
}

// roots-5: with bootstrap enabled, the legacy clone of a session without a
// project preserves existing staging instead of deleting it.
func TestPrepareRepoPreservesBootstrapStagingWithoutProject(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, runner := g.settings(t)
	s.PrivateProjectBootstrap = true
	staging := filepath.Join(s.ReposDir(), ".demo.agentd-clone")
	writeFile(t, filepath.Join(staging, "keep"), "preserved bootstrap staging\n")
	cloned := false
	wrapped := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if slices.Contains(c.Args, "clone") {
			cloned = true
		}
		return runner.Run(context.Background(), c)
	}}
	ws, step := PrepareRepo(context.Background(), wrapped, s, cloneSession("plain-task"))
	if step.State != StepFail || cloned || exists(ws.Clone) || !exists(filepath.Join(staging, "keep")) {
		t.Fatalf("legacy clone deleted preserved staging: cloned %v, %s %q", cloned, step.State, step.Notes)
	}
	if err := os.RemoveAll(staging); err != nil {
		t.Fatal(err)
	}
	if _, step := PrepareRepo(context.Background(), wrapped, s, cloneSession("plain-task")); step.State != StepOK {
		t.Fatalf("legacy clone without staging: %s %q", step.State, step.Notes)
	}
}

// roots-2: a task from before the record existed reports no source rather
// than presenting a resume fetch as its pin.
func TestPrivateProjectLegacyTaskReportsNoRecordedSource(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	if _, step := PrepareRepo(context.Background(), r, s, sess); step.State != StepOK {
		t.Fatalf("new task: %s %q", step.State, step.Notes)
	}
	// Simulate a task an earlier agentd created without a record.
	if err := os.Remove(s.statePath(taskSourceFile)); err != nil {
		t.Fatal(err)
	}
	pushPrivateProjectChange(t, g, "remote.txt")
	resumed, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || resumed.Source != nil || exists(s.statePath(taskSourceFile)) ||
		!strings.Contains(strings.Join(step.Notes, "\n"), "this task has no recorded source pin") {
		t.Fatalf("legacy resume: source %+v; %s %q", resumed.Source, step.State, step.Notes)
	}
}
