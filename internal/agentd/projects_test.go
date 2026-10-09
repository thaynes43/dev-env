package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const projectTestCatalog = `{"version":1,"repositories":{"demo":{"github":"fixture/demo","defaultBranch":"main"}},"projects":{"sample":{"repositories":[{"name":"demo"}],"rules":"PROJECT_RULE_IDENTIFIER\nExact rules."}}}`

// projectFixtureRunner routes only clone/fetch commands to a tiny file origin.
// Git's stored catalog identity remains HTTPS. No test reaches the network.
type projectFixtureRunner struct {
	ExecRunner
	remote       string
	failFetch    bool
	cloneFailure string
	cloneCalls   int
}

func (r *projectFixtureRunner) Run(ctx context.Context, cmd Cmd) (Result, error) {
	if cmd.Name != "git" {
		return Result{}, errors.New("unexpected non-Git fixture command")
	}
	cmd.Args = slices.Clone(cmd.Args)
	if i := slices.Index(cmd.Args, "clone"); i >= 0 {
		r.cloneCalls++
		if r.cloneFailure != "" {
			if r.cloneFailure == "partial" {
				if err := os.Mkdir(cmd.Args[len(cmd.Args)-1], 0o700); err != nil {
					return Result{}, err
				}
				if err := os.WriteFile(filepath.Join(cmd.Args[len(cmd.Args)-1], "keep"), []byte("partial"), 0o600); err != nil {
					return Result{}, err
				}
			}
			return Result{}, &CmdError{Name: "git", Sub: "clone", ExitCode: 128}
		}
		identity := cmd.Args[len(cmd.Args)-2]
		cmd.Args[len(cmd.Args)-2] = "file://" + r.remote
		// Fetching another fixture branch must not trigger Git's internal
		// promisor subprocess against the HTTPS identity during checkout.
		cmd.Args = slices.DeleteFunc(cmd.Args, func(arg string) bool { return arg == "--filter=blob:none" })
		result, err := r.ExecRunner.Run(ctx, cmd)
		if err == nil {
			_, err = r.ExecRunner.Run(ctx, Cmd{Name: "git", Args: []string{"-C", cmd.Args[len(cmd.Args)-1], "remote", "set-url", "origin", identity}})
		}
		return result, err
	}
	if i := slices.Index(cmd.Args, "fetch"); i >= 0 {
		if r.failFetch {
			return Result{}, &CmdError{Name: "git", Sub: "fetch", ExitCode: 128}
		}
		for j := i + 1; j < len(cmd.Args); j++ {
			if cmd.Args[j] == "origin" {
				cmd.Args[j] = "file://" + r.remote
				break
			}
		}
	}
	return r.ExecRunner.Run(ctx, cmd)
}

