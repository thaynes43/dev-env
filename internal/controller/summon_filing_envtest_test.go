package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Q-1's one vocabulary: every closed-set word goes through the actual
// blocked patch and the installed CRD, unchanged. No consumer parses Message.
func TestSummonFilingBlockedVocabularyOnTheCRD(t *testing.T) {
	for _, word := range []v1alpha1.SummonFilingReason{
		summonoutcome.FilingPolicyGone, summonoutcome.FilingPolicyNoEscalations, summonoutcome.FilingHourFull, summonoutcome.FilingQueueFull,
		summonoutcome.FilingRemoteOff, summonoutcome.FilingLedgerUnavailable, summonoutcome.FilingNoFreeName, summonoutcome.FilingEntryFull,
	} {
		t.Run(string(word), func(t *testing.T) {
			s := newSession(t, func(s *v1alpha1.AgentSession) {
				s.Spec.Caller = callerA
				s.Spec.Parent = callerParent(callerA)
				s.Spec.Profile = "ops"
				s.Spec.Lane = v1alpha1.LaneRemediation
				s.Spec.Summon = remediationSummon()
			})
			at := metav1.NewTime(summonT0)
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, At: &at}
			s.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: at, Source: "report", Reason: "escalate", Released: true}, Escalation: &v1alpha1.SummonEscalationStatus{State: v1alpha1.SummonEscalationPending, Name: "esc-" + s.Name}}
			if err := k8s.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{Client: k8s, APIReader: k8s, Now: func() time.Time { return summonT0 }}
			s = session(t, s.Name)
			if err := r.blockSummonFiling(context.Background(), s, word); err != nil {
				t.Fatal(err)
			}
			stored := session(t, s.Name)
			c := meta.FindStatusCondition(stored.Status.Conditions, summonoutcome.ConditionEscalationPending)
			if c == nil || c.Reason != "Blocked" || c.Status != metav1.ConditionTrue || c.Message != string(word) || len(stored.Status.Summon.Pages) != 1 || stored.Status.Summon.Pages[0].Event != v1alpha1.PageEscalationPending {
				t.Fatalf("blocked patch=%+v", stored.Status)
			}
			// Finish through the production patch too: chargedAt, then Filed.
			base := stored.DeepCopy()
			stored.Status.Summon.Escalation.ChargedAt = &at
			if err := r.patchSummonFiling(context.Background(), base, stored); err != nil {
				t.Fatal(err)
			}
			child, err := summonFilingChild(stored, stored.Status.Summon.Escalation.Name, "0123456789abcdef")
			if err != nil {
				t.Fatal(err)
			}
			if err := k8s.Create(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			if err := r.finishSummonFiling(context.Background(), stored, child); err != nil {
				t.Fatal(err)
			}
			got := session(t, s.Name)
			if got.Status.Summon.Escalation.State != v1alpha1.SummonEscalationFiled || got.Status.Summon.Escalation.UID != string(child.UID) || meta.FindStatusCondition(got.Status.Conditions, summonoutcome.ConditionEscalationPending).Status != metav1.ConditionFalse {
				t.Fatal("Filed patch did not survive")
			}
		})
	}
}

func TestSummonFilingRefusesUnknownBlockedWord(t *testing.T) {
	for _, word := range []v1alpha1.SummonFilingReason{"", "caller-supplied-text", summonoutcome.FilingNotFiled} {
		w := newFilingWorld(t)
		before := w.get(filingRem)
		if err := w.r.blockSummonFiling(context.Background(), before, word); err == nil {
			t.Fatalf("accepted blocked word %q", word)
		}
		if w.get(filingRem).ResourceVersion != before.ResourceVersion {
			t.Fatal("unknown word wrote status")
		}
	}
}
