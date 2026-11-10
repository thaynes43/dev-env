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
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func (rig rescueRig) strictRescue(t *testing.T) protocol.RescueReport {
	t.Helper()
	rep, err := Rescue(context.Background(), rig.r, rig.s, "demo-1006-170000", rescueNow, RescueOptions{StrictHome: true})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// gitInit makes dir a repository with one commit that has no origin.
func (rig rescueRig) gitInit(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "notes.txt"), "work nobody pushed\n")
	gitRun(t, rig.g.env, filepath.Dir(dir), "init", "-q", "-b", "main", dir)
	gitRun(t, rig.g.env, dir, "add", "notes.txt")
	gitRun(t, rig.g.env, dir, "commit", "-q", "-m", "local")
}

// A HOME laid out as boot leaves it, plus everything --strict-home must let
// through, passes, and the rescue then runs exactly as without the flag.
func TestStrictHomePassesAnOrdinaryHome(t *testing.T) {
	rig := newRescueRig(t)
	home, work := rig.s.Home, rig.s.WorkDir()
	clone := rig.s.ClonePath("demo")
	// A second worktree of the clone, and a link to the clone, under ~/work.
	gitRun(t, rig.g.env, clone, "worktree", "add", "-q", "-b", "second", filepath.Join(work, "second"))
	if err := os.Symlink(clone, filepath.Join(work, "demo-link")); err != nil {
		t.Fatal(err)
	}
	// A link from HOME to the worktree.
	if err := os.Symlink(rig.ws.Worktree, filepath.Join(home, "wt-link")); err != nil {
		t.Fatal(err)
	}
	// Files, plain directories, a link to nothing, and hidden tool state that
	// holds a repository directly under HOME.
	writeFile(t, filepath.Join(work, "demo-1006-170000.log"), "log\n")
	writeFile(t, filepath.Join(work, "scratch", "a.txt"), "a\n")
	writeFile(t, filepath.Join(home, "codex", "x"), "x\n")
	if err := os.Symlink(filepath.Join(home, "gone"), filepath.Join(work, "dangling")); err != nil {
		t.Fatal(err)
	}
	rig.gitInit(t, filepath.Join(home, ".cache", "tool"))
	rig.gitInit(t, filepath.Join(home, ".tool-state"))
	// A repository two levels down is a recorded limit (DESIGN-061 section 9).
	rig.gitInit(t, filepath.Join(work, "scratch", "deep"))
	writeFile(t, filepath.Join(rig.ws.Worktree, "edit.txt"), "dirty\n")

	rep := rig.strictRescue(t)
	if rep.StrictHome == nil || rep.StrictHome.Refused != "" || len(rep.StrictHome.Stray) != 0 {
		t.Fatalf("strict home: %+v", rep.StrictHome)
	}
	w := rig.worktree(t, rep, rig.ws.Worktree)
	if !rep.OK || len(rep.Repos) != 1 || !w.Dirty || w.RescueBranch == "" {
		t.Fatalf("the rescue did not run as usual: %+v, worktree %+v", rep, w)
	}
	var out map[string]json.RawMessage
	data, _ := json.Marshal(rep)
	if err := json.Unmarshal(data, &out); err != nil || string(out["strictHome"]) != "{}" {
		t.Errorf("a passed check is not {} in the report: %s", out["strictHome"])
	}
}

