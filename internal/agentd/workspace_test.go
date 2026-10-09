package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func workspaceMountTable(home string) []byte {
	var lines []string
	lines = append(lines, fmt.Sprintf("1 0 8:1 / %s rw - ext4 private rw", home))
	for i, sub := range []string{"metadata", "repos", "codex", "work"} {
		path := sub
		if sub == "metadata" {
			path = ".workspace"
		}
		lines = append(lines, fmt.Sprintf("%d 1 0:42 /retained/%s %s/%s rw - ceph workspace rw", i+2, sub, home, path))
	}
	return []byte(strings.Join(lines, "\n"))
}

func sharedSettings(t *testing.T, s Settings, task string) (Settings, protocol.Session) {
	t.Helper()
	s.WorkspaceID, s.PodUID = "test-workspace", "pod-1"
	sess := cloneSession(task)
	sess.Workspace = &protocol.WorkspaceBinding{ID: s.WorkspaceID, SessionUID: "session-1"}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	s.Getenv = func(key string) string {
		if key == protocol.SessionEnv {
			return string(data)
		}
		return ""
	}
	for _, p := range []string{s.workspaceDir(), s.ReposDir(), filepath.Join(s.Home, "codex"), s.WorkDir(), s.StateDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(s.workspaceDir(), "marker.json"), `{"version":1,"id":"test-workspace"}`)
	old := readWorkspaceMountInfo
	readWorkspaceMountInfo = func() ([]byte, error) { return workspaceMountTable(s.Home), nil }
	t.Cleanup(func() { readWorkspaceMountInfo = old })
	return s, sess
}

func TestWorkspaceMountIdentityRefusesFallbackAndForeignSubpaths(t *testing.T) {
	home := "/home/dev"
	good := string(workspaceMountTable(home))
	if err := verifyWorkspaceMounts(home, []byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"plain work directory": strings.Replace(good, "5 1 0:42 /retained/work /home/dev/work rw - ceph workspace rw", "", 1),
		"foreign claim":        strings.Replace(good, "0:42 /retained/work", "0:43 /retained/work", 1),
		"wrong subpath":        strings.Replace(good, "/retained/repos", "/retained/work", 1),
		"private home":         strings.Replace(good, "8:1", "0:42", 1),
		"stacked mount":        good + "\n2 1 0:42 /retained/metadata /home/dev/.workspace rw - ceph workspace rw",
	} {
		t.Run(name, func(t *testing.T) {
			if err := verifyWorkspaceMounts(home, []byte(bad)); err == nil {
				t.Fatal("accepted uncertain mount identity")
			}
		})
	}
}

func TestWorkspaceWriterRefusesUncertainTakeoverAndDifferentSession(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("second writer acquired a held task")
	}
	w.unlock()
	other := s
	other.PodUID = "pod-2"
	if _, err := acquireWorkspaceWriter(context.Background(), r, other, sess, rescueNow); err == nil {
		t.Fatal("free flock was accepted as old Pod stop proof")
	}
	old := w.owner
	old.State, old.StoppedAt, old.StopReason = "stopped", rescueNow, "no-agent-admitted"
	if err := writeWorkspaceJSON(s.ownerPath(sess.Name), old); err != nil {
		t.Fatal(err)
	}
	foreign := sess
	foreign.Workspace = &protocol.WorkspaceBinding{ID: s.WorkspaceID, SessionUID: "session-2"}
	if _, err := acquireWorkspaceWriter(context.Background(), r, other, foreign, rescueNow); err == nil {
		t.Fatal("a different AgentSession UID took over a stopped record")
	}
	w2, err := acquireWorkspaceWriter(context.Background(), r, other, sess, rescueNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer w2.unlock()
	if w2.owner.Generation != 2 || w2.owner.PodUID != "pod-2" || w2.owner.State != "owned" || w2.owner.StopReason != "" {
		t.Fatalf("resumed owner %+v", w2.owner)
	}
}

func TestWorkspaceAdmissionMarkerSymlinkAndPrivateWIP(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	marker := filepath.Join(s.workspaceDir(), "marker.json")
	writeFile(t, marker, `{"version":1,"id":"another-workspace"}`)
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("foreign marker admitted")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(g.root, "missing"), marker); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("symlink marker admitted")
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	writeFile(t, marker, `{"version":1,"id":"test-workspace"}`)
	writeFile(t, s.statePath(launchFile), `{}`)
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("private launch state adopted")
	}
	if exists(s.ownerPath(sess.Name)) {
		t.Fatal("refused admission created an owner")
	}
}

