package controller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The envtest cases of the transient rescue retry (DESIGN-001 4.4, #486). The
// operator runs with HoldRetry of one second, so the backoff is 1s, 2s, then
// 4s for good. Status times have whole seconds, so a retry may run up to a
// second before its backoff ends, as it may in production.

// transientFailure checks that the newest rescue failed for a transient
// reason only, as the n-th in a row, and waits delay before its retry.
func transientFailure(n int32, delay time.Duration) func(*v1alpha1.AgentSessionStatus) error {
	return func(st *v1alpha1.AgentSessionStatus) error {
		r := st.Rescue
		c := condition(st, ConditionRescueFailed)
		switch {
		case r == nil || r.Result != v1alpha1.RescueFailed || r.TransientFailures != n:
			return fmt.Errorf("rescue %+v, want transient failure %d", r, n)
		case c == nil || c.Status != metav1.ConditionTrue || c.Reason != "FetchFailed":
			return fmt.Errorf("RescueFailed %+v", c)
		case r.At == nil || r.NextRetryAt == nil || r.NextRetryAt.Sub(r.At.Time) != delay:
			return fmt.Errorf("retry at %v after %v, want %s later", r.NextRetryAt, r.At, delay)
		}
		return nil
	}
}

// claimKept fails unless the session's volume is the one it started with and
// nothing is deleting it.
func claimKept(t *testing.T, session string, claim *corev1.PersistentVolumeClaim) {
	t.Helper()
	if c := waitClaim(t, session); c.UID != claim.UID || !c.DeletionTimestamp.IsZero() {
		t.Error("the volume changed or is being deleted")
	}
}

// Tom's case, dev-env-1103-181528: a suspend's rescue fails because GitHub
// cannot be resolved. The pod goes as before (D-10), the record says when the
// rescue runs again, and once that is due a hold pod runs it (D-55). It
// passes, so RescueFailed turns False, which clears the page (D-57), and the
// hold pod goes. Nothing else is deleted, and the volume waits for its archive
// timer as after any good rescue.
func TestATransientRescueFailureRetriesInAHoldPod(t *testing.T) {
	op := startOperator(t, func(r *Reconciler) { r.HoldRetry = time.Second })
	s, pod, claim := runningSession(t, "talosw02")
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		if isHoldPod(p) {
			return goodReport(p.Name), nil
		}
		return fetchFailedReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	got := waitStatus(t, s.Name, "a transient failure", transientFailure(1, time.Second))
	if got.Status.Rescue.PodUID != string(pod.UID) {
		t.Errorf("the first rescue ran in %s, not the session pod", got.Status.Rescue.PodUID)
	}
	paging := "dev_env_session_rescue_failed{reason=FetchFailed,session=" + s.Name + "} 1"
	if m := rescueFailedSeries(t, s.Name); len(m) != 1 || m[0] != paging {
		t.Errorf("the page's series %v, want %s", m, paging)
	}
	hold := waitHoldPod(t, s.Name)
	got = waitStatus(t, s.Name, "the hold pod waits to start", blockedBy("SuspendNeedsRescue"))
	if c := condition(&got.Status, ConditionRemovalBlocked); c == nil || !containsAll(c.Message, "hold pod", errTransientRetry.Error(), "every problem is transient") {
		t.Errorf("RemovalBlocked %+v", c)
	}
	hold = markRunning(t, hold, "talosw03")
	finishPodDelete(t, hold)
	got = waitStatus(t, s.Name, "the retry rescued the volume", rescueIs(v1alpha1.RescueVerified))
	r := got.Status.Rescue
	if r.PodUID != string(hold.UID) || r.TransientFailures != 0 || r.NextRetryAt != nil || r.Superseded {
		t.Errorf("rescue %+v", r)
	}
	if c := condition(&got.Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Verified" {
		t.Errorf("RescueFailed %+v", c)
	}
	// DevEnvRescueFailed reads this series; it ends with the failure.
	if m := rescueFailedSeries(t, s.Name); len(m) != 0 {
		t.Errorf("the page's series %v after the rescue passed", m)
	}
	got = waitStatus(t, s.Name, "Suspended with no pod", func(st *v1alpha1.AgentSessionStatus) error {
		if st.Phase != v1alpha1.PhaseSuspended || st.PodName != "" {
			return fmt.Errorf("phase %s, pod %q", st.Phase, st.PodName)
		}
		return nil
	})
	if got.Status.ArchivedAt != nil {
		t.Error("the retry archived the volume")
	}
	if n := rescuer.callsFor(s.Name); n != 2 {
		t.Errorf("%d rescues, want the suspend's and one retry", n)
	}
	claimKept(t, s.Name, claim)
	// The session pod and then the hold pod, which has its name; nothing else.
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// A transient failure that persists: the hold pod stays and runs the rescue
// again, each time after a longer wait, up to the cap, and nothing is deleted
// meanwhile.
func TestTransientRetriesBackOffToACap(t *testing.T) {
	op := startOperator(t, func(r *Reconciler) { r.HoldRetry = time.Second })
	s, pod, claim := runningSession(t, "talosw02")
	var mu sync.Mutex
	var calls []time.Time
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		mu.Lock()
		calls = append(calls, time.Now())
		mu.Unlock()
		return fetchFailedReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	waitStatus(t, s.Name, "the first transient failure", transientFailure(1, time.Second))
	hold := markRunning(t, waitHoldPod(t, s.Name), "talosw03")
	for _, step := range []struct {
		n     int32
		delay time.Duration
	}{{2, 2 * time.Second}, {3, 4 * time.Second}, {4, 4 * time.Second}} {
		got := waitStatus(t, s.Name, fmt.Sprintf("transient failure %d, and the hold pod says when it runs again", step.n), func(st *v1alpha1.AgentSessionStatus) error {
			if err := transientFailure(step.n, step.delay)(st); err != nil {
				return err
			}
			next := "the rescue in it failed and runs again at " + st.Rescue.NextRetryAt.UTC().Format(time.RFC3339)
			if c := condition(st, ConditionRemovalBlocked); c == nil || !strings.Contains(c.Message, next) {
				return fmt.Errorf("RemovalBlocked %+v, want %q", c, next)
			}
			return nil
		})
		if got.Status.Rescue.PodUID != string(hold.UID) {
			t.Errorf("failure %d ran in %s, not the hold pod", step.n, got.Status.Rescue.PodUID)
		}
		if p := waitPod(t, s.Name); p.UID != hold.UID || !p.DeletionTimestamp.IsZero() {
			t.Fatal("the hold pod went while its rescue failed")
		}
		claimKept(t, s.Name, claim)
	}
	mu.Lock()
	got := append([]time.Time(nil), calls...)
	mu.Unlock()
	// The retries never ran sooner than their backoff, less the second a
	// status time drops: 2s before the third rescue, 4s before the fourth.
	if len(got) < 4 || got[2].Sub(got[1]) < time.Second || got[3].Sub(got[2]) < 3*time.Second {
		t.Errorf("rescues at %v", got)
	}
	if c := condition(&session(t, s.Name).Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("RescueFailed %+v while the failures go on", c)
	}
	assertWritesOnly(t, op, "delete *v1.Pod "+pod.Name)
}

// noRetryFor fails if the session gets a pod, or a second rescue, within d.
func noRetryFor(t *testing.T, name string, d time.Duration) {
	t.Helper()
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if _, err := get(t, sessionNS, name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
			t.Fatalf("session %s has a pod again (%v): a permanent failure was retried", name, err)
		}
		if n := rescuer.callsFor(name); n != 1 {
			t.Fatalf("%d rescues of %s, want only the suspend's", n, name)
		}
	}
}

// A permanent reason keeps today's behaviour: the suspended session keeps its
// volume and waits for a human, with no hold pod before its archive timer. A
// fetch failure beside a refused worktree is permanent too, and the condition
// names the refusal.
func TestAPermanentRescueFailureIsNotRetried(t *testing.T) {
	op := startOperator(t, func(r *Reconciler) { r.HoldRetry = time.Second })
	refused, refusedPod, refusedClaim := runningSession(t, "talosw02")
	mixed, mixedPod, mixedClaim := runningSession(t, "talosw03")
	rescuer.answer(t, refused.Name, reportFor(failedReport))
	rescuer.answer(t, mixed.Name, reportFor(func(session string) protocol.RescueReport {
		rep := fetchFailedReport(session)
		rep.OK = false
		rep.Repos[0].Worktrees[0].Refused = "an untracked nested git repo: vendor/x/"
		return rep
	}))
	setMode(t, refused.Name, v1alpha1.OperatingModeSuspended)
	setMode(t, mixed.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, refusedPod)
	finishPodDelete(t, mixedPod)
	for _, name := range []string{refused.Name, mixed.Name} {
		got := waitStatus(t, name, "Suspended after a permanent failure", phaseIs(v1alpha1.PhaseSuspended))
		r := got.Status.Rescue
		if r == nil || r.Result != v1alpha1.RescueFailed || r.TransientFailures != 0 || r.NextRetryAt != nil || containsAll(r.Message, "runs again") {
			t.Errorf("%s: rescue %+v", name, r)
		}
		if c := condition(&got.Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionTrue || c.Reason != "WorktreeRefused" {
			t.Errorf("%s: RescueFailed %+v", name, c)
		}
	}
	// Four backoff bases pass with no hold pod and no second rescue.
	noRetryFor(t, refused.Name, 4*time.Second)
	noRetryFor(t, mixed.Name, time.Second)
	claimKept(t, refused.Name, refusedClaim)
	claimKept(t, mixed.Name, mixedClaim)
	if w := op.writes.writesTo(); len(w) != 2 {
		t.Errorf("writes %v, want the two suspends' pod deletes only", w)
	}
}

// An operator before #486 wrote dev-env-1103-181528's record: Failed,
// FetchFailed on the condition, and no retry fields. The new operator retries
// it as a first transient failure, and nothing is deleted on the way.
func TestATransientRecordFromAnOlderOperatorIsRetried(t *testing.T) {
	old := startOperator(t) // HoldRescueRetry: no retry within the test
	s, pod, claim := runningSession(t, "talosw02")
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		if isHoldPod(p) {
			return goodReport(p.Name), nil
		}
		return fetchFailedReport(p.Name), nil
	})
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	finishPodDelete(t, pod)
	waitStatus(t, s.Name, "a transient failure", transientFailure(1, HoldRescueRetry))
	old.stop()
	assertWritesOnly(t, old, "delete *v1.Pod "+pod.Name)
	eventually(t, "the record as an older operator wrote it", func() error {
		live := session(t, s.Name)
		live.Status.Rescue.TransientFailures, live.Status.Rescue.NextRetryAt = 0, nil
		return k8s.Status().Update(context.Background(), live)
	})
	if r := session(t, s.Name).Status.Rescue; r.TransientFailures != 0 || r.NextRetryAt != nil {
		t.Fatalf("rescue %+v", r)
	}
	op := startOperator(t, func(r *Reconciler) { r.HoldRetry = time.Second })
	hold := markRunning(t, waitHoldPod(t, s.Name), "talosw03")
	finishPodDelete(t, hold)
	got := waitStatus(t, s.Name, "the retry rescued the volume", rescueIs(v1alpha1.RescueVerified))
	if c := condition(&got.Status, ConditionRescueFailed); c == nil || c.Status != metav1.ConditionFalse {
		t.Errorf("RescueFailed %+v", c)
	}
	claimKept(t, s.Name, claim)
	assertWritesOnly(t, op, "delete *v1.Pod "+hold.Name)
}

// rescueFailedSeries is what the operator's collector serves for the session
// on dev_env_session_rescue_failed, the series DevEnvRescueFailed reads (D-57).
func rescueFailedSeries(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	for _, m := range gather(t, &SessionCollector{Reader: k8s, Namespace: sessionNS}) {
		if strings.HasPrefix(m, MetricRescueFailed+"{") && strings.Contains(m, "session="+name+"}") {
			out = append(out, m)
		}
	}
	return out
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
