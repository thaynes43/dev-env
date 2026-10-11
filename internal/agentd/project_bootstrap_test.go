package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func privateBootstrapFixture(t *testing.T, declaration string) (gitFixture, Settings, *projectFixtureRunner, *ProjectCatalogSource) {
	t.Helper()
	g, s, r, _ := privateProjectFixture(t, declaration)
	s.PrivateProjectBootstrap, s.ProjectCatalog, s.ProjectCloneOwner = true, "dev-env-system/dev-env-project-catalog", "fixture"
	return g, s, r, projectSyncFixtureOptions(declaration).AcceptedCatalog
}

func requirePrivateBootstrapReady(t *testing.T, s Settings, r Runner, source *ProjectCatalogSource) PrivateProjectReport {
	t.Helper()
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err != nil || report.State != "Ready" {
		t.Fatalf("bootstrap: %v %+v", err, report)
	}
	var saved PrivateProjectReport
	if err := readWorkspaceJSON(s.statePath(privateProjectBootstrapFile), &saved); err != nil || saved.State != report.State || saved.CatalogRevision != report.CatalogRevision || saved.FinishedAt.IsZero() {
		t.Fatalf("retained report: %v %+v", err, saved)
	}
	return report
}

func pushBootstrapChange(t *testing.T, g gitFixture) string {
	t.Helper()
	writeFile(t, filepath.Join(g.seed, "README.md"), "fresh remote content\n")
	gitRun(t, g.env, g.seed, "commit", "-qam", "fresh remote")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	return gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
}

