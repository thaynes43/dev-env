package controller

// DESIGN-061's refusals and protections against the fake client: each refusal
// of section 3 and each protected group of section 5 keeps the Session and
// its HOME; the release check of 1.1 on the same node, another node, a node
// gone and a node with a new UID; a tampered or missing bundle at the second
// verify; and a bundle record at every reader of Expiry.Archive.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	utilexec "k8s.io/client-go/util/exec"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

// refusedWorktree is a report whose one worktree the rescue refused, with the
// strict check passed: verdict's WorktreeRefused.
func refusedWorktree(session, why string) protocol.RescueReport {
	rep := cleanRescue(session, "20261111-1200")
	rep.OK, rep.CleanAndPushed, rep.StrictHome = false, false, &protocol.StrictHomeCheck{}
	rep.Repos[0].Worktrees = []protocol.WorktreeRescue{{Path: "/home/dev/work/" + session, Refused: why}}
	return rep
}

// TestTaskCleanupRescueRefusalsKeepTheHome is section 3's table, a case per
// refusal: a merge, rebase, cherry-pick or revert in progress; an untracked
// nested repository; over 50 MiB untracked; a submodule off its commit; a stray
// repository (--strict-home); and a clone missing from the report. Each is
// rescue_refused: the hold pod goes, the slot frees, the HOME and the Session
// stay, nothing reaches Archived, and the breaker counts none of them but a
// report without the strict check (an agent image from before it). A day
// later the record goes back into the queue, last, and a new hold pod
// rescues again.
func TestTaskCleanupRescueRefusalsKeepTheHome(t *testing.T) {
	for _, tc := range []struct {
		name, says string
		breaker    bool
		report     func(session string) protocol.RescueReport
	}{
		{name: "merge in progress", says: "a merge is in progress (MERGE_HEAD)", report: func(s string) protocol.RescueReport {
			return refusedWorktree(s, "a merge is in progress (MERGE_HEAD)")
		}},
		{name: "rebase in progress", says: "a rebase is in progress", report: func(s string) protocol.RescueReport {
			return refusedWorktree(s, "a rebase is in progress (rebase-merge)")
		}},
		{name: "untracked nested repository", says: "untracked nested repository", report: func(s string) protocol.RescueReport {
			return refusedWorktree(s, "untracked nested repository vendor/lib")
		}},
		{name: "over 50 MiB untracked", says: "more than 50 MiB", report: func(s string) protocol.RescueReport {
			return refusedWorktree(s, "untracked files total more than 50 MiB")
		}},
		{name: "submodule off its commit", says: "submodule", report: func(s string) protocol.RescueReport {
			return refusedWorktree(s, "submodule libs/x is not at its recorded commit")
		}},
		{name: "stray repository", says: "stray repository /home/dev/work/side-project", report: func(s string) protocol.RescueReport {
			return protocol.RescueReport{Session: s, Stamp: "20261111-1200", Agent: &protocol.AgentStop{}, Repos: []protocol.RepoRescue{},
				StrictHome: &protocol.StrictHomeCheck{Stray: []string{"/home/dev/work/side-project"}, Refused: "stray repository /home/dev/work/side-project"}}
		}},
		{name: "clone missing", says: "is not in the report", report: func(s string) protocol.RescueReport {
			return protocol.RescueReport{Session: s, Stamp: "20261111-1200", OK: true, CleanAndPushed: true, Agent: &protocol.AgentStop{}, StrictHome: &protocol.StrictHomeCheck{},
				Repos: []protocol.RepoRescue{{Path: "/home/dev/repos/other", Fetched: true}}}
		}},
		{name: "no strict check in the report", says: "ran without the strict HOME check", breaker: true, report: func(s string) protocol.RescueReport {
			return cleanRescue(s, "20261111-1200")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, nil)
			f.run(t, 2)
			hold := f.toHold(t)
			f.report(func(p *corev1.Pod) (protocol.RescueReport, error) { return tc.report(p.Name), nil })
			f.run(t, 6)
			e := f.expiry(t)
			if e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.Refusal != expiryRescueRefused || e.Bundle != nil {
				t.Fatalf("record %+v; want rescue_refused at Rescuing", e)
			}
			if r := f.session(t).Status.Rescue; r == nil || r.Result != v1alpha1.RescueFailed || !strings.Contains(r.Message, tc.says) {
				t.Fatalf("status.rescue %+v; want a failure that says %q", r, tc.says)
			}
			if p := f.named(t); p != nil {
				t.Fatalf("the hold pod %s stays after the refusal", p.UID)
			}
			f.kept(t)
			if taskActive(f.session(t)) {
				t.Fatal("a refused record holds its slot")
			}
			if want := map[bool]int{false: 0, true: 1}[tc.breaker]; f.r.taskCleanup.failures != want {
				t.Fatalf("the breaker counted %d, want %d", f.r.taskCleanup.failures, want)
			}
			if v := terminalCount(f.taskCounters.Refused, expiryRescueRefused); v != 1 {
				t.Fatalf("rescue_refused counted %v, want 1", v)
			}
			if c := f.condition(t, ConditionHomeExpiry); c == nil || c.Reason != taskReasonRefused {
				t.Fatalf("HomeExpiry %+v; want Refused", c)
			}
			if c := f.condition(t, ConditionPodReady); c == nil || !strings.Contains(c.Message, "the clean-up keeps the HOME: the rescue refused") ||
				!strings.Contains(c.Message, "a person decides; it is tried again at "+e.RefusalAt.Add(24*time.Hour).UTC().Format(time.RFC3339)) {
				t.Fatalf("PodReady %+v; want the refusal and its retry", c)
			}
			// Nothing moves for a day.
			f.tick(t, f.clock.Add(23*time.Hour))
			f.run(t, 4)
			if p := f.named(t); p != nil || f.expiry(t).Refusal != expiryRescueRefused {
				t.Fatalf("moved within the day: pod %v, record %+v", p, f.expiry(t))
			}
			// A day on, a newer image might handle it: a new hold pod.
			f.passRescue()
			f.tick(t, f.clock.Add(2*time.Hour))
			f.run(t, 4)
			next := f.named(t)
			if next == nil || !isHoldPod(next) || next.UID == hold.UID {
				t.Fatalf("no new hold pod a day later: %+v, record %+v", next, f.expiry(t))
			}
			if e := f.expiry(t); e.Refusal != "" || e.HoldPodUID != string(next.UID) {
				t.Fatalf("record %+v; want the refusal cleared and the new hold pod adopted", e)
			}
			f.claimIntact(t)
		})
	}
}

