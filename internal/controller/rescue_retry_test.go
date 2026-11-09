package controller

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

// The unit cases of the transient rescue retry (DESIGN-001 4.4, #486): the
// backoff arithmetic, which verdicts count as transient, and the schedule a
// record carries. The envtest cases are in rescue_retry_envtest_test.go.

// fetchFailedReport is goodReport with every clone's fetch failed, as in a DNS
// outage: the only problem is transient.
func fetchFailedReport(session string) protocol.RescueReport {
	rep := goodReport(session)
	for i := range rep.Repos {
		rep.Repos[i].Fetched = false
		rep.Repos[i].FetchError = "git fetch: exit status 128: fatal: unable to access 'https://github.com/thaynes43/haynes-ops/': Could not resolve host: github.com"
	}
	return rep
}

func TestTransientRetryDelayDoublesToItsCap(t *testing.T) {
	base := 15 * time.Minute
	for _, tc := range []struct {
		failures int32
		want     time.Duration
	}{
		{-1, 15 * time.Minute},
		{0, 15 * time.Minute},
		{1, 15 * time.Minute},
		{2, 30 * time.Minute},
		{3, time.Hour},
		{4, time.Hour},
		{50, time.Hour},
		{math.MaxInt32, time.Hour},
	} {
		if got := transientRetryDelay(base, tc.failures); got != tc.want {
			t.Errorf("transientRetryDelay(15m, %d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
	if got := transientRetryDelay(time.Second, 9); got != 4*time.Second {
		t.Errorf("the cap scales with the base: %s, want 4s", got)
	}
	// The first hour: retries 15 and 45 minutes after the first failure, so
	// an outage that ends within the hour is cleared within it.
	var elapsed time.Duration
	for n := int32(1); n <= 2; n++ {
		elapsed += transientRetryDelay(base, n)
	}
	if elapsed != 45*time.Minute {
		t.Errorf("the second retry runs %s after the first failure, want 45m", elapsed)
	}
}

// Only FetchFailed is transient, and a verdict names it only when every
// problem was a fetch failure: any permanent problem takes the reason.
func TestVerdictNamesATransientReasonOnlyWhenEveryProblemIsTransient(t *testing.T) {
	for _, reason := range []string{"FetchFailed"} {
		if !transientRescueReason(reason) {
			t.Errorf("%s is not transient", reason)
		}
	}
	for _, reason := range []string{"WorktreeRefused", "NotProven", "BootstrapInterrupted", "ReportMismatch", "AgentNotStopped",
		"AgentStillRunning", "RepoError", "BundleFailed", "RescueFailed", "VolumeEmpty", "Verified", "CleanAndPushed", ""} {
		if transientRescueReason(reason) {
			t.Errorf("%s is transient", reason)
		}
	}
	cases := []struct {
		name, reason string
		edit         func(*protocol.RescueReport)
	}{
		{"fetch failed everywhere", "FetchFailed", func(*protocol.RescueReport) {}},
		{"and a refused worktree in the same clone", "WorktreeRefused", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos[0].Worktrees[0].Refused = "an untracked nested git repo: vendor/x/"
		}},
		{"and a later clone with a refused worktree", "WorktreeRefused", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos = append(r.Repos, protocol.RepoRescue{Path: "/home/dev/repos/other", Fetched: true,
				Worktrees: []protocol.WorktreeRescue{{Path: "/home/dev/repos/other", Refused: "a merge or rebase is in progress (MERGE_HEAD)"}}})
		}},
		{"and a failed bundle", "BundleFailed", func(r *protocol.RescueReport) {
			r.OK = false
			r.Repos[0].Bundle.Verified, r.Repos[0].Bundle.Error = false, "git bundle create: exit status 128"
		}},
		{"and agentd not ok for no named reason", "RescueFailed", func(r *protocol.RescueReport) { r.OK = false }},
		{"and the agent still running", "AgentStillRunning", func(r *protocol.RescueReport) { r.Agent.Running = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rep := fetchFailedReport("s1")
			tc.edit(&rep)
			rec, reason := verdictFor(rep)
			if rec.Result != v1alpha1.RescueFailed || reason != tc.reason || !strings.Contains(rec.Message, "could not be fetched") {
				t.Errorf("result %s, reason %s, message %q; want Failed, %s", rec.Result, reason, rec.Message, tc.reason)
			}
		})
	}
}

// retrySession is an ordinary suspended session whose newest rescue is prev,
// with the RescueFailed condition the operator wrote for it.
func retrySession(prev *v1alpha1.RescueStatus, reason string) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "s1", Generation: 4},
		Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", OperatingMode: v1alpha1.OperatingModeSuspended}}
	s.Status.Rescue = prev
	if prev != nil {
		setRescueCondition(s, &s.Status, reason)
	}
	return s
}

