package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The envtest cases of D-45. envtest runs no garbage collector, so these tests
// prove its precondition: the session object, owner of the pod and the volume,
// never goes away while they hold unrescued work, and the volume itself survives
// a direct delete.

func blockedBy(reason string) func(*v1alpha1.AgentSessionStatus) error {
	return func(st *v1alpha1.AgentSessionStatus) error {
		c := condition(st, ConditionRemovalBlocked)
		if c == nil || c.Status != metav1.ConditionTrue || c.Reason != reason {
			return fmt.Errorf("RemovalBlocked %+v, want True with %s", c, reason)
		}
		return nil
	}
}

// A session and its volume carry the finalizer from the start: the finalizer is
// on before the volume exists.
func TestTheFinalizerComesFirst(t *testing.T) {
	startOperator(t)
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	if !slices.Contains(session(t, s.Name).Finalizers, Finalizer) {
		t.Errorf("the session has a volume and no finalizer")
	}
	if !slices.Contains(claim.Finalizers, Finalizer) {
		t.Errorf("the volume has no finalizer: %v", claim.Finalizers)
	}
}

// 5.1, second rule, and D-45: deleting the AgentSession of a Running session is a
// reap. While no rescue can run in the pod (the fake rescuer fails, as an exec
// into a pod with no running container does), the pod keeps running, the volume
// stays, the session stays (deleting) and says why, and a fresh operator holds
// the same line.
func TestInvariantDeletingARunningSessionKeepsItsPodAndVolume(t *testing.T) {
	first := startOperator(t)
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	pod := waitPod(t, s.Name)

	if err := k8s.Delete(context.Background(), session(t, s.Name)); err != nil {
		t.Fatal(err)
	}
	live := waitStatus(t, s.Name, "the reap waits for rescue", blockedBy("DeleteNeedsRescue"))
	if c := condition(&live.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "the rescue could not run") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	if live.DeletionTimestamp.IsZero() || !slices.Contains(live.Finalizers, Finalizer) {
		t.Fatalf("session %+v, want deleting and held by the finalizer", live.ObjectMeta)
	}
	if live.Status.Phase != v1alpha1.PhaseRunning {
		t.Errorf("phase %s: the pod still runs", live.Status.Phase)
	}

	first.stop()
	// Clear the status, so the condition coming back proves the fresh operator
	// reconciled the deleting session and reached the same verdict.
	live.Status = v1alpha1.AgentSessionStatus{}
	if err := k8s.Status().Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	second := startOperator(t)
	waitStatus(t, s.Name, "the fresh operator still holds it", blockedBy("DeleteNeedsRescue"))

	after := waitPod(t, s.Name)
	if after.UID != pod.UID || after.ResourceVersion != pod.ResourceVersion || !after.DeletionTimestamp.IsZero() {
		t.Errorf("the pod changed: uid %s to %s, resourceVersion %s to %s, deleting %v",
			pod.UID, after.UID, pod.ResourceVersion, after.ResourceVersion, !after.DeletionTimestamp.IsZero())
	}
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Errorf("the volume changed or is being deleted")
	}
	assertNoPodOrVolumeWrites(t, first, second)
}

// 5.1 and D-10: suspending a Running session keeps its pod while no rescue can
// run in it, and resuming clears the block.
func TestInvariantSuspendKeepsThePodUntilRescue(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, nil)
	markRunning(t, waitPod(t, s.Name), "talosw03")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	pod := waitPod(t, s.Name)

	setMode := func(m v1alpha1.OperatingMode) {
		live := session(t, s.Name)
		live.Spec.OperatingMode = m
		if err := k8s.Update(context.Background(), live); err != nil {
			t.Fatal(err)
		}
	}
	setMode(v1alpha1.OperatingModeSuspended)
	waitStatus(t, s.Name, "suspend waits for rescue", blockedBy("SuspendNeedsRescue"))
	if after := waitPod(t, s.Name); after.UID != pod.UID || !after.DeletionTimestamp.IsZero() {
		t.Errorf("the pod was replaced or is being deleted")
	}

	setMode(v1alpha1.OperatingModeRunning)
	waitStatus(t, s.Name, "resume clears the block", func(st *v1alpha1.AgentSessionStatus) error {
		if c := condition(st, ConditionRemovalBlocked); c != nil {
			return fmt.Errorf("RemovalBlocked %+v", c)
		}
		return nil
	})
	assertNoPodOrVolumeWrites(t, op)
}

// The volume's own finalizer keeps it, and its data, through a direct delete:
// what the garbage collector's foreground cascade would do. With the pod gone too,
// no new pod starts on a volume that is being deleted.
func TestADeletedVolumeStaysUntilArchive(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	pod := waitPod(t, s.Name)
	if err := k8s.Delete(context.Background(), claim); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Delete(context.Background(), pod, client.GracePeriodSeconds(0)); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "no pod on a volume being deleted", func(st *v1alpha1.AgentSessionStatus) error {
		if c := condition(st, ConditionPodReady); c == nil || c.Reason != "VolumeTerminating" {
			return fmt.Errorf("PodReady %+v", c)
		}
		return nil
	})
	got := waitClaim(t, s.Name)
	if got.UID != claim.UID || got.DeletionTimestamp.IsZero() || !slices.Contains(got.Finalizers, Finalizer) {
		t.Errorf("volume %+v, want the same one, terminating and held", got.ObjectMeta)
	}
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("a pod started on a volume being deleted: %v", err)
	}
	assertNoPodOrVolumeWrites(t, op)
}

// A deleted session that never got a pod or a volume has nothing to rescue: the
// operator lets it go.
func TestADeletedSessionWithNothingToRescueGoes(t *testing.T) {
	startOperator(t)
	deleteTemplates(t)
	s := newSession(t, nil)
	eventually(t, "the finalizer", func() error {
		if !slices.Contains(session(t, s.Name).Finalizers, Finalizer) {
			return fmt.Errorf("no finalizer yet")
		}
		return nil
	})
	if err := k8s.Delete(context.Background(), session(t, s.Name)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the session is gone", func() error {
		_, err := get(t, sessionNS, s.Name, &v1alpha1.AgentSession{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("still there: %w", err)
	})
}

// A deleted session whose pod is gone but whose volume holds its work stays until
// a rescue. The pod here was never scheduled, so the suspend removes it without
// a rescue (no process ever ran in it), and no rescue can run on the volume.
func TestADeletedSessionWithOnlyAVolumeStays(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, nil)
	claim := waitClaim(t, s.Name)
	pod := waitPod(t, s.Name)
	live := session(t, s.Name)
	live.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s.Name, "Suspended with its volume", phaseIs(v1alpha1.PhaseSuspended))
	if n := rescuer.callsFor(s.Name); n != 0 {
		t.Errorf("a rescue ran %d times in a pod that never started", n)
	}
	if err := k8s.Delete(context.Background(), session(t, s.Name)); err != nil {
		t.Fatal(err)
	}
	got := waitStatus(t, s.Name, "the reap waits for rescue", blockedBy("DeleteNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); !strings.Contains(c.Message, "no rescue has run on the volume") {
		t.Errorf("RemovalBlocked %q", c.Message)
	}
	if c := waitClaim(t, s.Name); !c.DeletionTimestamp.IsZero() || c.UID != claim.UID {
		t.Error("the volume is being deleted")
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}