func TestWorkspaceOwnerReceiptRequiresExplicitAdmissionFields(t *testing.T) {
	owner := taskOwner{Version: 1, Workspace: "w", Task: "task", Repo: "demo", Clone: "/home/dev/repos/demo", Worktree: "/home/dev/work/task", SessionUID: "s", PodUID: "p", Generation: 1, State: "owned", UpdatedAt: rescueNow}
	data, err := json.Marshal(owner)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "launched")
	data, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, new(taskOwner)); err == nil {
		t.Fatal("missing launched field silently became proof of no launch")
	}
}

func TestSharedRescueSnapshotsOnlyOwnedTaskAndRetainsDaemonProtection(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	s.writer = w
	ws, step := PrepareRepo(context.Background(), r, s, sess)
	if step.State != StepOK {
		t.Fatalf("prepare %v", step.Notes)
	}
	peer := s.WorktreePath("task-a-peer")
	gitRun(t, g.env, ws.Clone, "worktree", "add", "-q", "-b", "agent/task-a-peer", peer, "origin/main")
	writeFile(t, filepath.Join(peer, "peer.txt"), "peer commit")
	gitRun(t, g.env, peer, "add", "peer.txt")
	gitRun(t, g.env, peer, "commit", "-q", "-m", "peer")
	peerHead := gitRun(t, g.env, peer, "rev-parse", "HEAD")
	gitRun(t, g.env, ws.Clone, "update-ref", "refs/heads/rescue/task-a-peer/old", peerHead)
	writeFile(t, filepath.Join(peer, "README.md"), "peer dirty")
	peerStatus := gitRun(t, g.env, peer, "status", "--porcelain")
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "task dirty")
	before := gitRun(t, g.env, ws.Worktree, "status", "--porcelain")
	rig := rescueRig{g: g, s: s, r: r, ws: ws}
	rig.sharedVolume(t)
	rep, err := Rescue(context.Background(), r, s, sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Bundle == nil || rep.Bundle.Error != "" || len(rep.Repos) != 1 || len(rep.Repos[0].Worktrees) != 1 {
		t.Fatalf("report %+v", rep)
	}
	for _, ref := range rep.Repos[0].Bundle.Refs {
		if ref.Source != "refs/heads/agent/task-a" && !strings.HasPrefix(ref.Source, "refs/heads/rescue/task-a/") {
			t.Fatalf("peer ref bundled %+v", ref)
		}
	}
	if gitRun(t, g.env, peer, "status", "--porcelain") != peerStatus || gitRun(t, g.env, peer, "rev-parse", "HEAD") != peerHead {
		t.Fatal("rescue touched its peer")
	}
	if gitRun(t, g.env, ws.Worktree, "status", "--porcelain") != before {
		t.Fatal("rescue changed owned worktree or index")
	}
	var stopped taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" || stopped.StoppedAt.IsZero() {
		t.Fatalf("no durable receipt %+v", stopped)
	}
	if _, err := workspaceLock(s, "writer-"+sess.Name); err == nil {
		t.Fatal("rescue released the daemon's writer lock")
	}
	if err := admitWorkspaceLaunch(s, sess, &Launch{}, rescueNow); err == nil {
		t.Fatal("stopped task launched after rescue")
	}
}

func TestSharedLaunchedRescueRefusesAndPreservesReceipt(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	s.writer = w
	if err := admitWorkspaceLaunch(s, sess, &Launch{}, rescueNow); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.ownerPath(sess.Name))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Rescue(context.Background(), r, s, sess.Name, rescueNow, RescueOptions{StopAgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Agent == nil || !rep.Agent.Running || rep.Bundle != nil {
		t.Fatalf("uncertain stop accepted %+v", rep)
	}
	after, err := os.ReadFile(s.ownerPath(sess.Name))
	if err != nil || string(before) != string(after) {
		t.Fatal("refused rescue changed durable ownership")
	}
}

func TestSharedPrepareNeverPrunesMissingPeerRegistrations(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	s.writer = w
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	peer := s.WorktreePath("peer")
	gitRun(t, g.env, s.ClonePath(sess.Repo), "worktree", "add", "-q", "-b", "agent/peer", peer, "origin/main")
	if err := os.RemoveAll(peer); err != nil {
		t.Fatal(err)
	}
	f := &fakeRunner{handle: func(c Cmd) (Result, error) { return r.Run(context.Background(), c) }}
	_, step := PrepareRepo(context.Background(), f, s, sess)
	if step.State != StepOK {
		t.Fatalf("prepare %v", step.Notes)
	}
	for _, c := range f.calls {
		if slices.Equal(c.Args[2:], []string{"worktree", "prune"}) {
			t.Fatal("shared preparation ran global prune")
		}
	}
	if !strings.Contains(gitRun(t, g.env, s.ClonePath(sess.Repo), "worktree", "list", "--porcelain"), peer) {
		t.Fatal("missing peer registration pruned")
	}
}