// TestTaskCleanupTransientRescueFailuresKeepTheHoldPod: a failed fetch and a
// failed bundle write keep the hold pod and the HOME, run again after
// HoldRetry, and count in the breaker, which pauses new starts after three in
// a row.
func TestTaskCleanupTransientRescueFailuresKeepTheHoldPod(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report func(session string) protocol.RescueReport
	}{
		{name: "fetch failed", report: func(s string) protocol.RescueReport {
			rep := cleanRescue(s, "20261111-1200")
			rep.CleanAndPushed, rep.StrictHome = false, &protocol.StrictHomeCheck{}
			rep.Repos[0].Fetched, rep.Repos[0].FetchError = false, "could not resolve host: github.com"
			return rep
		}},
		{name: "bundle write failed", report: func(s string) protocol.RescueReport {
			rep := verifiedRescue(s, "20261111-1200", "20261111-1200")
			rep.OK = false
			rep.Repos[0].Bundle.Verified, rep.Repos[0].Bundle.Error = false, "write rescue/x: no space left on device"
			return rep
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, nil)
			f.run(t, 2)
			hold := f.toHold(t)
			f.report(func(p *corev1.Pod) (protocol.RescueReport, error) { return tc.report(p.Name), nil })
			f.run(t, 4)
			e := f.expiry(t)
			if e.Phase != v1alpha1.HomeExpiryRescuing || e.Refusal != "" || e.HoldPodUID != string(hold.UID) {
				t.Fatalf("record %+v; want Rescuing, unrefused, with the hold pod", e)
			}
			if p := f.named(t); p == nil || p.UID != hold.UID {
				t.Fatal("the hold pod went after a transient failure")
			}
			if r := f.session(t).Status.Rescue; r == nil || r.Result != v1alpha1.RescueFailed {
				t.Fatalf("status.rescue %+v; want Failed", r)
			}
			if f.r.taskCleanup.failures != 1 {
				t.Fatalf("the breaker counted %d, want 1", f.r.taskCleanup.failures)
			}
			ran := len(f.rescuer.ranIn())
			f.run(t, 3)
			if n := len(f.rescuer.ranIn()); n != ran {
				t.Fatalf("the rescue ran again before HoldRetry: %d then %d", ran, n)
			}
			f.tick(t, f.clock.Add(HoldRescueRetry+time.Second))
			f.run(t, 2)
			if n := len(f.rescuer.ranIn()); n != ran+1 {
				t.Fatalf("the rescue did not run again after HoldRetry: %d then %d", ran, n)
			}
			f.kept(t)
		})
	}
}