func at(min int) *metav1.Time {
	t := metav1.NewTime(time.Date(2026, 11, 9, 10, min, 0, 0, time.UTC))
	return &t
}

func TestScheduleTransientRetry(t *testing.T) {
	base := 15 * time.Minute
	fetchFailed := func(s *v1alpha1.AgentSession, minute int) (*v1alpha1.RescueStatus, string) {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "hold-1"}}
		rec, reason := verdict(s, pod, fetchFailedReport("s1"), *at(minute))
		scheduleTransientRetry(s, rec, reason, base)
		return rec, reason
	}

	t.Run("a first transient failure waits the base", func(t *testing.T) {
		rec, _ := fetchFailed(retrySession(nil, ""), 6)
		if rec.TransientFailures != 1 || rec.NextRetryAt == nil || !rec.NextRetryAt.Equal(at(21)) {
			t.Fatalf("record %d at %v", rec.TransientFailures, rec.NextRetryAt)
		}
		if !strings.Contains(rec.Message, "runs again at 2026-11-09T10:21:00Z (transient failure 1 in a row)") {
			t.Errorf("message %q", rec.Message)
		}
	})
	t.Run("a later one doubles, up to the cap", func(t *testing.T) {
		prev := &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "pod-0", Generation: 4, TransientFailures: 1, NextRetryAt: at(21)}
		rec, _ := fetchFailed(retrySession(prev, "FetchFailed"), 22)
		if rec.TransientFailures != 2 || !rec.NextRetryAt.Equal(at(52)) {
			t.Fatalf("record %d at %v, want 2 at 10:52", rec.TransientFailures, rec.NextRetryAt)
		}
		prev.TransientFailures = 7
		rec, _ = fetchFailed(retrySession(prev, "FetchFailed"), 0)
		if rec.TransientFailures != 8 || !rec.NextRetryAt.Equal(&metav1.Time{Time: at(0).Add(time.Hour)}) {
			t.Fatalf("record %d at %v, want 8 an hour later", rec.TransientFailures, rec.NextRetryAt)
		}
		prev.TransientFailures = math.MaxInt32
		if rec, _ = fetchFailed(retrySession(prev, "FetchFailed"), 0); rec.TransientFailures != math.MaxInt32 {
			t.Errorf("the count wrapped: %d", rec.TransientFailures)
		}
	})
	t.Run("a record from before #486 counts as one", func(t *testing.T) {
		// dev-env-1103-181528's shape: Failed, FetchFailed on the
		// condition, and neither retry field.
		prev := &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(8), PodUID: "pod-0", Generation: 2, Stamp: "20261109-1006"}
		s := retrySession(prev, "FetchFailed")
		if n := transientFailures(s); n != 1 {
			t.Fatalf("legacy record counts %d", n)
		}
		if next, ok := transientRetryAt(s, base); !ok || !next.Equal(at(23).Time) {
			t.Fatalf("legacy retry at %v %v, want 10:23", next, ok)
		}
		if rec, _ := fetchFailed(s, 30); rec.TransientFailures != 2 || !rec.NextRetryAt.Equal(at(60)) {
			t.Errorf("record %d at %v, want 2 at 11:00", rec.TransientFailures, rec.NextRetryAt)
		}
	})
	t.Run("the count starts over after a permanent failure or a resume", func(t *testing.T) {
		permanent := &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "pod-0", Generation: 4}
		if rec, _ := fetchFailed(retrySession(permanent, "WorktreeRefused"), 6); rec.TransientFailures != 1 {
			t.Errorf("after a permanent failure: %d", rec.TransientFailures)
		}
		superseded := &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "pod-0", Generation: 3, TransientFailures: 3, NextRetryAt: at(30), Superseded: true}
		if rec, _ := fetchFailed(retrySession(superseded, ""), 6); rec.TransientFailures != 1 {
			t.Errorf("after a resume: %d", rec.TransientFailures)
		}
	})
	t.Run("a permanent verdict gets no retry", func(t *testing.T) {
		s := retrySession(nil, "")
		rec, reason := verdict(s, &corev1.Pod{}, failedReport("s1"), *at(6))
		scheduleTransientRetry(s, rec, reason, base)
		if reason != "WorktreeRefused" || rec.TransientFailures != 0 || rec.NextRetryAt != nil || strings.Contains(rec.Message, "runs again") {
			t.Errorf("reason %s, record %+v", reason, rec)
		}
		rec, reason = verdict(s, &corev1.Pod{}, goodReport("s1"), *at(6))
		scheduleTransientRetry(s, rec, reason, base)
		if rec.TransientFailures != 0 || rec.NextRetryAt != nil {
			t.Errorf("a verified rescue has a retry: %+v", rec)
		}
	})
	t.Run("shared and retained-history sessions keep their own retry", func(t *testing.T) {
		for name, edit := range map[string]func(*v1alpha1.AgentSession){
			"shared":    func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "w1"} },
			"retention": func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{} },
		} {
			s := retrySession(nil, "")
			edit(s)
			rec := &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), Message: "m"}
			scheduleTransientRetry(s, rec, "FetchFailed", base)
			if rec.TransientFailures != 0 || rec.NextRetryAt != nil || rec.Message != "m" {
				t.Errorf("%s: %+v", name, rec)
			}
			s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "pod-0"}
			setRescueCondition(s, &s.Status, "FetchFailed")
			if _, ok := transientRetryAt(s, base); ok {
				t.Errorf("%s: a legacy-shaped record is retried", name)
			}
		}
	})
}

