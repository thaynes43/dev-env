package controller

// DESIGN-061's smaller pieces: the terminal task reclaim beside a task record
// at Waiting, and the clean-up's gauges.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/shelf"
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

// TestTaskCleanupSlots: a record holds a slot at Rescuing, Archived or Deleting
// only while it is neither refused nor held; Waiting and Expired hold none.
func TestTaskCleanupSlots(t *testing.T) {
	at := metav1.NewTime(time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		phase, refusal string
		held, want     bool
	}{
		{v1alpha1.HomeExpiryWaiting, "", false, false},
		{v1alpha1.HomeExpiryRescuing, "", false, true},
		{v1alpha1.HomeExpiryArchived, "", false, true},
		{v1alpha1.HomeExpiryDeleting, "", false, true},
		{v1alpha1.HomeExpiryExpired, "", false, false},
		{v1alpha1.HomeExpiryRescuing, expiryVolumeNotReleased, false, false},
		{v1alpha1.HomeExpiryRescuing, expiryBundleMismatch, false, false},
		{v1alpha1.HomeExpiryArchived, expiryBundleMissing, false, false},
		{v1alpha1.HomeExpiryDeleting, expiryForbidden, false, false},
		{v1alpha1.HomeExpiryDeleting, expiryClaimInUse, false, false},
		{v1alpha1.HomeExpiryRescuing, "", true, false},
	} {
		tf := newTerminalFixture()
		s := tf.s
		h := s.Status.PrivateHistory
		h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, Phase: tc.phase, Refusal: tc.refusal, GraceStart: at, DueAt: at}
		if tc.held {
			s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}
		if got := taskActive(s); got != tc.want {
			t.Errorf("%s refused %q held %v: holds a slot %v, want %v", tc.phase, tc.refusal, tc.held, got, tc.want)
		}
	}
}

// TestTaskCleanupBusyShelfIsAskedOnceAMinute: a busy shelf is an answer kept
// for the minute, like any other, so queued Sessions do not ask it, or make
// the uncached reads, on every reconcile.
func TestTaskCleanupBusyShelfIsAskedOnceAMinute(t *testing.T) {
	f := newTaskFixture(t, nil)
	f.verifier.err = shelf.ErrSlotsBusy
	f.run(t, 6)
	if n := len(f.verifier.calls); n != 1 {
		t.Fatalf("the shelf was asked %d times in a minute; want once", n)
	}
	if !f.r.TaskCleanupPaused(*f.clock)[taskPauseShelfUnavailable] {
		t.Fatal("a busy shelf does not pause new starts")
	}
	f.tick(t, f.clock.Add(taskFreeCacheTTL+time.Second))
	f.run(t, 1)
	if n := len(f.verifier.calls); n != 2 {
		t.Fatalf("the shelf was asked %d times after the minute; want twice", n)
	}
	f.kept(t)
}

// TestTaskCleanupFourWorkersOnALaggingCacheStayUnderTheCap: one operator's
// four reconcile workers admit four due Sessions at once on a cache that has
// not seen any of their writes; the reservations keep it to the cap.
func TestTaskCleanupFourWorkersOnALaggingCacheStayUnderTheCap(t *testing.T) {
	f := newTaskFixture(t, nil)
	ctx := context.Background()
	var sessions []*v1alpha1.AgentSession
	for i := range 4 {
		s := f.session(t).DeepCopy()
		s.ObjectMeta = metav1.ObjectMeta{Name: fmt.Sprintf("lag-%d", i), Namespace: s.Namespace, UID: k8stypes.UID(fmt.Sprintf("uid-lag-%d", i)), Finalizers: []string{Finalizer}}
		at := metav1.NewTime(f.observed.Add(time.Duration(i) * time.Second))
		h := s.Status.PrivateHistory
		h.SessionUID = string(s.UID)
		h.Expiry = &v1alpha1.PrivateHomeExpiryStatus{Version: 1, ExecutorUID: h.LastExecutor.PodUID, HomeUID: h.HomeUID, GraceStart: at, DueAt: at, Phase: v1alpha1.HomeExpiryWaiting}
		st := s.Status
		if err := f.c.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			t.Fatal(err)
		}
		s.Status = st
		if err := f.c.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Delete(ctx, s); err != nil {
			t.Fatal(err)
		}
		if err := f.c.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, s)
	}
	// The cache: a list frozen before any admission.
	var frozen v1alpha1.AgentSessionList
	if err := f.c.List(ctx, &frozen, client.InNamespace(f.s.Namespace)); err != nil {
		t.Fatal(err)
	}
	r := f.fresh()
	r.Client = interceptor.NewClient(f.c, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
		if l, ok := list.(*v1alpha1.AgentSessionList); ok {
			frozen.DeepCopyInto(l)
			return nil
		}
		return c.List(ctx, list, opts...)
	}})
	var wg sync.WaitGroup
	admitted := make([]bool, len(sessions))
	for i, s := range sessions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admitted[i] = r.taskCleanupAdmittable(ctx, s, *f.clock, true)
		}()
	}
	wg.Wait()
	n := 0
	for _, a := range admitted {
		if a {
			n++
		}
	}
	if n != DefaultTaskCleanupMaxActive {
		t.Fatalf("%d of four admitted at once on a lagging cache; want the cap, %d", n, DefaultTaskCleanupMaxActive)
	}
	if !admitted[0] || !admitted[1] {
		t.Fatalf("admitted %v; want the two oldest", admitted)
	}
	// A Session reaped before them all shows up in the cache, which still
	// shows the two admitted ones Waiting: it heads the queue, but both
	// slots are reserved, so it waits.
	early := frozen.Items[0].DeepCopy()
	for i := range frozen.Items {
		if frozen.Items[i].Name == "lag-3" {
			early = frozen.Items[i].DeepCopy()
		}
	}
	early.Name, early.UID = "reaped-first", "uid-reaped-first"
	first := metav1.NewTime(early.DeletionTimestamp.Add(-time.Hour))
	early.DeletionTimestamp = &first
	frozen.Items = append(frozen.Items, *early)
	if r.taskCleanupAdmittable(ctx, early, f.clock.Add(time.Second), true) {
		t.Fatal("a third clean-up is admitted while the cache has not shown the two reserved ones")
	}
}
