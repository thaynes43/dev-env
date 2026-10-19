package taskbudget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type controlInspector struct {
	err    error
	during func()
}

func TestNativeConsumeNeverPermitsHistoryBoundCampaign(t *testing.T) {
	s, m, now, b := setup(t, 10*time.Minute, 10*time.Minute, time.Minute)
	s.NativeInspector = controlInspector{}
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	r := apiv1.NativeControlRequest{Binding: b, ID: "last-request", Operation: "turn/start", ThreadID: "thread-a", Text: "owner"}
	l, rv, _ := m.Read(context.Background(), b.TaskUID)
	for i := 0; i < MaxEvents-2; i++ {
		l.Events = append(l.Events, Event{At: *now, Request: apiv1.TaskBudgetEvent{Binding: b, ID: fmt.Sprintf("activity-%d", i), Kind: "activity"}})
	}
	if err := m.Update(context.Background(), l, rv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveNativeControl(context.Background(), r, "owner"); err == nil {
		t.Fatal("reservation filled final history slots")
	}
	// Simulate an intent already present when an unrelated event fills space.
	l, rv, _ = m.Read(context.Background(), b.TaskUID)
	l.Events = append(l.Events, Event{At: *now, Request: apiv1.TaskBudgetEvent{Binding: b, ID: nativeAuditID(nativeOwnerIntent, r.ID), Kind: "activity", AttemptID: r.Operation, WorkerID: "owner", Evidence: apiv1.TaskBudgetEvidence{ID: r.ID, Kind: nativeOwnerIntent, Reference: apiv1.NativeControlDigest(r)}}})
	if err := m.Update(context.Background(), l, rv); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeNativeControl(context.Background(), r); err == nil {
		t.Fatal("positive permit at HistoryBound")
	}
}

func TestNativeOwnerIntentRealKubeStoreAndLegacyEventEnvelope(t *testing.T) {
	k, s, l := kubeFixture(t)
	s.NativeInspector = controlInspector{}
	s.Validator = receiptValidator{}
	if _, err := s.Admit(context.Background(), l.Binding); err != nil {
		t.Fatal(err)
	}
	r := apiv1.NativeControlRequest{Binding: l.Binding, ID: strings.Repeat("a", 128), Operation: "turn/start", ThreadID: "thread-a", Text: "owner-only"}
	if _, err := s.ReserveNativeControl(context.Background(), r, "system:serviceaccount:dev-env-system:dev-env-human"); err != nil {
		t.Fatal("reserve in KubeStore", err)
	}
	if _, err := s.ConsumeNativeControl(context.Background(), r); err != nil {
		t.Fatal("consume in KubeStore", err)
	}
	stored, _, err := k.Read(context.Background(), l.Binding.TaskUID)
	if err != nil || len(stored.NativeControlRecords()) != 1 || stored.NativeControlRecords()[0].ConsumedAt == nil {
		t.Fatal("intent did not survive real serializer", stored, err)
	}
	for _, e := range stored.Events {
		if e.Request.Kind != "activity" || !validEvent(e.Request) {
			t.Fatal("not legacy-valid inert envelope")
		}
	}
	// An agent cannot forge these internal activity receipts through Record:
	// externally submitted activity only observes; it never appends fields.
	forged := stored.Events[0].Request
	forged.ID = "forged-intent"
	forged.Evidence.ID = "another"
	if _, err = s.Record(context.Background(), forged); err != nil {
		t.Fatal(err)
	}
	again, _, _ := k.Read(context.Background(), l.Binding.TaskUID)
	if len(again.Events) != len(stored.Events) {
		t.Fatal("agent forged owner intent")
	}
}

func (i controlInspector) InspectNativeAdmission(context.Context, *Ledger, Binding) error {
	return i.err
}
func (i controlInspector) InspectNativeContinuation(context.Context, *Ledger, Binding) error {
	if i.during != nil {
		i.during()
	}
	return i.err
}

func TestNativeOwnerReservationRetainsLostIntentAndRefusesUnknownAuthority(t *testing.T) {
	for _, scenario := range []string{"once", "lost-write", "unknown-stop", "expired", "cross-deadline", "unknown-native", "stale-binding"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, now, b := setup(t, 10*time.Minute, 10*time.Minute, time.Minute)
			s.NativeInspector = controlInspector{}
			if _, err := s.Admit(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			req := apiv1.NativeControlRequest{Binding: b, ID: "owner-once", Operation: "turn/start", ThreadID: "thread-a", Text: "not stored in ledger"}
			switch scenario {
			case "lost-write":
				m.unknownOnce = true
			case "unknown-stop":
				l, rv, _ := m.Read(context.Background(), b.TaskUID)
				l.Latched = true
				l.Reason = "NativeAuthorityUnavailable"
				_ = m.Update(context.Background(), l, rv)
			case "expired":
				*now = b.Deadline
			case "cross-deadline":
				s.NativeInspector = controlInspector{during: func() { *now = b.Deadline }}
			case "unknown-native":
				s.NativeInspector = controlInspector{err: ErrUnavailable}
			case "stale-binding":
				req.Binding.Epoch++
			}
			got, err := s.ReserveNativeControl(context.Background(), req, "human-owner")
			if scenario == "once" {
				if err != nil || len(got.NativeControlRecords()) != 1 || got.NativeControlRecords()[0].Owner != "human-owner" {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("unsafe reservation accepted")
			}
			if scenario == "once" || scenario == "lost-write" {
				if _, err = s.ReserveNativeControl(context.Background(), req, "human-owner"); !errors.Is(err, ErrConflict) {
					t.Fatal("consumed request replay", err)
				}
			}
			l, _, _ := m.Read(context.Background(), b.TaskUID)
			if scenario != "once" && scenario != "lost-write" && len(l.NativeControlRecords()) != 0 {
				t.Fatal("refusal consumed command")
			}
		})
	}
}

func TestNativeOwnerStopDoesNotClaimStopOrFreeBudget(t *testing.T) {
	s, m, _, b := setup(t, 10*time.Minute, 10*time.Minute, time.Minute)
	s.NativeInspector = controlInspector{}
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	l, err := s.ReserveNativeControl(context.Background(), apiv1.NativeControlRequest{Binding: b, ID: "stop-once", Operation: "host/stop"}, "owner")
	if err != nil || !l.Status().NativeStopRequested || l.Latched || l.Status().StopConfirmed || !l.NativeAdmitted() {
		t.Fatal("stop intent became stop proof", l, err)
	}
	if _, err = s.Admit(context.Background(), b); !errors.Is(err, ErrDenied) {
		t.Fatal("admitted while stopping", err)
	}
	if _, err = s.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: "queued-worker", Kind: "worker-start", WorkerID: "queued-child"}); err == nil {
		t.Fatal("queued child admitted while stopping")
	}
	if _, err = s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "self-resume"}, "owner"); err == nil {
		t.Fatal("continued unconfirmed stop")
	}
	stored, _, _ := m.Read(context.Background(), b.TaskUID)
	if len(stored.Workers) != 1 || !stored.Workers[0].Active {
		t.Fatal("freed original host budget")
	}
}