func TestTransientRetryAtAndTheHoldRetry(t *testing.T) {
	base := 15 * time.Minute
	now := at(30).Time
	for _, tc := range []struct {
		name      string
		rec       *v1alpha1.RescueStatus
		reason    string
		transient bool
		next      time.Time
		holdDue   bool
	}{
		{"no record", nil, "", false, time.Time{}, false},
		{"verified", &v1alpha1.RescueStatus{Result: v1alpha1.RescueVerified, At: at(6), PodUID: "p"}, "Verified", false, time.Time{}, false},
		{"permanent", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "p"}, "NotProven", false, at(21).Time, true},
		{"permanent, recent", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(20), PodUID: "p"}, "WorktreeRefused", false, at(35).Time, false},
		{"transient, due", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "p", TransientFailures: 2, NextRetryAt: at(29)}, "FetchFailed", true, at(29).Time, true},
		{"transient, backing off", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(10), PodUID: "p", TransientFailures: 3, NextRetryAt: at(70)}, "FetchFailed", true, at(70).Time, false},
		{"legacy transient", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(10), PodUID: "p"}, "FetchFailed", true, at(25).Time, true},
		{"superseded", &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "p", TransientFailures: 1, NextRetryAt: at(21), Superseded: true}, "", false, at(21).Time, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := retrySession(tc.rec, tc.reason)
			next, ok := transientRetryAt(s, base)
			if ok != tc.transient || ok && !next.Equal(tc.next) {
				t.Errorf("transientRetryAt %v %v, want %v %v", next, ok, tc.next, tc.transient)
			}
			if got := holdRetryDue(s, now, base); got != tc.holdDue {
				t.Errorf("holdRetryDue %v, want %v", got, tc.holdDue)
			}
			if failed := tc.rec != nil && tc.rec.Result == v1alpha1.RescueFailed; failed {
				if got, _ := nextRescueRetry(s, base); !got.Equal(tc.next) {
					t.Errorf("nextRescueRetry %v, want %v", got, tc.next)
				}
			}
		})
	}
}