func projectFixture(t *testing.T) (gitFixture, Settings, *projectFixtureRunner, *projectcatalog.Catalog) {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, execRunner := g.settings(t)
	execRunner.BaseEnv = append(execRunner.BaseEnv, "GIT_ALLOW_PROTOCOL=file")
	s, _ = sharedSettings(t, s, "task-a")
	c, err := projectcatalog.Parse([]byte(projectTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	return g, s, &projectFixtureRunner{ExecRunner: execRunner, remote: g.remote}, c
}

func syncProjectFixture(t *testing.T, s Settings, r Runner, c *projectcatalog.Catalog) ProjectSyncReport {
	t.Helper()
	report, err := SyncProjects(context.Background(), r, s, c, ProjectSyncOptions{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func assertNoPreservedProject(t *testing.T, report ProjectSyncReport) {
	t.Helper()
	for _, finding := range report.Findings {
		if finding.State == "preserved" {
			t.Fatalf("unexpected refusal: %+v", finding)
		}
	}
}

func TestProjectSyncDisabledAndMountIdentity(t *testing.T) {
	_, s, r, c := projectFixture(t)
	if _, err := SyncProjects(context.Background(), r, s, c, ProjectSyncOptions{}); err == nil {
		t.Fatal("catalog enabled by default")
	}
	if r.cloneCalls != 0 || exists(filepath.Join(s.Home, "codex", "sample")) {
		t.Fatal("disabled sync mutated storage")
	}
	if _, err := SyncProjects(context.Background(), r, s, &projectcatalog.Catalog{}, ProjectSyncOptions{Enabled: true}); err == nil {
		t.Fatal("zero value catalog accepted as server authority")
	}
	writeFile(t, filepath.Join(s.workspaceDir(), "marker.json"), `{"version":1,"id":"foreign"}`)
	if _, err := SyncProjects(context.Background(), r, s, c, ProjectSyncOptions{Enabled: true}); err == nil {
		t.Fatal("foreign mount identity accepted")
	}
	if r.cloneCalls != 0 {
		t.Fatal("identity refusal cloned")
	}
}

func TestProjectSyncIdempotentFreshAnchorsAndPreservedPeer(t *testing.T) {
	g, s, r, c := projectFixture(t)
	undeclared := filepath.Join(s.Home, "codex", "old-project")
	writeFile(t, filepath.Join(undeclared, "keep"), "old data")
	report := syncProjectFixture(t, s, r, c)
	assertNoPreservedProject(t, report)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == undeclared && f.State == "undeclared" }) {
		t.Fatal("undeclared root not reported")
	}
	anchor := filepath.Join(s.Home, "codex", "sample", "demo")
	first := gitRun(t, g.env, anchor, "rev-parse", "HEAD")
	agents, err := os.ReadFile(filepath.Join(s.Home, "codex", "sample", "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	claude, err := os.ReadFile(filepath.Join(s.Home, "codex", "sample", "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(agents) != string(claude) || !strings.Contains(string(agents), "PROJECT_RULE_IDENTIFIER") {
		t.Fatal("providers did not get identical project wrapper")
	}
	peer := filepath.Join(s.WorkDir(), "peer")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "-b", "agent/peer", peer, first)
	// Temporarily absent peer paths must never lose their Git registration.
	absent := peer + "-hidden"
	if err := os.Rename(peer, absent); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Rename(absent, peer) }()
	before := gitRun(t, g.env, s.ClonePath("demo"), "worktree", "list", "--porcelain")
	report = syncProjectFixture(t, s, r, c)
	assertNoPreservedProject(t, report)
	if r.cloneCalls != 1 || gitRun(t, g.env, s.ClonePath("demo"), "worktree", "list", "--porcelain") != before {
		t.Fatal("repeat sync cloned or pruned peer administration")
	}
	writeFile(t, filepath.Join(g.seed, "README.md"), "new remote commit\n")
	gitRun(t, g.env, g.seed, "add", "README.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "next")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	report = syncProjectFixture(t, s, r, c)
	assertNoPreservedProject(t, report)
	target := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	if target == first || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != target || gitRun(t, g.env, s.ClonePath("demo"), "rev-parse", "HEAD") != target {
		t.Fatal("reference or anchor did not use fresh target")
	}
	if !exists(filepath.Join(undeclared, "keep")) {
		t.Fatal("undeclared project removed")
	}
	receipts, err := os.ReadDir(filepath.Join(s.workspaceDir(), "repairs"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("behind repair receipt: %v %d", err, len(receipts))
	}
}

func TestProjectSyncDirtyIgnoredAnchorAndUnmanagedWrapper(t *testing.T) {
	g, s, r, c := projectFixture(t)
	assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
	anchor := filepath.Join(s.Home, "codex", "sample", "demo")
	writeFile(t, filepath.Join(anchor, "README.md"), "owner WIP\n")
	before := gitRun(t, g.env, anchor, "rev-parse", "HEAD")
	report := syncProjectFixture(t, s, r, c)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == anchor && f.State == "preserved" }) {
		t.Fatal("dirty anchor not reported")
	}
	data, _ := os.ReadFile(filepath.Join(anchor, "README.md"))
	if string(data) != "owner WIP\n" || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != before {
		t.Fatal("dirty anchor changed")
	}
	gitRun(t, g.env, anchor, "checkout", "--", "README.md")
	writeFile(t, filepath.Join(s.ClonePath("demo"), ".git", "info", "exclude"), "ignored\n")
	writeFile(t, filepath.Join(anchor, "ignored"), "hidden WIP\n")
	report = syncProjectFixture(t, s, r, c)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == anchor && f.State == "preserved" }) || !exists(filepath.Join(anchor, "ignored")) {
		t.Fatal("ignored anchor file not preserved")
	}
	wrapper := filepath.Join(s.Home, "codex", "sample", "AGENTS.md")
	writeFile(t, wrapper, "owner instructions\n")
	report = syncProjectFixture(t, s, r, c)
	data, _ = os.ReadFile(wrapper)
	if string(data) != "owner instructions\n" || !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return strings.Contains(f.Detail, "wrapper") && f.State == "preserved" }) {
		t.Fatal("unmanaged wrapper overwritten")
	}
}