// TestTaskCleanupProtectedGroupsKeepTheirHome is section 5: interactive
// Sessions, a running task, a reaped task whose stop is not proven
// (RetentionPending), one whose executor vanished first (PreservationUnknown),
// canaries, pilots and an original-HOME binding never get a record; an owner
// hold keeps a record at Waiting with nothing moving. Each keeps its Session
// and HOME, with the clean-up on and every grace long past.
func TestTaskCleanupProtectedGroupsKeepTheirHome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*terminalFixture)
		recorded bool
	}{
		{name: "interactive local", edit: func(tf *terminalFixture) { tf.s.Spec.Mode = v1alpha1.ModeLocal }},
		{name: "interactive remote", edit: func(tf *terminalFixture) { tf.s.Spec.Mode = v1alpha1.ModeRemote }},
		{name: "running task", edit: func(tf *terminalFixture) {
			tf.s.DeletionTimestamp = nil
			tf.s.Status.PrivateHistory.ExecutorTerminal = nil
			tf.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
		}},
		{name: "retention pending", edit: func(tf *terminalFixture) {
			tf.s.Status.PrivateHistory.ExecutorTerminal = nil
			tf.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
		}},
		{name: "preservation unknown", edit: func(tf *terminalFixture) {
			h := tf.s.Status.PrivateHistory
			h.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: h.LastExecutor.PodUID, ObservedAt: h.ExecutorTerminal.ObservedAt}
			h.ExecutorTerminal = nil
			tf.pod = nil
		}},
		{name: "canary", edit: func(tf *terminalFixture) {
			tf.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "canary-a"}
		}},
		{name: "pilot", edit: func(tf *terminalFixture) {
			tf.s.Annotations = map[string]string{projectcanary.PilotAnnotation: "true"}
		}},
		{name: "original HOME binding", edit: func(tf *terminalFixture) {
			tf.s.Annotations = map[string]string{homeguard.Annotation: "{}"}
		}},
		{name: "owner hold", recorded: true, edit: func(tf *terminalFixture) {
			tf.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTaskFixture(t, tc.edit)
			for _, at := range []time.Duration{0, time.Hour, 8 * 24 * time.Hour} {
				f.tick(t, f.now.Add(at))
				f.run(t, 5)
			}
			e := f.expiry(t)
			switch {
			case tc.recorded && (e == nil || e.Phase != v1alpha1.HomeExpiryWaiting):
				t.Fatalf("record %+v; want one held at Waiting", e)
			case !tc.recorded && e != nil:
				t.Fatalf("a protected Session got a record: %+v", e)
			}
			if p := f.named(t); p != nil && isHoldPod(p) {
				t.Fatal("a hold pod was made")
			}
			if len(f.rescuer.ranIn()) != 0 {
				t.Fatal("a rescue ran")
			}
			f.kept(t)
			if f.pod != nil && f.named(t) == nil {
				t.Fatal("the executor Pod was removed")
			}
		})
	}
}