// Every way --strict-home refuses: the rescue then commits and bundles
// nothing, and the report says why.
func TestStrictHomeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		// setup returns the path the refusal must name.
		setup func(t *testing.T, rig rescueRig) string
		want  string
	}{
		{"a clone directly under ~/work", func(t *testing.T, rig rescueRig) string {
			p := filepath.Join(rig.s.WorkDir(), "side-project")
			rig.gitInit(t, p)
			return p
		}, "stray repository "},
		{"a hidden clone directly under ~/work", func(t *testing.T, rig rescueRig) string {
			p := filepath.Join(rig.s.WorkDir(), ".tmp-clone")
			rig.gitInit(t, p)
			return p
		}, "stray repository "},
		{"a clone directly under HOME", func(t *testing.T, rig rescueRig) string {
			p := filepath.Join(rig.s.Home, "project")
			rig.gitInit(t, p)
			return p
		}, "stray repository "},
		{"HOME itself a repository", func(t *testing.T, rig rescueRig) string {
			gitRun(t, rig.g.env, rig.s.Home, "init", "-q")
			return rig.s.Home
		}, "stray repository "},
		{"a .git file pointing elsewhere", func(t *testing.T, rig rescueRig) string {
			p := filepath.Join(rig.s.WorkDir(), "separate")
			if err := os.MkdirAll(filepath.Join(rig.s.Home, ".gitdirs"), 0o700); err != nil {
				t.Fatal(err)
			}
			gitRun(t, rig.g.env, rig.s.WorkDir(), "init", "-q", "--separate-git-dir", filepath.Join(rig.s.Home, ".gitdirs", "separate"), p)
			return p
		}, "stray repository "},
		{"a worktree of a clone outside ~/repos", func(t *testing.T, rig rescueRig) string {
			other := filepath.Join(rig.s.Home, ".hidden-clone")
			rig.gitInit(t, other)
			p := filepath.Join(rig.s.WorkDir(), "foreign-wt")
			gitRun(t, rig.g.env, other, "worktree", "add", "-q", "-b", "foreign", p)
			return p
		}, "stray repository "},
		{"a link under ~/work to a repository outside ~/repos", func(t *testing.T, rig rescueRig) string {
			other := filepath.Join(rig.s.Home, ".elsewhere", "repo")
			rig.gitInit(t, other)
			p := filepath.Join(rig.s.WorkDir(), "linked")
			if err := os.Symlink(other, p); err != nil {
				t.Fatal(err)
			}
			return p
		}, "stray repository "},
		{"a directory it cannot inspect", func(t *testing.T, rig rescueRig) string {
			if os.Geteuid() == 0 {
				t.Skip("root reads every directory")
			}
			p := filepath.Join(rig.s.WorkDir(), "locked")
			if err := os.MkdirAll(p, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o700) })
			return p
		}, "cannot inspect "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRescueRig(t)
			// Work the rescue would commit and bundle if it ran.
			writeFile(t, filepath.Join(rig.ws.Worktree, "edit.txt"), "dirty\n")
			gitRun(t, rig.g.env, rig.ws.Worktree, "commit", "-q", "--allow-empty", "-m", "unpushed")
			path := tc.setup(t, rig)

			rep := rig.strictRescue(t)
			if rep.OK || rep.CleanAndPushed || len(rep.Repos) != 0 || rep.Bundle != nil || rep.StrictHome == nil {
				t.Fatalf("not refused: %+v", rep)
			}
			if want := tc.want + path; !strings.HasPrefix(rep.StrictHome.Refused, want) {
				t.Errorf("refused %q, want it to start with %q", rep.StrictHome.Refused, want)
			}
			if tc.want == "stray repository " && (len(rep.StrictHome.Stray) != 1 || rep.StrictHome.Stray[0] != path) {
				t.Errorf("stray %q, want [%s]", rep.StrictHome.Stray, path)
			}
			// Nothing was committed or written.
			if refs := gitRun(t, rig.g.env, rig.s.ClonePath("demo"), "for-each-ref", "refs/heads/rescue"); refs != "" {
				t.Errorf("a rescue branch was made: %s", refs)
			}
			if _, err := os.Stat(filepath.Join(rig.s.SharedDir, protocol.RescueRoot)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the shared volume was written to: %v", err)
			}
			// Without the flag the same HOME rescues as always.
			if plain := rig.rescue(t); plain.StrictHome != nil || len(plain.Repos) != 1 || !plain.OK {
				t.Errorf("without --strict-home: %+v", plain)
			}
		})
	}
}

