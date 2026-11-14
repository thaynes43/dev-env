package summonledger

import (
	"maps"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The keep rule over the listed sessions: the dispatch id of every session
// that carries one and is neither admitted nor closed, and FilingID(UID) of
// every session whose filing is Pending. Nothing else is kept.
func TestKeep(t *testing.T) {
	at := metav1.Now()
	closed := &v1alpha1.SummonClosedStatus{At: at, Source: "report", Reason: "escalated"}
	summoned := func(name, id string, st *v1alpha1.SummonStatus) v1alpha1.AgentSession {
		return v1alpha1.AgentSession{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)},
			Spec:       v1alpha1.AgentSessionSpec{Summon: &v1alpha1.SessionSummon{DispatchID: id}},
			Status:     v1alpha1.AgentSessionStatus{Summon: st},
		}
	}
	filing := func(state string) *v1alpha1.SummonEscalationStatus {
		return &v1alpha1.SummonEscalationStatus{State: state, Name: "esc-1"}
	}
	for _, tc := range []struct {
		name string
		sess v1alpha1.AgentSession
		want []string
	}{
		{"unsettled, no status", summoned("a", "id-a", nil), []string{"id-a"}},
		{"unsettled, empty status", summoned("a", "id-a", &v1alpha1.SummonStatus{}), []string{"id-a"}},
		{"admitted", summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at}), nil},
		{"closed", summoned("a", "id-a", &v1alpha1.SummonStatus{Closed: closed}), nil},
		{"admitted and closed", summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed}), nil},
		{"no spec.summon", v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "uid-a"}}, nil},
		{"spec.summon with no dispatch id", summoned("a", "", nil), nil},
		{"Pending filing, closed",
			summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed, Escalation: filing("Pending")}),
			[]string{"filing:uid-a"}},
		{"filing in another state", summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed, Escalation: filing("Filed")}), nil},
		{"filing with no state", summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed, Escalation: filing("")}), nil},
		{"no filing", summoned("a", "id-a", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed}), nil},
		{"unsettled and a Pending filing",
			summoned("a", "id-a", &v1alpha1.SummonStatus{Escalation: filing("Pending")}),
			[]string{"filing:uid-a", "id-a"}},
		{"Pending filing with no spec.summon",
			v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "a", UID: "uid-a"},
				Status: v1alpha1.AgentSessionStatus{Summon: &v1alpha1.SummonStatus{Escalation: filing("Pending")}}},
			[]string{"filing:uid-a"}},
	} {
		got := Keep([]v1alpha1.AgentSession{tc.sess})
		if got == nil {
			t.Errorf("%s: nil set", tc.name)
		}
		if ids := slices.Sorted(maps.Keys(got)); !slices.Equal(ids, tc.want) {
			t.Errorf("%s: kept %v, want %v", tc.name, ids, tc.want)
		}
	}

	// Over a list, each session adds its own ids, and an id two sessions
	// carry is kept while either is unsettled.
	list := []v1alpha1.AgentSession{
		summoned("settled", "id-shared", &v1alpha1.SummonStatus{AdmittedAt: &at}),
		summoned("queued", "id-shared", nil),
		summoned("rem", "id-rem", &v1alpha1.SummonStatus{AdmittedAt: &at, Closed: closed, Escalation: filing("Pending")}),
		summoned("other", "id-other", &v1alpha1.SummonStatus{Closed: closed}),
	}
	if ids := slices.Sorted(maps.Keys(Keep(list))); !slices.Equal(ids, []string{"filing:uid-rem", "id-shared"}) {
		t.Errorf("list: kept %v", ids)
	}
	if got := Keep(nil); got == nil || len(got) != 0 {
		t.Errorf("no sessions: %v", got)
	}
}
