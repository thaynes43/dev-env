package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The envtest cases of D-51: rescue before a suspend deletes the pod, and
// archive of a reaped session's volume only after a verified rescue. envtest has
// no kubelet, garbage collector or PVC protection controller, so the tests play
// them: they finish a pod's graceful delete and lift kubernetes.io/pvc-protection.

func reportFor(report func(string) protocol.RescueReport) func(*corev1.Pod) (protocol.RescueReport, error) {
	return func(pod *corev1.Pod) (protocol.RescueReport, error) { return report(pod.Name), nil }
}

func failedReport(session string) protocol.RescueReport {
	rep := goodReport(session)
	rep.OK = false
	rep.Repos[0].Worktrees[0].Refused = "a merge or rebase is in progress (MERGE_HEAD)"
	rep.Repos[0].Worktrees[0].RescueBranch = ""
	return rep
}

// setMode suspends or resumes the session, retrying while the operator's own
// writes (status, the finalizer) race it.
func setMode(t *testing.T, name string, m v1alpha1.OperatingMode) {
	t.Helper()
	eventually(t, "operatingMode "+string(m), func() error {
		live := session(t, name)
		live.Spec.OperatingMode = m
		return k8s.Update(context.Background(), live)
	})
}

func reap(t *testing.T, name string) {
	t.Helper()
	if err := k8s.Delete(context.Background(), session(t, name)); err != nil {
		t.Fatal(err)
	}
}

