package taskbudget

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type continueInspector struct {
	err    error
	during func()
}

func (*continueInspector) InspectNativeAdmission(context.Context, *Ledger, Binding) error { return nil }
func (i *continueInspector) AuthorizeNativeContinue(context.Context, *Ledger, Binding) error {
	if i.during != nil {
		i.during()
	}
	return i.err
}

func stoppedForContinue(t *testing.T) (*Service, *memoryStore, *time.Time, Binding) {
	t.Helper()
	s, m, now, b := setup(t, 45*time.Minute, time.Hour, 10*time.Minute)
	l, rv, err := m.Read(context.Background(), b.TaskUID)
	if err != nil {
		t.Fatal(err)
	}
	l.Latched = true
	l.Reason = "NativeRootStopped"
	l.EffortMilliseconds = 1234
	l.Workers = []Worker{{ID: "host:host-a:1", StoppedAt: *now, StopEvidence: b.PodUID}}
	l.Events = []Event{{Request: failureEvent(b, "failed-once", "attempt", "blocker"), At: *now}}
	if err = m.Update(context.Background(), l, rv); err != nil {
		t.Fatal(err)
	}
	s.NativeInspector = &continueInspector{}
	return s, m, now, b
}
func TestContinuePreservesAllPriorBudgetAndStopAccounting(t *testing.T) {
	s, m, now, b := stoppedForContinue(t)
	prior, _, _ := m.Read(context.Background(), b.TaskUID)
	*now = now.Add(time.Second)
	got, err := s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "owner-once"}, "trusted-owner")
	if err != nil {
		t.Fatal(err)
	}
	if got.Binding.Epoch != 2 || !got.Binding.Deadline.Equal(b.Deadline) || got.Binding.PodUID != b.PodUID || got.Spec != prior.Spec || got.EffortMilliseconds != prior.EffortMilliseconds || got.EffortCeilingMilliseconds != prior.EffortCeilingMilliseconds || !got.NextCheckpoint.Equal(prior.NextCheckpoint) || !got.LastProgress.Equal(prior.LastProgress) || !got.StallAnchor.Equal(prior.StallAnchor) || !reflect.DeepEqual(got.Events, prior.Events) || !reflect.DeepEqual(got.Workers, prior.Workers) || len(got.Continuations) != 1 || got.Continuations[0].Prior != b || got.Continuations[0].Owner != "trusted-owner" {
		t.Fatal("continuation reset prior authority/accounting")
	}
	if _, err = s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: got.Binding, DecisionID: "second"}, "trusted-owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("second continuation", err)
	}
	if _, err = s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "replay"}, "trusted-owner"); err == nil {
		t.Fatal("old binding replay")
	}
}
func TestContinueRefusesExhaustionUnknownStopAndUntrustedAuthority(t *testing.T) {
	for _, scenario := range []string{"owner", "unproven-stop", "active-child", "overall", "effort", "checkpoint", "repeated-failure", "lost-guard", "cross-deadline-in-probe", "unrelated-latch"} {
		t.Run(scenario, func(t *testing.T) {
			s, m, now, b := stoppedForContinue(t)
			l, rv, _ := m.Read(context.Background(), b.TaskUID)
			owner := "trusted-owner"
			switch scenario {
			case "owner":
				owner = ""
			case "unproven-stop":
				l.Workers[0].Active = true
			case "active-child":
				l.Workers = append(l.Workers, Worker{ID: "child", Active: true})
			case "overall":
				*now = b.Deadline
			case "effort":
				l.EffortMilliseconds = l.EffortCeilingMilliseconds
			case "checkpoint":
				*now = l.NextCheckpoint
			case "repeated-failure":
				l.Events = append(l.Events, l.Events[0], l.Events[0])
			case "lost-guard":
				s.NativeInspector = &continueInspector{err: ErrUnavailable}
			case "cross-deadline-in-probe":
				s.NativeInspector = &continueInspector{during: func() { *now = b.Deadline }}
			case "unrelated-latch":
				l.Reason = "NativeAuthorityUnavailable"
			}
			if err := m.Update(context.Background(), l, rv); err != nil {
				t.Fatal(err)
			}
			got, err := s.Continue(context.Background(), apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "once"}, owner)
			if err == nil {
				t.Fatal("unsafe continuation accepted", got)
			}
			retained, _, _ := m.Read(context.Background(), b.TaskUID)
			if retained.Binding != b || !retained.Latched || len(retained.Continuations) != 0 {
				t.Fatal("refusal reset authority")
			}
		})
	}
}
func TestContinueLostMutationAcknowledgementNeverReplays(t *testing.T) {
	s, m, _, b := stoppedForContinue(t)
	m.unknownOnce = true
	req := apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "once"}
	if _, err := s.Continue(context.Background(), req, "trusted-owner"); err == nil {
		t.Fatal("unknown ack accepted")
	}
	stored, _, _ := m.Read(context.Background(), b.TaskUID)
	if len(stored.Continuations) != 1 || stored.Binding.Epoch != 2 {
		t.Fatal("committed decision missing")
	}
	if _, err := s.Continue(context.Background(), req, "trusted-owner"); err == nil {
		t.Fatal("unknown ack mutation replayed")
	}
}
