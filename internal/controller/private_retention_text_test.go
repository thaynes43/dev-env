package controller

// DESIGN-025 section 25 (OP490): a protected private Session that wants its
// pod gone says what is true of its agent and HOME, and a refused terminal
// check backs off instead of repeating every 30 seconds.

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// reapedTerminalFixture is newTerminalFixture's Session reaped a minute ago:
// its recorded executor Pod ended Failed (DeadlineExceeded).
func reapedTerminalFixture() terminalFixture {
	f := newTerminalFixture()
	f.s.DeletionTimestamp = &metav1.Time{Time: f.now.Add(-time.Minute)}
	return f
}

// retentionConditions returns the three conditions a reaped private Session
// shows, and fails unless the phase is Pending with the pending reason that
// PodReady says.
func retentionConditions(t *testing.T, s *v1alpha1.AgentSession) (removal, history, ready *metav1.Condition) {
	t.Helper()
	removal = meta.FindStatusCondition(s.Status.Conditions, ConditionRemovalBlocked)
	history = meta.FindStatusCondition(s.Status.Conditions, ConditionPrivateHistory)
	ready = meta.FindStatusCondition(s.Status.Conditions, ConditionPodReady)
	if removal == nil || history == nil || ready == nil {
		t.Fatalf("a condition is missing: %+v", s.Status.Conditions)
	}
	if s.Status.Phase != v1alpha1.PhasePending || s.Status.PendingReason != ready.Message || removal.Status != metav1.ConditionTrue {
		t.Fatalf("phase %s, pending %q, RemovalBlocked %s", s.Status.Phase, s.Status.PendingReason, removal.Status)
	}
	return removal, history, ready
}

