package usagetotals

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

var (
	usageStart = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	idA        = strings.Repeat("a", 64)
	idB        = strings.Repeat("b", 64)
)

func TestBoundLabel(t *testing.T) {
	if got := boundLabel(strings.Repeat("m", 63)); got != strings.Repeat("m", 63) {
		t.Fatalf("63-character name = %q", got)
	}
	for _, bad := range []string{"", strings.Repeat("m", 64), "org/model", "-bad", "has space"} {
		if got := boundLabel(bad); got != labelOther {
			t.Fatalf("boundLabel(%q) = %q", bad, got)
		}
	}
	if got := boundLabel("claude-opus-5-5"); got != "claude-opus-5-5" {
		t.Fatalf("model = %q", got)
	}
}

func TestMarkHonouredAcrossRestart(t *testing.T) {
	c := fakeClient(t, finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5"))
	first := newReconciler(t, c)
	if err := reconcile(first, "s"); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(first, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, first.Counters, "claude", "claude-opus-5-5"); got != 1 {
		t.Fatalf("first process sessions = %v", got)
	}
	if got := annotation(t, c, "s"); got != "observation/"+idA {
		t.Fatalf("mark = %q", got)
	}

	restarted := newReconciler(t, c)
	if err := reconcile(restarted, "s"); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(restarted, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, restarted.Counters, "claude", "claude-opus-5-5"); got != 0 {
		t.Fatalf("restarted sessions = %v, want 0", got)
	}
	if got := sessionValue(t, first.Counters, "claude", "claude-opus-5-5"); got != 1 {
		t.Fatalf("first process changed to %v", got)
	}
}

func TestChangedObservationNotCountedTwice(t *testing.T) {
	c := fakeClient(t, finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5"))
	r := newReconciler(t, c)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	setObservationID(t, c, "s", idB)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 1 {
		t.Fatalf("same process sessions = %v", got)
	}
	if got := annotation(t, c, "s"); got != "observation/"+idA {
		t.Fatalf("mark changed to %q", got)
	}

	restarted := newReconciler(t, c)
	if err := reconcile(restarted, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, restarted.Counters, "claude", "claude-opus-5-5"); got != 0 {
		t.Fatalf("restarted sessions = %v, want 0", got)
	}
}

func TestAddPrecedesMark(t *testing.T) {
	session := finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5")
	r := &Reconciler{Counters: NewCounters(), SessionNamespace: "dev-agents", Elected: func() bool { return true }}
	var atPatch float64
	var patched bool
	r.Client = fakeClientIntercept(t, session, interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			atPatch = counterValue(t, r.Counters.Sessions.WithLabelValues("claude", "claude-opus-5-5"))
			patched = true
			return cl.Patch(ctx, obj, patch, opts...)
		},
	})
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	if !patched || atPatch != 1 {
		t.Fatalf("patch saw sessions %v, patched %v", atPatch, patched)
	}
	if got := annotation(t, r.Client, "s"); got != "observation/"+idA {
		t.Fatalf("mark = %q", got)
	}
}

func TestNonLeaderDoesNotCount(t *testing.T) {
	if !(&Reconciler{}).NeedLeaderElection() {
		t.Fatal("task usage totals must run on the leader only")
	}
	for _, tc := range []struct {
		name    string
		elected func() bool
	}{
		{name: "not elected", elected: func() bool { return false }},
		{name: "election off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := fakeClient(t, finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5"))
			r := newReconciler(t, c)
			r.Elected = tc.elected
			if err := reconcile(r, "s"); err != nil {
				t.Fatal(err)
			}
			if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 0 {
				t.Fatalf("sessions = %v", got)
			}
			if got := annotation(t, c, "s"); got != "" {
				t.Fatalf("mark = %q", got)
			}
		})
	}
}

