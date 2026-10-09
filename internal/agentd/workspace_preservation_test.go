package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func preparationHold(t *testing.T, s Settings, sess protocol.Session) (Settings, *protocol.WorkspaceStopProof) {
	t.Helper()
	p := fixtureWorkspaceProof(sess, s.PodUID, rescueNow)
	hold := s
	hold.PodUID, hold.writer = "hold-1", nil
	bound := *sess.Workspace
	bound.StopProof = p
	sess.Workspace = &bound
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	hold.Getenv = func(key string) string {
		if key == protocol.SessionEnv {
			return string(data)
		}
		return ""
	}
	return hold, p
}

func TestWorkspaceNoWorkPreservationResumesAbsentPreparation(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "no owner", true: "unlaunched owner"}[admitted], func(t *testing.T) {
			g := newGitFixture(t, "demo")
			s, r := g.settings(t)
			s, sess := sharedSettings(t, s, "task-a")
			if admitted {
				w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
				if err != nil {
					t.Fatal(err)
				}
				w.unlock()
			} else {
				marker := filepath.Join(s.workspaceDir(), "marker.json")
				writeFile(t, marker, `{"version":1,"id":"foreign"}`)
				if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
					t.Fatal("fixture admitted foreign mount")
				}
				writeFile(t, marker, `{"version":1,"id":"test-workspace"}`)
			}
			hold, proof := preparationHold(t, s, sess)
			rep, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof})
			if err != nil {
				t.Fatal(err)
			}
			p := rep.WorkspacePreservation
			if !rep.OK || rep.CleanAndPushed || rep.VolumeEmpty || rep.Bundle != nil || p == nil || p.Kind != "NoWorkAdmitted" ||
				(p.OwnerGeneration == 0) == admitted || !rep.Repos[0].Absent || !rep.Repos[0].Worktrees[0].Absent {
				t.Fatalf("report %+v", rep)
			}
			if !admitted && exists(s.ownerPath(sess.Name)) {
				t.Fatal("generation-zero preservation invented an owner")
			}
			resume := s
			resume.PodUID = "pod-2"
			w, err := acquireWorkspaceWriter(context.Background(), r, resume, sess, rescueNow.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			defer w.unlock()
			resume.writer = w
			ws, step := PrepareRepo(context.Background(), r, resume, sess)
			if step.State != StepOK || ws.Head == "" || w.owner.Generation != p.OwnerGeneration+1 {
				t.Fatalf("recovery generation=%d workspace=%+v step=%+v", w.owner.Generation, ws, step)
			}
		})
	}
}

func TestWorkspaceAbsentWorktreeBundlesAllOwnedRefsAndPreservesPeer(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	clone := s.ClonePath(sess.Repo)
	// Both owned refs already exist on origin/main. A thin unpushed-only
	// rescue would have nothing to bundle, so this requires a full bundle.
	for _, ref := range []string{"refs/heads/agent/task-a", "refs/heads/rescue/task-a/preparation"} {
		gitRun(t, g.env, clone, "update-ref", ref, "HEAD")
	}
	peer := s.WorktreePath("task-b")
	gitRun(t, g.env, clone, "worktree", "add", "-q", "-b", "agent/task-b", peer, "origin/main")
	writeFile(t, filepath.Join(peer, "README.md"), "peer unchanged")
	before := gitRun(t, g.env, peer, "status", "--porcelain")
	rescueRig{g: g, s: s, r: r}.sharedVolume(t)
	w.unlock()
	hold, proof := preparationHold(t, s, sess)
	rep, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.CleanAndPushed || rep.WorkspacePreservation == nil || rep.WorkspacePreservation.Kind != "OwnedRefsPreserved" ||
		len(rep.Repos[0].UnpushedRefs) != 2 || rep.Repos[0].Bundle == nil || !rep.Repos[0].Bundle.Verified || rep.Repos[0].Bundle.Base != "" {
		t.Fatalf("report %+v", rep)
	}
	for _, ref := range rep.Repos[0].Bundle.Refs {
		if ref.Source != "refs/heads/agent/task-a" && ref.Source != "refs/heads/rescue/task-a/preparation" {
			t.Fatal("peer ref bundled", ref)
		}
	}
	if got := gitRun(t, g.env, peer, "status", "--porcelain"); got != before {
		t.Fatal("peer state changed")
	}
	resume := s
	resume.PodUID = "pod-2"
	w2, err := acquireWorkspaceWriter(context.Background(), r, resume, sess, rescueNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer w2.unlock()
	resume.writer = w2
	ws, step := PrepareRepo(context.Background(), r, resume, sess)
	if step.State != StepOK || ws.Head == "" {
		t.Fatalf("owned branch recovery: %+v", step)
	}
}

func TestWorkspacePreparationRefusesPartialAndAmbiguousState(t *testing.T) {
	for _, kind := range []string{"partial worktree", "staging clone", "foreign owner", "missing marker", "write started", "checking", "private launch"} {
		t.Run(kind, func(t *testing.T) {
			g := newGitFixture(t, "demo")
			s, r := g.settings(t)
			s, sess := sharedSettings(t, s, "task-a")
			w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
			if err != nil {
				t.Fatal(err)
			}
			w.unlock()
			switch kind {
			case "partial worktree":
				writeFile(t, filepath.Join(s.WorktreePath(sess.Name), "partial"), "retained")
			case "staging clone":
				writeFile(t, filepath.Join(s.ReposDir(), ".demo.agentd-clone", "partial"), "retained")
			case "foreign owner":
				owner := w.owner
				owner.SessionUID = "peer"
				if err := writeWorkspaceJSON(s.ownerPath(sess.Name), owner); err != nil {
					t.Fatal(err)
				}
			case "missing marker":
				if err := os.Remove(s.statePath(workspaceAdmissionFile)); err != nil {
					t.Fatal(err)
				}
			case "private launch":
				writeFile(t, s.statePath(launchFile), `{}`)
			default:
				phase := "OwnerWriteStarted"
				if kind == "checking" {
					phase = "Checking"
				}
				if err := writeAdmissionPhase(s, sess, phase, rescueNow); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(s.ownerPath(sess.Name))
			if err != nil {
				t.Fatal(err)
			}
			hold, proof := preparationHold(t, s, sess)
			if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof}); err == nil {
				t.Fatal("uncertain preparation became a stopped receipt")
			}
			after, _ := os.ReadFile(s.ownerPath(sess.Name))
			if string(before) != string(after) {
				t.Fatal("refused rescue changed shared ownership")
			}
		})
	}
}

