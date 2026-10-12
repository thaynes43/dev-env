package agentd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// trackedSymlinks is the shape of the Q-30 canary's repository, widened: a
// relative link, a dangling link, a link inside a directory, a link to a
// directory and an absolute dangling link. None of them may be followed.
var trackedSymlinks = map[string]string{
	"link":     "README.md",
	"dangling": "missing/target",
	"sub/up":   "../README.md",
	"dirlink":  "sub",
	"abs":      "/nonexistent/dev-env/absolute-target",
}

func addTrackedSymlinks(t *testing.T, dir string, links map[string]string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "sub", "file.txt"), "inside\n")
	for link, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, link)); err != nil {
			t.Fatal(err)
		}
	}
}

// symlinkProofRepo is a plain repository in the given object format whose HEAD
// tracks regular, executable and symlink entries, and a clean checkout of it.
func symlinkProofRepo(t *testing.T, format string) (gitFixture, Settings, Runner, string) {
	t.Helper()
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	dir := filepath.Join(g.root, "proof-"+format)
	gitRun(t, g.env, g.root, "init", "-q", "-b", "main", "--object-format="+format, dir)
	writeFile(t, filepath.Join(dir, "README.md"), "hello\n")
	writeFile(t, filepath.Join(dir, "run.sh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{}
	for link, target := range trackedSymlinks {
		links[link] = target
	}
	// A FIFO outside the checkout: following this link would block the proof.
	fifo := filepath.Join(g.root, "outside-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	links["fifo"] = fifo
	addTrackedSymlinks(t, dir, links)
	gitRun(t, g.env, dir, "add", "-A")
	gitRun(t, g.env, dir, "commit", "-qm", "tracked symlinks")
	if staged := gitRun(t, g.env, dir, "ls-files", "--stage"); strings.Count(staged, "120000 ") != len(links) || !strings.Contains(staged, "100755 ") {
		t.Fatalf("fixture lacks tracked symlinks or an executable:\n%s", staged)
	}
	return g, s, r, dir
}

// proveWithin runs the index proof and fails instead of hanging when the proof
// follows the FIFO link.
func proveWithin(t *testing.T, s Settings, r Runner, dir string, readOnly bool) (string, error) {
	t.Helper()
	type outcome struct {
		tree string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		tree, err := projectIndexProof(context.Background(), r, s, dir, readOnly)
		done <- outcome{tree, err}
	}()
	select {
	case got := <-done:
		return got.tree, got.err
	case <-time.After(20 * time.Second):
		// Release an accidental FIFO reader before failing.
		if fifo, err := os.Readlink(filepath.Join(dir, "fifo")); err == nil {
			if writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				_ = writer.Close()
			}
		}
		t.Fatal("index proof followed a tracked symlink")
		return "", nil
	}
}

func TestProjectIndexProofAcceptsTrackedSymlinks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			g, s, r, dir := symlinkProofRepo(t, format)
			want := gitRun(t, g.env, dir, "rev-parse", "HEAD^{tree}")
			for _, readOnly := range []bool{true, false} {
				tree, err := proveWithin(t, s, r, dir, readOnly)
				if err != nil || tree != want {
					t.Fatalf("clean checkout with tracked symlinks refused (read-only %v): %q %v", readOnly, tree, err)
				}
			}
			if target, err := os.Readlink(filepath.Join(dir, "link")); err != nil || target != "README.md" {
				t.Fatal("proof changed a tracked symlink")
			}
		})
	}
}

