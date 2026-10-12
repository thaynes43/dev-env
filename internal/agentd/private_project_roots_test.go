package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func privateRootsFixture(t *testing.T, declaration string) (gitFixture, Settings, *projectFixtureRunner, *ProjectCatalogSource) {
	t.Helper()
	g, s, r, _ := privateBootstrapFixture(t, projectTestCatalog)
	s.PrivateProjectRoots = true
	return g, s, r, projectSyncFixtureOptions(declaration).AcceptedCatalog
}

func privateRootPath(s Settings) string { return filepath.Join(s.Home, "codex", "sample") }
func privateRootReceiptPath(s Settings) string {
	return filepath.Join(s.StateDir, "project-roots", "sample.json")
}

func requirePrivateRootReady(t *testing.T, s Settings, r Runner, source *ProjectCatalogSource) privateRootReceipt {
	t.Helper()
	report := requirePrivateBootstrapReady(t, s, r, source)
	if len(report.Projects) != 1 || report.Projects[0].State != "Ready" || report.Projects[0].Head == "" || report.Projects[0].RulesRevision == "" {
		t.Fatalf("project outcome: %+v", report)
	}
	var receipt privateRootReceipt
	if err := readWorkspaceJSONBounded(privateRootReceiptPath(s), &receipt, privateRootReceiptLimit); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestPrivateRootsRetainBoundedEscapedRuleSnapshot(t *testing.T) {
	encoded, err := json.Marshal(strings.Repeat("\x01", projectcatalog.MaxRulesBytes))
	if err != nil {
		t.Fatal(err)
	}
	declaration := strings.Replace(projectTestCatalog, `"PROJECT_RULE_IDENTIFIER\nExact rules."`, string(encoded), 1)
	_, s, r, source := privateRootsFixture(t, declaration)
	receipt := requirePrivateRootReady(t, s, r, source)
	if len(mustRead(t, privateRootReceiptPath(s))) <= 64<<10 {
		t.Fatal("fixture did not exercise expanded bounded ownership")
	}
	next := requirePrivateRootReady(t, s, r, source)
	if receipt.Head != next.Head || receipt.WrapperDigest != next.WrapperDigest {
		t.Fatal("saved escaped rules could not be proved on refresh")
	}
}

func TestPrivateRootsRefuseNonregularOwnershipMarker(t *testing.T) {
	_, s, r, source := privateRootsFixture(t, projectTestCatalog)
	receipt := requirePrivateRootReady(t, s, r, source)
	marker := filepath.Join(receipt.GitDir, "dev-env-project-id")
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan PrivateProjectReport, 1)
	go func() {
		report, _ := RefreshPrivateProjects(context.Background(), r, s, source)
		done <- report
	}()
	select {
	case report := <-done:
		if report.State == "Ready" || report.Projects[0].State != "Failed" {
			t.Fatal("nonregular ownership marker admitted")
		}
	case <-time.After(5 * time.Second):
		// Release an accidental FIFO reader so a failed regression cannot
		// strand the test's Git runner or the package process.
		if writer, err := os.OpenFile(marker, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_, _ = writer.Write([]byte("foreign marker"))
			_ = writer.Close()
		}
		t.Fatal("ownership proof blocked reading a nonregular marker")
	}
	if fi, err := os.Lstat(marker); err != nil || fi.Mode()&os.ModeNamedPipe == 0 {
		t.Fatal("nonregular owner state was modified")
	}
}

func TestPrivateRootsIndependentHomesAndSafeFastForward(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	first := requirePrivateRootReady(t, s, r, source)
	_, other, otherRunner, otherSource := privateRootsFixture(t, projectTestCatalog)
	second := requirePrivateRootReady(t, other, otherRunner, otherSource)
	if first.GitDir == second.GitDir || first.Anchors["demo"].GitDir == second.Anchors["demo"].GitDir || first.ID == second.ID {
		t.Fatal("independent private homes share Git identity")
	}
	root := privateRootPath(s)
	if gitRun(t, g.env, root, "rev-parse", "--show-toplevel") != root || gitRun(t, g.env, root, "remote") != "" || gitRun(t, g.env, root, "ls-tree", "--name-only", "HEAD") != "AGENTS.md\nCLAUDE.md" || gitRun(t, g.env, root, "status", "--porcelain") != "" || exists(s.workspaceDir()) {
		t.Fatal("root lacks ordinary Git discovery or exact generated-only ownership")
	}
	catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := catalog.ProjectSnapshot("sample")
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if string(mustRead(t, filepath.Join(root, name))) != snapshot.ProjectRules() {
			t.Fatal("generated wrapper bytes changed")
		}
	}
	peer := filepath.Join(s.Home, "peer")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "--detach", peer, first.Repositories[0].Target)
	writeFile(t, filepath.Join(peer, "README.md"), "peer staged work\n")
	gitRun(t, g.env, peer, "add", "README.md")
	peerHead := gitRun(t, g.env, peer, "rev-parse", "HEAD")
	peerIndexPath := gitRun(t, g.env, peer, "rev-parse", "--path-format=absolute", "--git-path", "index")
	peerIndex := mustRead(t, peerIndexPath)
	target := pushBootstrapChange(t, g)
	changed := strings.Replace(projectTestCatalog, "Exact rules.", "Changed exact rules.", 1)
	updated := requirePrivateRootReady(t, s, r, projectSyncFixtureOptions(changed).AcceptedCatalog)
	if updated.Head == first.Head || gitRun(t, g.env, root, "rev-parse", "HEAD^") != first.Head || updated.Repositories[0].Target != target || gitRun(t, g.env, filepath.Join(root, "demo"), "rev-parse", "HEAD") != target || updated.Anchors["demo"] != first.Anchors["demo"] || updated.WrapperDigest == first.WrapperDigest {
		t.Fatal("root or detached anchor did not safely advance with retained identity/provenance")
	}
	if gitRun(t, g.env, peer, "rev-parse", "HEAD") != peerHead || !bytes.Equal(peerIndex, mustRead(t, peerIndexPath)) || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer staged work\n" {
		t.Fatal("refresh changed a peer worktree")
	}
}

func TestPrivateRootsNoOpKeepsExactIndexAndHeadBytes(t *testing.T) {
	_, s, r, source := privateRootsFixture(t, projectTestCatalog)
	receipt := requirePrivateRootReady(t, s, r, source)
	paths := []string{filepath.Join(receipt.GitDir, "index"), filepath.Join(receipt.GitDir, "HEAD"), filepath.Join(receipt.Anchors["demo"].GitDir, "index"), filepath.Join(receipt.Anchors["demo"].GitDir, "HEAD")}
	before := make(map[string][]byte)
	for _, path := range paths {
		before[path] = mustRead(t, path)
	}
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if slices.Contains(c.Args, "merge") {
			t.Fatal("same-target refresh performed merge")
		}
		return r.Run(context.Background(), c)
	}}
	requirePrivateRootReady(t, s, runner, source)
	for _, path := range paths {
		if !bytes.Equal(before[path], mustRead(t, path)) {
			t.Fatalf("same-target refresh rewrote %s", path)
		}
	}
}