// The retry note survives a message at its limit.
func TestTheRetryNoteFitsTheMessage(t *testing.T) {
	long := strings.Repeat("x", maxMessage)
	got := withSuffix(long, "; runs again")
	if len(got) != maxMessage || !strings.HasSuffix(got, "...; runs again") {
		t.Errorf("len %d, tail %q", len(got), got[len(got)-20:])
	}
	if got := withSuffix("short", "; runs again"); got != "short; runs again" {
		t.Errorf("%q", got)
	}
	c := meta.FindStatusCondition(retrySession(&v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, Message: "m; runs again"}, "FetchFailed").Status.Conditions, ConditionRescueFailed)
	if c == nil || c.Message != "m; runs again" || c.Reason != "FetchFailed" {
		t.Errorf("RescueFailed %+v", c)
	}
}

// A due retry of a session that archive would give no hold pod creates none
// either, and RemovalBlocked says why; one not yet due only asks to be called
// back. Nothing is created in any case.
func TestARetryMakesNoHoldPodWhereArchiveWouldNot(t *testing.T) {
	now := at(40).Time
	legacy := func(edit func(*v1alpha1.AgentSession)) *v1alpha1.AgentSession {
		s := retrySession(&v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, At: at(6), PodUID: "pod-0", Generation: 4}, "FetchFailed")
		s.Namespace, s.UID, s.Spec.Mode = sessionNS, "uid-1", v1alpha1.ModeLocal
		if edit != nil {
			edit(s)
		}
		return s
	}
	run := func(r *Reconciler, s *v1alpha1.AgentSession) (wait time.Duration, pods int, why error) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s.DeepCopy()).Build()
		r.Client, r.APIReader = c, c
		obs := observation{now: now, claim: owned[*corev1.PersistentVolumeClaim]{obj: &corev1.PersistentVolumeClaim{}}}
		res, err := r.retryTransientRescue(context.Background(), s, &obs)
		if err != nil {
			t.Fatal(err)
		}
		var list corev1.PodList
		if err := c.List(context.Background(), &list); err != nil {
			t.Fatal(err)
		}
		return res.RequeueAfter, len(list.Items), obs.removalBlocked
	}
	for name, tc := range map[string]struct {
		r    *Reconciler
		edit func(*v1alpha1.AgentSession)
		why  error
	}{
		"original HOME binding": {&Reconciler{}, func(s *v1alpha1.AgentSession) {
			s.Annotations = map[string]string{homeguard.Annotation: "x"}
		}, homeguard.ErrUnsafe},
		"private history retention": {&Reconciler{PrivateHistoryRetention: true}, func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeTask
		}, errPrivateHistoryRetained},
	} {
		t.Run(name, func(t *testing.T) {
			wait, pods, why := run(tc.r, legacy(tc.edit))
			if wait != 0 || pods != 0 || !errors.Is(why, errTransientRetry) || !errors.Is(why, tc.why) {
				t.Errorf("requeue %s, %d pods, RemovalBlocked %v", wait, pods, why)
			}
		})
	}
	t.Run("not due yet", func(t *testing.T) {
		s := legacy(func(s *v1alpha1.AgentSession) { s.Status.Rescue.At = at(39) })
		wait, pods, why := run(&Reconciler{}, s)
		if wait != 14*time.Minute+time.Second || why != nil || pods != 0 {
			t.Errorf("requeue %s, %d pods, RemovalBlocked %v", wait, pods, why)
		}
	})
	t.Run("a permanent failure", func(t *testing.T) {
		s := legacy(func(s *v1alpha1.AgentSession) { setRescueCondition(s, &s.Status, "WorktreeRefused") })
		wait, pods, why := run(&Reconciler{}, s)
		if wait != 0 || why != nil || pods != 0 {
			t.Errorf("requeue %s, %d pods, RemovalBlocked %v", wait, pods, why)
		}
	})
}
