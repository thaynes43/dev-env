package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestRescueTreeCaptureDoesNotFailOrStoreTheBundle(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "README.md"), "edited\n")
	writeFile(t, filepath.Join(wt, "kept.txt"), "staged\n")
	gitRun(t, env, wt, "add", "kept.txt")
	writeFile(t, filepath.Join(wt, "staged.txt"), "index-only\n")
	gitRun(t, env, wt, "add", "staged.txt")
	writeFile(t, filepath.Join(wt, "staged.txt"), "worktree-wins\n")
	writeFile(t, filepath.Join(wt, "scratch", "new.sh"), "echo\n")
	if err := os.Chmod(filepath.Join(wt, "scratch", "new.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "build.log"), "SECRET-IGNORED\n")
	before := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified || rb.TreeRefused != protocol.TreeWithheld || len(rb.TreeArchives) != 0 {
		t.Fatalf("bundle %+v ok %v", rb, rep.OK)
	}
	if tars, _ := filepath.Glob(filepath.Join(rig.s.SharedDir, "rescue", rep.Session, rep.Stamp, "*.tree.tar")); len(tars) != 0 {
		t.Fatalf("stored a tree tar: %v", tars)
	}
	raw, err := os.ReadFile(filepath.Join(rig.s.SharedDir, filepath.FromSlash(rep.Bundle.Manifest)))
	if err != nil || strings.Contains(string(raw), "SECRET-IGNORED") || strings.Contains(string(raw), "worktree-wins") {
		t.Fatalf("manifest stored file bytes: %v", err)
	}
	if after := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Fatalf("status changed:\n%s", after)
	}
	sum := fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	got, err := ReadRescueTree(context.Background(), rig.r, rig.s, rep.Session+"/"+rep.Stamp, filepath.Base(rep.Repos[0].Path), rb.Refs[0].Commit)
	if err != nil || got.OK || got.Code != protocol.TreeWithheld || len(got.Files) != 0 {
		t.Fatalf("read %+v %v", got, err)
	}
	if fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))) != sum {
		t.Fatal("the read changed the bundle")
	}
}

func TestForgedTreeIsNotTheCanonicalCommit(t *testing.T) {
	rig := newRescueRig(t)
	writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "edited\n")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified {
		t.Fatalf("bundle %+v", rb)
	}
	sum := fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	dir := filepath.Join(rig.s.SharedDir, filepath.FromSlash(rep.Bundle.Dir))
	forged := []byte("FORGED-NOT-IN-COMMIT\nplanted.sh\n")
	if err := os.WriteFile(filepath.Join(dir, "forged.tree.tar"), forged, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, protocol.ManifestFile)
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest protocol.RescueManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(forged)
	manifest.Repos[0].Bundle.TreeArchives = []protocol.TreeArchive{{
		Ref: "refs/heads/rescue/forged", Commit: rb.Refs[0].Commit, File: rep.Bundle.Dir + "/forged.tree.tar",
		SHA256: hex.EncodeToString(digest[:]), Size: int64(len(forged)),
	}}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRescueTree(context.Background(), rig.r, rig.s, rep.Session+"/"+rep.Stamp, filepath.Base(rep.Repos[0].Path), rb.Refs[0].Commit)
	encoded, _ := json.Marshal(got)
	if err != nil || got.OK || len(got.Files) != 0 || strings.Contains(string(encoded), "FORGED-NOT-IN-COMMIT") {
		t.Fatalf("forged tree served %+v %v", got, err)
	}
	if fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))) != sum {
		t.Fatal("the forged manifest changed the bundle")
	}
}

func TestRescueTreeCaptureFailureKeepsTheThinBundle(t *testing.T) {
	rig := newRescueRig(t)
	writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "edited\n")
	runner := &failSubstr{base: rig.r, substr: "ls-tree"}
	rep, err := Rescue(context.Background(), runner, rig.s, "demo-1006-170000", rescueNow, RescueOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified || rb.Error != "" || rb.TreeRefused != protocol.TreeCaptureFailed || len(rb.TreeArchives) != 0 {
		t.Fatalf("report ok %v bundle %+v", rep.OK, rb)
	}
	if runner.hits == 0 {
		t.Fatal("the capture did not run")
	}
}

