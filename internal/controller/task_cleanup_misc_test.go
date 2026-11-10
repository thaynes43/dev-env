package controller

// DESIGN-061's smaller pieces: the terminal task reclaim beside a task record
// at Waiting, and the clean-up's gauges.

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestTaskCleanupLeavesTheEndedComputeToTheReclaimAtWaiting: a task record at
// Waiting does not keep the terminal task reclaim from removing the ended
// executor; a record past Waiting, and any DESIGN-025 record, still do.
func TestTaskCleanupLeavesTheEndedComputeToTheReclaimAtWaiting(t *testing.T) {
	f := terminalTaskFixture(true)
	h := f.s.Status.PrivateHistory
	at := metav1.NewTime(f.now)
	h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, Phase: "Failed", Reason: "DeadlineExceeded", ObservedAt: at}
	h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at, Phase: v1alpha1.HomeExpiryWaiting}
	c := f.client()
	r := terminalTaskReconciler(f, c, c)
	if reason, err := r.terminalTaskSessionAllowed(f.s, f.now); reason != "Protected" || err == nil {
		t.Fatalf("with the clean-up off a record keeps the reclaim off: %q %v", reason, err)
	}
	r.TaskSessionCleanup = true
	if reason, err := r.terminalTaskSessionAllowed(f.s, f.now); err != nil {
		t.Fatalf("a task record at Waiting keeps the reclaim off: %q %v", reason, err)
	}
	h.Expiry.Phase = v1alpha1.HomeExpiryRescuing
	if reason, err := r.terminalTaskSessionAllowed(f.s, f.now); reason != "Protected" || err == nil {
		t.Fatalf("a task record at Rescuing lets the reclaim act: %q %v", reason, err)
	}
}

// TestTaskCleanupGauges sorts finished and purged task Sessions into the
// gauge's states with their oldest clock; running tasks and interactive
// Sessions are in none, and the clean-up off reports nothing.
func TestTaskCleanupGauges(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	r := &Reconciler{TaskSessionCleanup: true, PrivateTerminalRecord: true, PrivateExecutorStop: true}
	base := func(name string, observed time.Time) v1alpha1.AgentSession {
		tf := newTerminalFixture()
		s := *tf.s.DeepCopy()
		s.Name, s.UID = name, k8stypes.UID("uid-"+name)
		h := s.Status.PrivateHistory
		h.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: h.LastExecutor.PodUID, ObservedAt: metav1.NewTime(observed)}
		return s
	}
	record := func(s v1alpha1.AgentSession, phase, refusal string) v1alpha1.AgentSession {
		h := s.Status.PrivateHistory
		at := h.ExecutorTerminal.ObservedAt
		h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at, Phase: phase, Refusal: refusal}
		return s
	}
	day := 24 * time.Hour
	waiting := base("waiting", now.Add(-day))
	queued := record(base("queued", now.Add(-9*day)), v1alpha1.HomeExpiryWaiting, "")
	rescuing := record(base("rescuing", now.Add(-10*day)), v1alpha1.HomeExpiryRescuing, "")
	refused := record(base("refused", now.Add(-11*day)), v1alpha1.HomeExpiryRescuing, expiryRescueRefused)
	unproven := record(base("unproven", now.Add(-12*day)), v1alpha1.HomeExpiryRescuing, expiryVolumeNotReleased)
	missed := base("missed", now)
	missed.Status.PrivateHistory.ExecutorMissed = &v1alpha1.PrivateExecutorMissedStatus{Version: 1, PodUID: "x", ObservedAt: metav1.NewTime(now.Add(-20 * day))}
	missed.Status.PrivateHistory.ExecutorTerminal = nil
	held := base("held", now.Add(-day))
	held.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
	canary := base("canary", now.Add(-30*day))
	canary.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{Version: 1, ID: "c"}
	running := base("running", now)
	running.Status.PrivateHistory.ExecutorTerminal = nil
	remote := base("remote", now.Add(-40*day))
	remote.Spec.Mode = v1alpha1.ModeRemote
	saved := record(base("saved", now.Add(-day)), v1alpha1.HomeExpiryArchived, "")
	expired := record(base("expired", now.Add(-day)), v1alpha1.HomeExpiryExpired, "")
	counts, oldest := r.taskCleanupGauges([]v1alpha1.AgentSession{waiting, queued, rescuing, refused, unproven, missed, held, canary, running, remote, saved, expired}, now)
	want := map[string]int{taskStateWaiting: 1, taskStateQueued: 1, taskStateRescuing: 1, taskStateRefused: 1, taskStateUnproven: 2, taskStateHeld: 1, taskStateProtected: 1, taskStateSaved: 1}
	for _, st := range taskCleanupStates {
		if counts[st] != want[st] {
			t.Errorf("%s: %d Sessions, want %d", st, counts[st], want[st])
		}
	}
	if oldest[taskStateUnproven] != (20*day).Seconds() || oldest[taskStateQueued] != (9*day).Seconds() || oldest[taskStateProtected] != (30*day).Seconds() {
		t.Errorf("oldest %v", oldest)
	}
	r.TaskSessionCleanup = false
	if len(r.TaskCleanupPaused(now)) != 0 {
		t.Error("the clean-up off reports a pause")
	}
}

// TestTaskRecordsLeaveTheExpiryGaugesWhileTheCleanupIsOn: a task record is
// counted by DESIGN-025's gauges only while the clean-up is off; on, its own
// gauges count it (dev_env_task_cleanup_sessions), so no alert counts it
// twice. Every series is still reported.
func TestTaskRecordsLeaveTheExpiryGaugesWhileTheCleanupIsOn(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.run(t, 2)
	f.updateStatus(t, func(st *v1alpha1.AgentSessionStatus) {
		at := metav1.NewTime(*f.clock)
		st.PrivateHistory.Expiry.Phase, st.PrivateHistory.Expiry.Refusal, st.PrivateHistory.Expiry.RefusalAt = v1alpha1.HomeExpiryRescuing, expiryRescueRefused, &at
	})
	off := gaugeValues(t, &HomeExpiryCollector{Reader: f.c, Namespace: f.s.Namespace, Now: func() time.Time { return *f.clock }})
	on := gaugeValues(t, &HomeExpiryCollector{Reader: f.c, Namespace: f.s.Namespace, Now: func() time.Time { return *f.clock }, TaskCleanupOn: true})
	if off[MetricHomeExpiryRefusing+"/"+expiryRescueRefused] != 1 || off[MetricHomeExpirySessions+"/rescuing"] != 1 {
		t.Fatalf("with the clean-up off the record is not counted: %v", off)
	}
	if _, ok := on[MetricHomeExpiryRefusing+"/"+expiryRescueRefused]; !ok || on[MetricHomeExpiryRefusing+"/"+expiryRescueRefused] != 0 || on[MetricHomeExpirySessions+"/rescuing"] != 0 {
		t.Fatalf("with the clean-up on the record is counted by DESIGN-025's gauges: %v", on)
	}
	task := gaugeValues(t, &TaskCleanupCollector{Reader: f.c, Namespace: f.s.Namespace, Now: func() time.Time { return *f.clock }, Reconciler: f.r})
	if task[MetricTaskCleanupSessions+"/"+taskStateRefused] != 1 || task[MetricTaskCleanupPaused+"/"+taskPauseMaxActiveZero] != 0 {
		t.Fatalf("the clean-up's gauges: %v", task)
	}
}
