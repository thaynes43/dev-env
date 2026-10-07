package controller

import (
	"sort"
	"strconv"
	"strings"
	"testing"

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
		"dev_env_session_rescue_failed{reason=WorktreeRefused,session=b} 1",
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