func TestNoUsageCountsSessionsAndSecondsOnly(t *testing.T) {
	s := finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5")
	unknown := v1alpha1.TokenObservationStatus{State: kindUnknown}
	s.Status.UsageObservation.InputTokens = unknown
	s.Status.UsageObservation.Cost = v1alpha1.CostObservationStatus{State: kindUnknown, Kind: kindUnknown}
	c := fakeClient(t, s)
	r := newReconciler(t, c)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	got := gatherCounters(t, r.Counters)
	if got[series(MetricSessions, "agent", "claude", "model", "claude-opus-5-5")] != 1 {
		t.Fatalf("sessions = %v", got)
	}
	if got[series(MetricSeconds, "agent", "claude", "model", "claude-opus-5-5")] != 125 {
		t.Fatalf("seconds = %v", got)
	}
	for name := range got {
		if strings.HasPrefix(name, MetricTokens) || strings.HasPrefix(name, MetricCostUSD) {
			t.Fatalf("usage series %s in %v", name, got)
		}
	}
}

func TestCursorCountsSessionsAndSecondsOnly(t *testing.T) {
	s := baseSession("s", "uid-1", "cursor", "dev-env", "grok-4.7")
	s.Generation = 9
	s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded"}
	c := fakeClient(t, s)
	r := newReconciler(t, c)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	got := gatherCounters(t, r.Counters)
	if got[series(MetricSessions, "agent", "cursor", "model", "grok-4.7")] != 1 {
		t.Fatalf("sessions = %v", got)
	}
	if got[series(MetricSeconds, "agent", "cursor", "model", "grok-4.7")] != 125 {
		t.Fatalf("seconds = %v", got)
	}
	for name := range got {
		if strings.HasPrefix(name, MetricTokens) || strings.HasPrefix(name, MetricCostUSD) {
			t.Fatalf("cursor usage series %s", name)
		}
	}
	if gotMark := annotation(t, c, "s"); gotMark != "generation/9" {
		t.Fatalf("mark = %q", gotMark)
	}

	t.Run("reported tokens are ignored", func(t *testing.T) {
		withObs := baseSession("with-obs", "uid-2", "cursor", "dev-env", "grok-4.7")
		n := int64(50)
		withObs.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded}
		withObs.Status.UsageObservation = finalObservation(idA, "grok-4.7")
		withObs.Status.UsageObservation.InputTokens = v1alpha1.TokenObservationStatus{State: stateReported, Amount: &n}
		c := fakeClient(t, withObs)
		r := newReconciler(t, c)
		if err := reconcile(r, "with-obs"); err != nil {
			t.Fatal(err)
		}
		got := gatherCounters(t, r.Counters)
		if got[series(MetricSessions, "agent", "cursor", "model", "grok-4.7")] != 1 {
			t.Fatalf("sessions = %v", got)
		}
		for name := range got {
			if strings.HasPrefix(name, MetricTokens) || strings.HasPrefix(name, MetricCostUSD) {
				t.Fatalf("cursor usage series %s", name)
			}
		}
		if gotMark := annotation(t, c, "with-obs"); gotMark != "observation/"+idA {
			t.Fatalf("mark = %q", gotMark)
		}
	})

	t.Run("task result required", func(t *testing.T) {
		waiting := baseSession("waiting", "uid-3", "cursor", "dev-env", "grok-4.7")
		waiting.Status.UsageObservation = finalObservation(idA, "grok-4.7")
		c := fakeClient(t, waiting)
		r := newReconciler(t, c)
		if err := reconcile(r, "waiting"); err != nil {
			t.Fatal(err)
		}
		if got := sessionValue(t, r.Counters, "cursor", "grok-4.7"); got != 0 {
			t.Fatalf("sessions = %v", got)
		}
		if got := annotation(t, c, "waiting"); got != "" {
			t.Fatalf("mark = %q", got)
		}
	})
}

