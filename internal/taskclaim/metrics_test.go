package taskclaim

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

func gatherRecords(t *testing.T, c prometheus.Collector) string {
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
	return strings.Join(out, "\n")
}

// The counts per project and the busiest source Session sit beside the bounds,
// so a lifetime bound is visible before Create refuses (#223 review).
func TestRecordCollectorCountsPerProjectAndSession(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	claim := func(name, ns string, labels map[string]string) client.Object {
		return &coord.TaskClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}}
	}
	a, b := SessionLabel(coord.SessionIdentity{Name: "a", UID: "a"}), SessionLabel(coord.SessionIdentity{Name: "b", UID: "b"})
	objs := []client.Object{
		claim("c1", "coord", map[string]string{ProjectLabel: "sample", SourceLabel: a}),
		claim("c2", "coord", map[string]string{ProjectLabel: "sample", SourceLabel: a}),
		claim("c3", "coord", map[string]string{ProjectLabel: "sample", SourceLabel: b}),
		claim("legacy", "coord", map[string]string{ProjectLabel: "sample"}),
		claim("other", "coord", map[string]string{ProjectLabel: "other", SourceLabel: b}),
		claim("unlabelled", "coord", nil),
		claim("elsewhere", "other-ns", map[string]string{ProjectLabel: "sample", SourceLabel: a}),
	}
	backend := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	limits := Limits{PerSession: 2, PerProject: 5}
	c := &RecordCollector{Reader: backend, Namespace: "coord", Kinds: []RecordKind{{Kind: "TaskClaim", Limits: limits}}}
	want := strings.Join([]string{
		`dev_env_coordination_record_limit{kind=TaskClaim,scope=project} 5`,
		`dev_env_coordination_record_limit{kind=TaskClaim,scope=session} 2`,
		`dev_env_coordination_records_session_max{kind=TaskClaim,project=other} 1`,
		`dev_env_coordination_records_session_max{kind=TaskClaim,project=sample} 2`,
		`dev_env_coordination_records{kind=TaskClaim,project=other} 1`,
		`dev_env_coordination_records{kind=TaskClaim,project=sample} 4`,
	}, "\n")
	if got := gatherRecords(t, c); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	// An unconfirmed count drops the count series but fails nothing else.
	c.Reader = interceptor.NewClient(backend, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return errors.New("unavailable")
	}})
	want = strings.Join([]string{
		`dev_env_coordination_record_limit{kind=TaskClaim,scope=project} 5`,
		`dev_env_coordination_record_limit{kind=TaskClaim,scope=session} 2`,
	}, "\n")
	if got := gatherRecords(t, c); got != want {
		t.Errorf("unconfirmed count: got\n%s\nwant\n%s", got, want)
	}
}
