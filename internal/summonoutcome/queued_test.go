package summonoutcome

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The one definition of Queued (DESIGN-064 5.9 "States"), shared by the
// leader's admission and the API's queue counts (OP778): admitted means
// admittedAt or an open grant; closed means status.summon.closed or a
// terminal outcome; a suspended or deleting session is not Queued.
func TestQueued(t *testing.T) {
	at := metav1.Now()
	queued := func() *v1alpha1.AgentSession {
		return &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Caller: "c", Summon: &v1alpha1.SessionSummon{DispatchID: "d"}}}
	}
	cases := []struct {
		name    string
		edit    func(*v1alpha1.AgentSession)
		granted bool
		want    bool
	}{
		{"created", func(*v1alpha1.AgentSession) {}, false, true},
		{"held", func(s *v1alpha1.AgentSession) {
			s.Status.Summon = &v1alpha1.SummonStatus{}
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomePending}
		}, false, true},
		{"granted, admittedAt not yet written", func(*v1alpha1.AgentSession) {}, true, false},
		{"admitted", func(s *v1alpha1.AgentSession) { s.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at} }, false, false},
		{"closed", func(s *v1alpha1.AgentSession) {
			s.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: Unreported, Reason: ReasonNotCharged, Released: true}}
		}, false, false},
		{"terminal outcome", func(s *v1alpha1.AgentSession) {
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, At: &at}
		}, false, false},
		{"suspended", func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, false, false},
		{"deleting", func(s *v1alpha1.AgentSession) { s.DeletionTimestamp = &at }, false, false},
		{"not summoned", func(s *v1alpha1.AgentSession) { s.Spec.Summon = nil }, false, false},
	}
	for _, tc := range cases {
		s := queued()
		tc.edit(s)
		if got := Queued(s, tc.granted); got != tc.want {
			t.Errorf("%s: Queued %v, want %v", tc.name, got, tc.want)
		}
	}
	if Queued(nil, false) {
		t.Error("nil is Queued")
	}
}