func TestProjectSyncMultiRepoOverrideAndRepositoryInstructions(t *testing.T) {
	g, s, r, _ := projectFixture(t)
	writeFile(t, filepath.Join(g.seed, "AGENTS.md"), "NATIVE_REPO_RULE\n")
	writeFile(t, filepath.Join(g.seed, "CLAUDE.md"), "NATIVE_CLAUDE_RULE\n")
	gitRun(t, g.env, g.seed, "add", "AGENTS.md", "CLAUDE.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "native rules")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	main := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	gitRun(t, g.env, g.seed, "checkout", "-q", "-b", "stable")
	writeFile(t, filepath.Join(g.seed, "README.md"), "stable branch\n")
	gitRun(t, g.env, g.seed, "add", "README.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "stable")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "stable")
	stable := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	c, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"demo":{"github":"fixture/demo"},"other":{"github":"fixture/other"}},"projects":{"sample":{"repositories":[{"name":"demo","defaultBranch":"stable"},{"name":"other"}],"rules":""}}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
	if gitRun(t, g.env, s.ClonePath("demo"), "rev-parse", "HEAD") != main {
		t.Fatal("project override moved canonical global default")
	}
	root := filepath.Join(s.Home, "codex", "sample")
	if gitRun(t, g.env, filepath.Join(root, "demo"), "rev-parse", "HEAD") != stable || gitRun(t, g.env, filepath.Join(root, "other"), "rev-parse", "HEAD") != main {
		t.Fatal("multi-repo anchors ignored per-project defaults")
	}
	for _, file := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root, "demo", file))
		if err != nil || !strings.HasPrefix(string(data), "NATIVE_") {
			t.Fatal("generated project rules overwrote repository instructions")
		}
	}
	// A branch deleted upstream cannot be admitted from its cached remote ref.
	gitRun(t, g.env, g.seed, "push", "-q", "origin", ":stable")
	report := syncProjectFixture(t, s, r, c)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.State == "preserved" && strings.Contains(f.Detail, "fetch") }) || gitRun(t, g.env, filepath.Join(root, "demo"), "rev-parse", "HEAD") != stable {
		t.Fatal("deleted remote branch selected cached source")
	}
}

func TestProjectReferenceRepairsAndPreservesLocalCommit(t *testing.T) {
	for _, scenario := range []string{"wrong branch", "detached", "stale index with local HEAD"} {
		t.Run(scenario, func(t *testing.T) {
			g, s, r, c := projectFixture(t)
			assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
			clone := s.ClonePath("demo")
			target := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			switch scenario {
			case "wrong branch":
				gitRun(t, g.env, clone, "checkout", "-q", "-b", "local-feature")
			case "detached":
				gitRun(t, g.env, clone, "checkout", "-q", "--detach")
			case "stale index with local HEAD":
				writeFile(t, filepath.Join(clone, "README.md"), "local commit\n")
				gitRun(t, g.env, clone, "add", "README.md")
				gitRun(t, g.env, clone, "commit", "-q", "-m", "local only")
				gitRun(t, g.env, clone, "read-tree", "--reset", "-u", target)
			}
			oldHead := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
			if gitRun(t, g.env, clone, "rev-parse", "HEAD") != target || gitRun(t, g.env, clone, "symbolic-ref", "HEAD") != "refs/heads/main" {
				t.Fatal("repair did not restore the fresh default")
			}
			refs := gitRun(t, g.env, clone, "for-each-ref", "--format=%(objectname)", "refs/dev-env/repairs")
			if !strings.Contains(refs, oldHead) {
				t.Fatal("repair discarded prior HEAD")
			}
			if scenario == "wrong branch" && gitRun(t, g.env, clone, "rev-parse", "refs/heads/local-feature") != oldHead {
				t.Fatal("repair moved peer/local branch")
			}
		})
	}
}