// finishPodDelete plays the kubelet: once the operator has deleted the pod
// (a pod on a node goes gracefully), it confirms the delete.
func finishPodDelete(t *testing.T, pod *corev1.Pod) {
	t.Helper()
	eventually(t, "the operator deletes pod "+pod.Name, func() error {
		p, err := get(t, sessionNS, pod.Name, &corev1.Pod{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case p.UID != pod.UID:
			return fmt.Errorf("a new pod %s replaced it", p.UID)
		case p.DeletionTimestamp.IsZero():
			return errors.New("not deleted yet")
		}
		return k8s.Delete(context.Background(), p, client.GracePeriodSeconds(0), client.Preconditions{UID: &p.UID})
	})
}

// finishClaimDelete plays the PVC protection controller: once the operator has
// deleted the volume and lifted its own finalizer, the claim goes.
func finishClaimDelete(t *testing.T, name string) {
	t.Helper()
	eventually(t, "the operator archives the volume of "+name, func() error {
		c, err := get(t, sessionNS, HomeClaimName(name), &corev1.PersistentVolumeClaim{})
		switch {
		case apierrors.IsNotFound(err):
			return nil
		case err != nil:
			return err
		case c.DeletionTimestamp.IsZero():
			return errors.New("not deleted yet")
		case slices.Contains(c.Finalizers, Finalizer):
			return errors.New("the operator's finalizer is still on it")
		}
		c.Finalizers = nil
		return k8s.Update(context.Background(), c)
	})
}

func rescueIs(result v1alpha1.RescueResult) func(*v1alpha1.AgentSessionStatus) error {
	return func(st *v1alpha1.AgentSessionStatus) error {
		if st.Rescue == nil || st.Rescue.Result != result {
			return fmt.Errorf("rescue %+v, want %s", st.Rescue, result)
		}
		return nil
	}
}

func waitGone(t *testing.T, name string) {
	t.Helper()
	eventually(t, "the session is gone", func() error {
		_, err := get(t, sessionNS, name, &v1alpha1.AgentSession{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("still there: %w", err)
	})
}

func runningSession(t *testing.T, node string) (*v1alpha1.AgentSession, *corev1.Pod, *corev1.PersistentVolumeClaim) {
	t.Helper()
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), node)
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	return s, waitPod(t, s.Name), claim
}

// A suspend rescues the pod, records the verdict, then deletes the pod; the
// volume stays.
func TestSuspendRescuesThenDeletesThePod(t *testing.T) {
	op := startOperator(t)
	s, pod, claim := runningSession(t, "talosw02")
	rescuer.answer(t, s.Name, reportFor(goodReport))
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	r := got.Status.Rescue
	if r == nil || r.Result != v1alpha1.RescueVerified || r.PodUID != string(pod.UID) || r.Generation != got.Generation || r.Superseded {
		t.Fatalf("rescue %+v", r)
	}
	if r.LastBundle != "rescue/"+s.Name+"/20261006-1730/manifest.json" || len(r.UnpushedRefs) != 2 {
		t.Errorf("rescue %+v", r)
	}
	if c := condition(&got.Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Verified" {
		t.Errorf("RescueFailed %+v", c)
	}
	if c := condition(&got.Status, ConditionRemovalBlocked); c != nil {
		t.Errorf("RemovalBlocked %+v", c)
	}
	if n := rescuer.callsFor(s.Name); n != 1 {
		t.Errorf("%d rescues, want 1", n)
	}
	// The record reached the API server before the pod was deleted.
	if !op.writes.before("record rescue of "+string(pod.UID), "delete *v1.Pod "+pod.Name) {
		t.Errorf("the pod was deleted before its rescue was recorded: %v", op.writes.orderLog())
	}
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume changed")
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// agentd's heartbeat patches the session's status every minute, also while a
// rescue runs. The record still lands, once, and the pod goes after it.
func TestARescueOutlastsStatusWritesDuringIt(t *testing.T) {
	op := startOperator(t)
	s, pod, _ := runningSession(t, "talosw02")
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		// This runs in the operator's goroutine: no t.Fatal here.
		live := &v1alpha1.AgentSession{}
		if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(p), live); err != nil {
			return protocol.RescueReport{}, err
		}
		base := live.DeepCopy()
		live.Status.Agent = &v1alpha1.AgentStatus{Status: "exited"}
		if err := k8s.Status().Patch(context.Background(), live, client.MergeFrom(base)); err != nil {
			return protocol.RescueReport{}, err
		}
		return goodReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "Suspended and rescued", rescueIs(v1alpha1.RescueVerified))
	if got.Status.Agent == nil || got.Status.Agent.Status != "exited" {
		t.Errorf("the heartbeat's status was lost: %+v", got.Status.Agent)
	}
	if n := rescuer.callsFor(s.Name); n != 1 {
		t.Errorf("%d rescues, want 1", n)
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// D-10: a rescue that ran and failed still suspends, keeping the volume, and
// RescueFailed blocks the archive of a later reap.
func TestAFailedRescueStillSuspendsAndBlocksArchive(t *testing.T) {
	op := startOperator(t)
	s, pod, claim := runningSession(t, "talosw03")
	rescuer.answer(t, s.Name, reportFor(failedReport))
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	if c := condition(&got.Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "WorktreeRefused" || !strings.Contains(c.Message, "merge or rebase") {
		t.Errorf("RescueFailed %+v", c)
	}
	reap(t, s.Name)
	got = waitStatus(t, s.Name, "the reap keeps the volume", blockedBy("DeleteNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "the newest rescue failed") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume of a failed rescue is being deleted")
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// A reap of a Running session: rescue, suspend, archive, and the session goes.
// The rescue and the archive leave events behind.
func TestReapArchivesAfterAVerifiedRescue(t *testing.T) {
	for name, report := range map[string]func(string) protocol.RescueReport{"verified": goodReport, "clean and pushed": cleanReport} {
		t.Run(name, func(t *testing.T) {
			op := startOperator(t)
			s, pod, claim := runningSession(t, "talosw02")
			rescuer.answer(t, s.Name, reportFor(report))
			reap(t, s.Name)
			finishPodDelete(t, pod)
			finishClaimDelete(t, s.Name)
			waitGone(t, s.Name)
			assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name, "delete *v1.PersistentVolumeClaim "+claim.Name, "patch *v1.PersistentVolumeClaim "+claim.Name)
			eventually(t, "the events", func() error {
				var list eventsv1.EventList
				if err := k8s.List(context.Background(), &list, client.InNamespace(sessionNS)); err != nil {
					return err
				}
				var reasons []string
				for _, e := range list.Items {
					if e.Regarding.Name == s.Name {
						reasons = append(reasons, e.Reason)
					}
				}
				if !slices.Contains(reasons, "Archived") || (!slices.Contains(reasons, "Verified") && !slices.Contains(reasons, "CleanAndPushed")) {
					return fmt.Errorf("events %v", reasons)
				}
				return nil
			})
		})
	}
}

// D-10: an old bundle never counts. A resume puts a new pod on the volume, and
// the rescue of the old pod is superseded before the new pod exists, so a reap
// whose new pod cannot be rescued keeps the volume.
func TestANewPodSupersedesTheRescue(t *testing.T) {
	startOperator(t)
	s, pod, claim := runningSession(t, "talosw02")
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		if p.UID != pod.UID {
			return protocol.RescueReport{}, errors.New("no exec into the new pod")
		}
		return goodReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	waitStatus(t, s.Name, "Suspended and rescued", rescueIs(v1alpha1.RescueVerified))

	setMode(t, s.Name, v1alpha1.OperatingModeRunning)
	var second *corev1.Pod
	eventually(t, "a new pod", func() error {
		p, err := get(t, sessionNS, s.Name, &corev1.Pod{})
		if err != nil {
			return err
		}
		if p.UID == pod.UID {
			return errors.New("still the old pod")
		}
		second = p
		return nil
	})
	// The record said so before the pod was created.
	if r := session(t, s.Name).Status.Rescue; r == nil || !r.Superseded || r.LastBundle == "" {
		t.Fatalf("rescue %+v, want superseded with its bundle kept", r)
	}
	if c := condition(&session(t, s.Name).Status, ConditionRescueFailed); c != nil {
		t.Errorf("RescueFailed %+v for a superseded rescue", c)
	}
	markRunning(t, second, "talosw03")

	reap(t, s.Name)
	got := waitStatus(t, s.Name, "the reap waits for a rescue of the new pod", blockedBy("DeleteNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "the rescue could not run") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	// The new pod goes away without a rescue (a preemption, say): the
	// superseded rescue still does not count.
	if err := k8s.Delete(context.Background(), second, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "the volume waits", func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionRemovalBlocked)
		if c == nil || !strings.Contains(c.Message, "superseded") {
			return fmt.Errorf("RemovalBlocked %+v", c)
		}
		return nil
	})
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume is being deleted on a superseded rescue")
	}
}

// A resume that lands while the rescued pod still runs (its delete hit a
// conflict, or the operator restarted in between) supersedes the rescue too:
// the pod may work again, so if it later ends and the session is reaped, the
// old bundle does not let the volume go.
func TestAResumeWithTheRescuedPodStillThereSupersedesTheRescue(t *testing.T) {
	op := startOperator(t)
	s, pod, claim := runningSession(t, "talosw02")
	// The record a suspend left before its pod delete failed.
	eventually(t, "a rescue record of this pod", func() error {
		live := session(t, s.Name)
		live.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: string(pod.UID), Generation: live.Generation,
			Stamp: "20261006-1700", LastBundle: "rescue/" + s.Name + "/20261006-1700/manifest.json"}
		return k8s.Status().Update(context.Background(), live)
	})
	waitStatus(t, s.Name, "the running session supersedes it", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Rescue == nil || !st.Rescue.Superseded || st.Rescue.LastBundle == "" {
			return fmt.Errorf("rescue %+v", st.Rescue)
		}
		return nil
	})
	pod = waitPod(t, s.Name)
	pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "Evicted"
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "Failed", phaseIs(v1alpha1.PhaseFailed))
	reap(t, s.Name)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "the volume waits", blockedBy("DeleteNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "superseded") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume is being deleted on a rescue the pod outlived")
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// A rescue record of the pod from before the session last changed does not let
// the pod go: the pod may have worked since (a resume and a new suspend the
// operator never saw in between), so a new rescue must run.
func TestARescueFromAnOlderGenerationIsNotReused(t *testing.T) {
	op := startOperator(t)
	s, pod, _ := runningSession(t, "talosw02")
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	waitStatus(t, s.Name, "suspend waits for a rescue", blockedBy("SuspendNeedsRescue"))
	before := rescuer.callsFor(s.Name)
	// The operator writes status too; retry on its conflicts.
	eventually(t, "a rescue record of this pod at an older generation", func() error {
		live := session(t, s.Name)
		live.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, PodUID: string(pod.UID), Generation: live.Generation - 1, Stamp: "20261006-1700"}
		return k8s.Status().Update(context.Background(), live)
	})
	eventually(t, "a new rescue is tried", func() error {
		if rescuer.callsFor(s.Name) <= before {
			return errors.New("no new rescue yet")
		}
		return nil
	})
	got := waitStatus(t, s.Name, "the pod still waits", blockedBy("SuspendNeedsRescue"))
	if got.Status.Rescue == nil || got.Status.Rescue.Generation == got.Generation || got.Status.Rescue.Superseded {
		t.Fatalf("rescue %+v at generation %d", got.Status.Rescue, got.Generation)
	}
	if p := waitPod(t, s.Name); p.UID != pod.UID || !p.DeletionTimestamp.IsZero() {
		t.Error("the pod was deleted on an old rescue")
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A pod that ended (evicted) runs nothing a rescue could reach: a reap removes
// it, and the volume waits for a rescue no pod can run (a rescue pod is plan
// 02's).
func TestReapOfAnEvictedPod(t *testing.T) {
	op := startOperator(t)
	s, pod, claim := runningSession(t, "talosw02")
	pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "Evicted"
	if err := k8s.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "Failed", phaseIs(v1alpha1.PhaseFailed))
	reap(t, s.Name)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "the volume waits", blockedBy("DeleteNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "no rescue has run on the volume") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	if n := rescuer.callsFor(s.Name); n != 0 {
		t.Errorf("%d rescues in an ended pod", n)
	}
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume is being deleted")
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// A report from a pod that was replaced while the rescue ran is not recorded,
// and nothing is deleted on it.
func TestARescueOfAReplacedPodIsNotTrusted(t *testing.T) {
	op := startOperator(t)
	s, pod, _ := runningSession(t, "talosw02")
	var replaced atomic.Bool
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		if replaced.Swap(true) {
			// The replacement has no agent to answer.
			return protocol.RescueReport{}, errors.New("unable to upgrade connection: container not found")
		}
		// The pod goes during the rescue, and a new one takes its name.
		if err := k8s.Delete(context.Background(), p, client.GracePeriodSeconds(0)); err != nil {
			return protocol.RescueReport{}, err
		}
		replacement := p.DeepCopy()
		replacement.ObjectMeta = metav1.ObjectMeta{Name: p.Name, Namespace: p.Namespace, Labels: p.Labels, OwnerReferences: p.OwnerReferences}
		// Bound to a node, so an agent may run in it and it needs a
		// rescue of its own.
		replacement.Spec.NodeName = "talosw03"
		replacement.Status = corev1.PodStatus{}
		if err := k8s.Create(context.Background(), replacement); err != nil {
			return protocol.RescueReport{}, err
		}
		return goodReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	// The operator turns to the replacement, which needs a rescue of its own.
	eventually(t, "a rescue of the replacement", func() error {
		if n := rescuer.callsFor(s.Name); n < 2 {
			return fmt.Errorf("%d rescues", n)
		}
		return nil
	})
	got := waitStatus(t, s.Name, "the replacement waits", blockedBy("SuspendNeedsRescue"))
	if got.Status.Rescue != nil {
		t.Errorf("a rescue of a replaced pod was recorded: %+v", got.Status.Rescue)
	}
	if p := waitPod(t, s.Name); p.UID == pod.UID || !p.DeletionTimestamp.IsZero() {
		t.Errorf("pod %s, deleting %v: want the replacement, untouched", p.UID, !p.DeletionTimestamp.IsZero())
	}
	assertNoPodOrVolumeWrites(t, op)
}