func TestPrivateRootsIsolateInheritedGitStorageAndConfigRouting(t *testing.T) {
	for _, mode := range []string{"foreign index", "foreign objects", "foreign repository", "config override"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			foreign := t.TempDir()
			foreignIndex := filepath.Join(foreign, "index")
			index := mustRead(t, filepath.Join(g.seed, ".git", "index"))
			if err := os.WriteFile(foreignIndex, index, 0o600); err != nil {
				t.Fatal(err)
			}
			objects := filepath.Join(foreign, "objects")
			if err := os.Mkdir(objects, 0o700); err != nil {
				t.Fatal(err)
			}
			var env []string
			switch mode {
			case "foreign index":
				env = []string{"GIT_INDEX_FILE=" + foreignIndex}
			case "foreign objects":
				env = []string{"GIT_OBJECT_DIRECTORY=" + objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(g.seed, ".git", "objects")}
			case "foreign repository":
				env = []string{"GIT_DIR=" + filepath.Join(g.seed, ".git"), "GIT_COMMON_DIR=" + filepath.Join(g.seed, ".git"), "GIT_WORK_TREE=" + g.seed}
			case "config override":
				env = []string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0=" + g.seed, "GIT_CONFIG_PARAMETERS='core.worktree=" + g.seed + "'"}
			}
			r.BaseEnv = append(r.BaseEnv, env...)
			first := requirePrivateRootReady(t, s, r, source)
			// Point the inherited index at an exact same-tree copy of the
			// owned root index before the generated rules fast-forward.
			if mode == "foreign index" {
				index = mustRead(t, filepath.Join(first.GitDir, "index"))
				if err := os.WriteFile(foreignIndex, index, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			pushBootstrapChange(t, g)
			changed := strings.Replace(projectTestCatalog, "Exact rules.", "Different rules.", 1)
			updated := requirePrivateRootReady(t, s, r, projectSyncFixtureOptions(changed).AcceptedCatalog)
			entries, err := os.ReadDir(objects)
			if err != nil || len(entries) != 0 || !bytes.Equal(index, mustRead(t, foreignIndex)) || updated.Head == first.Head || gitRun(t, g.env, privateRootPath(s), "cat-file", "-t", updated.Head) != "commit" {
				t.Fatal("private Git redirected storage or could not read owned synthetic objects")
			}
		})
	}
}

func TestPrivateRootsMultipleRepositoriesAndOverridePin(t *testing.T) {
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	declaration = strings.Replace(declaration, `[{"name":"demo"}]`, `[{"name":"demo","defaultBranch":"stable"},{"name":"extra"}]`, 1)
	g, s, r, source := privateRootsFixture(t, declaration)
	main := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	gitRun(t, g.env, g.seed, "checkout", "-q", "-b", "stable")
	writeFile(t, filepath.Join(g.seed, "AGENTS.md"), "NATIVE_REPOSITORY_RULE\n")
	gitRun(t, g.env, g.seed, "add", "AGENTS.md")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "stable instructions")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "stable")
	stable := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	receipt := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	if len(receipt.Repositories) != 2 || receipt.Exclusion != "/demo/\n/extra/\n" || gitRun(t, g.env, filepath.Join(root, "demo"), "rev-parse", "HEAD") != stable || gitRun(t, g.env, filepath.Join(root, "extra"), "rev-parse", "HEAD") != main || gitRun(t, g.env, s.ClonePath("demo"), "rev-parse", "HEAD") != main || string(mustRead(t, filepath.Join(root, "demo", "AGENTS.md"))) != "NATIVE_REPOSITORY_RULE\n" {
		t.Fatal("project overrides lost fresh immutable pins or native repository rules")
	}
}

func TestPrivateRootsPreserveReferenceWIPWhilePreparingFreshAnchor(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	requirePrivateRootReady(t, s, r, source)
	clone := s.ClonePath("demo")
	writeFile(t, filepath.Join(clone, "README.md"), "owner staged reference\n")
	gitRun(t, g.env, clone, "add", "README.md")
	index := mustRead(t, filepath.Join(clone, ".git", "index"))
	head := gitRun(t, g.env, clone, "rev-parse", "HEAD")
	target := pushBootstrapChange(t, g)
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.State != "Partial" || report.Projects[0].State != "Ready" || report.Projects[0].Repositories[0].Target != target || !bytes.Equal(index, mustRead(t, filepath.Join(clone, ".git", "index"))) || gitRun(t, g.env, clone, "rev-parse", "HEAD") != head || string(mustRead(t, filepath.Join(clone, "README.md"))) != "owner staged reference\n" {
		t.Fatalf("dirty reference was not preserved independently: %v %+v", err, report)
	}
}