func TestRescueTreeOverCapIsRefusedBeforeBlobReads(t *testing.T) {
	if !encodedTreeExceedsCap(9<<20, 1) || !encodedTreeExceedsCap(16<<20, 1) {
		t.Fatal("9 MiB and 16 MiB must exceed the existing 8 MiB response cap")
	}
	if encodedTreeExceedsCap(64, 2) {
		t.Fatal("a tiny tree is inside the cap")
	}
	for _, size := range []int{9 << 20, 16 << 20} {
		rig := newRescueRig(t)
		blob := make([]byte, size)
		blob[0], blob[len(blob)-1] = 'A', 'Z'
		writeFile(t, filepath.Join(rig.ws.Worktree, "big.bin"), string(blob))
		runner := &failSubstr{base: rig.r, substr: "cat-file"}
		rep, err := Rescue(context.Background(), runner, rig.s, "demo-1006-170000", rescueNow, RescueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		rb := rep.Repos[0].Bundle
		if !rep.OK || rb == nil || !rb.Verified || rb.TreeRefused != protocol.TreeTooLarge {
			t.Fatalf("%d: bundle %+v ok %v", size, rb, rep.OK)
		}
		if runner.hits != 0 {
			t.Fatalf("%d: capture read blob bodies (%d)", size, runner.hits)
		}
		info, err := os.Stat(filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("raw %d bytes, thin bundle %d bytes, tree refused %s", size, info.Size(), rb.TreeRefused)
	}
}

func TestReplaceRefDoesNotSelectTheReplacement(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "victim.txt"), "real-victim\n")
	gitRun(t, env, wt, "add", "victim.txt")
	gitRun(t, env, wt, "commit", "-q", "-m", "victim")
	real := strings.TrimSpace(gitRun(t, env, wt, "rev-parse", "HEAD:victim.txt"))
	writeFile(t, filepath.Join(wt, "other.txt"), "REPLACED-CONTENT\n")
	other := strings.TrimSpace(gitRun(t, env, wt, "hash-object", "-w", filepath.Join(wt, "other.txt")))
	gitRun(t, env, wt, "replace", real, other)
	replaced := gitRun(t, env, wt, "cat-file", "-p", real)
	if !strings.Contains(replaced, "REPLACED-CONTENT") {
		t.Fatalf("the fixture did not install a replace ref: %q", replaced)
	}
	runner := &recordEnv{base: rig.r}
	code, err := classifyOneTree(context.Background(), runner, rig.ws.Clone, strings.TrimSpace(gitRun(t, env, wt, "rev-parse", "HEAD")))
	if err != nil || code != protocol.TreeWithheld {
		t.Fatalf("classify %s %v", code, err)
	}
	var saw bool
	for _, call := range runner.calls {
		if !strings.Contains(call.args, "ls-tree") {
			continue
		}
		saw = true
		joined := strings.Join(call.env, "\n")
		for _, want := range []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"} {
			if !strings.Contains(joined, want) {
				t.Errorf("object read missing %s", want)
			}
		}
	}
	if !saw {
		t.Fatal("no isolated object read")
	}
	res, err := rig.r.Run(context.Background(), Cmd{
		Name: "git",
		Args: []string{"-C", rig.ws.Clone, "-c", "core.hooksPath=/dev/null", "cat-file", "-p", real},
		Env:  isolatedGitEnv(), UnsetEnv: isolatedGitUnset,
	})
	if err != nil || strings.Contains(string(res.Stdout), "REPLACED-CONTENT") || !strings.Contains(string(res.Stdout), "real-victim") {
		t.Fatalf("isolated read = %q %v", res.Stdout, err)
	}
}