func TestNativeConsumeCASExactPayloadOnceAndUnknownWrite(t *testing.T) {
	for _, scenario := range []string{"once", "unreserved", "payload", "epoch", "lost-write", "stop-race", "deadline"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, now, b := setup(t, 10*time.Minute, 10*time.Minute, time.Minute)
			s.NativeInspector = controlInspector{}
			if _, err := s.Admit(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			req := apiv1.NativeControlRequest{Binding: b, ID: "owner-only", Operation: "turn/start", ThreadID: "thread-a", Text: "owner request"}
			if scenario != "unreserved" {
				if _, err := s.ReserveNativeControl(context.Background(), req, "human-owner"); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "payload":
				req.Text = "model replacement"
			case "epoch":
				req.Binding.Epoch++
			case "lost-write":
				m.unknownOnce = true
			case "stop-race":
				_, err := s.ReserveNativeControl(context.Background(), apiv1.NativeControlRequest{Binding: b, ID: "stop-owner", Operation: "host/stop"}, "human-owner")
				if err != nil {
					t.Fatal(err)
				}
			case "deadline":
				*now = b.Deadline
			}
			_, err := s.ConsumeNativeControl(context.Background(), req)
			if (err == nil) != (scenario == "once") {
				t.Fatal(scenario, err)
			}
			if scenario == "once" || scenario == "lost-write" {
				if _, err = s.ConsumeNativeControl(context.Background(), req); err == nil {
					t.Fatal("consumption replayed")
				}
				l, _, _ := m.Read(context.Background(), b.TaskUID)
				if l.NativeControlRecords()[0].ConsumedAt == nil {
					t.Fatal("lost consumed record")
				}
			}
		})
	}
}