func TestPrivateRootsPreserveAmbiguousRootAndAnchorState(t *testing.T) {
	// reason, repository and cause, as status reports them.
	codes := map[string][3]string{
		"root dirty":          {protocol.ReasonRootUnproven},
		"root staged":         {protocol.ReasonRootUnproven},
		"root ignored":        {protocol.ReasonRootUnproven},
		"root unknown":        {protocol.ReasonRootUnproven},
		"root local commit":   {protocol.ReasonRootUnproven},
		"root operation":      {protocol.ReasonRootUnproven},
		"root exclusion":      {protocol.ReasonRootUnproven},
		"root config":         {protocol.ReasonRootUnproven},
		"anchor dirty":        {protocol.ReasonAnchorUnproven, "demo", protocol.ReasonWorkingTreeDiffers},
		"anchor staged":       {protocol.ReasonAnchorUnproven, "demo", protocol.ReasonStagedChanges},
		"anchor ignored":      {protocol.ReasonAnchorUnproven, "demo", protocol.ReasonUntrackedFiles},
		"anchor local commit": {protocol.ReasonAnchorUnproven, "demo"},
		"anchor attached":     {protocol.ReasonAnchorUnproven, "demo"},
		"anchor operation":    {protocol.ReasonAnchorUnproven, "demo", protocol.ReasonGitOperationPresent},
		"removed anchor":      {protocol.ReasonAnchorRemoved, "demo"},
		"missing receipt":     {protocol.ReasonRootUnproven},
	}
	for _, mode := range []string{"root dirty", "root staged", "root ignored", "root unknown", "root local commit", "root operation", "root exclusion", "root config", "anchor dirty", "anchor staged", "anchor ignored", "anchor local commit", "anchor attached", "anchor operation", "removed anchor", "missing receipt"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			prior := requirePrivateRootReady(t, s, r, source)
			root, anchor := privateRootPath(s), filepath.Join(privateRootPath(s), "demo")
			marker := ""
			switch mode {
			case "root dirty", "root staged", "root local commit":
				writeFile(t, filepath.Join(root, "AGENTS.md"), "owner rules\n")
				if mode != "root dirty" {
					gitRun(t, g.env, root, "add", "AGENTS.md")
				}
				if mode == "root local commit" {
					gitRun(t, g.env, root, "commit", "-qm", "owner root")
				}
			case "root ignored", "root exclusion":
				writeFile(t, filepath.Join(root, ".git", "info", "exclude"), prior.Exclusion+"owner-ignored\n")
				if mode == "root ignored" {
					writeFile(t, filepath.Join(root, "owner-ignored"), "ignored root work\n")
				}
			case "root operation":
				marker = filepath.Join(root, ".git", "MERGE_HEAD")
			case "root unknown":
				writeFile(t, filepath.Join(root, "unknown"), "untracked owner file\n")
			case "root config":
				gitRun(t, g.env, root, "config", "core.fileMode", "false")
			case "anchor dirty", "anchor staged", "anchor local commit":
				writeFile(t, filepath.Join(anchor, "README.md"), "owner anchor\n")
				if mode != "anchor dirty" {
					gitRun(t, g.env, anchor, "add", "README.md")
				}
				if mode == "anchor local commit" {
					gitRun(t, g.env, anchor, "commit", "-qm", "owner anchor")
				}
			case "anchor ignored":
				writeFile(t, filepath.Join(s.ClonePath("demo"), ".git", "info", "exclude"), "owner-ignored\n")
				writeFile(t, filepath.Join(anchor, "owner-ignored"), "ignored anchor work\n")
			case "anchor attached":
				gitRun(t, g.env, anchor, "checkout", "-qb", "owner-anchor")
			case "anchor operation":
				marker = filepath.Join(prior.Anchors["demo"].GitDir, "MERGE_HEAD")
			case "removed anchor":
				changed := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
				changed = strings.Replace(changed, `[{"name":"demo"}]`, `[{"name":"extra"}]`, 1)
				source = projectSyncFixtureOptions(changed).AcceptedCatalog
			case "missing receipt":
				if err := os.Remove(privateRootReceiptPath(s)); err != nil {
					t.Fatal(err)
				}
			}
			if marker != "" {
				writeFile(t, marker, "owner operation\n")
			}
			rootHead, anchorHead := gitRun(t, g.env, root, "rev-parse", "HEAD"), gitRun(t, g.env, anchor, "rev-parse", "HEAD")
			rootIndex := mustRead(t, filepath.Join(root, ".git", "index"))
			anchorIndex := mustRead(t, filepath.Join(prior.Anchors["demo"].GitDir, "index"))
			wrapper, readme := mustRead(t, filepath.Join(root, "AGENTS.md")), mustRead(t, filepath.Join(anchor, "README.md"))
			config := mustRead(t, filepath.Join(root, ".git", "config"))
			pushBootstrapChange(t, g)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != "Partial" || len(report.Projects) != 1 || report.Projects[0].State != "Failed" || gitRun(t, g.env, root, "rev-parse", "HEAD") != rootHead || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != anchorHead || !bytes.Equal(rootIndex, mustRead(t, filepath.Join(root, ".git", "index"))) || !bytes.Equal(anchorIndex, mustRead(t, filepath.Join(prior.Anchors["demo"].GitDir, "index"))) || !bytes.Equal(wrapper, mustRead(t, filepath.Join(root, "AGENTS.md"))) || !bytes.Equal(readme, mustRead(t, filepath.Join(anchor, "README.md"))) || !bytes.Equal(config, mustRead(t, filepath.Join(root, ".git", "config"))) {
				t.Fatalf("unsafe owner state changed: %v %+v", err, report)
			}
			if got := report.Projects[0]; [3]string{got.Reason, got.Repository, got.Cause} != codes[mode] {
				t.Fatalf("root refusal reported as %s@%s(%s), want %v", got.Reason, got.Repository, got.Cause, codes[mode])
			}
			var operation PrivateProjectRootReport
			if readWorkspaceJSON(filepath.Join(s.StateDir, "project-roots", "sample.operation.json"), &operation) != nil || operation.State == "Ready" || operation.State == "Started" {
				t.Fatal("partial operation retained stale Ready")
			}
			if marker != "" && string(mustRead(t, marker)) != "owner operation\n" {
				t.Fatal("operation marker changed")
			}
			if strings.HasSuffix(mode, "ignored") {
				dir := root
				if strings.HasPrefix(mode, "anchor") {
					dir = anchor
				}
				if !exists(filepath.Join(dir, "owner-ignored")) {
					t.Fatal("ignored owner data disappeared")
				}
			}
		})
	}
}

func TestPrivateRootsRejectAliasedPeerAdministration(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "gitfile", true: "symlink"}[symlink], func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			prior := requirePrivateRootReady(t, s, r, source)
			root, anchor, peer := privateRootPath(s), filepath.Join(privateRootPath(s), "demo"), filepath.Join(s.Home, "peer")
			gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "--detach", peer, prior.Repositories[0].Target)
			peerAdmin := gitRun(t, g.env, peer, "rev-parse", "--absolute-git-dir")
			// The peer index is clean, but its files contain owner work. An
			// aliased anchor can otherwise look clean against this same index.
			writeFile(t, filepath.Join(peer, "README.md"), "peer work in progress\n")
			peerHead := gitRun(t, g.env, peer, "rev-parse", "HEAD")
			peerIndex := mustRead(t, filepath.Join(peerAdmin, "index"))
			rootIndex := mustRead(t, filepath.Join(root, ".git", "index"))
			gitfile := filepath.Join(anchor, ".git")
			if symlink {
				if err := os.Remove(gitfile); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(peer, ".git"), gitfile); err != nil {
					t.Fatal(err)
				}
			} else {
				writeFile(t, gitfile, "gitdir: "+peerAdmin+"\n")
			}
			pushBootstrapChange(t, g)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, peer, "rev-parse", "HEAD") != peerHead || !bytes.Equal(peerIndex, mustRead(t, filepath.Join(peerAdmin, "index"))) || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer work in progress\n" || gitRun(t, g.env, root, "rev-parse", "HEAD") != prior.Head || !bytes.Equal(rootIndex, mustRead(t, filepath.Join(root, ".git", "index"))) {
				t.Fatalf("aliased Git metadata changed peer state: %v %+v", err, report)
			}
		})
	}
}