func TestWorkspaceAdmissionWriteFailureCannotBecomeRefused(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	prior := writeWorkspaceAdmission
	writeWorkspaceAdmission = func(path string, value any) error {
		if a := value.(workspaceAdmission); a.Phase == "OwnerAdmitted" {
			return errors.New("private result publication failed")
		}
		return prior(path, value)
	}
	t.Cleanup(func() { writeWorkspaceAdmission = prior })
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("failed final private marker reported admission success")
	}
	var a workspaceAdmission
	if err := readWorkspaceJSON(s.statePath(workspaceAdmissionFile), &a); err != nil {
		t.Fatal(err)
	}
	if a.Phase != "OwnerWriteStarted" || !exists(s.ownerPath(sess.Name)) {
		t.Fatalf("ambiguous result %+v", a)
	}
	writeWorkspaceAdmission = prior
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("same Pod downgraded ambiguous owner admission")
	}
	hold, proof := preparationHold(t, s, sess)
	if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof}); err == nil {
		t.Fatal("ambiguous owner write became no-work proof")
	}
}

func TestWorkspaceHoldProofClockSkewHasStrictBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delta time.Duration
		ok    bool
	}{
		{"future boundary", 5 * time.Second, true},
		{"future beyond boundary", 5*time.Second + time.Nanosecond, false},
		{"age boundary", -40 * time.Second, true},
		{"age beyond boundary", -40*time.Second - time.Nanosecond, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGitFixture(t, "demo")
			s, r := g.settings(t)
			s, sess := sharedSettings(t, s, "task-a")
			w, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow)
			if err != nil {
				t.Fatal(err)
			}
			w.unlock()
			hold, proof := preparationHold(t, s, sess)
			fresh := *proof
			fresh.VerifiedAt = rescueNow.Add(tc.delta)
			fresh.LeaseRenewedAt = fresh.VerifiedAt.Add(-time.Second)
			rep, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: &fresh})
			if tc.ok && (err != nil || !rep.OK) || !tc.ok && err == nil {
				t.Fatalf("ok=%t report=%+v err=%v", tc.ok, rep, err)
			}
		})
	}
}

func TestWorkspaceSharedClonePreservesPreexistingAndFailedStaging(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		t.Run(map[bool]string{false: "failed clone", true: "preexisting staging"}[preexisting], func(t *testing.T) {
			g := newGitFixture(t, "demo")
			s, _ := g.settings(t)
			s.WorkspaceID = "shared"
			tmp := filepath.Join(s.ReposDir(), ".demo.agentd-clone")
			if preexisting {
				writeFile(t, filepath.Join(tmp, "partial"), "retained")
			}
			calls := 0
			r := &fakeRunner{handle: func(Cmd) (Result, error) {
				calls++
				writeFile(t, filepath.Join(tmp, "partial"), "retained")
				return Result{}, errors.New("interrupted clone")
			}}
			if _, err := cloneRepo(context.Background(), r, s, "demo", s.ClonePath("demo")); err == nil {
				t.Fatal("partial shared clone reported success")
			}
			wantCalls := 1
			if preexisting {
				wantCalls = 0
			}
			data, err := os.ReadFile(filepath.Join(tmp, "partial"))
			if err != nil || string(data) != "retained" || calls != wantCalls {
				t.Fatalf("partial data=%q err=%v calls=%d", data, err, calls)
			}
		})
	}
}

func TestWorkspacePreOwnerRefsCannotBecomeNoWorkProof(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, r := g.settings(t)
	s, sess := sharedSettings(t, s, "task-a")
	if _, err := cloneRepo(context.Background(), r, s, sess.Repo, s.ClonePath(sess.Repo)); err != nil {
		t.Fatal(err)
	}
	clone := s.ClonePath(sess.Repo)
	gitRun(t, g.env, clone, "update-ref", "refs/heads/agent/task-a", "HEAD")
	before := gitRun(t, g.env, clone, "show-ref")
	if _, err := acquireWorkspaceWriter(context.Background(), r, s, sess, rescueNow); err == nil {
		t.Fatal("unowned branch admitted")
	}
	hold, proof := preparationHold(t, s, sess)
	if _, err := Rescue(context.Background(), r, hold, sess.Name, rescueNow, RescueOptions{StopAgent: true, WorkspaceStopProof: proof}); err == nil {
		t.Fatal("unowned refs became NoWorkAdmitted")
	}
	if exists(s.ownerPath(sess.Name)) || gitRun(t, g.env, clone, "show-ref") != before {
		t.Fatal("unowned refs or ownership changed")
	}
}