// A clone whose worktrees cannot be listed refuses, since its worktrees under
// ~/work could not be told from stray repositories.
func TestStrictHomeRefusesAnUnlistableClone(t *testing.T) {
	rig := newRescueRig(t)
	r := worktreeListFails{Runner: rig.r}
	rep, err := Rescue(context.Background(), r, rig.s, "demo-1006-170000", rescueNow, RescueOptions{StrictHome: true})
	if err != nil {
		t.Fatal(err)
	}
	clone := rig.s.ClonePath("demo")
	if rep.OK || rep.StrictHome == nil || !strings.HasPrefix(rep.StrictHome.Refused, "cannot list the worktrees of "+clone) {
		t.Fatalf("not refused: %+v %+v", rep, rep.StrictHome)
	}
	// Its worktree is then reported stray as well, after the cause.
	if !strings.Contains(rep.StrictHome.Refused, "stray repository "+rig.ws.Worktree) {
		t.Errorf("refused %q", rep.StrictHome.Refused)
	}
}

type worktreeListFails struct{ Runner }

func (w worktreeListFails) Run(ctx context.Context, c Cmd) (Result, error) {
	if strings.Contains(strings.Join(c.Args, " "), "worktree list") {
		return Result{}, &CmdError{Name: "git", ExitCode: 128}
	}
	return w.Runner.Run(ctx, c)
}

// Many problems make one bounded line, every stray sorted in the list.
func TestStrictHomeBoundsItsReport(t *testing.T) {
	rig := newRescueRig(t)
	var want []string
	for _, n := range []string{"e", "a", "d", "b", "c"} {
		p := filepath.Join(rig.s.WorkDir(), n)
		rig.gitInit(t, p)
		want = append(want, p)
	}
	rep := rig.strictRescue(t)
	got := rep.StrictHome
	if len(got.Stray) != 5 || got.Stray[0] != want[1] || got.Stray[4] != want[0] {
		t.Fatalf("stray %q", got.Stray)
	}
	if !strings.HasSuffix(got.Refused, "; and 1 more") || strings.Count(got.Refused, "stray repository ") != strictHomeShown {
		t.Errorf("refused %q", got.Refused)
	}
}

// An empty volume holds no repository, so the check passes and is recorded;
// a shared workspace rescue refuses the flag.
func TestStrictHomeOnAnEmptyVolumeAndAWorkspace(t *testing.T) {
	old := sharedIsMounted
	sharedIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { sharedIsMounted = old })
	home := t.TempDir()
	s := testSettings(t, home)
	if err := os.MkdirAll(filepath.Join(home, lostAndFound), 0o700); err != nil {
		t.Fatal(err)
	}
	rep, err := Rescue(context.Background(), &fakeRunner{}, s, "s-1", rescueNow, RescueOptions{StopAgent: true, StrictHome: true})
	if err != nil || !rep.OK || !rep.VolumeEmpty || rep.StrictHome == nil || rep.StrictHome.Refused != "" {
		t.Fatalf("empty volume: %+v %v", rep, err)
	}
	s.WorkspaceID = "ws-1"
	if _, err := Rescue(context.Background(), &fakeRunner{}, s, "s-1", rescueNow, RescueOptions{StopAgent: true, StrictHome: true}); err == nil || !strings.Contains(err.Error(), "shared workspace") {
		t.Fatalf("a workspace rescue took --strict-home: %v", err)
	}
}

// verifyRig is a rescue with one bundle on the shared volume, and a statfs
// that reports 20 GiB with 18 free.
type verifyRig struct {
	rescueRig
	id, dir, manifest string
}