func TestLabelValuesAreBounded(t *testing.T) {
	longRepo := strings.Repeat("r", 64)
	served := finalSession("served", "uid-1", idA, longRepo, "claude-opus-5-5")
	n := int64(10)
	cache := int64(4)
	top := int64(999)
	served.Status.UsageObservation.InputTokens = v1alpha1.TokenObservationStatus{State: stateReported, Amount: &top}
	served.Status.UsageObservation.ActualModelState = "ProviderReported"
	served.Status.UsageObservation.ServedModels = []v1alpha1.ModelUsageStatus{{
		Model:                    "org/model",
		InputTokens:              v1alpha1.TokenObservationStatus{State: stateReported, Amount: &n},
		OutputTokens:             v1alpha1.TokenObservationStatus{State: kindUnknown},
		CacheReadInputTokens:     v1alpha1.TokenObservationStatus{State: stateReported, Amount: &cache},
		CacheCreationInputTokens: v1alpha1.TokenObservationStatus{State: kindUnknown},
		Cost:                     v1alpha1.CostObservationStatus{State: stateReported, Amount: "1.25", Unit: "USD", Kind: kindEstimate},
	}}

	plain := finalSession("plain", "uid-2", idB, "dev-env", "has space")
	out := int64(3)
	plain.Status.UsageObservation.OutputTokens = v1alpha1.TokenObservationStatus{State: stateReported, Amount: &out}
	plain.Status.UsageObservation.Cost = v1alpha1.CostObservationStatus{State: kindUnknown, Kind: kindUnknown}

	c := fakeClient(t, served, plain)
	r := newReconciler(t, c)
	if err := reconcile(r, "served"); err != nil {
		t.Fatal(err)
	}
	if err := reconcile(r, "plain"); err != nil {
		t.Fatal(err)
	}
	got := gatherCounters(t, r.Counters)
	want := map[string]float64{
		series(MetricTokens, "agent", "claude", "cost_kind", kindEstimate, "model", labelOther, "repository", labelOther, "token", tokenInput):     10,
		series(MetricTokens, "agent", "claude", "cost_kind", kindEstimate, "model", labelOther, "repository", labelOther, "token", tokenCacheRead): 4,
		series(MetricCostUSD, "agent", "claude", "cost_kind", kindEstimate, "model", labelOther, "repository", labelOther):                         1.25,
		series(MetricSessions, "agent", "claude", "model", "claude-opus-5-5"):                                                                      1,
		series(MetricSeconds, "agent", "claude", "model", "claude-opus-5-5"):                                                                       125,
		series(MetricTokens, "agent", "claude", "cost_kind", kindUnknown, "model", labelOther, "repository", "dev-env", "token", tokenOutput):      3,
		series(MetricSessions, "agent", "claude", "model", labelOther):                                                                             1,
		series(MetricSeconds, "agent", "claude", "model", labelOther):                                                                              125,
	}
	if len(got) != len(want) {
		t.Fatalf("series = %#v", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %v, want %v in %#v", key, got[key], value, got)
		}
	}
}

func TestRefusedMarkDoesNotAddAgain(t *testing.T) {
	session := finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5")
	var seen []float64
	r := &Reconciler{Counters: NewCounters(), SessionNamespace: "dev-agents", Elected: func() bool { return true }}
	c := fakeClientIntercept(t, session, interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			seen = append(seen, counterValue(t, r.Counters.Sessions.WithLabelValues("claude", "claude-opus-5-5")))
			return errors.New("annotation refused")
		},
	})
	r.Client = c
	if err := reconcile(r, "s"); err == nil {
		t.Fatal("expected the mark write to fail")
	}
	if err := reconcile(r, "s"); err == nil {
		t.Fatal("expected the mark write to fail again")
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 1 {
		t.Fatalf("sessions at each mark write = %v", seen)
	}
	if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 1 {
		t.Fatalf("sessions = %v", got)
	}
	if got := annotation(t, c, "s"); got != "" {
		t.Fatalf("mark = %q", got)
	}
}

func TestPartialObservationWaitsForFinal(t *testing.T) {
	s := finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5")
	s.Status.UsageObservation.ResultState = "Partial"
	c := fakeClient(t, s)
	r := newReconciler(t, c)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 0 {
		t.Fatalf("partial sessions = %v", got)
	}
	setResultState(t, c, "s", stateFinal)
	if err := reconcile(r, "s"); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 1 {
		t.Fatalf("final sessions = %v", got)
	}
}

func TestOtherNamespaceDoesNotCount(t *testing.T) {
	s := finalSession("s", "uid-1", idA, "dev-env", "claude-opus-5-5")
	s.Namespace = "elsewhere"
	c := fakeClient(t, s)
	r := newReconciler(t, c)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "elsewhere", Name: "s"}}); err != nil {
		t.Fatal(err)
	}
	if got := sessionValue(t, r.Counters, "claude", "claude-opus-5-5"); got != 0 {
		t.Fatalf("sessions = %v", got)
	}
}

