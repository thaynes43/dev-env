package controller

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// gather registers the collector on its own registry and returns each series as
// "name{label=value,...} value", sorted.
func gather(t *testing.T, c prometheus.Collector) []string {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	reg.MustRegister(c)
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			var labels []string
			for _, l := range m.GetLabel() {
				labels = append(labels, l.GetName()+"="+l.GetValue())
			}
			out = append(out, mf.GetName()+"{"+strings.Join(labels, ",")+"} "+strconv.FormatFloat(m.GetGauge().GetValue(), 'g', -1, 64))
		}
	}
	sort.Strings(out)
	return out
}

func sessionWith(name string, phase v1alpha1.SessionPhase, conds ...metav1.Condition) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents"}}
	s.Status.Phase = phase
	s.Status.Conditions = conds
	return s
}

// D-57: one series per session whose newest rescue failed, with the verdict's
// reason, and a count per phase that exists even when nothing failed.
func TestSessionCollector(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	failed := metav1.Condition{Type: ConditionRescueFailed, Status: metav1.ConditionTrue, Reason: "WorktreeRefused"}
	passed := metav1.Condition{Type: ConditionRescueFailed, Status: metav1.ConditionFalse, Reason: "Verified"}
	objs := []runtime.Object{
		sessionWith("a", v1alpha1.PhaseRunning),
		sessionWith("b", v1alpha1.PhaseSuspended, failed),
		sessionWith("c", v1alpha1.PhaseSuspended, passed),
		sessionWith("d", ""),
	}
	other := sessionWith("e", v1alpha1.PhaseSuspended, failed)
	other.Namespace = "elsewhere"
	objs = append(objs, other)
	c := &SessionCollector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build(), Namespace: "dev-agents"}
	got := gather(t, c)
	want := []string{
		"dev_env_reaped_sessions_oldest_seconds{reason=DeleteNeedsRescue} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=ExecutorEnded} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=HomeDeleting} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=HomeExpired} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=Other} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=PreservationUnknown} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=RetentionPending} 0",
		"dev_env_reaped_sessions_oldest_seconds{reason=TaskComputeReclaimed} 0",
		"dev_env_reaped_sessions_retained{reason=DeleteNeedsRescue} 0",
		"dev_env_reaped_sessions_retained{reason=ExecutorEnded} 0",
		"dev_env_reaped_sessions_retained{reason=HomeDeleting} 0",
		"dev_env_reaped_sessions_retained{reason=HomeExpired} 0",
		"dev_env_reaped_sessions_retained{reason=Other} 0",
		"dev_env_reaped_sessions_retained{reason=PreservationUnknown} 0",
		"dev_env_reaped_sessions_retained{reason=RetentionPending} 0",
		"dev_env_reaped_sessions_retained{reason=TaskComputeReclaimed} 0",
		"dev_env_session_rescue_failed{reason=WorktreeRefused,session=b} 1",
		"dev_env_session_rescue_notification{owner_uid=,pending_delete=false,reason=WorktreeRefused,risk_key=unclassified,session=b,session_generation=0,session_uid=} 1",
		"dev_env_sessions{phase=Archived} 0",
		"dev_env_sessions{phase=Draining} 0",
		"dev_env_sessions{phase=Failed} 0",
		"dev_env_sessions{phase=Idle} 0",
		"dev_env_sessions{phase=Pending} 0",
		"dev_env_sessions{phase=Running} 1",
		"dev_env_sessions{phase=Suspended} 2",
		"dev_env_sessions{phase=Unknown} 1",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestTaskCleanupRescueLeavesTheRescueFailedGauge: a failed rescue the task
// clean-up ran in its own hold pod is absent from dev_env_session_rescue_failed.
// The same failure with no clean-up record, and an earlier rescue the clean-up
// has not replaced, still report 1. The refusal is counted by
// dev_env_task_cleanup_sessions{state="refused"} (task_cleanup_metrics.go, the
// case e.Refusal != "").
func TestTaskCleanupRescueLeavesTheRescueFailedGauge(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	at := metav1.NewTime(time.Date(2026, 11, 10, 13, 31, 0, 0, time.UTC))
	failed := metav1.Condition{Type: ConditionRescueFailed, Status: metav1.ConditionTrue, Reason: "WorktreeRefused", LastTransitionTime: at}
	rescue := func(name string, mode v1alpha1.SessionMode, holdUID, rescueUID, refusal string) *v1alpha1.AgentSession {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents"}}
		s.Spec.Mode = mode
		s.Status.Phase = v1alpha1.PhaseSuspended
		s.Status.Conditions = []metav1.Condition{failed}
		s.Status.Rescue = &v1alpha1.RescueStatus{Result: v1alpha1.RescueFailed, PodUID: rescueUID, At: &at}
		if mode != v1alpha1.ModeTask {
			return s
		}
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{
			Version:          1,
			LastExecutor:     &v1alpha1.PrivateExecutorStatus{PodUID: "exec-" + name},
			ExecutorTerminal: &v1alpha1.PrivateExecutorTerminalStatus{Version: 1, PodUID: "exec-" + name, ObservedAt: at},
		}
		if holdUID != "" || refusal != "" {
			s.Status.PrivateHistory.Expiry = &v1alpha1.PrivateHomeExpiryStatus{
				Version: 1, Phase: v1alpha1.HomeExpiryRescuing, HoldPodUID: holdUID, Refusal: refusal,
			}
		}
		return s
	}
	// The live case: the refusal and the hold pod are the same rescue.
	cleanup := rescue("cleanup", v1alpha1.ModeTask, "hold-cleanup", "hold-cleanup", expiryRescueRefused)
	// The clean-up's rescue is stored, and rescue_refused is not, yet.
	held := rescue("held", v1alpha1.ModeTask, "hold-held", "hold-held", "")
	// An earlier rescue failed. The clean-up has a record and has not rescued.
	earlier := rescue("earlier", v1alpha1.ModeTask, "hold-next", "old-executor", "")
	// The same failed rescue, with no clean-up record.
	plain := rescue("plain", v1alpha1.ModeRemote, "", "hold-cleanup", "")
	// The daily retry cleared the hold pod UID and wrote a new intent. The
	// failed rescue is still the previous hold's, not the executor's.
	retry := rescue("retry", v1alpha1.ModeTask, "", "old-hold", "")
	retry.Status.PrivateHistory.Expiry = &v1alpha1.PrivateHomeExpiryStatus{
		Version: 1, Phase: v1alpha1.HomeExpiryRescuing, ExecutorUID: "exec-retry", HoldIntent: "20261111T120000Z/retry-intent",
	}
	// The same gap, but the failed rescue ran in the recorded executor.
	executor := rescue("executor", v1alpha1.ModeTask, "", "exec-executor", "")
	executor.Status.PrivateHistory.Expiry = &v1alpha1.PrivateHomeExpiryStatus{
		Version: 1, Phase: v1alpha1.HomeExpiryRescuing, ExecutorUID: "exec-executor", HoldIntent: "20261111T120000Z/executor-intent",
	}
	series := func(name string) string {
		return "dev_env_session_rescue_failed{reason=WorktreeRefused,session=" + name + "} 1"
	}
	got := strings.Join(gather(t, &SessionCollector{
		Reader:    fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cleanup, held, earlier, plain, retry, executor).Build(),
		Namespace: "dev-agents", TaskCleanupOn: true,
	}), "\n")
	for _, name := range []string{"cleanup", "held", "retry"} {
		if strings.Contains(got, series(name)) {
			t.Fatalf("the clean-up's rescue %s is in the gauge:\n%s", name, got)
		}
	}
	for _, name := range []string{"earlier", "plain", "executor"} {
		if !strings.Contains(got, series(name)) {
			t.Fatalf("missing %s in\n%s", series(name), got)
		}
	}
	off := strings.Join(gather(t, &SessionCollector{
		Reader: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cleanup).Build(), Namespace: "dev-agents",
	}), "\n")
	if !strings.Contains(off, series("cleanup")) {
		t.Fatalf("with the clean-up off the refusal is not counted:\n%s", off)
	}
	task := strings.Join(gather(t, &TaskCleanupCollector{
		Reader: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(cleanup).Build(), Namespace: "dev-agents",
		Reconciler: &Reconciler{TaskSessionCleanup: true, PrivateTerminalRecord: true, PrivateExecutorStop: true},
	}), "\n")
	if !strings.Contains(task, "dev_env_task_cleanup_sessions{state=refused} 1") {
		t.Fatalf("rescue_refused is not state refused:\n%s", task)
	}
}