func newVerifyRig(t *testing.T) verifyRig {
	t.Helper()
	rig := newRescueRig(t)
	gitRun(t, rig.g.env, rig.ws.Worktree, "commit", "-q", "--allow-empty", "-m", "unpushed")
	rep := rig.rescue(t)
	if !rep.OK || rep.Bundle == nil || rep.Bundle.Manifest == "" {
		t.Fatalf("no bundle: %+v", rep)
	}
	old := sharedStatfs
	sharedStatfs = func(_ string, st *syscall.Statfs_t) error {
		*st = syscall.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 5 << 20, Bfree: 9 << 19, Bavail: 9 << 19}
		return nil
	}
	t.Cleanup(func() { sharedStatfs = old })
	return verifyRig{rescueRig: rig, id: strings.TrimPrefix(rep.Bundle.Dir, protocol.RescueRoot+"/"), dir: rep.Bundle.Dir, manifest: rep.Bundle.Manifest}
}

func (v verifyRig) abs(rel string) string { return filepath.Join(v.s.SharedDir, filepath.FromSlash(rel)) }

func (v verifyRig) editManifest(t *testing.T, edit func(*protocol.RescueManifest)) {
	t.Helper()
	var m protocol.RescueManifest
	data, err := os.ReadFile(v.abs(v.manifest))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	edit(&m)
	writeJSON(t, v.abs(v.manifest), m)
}

func (v verifyRig) bundle(t *testing.T) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(v.abs(v.dir), "*.bundle"))
	if len(matches) != 1 {
		t.Fatalf("bundles: %q", matches)
	}
	return matches[0]
}

func TestRescueVerifyReHashesEveryBundle(t *testing.T) {
	v := newVerifyRig(t)
	res, err := VerifyRescue(v.s, v.id)
	if err != nil || !res.OK || res.Refusal != "" {
		t.Fatalf("verify: %+v %v", res, err)
	}
	data, _ := os.ReadFile(v.abs(v.manifest))
	sum := sha256.Sum256(data)
	fi, _ := os.Stat(v.bundle(t))
	if res.Version != 1 || res.ID != v.id || res.Manifest != v.manifest || res.ManifestSHA256 != hex.EncodeToString(sum[:]) || res.Bundles != 1 || res.Bytes != fi.Size() {
		t.Errorf("result %+v", res)
	}
	if res.SharedTotalBytes != 20<<30 || res.SharedFreeBytes != 18<<30 {
		t.Errorf("space: %d of %d", res.SharedFreeBytes, res.SharedTotalBytes)
	}
	// Without --id it reports the space only.
	res, err = VerifyRescue(v.s, "")
	if err != nil || !res.OK || res.ID != "" || res.ManifestSHA256 != "" || res.SharedFreeBytes != 18<<30 {
		t.Fatalf("space only: %+v %v", res, err)
	}
	// Any change to the manifest is another digest, even a valid one.
	v.editManifest(t, func(m *protocol.RescueManifest) { m.CreatedAt = m.CreatedAt.Add(1) })
	again, err := VerifyRescue(v.s, v.id)
	if err != nil || !again.OK || again.ManifestSHA256 == hex.EncodeToString(sum[:]) {
		t.Fatalf("a changed manifest kept its digest: %+v %v", again, err)
	}
}

func TestRescueVerifyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tamper func(t *testing.T, v verifyRig)
		code   string
	}{
		{"a bundle with a byte flipped", func(t *testing.T, v verifyRig) {
			p := v.bundle(t)
			data, _ := os.ReadFile(p)
			data[len(data)-1] ^= 0xff
			writeFile(t, p, string(data))
		}, protocol.RescueVerifyBundleMismatch},
		{"a bundle grown by a byte", func(t *testing.T, v verifyRig) {
			p := v.bundle(t)
			data, _ := os.ReadFile(p)
			writeFile(t, p, string(data)+"x")
		}, protocol.RescueVerifyBundleMismatch},
		{"a bundle removed", func(t *testing.T, v verifyRig) {
			if err := os.Remove(v.bundle(t)); err != nil {
				t.Fatal(err)
			}
		}, protocol.RescueVerifyBundleMissing},
		{"a bundle replaced by a link to the same bytes", func(t *testing.T, v verifyRig) {
			p := v.bundle(t)
			copyTo := filepath.Join(v.s.SharedDir, "copy.bundle")
			data, _ := os.ReadFile(p)
			writeFile(t, copyTo, string(data))
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(copyTo, p); err != nil {
				t.Fatal(err)
			}
		}, protocol.RescueVerifyBundleMissing},
		{"the manifest removed", func(t *testing.T, v verifyRig) {
			if err := os.Remove(v.abs(v.manifest)); err != nil {
				t.Fatal(err)
			}
		}, protocol.RescueVerifyBundleMissing},
		{"the rescue removed", func(t *testing.T, v verifyRig) {
			if err := os.RemoveAll(v.abs(v.dir)); err != nil {
				t.Fatal(err)
			}
		}, protocol.RescueVerifyBundleMissing},
		{"the manifest not JSON", func(t *testing.T, v verifyRig) { writeFile(t, v.abs(v.manifest), "{") }, protocol.RescueVerifyBundleMismatch},
		{"the manifest another version", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Version = 2 })
		}, protocol.RescueVerifyBundleMismatch},
		{"the manifest incomplete", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Complete = false })
		}, protocol.RescueVerifyBundleMismatch},
		{"the manifest for another session", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Session = "other-1006-170000" })
		}, protocol.RescueVerifyBundleMismatch},
		{"a bundle the manifest says differs", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Repos[0].Bundle.SHA256 = strings.Repeat("0", 64) })
		}, protocol.RescueVerifyBundleMismatch},
		{"a bundle outside the rescue", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Repos[0].Bundle.File = v.dir + "/../demo.bundle" })
		}, protocol.RescueVerifyBundleMismatch},
		{"a repo without a verified bundle", func(t *testing.T, v verifyRig) {
			v.editManifest(t, func(m *protocol.RescueManifest) { m.Repos[0].Bundle.Verified = false })
		}, protocol.RescueVerifyBundleMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifyRig(t)
			tc.tamper(t, v)
			res, err := VerifyRescue(v.s, v.id)
			if err != nil || res.OK || res.Refusal != tc.code || res.Detail == "" || res.SharedTotalBytes == 0 {
				t.Fatalf("got %+v %v, want refusal %s", res, err, tc.code)
			}
		})
	}
}

// What proves nothing either way is an error, never a refusal or ok.
func TestRescueVerifyFailures(t *testing.T) {
	v := newVerifyRig(t)
	if _, err := VerifyRescue(v.s, "../etc/20261006-1730"); err == nil {
		t.Error("a bad id was read")
	}
	old := sharedIsMounted
	sharedIsMounted = func(string, string) error { return errors.New("not mounted") }
	if _, err := VerifyRescue(v.s, v.id); err == nil {
		t.Error("verified without the shared volume")
	}
	sharedIsMounted = old
	sharedStatfs = func(string, *syscall.Statfs_t) error { return syscall.EIO }
	if _, err := VerifyRescue(v.s, ""); err == nil {
		t.Error("no statfs, still an answer")
	}
	sharedStatfs = func(_ string, st *syscall.Statfs_t) error { *st = syscall.Statfs_t{Bsize: 4096, Blocks: 1, Bfree: 2}; return nil }
	if _, err := VerifyRescue(v.s, ""); err == nil {
		t.Error("contradictory figures, still an answer")
	}
}