func TestPrivateCatalogBootstrapAllRepositoriesAndCleanFastForward(t *testing.T) {
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	g, s, r, source := privateBootstrapFixture(t, declaration)
	report := requirePrivateBootstrapReady(t, s, r, source)
	if len(report.Repositories) != 2 || report.CatalogUID != "catalog-1" || report.CatalogResourceVersion != "1" || report.CatalogRevision == "" || r.cloneCalls != 2 {
		t.Fatalf("catalog-wide receipt: %+v clones %d", report, r.cloneCalls)
	}
	peer := filepath.Join(s.Home, "peer-worktree")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "-b", "agent/peer", peer)
	peerHead := gitRun(t, g.env, peer, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(peer, "README.md"), "peer WIP\n")
	target := pushBootstrapChange(t, g)
	report = requirePrivateBootstrapReady(t, s, r, source)
	for _, repo := range report.Repositories {
		if repo.Target != target || repo.FetchedAt.IsZero() || gitRun(t, g.env, s.ClonePath(repo.Repository.Name), "rev-parse", "HEAD") != target {
			t.Fatalf("reference not refreshed: %+v", repo)
		}
	}
	if gitRun(t, g.env, peer, "rev-parse", "HEAD") != peerHead || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer WIP\n" || r.cloneCalls != 2 {
		t.Fatal("refresh changed peer work or recloned")
	}
	for _, path := range []string{s.workspaceDir(), filepath.Join(s.Home, "codex"), s.WorkDir()} {
		if exists(path) {
			t.Fatalf("bootstrap introduced project/task/shared state %s", path)
		}
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPrivateCatalogBootstrapPreservesOwnerState(t *testing.T) {
	for _, mode := range []string{"dirty", "staged", "ignored", "local commit", "wrong branch", "detached", "operation", "index lock", "assume unchanged", "wrong origin", "wrong push origin", "failed fetch"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			requirePrivateBootstrapReady(t, s, r, source)
			clone := s.ClonePath("demo")
			marker := ""
			switch mode {
			case "dirty", "staged", "assume unchanged":
				writeFile(t, filepath.Join(clone, "README.md"), "owner WIP\n")
				if mode == "staged" {
					gitRun(t, g.env, clone, "add", "README.md")
				}
				if mode == "assume unchanged" {
					gitRun(t, g.env, clone, "update-index", "--assume-unchanged", "README.md")
				}
			case "ignored":
				writeFile(t, filepath.Join(clone, ".git", "info", "exclude"), "ignored\n")
				writeFile(t, filepath.Join(clone, "ignored"), "owner ignored state\n")
			case "local commit":
				writeFile(t, filepath.Join(clone, "README.md"), "owner local commit\n")
				gitRun(t, g.env, clone, "commit", "-qam", "local-only")
			case "wrong branch":
				gitRun(t, g.env, clone, "checkout", "-qb", "owner-branch")
			case "detached":
				gitRun(t, g.env, clone, "checkout", "-q", "--detach")
			case "operation", "index lock":
				marker = filepath.Join(clone, ".git", "MERGE_HEAD")
				if mode == "index lock" {
					marker = filepath.Join(clone, ".git", "index.lock")
				}
				writeFile(t, marker, "owner operation\n")
			case "wrong origin":
				gitRun(t, g.env, clone, "remote", "set-url", "origin", "https://credential:secret-canary@github.com/fixture/demo")
			case "wrong push origin":
				gitRun(t, g.env, clone, "remote", "set-url", "--push", "origin", "https://credential:secret-canary@github.com/fixture/demo")
			case "failed fetch":
				r.failFetch = true
			}
			index := mustRead(t, filepath.Join(clone, ".git", "index"))
			readme := mustRead(t, filepath.Join(clone, "README.md"))
			head := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			refs := gitRun(t, g.env, clone, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads")
			pushBootstrapChange(t, g)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != "Partial" || len(report.Repositories) != 1 || report.Repositories[0].State != "Preserved" {
				t.Fatalf("unsafe state admitted: %v %+v", err, report)
			}
			if !bytes.Equal(index, mustRead(t, filepath.Join(clone, ".git", "index"))) || !bytes.Equal(readme, mustRead(t, filepath.Join(clone, "README.md"))) || gitRun(t, g.env, clone, "rev-parse", "HEAD") != head || gitRun(t, g.env, clone, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads") != refs {
				t.Fatal("bootstrap modified index, worktree, HEAD or owner branches")
			}
			if marker != "" && string(mustRead(t, marker)) != "owner operation\n" {
				t.Fatal("bootstrap modified an operation marker")
			}
			if mode == "ignored" && string(mustRead(t, filepath.Join(clone, "ignored"))) != "owner ignored state\n" {
				t.Fatal("bootstrap removed ignored state")
			}
			data, _ := json.Marshal(report)
			if strings.Contains(string(data), "secret-canary") {
				t.Fatal("receipt exposed a remote credential")
			}
		})
	}
}

func TestPrivateCatalogBootstrapPreservesPeerOnDefaultBranch(t *testing.T) {
	g, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
	requirePrivateBootstrapReady(t, s, r, source)
	clone, peer := s.ClonePath("demo"), filepath.Join(s.Home, "peer-default")
	gitRun(t, g.env, clone, "worktree", "add", "--force", "-q", peer, "main")
	writeFile(t, filepath.Join(peer, "README.md"), "peer staged WIP\n")
	gitRun(t, g.env, peer, "add", "README.md")
	head := gitRun(t, g.env, peer, "rev-parse", "HEAD")
	indexPath := gitRun(t, g.env, peer, "rev-parse", "--path-format=absolute", "--git-path", "index")
	index := mustRead(t, indexPath)
	worktrees := gitRun(t, g.env, clone, "worktree", "list", "--porcelain")
	target := pushBootstrapChange(t, g)
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.State != "Partial" || report.Repositories[0].Target != target || gitRun(t, g.env, clone, "rev-parse", "HEAD") != head || gitRun(t, g.env, peer, "rev-parse", "HEAD") != head || !bytes.Equal(index, mustRead(t, indexPath)) || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer staged WIP\n" || gitRun(t, g.env, clone, "worktree", "list", "--porcelain") != worktrees {
		t.Fatalf("bootstrap changed a linked default worktree: %v %+v", err, report)
	}
}

