package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func deltaReq(t *testing.T, rep protocol.RescueReport, clone string) RescueMaterialize {
	t.Helper()
	rb := rep.Repos[0].Bundle
	var commit, ref string
	for _, one := range rb.Refs {
		if protocol.RescueWorkRef(one.Name) {
			commit, ref = one.Commit, one.Name
		}
	}
	if commit == "" || ref == "" {
		t.Fatalf("no rescue ref in %+v", rb.Refs)
	}
	return RescueMaterialize{
		ID: rep.Session + "/" + rep.Stamp, Repo: filepath.Base(rep.Repos[0].Path),
		Clone: clone, Bundle: rb.File, Commit: commit, Base: rb.BaseCommit, Ref: ref,
	}
}

func deltaClone(t *testing.T, rig rescueRig, partial bool) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "recipient")
	args := []string{"clone", "-q"}
	if partial {
		args = append(args, "--filter=blob:none")
	} else {
		args = append(args, "--no-local")
	}
	args = append(args, "file://"+rig.g.remote, dst)
	gitRun(t, rig.g.env, rig.g.root, args...)
	return dst
}

func TestRescueDeltaOriginAdvanced(t *testing.T) {
	rig := newRescueRig(t)
	g := rig.g
	writeFile(t, filepath.Join(g.seed, "other.txt"), "other-v2\n")
	gitRun(t, g.env, g.seed, "add", "other.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "B")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	tip := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "edited\n")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified || rb.BaseCommit != tip {
		t.Fatalf("base %s want %s ok %v", rb.BaseCommit, tip, rep.OK)
	}
	req := deltaReq(t, rep, "")
	if req.Base == "" {
		t.Fatal("no base")
	}
	for _, partial := range []bool{false, true} {
		recipient := deltaClone(t, rig, partial)
		gitRun(t, g.env, recipient, "remote", "set-url", "origin", "https://127.0.0.1:9/no.git")
		req.Clone = recipient
		got, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
		if err != nil || !got.OK || got.Prerequisite == "" || got.Prerequisite == rb.BaseCommit {
			t.Fatalf("partial %v %+v %v", partial, got, err)
		}
		var readme string
		for _, f := range got.Files {
			if f.Path == "README.md" {
				readme = string(f.Bytes)
			}
			if f.Path == "other.txt" {
				t.Fatalf("unchanged base file was exported: %+v", f)
			}
		}
		if readme != "edited\n" {
			t.Fatalf("partial %v readme %q files %d", partial, readme, len(got.Files))
		}
	}
}

func TestRescueDeltaMissingAndShallowBase(t *testing.T) {
	rig := newRescueRig(t)
	g := rig.g
	writeFile(t, filepath.Join(g.seed, "other.txt"), "other-v2\n")
	gitRun(t, g.env, g.seed, "add", "other.txt")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "B")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	tip := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "edited\n")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || rb.BaseCommit != tip {
		t.Fatalf("recorded base %v want %s", rb, tip)
	}
	empty := t.TempDir()
	gitRun(t, g.env, empty, "init", "-q")
	req := deltaReq(t, rep, empty)
	missing, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || missing.OK || missing.Code != protocol.RescueTreeMissingBase || len(missing.Files) != 0 {
		t.Fatalf("missing %+v %v", missing, err)
	}
	// Depth 1 of the advanced tip has the recorded base and not the older
	// bundle prerequisite. The dead remote must not be fetched.
	shallow := filepath.Join(t.TempDir(), "shallow")
	gitRun(t, g.env, g.root, "clone", "-q", "--depth", "1", "file://"+g.remote, shallow)
	gitRun(t, g.env, shallow, "remote", "set-url", "origin", "https://127.0.0.1:9/no.git")
	req.Clone = shallow
	got, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || got.OK || (got.Code != protocol.RescueTreeIncomplete && got.Code != protocol.RescueTreeMismatch) || len(got.Files) != 0 {
		t.Fatalf("shallow %+v %v", got, err)
	}
}

func TestRescueDeltaRejectsAlternatesAndConfigCommand(t *testing.T) {
	rig := newRescueRig(t)
	writeFile(t, filepath.Join(rig.ws.Worktree, "README.md"), "edited\n")
	rep := rig.rescue(t)
	req := deltaReq(t, rep, "")
	recipient := deltaClone(t, rig, true)
	req.Clone = recipient
	ext := filepath.Join(t.TempDir(), "external.git")
	gitRun(t, rig.g.env, rig.g.root, "init", "-q", "--bare", ext)
	marks := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipient, ".git", "objects", "info", "alternates"), []byte(filepath.Join(ext, "objects")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, rig.g.env, recipient, "config", "core.alternateRefsCommand", "touch "+filepath.Join(marks, "alternateRefsCommand")+"; true")
	gitRun(t, rig.g.env, recipient, "remote", "set-url", "origin", "https://127.0.0.1:9/no.git")
	got, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || got.OK || got.Code != protocol.RescueTreeRefused || len(got.Files) != 0 {
		t.Fatalf("alternates %+v %v", got, err)
	}
	if _, statErr := os.Stat(filepath.Join(marks, "alternateRefsCommand")); !os.IsNotExist(statErr) {
		t.Fatal("alternateRefsCommand ran")
	}

	plain := deltaClone(t, rig, false)
	gitRun(t, rig.g.env, plain, "config", "core.alternateRefsCommand", "touch "+filepath.Join(marks, "command-only")+"; true")
	req.Clone = plain
	got, err = MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	if err != nil || got.OK || got.Code != protocol.RescueTreeRefused {
		t.Fatalf("config command %+v %v", got, err)
	}
	if _, statErr := os.Stat(filepath.Join(marks, "command-only")); !os.IsNotExist(statErr) {
		t.Fatal("config command ran")
	}
}

