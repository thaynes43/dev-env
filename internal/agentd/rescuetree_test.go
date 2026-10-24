package agentd

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestReadRescueTreeReturnsWorktreeBytes(t *testing.T) {
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "public.go"), "public\n")
	gitRun(t, env, wt, "add", "public.go")
	gitRun(t, env, wt, "commit", "-q", "-m", "unpushed")
	agent := strings.TrimSpace(gitRun(t, env, wt, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(wt, "README.md"), "edited\n")
	writeFile(t, filepath.Join(wt, "staged.txt"), "index-only\n")
	gitRun(t, env, wt, "add", "staged.txt")
	writeFile(t, filepath.Join(wt, "staged.txt"), "worktree-wins\n")
	writeFile(t, filepath.Join(wt, "kept.txt"), "staged\n")
	gitRun(t, env, wt, "add", "kept.txt")
	writeFile(t, filepath.Join(wt, "scratch", "new.sh"), "echo\n")
	if err := os.Chmod(filepath.Join(wt, "scratch", "new.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(wt, "build.log"), "SECRET-IGNORED\n")
	before := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all")

	start := time.Now()
	rep := rig.rescue(t)
	w := rig.worktree(t, rep, wt)
	if !rep.OK || rep.Bundle == nil || w.RescueBranch == "" {
		t.Fatalf("report %+v", rep)
	}
	rb := rep.Repos[0].Bundle
	if rb == nil || !rb.Verified || rb.TreeRefused != "" || len(rb.TreeArchives) != 1 {
		t.Fatalf("bundle %+v", rb)
	}
	commit := ""
	for _, ref := range rb.Refs {
		if ref.Name == "refs/heads/"+w.RescueBranch {
			commit = ref.Commit
		}
	}
	if commit == "" || commit == agent || rb.TreeArchives[0].Commit != commit {
		t.Fatalf("rescue commit %q agent %q archive %+v", commit, agent, rb.TreeArchives[0])
	}
	repo := filepath.Base(rep.Repos[0].Path)
	id := rep.Session + "/" + rep.Stamp
	dir := filepath.Join(rig.s.SharedDir, filepath.FromSlash(rep.Bundle.Dir))
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	bundleSum := fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	tarSum := fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.TreeArchives[0].File)))

	read := func() protocol.RescueTree {
		t.Helper()
		got, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, commit)
		if err != nil || !got.OK {
			t.Fatalf("read %+v %v", got, err)
		}
		return got
	}
	got := read()
	again := read()
	var wg sync.WaitGroup
	concurrent := make([]protocol.RescueTree, 2)
	for i := range concurrent {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tree, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, commit)
			if err != nil || !tree.OK {
				t.Errorf("concurrent %+v %v", tree, err)
			}
			concurrent[i] = tree
		}(i)
	}
	wg.Wait()
	if after := gitRun(t, env, wt, "status", "--porcelain=v1", "--untracked-files=all"); after != before {
		t.Errorf("status changed:\n%s", after)
	}
	if now, err := os.Stat(dir); err != nil || !now.ModTime().Equal(info.ModTime()) {
		t.Errorf("rescue directory changed: %v", err)
	}
	if fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))) != bundleSum || fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.TreeArchives[0].File))) != tarSum {
		t.Error("the rescue files changed")
	}

	want := map[string]struct {
		body string
		mode string
	}{
		"README.md":      {"edited\n", "100644"},
		"staged.txt":     {"worktree-wins\n", "100644"},
		"kept.txt":       {"staged\n", "100644"},
		"scratch/new.sh": {"echo\n", "100755"},
		"public.go":      {"public\n", "100644"},
		".gitignore":     {"node_modules/\n*.log\n", "100644"},
	}
	seen := map[string]bool{}
	for _, f := range got.Files {
		spec, ok := want[f.Path]
		if !ok {
			if strings.Contains(string(f.Bytes), "SECRET-IGNORED") || f.Path == "build.log" {
				t.Errorf("ignored bytes returned: %s", f.Path)
			}
			continue
		}
		seen[f.Path] = true
		sum := sha256.Sum256([]byte(spec.body))
		if f.Mode != spec.mode || string(f.Bytes) != spec.body || f.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("%s mode %s hash %s", f.Path, f.Mode, f.SHA256)
		}
		if string(f.Bytes) == "index-only\n" {
			t.Errorf("%s kept the index instead of the worktree", f.Path)
		}
	}
	for path := range want {
		if !seen[path] {
			t.Errorf("missing %s", path)
		}
	}
	if !sameTree(got, again) || !sameTree(got, concurrent[0]) || !sameTree(got, concurrent[1]) {
		t.Error("repeated reads differ")
	}
	for _, wrong := range []string{agent, strings.Repeat("ab", 20)} {
		miss, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, wrong)
		if err != nil || miss.OK || miss.Code != protocol.RescueTreeMismatch || len(miss.Files) != 0 {
			t.Errorf("commit %s: %+v %v", wrong, miss, err)
		}
	}
	if miss, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, "other", commit); err != nil || miss.OK || len(miss.Files) != 0 {
		t.Errorf("other repo %+v %v", miss, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadRescueTree(ctx, rig.r, rig.s, id, repo, commit); err == nil {
		t.Fatal("a canceled read succeeded")
	}
	if retry := read(); !sameTree(got, retry) {
		t.Error("retry after a canceled read differs")
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
	manifest.Complete = false
	rewritten, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	if miss, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, commit); err != nil || miss.Code != protocol.RescueTreeIncomplete || len(miss.Files) != 0 {
		t.Errorf("incomplete %+v %v", miss, err)
	}
	manifest.Complete = true
	restored, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, restored, 0o600); err != nil {
		t.Fatal(err)
	}
	archive := &manifest.Repos[0].Bundle.TreeArchives[0]
	tarPath := filepath.Join(rig.s.SharedDir, filepath.FromSlash(archive.File))
	original, err := os.ReadFile(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	damaged := append([]byte(nil), original...)
	damaged[len(damaged)-1] ^= 0x01
	if err := os.WriteFile(tarPath, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	if miss, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, commit); err != nil || miss.Code != protocol.RescueTreeMismatch || len(miss.Files) != 0 {
		t.Errorf("tampered %+v %v", miss, err)
	}
	if fileSum(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File))) != bundleSum {
		t.Error("tampering the tar changed the bundle")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	crafted := tarWith(t, map[string]string{"../" + filepath.Base(outside): "ESCAPED\n", "ok.txt": "ok\n"})
	sum := sha256.Sum256(crafted)
	archive.SHA256 = hex.EncodeToString(sum[:])
	archive.Size = int64(len(crafted))
	if err := os.WriteFile(tarPath, crafted, 0o600); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if miss, err := ReadRescueTree(context.Background(), rig.r, rig.s, id, repo, commit); err != nil || miss.Code != protocol.RescueTreeRefused || len(miss.Files) != 0 {
		t.Errorf("traversal %+v %v", miss, err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Errorf("traversal wrote outside: %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rescue bundle %d bytes, tree tar %d bytes, files %d, response JSON %d bytes, rescue %s", rb.Size, rb.TreeArchives[0].Size, len(got.Files), len(encoded), time.Since(start))
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
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRET-TARGET") {
		t.Fatal("the manifest contains the link target")
	}
	commit := rb.Refs[0].Commit
	for _, ref := range rb.Refs {
		if protocol.RescueWorkRef(ref.Name) {
			commit = ref.Commit
		}
	}
	got, err := ReadRescueTree(context.Background(), rig.r, rig.s, rep.Session+"/"+rep.Stamp, filepath.Base(rep.Repos[0].Path), commit)
	if err != nil || got.OK || got.Code != protocol.RescueTreeRefused || len(got.Files) != 0 || strings.Contains(string(mustJSON(t, got)), "SECRET-TARGET") {
		t.Fatalf("read %+v %v", got, err)
	}
	restored := rig.restore(t, filepath.Join(rig.s.SharedDir, filepath.FromSlash(rb.File)))
	if restored == "" {
		t.Fatal("restore")
	}
}

func sameTree(a, b protocol.RescueTree) bool {
	if a.OK != b.OK || a.Commit != b.Commit || len(a.Files) != len(b.Files) {
		return false
	}
	for i := range a.Files {
		if a.Files[i].Path != b.Files[i].Path || a.Files[i].SHA256 != b.Files[i].SHA256 || !bytes.Equal(a.Files[i].Bytes, b.Files[i].Bytes) {
			return false
		}
	}
	return true
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

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tarWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