func newReconciler(t *testing.T, c client.Client) *Reconciler {
	t.Helper()
	return &Reconciler{Client: c, Counters: NewCounters(), SessionNamespace: "dev-agents", Elected: func() bool { return true }}
}

func reconcile(r *Reconciler, name string) error {
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "dev-agents", Name: name}})
	return err
}

func fakeClient(t *testing.T, sessions ...*v1alpha1.AgentSession) client.Client {
	t.Helper()
	return fakeClientIntercept(t, nil, interceptor.Funcs{}, sessions...)
}

func fakeClientIntercept(t *testing.T, extra *v1alpha1.AgentSession, funcs interceptor.Funcs, sessions ...*v1alpha1.AgentSession) client.Client {
	t.Helper()
	objs := make([]client.Object, 0, len(sessions)+1)
	for _, s := range sessions {
		objs = append(objs, s)
	}
	if extra != nil {
		objs = append(objs, extra)
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}).WithObjects(objs...).WithInterceptorFuncs(funcs).Build()
}

func baseSession(name, uid, agent, repo, model string) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "dev-agents",
			UID:               types.UID(uid),
			CreationTimestamp: metav1.NewTime(usageStart),
		},
		Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentKind(agent), Repo: repo, Model: model},
		Status: v1alpha1.AgentSessionStatus{
			Agent: &v1alpha1.AgentStatus{Task: &v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(usageStart.Add(125 * time.Second))}},
		},
	}
}

func finalSession(name, uid, id, repo, model string) *v1alpha1.AgentSession {
	s := baseSession(name, uid, "claude", repo, model)
	s.Status.UsageObservation = finalObservation(id, model)
	return s
}

func finalObservation(id, model string) *v1alpha1.UsageObservationStatus {
	unknown := v1alpha1.TokenObservationStatus{State: kindUnknown}
	return &v1alpha1.UsageObservationStatus{
		Version:                  1,
		ID:                       id,
		Source:                   v1alpha1.UsageSourceStatus{Provider: "claude", RequestedModel: model},
		ResultState:              stateFinal,
		InputTokens:              unknown,
		OutputTokens:             unknown,
		CacheReadInputTokens:     unknown,
		CacheCreationInputTokens: unknown,
		Cost:                     v1alpha1.CostObservationStatus{State: kindUnknown, Kind: kindUnknown},
	}
}

func annotation(t *testing.T, c client.Client, name string) string {
	t.Helper()
	var got v1alpha1.AgentSession
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	return got.Annotations[AnnotationCounted]
}

func setObservationID(t *testing.T, c client.Client, name, id string) {
	t.Helper()
	var got v1alpha1.AgentSession
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Status.UsageObservation.ID = id
	if err := c.Status().Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
}

func setResultState(t *testing.T, c client.Client, name, state string) {
	t.Helper()
	var got v1alpha1.AgentSession
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "dev-agents", Name: name}, &got); err != nil {
		t.Fatal(err)
	}
	got.Status.UsageObservation.ResultState = state
	if err := c.Status().Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}
}

func sessionValue(t *testing.T, c *Counters, agent, model string) float64 {
	t.Helper()
	return counterValue(t, c.Sessions.WithLabelValues(agent, model))
}

func counterValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()
	var m dto.Metric
	if err := metric.Write(&m); err != nil {
		t.Fatal(err)
	}
	return m.GetCounter().GetValue()
}

func series(name string, kv ...string) string {
	if len(kv)%2 != 0 {
		panic("series: odd labels")
	}
	parts := make([]string, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		parts = append(parts, kv[i]+"="+kv[i+1])
	}
	sort.Strings(parts)
	return name + "{" + strings.Join(parts, ",") + "}"
}

func gatherCounters(t *testing.T, c *Counters) map[string]float64 {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	if err := c.Register(reg); err != nil {
		t.Fatal(err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			out[metricKey(mf.GetName(), m)] = m.GetCounter().GetValue()
		}
	}
	return out
}

func metricKey(name string, m *dto.Metric) string {
	parts := make([]string, 0, len(m.GetLabel()))
	for _, l := range m.GetLabel() {
		parts = append(parts, l.GetName()+"="+l.GetValue())
	}
	sort.Strings(parts)
	return name + "{" + strings.Join(parts, ",") + "}"
}