func TestPrivateRootsRejectFilesystemRefLogAndObjectAliases(t *testing.T) {
	for _, mode := range []string{"refs", "reflogs", "objects", "ORIG_HEAD"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			prior := requirePrivateRootReady(t, s, r, source)
			root, peer := privateRootPath(s), filepath.Join(s.Home, "independent-peer")
			gitRun(t, g.env, s.Home, "clone", "-q", root, peer)
			writeFile(t, filepath.Join(peer, "AGENTS.md"), "peer staged rules\n")
			gitRun(t, g.env, peer, "add", "AGENTS.md")
			peerHead := gitRun(t, g.env, peer, "rev-parse", "HEAD")
			peerIndex := mustRead(t, filepath.Join(peer, ".git", "index"))
			peerLog := mustRead(t, filepath.Join(peer, ".git", "logs", "refs", "heads", "dev-env", "project"))
			changed := strings.Replace(projectTestCatalog, "Exact rules.", "Aliased target rules.", 1)
			path, target := "", ""
			switch mode {
			case "refs", "reflogs":
				relative := "refs/heads/dev-env"
				if mode == "reflogs" {
					relative = "logs/refs/heads/dev-env"
				}
				path, target = filepath.Join(prior.GitDir, relative), filepath.Join(peer, ".git", relative)
			case "objects":
				catalog, _ := projectcatalog.Parse([]byte(changed))
				snapshot, _ := catalog.ProjectSnapshot("sample")
				blob, err := r.ExecRunner.Run(context.Background(), Cmd{Name: "git", Args: []string{"-C", root, "hash-object", "--stdin"}, Stdin: strings.NewReader(snapshot.ProjectRules())})
				if err != nil {
					t.Fatal(err)
				}
				prefix := strings.TrimSpace(string(blob.Stdout))[:2]
				path, target = filepath.Join(prior.GitDir, "objects", prefix), filepath.Join(peer, ".git", "objects", prefix)
				if err := os.MkdirAll(target, 0o755); err != nil {
					t.Fatal(err)
				}
				writeFile(t, filepath.Join(target, "OWNER_WIP"), "peer object directory\n")
			case "ORIG_HEAD":
				path, target = filepath.Join(prior.GitDir, "ORIG_HEAD"), filepath.Join(peer, ".git", "owner-orig")
				writeFile(t, target, "peer original head\n")
			}
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			var objectEntries []os.DirEntry
			if mode == "objects" {
				objectEntries, _ = os.ReadDir(target)
			}
			report, err := RefreshPrivateProjects(context.Background(), r, s, projectSyncFixtureOptions(changed).AcceptedCatalog)
			if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, peer, "rev-parse", "HEAD") != peerHead || !bytes.Equal(peerIndex, mustRead(t, filepath.Join(peer, ".git", "index"))) || string(mustRead(t, filepath.Join(peer, "AGENTS.md"))) != "peer staged rules\n" || !bytes.Equal(peerLog, mustRead(t, filepath.Join(peer, ".git", "logs", "refs", "heads", "dev-env", "project"))) {
				t.Fatalf("aliased root storage changed peer state: %v %+v", err, report)
			}
			if mode == "objects" {
				entries, _ := os.ReadDir(target)
				if len(entries) != len(objectEntries) || string(mustRead(t, filepath.Join(target, "OWNER_WIP"))) != "peer object directory\n" {
					t.Fatal("synthetic root objects were written into peer storage")
				}
			}
			if mode == "ORIG_HEAD" && string(mustRead(t, target)) != "peer original head\n" {
				t.Fatal("aliased original-head file changed")
			}
		})
	}
}

func TestPrivateRootsRejectAliasedAnchorReflog(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	prior := requirePrivateRootReady(t, s, r, source)
	anchor, peer := filepath.Join(privateRootPath(s), "demo"), filepath.Join(s.Home, "peer")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "--detach", peer, prior.Repositories[0].Target)
	peerAdmin := gitRun(t, g.env, peer, "rev-parse", "--absolute-git-dir")
	peerLog := filepath.Join(peerAdmin, "logs", "HEAD")
	logBytes := mustRead(t, peerLog)
	writeFile(t, filepath.Join(peer, "README.md"), "peer staged WIP\n")
	gitRun(t, g.env, peer, "add", "README.md")
	peerIndex := mustRead(t, filepath.Join(peerAdmin, "index"))
	path := filepath.Join(prior.Anchors["demo"].GitDir, "logs", "HEAD")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(peerLog, path); err != nil {
		t.Fatal(err)
	}
	pushBootstrapChange(t, g)
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.Projects[0].State != "Failed" || !bytes.Equal(logBytes, mustRead(t, peerLog)) || !bytes.Equal(peerIndex, mustRead(t, filepath.Join(peerAdmin, "index"))) || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer staged WIP\n" || gitRun(t, g.env, peer, "rev-parse", "HEAD") != prior.Repositories[0].Target || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != prior.Repositories[0].Target {
		t.Fatalf("private anchor wrote through peer reflog: %v %+v", err, report)
	}
}

func TestPrivateRootsRefuseForeignAndPartialPublication(t *testing.T) {
	for _, mode := range []string{"plain", "foreign Git", "authority changed", "anchor failure"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			root := privateRootPath(s)
			runner := Runner(r)
			if mode == "plain" || mode == "foreign Git" {
				writeFile(t, filepath.Join(root, "owner"), "owner root\n")
				if mode == "foreign Git" {
					gitRun(t, g.env, root, "init", "-q")
				}
			}
			changed := false
			read := source.Read
			source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				resource, err := read(ctx)
				if changed {
					resource.ResourceVersion = "2"
				}
				return resource, err
			}
			if mode == "authority changed" || mode == "anchor failure" {
				runner = &fakeRunner{handle: func(c Cmd) (Result, error) {
					if mode == "anchor failure" && slices.Contains(c.Args, "worktree") && slices.Contains(c.Args, "add") {
						return Result{}, errors.New("secret-canary")
					}
					result, err := r.Run(context.Background(), c)
					if mode == "authority changed" && slices.Contains(c.Args, "checkout") && strings.Contains(c.Args[1], ".sample.agentd-root") {
						changed = true
					}
					return result, err
				}}
			}
			report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
			if err == nil || report.State == "Ready" || exists(privateRootReceiptPath(s)) {
				t.Fatalf("unconfirmed root adopted: %v %+v", err, report)
			}
			staging, _ := filepath.Glob(filepath.Join(s.Home, "codex", ".sample.agentd-root-*"))
			if mode == "authority changed" && (exists(root) || len(staging) != 1 || !exists(filepath.Join(staging[0], ".git"))) {
				t.Fatal("changed authority published or erased staging")
			}
			if mode == "plain" || mode == "foreign Git" {
				if string(mustRead(t, filepath.Join(root, "owner"))) != "owner root\n" {
					t.Fatal("foreign root overwritten")
				}
				retry, retryErr := RefreshPrivateProjects(context.Background(), r, s, source)
				if retryErr == nil || retry.Projects[0].State == "Ready" || exists(privateRootReceiptPath(s)) || string(mustRead(t, filepath.Join(root, "owner"))) != "owner root\n" {
					t.Fatal("foreign root adopted on retry")
				}
			}
			// agentd's own interrupted creation resumes: the published root
			// keeps its generated commit, and unpublished staging is rebuilt.
			if mode == "anchor failure" || mode == "authority changed" {
				partialHead := ""
				if mode == "anchor failure" {
					partialHead = gitRun(t, g.env, root, "rev-parse", "HEAD")
				}
				retry := requirePrivateRootReady(t, s, r, source)
				left, _ := filepath.Glob(filepath.Join(s.Home, "codex", ".sample.agentd-root*"))
				if partialHead != "" && retry.Head != partialHead || len(left) != 0 || exists(privateRootPendingPath(s)) || !exists(filepath.Join(root, "demo", "README.md")) {
					t.Fatalf("interrupted root creation was not resumed: %+v %v", retry, left)
				}
			}
		})
	}
}