func TestProjectReferenceRefusesUnsafeStates(t *testing.T) {
	for _, scenario := range []string{"dirty files", "local index", "ignored file", "operation", "foreign identity", "foreign push identity", "peer default", "mode change", "failed fetch"} {
		t.Run(scenario, func(t *testing.T) {
			g, s, r, c := projectFixture(t)
			assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
			clone := s.ClonePath("demo")
			switch scenario {
			case "dirty files":
				writeFile(t, filepath.Join(clone, "README.md"), "dirty\n")
			case "local index":
				writeFile(t, filepath.Join(clone, "README.md"), "staged local\n")
				gitRun(t, g.env, clone, "add", "README.md")
			case "ignored file":
				writeFile(t, filepath.Join(clone, ".git", "info", "exclude"), "ignored\n")
				writeFile(t, filepath.Join(clone, "ignored"), "keep\n")
			case "operation":
				writeFile(t, filepath.Join(clone, ".git", "MERGE_HEAD"), gitRun(t, g.env, clone, "rev-parse", "HEAD")+"\n")
			case "foreign identity":
				gitRun(t, g.env, clone, "remote", "set-url", "origin", "https://github.com/foreign/demo")
			case "foreign push identity":
				gitRun(t, g.env, clone, "remote", "set-url", "--push", "origin", "https://github.com/foreign/demo")
			case "peer default":
				gitRun(t, g.env, clone, "checkout", "-q", "--detach")
				gitRun(t, g.env, clone, "worktree", "add", "-q", filepath.Join(s.WorkDir(), "peer"), "main")
			case "mode change":
				if err := os.Chmod(filepath.Join(clone, "README.md"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "failed fetch":
				r.failFetch = true
			}
			beforeHead := gitRun(t, g.env, clone, "rev-parse", "HEAD")
			beforeTree := gitRun(t, g.env, clone, "write-tree")
			report := syncProjectFixture(t, s, r, c)
			if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == clone && f.State == "preserved" }) {
				t.Fatalf("unsafe state accepted: %+v", report.Findings)
			}
			if gitRun(t, g.env, clone, "rev-parse", "HEAD") != beforeHead || gitRun(t, g.env, clone, "write-tree") != beforeTree {
				t.Fatal("unsafe reference changed")
			}
		})
	}
}

func TestProjectCloneStagingPreservation(t *testing.T) {
	for _, scenario := range []string{"preexisting", "partial", "no staging"} {
		t.Run(scenario, func(t *testing.T) {
			_, s, r, c := projectFixture(t)
			noSleep(t)
			staging := filepath.Join(s.ReposDir(), ".demo.agentd-clone")
			switch scenario {
			case "preexisting":
				writeFile(t, filepath.Join(staging, "keep"), "original")
			case "partial":
				r.cloneFailure = "partial"
			default:
				r.cloneFailure = "empty"
			}
			report := syncProjectFixture(t, s, r, c)
			if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.Path == s.ClonePath("demo") && f.State == "preserved" }) {
				t.Fatal("failed clone not reported")
			}
			want := cloneAttempts
			switch scenario {
			case "preexisting":
				want = 0
			case "partial":
				want = 1
			}
			if r.cloneCalls != want {
				t.Fatalf("clone retries: got %d want %d", r.cloneCalls, want)
			}
			if scenario != "no staging" && !exists(filepath.Join(staging, "keep")) {
				t.Fatal("clone staging deleted")
			}
		})
	}
}