func TestPrivateCatalogBootstrapRefusesSymbolicFetchDestination(t *testing.T) {
	for _, branch := range []string{"main", "stable"} {
		t.Run(branch, func(t *testing.T) {
			declaration := projectTestCatalog
			if branch == "stable" {
				declaration = strings.Replace(declaration, `[{"name":"demo"}]`, `[{"name":"demo","defaultBranch":"stable"}]`, 1)
			}
			g, s, r, source := privateRootsFixture(t, declaration)
			if branch == "stable" {
				gitRun(t, g.env, g.seed, "checkout", "-qb", branch)
				gitRun(t, g.env, g.seed, "push", "-q", "origin", branch)
			}
			rootReceipt := requirePrivateRootReady(t, s, r, source)
			clone := s.ClonePath("demo")
			writeFile(t, filepath.Join(clone, "README.md"), "owner staged reference\n")
			gitRun(t, g.env, clone, "add", "README.md")
			gitRun(t, g.env, clone, "symbolic-ref", "refs/remotes/origin/"+branch, "refs/heads/main")
			head := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			index := mustRead(t, filepath.Join(clone, ".git", "index"))
			writeFile(t, filepath.Join(g.seed, "README.md"), "fresh branch content\n")
			gitRun(t, g.env, g.seed, "commit", "-qam", "fresh selected branch")
			gitRun(t, g.env, g.seed, "push", "-q", "origin", branch)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State == "Ready" || report.Projects[0].State != "Failed" || gitRun(t, g.env, clone, "rev-parse", "HEAD") != head || !bytes.Equal(index, mustRead(t, filepath.Join(clone, ".git", "index"))) || string(mustRead(t, filepath.Join(clone, "README.md"))) != "owner staged reference\n" || gitRun(t, g.env, privateRootPath(s), "rev-parse", "HEAD") != rootReceipt.Head || gitRun(t, g.env, clone, "symbolic-ref", "refs/remotes/origin/"+branch) != "refs/heads/main" {
				t.Fatalf("symbolic fetch destination changed owner state: %v %+v", err, report)
			}
		})
	}
}

func TestPrivateCatalogBootstrapRejectsFilesystemStorageAliases(t *testing.T) {
	for _, mode := range []string{"refs", "objects"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			requirePrivateBootstrapReady(t, s, r, source)
			clone, peer := s.ClonePath("demo"), filepath.Join(s.Home, "independent-peer")
			gitRun(t, g.env, s.Home, "clone", "-q", clone, peer)
			writeFile(t, filepath.Join(clone, "README.md"), "owner staged reference\n")
			gitRun(t, g.env, clone, "add", "README.md")
			writeFile(t, filepath.Join(peer, "README.md"), "peer staged reference\n")
			gitRun(t, g.env, peer, "add", "README.md")
			head := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			index := mustRead(t, filepath.Join(clone, ".git", "index"))
			peerIndex := mustRead(t, filepath.Join(peer, ".git", "index"))
			pushBootstrapChange(t, g)
			relative := "refs/heads"
			if mode == "objects" {
				blob := gitRun(t, g.env, g.seed, "hash-object", "README.md")
				relative = "objects/" + blob[:2]
			}
			path, target := filepath.Join(clone, ".git", relative), filepath.Join(peer, ".git", relative)
			if err := os.MkdirAll(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadDir(target)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			after, _ := os.ReadDir(target)
			if err == nil || report.State != "Partial" || report.Repositories[0].State != "Preserved" || gitRun(t, g.env, clone, "rev-parse", "HEAD") != head || gitRun(t, g.env, peer, "rev-parse", "HEAD") != head || !bytes.Equal(index, mustRead(t, filepath.Join(clone, ".git", "index"))) || !bytes.Equal(peerIndex, mustRead(t, filepath.Join(peer, ".git", "index"))) || string(mustRead(t, filepath.Join(clone, "README.md"))) != "owner staged reference\n" || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer staged reference\n" || len(before) != len(after) {
				t.Fatalf("private reference wrote through aliased storage: %v %+v", err, report)
			}
		})
	}
}