func TestPrivateRootsRequireBootstrapBeforeDaemonWork(t *testing.T) {
	s := Settings{PrivateProjectRoots: true, Home: t.TempDir()}
	s.StateDir = filepath.Join(s.Home, ".agentd")
	d := &Daemon{S: s}
	if err := d.Run(context.Background()); err == nil || exists(s.StateDir) {
		t.Fatal("invalid roots configuration reached daemon work")
	}
	report, err := RefreshPrivateProjects(context.Background(), nil, s, nil)
	if err == nil || report.State == "Ready" || exists(filepath.Join(s.Home, "codex")) {
		t.Fatal("roots without bootstrap performed work")
	}
}

func TestPrivateRootsRetainOperationBeforeMutationAndFailUnretainedOutcome(t *testing.T) {
	for _, mode := range []string{"started", "symlink operation", "terminal retention"} {
		t.Run(mode, func(t *testing.T) {
			_, s, r, source := privateRootsFixture(t, projectTestCatalog)
			operation := filepath.Join(s.StateDir, "project-roots", "sample.operation.json")
			outside := filepath.Join(t.TempDir(), "owner-outcome")
			writeFile(t, outside, "owner outcome\n")
			if mode == "symlink operation" {
				if err := os.MkdirAll(filepath.Dir(operation), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, operation); err != nil {
					t.Fatal(err)
				}
			}
			startedChecked := false
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if strings.Contains(c.Args[1], ".sample.agentd-root") {
					var saved PrivateProjectRootReport
					if readWorkspaceJSON(operation, &saved) != nil || saved.State != "Started" {
						t.Fatal("root mutation preceded retained Started")
					}
					startedChecked = true
				}
				result, err := r.Run(context.Background(), c)
				if mode == "terminal retention" && slices.Contains(c.Args, "worktree") && slices.Contains(c.Args, "add") {
					if err := os.Remove(operation); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, operation); err != nil {
						t.Fatal(err)
					}
				}
				return result, err
			}}
			report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
			if mode == "started" {
				if err != nil || report.State != "Ready" || !startedChecked {
					t.Fatalf("root operation was not retained: %v %+v", err, report)
				}
				return
			}
			if err == nil || report.State == "Ready" || report.Projects[0].State != "Failed" || string(mustRead(t, outside)) != "owner outcome\n" {
				t.Fatalf("unretained root outcome claimed Ready: %v %+v", err, report)
			}
			if mode == "symlink operation" && exists(privateRootPath(s)) {
				t.Fatal("uncertain operation path allowed root mutation")
			}
			if got := report.Projects[0]; got.Reason != protocol.ReasonReceiptUnconfirmed || got.Repository != "" || got.Cause != "" {
				t.Fatalf("unretained outcome reported as %+v", got)
			}
		})
	}
}

func privateRootPendingPath(s Settings) string {
	return filepath.Join(s.StateDir, "project-roots", "sample.pending.json")
}

// failingMerge fails one fast-forward run in dir, before Git runs, once.
func failingMerge(r Runner, dir string) *fakeRunner {
	failed := false
	return &fakeRunner{handle: func(c Cmd) (Result, error) {
		if !failed && len(c.Args) > 1 && c.Args[1] == dir && slices.Contains(c.Args, "merge") && slices.Contains(c.Args, "--ff-only") {
			failed = true
			return Result{}, &CmdError{Name: "git", Sub: "merge", ExitCode: 128}
		}
		return r.Run(context.Background(), c)
	}}
}

// afterMerge marks when a fast-forward in dir succeeded, so a test can fail
// the next authority confirmation after the change it made.
func afterMerge(r Runner, dir string, merged *bool) *fakeRunner {
	return &fakeRunner{handle: func(c Cmd) (Result, error) {
		result, err := r.Run(context.Background(), c)
		if err == nil && len(c.Args) > 1 && c.Args[1] == dir && slices.Contains(c.Args, "merge") && slices.Contains(c.Args, "--ff-only") {
			*merged = true
		}
		return result, err
	}}
}

func failConfirmWhen(source *ProjectCatalogSource, fail *bool) *ProjectCatalogSource {
	read := source.Read
	copied := *source
	copied.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
		if *fail {
			return ProjectCatalogResource{}, errors.New("transient catalog read")
		}
		return read(ctx)
	}
	return &copied
}

func TestPrivateRootsRecoverInterruptedMultiAnchorAdvance(t *testing.T) {
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	declaration = strings.Replace(declaration, `[{"name":"demo"}]`, `[{"name":"demo"},{"name":"extra"}]`, 1)
	g, s, r, source := privateRootsFixture(t, declaration)
	prior := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	demo, extra := filepath.Join(root, "demo"), filepath.Join(root, "extra")
	target := pushBootstrapChange(t, g)
	report, err := RefreshPrivateProjects(context.Background(), failingMerge(r, extra), s, source)
	if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != prior.Repositories[1].Target || !exists(privateRootPendingPath(s)) {
		t.Fatalf("fixture did not leave a partial two-anchor advance: %v %+v", err, report)
	}
	updated := requirePrivateRootReady(t, s, r, source)
	if gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != target || updated.Repositories[0].Target != target || updated.Repositories[1].Target != target || updated.Head != prior.Head || updated.ID != prior.ID || updated.Anchors["demo"] != prior.Anchors["demo"] || updated.Anchors["extra"] != prior.Anchors["extra"] || exists(privateRootPendingPath(s)) {
		t.Fatalf("interrupted advance was not resumed: %+v", updated)
	}
	again := requirePrivateRootReady(t, s, r, source)
	if again.Repositories[0].Target != target || again.Head != updated.Head {
		t.Fatal("resumed root did not stay proved")
	}
}

func TestPrivateRootsRecoverAdvanceAfterAuthorityConfirmFailure(t *testing.T) {
	for _, mode := range []string{"anchor", "root wrappers"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "root wrappers" {
				// Git writes the fast-forwarded wrappers through this umask.
				priorMask := syscall.Umask(0o077)
				defer syscall.Umask(priorMask)
			}
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			prior := requirePrivateRootReady(t, s, r, source)
			root := privateRootPath(s)
			anchor := filepath.Join(root, "demo")
			target := pushBootstrapChange(t, g)
			dir := anchor
			if mode == "root wrappers" {
				source = projectSyncFixtureOptions(strings.Replace(projectTestCatalog, "Exact rules.", "Recovered rules.", 1)).AcceptedCatalog
				dir = root
			}
			fail := false
			report, err := RefreshPrivateProjects(context.Background(), afterMerge(r, dir, &fail), s, failConfirmWhen(source, &fail))
			if err == nil || !fail || report.State == "Ready" || len(report.Projects) != 0 && report.Projects[0].State == "Ready" || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != target {
				t.Fatalf("fixture did not fail after the change: %v %+v", err, report)
			}
			if report.Reason != protocol.ReasonCatalogUnconfirmed && (len(report.Projects) == 0 || report.Projects[0].Reason != protocol.ReasonCatalogUnconfirmed) {
				t.Fatalf("authority failure reported as %s %+v", report.Reason, report.Projects)
			}
			partialHead := gitRun(t, g.env, root, "rev-parse", "HEAD")
			if mode == "root wrappers" {
				if partialHead == prior.Head {
					t.Fatal("fixture did not fast-forward the root")
				}
				if fi, err := os.Lstat(filepath.Join(root, "AGENTS.md")); err != nil || fi.Mode().Perm() == 0o644 {
					t.Fatal("fixture did not leave umask wrapper modes")
				}
			}
			updated := requirePrivateRootReady(t, s, r, source)
			if gitRun(t, g.env, anchor, "rev-parse", "HEAD") != target || updated.Repositories[0].Target != target || updated.Head != partialHead || gitRun(t, g.env, root, "rev-parse", "HEAD") != partialHead || exists(privateRootPendingPath(s)) {
				t.Fatalf("authority failure stranded the root: %+v", updated)
			}
			for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
				if fi, err := os.Lstat(filepath.Join(root, name)); err != nil || fi.Mode().Perm() != 0o644 {
					t.Fatal("recovered wrapper lacks the declared mode")
				}
			}
		})
	}
}