// TestTaskCleanupReleaseCheck is DESIGN-061 1.1. The same node still listing
// the HOME's volume keeps the hold pod from being made for 30 minutes, then
// records volume_not_released, frees the slot and keeps the HOME; once the
// node drops it, the 15-minute re-check makes the hold pod. Another node
// listing the volume does not hold it back: only the old node's teardown is
// in question. A node that is gone, and one with a new UID, never show the
// release.
func TestTaskCleanupReleaseCheck(t *testing.T) {
	toRelease := func(t *testing.T, f *taskFixture) {
		t.Helper()
		f.run(t, 4) // the record, Rescuing, the executor's delete, the intent
		if e := f.expiry(t); e == nil || e.Phase != v1alpha1.HomeExpiryRescuing || e.HoldIntent == "" {
			t.Fatalf("not ready for a hold pod: %+v", e)
		}
	}
	unreleased := func(t *testing.T, f *taskFixture) {
		t.Helper()
		f.run(t, 3)
		if p := f.named(t); p != nil {
			t.Fatalf("a Pod %s mounts the HOME before its release was shown", p.Name)
		}
		if e := f.expiry(t); e.Refusal != "" || !taskActive(f.session(t)) {
			t.Fatalf("record %+v; want it waiting with its slot", e)
		}
		f.tick(t, f.clock.Add(taskReleaseWait+time.Minute))
		rec := events.NewFakeRecorder(16)
		f.r.Recorder = rec
		f.run(t, 2)
		e := f.expiry(t)
		if e.Refusal != expiryVolumeNotReleased || taskActive(f.session(t)) || f.named(t) != nil {
			t.Fatalf("record %+v, pod %v; want volume_not_released, the slot free, no hold pod", e, f.named(t))
		}
		// The record stores the reason code only, so the condition carries the
		// check read again on the next reconcile and does not invent "within
		// 30m0s". The event, written when the refusal was recorded, names the
		// wait that path ran: here the attempt bound, which is reached at the
		// same moment as the release wait and is checked first.
		why, ev := taskRescueReleaseRefusal(taskDrainEvents(rec))
		c := f.condition(t, ConditionPodReady)
		if c == nil || why == "" || !strings.Contains(c.Message, why) || strings.Contains(c.Message, "within 30m0s") {
			t.Fatalf("PodReady %+v; want the check's reason %q and no fixed 30-minute claim; event %q", c, why, ev)
		}
		if !strings.Contains(ev, why) || (!strings.Contains(ev, "for "+taskReleaseWait.String()) && !strings.Contains(ev, "at the attempt's "+taskAttemptDeadline.String()+" bound")) {
			t.Fatalf("rescue refusal event %q; want the check's reason and the wait that ran", ev)
		}
		counts, _ := f.r.taskCleanupGauges([]v1alpha1.AgentSession{*f.session(t)}, *f.clock)
		if counts[taskStateUnproven] != 1 {
			t.Fatalf("gauge %v; want the Session unproven", counts)
		}
		f.kept(t)
	}
	t.Run("same node", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		f.nodeInUse(t, homeVolumeUniqueName())
		toRelease(t, f)
		unreleased(t, f)
		f.nodeInUse(t)
		f.tick(t, f.clock.Add(10*time.Minute))
		f.run(t, 2)
		if f.named(t) != nil || f.expiry(t).Refusal != expiryVolumeNotReleased {
			t.Fatal("re-checked before 15 minutes")
		}
		f.tick(t, f.clock.Add(6*time.Minute))
		f.run(t, 3)
		if p := f.named(t); p == nil || !isHoldPod(p) || f.expiry(t).Refusal != "" {
			t.Fatalf("released and re-checked: pod %v, record %+v; want the hold pod", p, f.expiry(t))
		}
	})
	t.Run("another node", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		other := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-b", UID: "node-2"}, Status: corev1.NodeStatus{VolumesInUse: []corev1.UniqueVolumeName{homeVolumeUniqueName()}}}
		if err := f.c.Create(context.Background(), other); err != nil {
			t.Fatal(err)
		}
		toRelease(t, f)
		f.run(t, 2)
		if p := f.named(t); p != nil {
			t.Fatalf("another node still holds the volume, yet hold pod %s was created", p.UID)
		}
		f.kept(t)
	})
	t.Run("node gone", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		toRelease(t, f)
		if err := f.c.Delete(context.Background(), f.node); err != nil {
			t.Fatal(err)
		}
		unreleased(t, f)
	})
	t.Run("node with a new UID", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		toRelease(t, f)
		if err := f.c.Delete(context.Background(), f.node); err != nil {
			t.Fatal(err)
		}
		n := f.node.DeepCopy()
		n.ResourceVersion, n.UID = "", "node-1-replaced"
		if err := f.c.Create(context.Background(), n); err != nil {
			t.Fatal(err)
		}
		var l = f.lease.DeepCopy()
		if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.lease), l); err != nil {
			t.Fatal(err)
		}
		l.OwnerReferences[0].UID = n.UID
		if err := f.c.Update(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		unreleased(t, f)
	})
	t.Run("a volume that is not CSI", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		pv := f.pv.DeepCopy()
		if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(pv), pv); err != nil {
			t.Fatal(err)
		}
		pv.Spec.CSI, pv.Spec.HostPath = nil, &corev1.HostPathVolumeSource{Path: "/srv/home"}
		if err := f.c.Update(context.Background(), pv); err != nil {
			t.Fatal(err)
		}
		toRelease(t, f)
		unreleased(t, f)
	})
}

