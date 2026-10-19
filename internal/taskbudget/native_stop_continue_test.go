package taskbudget

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Synthetic independent inspector/stop validator, with production ledger/CAS.
// Actual kernel/native stop proof is covered by the owned-root vertical suite.
type ownerStopFixture struct{ stopping, confirmed bool }

func (f *ownerStopFixture) InspectNativeAdmission(context.Context, *Ledger, Binding) error {
	return nil
}
func (f *ownerStopFixture) InspectNativeContinuation(context.Context, *Ledger, Binding) error {
	if f.stopping {
		return ErrUnavailable
	}
	return nil
}
func (f *ownerStopFixture) AuthorizeNativeContinue(_ context.Context, l *Ledger, _ Binding) error {
	if !f.confirmed || !l.stopped() {
		return ErrDenied
	}
	return nil
}
func (f *ownerStopFixture) Validate(_ context.Context, l *Ledger, e apiv1.TaskBudgetEvent) error {
	if !f.confirmed || e.Kind != "worker-stop" || e.Evidence.Kind != "owned-root-stop" || e.Evidence.Reference != l.Binding.PodUID {
		return ErrDenied
	}
	return nil
}
func (*ownerStopFixture) ValidateOwnerDecision(context.Context, *Ledger, apiv1.TaskBudgetExtension, string) error {
	return ErrDenied
}

func TestOwnerStopObservationThenIndependentReceiptAndOriginalBudgetContinue(t *testing.T) {
	s, m, _, b := setup(t, 10*time.Minute, 10*time.Minute, time.Minute)
	proof := &ownerStopFixture{}
	s.NativeInspector = proof
	s.Validator = proof
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveNativeControl(context.Background(), apiv1.NativeControlRequest{Binding: b, ID: "owner-stop", Operation: "host/stop"}, "human"); err != nil {
		t.Fatal(err)
	}
	proof.stopping = true
	observed, err := s.ObserveNative(context.Background(), b)
	if err != nil || observed.Latched || !observed.Status().NativeStopRequested || !observed.NativeAdmitted() {
		t.Fatal("stop intent observation poisoned continuation", observed, err)
	}
	e := apiv1.TaskBudgetEvent{Binding: b, ID: apiv1.OwnedStopEventID(b), Kind: "worker-stop", WorkerID: fmt.Sprintf("host:%s:%d", b.HostID, b.Epoch), Evidence: apiv1.TaskBudgetEvidence{ID: apiv1.OwnedStopEventID(b), Kind: "owned-root-stop", Reference: b.PodUID}}
	if _, err = s.Record(context.Background(), e); err == nil {
		t.Fatal("unknown stop accepted")
	}
	proof.confirmed = true
	stopped, err := s.Record(context.Background(), e)
	if err != nil || !stopped.Latched || stopped.Reason != "NativeRootStopped" || !stopped.Status().StopConfirmed {
		t.Fatal("independent stop not retained", stopped, err)
	}
	next, err := s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "explicit-human-once"}, "human")
	if err != nil || next.Binding.Epoch != 2 || next.Binding.Deadline != b.Deadline || next.NativeStopRequested() || len(next.NativeControlRecords()) != 1 || len(next.Events) != len(stopped.Events) || next.Events[len(next.Events)-1].Request != e || next.Spec != stopped.Spec || next.EffortMilliseconds != stopped.EffortMilliseconds {
		t.Fatal("continuation changed original history/budget", next, err)
	}
	stored, _, _ := m.Read(context.Background(), b.TaskUID)
	if stored.Events[len(stored.Events)-1].Request != e {
		t.Fatal("old stop receipt lost")
	}
}