func TestTerminalTaskReclaimCollectorRetainedCountAndAge(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	s := sessionWith("held-task", v1alpha1.PhaseSuspended, metav1.Condition{Type: ConditionTerminalTaskReclaim, Status: metav1.ConditionTrue, Reason: "Held"})
	s.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
	s.Status.PodName = s.Name
	reclaimed := sessionWith("reclaimed-task", v1alpha1.PhaseSuspended, metav1.Condition{Type: ConditionTerminalTaskReclaim, Status: metav1.ConditionFalse, Reason: "Reclaimed"})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, reclaimed).Build()
	got := strings.Join(gather(t, &SessionCollector{Reader: c, Namespace: s.Namespace, TerminalTaskReclaim: true, Now: func() time.Time { return now }}), "\n")
	for _, want := range []string{"dev_env_terminal_task_pods_retained{reason=Held} 1", "dev_env_terminal_task_pods_oldest_seconds{reason=Held} 3600", "dev_env_terminal_task_pods_retained{reason=ProofUnknown} 0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
}

// DESIGN-025 25: reaped sessions still kept, by RemovalBlocked reason, with
// the age of the oldest reap. A session not reaped, or whose RemovalBlocked
// is not True, is not counted; an unknown reason counts as Other.
func TestReapedSessionsRetainedCountAndAge(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	reaped := func(name string, ago time.Duration, reason string, status metav1.ConditionStatus) *v1alpha1.AgentSession {
		s := sessionWith(name, v1alpha1.PhasePending, metav1.Condition{Type: ConditionRemovalBlocked, Status: status, Reason: reason})
		s.DeletionTimestamp = &metav1.Time{Time: now.Add(-ago)}
		s.Finalizers = []string{Finalizer}
		return s
	}
	suspended := sessionWith("suspended", v1alpha1.PhaseSuspended, metav1.Condition{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue, Reason: "RetentionPending"})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		reaped("stopped-old", 3*time.Hour, "ExecutorEnded", metav1.ConditionTrue),
		reaped("stopped-new", time.Hour, "ExecutorEnded", metav1.ConditionTrue),
		reaped("unproven", 2*time.Hour, "RetentionPending", metav1.ConditionTrue),
		reaped("odd", time.Minute, "SomethingNew", metav1.ConditionTrue),
		reaped("released", 4*time.Hour, "RetentionPending", metav1.ConditionFalse),
		suspended).Build()
	got := strings.Join(gather(t, &SessionCollector{Reader: c, Namespace: "dev-agents", Now: func() time.Time { return now }}), "\n")
	for _, want := range []string{
		"dev_env_reaped_sessions_retained{reason=ExecutorEnded} 2",
		"dev_env_reaped_sessions_oldest_seconds{reason=ExecutorEnded} 10800",
		"dev_env_reaped_sessions_retained{reason=RetentionPending} 1",
		"dev_env_reaped_sessions_oldest_seconds{reason=RetentionPending} 7200",
		"dev_env_reaped_sessions_retained{reason=Other} 1",
		"dev_env_reaped_sessions_oldest_seconds{reason=Other} 60",
		"dev_env_reaped_sessions_retained{reason=DeleteNeedsRescue} 0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
}

