package apiserver

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/controller"
)

// Story 790, proof 5: the session view, what agent-run show prints, carries
// a Queued summoned session's SummonAdmitted condition as the operator wrote
// it, reason and message unchanged.
func TestSessionViewCarriesTheSummonHold(t *testing.T) {
	const msg = "held: quota: the 5-hour window is 81% used, past its line of 80% (reading taken at 2026-11-12T15:59:00Z)"
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "curate-1", Namespace: "dev-agents", CreationTimestamp: metav1.NewTime(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))},
		Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Caller: "cigar-ns.cigar-curator", Lane: v1alpha1.LaneCuration}}
	s.Status.Conditions = []metav1.Condition{{Type: controller.ConditionSummonAdmitted, Status: metav1.ConditionFalse, Reason: controller.SummonReasonQuotaHold, Message: msg}}
	v := view(s, false)
	if len(v.Conditions) != 1 {
		t.Fatalf("conditions %+v", v.Conditions)
	}
	if c := v.Conditions[0]; c.Type != "SummonAdmitted" || c.Status != "False" || c.Reason != "QuotaHold" || c.Message != msg {
		t.Fatalf("condition %+v", c)
	}
}