func TestProjectSnapshotPrivateImmutableAndProviderInputs(t *testing.T) {
	_, s, _, c := projectFixture(t)
	snapshot, err := c.Snapshot("sample", "")
	if err != nil {
		t.Fatal(err)
	}
	path, err := StoreProjectSnapshot(s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreProjectSnapshot(s, snapshot); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("rules snapshot is not private")
	}
	claude, err := ProjectRuleInputs("claude", path, "configured", "existing guard", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(claude, []string{"--append-system-prompt-file", path, "--append-system-prompt", "existing guard"}) {
		t.Fatal("Claude inputs changed guard or prompt")
	}
	codex, err := ProjectRuleInputs("codex", path, "configured developer identifier", "existing guard", snapshot, "REPO_RULES.md")
	if err != nil {
		t.Fatal(err)
	}
	if len(codex) != 4 || codex[0] != "-c" || codex[2] != "-c" {
		t.Fatal("Codex has multiple developer overrides")
	}
	if codex[3] != `project_doc_fallback_filenames=["REPO_RULES.md","CLAUDE.md"]` {
		t.Fatal("native repository fallback configuration replaced")
	}
	text, err := strconv.Unquote(strings.TrimPrefix(codex[1], "developer_instructions="))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, "configured developer identifier\n\n") || !strings.Contains(text, "PROJECT_RULE_IDENTIFIER") || !strings.HasSuffix(text, "existing guard") {
		t.Fatal("Codex lost configured developer text, project rules or guard")
	}
	changed, err := projectcatalog.Parse([]byte(strings.Replace(projectTestCatalog, "Exact rules.", "New rules.", 1)))
	if err != nil {
		t.Fatal(err)
	}
	newSnapshot, err := changed.Snapshot("sample", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StoreProjectSnapshot(s, newSnapshot); err == nil {
		t.Fatal("catalog update replaced task rules")
	}
	public := s
	public.StateDir = s.WorkDir()
	if _, err := StoreProjectSnapshot(public, snapshot); err == nil {
		t.Fatal("snapshot written into shared worktree scope")
	}
	data, err := os.ReadFile(filepath.Join(s.StateDir, "project-snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := projectcatalog.ParseSnapshot(data)
	if err != nil || loaded.RulesRevision() != snapshot.RulesRevision() {
		t.Fatal("resume snapshot changed")
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["catalogRevision"] != c.Revision() {
		t.Fatal("snapshot has client revision instead of accepted authority")
	}
	controlInputs, err := ProjectRuleInputs("codex", path, "developer\x01\a\v", "guard", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(controlInputs[1], `\x`) || strings.Contains(controlInputs[1], `\a`) || strings.Contains(controlInputs[1], `\v`) {
		t.Fatal("Codex scalar contains non-TOML Go escapes")
	}
}

func TestProjectSyncUsesExistingCommonGitLock(t *testing.T) {
	_, s, r, c := projectFixture(t)
	unlock, err := workspaceAdminLock(context.Background(), s, "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = SyncProjects(ctx, r, s, c, ProjectSyncOptions{Enabled: true})
	if !errors.Is(err, context.DeadlineExceeded) || r.cloneCalls != 0 {
		t.Fatal("sync bypassed common Git lock or ignored cancellation")
	}
}

func TestProjectSyncPreservesExistingGitRoot(t *testing.T) {
	g, s, r, c := projectFixture(t)
	repo := c.Repositories()[0]
	if err := cloneProjectReference(context.Background(), r, s, repo); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(s.Home, "codex", "sample")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "--detach", root, "HEAD")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "owner root instructions\n")
	writeFile(t, filepath.Join(root, "README.md"), "owner root WIP\n")
	beforeHead := gitRun(t, g.env, root, "rev-parse", "HEAD")
	beforeRefs := gitRun(t, g.env, s.ClonePath("demo"), "worktree", "list", "--porcelain")
	report := syncProjectFixture(t, s, r, c)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool {
		return f.Path == root && f.State == "preserved" && strings.Contains(f.Detail, "Git administration")
	}) {
		t.Fatal("existing Git project root not reported")
	}
	for file, want := range map[string]string{"AGENTS.md": "owner root instructions\n", "README.md": "owner root WIP\n"} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || string(data) != want {
			t.Fatal("existing Git root changed", file)
		}
	}
	if gitRun(t, g.env, root, "rev-parse", "HEAD") != beforeHead || gitRun(t, g.env, s.ClonePath("demo"), "worktree", "list", "--porcelain") != beforeRefs || exists(filepath.Join(root, "CLAUDE.md")) {
		t.Fatal("existing root was migrated or rewritten")
	}
}

func TestProjectLockSetReleasesPartialAcquisition(t *testing.T) {
	_, s, _, _ := projectFixture(t)
	unlock, err := workspaceAdminLock(context.Background(), s, "z")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := workspaceAdminLocks(ctx, s, []string{"z", "a"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("uncertain multi-lock acquisition accepted")
	}
	free, err := workspaceAdminLock(context.Background(), s, "a")
	if err != nil {
		t.Fatal("partial set retained a lock", err)
	}
	free()
}
