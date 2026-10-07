package controller

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The envtest cases of D-62, the archive timer of a suspended session.

func withArchiveAfter(d time.Duration) func(*v1alpha1.AgentSession) {
	return func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: d}}
	}
}

func archived(st *v1alpha1.AgentSessionStatus) error {
	if st.Phase != v1alpha1.PhaseArchived || st.ArchivedAt == nil {
		return fmt.Errorf("phase %s, archivedAt %v", st.Phase, st.ArchivedAt)
	}
	return nil
}

// A suspended session whose suspend rescue was verified is archived once its
// timer is due: the volume goes, and the session stays as an Archived record.
// A resume of it starts nothing: a new volume would run its task again.
func TestTheArchiveTimerArchivesASuspendedSession(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, withArchiveAfter(3*time.Second))
	claim := waitClaim(t, s.Name)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	rescuer.answer(t, s.Name, reportFor(cleanReport))
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	if got.Status.SuspendedAt == nil {
		eventually(t, "suspendedAt", func() error {
			if session(t, s.Name).Status.SuspendedAt == nil {
				return fmt.Errorf("no suspendedAt")
			}
			return nil
		})
	}
	finishClaimDelete(t, s.Name)
	waitStatus(t, s.Name, "Archived", archived)
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name, "delete *v1.PersistentVolumeClaim "+claim.Name, "patch *v1.PersistentVolumeClaim "+claim.Name)
	// The record comes first, so the session can never be resumed onto a
	// new volume (D-62).
	if !op.writes.before("record archive of "+s.Name, "delete *v1.PersistentVolumeClaim "+claim.Name) {
		t.Errorf("the volume was deleted before the archive was recorded: %v", op.writes.orderLog())
	}

	setMode(t, s.Name, v1alpha1.OperatingModeRunning)
	time.Sleep(2 * time.Second)
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("a resumed archived session got a pod: %v", err)
	}
	if _, err := get(t, sessionNS, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Errorf("a resumed archived session got a volume: %v", err)
	}
	waitStatus(t, s.Name, "still Archived", archived)
}

// A suspended session whose pod never ran has no rescue, so its archive gets a
// hold pod (D-55) first; the session stays Archived afterwards.
func TestTheArchiveTimerRescuesInAHoldPod(t *testing.T) {
	startOperator(t)
	s := newSession(t, withArchiveAfter(2*time.Second))
	waitClaim(t, s.Name)
	waitPod(t, s.Name)
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	hold := waitHoldPod(t, s.Name)
	rescuer.answer(t, s.Name, reportFor(emptyReport))
	markRunning(t, hold, "talosw03")
	finishPodDelete(t, hold)
	finishClaimDelete(t, s.Name)
	got := waitStatus(t, s.Name, "Archived", archived)
	if got.Status.Rescue == nil || got.Status.Rescue.Result != v1alpha1.RescueCleanAndPushed {
		t.Errorf("rescue %+v", got.Status.Rescue)
	}
}

// Before the timer is due the volume stays.
func TestTheArchiveTimerWaits(t *testing.T) {
	op := startOperator(t)
	s := newSession(t, withArchiveAfter(time.Hour))
	claim := waitClaim(t, s.Name)
	pod := waitPod(t, s.Name)
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
	time.Sleep(2 * time.Second)
	if c := waitClaim(t, s.Name); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume is going before its timer")
	}
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("a hold pod before the timer: %v", err)
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// Without a spec override and with templates that do not load, the timer is
// not due: their window may be longer than D-09's default (D-62).
func TestArchiveDueNeedsAWindow(t *testing.T) {
	r := &Reconciler{}
	now := time.Now()
	since := metav1.NewTime(now.Add(-1000 * time.Hour))
	s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeSuspended}}
	s.Status.SuspendedAt = &since
	if due, wait := r.archiveDue(s, nil, observation{now: now}); due || wait <= 0 {
		t.Errorf("no templates: due %v, wait %s", due, wait)
	}
	s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: time.Hour}}
	if due, _ := r.archiveDue(s, nil, observation{now: now}); !due {
		t.Error("a spec window is due without templates")
	}
	s.Status.SuspendedAt = nil
	if due, _ := r.archiveDue(s, nil, observation{now: now}); due {
		t.Error("due before the session was seen suspended")
	}
}