func TestRescueNotificationMetricKeepsRetryClockOutOfIdentity(t *testing.T) {
	s, owner, config, now := notificationFixture(t)
	collect := func(s, owner *v1alpha1.AgentSession, at time.Time) (string, string) {
		t.Helper()
		c := &SessionCollector{Reader: fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, owner).Build(), Namespace: s.Namespace, RescueIncidentOwners: config, Now: func() time.Time { return at }}
		var notification, attempt string
		for _, line := range gather(t, c) {
			if strings.HasPrefix(line, MetricRescueNotification+"{") {
				notification = line
			}
			if strings.HasPrefix(line, MetricRescueLatestAttempt+"{") {
				attempt = line
			}
		}
		return notification, attempt
	}
	first, attempt := collect(s, owner, now)
	if !strings.Contains(first, "owner_uid="+string(owner.UID)) || attempt == "" {
		t.Fatal("qualified owner or latest-attempt metric absent", first, attempt)
	}
	retry := s.DeepCopy()
	retry.Status.Rescue.At = &metav1.Time{Time: now.Add(time.Minute)}
	retry.Status.Rescue.Stamp = "later-retry"
	retry.Status.Rescue.Message = "different diagnostic retry text"
	next, nextAttempt := collect(retry, owner, now.Add(time.Minute))
	if first != next || attempt == nextAttempt {
		t.Fatal("retry changed incident identity or lost latest attempt clock")
	}
	unknown, _ := collect(retry, owner, now.Add(4*time.Minute))
	if !strings.Contains(unknown, "owner_uid=,") || unknown == first {
		t.Fatal("stale native owner still lowered notification urgency", unknown)
	}
}