func TestPrivateCatalogBootstrapRefusesAuthorityBeforeGitAndPreservesStaging(t *testing.T) {
	for _, mode := range []string{"disabled", "missing source", "wrong owner", "missing UID", "read error", "changed before clone publication", "symlink repos", "staging exists"} {
		t.Run(mode, func(t *testing.T) {
			_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			switch mode {
			case "disabled":
				s.PrivateProjectBootstrap = false
				source.Read = func(context.Context) (ProjectCatalogResource, error) {
					t.Fatal("disabled bootstrap read authority")
					return ProjectCatalogResource{}, nil
				}
			case "missing source":
				source = nil
			case "wrong owner":
				s.ProjectCloneOwner = "unaccepted"
			case "missing UID", "read error", "changed before clone publication":
				read, calls := source.Read, 0
				source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
					calls++
					resource, err := read(ctx)
					if mode == "missing UID" {
						resource.UID = ""
					}
					if mode == "read error" {
						return ProjectCatalogResource{}, errors.New("secret-canary")
					}
					if mode == "changed before clone publication" && calls >= 4 {
						resource.ResourceVersion = "2"
					}
					return resource, err
				}
			case "symlink repos":
				if err := os.Symlink(t.TempDir(), s.ReposDir()); err != nil {
					t.Fatal(err)
				}
			case "staging exists":
				if err := os.MkdirAll(filepath.Join(s.ReposDir(), ".demo.agentd-clone"), 0o700); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(s.ReposDir(), ".demo.agentd-clone", "keep"), "owner staging")
			}
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if mode == "disabled" {
				if err != nil || report.State != "Disabled" || exists(s.statePath(privateProjectBootstrapFile)) {
					t.Fatal("disabled bootstrap performed work")
				}
				return
			}
			if err == nil || report.State == "Ready" || exists(s.ClonePath("demo")) {
				t.Fatalf("authority admitted: %v %+v", err, report)
			}
			if mode == "changed before clone publication" {
				if r.cloneCalls != 1 || !exists(filepath.Join(s.ReposDir(), ".demo.agentd-clone", ".git")) {
					t.Fatal("changed authority published or removed staging")
				}
			} else if r.cloneCalls != 0 {
				t.Fatal("refusal attempted clone")
			}
			data, _ := json.Marshal(report)
			if strings.Contains(string(data), "secret-canary") {
				t.Fatal("receipt leaked source error")
			}
		})
	}
}

func TestPrivateCatalogBootstrapDoesNotReplaceTaskFreshnessOrResumeWIP(t *testing.T) {
	g, s, r, sess := privateProjectFixture(t, projectTestCatalog)
	s.PrivateProjectBootstrap, s.ProjectCatalog, s.ProjectCloneOwner = true, "dev-env-system/dev-env-project-catalog", "fixture"
	s.PrivateProjectRoots = true
	source := projectSyncFixtureOptions().AcceptedCatalog
	requirePrivateBootstrapReady(t, s, r, source)
	target := pushBootstrapChange(t, g)
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	r.failFetch = true
	if _, step := PrepareRepo(context.Background(), r, s, sess); step.State != StepFail || exists(s.WorktreePath(sess.Name)) {
		t.Fatal("startup receipt bypassed task fetch failure")
	}
	r.failFetch = false
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || ws.Head != target {
		t.Fatalf("task did not pin its fresh source: %+v %+v", ws, step)
	}
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "resume WIP\n")
	gitRun(t, g.env, ws.Worktree, "add", "README.md")
	indexPath := gitRun(t, g.env, ws.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "index")
	index := mustRead(t, indexPath)
	savedRules := mustRead(t, s.statePath("project-snapshot.json"))
	changed := strings.Replace(projectTestCatalog, "Exact rules.", "Refreshed permanent rules.", 1)
	requirePrivateBootstrapReady(t, s, r, projectSyncFixtureOptions(changed).AcceptedCatalog)
	r.failFetch = true
	resumed, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State == StepFail || resumed.Head != target || !bytes.Equal(index, mustRead(t, indexPath)) || string(mustRead(t, filepath.Join(ws.Worktree, "README.md"))) != "resume WIP\n" || !bytes.Equal(savedRules, mustRead(t, s.statePath("project-snapshot.json"))) {
		t.Fatal("resume destroyed WIP or required startup freshness")
	}
}