func TestDropLogRemovesOnlyTheSessionsLogCopy(t *testing.T) {
	rig := newRescueRig(t)
	shared := rig.s.SharedDir
	own := filepath.Join(shared, protocol.LogsRoot, "demo-1006-170000.log")
	writeFile(t, own, "the task's log\n")
	others := []string{
		filepath.Join(shared, protocol.LogsRoot, "other-1006-170000.log"),
		filepath.Join(shared, protocol.LogsRoot, ".demo-1006-170000.log.tmp-1"),
		filepath.Join(shared, protocol.RescueRoot, "demo-1006-170000", "20261006-1730", "manifest.json"),
		rig.s.LogPath("demo-1006-170000"),
	}
	for _, p := range others {
		writeFile(t, p, "keep\n")
	}
	res, err := DropLog(rig.s, "demo-1006-170000")
	if err != nil || !res.Removed || res.Bytes != int64(len("the task's log\n")) || res.Path != "logs/demo-1006-170000.log" || res.Session != "demo-1006-170000" {
		t.Fatalf("drop-log: %+v %v", res, err)
	}
	if _, err := os.Lstat(own); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the copy is still there: %v", err)
	}
	for _, p := range others {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s went too: %v", p, err)
		}
	}
	// Again: nothing to remove is no error.
	if res, err := DropLog(rig.s, "demo-1006-170000"); err != nil || res.Removed {
		t.Fatalf("a second drop-log: %+v %v", res, err)
	}
}

func TestDropLogRefusals(t *testing.T) {
	const name = "demo-1006-170000"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, rig rescueRig) (session string, kept string)
		want  string
	}{
		{"a link in place of the copy", func(t *testing.T, rig rescueRig) (string, string) {
			target := filepath.Join(rig.s.SharedDir, "target.log")
			writeFile(t, target, "x")
			link := rig.s.SharedLogPath(name)
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return name, link
		}, "not a regular file"},
		{"a directory in place of the copy", func(t *testing.T, rig rescueRig) (string, string) {
			p := rig.s.SharedLogPath(name)
			writeFile(t, filepath.Join(p, "inner"), "x")
			return name, p
		}, "not a regular file"},
		{"logs/ a link", func(t *testing.T, rig rescueRig) (string, string) {
			elsewhere := filepath.Join(rig.s.SharedDir, "elsewhere")
			writeFile(t, filepath.Join(elsewhere, name+".log"), "x")
			if err := os.Symlink(elsewhere, filepath.Join(rig.s.SharedDir, protocol.LogsRoot)); err != nil {
				t.Fatal(err)
			}
			return name, filepath.Join(elsewhere, name+".log")
		}, "not a directory"},
		{"a session name that is not one", func(t *testing.T, rig rescueRig) (string, string) {
			p := filepath.Join(rig.s.SharedDir, "x.log")
			writeFile(t, p, "x")
			return "../x", p
		}, "is not one"},
		{"no session name", func(t *testing.T, rig rescueRig) (string, string) {
			p := filepath.Join(rig.s.SharedDir, protocol.LogsRoot, ".log")
			writeFile(t, p, "x")
			return "", p
		}, "is not one"},
		{"the agent running", func(t *testing.T, rig rescueRig) (string, string) {
			writeJSON(t, rig.s.statePath(pidFile), agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())})
			p := rig.s.SharedLogPath(name)
			writeFile(t, p, "x")
			return name, p
		}, "agent is running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newRescueRig(t)
			session, kept := tc.setup(t, rig)
			if _, err := DropLog(rig.s, session); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if _, err := os.Lstat(kept); err != nil {
				t.Errorf("%s was removed: %v", kept, err)
			}
		})
	}
	rig := newRescueRig(t)
	writeFile(t, rig.s.SharedLogPath(name), "x")
	old := sharedIsMounted
	sharedIsMounted = func(string, string) error { return errors.New("not mounted") }
	t.Cleanup(func() { sharedIsMounted = old })
	if _, err := DropLog(rig.s, name); err == nil {
		t.Error("removed without the shared volume")
	}
	if _, err := os.Stat(rig.s.SharedLogPath(name)); err != nil {
		t.Error("the copy went without the shared volume")
	}
}