func TestAncestorSymlinkCannotLeaveTheSharedRoot(t *testing.T) {
	rig := newRescueRig(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("OUTSIDE-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(rig.s.SharedDir, "rescue")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "evil-1006-170000")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRescueTree(context.Background(), rig.r, rig.s, "evil-1006-170000/20261006-1730", "demo", strings.Repeat("ab", 20))
	encoded, _ := json.Marshal(got)
	if err != nil || got.OK || len(got.Files) != 0 || strings.Contains(string(encoded), "OUTSIDE-SECRET") {
		t.Fatalf("symlink read %+v %v", got, err)
	}
}

func TestRescueSymlinkRefusesTheTreeAndKeepsTheBundle(t *testing.T) {
	rig := newRescueRig(t)
	wt := rig.ws.Worktree
	if err := os.Symlink("SECRET-TARGET", filepath.Join(wt, "link")); err != nil {
		t.Fatal(err)
	}
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified || rb.TreeRefused != protocol.TreeNotRegular || len(rb.TreeArchives) != 0 {
		t.Fatalf("bundle %+v ok %v", rb, rep.OK)
	}
	raw, err := os.ReadFile(filepath.Join(rig.s.SharedDir, filepath.FromSlash(rep.Bundle.Manifest)))
	if err != nil || strings.Contains(string(raw), "SECRET-TARGET") {
		t.Fatal("the manifest contains the link target")
	}
	restored := rig.restore(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	if restored == "" {
		t.Fatal("restore")
	}
}

type failSubstr struct {
	base   Runner
	substr string
	hits   int
}

func (f *failSubstr) LookPath(name string) (string, error) { return f.base.LookPath(name) }

func (f *failSubstr) Run(ctx context.Context, c Cmd) (Result, error) {
	if strings.Contains(strings.Join(c.Args, " "), f.substr) {
		f.hits++
		return Result{}, errors.New("injected")
	}
	return f.base.Run(ctx, c)
}

type recordedCall struct {
	args string
	env  []string
}

type recordEnv struct {
	base  Runner
	calls []recordedCall
}

func (r *recordEnv) LookPath(name string) (string, error) { return r.base.LookPath(name) }

func (r *recordEnv) Run(ctx context.Context, c Cmd) (Result, error) {
	r.calls = append(r.calls, recordedCall{args: strings.Join(c.Args, " "), env: append([]string(nil), c.Env...)})
	return r.base.Run(ctx, c)
}

func TestUnsafeTreeNames(t *testing.T) {
	for _, p := range []string{".GIT/config", "dir/.git ", "git~1", "a:b", "ok/.git~1", ".git.", "dir/.Git"} {
		if protocol.AcceptableRescueTreePath(p) {
			t.Errorf("accepted %q", p)
		}
	}
	if !protocol.AcceptableRescueTreePath("src/main.go") {
		t.Error("a normal path was refused")
	}
}

func TestMaterializeRescueTreeMatchesTheWorktree(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "README.md"), "edited\n")
	writeFile(t, filepath.Join(wt, "staged.txt"), "index-only\n")
	gitRun(t, env, wt, "add", "staged.txt")
	writeFile(t, filepath.Join(wt, "staged.txt"), "worktree-wins\n")
	writeFile(t, filepath.Join(wt, "scratch", "new.sh"), "echo\n")
	if err := os.Chmod(filepath.Join(wt, "scratch", "new.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "build.log"), "SECRET-IGNORED\n")
	before := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	var commit, ref string
	for _, one := range rb.Refs {
		if protocol.RescueWorkRef(one.Name) {
			commit, ref = one.Commit, one.Name
		}
	}
	if !rep.OK || rb == nil || !rb.Verified || !protocol.ValidGitObject(rb.BaseCommit) || commit == "" {
		t.Fatalf("bundle %+v commit %s", rb, commit)
	}
	sum := fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	hook := filepath.Join(t.TempDir(), "hook-ran")
	hookDir := t.TempDir()
	writeFile(t, filepath.Join(hookDir, "post-checkout"), "#!/bin/sh\ntouch "+hook+"\n")
	if err := os.Chmod(filepath.Join(hookDir, "post-checkout"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, env, rig.ws.Clone, "config", "core.hooksPath", hookDir)
	gitRun(t, env, rig.ws.Clone, "remote", "set-url", "origin", "https://127.0.0.1:9/no.git")
	req := RescueMaterialize{ID: rep.Session + "/" + rep.Stamp, Repo: filepath.Base(rep.Repos[0].Path), Clone: rig.ws.Clone, Bundle: rb.File, Commit: commit, Base: rb.BaseCommit}
	got, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || !got.OK || got.Ref != ref || got.Commit != commit {
		t.Fatalf("materialize %+v %v", got, err)
	}
	files := map[string]protocol.RescueTreeFile{}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "SECRET-IGNORED") || strings.Contains(string(encoded), "index-only") {
		t.Fatal("the response contains ignored or staged-only bytes")
	}
	for _, f := range got.Files {
		files[f.Path] = f
		sum := sha256.Sum256(f.Bytes)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			t.Fatalf("hash %s", f.Path)
		}
	}
	if string(files["README.md"].Bytes) != "edited\n" || string(files["staged.txt"].Bytes) != "worktree-wins\n" || string(files["scratch/new.sh"].Bytes) != "echo\n" || files["scratch/new.sh"].Mode != "100755" {
		t.Fatalf("files %#v", files)
	}
	if _, err := os.Stat(hook); !os.IsNotExist(err) {
		t.Fatal("a hook ran")
	}
	if after := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Fatalf("status changed:\n%s", after)
	}
	if fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))) != sum {
		t.Fatal("the read changed the bundle")
	}
	if tars, _ := filepath.Glob(filepath.Join(rig.s.SharedDir, "rescue", rep.Session, rep.Stamp, "*.tree.tar")); len(tars) != 0 {
		t.Fatalf("stored a tree tar: %v", tars)
	}

	empty := t.TempDir()
	gitRun(t, env, empty, "init", "-q")
	missing, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, RescueMaterialize{ID: req.ID, Repo: req.Repo, Clone: empty, Bundle: rb.File, Commit: commit, Base: rb.BaseCommit})
	if err != nil || missing.OK || missing.Code != protocol.RescueTreeMissingBase || len(missing.Files) != 0 {
		t.Fatalf("missing base %+v %v", missing, err)
	}
	wrong, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, RescueMaterialize{ID: req.ID, Repo: req.Repo, Clone: rig.ws.Clone, Bundle: rb.File, Commit: rb.BaseCommit, Base: rb.BaseCommit})
	if err != nil || wrong.OK || len(wrong.Files) != 0 {
		t.Fatalf("base used as the rescue commit %+v %v", wrong, err)
	}
	bundlePath := filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))
	saved, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, []byte("FORGED-BUNDLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	forged, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	body, _ := json.Marshal(forged)
	if err != nil || forged.OK || len(forged.Files) != 0 || strings.Contains(string(body), "FORGED-BUNDLE") {
		t.Fatalf("forged bundle %+v %v", forged, err)
	}
	if err := os.WriteFile(bundlePath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	real := strings.TrimSpace(gitRun(t, env, wt, "rev-parse", commit+":README.md"))
	writeFile(t, filepath.Join(wt, "replaced-blob"), "REPLACED-CONTENT\n")
	other := strings.TrimSpace(gitRun(t, env, wt, "hash-object", "-w", filepath.Join(wt, "replaced-blob")))
	gitRun(t, env, rig.ws.Clone, "replace", real, other)
	again, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || !again.OK {
		t.Fatalf("replace %+v %v", again, err)
	}
	for _, f := range again.Files {
		if f.Path == "README.md" && string(f.Bytes) != "edited\n" {
			t.Fatalf("replace selected %q", f.Bytes)
		}
	}

	runner := &failSubstr{base: rig.r, substr: " fetch "}
	failed, err := MaterializeRescueTree(context.Background(), runner, rig.s, req)
	if err != nil || failed.OK || len(failed.Files) != 0 || runner.hits == 0 {
		t.Fatalf("injected fetch %+v hits %d %v", failed, runner.hits, err)
	}
	if fileSum(t, bundlePath) != sum {
		t.Fatal("the injected failure changed the bundle")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("OUTSIDE-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rig.s.SharedDir, "rescue", "link-1006-170000")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	swapped, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, RescueMaterialize{
		ID: "link-1006-170000/20261006-1730", Repo: req.Repo, Clone: rig.ws.Clone,
		Bundle: "rescue/link-1006-170000/20261006-1730/demo.bundle", Commit: commit, Base: rb.BaseCommit,
	})
	swappedBody, _ := json.Marshal(swapped)
	if err != nil || swapped.OK || len(swapped.Files) != 0 || strings.Contains(string(swappedBody), "OUTSIDE-SECRET") {
		t.Fatalf("symlink %+v %v", swapped, err)
	}
}

func TestMaterializeOverCapDoesNotReadBodies(t *testing.T) {
	rig := newRescueRig(t)
	blob := make([]byte, 9<<20)
	blob[0], blob[len(blob)-1] = 'A', 'Z'
	writeFile(t, filepath.Join(rig.ws.Worktree, "big.bin"), string(blob))
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	var commit string
	for _, one := range rb.Refs {
		if protocol.RescueWorkRef(one.Name) {
			commit = one.Commit
		}
	}
	if !rep.OK || rb == nil || !rb.Verified || rb.TreeRefused != protocol.TreeTooLarge || commit == "" {
		t.Fatalf("rescue %+v", rb)
	}
	runner := &failSubstr{base: rig.r, substr: "cat-file --batch"}
	got, err := MaterializeRescueTree(context.Background(), runner, rig.s, RescueMaterialize{
		ID: rep.Session + "/" + rep.Stamp, Repo: filepath.Base(rep.Repos[0].Path), Clone: rig.ws.Clone,
		Bundle: rb.File, Commit: commit, Base: rb.BaseCommit,
	})
	if err != nil || got.OK || got.Code != protocol.TreeTooLarge || len(got.Files) != 0 || runner.hits != 0 {
		t.Fatalf("over cap %+v hits %d %v", got, runner.hits, err)
	}
	info, err := os.Stat(filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("9MiB raw, thin bundle %d bytes, read code %s", info.Size(), got.Code)
}

func fileSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
