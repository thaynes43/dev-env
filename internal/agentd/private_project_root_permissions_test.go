package agentd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestPrivateRootsWrapperModesUnderPrivateUmask(t *testing.T) {
	// Umask is process-wide; this package does not run tests in parallel.
	priorMask := syscall.Umask(0o077)
	defer syscall.Umask(priorMask)
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	first := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	wrappers := []string{filepath.Join(root, "AGENTS.md"), filepath.Join(root, "CLAUDE.md")}
	beforeFiles := map[string]os.FileInfo{}
	beforeBytes := map[string][]byte{}
	for _, path := range wrappers {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode().Perm() != 0o644 {
			t.Fatalf("owned wrapper lacks declared mode: %s %v", path, err)
		}
		beforeFiles[path], beforeBytes[path] = fi, mustRead(t, path)
	}
	for _, dir := range []string{s.Home, s.StateDir, filepath.Join(s.Home, "codex"), root} {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
			t.Fatalf("private directory permissions changed: %s %v", dir, err)
		}
	}
	rootIndex, rootHead := mustRead(t, filepath.Join(first.GitDir, "index")), mustRead(t, filepath.Join(first.GitDir, "HEAD"))
	unchanged := requirePrivateRootReady(t, s, r, source)
	if unchanged.Head != first.Head || !bytes.Equal(rootIndex, mustRead(t, filepath.Join(first.GitDir, "index"))) || !bytes.Equal(rootHead, mustRead(t, filepath.Join(first.GitDir, "HEAD"))) {
		t.Fatal("unchanged refresh modified owned Git state")
	}
	for _, path := range wrappers {
		fi, err := os.Lstat(path)
		if err != nil || fi.Mode().Perm() != 0o644 || !os.SameFile(beforeFiles[path], fi) || !fi.ModTime().Equal(beforeFiles[path].ModTime()) || !bytes.Equal(beforeBytes[path], mustRead(t, path)) {
			t.Fatal("unchanged refresh rewrote an owned wrapper")
		}
	}
	peer := filepath.Join(s.Home, "peer")
	gitRun(t, g.env, s.ClonePath("demo"), "worktree", "add", "-q", "--detach", peer, first.Repositories[0].Target)
	writeFile(t, filepath.Join(peer, "README.md"), "peer staged work\n")
	gitRun(t, g.env, peer, "add", "README.md")
	peerGit := gitRun(t, g.env, peer, "rev-parse", "--absolute-git-dir")
	peerIndex, peerHead := mustRead(t, filepath.Join(peerGit, "index")), mustRead(t, filepath.Join(peerGit, "HEAD"))
	pushBootstrapChange(t, g)
	changed := strings.Replace(projectTestCatalog, "Exact rules.", "Changed exact rules.", 1)
	updated := requirePrivateRootReady(t, s, r, projectSyncFixtureOptions(changed).AcceptedCatalog)
	if updated.Head == first.Head || gitRun(t, g.env, root, "rev-parse", "HEAD^") != first.Head || provePrivateRoot(context.Background(), r, s, updated, updated.Repositories, updated.Anchors) != nil {
		t.Fatal("changed wrappers did not retain a proved fast-forward root")
	}
	for _, path := range wrappers {
		if fi, err := os.Lstat(path); err != nil || fi.Mode().Perm() != 0o644 || !strings.Contains(string(mustRead(t, path)), "Changed exact rules.") {
			t.Fatal("refreshed wrapper lacks exact bytes or declared mode")
		}
	}
	if !bytes.Equal(peerIndex, mustRead(t, filepath.Join(peerGit, "index"))) || !bytes.Equal(peerHead, mustRead(t, filepath.Join(peerGit, "HEAD"))) || string(mustRead(t, filepath.Join(peer, "README.md"))) != "peer staged work\n" {
		t.Fatal("wrapper mode correction modified a peer")
	}
	if fi, err := os.Stat(filepath.Join(peer, "README.md")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("wrapper mode correction chmodded a repository file")
	}
}