func TestProjectIndexProofRefusesChangedSymlinksAndSpecialEntries(t *testing.T) {
	for _, mode := range []string{"retargeted", "file with the target text", "directory", "missing", "symlink parent", "file replaced by symlink", "staged retarget", "submodule", "conflict"} {
		for _, format := range []string{"sha1", "sha256"} {
			t.Run(mode+"/"+format, func(t *testing.T) {
				g, s, r, dir := symlinkProofRepo(t, format)
				link := filepath.Join(dir, "link")
				switch mode {
				case "retargeted":
					replaceWithSymlink(t, link, "run.sh")
				case "file with the target text":
					// What a core.symlinks=false checkout writes: Git hashes it to
					// the same blob id, but the entry is not a symlink.
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(link, []byte("README.md"), 0o644); err != nil {
						t.Fatal(err)
					}
				case "directory":
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(link, 0o755); err != nil {
						t.Fatal(err)
					}
				case "missing":
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
				case "symlink parent":
					// sub/up is reached through a symlinked directory whose
					// link has the same target text as the tracked one.
					outside := filepath.Join(g.root, "outside-sub")
					if err := os.Rename(filepath.Join(dir, "sub"), outside); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, filepath.Join(dir, "sub")); err != nil {
						t.Fatal(err)
					}
				case "file replaced by symlink":
					outside := filepath.Join(g.root, "outside-readme")
					writeFile(t, outside, "hello\n")
					replaceWithSymlink(t, filepath.Join(dir, "README.md"), outside)
				case "staged retarget":
					replaceWithSymlink(t, link, "run.sh")
					gitRun(t, g.env, dir, "add", "link")
				case "submodule":
					head := gitRun(t, g.env, dir, "rev-parse", "HEAD")
					gitRun(t, g.env, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
					gitRun(t, g.env, dir, "commit", "-qm", "submodule")
					if err := os.Mkdir(filepath.Join(dir, "module"), 0o755); err != nil {
						t.Fatal(err)
					}
				case "conflict":
					gitRun(t, g.env, dir, "checkout", "-qb", "other")
					replaceWithSymlink(t, link, "run.sh")
					gitRun(t, g.env, dir, "commit", "-qam", "other target")
					gitRun(t, g.env, dir, "checkout", "-q", "main")
					replaceWithSymlink(t, link, "sub")
					gitRun(t, g.env, dir, "commit", "-qam", "main target")
					cmd := gitCommand(g.env, dir, "merge", "-q", "other")
					if err := cmd.Run(); err == nil {
						t.Fatal("fixture merge did not conflict")
					}
					// Clear the operation marker so only the conflict entry refuses.
					if err := os.Remove(filepath.Join(dir, ".git", "MERGE_HEAD")); err != nil {
						t.Fatal(err)
					}
				}
				for _, readOnly := range []bool{true, false} {
					if mode == "staged retarget" && !readOnly {
						// A shared caller proves the staged tree with write-tree and
						// compares it with history itself.
						continue
					}
					if tree, err := proveWithin(t, s, r, dir, readOnly); err == nil {
						t.Fatalf("%s accepted (read-only %v): %s", mode, readOnly, tree)
					}
				}
			})
		}
	}
}