// TestTaskCleanupSecondVerifyKeepsTheHome: the bundle verified at Archived is
// verified again before the delete; a tampered bundle (another digest) or a
// missing one refuses, at Archived and in Deleting, and the HOME stays. A
// clean and pushed record whose status.rescue no longer names its verdict
// refuses too.
func TestTaskCleanupSecondVerifyKeepsTheHome(t *testing.T) {
	toArchived := func(t *testing.T) (*taskFixture, string) {
		t.Helper()
		f := newTaskFixture(t, nil)
		id := f.s.Name + "/20261111-1200"
		f.verifier.set(id, protocol.RescueVerifyResult{OK: true, Manifest: "rescue/" + id + "/manifest.json", ManifestSHA256: testDigest, Bundles: 1, Bytes: 2048})
		f.run(t, 2)
		f.toHold(t)
		f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
			return verifiedRescue(p.Name, "20261111-1200", "20261111-1200"), nil
		})
		f.run(t, 2)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Bundle == nil {
			t.Fatalf("not Archived with a bundle: %+v", e)
		}
		return f, id
	}
	tampered := protocol.RescueVerifyResult{OK: true, ManifestSHA256: strings.Repeat("e", 64), Bundles: 1, Bytes: 2048}
	for _, tc := range []struct {
		name, want string
		answer     *protocol.RescueVerifyResult
	}{
		{name: "tampered", want: expiryBundleMismatch, answer: &tampered},
		{name: "missing", want: expiryBundleMissing},
	} {
		t.Run(tc.name+" at Archived", func(t *testing.T) {
			f, id := toArchived(t)
			f.verifier.mu.Lock()
			delete(f.verifier.answers, id)
			f.verifier.mu.Unlock()
			if tc.answer != nil {
				a := *tc.answer
				a.Manifest = "rescue/" + id + "/manifest.json"
				f.verifier.set(id, a)
			}
			f.run(t, 4)
			e := f.expiry(t)
			if e.Phase != v1alpha1.HomeExpiryArchived || e.Refusal != tc.want {
				t.Fatalf("record %+v; want %s at Archived", e, tc.want)
			}
			f.kept(t)
		})
		t.Run(tc.name+" in Deleting", func(t *testing.T) {
			f, id := toArchived(t)
			f.deleteErr = func(obj client.Object) error {
				if _, ok := obj.(*corev1.PersistentVolumeClaim); ok {
					f.deleteErr = nil
					f.verifier.mu.Lock()
					delete(f.verifier.answers, id)
					if tc.answer != nil {
						a := *tc.answer
						a.Manifest = "rescue/" + id + "/manifest.json"
						f.verifier.answers[id] = a
					}
					f.verifier.mu.Unlock()
					return errConflictForTest(obj)
				}
				return nil
			}
			f.run(t, 6)
			e := f.expiry(t)
			if e.Phase != v1alpha1.HomeExpiryDeleting || e.Refusal != tc.want {
				t.Fatalf("record %+v; want %s in Deleting", e, tc.want)
			}
			f.claimIntact(t)
			if f.sessionGone(t) {
				t.Fatal("the Session went")
			}
		})
	}
	t.Run("clean and pushed, rescue superseded", func(t *testing.T) {
		f := newTaskFixture(t, nil)
		f.run(t, 2)
		f.toHold(t)
		f.passRescue()
		f.run(t, 2)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Bundle == nil || e.Bundle.Result != bundleCleanAndPushed {
			t.Fatalf("not Archived clean and pushed: %+v", e)
		}
		f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) { st.Rescue.Superseded = true })
		f.run(t, 4)
		if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Refusal != expiryBundleMismatch {
			t.Fatalf("record %+v; want bundle_mismatch at Archived", e)
		}
		f.kept(t)
	})
}

// TestTaskCleanupBundleRecordsAreSafeAtEveryArchiveReader puts a bundle
// record, Verified and clean and pushed, at Archived, Deleting and Expired,
// on a task Session and on one outside the clean-up's scope, through every
// reader of Expiry.Archive (DESIGN-061 2, item 6): the HomeExpiry condition,
// privateRetention, the PrivateHistoryRetained and RemovalBlocked conditions,
// podPhase, the volume guard, and the delete path's logs and events driven by
// an operator with DESIGN-025's delete on and the clean-up off.
func TestTaskCleanupBundleRecordsAreSafeAtEveryArchiveReader(t *testing.T) {
	bundles := map[string]*v1alpha1.PrivateHomeBundleStatus{
		"verified": {Stamp: "20261111-1200", Result: bundleVerified, Manifest: "rescue/haynes-ops-1006-120000/20261111-1200/manifest.json",
			ManifestSHA256: testDigest, Bundles: 1, Bytes: 10},
		"clean": {Stamp: "20261111-1200", Result: bundleCleanAndPushed},
	}
	for bname, b := range bundles {
		for _, phase := range []string{v1alpha1.HomeExpiryArchived, v1alpha1.HomeExpiryDeleting, v1alpha1.HomeExpiryExpired} {
			for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeTask, v1alpha1.ModeRemote} {
				t.Run(bname+"/"+phase+"/"+string(mode), func(t *testing.T) {
					f := newTaskFixture(t, func(tf *terminalFixture) {
						tf.s.Spec.Mode = mode
						tf.pod = nil
						h := tf.s.Status.PrivateHistory
						at := metav1.NewTime(tf.now)
						bb := *b
						bb.VerifiedAt = at
						h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID,
							GraceStart: h.ExecutorTerminal.ObservedAt, DueAt: h.ExecutorTerminal.ObservedAt, Phase: phase, RescueStamp: b.Stamp, Bundle: &bb, HoldPodUID: "hold-1"}
						if phase != v1alpha1.HomeExpiryArchived {
							h.Expiry.DeletingAt = &at
						}
						if phase == v1alpha1.HomeExpiryExpired {
							h.Expiry.HomeDeletedAt = &at
						}
						tf.s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueCleanAndPushed, Stamp: b.Stamp, PodUID: "hold-1", Generation: tf.s.Generation}
						tf.s.Status.ArchivedAt = &at
					})
					s := f.session(t)
					for _, on := range []bool{true, false} {
						obs := observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: f.home}, taskCleanupOn: on,
							taskCleanupGrace: DefaultTaskCleanupGrace, privateExecutorEnded: true}
						st := s.Status.DeepCopy()
						setHomeExpiryCondition(s, obs, st)
						_, _ = privateRetention(s, obs)
						setPrivateHistoryConditions(s, obs, st)
						_, _ = podPhase(s, obs)
						notDeleting := s.DeepCopy()
						notDeleting.DeletionTimestamp = nil
						_, _ = podPhase(notDeleting, obs)
					}
					_ = expiryVolumeRemovalAllowed(s, f.home)
					_ = keptName(s, s.Status.PrivateHistory.Expiry)
					// DESIGN-025's operator, delete on, clean-up off.
					r := f.reconciler()
					r.HomeExpiryDelete = true
					for range 4 {
						if _, err := r.Reconcile(context.Background(), reqFor(f.s)); err != nil {
							t.Fatal(err)
						}
					}
					// And the clean-up's.
					f.run(t, 4)
					if mode != v1alpha1.ModeTask {
						f.kept(t)
					}
				})
			}
		}
	}
}