func TestPrivateRootsInterruptedAdvanceStillPreservesOwnerState(t *testing.T) {
	for _, mode := range []string{"anchor dirty", "anchor local commit", "root unknown"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			requirePrivateRootReady(t, s, r, source)
			root := privateRootPath(s)
			anchor := filepath.Join(root, "demo")
			pushBootstrapChange(t, g)
			fail := false
			if _, err := RefreshPrivateProjects(context.Background(), afterMerge(r, anchor, &fail), s, failConfirmWhen(source, &fail)); err == nil || !fail {
				t.Fatal("fixture did not fail after the anchor change")
			}
			switch mode {
			case "anchor dirty", "anchor local commit":
				writeFile(t, filepath.Join(anchor, "README.md"), "owner anchor\n")
				if mode == "anchor local commit" {
					gitRun(t, g.env, anchor, "commit", "-qam", "owner anchor")
				}
			case "root unknown":
				writeFile(t, filepath.Join(root, "unknown"), "owner root\n")
			}
			anchorHead, rootHead := gitRun(t, g.env, anchor, "rev-parse", "HEAD"), gitRun(t, g.env, root, "rev-parse", "HEAD")
			readme := mustRead(t, filepath.Join(anchor, "README.md"))
			writeFile(t, filepath.Join(g.seed, "README.md"), "later remote content\n")
			gitRun(t, g.env, g.seed, "commit", "-qam", "later remote")
			gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != anchorHead || gitRun(t, g.env, root, "rev-parse", "HEAD") != rootHead || !bytes.Equal(readme, mustRead(t, filepath.Join(anchor, "README.md"))) || !exists(privateRootPendingPath(s)) {
				t.Fatalf("recovery adopted owner state: %v %+v", err, report)
			}
			if mode == "root unknown" && string(mustRead(t, filepath.Join(root, "unknown"))) != "owner root\n" {
				t.Fatal("unknown root file changed")
			}
		})
	}
}

func TestPrivateRootsLeaveOtherStagingUntouched(t *testing.T) {
	_, s, r, source := privateRootsFixture(t, projectTestCatalog)
	legacy := filepath.Join(s.Home, "codex", ".sample.agentd-root")
	writeFile(t, filepath.Join(legacy, "owner"), "owner staging\n")
	requirePrivateRootReady(t, s, r, source)
	if string(mustRead(t, filepath.Join(legacy, "owner"))) != "owner staging\n" {
		t.Fatal("staging agentd does not own was changed")
	}
}

func TestPrivateRootsRecoverAddedAnchorBeforeItsIdentityIsRecorded(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	prior := requirePrivateRootReady(t, s, r, source)
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	declaration = strings.Replace(declaration, `[{"name":"demo"}]`, `[{"name":"demo"},{"name":"extra"}]`, 1)
	source = projectSyncFixtureOptions(declaration).AcceptedCatalog
	root := privateRootPath(s)
	extra := filepath.Join(root, "extra")
	// Fail agentd's post-create proof of the new anchor once, so its
	// worktree exists while the pending record has no identity for it.
	created, failed := false, false
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if created && !failed && len(c.Args) > 1 && c.Args[1] == extra {
			failed = true
			return Result{}, &CmdError{Name: "git", Sub: "rev-parse", ExitCode: 128}
		}
		result, err := r.Run(context.Background(), c)
		if err == nil && slices.Contains(c.Args, "worktree") && slices.Contains(c.Args, "add") && slices.Contains(c.Args, extra) {
			created = true
		}
		return result, err
	}}
	report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
	var pending privateRootPending
	if err == nil || !failed || report.Projects[0].State != "Failed" || !exists(filepath.Join(extra, ".git")) || readWorkspaceJSONBounded(privateRootPendingPath(s), &pending, privateRootPendingLimit) != nil || pending.To.Anchors["extra"].Identity != nil {
		t.Fatalf("fixture did not leave an unrecorded new anchor: %v %+v", err, report)
	}
	updated := requirePrivateRootReady(t, s, r, source)
	if updated.ID != prior.ID || updated.Exclusion != "/demo/\n/extra/\n" || updated.Anchors["extra"].GitDir == "" || updated.Anchors["demo"] != prior.Anchors["demo"] || gitRun(t, g.env, extra, "rev-parse", "HEAD") != updated.Repositories[1].Target || exists(privateRootPendingPath(s)) {
		t.Fatalf("new anchor was not resumed: %+v", updated)
	}
}

func TestPrivateRootsRefuseForeignPendingRecord(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	prior := requirePrivateRootReady(t, s, r, source)
	pending := privateRootPending{Version: 1, ID: "foreign-root-id", Root: privateRootPath(s), ConfigDigest: prior.ConfigDigest, From: prior.state(prior.Repositories, prior.Anchors), To: prior.state(prior.Repositories, prior.Anchors)}
	if err := writeWorkspaceJSON(privateRootPendingPath(s), pending); err != nil {
		t.Fatal(err)
	}
	pushBootstrapChange(t, g)
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, filepath.Join(privateRootPath(s), "demo"), "rev-parse", "HEAD") != prior.Repositories[0].Target {
		t.Fatalf("pending record for another root was accepted: %v %+v", err, report)
	}
}

