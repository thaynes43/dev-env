package agentd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// realSharedIsMounted is the check the rigs replace.
var realSharedIsMounted = sharedIsMounted

// sharedVolume makes the rig's shared directory and lets it count as mounted:
// in a test it shares a filesystem with the session home. newRescueRig calls it.
func (rig rescueRig) sharedVolume(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(rig.s.SharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := sharedIsMounted
	sharedIsMounted = func(shared, _ string) error {
		_, err := os.Stat(shared)
		return err
	}
	t.Cleanup(func() { sharedIsMounted = old })
	return rig.s.SharedDir
}

// restore does what `agent-run rescue restore` will: a fresh partial clone of
// origin, then a fetch of every ref in the bundle. It returns the clone.
func (rig rescueRig) restore(t *testing.T, bundle string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "restored")
	gitRun(t, rig.g.env, rig.g.root, "clone", "-q", "--filter=blob:none", "file://"+rig.g.remote, dst)
	gitRun(t, rig.g.env, dst, "bundle", "verify", "--quiet", bundle)
	gitRun(t, rig.g.env, dst, "fetch", "-q", bundle, "refs/*:refs/rescued/*")
	return dst
}

func TestRescueBundlesEveryUnpushedRef(t *testing.T) {
	rig := newRescueRig(t)
	shared := rig.s.SharedDir
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "done.txt"), "done\n")
	gitRun(t, env, wt, "add", "done.txt")
	gitRun(t, env, wt, "commit", "-q", "-m", "work")
	writeFile(t, filepath.Join(wt, "README.md"), "stashed\n")
	gitRun(t, env, wt, "stash", "push", "-q", "-m", "older")
	writeFile(t, filepath.Join(wt, "README.md"), "stashed again\n")
	gitRun(t, env, wt, "stash", "push", "-q", "-m", "newer")
	writeFile(t, filepath.Join(wt, "notes", "uncommitted.md"), "only on the volume\n")
	older := gitRun(t, env, wt, "rev-parse", "stash@{1}")

	rep := rig.rescue(t)
	if !rep.OK || rep.CleanAndPushed || rep.Bundle == nil || rep.Bundle.Error != "" {
		t.Fatalf("report ok=%v bundle %+v repo %+v", rep.OK, rep.Bundle, rep.Repos[0].Bundle)
	}
	dir := "rescue/demo-1006-170000/20261006-1730"
	if rep.Bundle.Dir != dir || rep.Bundle.Manifest != dir+"/manifest.json" {
		t.Errorf("bundle %+v", rep.Bundle)
	}
	rb := rep.Repos[0].Bundle
	if rb == nil || !rb.Verified || rb.File != dir+"/demo.bundle" || rb.Base != "refs/remotes/origin/HEAD" || rb.SHA256 == "" || rb.Size == 0 {
		t.Fatalf("repo bundle %+v", rb)
	}
	// The bundle holds every ref on the list, each stash entry under a ref
	// of its own.
	w := rig.worktree(t, rep, wt)
	want := map[string]string{
		"refs/heads/" + rig.ws.Branch:  "refs/heads/" + rig.ws.Branch,
		"refs/heads/" + w.RescueBranch: "refs/heads/" + w.RescueBranch,
		protocol.StashRefPrefix + "0":  "stash@{0}",
		protocol.StashRefPrefix + "1":  "stash@{1}",
	}
	if len(rb.Refs) != len(want) || len(rep.Repos[0].UnpushedRefs) != len(want) {
		t.Errorf("bundle refs %+v, unpushed %+v", rb.Refs, rep.Repos[0].UnpushedRefs)
	}
	for _, ref := range rb.Refs {
		if want[ref.Name] != ref.Source {
			t.Errorf("bundle ref %+v", ref)
		}
	}
	// The stash anchors are gone from the clone again.
	if left := gitRun(t, env, rig.ws.Clone, "for-each-ref", "refs/agentd-rescue"); left != "" {
		t.Errorf("temporary refs left: %s", left)
	}
	if left, _ := filepath.Glob(filepath.Join(rig.s.StateDir, "rescue-*.bundle")); len(left) != 0 {
		t.Errorf("temporary bundles left: %q", left)
	}

	// The manifest says the same, and the file is the one it describes.
	var m protocol.RescueManifest
	raw, err := os.ReadFile(filepath.Join(shared, dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Complete || m.Version != 1 || m.Session != "demo-1006-170000" || len(m.Repos) != 1 || m.Repos[0].Bundle.SHA256 != rb.SHA256 || m.Repos[0].Path != rig.ws.Clone {
		t.Errorf("manifest %+v", m)
	}
	if m.Repos[0].Remote != "file://"+rig.g.remote {
		t.Errorf("manifest remote %q", m.Repos[0].Remote)
	}
	if sum, size, err := hashFile(filepath.Join(shared, dir, "demo.bundle")); err != nil || sum != rb.SHA256 || size != rb.Size {
		t.Errorf("file sha256 %s size %d err %v; report %s %d", sum, size, err, rb.SHA256, rb.Size)
	}

	// Origin moves on; the bundle still restores everything, the
	// uncommitted file included.
	writeFile(t, filepath.Join(rig.g.seed, "later.txt"), "later\n")
	gitRun(t, env, rig.g.seed, "add", "later.txt")
	gitRun(t, env, rig.g.seed, "commit", "-q", "-m", "later")
	gitRun(t, env, rig.g.seed, "push", "-q", "origin", "HEAD:main")
	got := rig.restore(t, filepath.Join(shared, rb.File))
	if c := gitRun(t, env, got, "show", "refs/rescued/heads/"+w.RescueBranch+":notes/uncommitted.md"); c != "only on the volume" {
		t.Errorf("restored uncommitted file = %q", c)
	}
	if c := gitRun(t, env, got, "show", "refs/rescued/heads/"+rig.ws.Branch+":done.txt"); c != "done" {
		t.Errorf("restored commit = %q", c)
	}
	if c := gitRun(t, env, got, "rev-parse", "refs/rescued/agentd-rescue/stash/1"); c != older {
		t.Errorf("restored stash@{1} = %s, want %s", c, older)
	}
}

// A clean, pushed session writes nothing to the shared volume: D-10's proof
// needs no bundle.
func TestRescueCleanWritesNoBundle(t *testing.T) {
	rig := newRescueRig(t)
	shared := rig.s.SharedDir
	rep := rig.rescue(t)
	if !rep.OK || !rep.CleanAndPushed || rep.Bundle != nil || rep.Repos[0].Bundle != nil {
		t.Fatalf("report %+v", rep)
	}
	if exists(filepath.Join(shared, "rescue")) {
		t.Error("a clean rescue wrote to the shared volume")
	}
}

// A second rescue in the same minute gets a directory of its own.
func TestRescueBundleDirectoryPerRescue(t *testing.T) {
	rig := newRescueRig(t)
	writeFile(t, filepath.Join(rig.ws.Worktree, "a.txt"), "a\n")
	first := rig.rescue(t)
	second := rig.rescue(t)
	if first.Bundle == nil || second.Bundle == nil || first.Bundle.Dir == second.Bundle.Dir || second.Bundle.Dir != first.Bundle.Dir+"-2" {
		t.Errorf("dirs %+v and %+v", first.Bundle, second.Bundle)
	}
	if !second.OK || !second.Repos[0].Bundle.Verified {
		t.Errorf("second %+v", second.Repos[0].Bundle)
	}
}

// In a blob:none clone, a ref can hold blobs the clone never downloaded; the
// bundle needs them, and git fetches them from origin while it writes it.
func TestRescueBundleInAPartialClone(t *testing.T) {
	rig := newRescueRig(t)
	shared := rig.s.SharedDir
	env := rig.g.env
	gitRun(t, env, rig.g.seed, "switch", "-q", "-c", "other")
	writeFile(t, filepath.Join(rig.g.seed, "other.txt"), "never downloaded\n")
	gitRun(t, env, rig.g.seed, "add", "other.txt")
	gitRun(t, env, rig.g.seed, "commit", "-q", "-m", "other")
	gitRun(t, env, rig.g.seed, "push", "-q", "origin", "other")
	clone := rig.ws.Clone
	gitRun(t, env, clone, "fetch", "-q", "origin")
	blob := gitRun(t, env, clone, "rev-parse", "origin/other:other.txt")
	if missing := gitRun(t, env, clone, "rev-list", "--objects", "--missing=print", "origin/other"); !strings.Contains(missing, "?"+blob) {
		t.Fatalf("the blob is not missing in the partial clone:\n%s", missing)
	}
	// A local commit on top of origin/other, made without a checkout, so its
	// tree names the missing blob.
	tree := gitRun(t, env, clone, "rev-parse", "origin/other^{tree}")
	commit := gitRun(t, env, clone, "commit-tree", tree, "-p", "origin/other", "-m", "local")
	gitRun(t, env, clone, "update-ref", "refs/heads/mine", commit)
	// origin forgets the branch, so only the bundle has its commits.
	gitRun(t, env, rig.g.seed, "push", "-q", "origin", "--delete", "other")

	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified {
		t.Fatalf("ok=%v bundle %+v repo error %q", rep.OK, rb, rep.Repos[0].Error)
	}
	got := rig.restore(t, filepath.Join(shared, rb.File))
	// Without origin, the blob can only come from the bundle.
	if err := os.RemoveAll(rig.g.remote); err != nil {
		t.Fatal(err)
	}
	if c := gitRun(t, env, got, "show", "refs/rescued/heads/mine:other.txt"); c != "never downloaded" {
		t.Errorf("restored other.txt = %q", c)
	}
}

// Without origin, a bundle that needs a blob the clone never downloaded cannot
// be written: the rescue is not OK, and the manifest says it is incomplete.
func TestRescueBundleFailureIsNotOK(t *testing.T) {
	rig := newRescueRig(t)
	shared := rig.s.SharedDir
	env := rig.g.env
	gitRun(t, env, rig.g.seed, "switch", "-q", "-c", "other")
	writeFile(t, filepath.Join(rig.g.seed, "other.txt"), "never downloaded\n")
	gitRun(t, env, rig.g.seed, "add", "other.txt")
	gitRun(t, env, rig.g.seed, "commit", "-q", "-m", "other")
	gitRun(t, env, rig.g.seed, "push", "-q", "origin", "other")
	clone := rig.ws.Clone
	gitRun(t, env, clone, "fetch", "-q", "origin")
	tree := gitRun(t, env, clone, "rev-parse", "origin/other^{tree}")
	commit := gitRun(t, env, clone, "commit-tree", tree, "-p", "origin/other", "-m", "local")
	gitRun(t, env, clone, "update-ref", "refs/heads/mine", commit)
	if err := os.RemoveAll(rig.g.remote); err != nil {
		t.Fatal(err)
	}

	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if rep.OK || rb == nil || rb.Verified || !strings.Contains(rb.Error, "git bundle create") || rep.Bundle.Error == "" {
		t.Fatalf("ok=%v bundle %+v report bundle %+v", rep.OK, rb, rep.Bundle)
	}
	var m protocol.RescueManifest
	raw, err := os.ReadFile(filepath.Join(shared, rep.Bundle.Dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &m); err != nil || m.Complete {
		t.Errorf("manifest %+v err %v", m, err)
	}
	if exists(filepath.Join(shared, rep.Bundle.Dir, "demo.bundle")) {
		t.Error("a failed bundle was left on the shared volume")
	}
}

// The shared volume must be mounted: a bundle on the session volume would be
// deleted with it.
func TestRescueBundleNeedsTheSharedVolume(t *testing.T) {
	rig := newRescueRig(t)
	sharedIsMounted = realSharedIsMounted
	if err := os.RemoveAll(rig.s.SharedDir); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(rig.ws.Worktree, "a.txt"), "a\n")
	rep := rig.rescue(t)
	if rep.OK || rep.Bundle == nil || !strings.Contains(rep.Bundle.Error, "not mounted") {
		t.Errorf("no shared dir: ok=%v bundle %+v", rep.OK, rep.Bundle)
	}
	// A plain directory on the session volume is refused too.
	if err := os.MkdirAll(rig.s.SharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rep = rig.rescue(t)
	if rep.OK || rep.Bundle == nil || !strings.Contains(rep.Bundle.Error, "on the session volume") {
		t.Errorf("same filesystem: ok=%v bundle %+v", rep.OK, rep.Bundle)
	}
	if exists(filepath.Join(rig.s.SharedDir, "rescue")) {
		t.Error("wrote to a shared directory that is not mounted")
	}
}

func TestOriginURLDropsCredentials(t *testing.T) {
	rig := newRescueRig(t)
	gitRun(t, rig.g.env, rig.ws.Clone, "remote", "set-url", "origin", "https://x-access-token:secret@github.com/thaynes43/demo")
	if got := originURL(t.Context(), rig.r, rig.s, rig.ws.Clone); got != "https://github.com/thaynes43/demo" {
		t.Errorf("origin = %q", got)
	}
}

func TestStashIndex(t *testing.T) {
	for in, want := range map[string]int{"stash@{0}": 0, "stash@{12}": 12, "stash@{-1}": -1, "stash@{x}": -1, "refs/heads/a": -1, "stash@{1": -1} {
		n, ok := stashIndex(in)
		if (want >= 0) != ok || (ok && n != want) {
			t.Errorf("%s: %d %v", in, n, ok)
		}
	}
}