// TestTaskCleanupVolumeGuardTakesOnlyATaskBundle: the HOME's guard accepts a
// kept bundle on a task Session's record at Deleting, and refuses one outside
// the clean-up's scope, one beside an archive, one of another rescue, and one
// that names nothing.
func TestTaskCleanupVolumeGuardTakesOnlyATaskBundle(t *testing.T) {
	f := newTaskFixture(t, func(tf *terminalFixture) { tf.pod = nil })
	base := f.session(t)
	at := metav1.NewTime(f.now)
	h := base.Status.PrivateHistory
	h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at,
		Phase: v1alpha1.HomeExpiryDeleting, DeletingAt: &at, RescueStamp: "20261111-1200",
		Bundle: &v1alpha1.PrivateHomeBundleStatus{Stamp: "20261111-1200", Result: bundleCleanAndPushed, VerifiedAt: at}}
	claim := &corev1.PersistentVolumeClaim{}
	if err := f.c.Get(context.Background(), client.ObjectKeyFromObject(f.home), claim); err != nil {
		t.Fatal(err)
	}
	if err := expiryVolumeRemovalAllowed(base, claim); err != nil {
		t.Fatalf("a task Session's kept bundle is refused: %v", err)
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession){
		"interactive": func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeRemote },
		"canary": func(s *v1alpha1.AgentSession) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "c"}
		},
		"beside an archive": func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.Expiry.Archive = &v1alpha1.PrivateHomeArchiveStatus{Entry: "e", ContentSHA256: testDigest}
		},
		"another rescue":            func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.RescueStamp = "20261111-1300" },
		"verified, no digest":       func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.Bundle.Result = bundleVerified },
		"not Deleting":              func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryArchived },
		"not reaped, no archivedAt": func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = nil },
	} {
		s := base.DeepCopy()
		edit(s)
		if err := expiryVolumeRemovalAllowed(s, claim); err == nil {
			t.Errorf("%s: the guard allows the HOME's delete", name)
		}
	}
	// The Session guard: only a finished task Session whose record is
	// Expired with its bundle.
	done := base.DeepCopy()
	done.DeletionTimestamp = nil
	done.Status.PrivateHistory.Expiry.Phase, done.Status.PrivateHistory.Expiry.HomeDeletedAt = v1alpha1.HomeExpiryExpired, &at
	if err := taskSessionRemovalAllowed(done); err != nil {
		t.Fatalf("a finished cleaned Session is refused: %v", err)
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession){
		"reaped":      func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = &at },
		"interactive": func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal },
		"pilot": func(s *v1alpha1.AgentSession) {
			s.Annotations = map[string]string{projectcanary.PilotAnnotation: "true"}
		},
		"not Expired": func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory.Expiry.Phase = v1alpha1.HomeExpiryDeleting },
		"archive record": func(s *v1alpha1.AgentSession) {
			s.Status.PrivateHistory.Expiry.Bundle = nil
			s.Status.PrivateHistory.Expiry.Archive = &v1alpha1.PrivateHomeArchiveStatus{Entry: "e", ContentSHA256: testDigest}
		},
		"no resourceVersion": func(s *v1alpha1.AgentSession) { s.ResourceVersion = "" },
	} {
		s := done.DeepCopy()
		edit(s)
		if err := taskSessionRemovalAllowed(s); err == nil {
			t.Errorf("%s: the Session guard allows the delete", name)
		}
	}
}

func errConflictForTest(obj client.Object) error {
	return apierrors.NewConflict(corev1.Resource("persistentvolumeclaims"), obj.GetName(), errors.New("held by the test"))
}