func TestPrivateRootsPreserveChangedStagingGitDirectory(t *testing.T) {
	// "extra file" fails the path allowlist by name. The others sit at paths
	// the allowlist accepts, so only the file-type and link-count checks
	// refuse them.
	for _, mode := range []string{"extra file", "symlinked object", "symlinked directory", "hard-linked object"} {
		t.Run(mode, func(t *testing.T) {
			_, s, r, source := privateRootsFixture(t, projectTestCatalog)
			changed := false
			read := source.Read
			source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				resource, err := read(ctx)
				if changed {
					resource.ResourceVersion = "2"
				}
				return resource, err
			}
			// Authority changes after the staging checkout, so agentd's own
			// unpublished staging is left behind with its pending record.
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				result, err := r.Run(context.Background(), c)
				if slices.Contains(c.Args, "checkout") && strings.Contains(c.Args[1], ".sample.agentd-root-") {
					changed = true
				}
				return result, err
			}}
			if _, err := RefreshPrivateProjects(context.Background(), runner, s, source); err == nil {
				t.Fatal("fixture did not leave staging")
			}
			staging, _ := filepath.Glob(filepath.Join(s.Home, "codex", ".sample.agentd-root-*"))
			if len(staging) != 1 {
				t.Fatalf("staging: %v", staging)
			}
			owner := filepath.Join(s.Home, "owner-notes")
			writeFile(t, owner, "owner notes\n")
			path := filepath.Join(staging[0], ".git", "info", "owner-notes")
			switch mode {
			case "extra file":
				writeFile(t, path, "owner notes\n")
			case "symlinked object", "hard-linked object":
				objects, _ := filepath.Glob(filepath.Join(staging[0], ".git", "objects", "??", "*"))
				if len(objects) == 0 {
					t.Fatal("no staging objects")
				}
				path = objects[0]
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				link := os.Symlink
				if mode == "hard-linked object" {
					link = os.Link
				}
				if err := link(owner, path); err != nil {
					t.Fatal(err)
				}
			case "symlinked directory":
				path = filepath.Join(staging[0], ".git", "objects", "info")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(owner), path); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(path)
			allowed := privateRootStagingGitPaths.MatchString(strings.TrimPrefix(path, filepath.Join(staging[0], ".git")+"/"))
			if err != nil || allowed == (mode == "extra file") {
				t.Fatalf("fixture path %s does not test what %q names: %v", path, mode, err)
			}
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			after, lerr := os.Lstat(path)
			if err == nil || report.Projects[0].State != "Failed" || exists(privateRootPath(s)) || lerr != nil || !os.SameFile(before, after) || after.Mode().Type() != before.Mode().Type() || string(mustRead(t, owner)) != "owner notes\n" {
				t.Fatalf("changed staging was discarded or adopted: %v %+v", err, report)
			}
		})
	}
}

// ctxRunner hands the caller's context to the real runner, so a refresh whose
// time budget ends stops the Git process it is running.
type ctxRunner struct {
	Runner
	run func(ctx context.Context, c Cmd) (Result, error)
}

func (r ctxRunner) Run(ctx context.Context, c Cmd) (Result, error) { return r.run(ctx, c) }

func twoRepositoryRootDeclaration() string {
	declaration := strings.Replace(projectTestCatalog, `"defaultBranch":"main"}},`, `"defaultBranch":"main"},"extra":{"github":"fixture/extra"}},`, 1)
	return strings.Replace(declaration, `[{"name":"demo"}]`, `[{"name":"demo"},{"name":"extra"}]`, 1)
}

// stopInsideExtraCheckout runs a refresh whose context ends while the extra
// anchor's fast-forward is writing its checkout: a smudge filter holds Git
// there, after it has removed the file it rewrites. That is the stop the step
// reserve keeps the budget from making, and a hard kill can still make.
func stopInsideExtraCheckout(t *testing.T, r Runner, s Settings, source *ProjectCatalogSource, extra string) {
	t.Helper()
	started := filepath.Join(t.TempDir(), "checkout-started")
	attributes := filepath.Join(t.TempDir(), "attributes")
	writeFile(t, attributes, "* filter=slow\n")
	runner := ctxRunner{Runner: r, run: func(ctx context.Context, c Cmd) (Result, error) {
		if len(c.Args) > 1 && c.Args[1] == extra && slices.Contains(c.Args, "merge") {
			c.Args = append([]string{"-c", "core.attributesFile=" + attributes, "-c", "filter.slow.smudge=touch '" + started + "'; sleep 60; cat"}, c.Args...)
		}
		return r.Run(ctx, c)
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var report PrivateProjectReport
	var err error
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		report, err = RefreshPrivateProjects(ctx, runner, s, source)
	}()
	awaitFixtureFile(t, started, finished)
	cancel()
	<-finished
	if err == nil || len(report.Projects) != 1 || report.Projects[0].State != "Failed" {
		t.Fatalf("the stopped refresh did not fail: %v %+v", err, report)
	}
}

// DESIGN-007's repair of a preserved root, step by step: a leftover lock
// alone, and a half-written anchor that needs the root rebuilt.
func TestPrivateRootsRepairAPreservedRoot(t *testing.T) {
	for _, mode := range []string{"leftover lock", "half-written anchor"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, twoRepositoryRootDeclaration())
			prior := requirePrivateRootReady(t, s, r, source)
			root := privateRootPath(s)
			demo, extra := filepath.Join(root, "demo"), filepath.Join(root, "extra")
			lock := gitRun(t, g.env, extra, "rev-parse", "--path-format=absolute", "--git-path", "index.lock")
			target := pushBootstrapChange(t, g)
			switch mode {
			case "leftover lock":
				// What a hard kill leaves when it stops Git holding the lock.
				if _, err := RefreshPrivateProjects(context.Background(), failingMerge(r, extra), s, source); err == nil {
					t.Fatal("fixture did not stop part-way")
				}
				writeFile(t, lock, "")
			case "half-written anchor":
				stopInsideExtraCheckout(t, r, s, source, extra)
				if exists(lock) {
					t.Fatal("Git stopped by SIGTERM left its index.lock")
				}
				if exists(filepath.Join(extra, "README.md")) {
					t.Fatal("the fixture did not stop inside the checkout")
				}
			}
			for range 2 {
				report, err := RefreshPrivateProjects(context.Background(), r, s, source)
				if err == nil || report.Projects[0].State != "Failed" || gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != prior.Repositories[1].Target || !exists(privateRootPendingPath(s)) {
					t.Fatalf("an unprovable root was not preserved: %v %+v", err, report)
				}
			}
			switch mode {
			case "leftover lock":
				// Step 3: no Git process runs there; remove the lock and refresh.
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				updated := requirePrivateRootReady(t, s, r, source)
				if updated.ID != prior.ID || gitRun(t, g.env, extra, "rev-parse", "HEAD") != target || exists(privateRootPendingPath(s)) {
					t.Fatalf("lock repair did not resume the advance: %+v", updated)
				}
			case "half-written anchor":
				// Step 4: move the root aside, repair and prune each
				// reference's worktrees, remove the receipt and pending
				// record, and refresh.
				old := root + ".preserved-test"
				if err := os.Rename(root, old); err != nil {
					t.Fatal(err)
				}
				for _, repo := range []string{"demo", "extra"} {
					gitRun(t, g.env, s.ClonePath(repo), "worktree", "repair", filepath.Join(old, repo))
					gitRun(t, g.env, s.ClonePath(repo), "worktree", "prune")
				}
				for _, path := range []string{privateRootReceiptPath(s), privateRootPendingPath(s)} {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				}
				updated := requirePrivateRootReady(t, s, r, source)
				if updated.ID == prior.ID || gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != target || exists(privateRootPendingPath(s)) {
					t.Fatalf("rebuilt root is not Ready at the target: %+v", updated)
				}
				// The old root keeps its files, and its anchors stay worktrees
				// for inspection until the operator removes them.
				if gitRun(t, g.env, filepath.Join(old, "extra"), "rev-parse", "HEAD") != prior.Repositories[1].Target || gitRun(t, g.env, filepath.Join(old, "extra"), "status", "--porcelain") != "D README.md" {
					t.Fatal("the preserved root lost its state")
				}
				for _, repo := range []string{"demo", "extra"} {
					gitRun(t, g.env, s.ClonePath(repo), "worktree", "remove", "--force", filepath.Join(old, repo))
				}
				requirePrivateRootReady(t, s, r, source)
			}
		})
	}
}

