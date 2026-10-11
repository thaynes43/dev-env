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
	"syscall"
	"testing"
	"time"

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
			if mode == "authority changed" && (exists(root) || !exists(filepath.Join(s.Home, "codex", ".sample.agentd-root", ".git"))) {
				t.Fatal("changed authority published or erased staging")
			}
			if mode == "plain" || mode == "foreign Git" {
				if string(mustRead(t, filepath.Join(root, "owner"))) != "owner root\n" {
					t.Fatal("foreign root overwritten")
				}
			}
			if mode == "anchor failure" {
				partialHead := gitRun(t, g.env, root, "rev-parse", "HEAD")
				retry, retryErr := RefreshPrivateProjects(context.Background(), r, s, source)
				if retryErr == nil || retry.Projects[0].State == "Ready" || exists(privateRootReceiptPath(s)) || gitRun(t, g.env, root, "rev-parse", "HEAD") != partialHead {
					t.Fatal("interrupted root silently adopted on retry")
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
		})
	}
}