func TestPrivateRootsPreserveExistingWrapperModeDrift(t *testing.T) {
	for _, changeRules := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged rules", true: "changed rules"}[changeRules], func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			receipt := requirePrivateRootReady(t, s, r, source)
			root := privateRootPath(s)
			path := filepath.Join(root, "AGENTS.md")
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if gitRun(t, g.env, root, "status", "--porcelain") != "" {
				t.Fatal("mode-only fixture should remain Git clean")
			}
			before := map[string][]byte{}
			for _, p := range []string{path, filepath.Join(root, "CLAUDE.md"), filepath.Join(receipt.GitDir, "HEAD"), filepath.Join(receipt.GitDir, "index"), privateRootReceiptPath(s), filepath.Join(receipt.Anchors["demo"].GitDir, "HEAD"), filepath.Join(receipt.Anchors["demo"].GitDir, "index")} {
				before[p] = mustRead(t, p)
			}
			if changeRules {
				source = projectSyncFixtureOptions(strings.Replace(projectTestCatalog, "Exact rules.", "Changed exact rules.", 1)).AcceptedCatalog
			}
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != "Partial" || report.Projects[0].State != "Failed" || gitRun(t, g.env, root, "rev-parse", "HEAD") != receipt.Head {
				t.Fatalf("existing mode drift was adopted: %v %+v", err, report)
			}
			for p, data := range before {
				if !bytes.Equal(data, mustRead(t, p)) {
					t.Fatalf("existing mode drift changed %s", p)
				}
			}
			if fi, err := os.Lstat(path); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatal("existing mode drift was chmodded")
			}
		})
	}
}

func TestPrivateRootsRefuseUnconfirmedGeneratedWrapperModes(t *testing.T) {
	for _, mode := range []string{"edited", "symlink", "hardlink", "authority drift", "edited during authority"} {
		t.Run(mode, func(t *testing.T) {
			priorMask := syscall.Umask(0o077)
			defer syscall.Umask(priorMask)
			_, s, r, source := privateRootsFixture(t, projectTestCatalog)
			catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _ := catalog.ProjectSnapshot("sample")
			foreign := filepath.Join(s.Home, "provider", "owner-rules")
			writeFile(t, foreign, snapshot.ProjectRules())
			staging := filepath.Join(s.Home, "codex", ".sample.agentd-root")
			path := filepath.Join(staging, "AGENTS.md")
			written, edited := false, false
			read := source.Read
			source.Read = func(ctx context.Context) (ProjectCatalogResource, error) {
				resource, err := read(ctx)
				if written && mode == "authority drift" {
					resource.ResourceVersion = "2"
				}
				if written && mode == "edited during authority" && !edited {
					writeFile(t, path, strings.Replace(snapshot.ProjectRules(), "Exact rules.", "Owner rules.", 1))
					edited = true
				}
				return resource, err
			}
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				result, err := r.Run(context.Background(), c)
				if err != nil || !slices.Contains(c.Args, "checkout") || c.Args[1] != staging {
					return result, err
				}
				written = true
				switch mode {
				case "edited":
					writeFile(t, path, strings.Replace(snapshot.ProjectRules(), "Exact rules.", "Owner rules.", 1))
				case "symlink", "hardlink":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if mode == "symlink" {
						err = os.Symlink(foreign, path)
					} else {
						err = os.Link(foreign, path)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				return result, err
			}}
			report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
			if !written || err == nil || report.State == "Ready" || report.Projects[0].State != "Failed" || exists(privateRootPath(s)) || exists(privateRootReceiptPath(s)) || !exists(staging) {
				t.Fatalf("unconfirmed generated wrapper was adopted: %v %+v", err, report)
			}
			if fi, err := os.Stat(foreign); err != nil || fi.Mode().Perm() != 0o600 || string(mustRead(t, foreign)) != snapshot.ProjectRules() {
				t.Fatal("foreign file mode or contents changed")
			}
			if fi, err := os.Lstat(filepath.Join(staging, "CLAUDE.md")); err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatal("refused generation changed the other wrapper mode")
			}
			if mode == "edited" || mode == "edited during authority" {
				if fi, err := os.Lstat(path); err != nil || fi.Mode().Perm() != 0o600 || !strings.Contains(string(mustRead(t, path)), "Owner rules.") {
					t.Fatal("edited wrapper was chmodded or replaced")
				}
			}
		})
	}
}