func TestPrivateCatalogBootstrapReportsFailureAsSeparateBootStep(t *testing.T) {
	rig := newDaemonRig(t, nil)
	rig.d.S.PrivateProjectBootstrap, rig.d.S.ProjectCatalog, rig.d.S.ProjectCloneOwner = true, "dev-env-system/dev-env-project-catalog", "fixture"
	rig.d.ProjectCatalog = projectSyncFixtureOptions().AcceptedCatalog
	rig.d.ProjectCatalog.Read = func(context.Context) (ProjectCatalogResource, error) {
		return ProjectCatalogResource{}, errors.New("unavailable")
	}
	stop := rig.start(t)
	waitFor(t, func() bool {
		st, count := rig.beats.last()
		return count > 0 && (st.Boot == protocol.BootReady || st.Boot == protocol.BootFailed)
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	var record bootRecord
	if err := readJSONFile(rig.d.S.statePath(bootFile), &record); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(record.Steps, func(step Step) bool { return step.Name == "project-bootstrap" && step.State == StepFail }) || !slices.ContainsFunc(record.Steps, func(step Step) bool { return step.Name == "repo" && step.State == StepOK }) {
		t.Fatalf("bootstrap and task outcomes conflated: %+v", record.Steps)
	}
}

func TestPrivateCatalogBootstrapBoundsGitPreparation(t *testing.T) {
	_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
	s.PrivateProjectRoots = true
	runner := &deadlineBootstrapRunner{Runner: r, t: t}
	requirePrivateBootstrapReady(t, s, runner, source)
}

func TestPrivateCatalogBootstrapReceiptPrecedesMutationsAndCannotClaimUnretainedReady(t *testing.T) {
	for _, mode := range []string{"started", "symlink report"} {
		t.Run(mode, func(t *testing.T) {
			_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			if mode == "symlink report" {
				outside := filepath.Join(t.TempDir(), "owner-report")
				writeFile(t, outside, "owner report")
				if err := os.Symlink(outside, s.statePath(privateProjectBootstrapFile)); err != nil {
					t.Fatal(err)
				}
				source.Read = func(context.Context) (ProjectCatalogResource, error) {
					t.Fatal("uncertain report path read authority")
					return ProjectCatalogResource{}, nil
				}
				report, err := RefreshPrivateProjects(context.Background(), r, s, source)
				if err == nil || report.State != "Failed" || r.cloneCalls != 0 || string(mustRead(t, outside)) != "owner report" {
					t.Fatalf("unretained report admitted: %v %+v", err, report)
				}
				return
			}
			read := source.Read
			source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				var saved PrivateProjectReport
				if readWorkspaceJSON(s.statePath(privateProjectBootstrapFile), &saved) != nil || saved.State != "Started" || saved.StartedAt.IsZero() {
					t.Fatal("bootstrap mutated/read authority before superseding its old receipt")
				}
				return read(ctx)
			}
			requirePrivateBootstrapReady(t, s, r, source)
		})
	}
}

type deadlineBootstrapRunner struct {
	Runner
	t *testing.T
}

func (r *deadlineBootstrapRunner) Run(ctx context.Context, cmd Cmd) (Result, error) {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > sharedGitPrepareBudget {
		r.t.Fatal("bootstrap Git has no bounded per-repository deadline")
	}
	return r.Runner.Run(ctx, cmd)
}