// The two cases have distinct reasons and say, in plain words, what is true:
// stop proven by the terminal record (ExecutorEnded), or not proven and why
// (RetentionPending). Lifecycle is unchanged: the Pod, the HOME and the
// finalizer all stay.
func TestReapedPrivateSessionSaysWhatIsTrue(t *testing.T) {
	const (
		stopped = "reap accepted; the agent stopped: its Pod ended Failed (DeadlineExceeded), recorded 2026-10-12T12:00:00Z; " +
			"its private HOME is kept, and nothing is archived or deleted yet; " +
			"the session is released only after the HOME expiry (DESIGN-025) archives its history and deletes the HOME, and the history archive is not switched on yet"
		refused = "reap accepted; the agent's stop is not proven yet: its Pod ended Failed, but the operator cannot confirm that yet " +
			"(executor has a duplicate, uncertain or nonterminated current container); it checks again after a wait that doubles from 30 seconds to at most 30 minutes; " +
			"its private HOME is kept, and nothing is archived or deleted yet; " +
			"the session is released only after its stop is proven and the HOME expiry (DESIGN-025) archives its history and deletes the HOME, and the history archive is not switched on yet"
		requested = "reap accepted; the agent's stop is not proven yet: a stop was requested at 2026-10-12T11:59:30Z, and its Pod has not ended yet; " +
			"its private HOME is kept, and nothing is archived or deleted yet; " +
			"the session is released only after its stop is proven and the HOME expiry (DESIGN-025) archives its history and deletes the HOME, and the history archive is not switched on yet"
		reclaimed = "reap accepted; the agent stopped: its Pod ended Failed (DeadlineExceeded), recorded 2026-10-12T12:00:00Z; its Pod was then removed to free compute; " +
			"its private HOME is kept, and nothing is archived or deleted yet; " +
			"the session is released only after the HOME expiry (DESIGN-025) archives its history and deletes the HOME, and the history archive is not switched on yet"
	)
	running := func(f *terminalFixture) {
		f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: f.now.Add(-time.Hour)},
			ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(f.now.Add(-time.Hour))}}}}}
	}
	for _, tc := range []struct {
		name            string
		edit            func(*terminalFixture)
		podGone         bool
		reason, message string
		history         string
		// PodReady's, when they differ from RemovalBlocked's.
		readyReason, readyMessage string
	}{
		{name: "terminal record, Pod standing", edit: func(f *terminalFixture) { recordTerminal(*f) }, reason: "ExecutorEnded", message: stopped, history: "ExecutorEnded"},
		{name: "terminal record, Pod gone", edit: func(f *terminalFixture) { recordTerminal(*f) }, podGone: true, reason: "ExecutorEnded", message: stopped, history: "ExecutorEnded"},
		// The live case: the Pod is Failed, but a container status is
		// unknown, so the record is refused and the condition says why.
		{name: "terminal Pod, record refused", edit: func(f *terminalFixture) {
			f.pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerStatusUnknown"}}
		}, reason: "RetentionPending", message: refused, history: "RetentionPending"},
		{name: "stop requested, Pod running", edit: func(f *terminalFixture) {
			running(f)
			h := f.s.Status.PrivateHistory
			h.StopRequest = &v1alpha1.PrivateExecutorStopRequestStatus{Version: 1, PodUID: h.LastExecutor.PodUID, RequestedAt: recordTime(f.now.Add(-30 * time.Second))}
		}, reason: "RetentionPending", message: requested, history: "RetentionPending"},
		// Reaped after the terminal task reclaim removed its ended Pod.
		{name: "task compute reclaimed", edit: func(f *terminalFixture) {
			*f = terminalTaskFixture(true)
			f.s.DeletionTimestamp = &metav1.Time{Time: f.now.Add(-time.Minute)}
			recordTerminal(*f)
		}, podGone: true, reason: "TaskComputeReclaimed", message: reclaimed, history: "TaskComputeReclaimed", readyReason: "ExecutorEnded", readyMessage: stopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reapedTerminalFixture()
			tc.edit(&f)
			var c client.WithWatch
			if tc.podGone {
				c = f.client(f.pod)
			} else {
				c = f.client()
			}
			reconcileSession(t, f.reconciler(c, c, NewPrivateExecutorTerminalCounter()), f.s, 3)
			got := storedHistory(t, c, f.s)
			removal, history, ready := retentionConditions(t, got)
			if removal.Reason != tc.reason || removal.Message != tc.message {
				t.Errorf("RemovalBlocked\n got %s: %s\nwant %s: %s", removal.Reason, removal.Message, tc.reason, tc.message)
			}
			if tc.readyReason == "" {
				tc.readyReason, tc.readyMessage = tc.reason, tc.message
			}
			if ready.Reason != tc.readyReason || ready.Message != tc.readyMessage {
				t.Errorf("PodReady\n got %s: %s\nwant %s: %s", ready.Reason, ready.Message, tc.readyReason, tc.readyMessage)
			}
			if history.Reason != tc.history || history.Message != tc.message {
				t.Errorf("PrivateHistoryRetained\n got %s: %s\nwant %s: %s", history.Reason, history.Message, tc.history, tc.message)
			}
			for _, old := range []string{"stop state is Unknown", "stop evidence is unknown"} {
				for _, cond := range got.Status.Conditions {
					if strings.Contains(cond.Message, old) {
						t.Errorf("%s still says %q", cond.Type, old)
					}
				}
			}
			// Nothing in the lifecycle changed: the HOME, the finalizer and
			// stopState stay; a standing Pod stays.
			if !strings.Contains(strings.Join(got.Finalizers, ","), Finalizer) || got.Status.PrivateHistory.StopState != "Unknown" {
				t.Fatalf("the finalizer or stopState changed: %v %q", got.Finalizers, got.Status.PrivateHistory.StopState)
			}
			var home corev1.PersistentVolumeClaim
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil || home.UID != f.home.UID {
				t.Fatal("the HOME was touched", err)
			}
			if !tc.podGone {
				var pod corev1.Pod
				if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.UID != f.pod.UID {
					t.Fatal("the executor Pod was touched", err)
				}
			}
		})
	}
}

// A suspended private Session says the same in its own words, and a running
// one names a refused terminal check in PrivateHistoryRetained.
func TestSuspendedAndRunningPrivateSessionsSayWhatIsTrue(t *testing.T) {
	t.Run("suspended, Pod running", func(t *testing.T) {
		f := newTerminalFixture()
		f.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}
		c := f.client()
		reconcileSession(t, f.reconciler(c, c, nil), f.s, 3)
		got := storedHistory(t, c, f.s)
		want := "suspended; the agent's stop is not proven yet: its Pod keeps running, because a suspend does not stop a private agent; its private HOME is kept, and nothing is archived or deleted yet"
		if c := meta.FindStatusCondition(got.Status.Conditions, ConditionRemovalBlocked); c == nil || c.Reason != "RetentionPending" || c.Message != want {
			t.Fatalf("RemovalBlocked = %+v\nwant RetentionPending: %s", c, want)
		}
	})
	t.Run("running, Pod ended, record refused", func(t *testing.T) {
		f := newTerminalFixture()
		f.node.Status.Conditions[0].Status = corev1.ConditionFalse
		c := f.client()
		reconcileSession(t, f.reconciler(c, c, nil), f.s, 3)
		got := storedHistory(t, c, f.s)
		want := "the private HOME and the session's record are protected (DESIGN-025); its Pod ended Failed, but the operator cannot confirm that yet " +
			"(executor node is missing, deleting, unready or unknown); it checks again after a wait that doubles from 30 seconds to at most 30 minutes"
		if c := meta.FindStatusCondition(got.Status.Conditions, ConditionPrivateHistory); c == nil || c.Reason != "HomeProtected" || c.Message != want {
			t.Fatalf("PrivateHistoryRetained = %+v\nwant HomeProtected: %s", c, want)
		}
	})
}