func TestRescueDeltaFanOutStopsBeforeTheCap(t *testing.T) {
	const n = 1000
	rig := newRescueRig(t)
	g, wt := rig.g, rig.ws.Worktree
	empty := strings.TrimSpace(deltaGitIn(t, g.env, wt, "", "hash-object", "-w", "--stdin"))
	var l2 strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&l2, "100644 blob %s\tf%04d\n", empty, i)
	}
	t2 := strings.TrimSpace(deltaGitIn(t, g.env, wt, l2.String(), "mktree"))
	var l1 strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&l1, "040000 tree %s\td%04d\n", t2, i)
	}
	t1 := strings.TrimSpace(deltaGitIn(t, g.env, wt, l1.String(), "mktree"))
	head := gitRun(t, g.env, wt, "rev-parse", "HEAD")
	c := strings.TrimSpace(deltaGitIn(t, g.env, wt, "", "commit-tree", t1, "-p", head, "-m", "fan-out"))
	gitRun(t, g.env, wt, "update-ref", "refs/heads/rescue/crafted", c)

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	rep := rig.rescue(t)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified || rb.TreeRefused != protocol.TreeTooLarge {
		t.Fatalf("rescue %+v", rb)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("rescue allocated %d MiB", grew>>20)
	}
	t.Logf("rescue fan-out TotalAlloc +%d MiB in %s", (after.TotalAlloc-before.TotalAlloc)>>20, time.Since(start).Round(time.Millisecond))

	recipient := deltaClone(t, rig, true)
	req := deltaReq(t, rep, recipient)
	req.Commit = c
	req.Ref = "refs/heads/rescue/crafted"
	runtime.GC()
	runtime.ReadMemStats(&before)
	got, err := MaterializeRescueTree(context.Background(), rig.r, rig.s, req)
	runtime.ReadMemStats(&after)
	if err != nil || got.OK || got.Code != protocol.TreeTooLarge || len(got.Files) != 0 {
		t.Fatalf("read %+v %v", got, err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("read allocated %d MiB", grew>>20)
	}
	t.Logf("read fan-out TotalAlloc +%d MiB code %s", (after.TotalAlloc-before.TotalAlloc)>>20, got.Code)
}

func TestRescueDeltaDevEnvSizedBase(t *testing.T) {
	root := strings.TrimSpace(gitRun(t, os.Environ(), ".", "rev-parse", "--show-toplevel"))
	g := newGitFixture(t, "demo")
	archive := exec.Command("git", "-C", root, "archive", "--format=tar", "HEAD")
	extract := exec.Command("tar", "-x", "-C", g.seed)
	out, err := archive.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	extract.Stdin = out
	if err := extract.Start(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Start(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := extract.Wait(); err != nil {
		t.Fatal(err)
	}
	gitRun(t, g.env, g.seed, "add", "-A")
	gitRun(t, g.env, g.seed, "commit", "-q", "-m", "dev-env tree")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "HEAD:main")
	s, r := g.settings(t)
	ws, step := PrepareRepo(context.Background(), r, s, cloneSession("demo-1006-170000"))
	if step.State != StepOK {
		t.Fatalf("prepare: %q", step.Notes)
	}
	rig := rescueRig{g: g, s: s, r: r, ws: ws}
	rig.sharedVolume(t)
	f, err := os.OpenFile(filepath.Join(ws.Worktree, "README.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\nOne line of WIP.\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(ws.Worktree, "notes", "wip.md"), "a small untracked note\n")
	rep := rig.rescue(t)
	rb := rep.Repos[0].Bundle
	if !rep.OK || rb == nil || !rb.Verified {
		t.Fatalf("rescue %+v", rb)
	}
	info, err := os.Stat(filepath.Join(s.SharedDir, filepath.FromSlash(rb.File)))
	if err != nil {
		t.Fatal(err)
	}
	recipient := filepath.Join(t.TempDir(), "recipient")
	gitRun(t, g.env, g.root, "clone", "-q", "--filter=blob:none", "file://"+g.remote, recipient)
	gitRun(t, g.env, recipient, "remote", "set-url", "origin", "https://127.0.0.1:9/no.git")
	got, err := MaterializeRescueTree(context.Background(), r, s, deltaReq(t, rep, recipient))
	body, _ := json.Marshal(got)
	if err != nil || !got.OK || len(got.Files) > 8 || len(body) > 1<<20 {
		t.Fatalf("delta ok=%v code=%q files=%d response=%d err=%v", got.OK, got.Code, len(got.Files), len(body), err)
	}
	var readme, note string
	for _, file := range got.Files {
		switch file.Path {
		case "README.md":
			readme = string(file.Bytes)
		case "notes/wip.md":
			note = string(file.Bytes)
		}
	}
	if !strings.Contains(readme, "One line of WIP.\n") || note != "a small untracked note\n" {
		t.Fatalf("wip readme %q note %q", readme, note)
	}
	t.Logf("thin bundle %d bytes, response %d bytes, files %d", info.Size(), len(body), len(got.Files))
}

func deltaGitIn(t *testing.T, env []string, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}