// Git itself reports a symlinked directory inside the checkout as untracked,
// so reach a symlink-only checkout through an aliased parent to prove the
// parent-component check on its own.
func TestProjectIndexProofRefusesSymlinkThroughAliasedParent(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	dir := filepath.Join(g.root, "links-only")
	gitRun(t, g.env, g.root, "init", "-q", "-b", "main", dir)
	if err := os.Symlink("missing/target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, g.env, dir, "add", "link")
	gitRun(t, g.env, dir, "commit", "-qm", "only a symlink")
	if tree, err := proveWithin(t, s, r, dir, true); err != nil || tree != gitRun(t, g.env, dir, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("symlink-only checkout refused: %v", err)
	}
	alias := filepath.Join(g.root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := proveWithin(t, s, r, alias, true); err == nil || !strings.Contains(err.Error(), "symlink components") {
		t.Fatalf("symlink reached through an aliased parent was not refused by the parent check: %v", err)
	}
}

func gitCommand(env []string, dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	return cmd
}

func replaceWithSymlink(t *testing.T, path, target string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func TestGitBlobObjectIDMatchesGit(t *testing.T) {
	g := newGitFixture(t, "demo")
	for _, format := range []string{"sha1", "sha256"} {
		dir := filepath.Join(g.root, "hash-"+format)
		gitRun(t, g.env, g.root, "init", "-q", "--object-format="+format, dir)
		for _, content := range []string{"", "README.md", "../a b/\xff\n", strings.Repeat("x", 5000)} {
			cmd := gitCommand(g.env, dir, "hash-object", "--stdin")
			cmd.Stdin = strings.NewReader(content)
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			if id, err := gitBlobObjectID(format, []byte(content)); err != nil || id != strings.TrimSpace(string(out)) {
				t.Fatalf("%s blob id of %q: %q %v, Git says %s", format, content, id, err, out)
			}
		}
	}
	if _, err := gitBlobObjectID("sha512", []byte("x")); err == nil {
		t.Fatal("unknown object format accepted")
	}
}

// pushTrackedSymlinks commits the tracked symlinks to the fixture origin, which
// serves every catalog repository.
func pushTrackedSymlinks(t *testing.T, g gitFixture) string {
	t.Helper()
	addTrackedSymlinks(t, g.seed, trackedSymlinks)
	gitRun(t, g.env, g.seed, "add", "-A")
	gitRun(t, g.env, g.seed, "commit", "-qm", "tracked symlinks")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	return gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
}

const twoRepoProjectCatalog = `{"version":1,"repositories":{"demo":{"github":"fixture/demo","defaultBranch":"main"},"extra":{"github":"fixture/extra","defaultBranch":"main"}},"projects":{"sample":{"repositories":[{"name":"demo"},{"name":"extra"}],"rules":"PROJECT_RULE_IDENTIFIER\nExact rules."}}}`

func requireTrackedLinks(t *testing.T, dir string) {
	t.Helper()
	for link, target := range trackedSymlinks {
		if got, err := os.Readlink(filepath.Join(dir, link)); err != nil || got != target {
			t.Fatalf("%s/%s is not the tracked symlink: %q %v", dir, link, got, err)
		}
	}
}

func TestPrivateRootsAcceptTrackedSymlinks(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, twoRepoProjectCatalog)
	target := pushTrackedSymlinks(t, g)
	first := requirePrivateRootReady(t, s, r, source)
	root := privateRootPath(s)
	for _, repo := range []string{"demo", "extra"} {
		requireTrackedLinks(t, s.ClonePath(repo))
		requireTrackedLinks(t, filepath.Join(root, repo))
		if gitRun(t, g.env, filepath.Join(root, repo), "rev-parse", "HEAD") != target {
			t.Fatalf("%s anchor is not at the fetched target", repo)
		}
	}
	// A remote retarget fast-forwards the references and the anchors.
	replaceWithSymlink(t, filepath.Join(g.seed, "link"), "sub/file.txt")
	gitRun(t, g.env, g.seed, "commit", "-qam", "retarget")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	target = gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	updated := requirePrivateRootReady(t, s, r, source)
	if updated.ID != first.ID || updated.Anchors["demo"] != first.Anchors["demo"] || updated.Repositories[1].Target != target {
		t.Fatalf("symlink retarget did not advance the owned root: %+v", updated)
	}
	for _, dir := range []string{s.ClonePath("demo"), filepath.Join(root, "extra")} {
		if got, err := os.Readlink(filepath.Join(dir, "link")); err != nil || got != "sub/file.txt" || gitRun(t, g.env, dir, "rev-parse", "HEAD") != target {
			t.Fatalf("%s did not fast-forward the symlink: %q %v", dir, got, err)
		}
	}
}

func TestPrivateRootsPreserveChangedTrackedSymlinks(t *testing.T) {
	for _, mode := range []string{"anchor retargeted", "anchor file with the target text", "reference retargeted", "reference file with the target text"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			pushTrackedSymlinks(t, g)
			requirePrivateRootReady(t, s, r, source)
			dir := filepath.Join(privateRootPath(s), "demo")
			if strings.HasPrefix(mode, "reference") {
				dir = s.ClonePath("demo")
			}
			link := filepath.Join(dir, "link")
			if strings.HasSuffix(mode, "retargeted") {
				replaceWithSymlink(t, link, "sub/file.txt")
			} else {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				writeFile(t, link, "README.md")
			}
			head := gitRun(t, g.env, dir, "rev-parse", "HEAD")
			pushBootstrapChange(t, g)
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != "Partial" {
				t.Fatalf("changed tracked symlink admitted: %v %+v", err, report)
			}
			if strings.HasPrefix(mode, "reference") && (report.Repositories[0].State != "Preserved" || report.Projects[0].State != "Ready") ||
				strings.HasPrefix(mode, "anchor") && (report.Repositories[0].State != "Ready" || report.Projects[0].State != "Failed") {
				t.Fatalf("wrong component preserved: %+v", report)
			}
			if gitRun(t, g.env, dir, "rev-parse", "HEAD") != head {
				t.Fatal("preserved checkout moved")
			}
			fi, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasSuffix(mode, "retargeted") {
				if got, _ := os.Readlink(link); got != "sub/file.txt" {
					t.Fatal("retargeted symlink changed")
				}
			} else if !fi.Mode().IsRegular() || !bytes.Equal(mustRead(t, link), []byte("README.md")) {
				t.Fatal("owner file changed")
			}
		})
	}
}

