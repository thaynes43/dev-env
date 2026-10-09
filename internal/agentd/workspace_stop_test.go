package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func fixtureWorkspaceProof(sess protocol.Session, podUID string, now time.Time) *protocol.WorkspaceStopProof {
	return &protocol.WorkspaceStopProof{Version: 1, Workspace: sess.Workspace.ID, Task: sess.Name, SessionUID: sess.Workspace.SessionUID, PodName: sess.Name, PodUID: podUID,
		PodResourceVersion: "10", NodeName: "worker-a", NodeUID: "node-1", LeaseResourceVersion: "20", LeaseRenewedAt: now.Add(-time.Second), VerifiedAt: now}
}

func TestWorkspaceStopRequestIsScopedDurableAndDeniesLatePane(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	s.writer = w
	launch := Launch{Session: sess.Name, Dir: s.WorktreePath(sess.Name)}
	if err := admitWorkspaceLaunch(context.Background(), s, sess, &launch); err != nil {
		t.Fatal(err)
	}
	if err := RequestWorkspaceStop(context.Background(), s, sess, rescueNow); err != nil {
		t.Fatal(err)
	}
	if stop, err := workspaceStopRequested(s, sess); err != nil || !stop {
		t.Fatalf("stop=%t, err=%v", stop, err)
	}
	if err := finalizeWorkspaceLaunch(s, sess, launch, rescueNow.Add(time.Second)); err == nil {
		t.Fatal("late pane started after serialized stop request")
	}
	var before, after workspaceStopRequest
	if err := readWorkspaceJSON(s.statePath(workspaceStopRequestFile), &before); err != nil {
		t.Fatal(err)
	}
	if err := RequestWorkspaceStop(context.Background(), s, sess, rescueNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := readWorkspaceJSON(s.statePath(workspaceStopRequestFile), &after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("duplicate stop request rewrote its durable receipt")
	}
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		t.Fatal(err)
	}
	if !sameOwner(owner, w.owner) {
		t.Fatal("stop request claimed proof of stopped writers")
	}
	newPod := s
	newPod.PodUID = "pod-2"
	if stop, err := workspaceStopRequested(newPod, sess); err != nil || stop {
		t.Fatal("old private stop request stopped the new Pod generation")
	}
	before.Generation++
	if err := writeWorkspaceJSON(s.statePath(workspaceStopRequestFile), before); err != nil {
		t.Fatal(err)
	}
	if _, err := workspaceStopRequested(s, sess); err == nil {
		t.Fatal("wrong generation stop request accepted")
	}
}

func TestWorkspaceStopRequestRespectsCanceledAdminWait(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	unlock, err := workspaceAdminLock(context.Background(), s, sess.Repo)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RequestWorkspaceStop(ctx, s, sess, rescueNow); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if exists(s.statePath(workspaceStopRequestFile)) {
		t.Fatal("cancelled stop admission wrote a request")
	}
}

func TestWorkspaceHoldRescueNeedsFreshProofAndPreservesPeers(t *testing.T) {
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
	writeFile(t, filepath.Join(peer, "README.md"), "peer dirty")
	peerStatus := gitRun(t, g.env, peer, "status", "--porcelain")
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "owned dirty")
	owner := w.owner
	owner.Launched = true
	if err := writeWorkspaceJSON(s.ownerPath(sess.Name), owner); err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.statePath(launchFile), `{}`)
	rig := rescueRig{g: g, s: s, r: r, ws: ws}
	rig.sharedVolume(t)
	w.unlock()
	proof := fixtureWorkspaceProof(sess, s.PodUID, rescueNow)
	hold := s
	hold.PodUID = "hold-1"
	hold.writer = nil
	holdSession := sess
	binding := *sess.Workspace
	binding.StopProof = proof
	holdSession.Workspace = &binding
	data, err := json.Marshal(holdSession)
	if err != nil {
		t.Fatal(err)
	}
	hold.Getenv = func(key string) string {
		if key == protocol.SessionEnv {
			return string(data)
		}
		return ""
	}
	if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true}); err == nil {
		t.Fatal("immutable hold environment was accepted as fresh proof")
	}
	stale := *proof
	stale.VerifiedAt = rescueNow.Add(-time.Minute)
	stale.LeaseRenewedAt = stale.VerifiedAt.Add(-time.Second)
	if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: &stale}); err == nil {
		t.Fatal("stale fresh-input proof accepted")
	}
	foreign := *proof
	foreign.PodUID = "foreign-executor"
	if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: &foreign}); err == nil {
		t.Fatal("foreign executor proof accepted")
	}
	rep, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.SourcePodUID != owner.PodUID || rep.Bundle == nil || rep.Bundle.Error != "" {
		t.Fatalf("report %+v", rep)
	}
	var stopped taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &stopped); err != nil {
		t.Fatal(err)
	}
	if stopped.State != "stopped" || stopped.StopReason != "terminated-pod-rescue" || stopped.StopProof == nil || stopped.StopProof.PodUID != owner.PodUID || !stopped.Launched {
		t.Fatalf("stopped receipt %+v", stopped)
	}
	if gitRun(t, g.env, peer, "status", "--porcelain") != peerStatus {
		t.Fatal("hold rescue changed its peer")
	}
	for _, ref := range rep.Repos[0].Bundle.Refs {
		if ref.Source != "refs/heads/agent/"+sess.Name && !strings.HasPrefix(ref.Source, "refs/heads/rescue/"+sess.Name+"/") {
			t.Fatalf("peer ref in owned bundle: %+v", ref)
		}
	}
	resume := s
	resume.PodUID = "pod-2"
	resume.writer = nil
	resumed, err := acquireWorkspaceWriter(context.Background(), r, resume, sess, rescueNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.unlock()
	if resumed.owner.Generation != owner.Generation+1 || resumed.owner.StopProof != nil || resumed.owner.Launched {
		t.Fatalf("new generation %+v", resumed.owner)
	}
}

func TestWorkspaceDaemonStopsWithoutClaimingStoppedProof(t *testing.T) {
	r := newDaemonRig(t, nil)
	s, sess := sharedSettings(t, r.d.S, r.d.Session.Name)
	r.d.S, r.d.Session = s, sess
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.d.Run(ctx) }()
	waitFor(t, func() bool { st, _ := r.beats.last(); return st.Boot == protocol.BootFailed })
	if err := RequestWorkspaceStop(context.Background(), s, sess, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor ignored exact stop request")
	}
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		t.Fatal(err)
	}
	if owner.State != "owned" || owner.StopProof != nil {
		t.Fatal("local shutdown marked escaped descendants stopped")
	}
	if r.tmuxStarted() {
		t.Fatal("stop fixture spawned an agent")
	}
}
