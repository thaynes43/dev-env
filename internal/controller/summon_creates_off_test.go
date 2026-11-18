package controller

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Ordinary sessions do not enter filing cleanup or the summoned reap hold,
// regardless of the create switch. Their existing lifecycle remains in charge.
func TestSummonFilingAndReapIgnoreSessionWithoutCaller(t *testing.T) {
	s := taskSession()
	s.Name, s.UID = "ordinary-no-caller", "uid-ordinary-no-caller"
	s.Spec.Caller, s.Spec.Summon = "", nil
	s.Status.Summon = nil
	s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeFailed}
	s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: "session/worker"}
	w := newSummonWorld(t, s)
	w.r.SummonCreateKinds = nil
	w.now = summonT0.Add(25 * time.Hour)
	before := w.get(s.Name)
	if err := w.r.SummonAdmissionPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if changed, err := w.r.reconcileSummonReap(context.Background(), w.get(s.Name)); err != nil || changed {
		t.Fatalf("caller-empty session entered summoned reap: changed=%v err=%v", changed, err)
	}
	if !reflect.DeepEqual(before, w.get(s.Name)) {
		t.Fatal("filing/reap changed the caller-empty session")
	}
}