func TestTerminalRetryAfterDoublesToThirtyMinutes(t *testing.T) {
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute, 30 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got := terminalRetryAfter(i + 1); got != w {
			t.Errorf("refusal %d: wait %v, want %v", i+1, got, w)
		}
	}
	if got := terminalRetryAfter(1000); got != privateTerminalRetryMax {
		t.Errorf("a long run of refusals waits %v", got)
	}
}

// The refused terminal check backs off on a fake clock: a reconcile inside the
// wait checks nothing and counts nothing; each refusal of the same Pod revision
// doubles the wait up to 30 minutes; a changed Pod is checked at once; once the
// checks pass, the record is written and the backoff is forgotten. What is
// refused is unchanged.
func TestPrivateTerminalRefusalBacksOff(t *testing.T) {
	f := reapedTerminalFixture()
	f.node.Status.Conditions[0].Status = corev1.ConditionFalse
	c := f.client()
	counter := NewPrivateExecutorTerminalCounter()
	now := f.now
	r := f.reconciler(c, c, counter)
	r.Now = func() time.Time { return now }
	// Keep the lease fresh at every step, so only the node refuses.
	renew := func() {
		var lease = f.lease.DeepCopy()
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.lease), lease); err != nil {
			t.Fatal(err)
		}
		lease.Spec.RenewTime.Time = now.Add(-time.Second)
		if err := c.Update(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
	}
	step := func(at time.Duration, refused float64, wait time.Duration) {
		t.Helper()
		now = f.now.Add(at)
		renew()
		res := reconcileSession(t, r, f.s, 1)
		if n := terminalCount(counter, terminalRefused); n != refused {
			t.Fatalf("at +%v: refused counted %v times, want %v", at, n, refused)
		}
		if res.RequeueAfter != wait {
			t.Fatalf("at +%v: asks again after %v, want %v", at, res.RequeueAfter, wait)
		}
	}
	step(0, 1, 30*time.Second)
	step(0, 1, 30*time.Second) // a status event inside the wait checks nothing
	step(10*time.Second, 1, 20*time.Second)
	step(30*time.Second, 2, time.Minute)
	step(90*time.Second, 3, 2*time.Minute)
	elapsed := 90 * time.Second
	for _, wait := range []time.Duration{2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute} {
		elapsed += wait
		step(elapsed, terminalCount(counter, terminalRefused)+1, min(2*wait, privateTerminalRetryMax))
	}
	elapsed += 30 * time.Minute
	step(elapsed, 8, 30*time.Minute)
	step(elapsed+time.Minute, 8, 29*time.Minute)

	// The refusal shows once, in the condition, with the reason.
	got := storedHistory(t, c, f.s)
	if c := meta.FindStatusCondition(got.Status.Conditions, ConditionRemovalBlocked); c == nil || c.Reason != "RetentionPending" ||
		!strings.Contains(c.Message, "(executor node is missing, deleting, unready or unknown)") {
		t.Fatalf("the refusal is not in RemovalBlocked: %+v", c)
	}

	// A changed Pod revision is checked at once, still refused, and the
	// wait stays at the cap.
	var pod corev1.Pod
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Annotations["fixture/changed"] = "true"
	if err := c.Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	step(elapsed+2*time.Minute, 9, 30*time.Minute)

	// The node is Ready again: the next due check records the stop.
	var node corev1.Node
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.node), &node); err != nil {
		t.Fatal(err)
	}
	node.Status.Conditions[0].Status = corev1.ConditionTrue
	if err := c.Status().Update(context.Background(), &node); err != nil {
		t.Fatal(err)
	}
	now = f.now.Add(elapsed + 32*time.Minute)
	renew()
	reconcileSession(t, r, f.s, 3)
	got = storedHistory(t, c, f.s)
	if got.Status.PrivateHistory.ExecutorTerminal == nil || terminalCount(counter, terminalRecorded) != 1 || terminalCount(counter, terminalRefused) != 9 {
		t.Fatalf("the due check did not record the stop: %+v", got.Status.PrivateHistory.ExecutorTerminal)
	}
	if c := meta.FindStatusCondition(got.Status.Conditions, ConditionRemovalBlocked); c == nil || c.Reason != "ExecutorEnded" {
		t.Fatalf("RemovalBlocked after the record = %+v", c)
	}
	r.terminalMu.Lock()
	left := len(r.terminalRefusals)
	r.terminalMu.Unlock()
	if left != 0 {
		t.Fatalf("the backoff kept %d entries after the record", left)
	}
}