func TestPrivateCatalogBootstrapStillRefusesSubmodules(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	head := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	gitRun(t, g.env, g.seed, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	gitRun(t, g.env, g.seed, "commit", "-qm", "submodule")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.State != "Partial" || report.Repositories[0].State != "Preserved" || report.Projects[0].State != "Failed" {
		t.Fatalf("submodule admitted: %v %+v", err, report)
	}
}

// refuseTrackedSymlinks reproduces the 2.24.1 proof: any index listing a
// symlink is refused, at the point the old mode check refused it.
func refuseTrackedSymlinks(r Runner) Runner {
	return &fakeRunner{handle: func(c Cmd) (Result, error) {
		result, err := r.Run(context.Background(), c)
		if err == nil && slices.Contains(c.Args, "ls-files") && slices.Contains(c.Args, "--stage") && bytes.Contains(result.Stdout, []byte("120000 ")) {
			return Result{}, &CmdError{Name: "git", Sub: "ls-files", ExitCode: 1}
		}
		return result, err
	}}
}

func TestPrivateRootsResumeAnchorLeftBySymlinkRefusal(t *testing.T) {
	for _, mode := range []string{"resumes", "remote moved", "anchor retargeted", "anchor HEAD moved", "anchor dirty"} {
		t.Run(mode, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, twoRepoProjectCatalog)
			target := pushTrackedSymlinks(t, g)
			root := privateRootPath(s)
			demo, extra := filepath.Join(root, "demo"), filepath.Join(root, "extra")
			report, err := RefreshPrivateProjects(context.Background(), refuseTrackedSymlinks(r), s, source)
			var pending privateRootPending
			if err == nil || report.State != "Partial" || report.Repositories[0].State != "Preserved" || report.Repositories[1].State != "Preserved" || report.Projects[0].State != "Failed" ||
				readWorkspaceJSONBounded(privateRootPendingPath(s), &pending, privateRootPendingLimit) != nil || pending.From.Head != "" || pending.To.Anchors["demo"].Identity != nil ||
				gitRun(t, g.env, demo, "rev-parse", "HEAD") != target || exists(extra) || exists(privateRootReceiptPath(s)) {
				t.Fatalf("fixture did not leave the 2.24.1 state: %v %+v", err, report)
			}
			switch mode {
			case "remote moved":
				target = pushBootstrapChange(t, g)
			case "anchor retargeted":
				replaceWithSymlink(t, filepath.Join(demo, "link"), "sub/file.txt")
			case "anchor HEAD moved":
				gitRun(t, g.env, demo, "checkout", "-q", "--detach", "HEAD~1")
			case "anchor dirty":
				writeFile(t, filepath.Join(demo, "README.md"), "owner work\n")
			}
			if mode == "resumes" || mode == "remote moved" {
				updated := requirePrivateRootReady(t, s, r, source)
				if updated.ID != pending.ID || updated.Anchors["demo"].GitDir == "" || updated.Anchors["extra"].GitDir == "" || exists(privateRootPendingPath(s)) {
					t.Fatalf("unrecorded anchor was not resumed: %+v", updated)
				}
				for _, dir := range []string{demo, extra, s.ClonePath("demo"), s.ClonePath("extra")} {
					requireTrackedLinks(t, dir)
					if gitRun(t, g.env, dir, "rev-parse", "HEAD") != target {
						t.Fatalf("%s is not at the fresh target", dir)
					}
				}
				return
			}
			head := gitRun(t, g.env, demo, "rev-parse", "HEAD")
			report, err = RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != "Partial" || report.Projects[0].State != "Failed" || report.Repositories[0].State != "Ready" {
				t.Fatalf("changed unrecorded anchor admitted: %v %+v", err, report)
			}
			if gitRun(t, g.env, demo, "rev-parse", "HEAD") != head || exists(extra) || !exists(privateRootPendingPath(s)) || exists(privateRootReceiptPath(s)) {
				t.Fatal("changed unrecorded anchor was adopted or moved")
			}
			switch mode {
			case "anchor retargeted":
				if got, _ := os.Readlink(filepath.Join(demo, "link")); got != "sub/file.txt" {
					t.Fatal("owner symlink changed")
				}
			case "anchor dirty":
				if string(mustRead(t, filepath.Join(demo, "README.md"))) != "owner work\n" {
					t.Fatal("owner file changed")
				}
			}
		})
	}
}

// The shared-workspace sync uses the same proof for its reference repair and
// its anchor reset.
func TestProjectSyncAcceptsTrackedSymlinks(t *testing.T) {
	g, s, r, c := projectFixture(t)
	pushTrackedSymlinks(t, g)
	assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
	anchor := filepath.Join(s.Home, "codex", "sample", "demo")
	requireTrackedLinks(t, anchor)
	target := pushBootstrapChange(t, g)
	assertNoPreservedProject(t, syncProjectFixture(t, s, r, c))
	for _, dir := range []string{anchor, s.ClonePath("demo")} {
		requireTrackedLinks(t, dir)
		if gitRun(t, g.env, dir, "rev-parse", "HEAD") != target {
			t.Fatalf("%s is not at the fresh target", dir)
		}
	}
	replaceWithSymlink(t, filepath.Join(anchor, "link"), "sub/file.txt")
	writeFile(t, filepath.Join(g.seed, "README.md"), "later remote content\n")
	gitRun(t, g.env, g.seed, "commit", "-qam", "later remote")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	report := syncProjectFixture(t, s, r, c)
	if !slices.ContainsFunc(report.Findings, func(f ProjectFinding) bool { return f.State == "preserved" }) {
		t.Fatalf("retargeted anchor symlink admitted: %+v", report)
	}
	if got, _ := os.Readlink(filepath.Join(anchor, "link")); got != "sub/file.txt" || gitRun(t, g.env, anchor, "rev-parse", "HEAD") != target {
		t.Fatal("preserved anchor changed")
	}
}