// A blob:none fast-forward fetches the blobs it writes from inside its
// checkout. prefetchPrivateAnchor fetches them first, so the fast-forward
// itself needs no network: with every fetch failing, it still succeeds.
func TestPrivateAnchorPrefetchMakesTheFastForwardLocal(t *testing.T) {
	g := newGitFixture(t, "demo")
	writeFile(t, filepath.Join(g.seed, "old.txt"), "removed by the target\n")
	gitRun(t, g.env, g.seed, "add", "old.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "old")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	head := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	clones := map[string]string{}
	for _, name := range []string{"control", "prefetched"} {
		clones[name] = filepath.Join(g.root, name)
		gitRun(t, g.env, g.root, "clone", "-q", "--filter=blob:none", "file://"+g.remote, clones[name])
	}
	writeFile(t, filepath.Join(g.seed, "README.md"), "changed by the target\n")
	writeFile(t, filepath.Join(g.seed, "new.txt"), "added by the target\n")
	gitRun(t, g.env, g.seed, "rm", "-q", "old.txt")
	gitRun(t, g.env, g.seed, "add", "README.md", "new.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "target")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	target := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	s := testSettings(t, filepath.Join(g.root, "session-home"))
	for name, clone := range clones {
		gitRun(t, g.env, clone, "fetch", "-q", "origin")
		if name == "prefetched" {
			if err := prefetchPrivateAnchor(context.Background(), ExecRunner{BaseEnv: g.env}, s, clone, head, target); err != nil {
				t.Fatal(err)
			}
		}
		gitRun(t, g.env, clone, "config", "remote.origin.uploadpack", "false")
		cmd := exec.Command("git", "-C", clone, "merge", "--ff-only", "-q", target)
		cmd.Env = g.env
		out, err := cmd.CombinedOutput()
		switch {
		case name == "control" && err == nil:
			t.Fatal("the control fast-forward did not need the network; the fixture proves nothing")
		case name == "prefetched" && err != nil:
			t.Fatalf("the prefetched fast-forward still needed the network: %v\n%s", err, out)
		}
	}
	if gitRun(t, g.env, clones["prefetched"], "rev-parse", "HEAD") != target || gitRun(t, g.env, clones["prefetched"], "status", "--porcelain") != "" {
		t.Fatal("the prefetched fast-forward did not reach a clean target")
	}
}

// A mutating step starts only with privateRootStepReserve of the budget left.
// When the budget runs short after the first anchor moved, the second anchor's
// fast-forward never starts, and the next refresh resumes the advance.
func TestPrivateRootsStartNoMutatingStepWithoutTheReserve(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, twoRepositoryRootDeclaration())
	prior := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	demo, extra := filepath.Join(root, "demo"), filepath.Join(root, "extra")
	target := pushBootstrapChange(t, g)
	reserve := privateRootStepReserve
	t.Cleanup(func() { privateRootStepReserve = reserve })
	extraMerged, prefetched := false, map[string]bool{}
	runner := ctxRunner{Runner: r, run: func(ctx context.Context, c Cmd) (Result, error) {
		isMerge := len(c.Args) > 1 && slices.Contains(c.Args, "merge") && slices.Contains(c.Args, "--ff-only")
		if isMerge && c.Args[1] == extra {
			extraMerged = true
		}
		if len(c.Args) > 1 && slices.Contains(c.Args, "diff") && slices.Contains(c.Args, "--shortstat") && slices.Contains(c.Args, target) {
			prefetched[c.Args[1]] = true
		}
		if isMerge && c.Args[1] == demo && (!prefetched[demo] || !prefetched[extra]) {
			t.Error("an anchor moved before every checkout's blobs were fetched")
		}
		result, err := r.Run(ctx, c)
		if err == nil && isMerge && c.Args[1] == demo {
			// The rest of the budget is now shorter than the reserve.
			privateRootStepReserve = 24 * time.Hour
		}
		return result, err
	}}
	report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
	if err == nil || report.Projects[0].State != "Failed" || extraMerged || gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != prior.Repositories[1].Target || !exists(privateRootPendingPath(s)) {
		t.Fatalf("a mutating step started without the reserve: merged=%v %v %+v", extraMerged, err, report)
	}
	if got := report.Projects[0]; got.Reason != protocol.ReasonBudgetReserve {
		t.Fatalf("reserve refusal reported as %+v", got)
	}
	privateRootStepReserve = reserve
	updated := requirePrivateRootReady(t, s, r, source)
	if gitRun(t, g.env, extra, "rev-parse", "HEAD") != target || updated.Repositories[1].Target != target || exists(privateRootPendingPath(s)) {
		t.Fatalf("advance stopped for the reserve was not resumed: %+v", updated)
	}
}

// Every anchor's fast-forward is proved before the first change: a target
// that does not descend from the second anchor's commit refuses the refresh
// with the first anchor, the root and the records untouched.
func TestPrivateRootsProveEveryFastForwardBeforeTheFirstChange(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, twoRepositoryRootDeclaration())
	prior := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	demo, extra := filepath.Join(root, "demo"), filepath.Join(root, "extra")
	rootHead := gitRun(t, g.env, root, "rev-parse", "HEAD")
	pushBootstrapChange(t, g)
	runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if len(c.Args) > 1 && c.Args[1] == extra && slices.Contains(c.Args, "merge-base") && slices.Contains(c.Args, "--is-ancestor") {
			return Result{ExitCode: 1}, &CmdError{Name: "git", Sub: "merge-base", ExitCode: 1}
		}
		return r.Run(context.Background(), c)
	}}
	report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
	if err == nil || report.Projects[0].State != "Failed" {
		t.Fatalf("a refused fast-forward was accepted: %v %+v", err, report)
	}
	if got := report.Projects[0]; got.Reason != protocol.ReasonAnchorNotFastForward || got.Repository != "extra" {
		t.Fatalf("refused fast-forward reported as %+v", got)
	}
	if gitRun(t, g.env, demo, "rev-parse", "HEAD") != prior.Repositories[0].Target || gitRun(t, g.env, extra, "rev-parse", "HEAD") != prior.Repositories[1].Target || gitRun(t, g.env, root, "rev-parse", "HEAD") != rootHead || exists(privateRootPendingPath(s)) {
		t.Fatal("a refused fast-forward changed the root before its proof")
	}
	var receipt privateRootReceipt
	if readWorkspaceJSONBounded(privateRootReceiptPath(s), &receipt, privateRootReceiptLimit) != nil || receipt.Repositories[0].Target != prior.Repositories[0].Target {
		t.Fatal("the receipt changed")
	}
}
