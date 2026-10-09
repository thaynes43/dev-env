package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// shelfDir is a shared volume for a list or prune test, counted as mounted.
func shelfDir(t *testing.T) Settings {
	t.Helper()
	home := t.TempDir()
	s := Settings{Home: home, SharedDir: filepath.Join(home, ".shared")}
	if err := os.MkdirAll(s.SharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := sharedIsMounted
	sharedIsMounted = func(shared, _ string) error {
		_, err := os.Stat(shared)
		return err
	}
	t.Cleanup(func() { sharedIsMounted = old })
	return s
}

// age sets a path's modification time; a test makes its files first, because
// adding a file to a directory moves the directory's time.
func age(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// putRescue writes a rescue directory with a bundle and, unless unfinished, a
// manifest, and dates both.
func putRescue(t *testing.T, s Settings, session, name string, created time.Time, finished bool) string {
	t.Helper()
	dir := protocol.RescueDir(session, name)
	abs := filepath.Join(s.SharedDir, dir)
	writeFile(t, filepath.Join(abs, "demo.bundle"), "0123456789")
	if finished {
		m := protocol.RescueManifest{Version: 1, Session: session, Stamp: name, Dir: dir, CreatedAt: created, Complete: true,
			Repos: []protocol.ManifestRepo{{Path: "/home/dev/repos/demo", Bundle: protocol.RepoBundle{File: dir + "/demo.bundle", Size: 10}}}}
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(abs, protocol.ManifestFile), string(data))
	}
	age(t, abs, created)
	return abs
}

var shelfNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestListRescuesAndPrune(t *testing.T) {
	s := shelfDir(t)
	old, recent := shelfNow.Add(-40*24*time.Hour), shelfNow.Add(-2*24*time.Hour)
	putRescue(t, s, "sess-a", "20260829-1200", old, true)
	putRescue(t, s, "sess-a", "20261006-1200", recent, true)
	putRescue(t, s, "sess-b", "20260829-1300", old, false)
	putRescue(t, s, "sess-keep", "20260801-0000", old.Add(-30*24*time.Hour), true)
	bad := putRescue(t, s, "sess-bad", "20260829-1400", old, false)
	writeFile(t, filepath.Join(bad, protocol.ManifestFile), "{not json")
	age(t, bad, old)
	writeFile(t, filepath.Join(s.SharedDir, "rescue", "notes.txt"), "a human's note")
	if err := os.MkdirAll(filepath.Join(s.SharedDir, "rescue", "sess-a", "scratch"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "precious"), "never pruned")
	age(t, outside, old)
	if err := os.Symlink(outside, filepath.Join(s.SharedDir, "rescue", "sess-a", "20260830-0000")); err != nil {
		t.Fatal(err)
	}
	for _, sa := range []string{"sess-a", "sess-b", "sess-keep", "sess-bad"} {
		age(t, filepath.Join(s.SharedDir, "rescue", sa), old)
	}
	logs := filepath.Join(s.SharedDir, "logs")
	for name, at := range map[string]time.Time{"sess-a.log": old, "sess-keep.log": old, "sess-new.log": recent, "other.txt": old} {
		writeFile(t, filepath.Join(logs, name), "log\n")
		age(t, filepath.Join(logs, name), at)
	}

	list, err := ListRescues(s, "")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range list.Rescues {
		ids = append(ids, e.ID)
	}
	want := "sess-a/20261006-1200 sess-bad/20260829-1400 sess-b/20260829-1300 sess-a/20260829-1200 sess-keep/20260801-0000"
	if strings.Join(ids, " ") != want {
		t.Errorf("list = %v, want %s", ids, want)
	}
	byID := map[string]protocol.RescueEntry{}
	for _, e := range list.Rescues {
		byID[e.ID] = e
	}
	if e := byID["sess-a/20260829-1200"]; !e.Finished || e.Manifest == nil || e.Error != "" || e.Bytes == 0 || !e.CreatedAt.Equal(old) {
		t.Errorf("finished rescue = %+v", e)
	}
	if e := byID["sess-b/20260829-1300"]; e.Finished || e.Manifest != nil {
		t.Errorf("unfinished rescue = %+v", e)
	}
	if e := byID["sess-bad/20260829-1400"]; !e.Finished || e.Manifest != nil || !strings.Contains(e.Error, "not JSON") {
		t.Errorf("unreadable manifest = %+v", e)
	}
	if got := strings.Join(list.Unrecognized, " "); got != "rescue/notes.txt rescue/sess-a/20260830-0000 rescue/sess-a/scratch" {
		t.Errorf("unrecognized = %q", got)
	}
	if only, _ := ListRescues(s, "sess-b"); len(only.Rescues) != 1 || only.Rescues[0].ID != "sess-b/20260829-1300" {
		t.Errorf("list --session sess-b = %+v", only.Rescues)
	}

	if _, err := Prune(s, protocol.PruneRequest{OlderThan: "23h"}, shelfNow); err == nil || !strings.Contains(err.Error(), "floor") {
		t.Errorf("a retention below the floor: err = %v", err)
	}
	req := protocol.PruneRequest{OlderThan: "720h", Keep: []string{"sess-keep"}, DryRun: true}
	dry, err := Prune(s, req, shelfNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Removed) != 4 {
		t.Errorf("dry run would remove %+v, want 4", dry.Removed)
	}
	if _, err := os.Stat(filepath.Join(s.SharedDir, "rescue", "sess-a", "20260829-1200")); err != nil {
		t.Errorf("a dry run removed a rescue: %v", err)
	}

	req.DryRun = false
	rep, err := Prune(s, req, shelfNow)
	if err != nil {
		t.Fatal(err)
	}
	var removed []string
	for _, p := range rep.Removed {
		removed = append(removed, p.Path)
	}
	if got := strings.Join(removed, " "); got != "rescue/sess-bad/20260829-1400 rescue/sess-b/20260829-1300 rescue/sess-a/20260829-1200 logs/sess-a.log" {
		t.Errorf("removed %q", got)
	}
	if len(rep.Errors) != 0 || rep.Kept != 4 {
		t.Errorf("report = %+v, want 4 kept and no errors", rep)
	}
	for _, gone := range []string{"rescue/sess-b", "rescue/sess-bad", "rescue/sess-a/20260829-1200", "logs/sess-a.log"} {
		if _, err := os.Lstat(filepath.Join(s.SharedDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived the prune: %v", gone, err)
		}
	}
	for _, kept := range []string{"rescue/sess-a/20261006-1200", "rescue/sess-keep/20260801-0000", "rescue/notes.txt",
		"rescue/sess-a/scratch", "rescue/sess-a/20260830-0000", "logs/sess-keep.log", "logs/sess-new.log", "logs/other.txt"} {
		if _, err := os.Lstat(filepath.Join(s.SharedDir, kept)); err != nil {
			t.Errorf("%s was pruned: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Errorf("the prune followed a link: %v", err)
	}
}

// A restore's hold resets a rescue's age, so the prune that follows keeps it;
// a prune and a hold never run at once.
func TestHoldRescueKeepsItFromThePrune(t *testing.T) {
	s := shelfDir(t)
	old := shelfNow.Add(-40 * 24 * time.Hour)
	putRescue(t, s, "gone-1001-000000", "20260829-1200", old, true)
	putRescue(t, s, "gone-1001-000000", "20260829-1300", old, true)
	age(t, filepath.Join(s.SharedDir, "rescue", "gone-1001-000000"), old)

	if res, err := HoldRescue(s, "gone-1001-000000/20260101-0000", shelfNow); err != nil || res.Found {
		t.Fatalf("hold of a missing rescue = %+v, %v", res, err)
	}
	if _, err := HoldRescue(s, "../etc", shelfNow); err == nil {
		t.Fatal("held a path outside rescue/")
	}
	res, err := HoldRescue(s, "gone-1001-000000/20260829-1200", shelfNow)
	if err != nil || !res.Found || res.Rescue == nil || res.Rescue.Manifest == nil || !res.Rescue.ModifiedAt.Equal(shelfNow) {
		t.Fatalf("hold = %+v, %v", res, err)
	}
	rep, err := Prune(s, protocol.PruneRequest{OlderThan: "720h"}, shelfNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0].Path != "rescue/gone-1001-000000/20260829-1300" {
		t.Errorf("removed %+v, want only the rescue no one held", rep.Removed)
	}

	unlock, err := lockShelf(s)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Prune(s, protocol.PruneRequest{OlderThan: "720h"}, shelfNow)
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("a prune ran while the shelf lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestListRescuesNeedsTheSharedVolume(t *testing.T) {
	home := t.TempDir()
	s := Settings{Home: home, SharedDir: filepath.Join(home, ".shared")}
	if err := os.MkdirAll(s.SharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The real check: here the directory shares the home's filesystem.
	if _, err := ListRescues(s, ""); err == nil {
		t.Fatal("listed a shared directory that is not a mount")
	}
	if _, err := Prune(s, protocol.PruneRequest{OlderThan: "720h"}, shelfNow); err == nil {
		t.Fatal("pruned a shared directory that is not a mount")
	}
}

// restoreRig rescues the rescue rig's session, then returns settings for a
// second session's home on the same origin and shared volume.
func restoreRig(t *testing.T) (rescueRig, protocol.RescueReport, Settings, ExecRunner, string) {
	t.Helper()
	rig := newRescueRig(t)
	wt, env := rig.ws.Worktree, rig.g.env
	writeFile(t, filepath.Join(wt, "done.txt"), "done\n")
	gitRun(t, env, wt, "add", "done.txt")
	gitRun(t, env, wt, "commit", "-q", "-m", "work")
	writeFile(t, filepath.Join(wt, "notes", "uncommitted.md"), "only on the volume\n")
	rep := rig.rescue(t)
	if !rep.OK || rep.Bundle == nil || rep.Bundle.Manifest == "" {
		t.Fatalf("rescue: %+v", rep)
	}
	home := filepath.Join(rig.g.root, "second-home")
	s := testSettings(t, home)
	s.RemoteBase = rig.s.RemoteBase
	s.TokenWait = 0
	s.SharedDir = rig.s.SharedDir
	return rig, rep, s, ExecRunner{BaseEnv: gitTestEnv(home)}, protocol.RescueID("demo-1006-170000", filepath.Base(rep.Bundle.Dir))
}

func TestRestoreRescueIntoANewSession(t *testing.T) {
	rig, rep, s, r, id := restoreRig(t)
	sess := cloneSession("demo-1008-090000")
	sess.Restore = id
	sess.Base = "refs/rescued/heads/" + rig.ws.Branch
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || !strings.Contains(strings.Join(step.Notes, "\n"), "restored 2 refs from rescue "+id) {
		t.Fatalf("restore: %s %q", step.State, step.Notes)
	}
	env := gitTestEnv(s.Home)
	if got := gitRun(t, env, ws.Worktree, "show", "HEAD:done.txt"); got != "done" {
		t.Errorf("the new worktree starts at %q, not the rescued branch", got)
	}
	w := rig.worktree(t, rep, rig.ws.Worktree)
	if got := gitRun(t, env, ws.Clone, "show", "refs/rescued/heads/"+w.RescueBranch+":notes/uncommitted.md"); got != "only on the volume" {
		t.Errorf("the rescue branch holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(ws.Worktree, "notes", "uncommitted.md")); !os.IsNotExist(err) {
		t.Errorf("the uncommitted work landed in the worktree; it stays on the rescue branch: %v", err)
	}

	// The next boot reuses the worktree and restores nothing again.
	_, step = PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK || strings.Contains(strings.Join(step.Notes, "\n"), "restored") {
		t.Errorf("second boot: %s %q", step.State, step.Notes)
	}
}

func TestRestoreRescueAfterFailedOriginFetch(t *testing.T) {
	for _, base := range []string{"rescued branch", "origin/main", "unrelated rescued ref", "mislabeled ref"} {
		t.Run(base, func(t *testing.T) {
			rig, rep, s, r, id := restoreRig(t)
			sess := cloneSession("demo-1009-restore")
			sess.Restore = id
			// Simulate the previous boot stopping after the clone was saved.
			if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
				t.Fatal(err)
			}
			switch base {
			case "rescued branch":
				sess.Base = "refs/rescued/heads/" + rig.ws.Branch
			case "origin/main":
				sess.Base = base
			case "unrelated rescued ref", "mislabeled ref":
				sess.Base = "refs/rescued/heads/unrelated"
				gitRun(t, gitTestEnv(s.Home), s.ClonePath(sess.Repo), "update-ref", sess.Base, "HEAD")
				if base == "mislabeled ref" {
					// A manifest naming an old local ref cannot stand in for a
					// ref actually held by the verified bundle.
					mp := filepath.Join(s.SharedDir, rep.Bundle.Manifest)
					m, err := readManifest(mp)
					if err != nil {
						t.Fatal(err)
					}
					m.Repos[0].Bundle.Refs[0].Name = "refs/heads/unrelated"
					if err := writeJSONFile(mp, m); err != nil {
						t.Fatal(err)
					}
				}
			}
			ws, step := PrepareRepo(context.Background(), failOriginFetch(r), s, sess)
			if base != "rescued branch" {
				if step.State != StepFail || exists(ws.Worktree) {
					t.Fatalf("stale base accepted: %s %q", step.State, step.Notes)
				}
				return
			}
			if step.State != StepWarn || !strings.Contains(strings.Join(step.Notes, "\n"), "verified rescue base") {
				t.Fatalf("%s %q", step.State, step.Notes)
			}
			if got := gitRun(t, gitTestEnv(s.Home), ws.Worktree, "show", "HEAD:done.txt"); got != "done" {
				t.Errorf("restored work = %q", got)
			}
		})
	}
}

func TestRestoreRefusesAnUnimportedBundleHead(t *testing.T) {
	rig, rep, s, r, id := restoreRig(t)
	sess := cloneSession("demo-1009-head")
	sess.Restore, sess.Base = id, "refs/rescued/HEAD"
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	gitRun(t, gitTestEnv(s.Home), s.ClonePath(sess.Repo), "update-ref", sess.Base, "HEAD")
	mp := filepath.Join(s.SharedDir, rep.Bundle.Manifest)
	m, err := readManifest(mp)
	if err != nil {
		t.Fatal(err)
	}
	b := &m.Repos[0].Bundle
	file := filepath.Join(s.SharedDir, b.File)
	// A valid bundle can advertise HEAD, but refs/*:refs/rescued/* cannot
	// import it. It must not authorize the stale refs/rescued/HEAD above.
	gitRun(t, rig.g.env, rig.ws.Worktree, "bundle", "create", "--quiet", file, "HEAD")
	b.Refs = []protocol.BundleRef{{Name: "HEAD", Source: "HEAD", Commit: gitRun(t, rig.g.env, rig.ws.Worktree, "rev-parse", "HEAD")}}
	b.SHA256, b.Size, err = hashFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(mp, m); err != nil {
		t.Fatal(err)
	}
	ws, step := PrepareRepo(context.Background(), failOriginFetch(r), s, sess)
	if step.State != StepFail || exists(ws.Worktree) || !strings.Contains(strings.Join(step.Notes, "\n"), "not a ref in this rescue's bundle") {
		t.Fatalf("unimported HEAD accepted: %s %q", step.State, step.Notes)
	}
}

func TestRestoreRefusesABundleThatDoesNotMatch(t *testing.T) {
	cases := map[string]struct {
		edit func(t *testing.T, s Settings, m *protocol.RescueManifest)
		want string
	}{
		"changed bytes": {func(t *testing.T, s Settings, m *protocol.RescueManifest) {
			p := filepath.Join(s.SharedDir, m.Repos[0].Bundle.File)
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)-1] ^= 0xff
			if err := os.WriteFile(p, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}, "SHA-256"},
		"incomplete": {func(_ *testing.T, _ Settings, m *protocol.RescueManifest) { m.Complete = false }, "did not complete"},
		"other repo": {func(_ *testing.T, _ Settings, m *protocol.RescueManifest) { m.Repos[0].Path = "/home/dev/repos/other" }, "holds no bundle for demo"},
		"escapes": {func(_ *testing.T, _ Settings, m *protocol.RescueManifest) {
			m.Repos[0].Bundle.File = "rescue/x/../../etc/passwd"
		}, "not inside"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, rep, s, r, id := restoreRig(t)
			mp := filepath.Join(s.SharedDir, rep.Bundle.Manifest)
			m, err := readManifest(mp)
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(t, s, &m)
			data, _ := json.Marshal(m)
			if err := os.WriteFile(mp, data, 0o600); err != nil {
				t.Fatal(err)
			}
			sess := cloneSession("demo-1008-090000")
			sess.Restore = id
			ws, step := PrepareRepo(context.Background(), r, s, sess)
			if step.State == StepOK || !strings.Contains(strings.Join(step.Notes, "\n"), tc.want) {
				t.Fatalf("restore: %s %q, want a failure naming %q", step.State, step.Notes, tc.want)
			}
			if _, err := os.Stat(ws.Worktree); !os.IsNotExist(err) {
				t.Errorf("a failed restore left a worktree: %v", err)
			}
		})
	}
}

func TestParseRescueID(t *testing.T) {
	for _, ok := range []string{"dev-env-1008-001530/20261008-0024", "a/20261008-0024-2"} {
		if _, _, err := protocol.ParseRescueID(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for ref, want := range map[string]bool{
		"refs/rescued/heads/rescue/s-20261008-0024": true, "rescued/heads/rescue/x": true,
		"refs/rescued/agentd-rescue/stash/0": true, "refs/rescued/heads/agent/s": false, "origin/main": false,
		"refs/heads/rescue/x": false,
	} {
		if got := protocol.IsRescueSnapshot(ref); got != want {
			t.Errorf("IsRescueSnapshot(%q) = %v", ref, got)
		}
	}
	sess := cloneSession("demo-1008-090000")
	sess.Base = "refs/rescued/heads/rescue/demo-1006-170000-20261006-1730"
	if err := sess.Validate(); err == nil {
		t.Error("a session document with a rescued snapshot as its base passed")
	}
	for _, bad := range []string{"", "x", "../20261008-0024", "a/../b", "a/20261008-0024/x", "A/20261008-0024", "a/2026", "a/20261008-0024-", "/20261008-0024"} {
		if _, _, err := protocol.ParseRescueID(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