// TestTaskCleanupOldAgentImageIsRefusedAndPausesTheCleanup: an agent image
// from before --strict-home answers it with a usage error (exit 2). That is
// recorded as a failed rescue, the hold pod goes so a later one can run a
// newer image, the HOME stays, and the breaker counts it.
func TestTaskCleanupOldAgentImageIsRefusedAndPausesTheCleanup(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	f.toHold(t)
	f.report(func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{}, fmt.Errorf("%w: agentd: usage: ctl status | rescue [--stop-agent]", utilexec.CodeExitError{Err: errors.New("command terminated with exit code 2"), Code: 2})
	})
	f.run(t, 6)
	e := f.expiry(t)
	if e.Refusal != expiryRescueRefused || f.named(t) != nil {
		t.Fatalf("record %+v, pod %v; want rescue_refused and the hold pod gone", e, f.named(t))
	}
	if r := f.session(t).Status.Rescue; r == nil || r.Result != v1alpha1.RescueFailed || !strings.Contains(r.Message, "does not know --strict-home") {
		t.Fatalf("status.rescue %+v", r)
	}
	if f.r.taskCleanup.failures != 1 {
		t.Fatalf("the breaker counted %d, want 1", f.r.taskCleanup.failures)
	}
	f.kept(t)
}

// TestTaskCleanupStuckAttemptFreesItsSlot: a hold pod that never runs gives up
// after half an hour: attempt_stuck, the hold pod goes, the slot frees, the
// breaker counts it, the HOME stays, and an hour later a new hold pod tries.
func TestTaskCleanupStuckAttemptFreesItsSlot(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 8)
	hold := f.named(t)
	if hold == nil || !isHoldPod(hold) {
		t.Fatalf("no hold pod: %+v", f.expiry(t))
	}
	f.run(t, 2)
	if e := f.expiry(t); e.Refusal != "" || !taskActive(f.session(t)) {
		t.Fatalf("refused before the deadline: %+v", e)
	}
	f.tick(t, f.clock.Add(taskAttemptDeadline+time.Minute))
	f.run(t, 3)
	e := f.expiry(t)
	if e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) || f.named(t) != nil {
		t.Fatalf("record %+v, pod %v; want attempt_stuck, the slot free, the hold pod gone", e, f.named(t))
	}
	if f.r.taskCleanup.failures != 1 {
		t.Fatalf("the breaker counted %d, want 1", f.r.taskCleanup.failures)
	}
	f.kept(t)
	f.tick(t, f.clock.Add(taskAttemptRetry+time.Minute))
	f.run(t, 4)
	if next := f.named(t); next == nil || !isHoldPod(next) || next.UID == hold.UID || f.expiry(t).Refusal != "" {
		t.Fatalf("no new hold pod an hour later: %+v, record %+v", next, f.expiry(t))
	}
}

// TestTaskCleanupRescueOfAnotherOperatorRunsAgain: a rescue in the hold pod
// that this operator did not run and grade with --strict-home (an earlier
// operator, or DESIGN-025's) is never kept: the strict rescue runs first.
func TestTaskCleanupRescueOfAnotherOperatorRunsAgain(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	hold := f.toHold(t)
	// A valid rescue in the hold pod, written by someone else.
	f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) {
		at := metav1.NewTime(*f.clock)
		st.Rescue = &v1alpha1.RescueStatus{Stamp: "20261111-1100", At: &at, PodUID: string(hold.UID), Generation: f.s.Generation, Result: v1alpha1.RescueCleanAndPushed}
	})
	ran := len(f.rescuer.ranIn())
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		rep := cleanRescue(p.Name, "20261111-1200")
		rep.StrictHome = &protocol.StrictHomeCheck{}
		return rep, nil
	})
	f.run(t, 2)
	if n := len(f.rescuer.ranIn()); n != ran+1 {
		t.Fatalf("the strict rescue did not run: %d then %d", ran, n)
	}
	if e := f.expiry(t); e.Phase != v1alpha1.HomeExpiryArchived || e.Bundle == nil || e.Bundle.Stamp != "20261111-1200" {
		t.Fatalf("record %+v; want Archived on the strict rescue's stamp", e)
	}
}

// TestTaskCleanupChecksTheLastHoldPodsNode: when the clean-up's last hold pod
// stood on another node than the executor, the next hold pod waits until that
// node no longer lists the HOME's volume either.
func TestTaskCleanupChecksTheLastHoldPodsNode(t *testing.T) {
	f := newTaskFixture(t, nil)
	other := taskReadyOtherNode(t, f)
	f.run(t, 8)
	hold := f.named(t)
	if hold == nil || !isHoldPod(hold) {
		t.Fatalf("no hold pod: %+v", f.expiry(t))
	}
	// The scheduler put it on worker-b, where it ended; worker-b still
	// lists the volume while it tears the pod down.
	hold.Spec.NodeName = "worker-b"
	if err := f.c.Update(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	hold = f.named(t)
	hold.Status.Phase = corev1.PodFailed
	if err := f.c.Status().Update(context.Background(), hold); err != nil {
		t.Fatal(err)
	}
	other.Status.VolumesInUse = []corev1.UniqueVolumeName{homeVolumeUniqueName()}
	if err := f.c.Status().Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	f.run(t, 1) // delete the ended hold, then lose the leader before unmount
	f.r = f.fresh()
	f.run(t, 4)
	if p := f.named(t); p != nil {
		t.Fatalf("a new hold pod %s while the last one's node lists the volume", p.UID)
	}
	other.Status.VolumesInUse = nil
	if err := f.c.Status().Update(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	f.run(t, 2)
	if p := f.named(t); p == nil || !isHoldPod(p) || p.UID == hold.UID {
		t.Fatalf("no new hold pod once the node released the volume: %v", p)
	}
}

// TestTaskCleanupRetryThatNeverRunsIsStuck: after a graded transient failure,
// a retry whose exec never reaches the hold pod ends the attempt 30 minutes
// after the last graded rescue, and frees the slot.
func TestTaskCleanupRetryThatNeverRunsIsStuck(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	f.toHold(t)
	f.report(func(p *corev1.Pod) (protocol.RescueReport, error) {
		rep := cleanRescue(p.Name, "20261111-1200")
		rep.CleanAndPushed, rep.StrictHome = false, &protocol.StrictHomeCheck{}
		rep.Repos[0].Fetched, rep.Repos[0].FetchError = false, "could not resolve host: github.com"
		return rep, nil
	})
	f.run(t, 2)
	if r := f.session(t).Status.Rescue; r == nil || r.Result != v1alpha1.RescueFailed {
		t.Fatalf("no graded transient failure: %+v", r)
	}
	f.failRescue()
	f.tick(t, f.clock.Add(HoldRescueRetry+time.Minute))
	f.run(t, 3)
	if e := f.expiry(t); e.Refusal != "" {
		t.Fatalf("refused before the deadline: %+v", e)
	}
	f.tick(t, f.clock.Add(taskAttemptDeadline))
	f.run(t, 3)
	if e := f.expiry(t); e.Refusal != expiryAttemptStuck || taskActive(f.session(t)) || f.named(t) != nil {
		t.Fatalf("record %+v, pod %v; want attempt_stuck and the hold pod gone", e, f.named(t))
	}
	f.kept(t)
}

// TestTaskCleanupHoldPodsThatKeepEndingAreStuck: three hold pods that end
// with no rescue graded end the attempt.
func TestTaskCleanupHoldPodsThatKeepEndingAreStuck(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	var uids []string
	for range 3 {
		f.run(t, 6)
		p := f.named(t)
		if p == nil || !isHoldPod(p) {
			t.Fatalf("no hold pod after %d: %+v", len(uids), f.expiry(t))
		}
		uids = append(uids, string(p.UID))
		p.Status.Phase = corev1.PodFailed
		if err := f.c.Status().Update(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	f.run(t, 3)
	e := f.expiry(t)
	if e.Refusal != expiryAttemptStuck || f.named(t) != nil {
		t.Fatalf("record %+v, pod %v after hold pods %v ended; want attempt_stuck", e, f.named(t), uids)
	}
	if len(f.rescuer.ranIn()) != 0 {
		t.Fatal("a rescue ran in a hold pod that never ran")
	}
	f.kept(t)
}

// TestTaskCleanupLiftedHoldGoesBackThroughTheQueue: the owner holding a
// clean-up at Rescuing removes its hold pod and frees its slot (owner_hold);
// once the hold is lifted it starts again only when the queue admits it.
func TestTaskCleanupLiftedHoldGoesBackThroughTheQueue(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	hold := f.toHold(t)
	f.update(t, func(s *v1alpha1.AgentSession) {
		s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	})
	f.run(t, 4)
	e := f.expiry(t)
	if f.named(t) != nil || e.Refusal != expiryOwnerHold || taskActive(f.session(t)) {
		t.Fatalf("held at Rescuing: pod %v, record %+v; want the hold pod gone and owner_hold", f.named(t), e)
	}
	// Lifted while the clean-up is paused: nothing starts.
	f.update(t, func(s *v1alpha1.AgentSession) { delete(s.Annotations, v1alpha1.AnnotationHomeExpiry) })
	f.r.TaskCleanupMaxActive = 0
	f.tick(t, f.clock.Add(homeExpiryRefusalRetry+time.Minute))
	f.run(t, 4)
	if p := f.named(t); p != nil || f.expiry(t).Refusal != expiryOwnerHold {
		t.Fatalf("a lifted hold started outside the queue: pod %v, record %+v", p, f.expiry(t))
	}
	f.r.TaskCleanupMaxActive = 2
	f.run(t, 4)
	if p := f.named(t); p == nil || !isHoldPod(p) || p.UID == hold.UID || f.expiry(t).Refusal != "" {
		t.Fatalf("admitted after the lift: pod %v, record %+v; want a new hold pod", p, f.expiry(t))
	}
	f.claimIntact(t)
}
