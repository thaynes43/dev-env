package taskbudget

// Saga decision 94 (ADR-019, OpenProject #399 and #400): StallLimit without
// validated progress is a diagnostic and checkpoint signal, never a stop.
// Every other limit, fence and barrier still stops. Fake clock and stores.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// stallAuthority is a synthetic Ready original guard whose policy allows the
// one continuation. Each field is that check's result.
type stallAuthority struct {
	admission, continuation error
	during                  func()
}

func (a *stallAuthority) InspectNativeAdmission(context.Context, *Ledger, Binding) error {
	if a.during != nil {
		a.during()
	}
	return a.admission
}
func (a *stallAuthority) InspectNativeContinuation(context.Context, *Ledger, Binding) error {
	return a.continuation
}
func (a *stallAuthority) AuthorizeNativeContinue(context.Context, *Ledger, Binding) error {
	return nil
}

// stallValidator accepts the fixture receipts and the original guard's exact
// stop. Like production, it has no owner-decision adapter.
type stallValidator struct{}

func (stallValidator) Validate(ctx context.Context, l *Ledger, e apiv1.TaskBudgetEvent) error {
	if e.Kind == "worker-stop" && e.Evidence.Kind == "owned-root-stop" && e.Evidence.Reference == l.Binding.PodUID {
		return nil
	}
	return receiptValidator{}.Validate(ctx, l, e)
}
func (stallValidator) ValidateOwnerDecision(context.Context, *Ledger, apiv1.TaskBudgetExtension, string) error {
	return ErrUnavailable
}

// stalledCampaign is an admitted host with one failed attempt, a 3-hour
// overall budget, an 8-hour effort budget and a 2-hour checkpoint, so the
// 60-minute stall comes first. The clock still reads the creation time.
func stalledCampaign(t *testing.T, overall, effort, checkpoint time.Duration) (*Service, *memoryStore, *time.Time, Binding, *stallAuthority) {
	t.Helper()
	s, m, now, b := setup(t, overall, effort, checkpoint)
	auth := &stallAuthority{}
	s.NativeInspector, s.Validator = auth, stallValidator{}
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Record(context.Background(), failureEvent(b, "f1", "attempt-1", "blocker")); err != nil {
		t.Fatal(err)
	}
	return s, m, now, b, auth
}

func defaultStalledCampaign(t *testing.T) (*Service, *memoryStore, *time.Time, Binding, *stallAuthority) {
	t.Helper()
	return stalledCampaign(t, 3*time.Hour, 8*time.Hour, 2*time.Hour)
}

func stallRecords(l *Ledger) []Event {
	var out []Event
	for _, e := range l.Events {
		if e.Request.Kind == "activity" && e.Request.Evidence.Kind == "stall-signal" {
			out = append(out, e)
		}
	}
	return out
}

func sameStall(got *apiv1.TaskBudgetStall, want apiv1.TaskBudgetStall) bool {
	return got != nil && got.ID == want.ID && got.Since.Equal(want.Since) && got.At.Equal(want.At) && got.Signals == want.Signals
}

func ownedStop(b Binding) apiv1.TaskBudgetEvent {
	id := apiv1.OwnedStopEventID(b)
	return apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: "worker-stop", WorkerID: fmt.Sprintf("host:%s:%d", b.HostID, b.Epoch),
		Evidence: apiv1.TaskBudgetEvidence{ID: id, Kind: "owned-root-stop", Reference: b.PodUID}}
}

// 1. The signal and its retained state are observable.
func TestStallSignalIsObservableRetainedAndNotALatch(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	created := *now
	for _, d := range []time.Duration{30 * time.Minute, StallLimit - time.Millisecond} {
		*now = created.Add(d)
		l, err := record(t, s, b, "heartbeat", "heartbeat")
		if err != nil || l.Latched || l.Status().Stall != nil {
			t.Fatalf("stall before the limit: %+v %v", l.Status(), err)
		}
	}
	*now = created.Add(StallLimit)
	writes := m.updates
	// Heartbeats are not progress: they reach the limit like silence does.
	l, err := record(t, s, b, "heartbeat", "heartbeat")
	if err != nil {
		t.Fatal(err)
	}
	st := l.Status()
	if l.Latched || st.Latched || l.Reason != "" || st.Reason != "" || l.Escalation != nil || st.Escalation != nil {
		t.Fatalf("stall stopped the campaign or asked the owner: %+v", st)
	}
	want := apiv1.TaskBudgetStall{ID: fmt.Sprintf("stall:%d", created.UnixNano()), Since: created, At: created.Add(StallLimit), Signals: 1}
	if !sameStall(st.Stall, want) {
		t.Fatalf("stall signal = %+v, want %+v", st.Stall, want)
	}
	if st.LastEvidence == nil || st.LastEvidence.ID != "receipt:attempt-1" {
		t.Fatalf("a stall signal became evidence: %+v", st.LastEvidence)
	}
	stored, _, _ := m.Read(ctx, b.TaskUID)
	records := stallRecords(stored)
	if len(records) != 1 || m.updates != writes+1 || records[0].Request.ID != want.ID || records[0].Request.Binding != b || !records[0].At.Equal(want.At) {
		t.Fatalf("stall not retained exactly once: %+v, %d writes", records, m.updates-writes)
	}
	// More quiet time keeps the same signal and writes nothing.
	for _, d := range []time.Duration{time.Minute, 30 * time.Minute} {
		*now = created.Add(StallLimit + d)
		writes = m.updates
		l, err = s.Observe(ctx, b)
		if err != nil || l.Latched || !sameStall(l.Status().Stall, want) || m.updates != writes {
			t.Fatalf("repeated observation changed the signal: %+v, %d writes, %v", l.Status().Stall, m.updates-writes, err)
		}
	}
	// A caller cannot forge a record: public activity is never retained.
	forged := apiv1.TaskBudgetEvent{Binding: b, ID: "forged", Kind: "activity", Evidence: apiv1.TaskBudgetEvidence{ID: "forged", Kind: "stall-signal", Reference: "x"}}
	if _, err = s.Record(ctx, forged); err != nil {
		t.Fatal(err)
	}
	stored, _, _ = m.Read(ctx, b.TaskUID)
	if len(stallRecords(stored)) != 1 {
		t.Fatal("caller forged a stall record")
	}
}

// 2. Safe inspection and diagnosis while stalled.
func TestStalledCampaignStaysInspectableAndSteerable(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(StallLimit)
	// The original guard's periodic gate and the API's thread/read precheck.
	l, err := s.ObserveNative(ctx, b)
	if err != nil || l.Latched || l.NativeStopRequested() || l.Status().Stall == nil {
		t.Fatalf("stalled host cannot be observed for diagnosis: %+v %v", l.Status(), err)
	}
	st := l.Status()
	if len(st.Blockers) != 1 || st.Blockers[0].FailedAttempts != 1 || !st.LastProgress.Equal(created) || st.EffortMilliseconds != StallLimit.Milliseconds() || !st.NextCheckpoint.Equal(created.Add(2*time.Hour)) {
		t.Fatalf("diagnosis view lost counters: %+v", st)
	}
	inspected, _, _ := m.Read(ctx, b.TaskUID)
	// Steering: one owner request is reserved, then consumed exactly once.
	r := apiv1.NativeControlRequest{Binding: b, ID: "steer-1", Operation: "turn/start", ThreadID: "thread-a", Text: "use a fake clock"}
	if _, err = s.ReserveNativeControl(ctx, r, "owner"); err != nil {
		t.Fatal("owner steering refused while stalled", err)
	}
	if _, err = s.ConsumeNativeControl(ctx, r); err != nil {
		t.Fatal("reserved steering not consumable while stalled", err)
	}
	if _, err = s.ConsumeNativeControl(ctx, r); !errors.Is(err, ErrDenied) {
		t.Fatal("steering request replayed", err)
	}
	after, _, _ := m.Read(ctx, b.TaskUID)
	if after.Latched || !after.StallAnchor.Equal(inspected.StallAnchor) || !after.LastProgress.Equal(created) || len(stallRecords(after)) != 1 || after.EffortMilliseconds != inspected.EffortMilliseconds || len(after.NativeControlRecords()) != 1 {
		t.Fatal("inspection or steering changed the record beyond its own audit")
	}
}

// 3. The same owner continues; counters and remaining budget are kept.
func TestStalledSameOwnerContinuationKeepsCountersAndBudget(t *testing.T) {
	ctx := context.Background()
	s, _, now, b, _ := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(StallLimit)
	before, err := s.Observe(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Admit(ctx, b)
	if err != nil || l.Latched || len(l.Workers) != 1 || l.Binding != b || l.Spec != before.Spec ||
		l.EffortMilliseconds != before.EffortMilliseconds || l.EffortCeilingMilliseconds != before.EffortCeilingMilliseconds ||
		!l.StallAnchor.Equal(created) || !l.LastProgress.Equal(created) || !l.NextCheckpoint.Equal(before.NextCheckpoint) || len(l.Status().Blockers) != 1 {
		t.Fatalf("stalled owner continuation refused or reset: %+v %v", l, err)
	}
	// The same budget keeps charging.
	*now = now.Add(10 * time.Minute)
	if l, err = s.Observe(ctx, b); err != nil || l.EffortMilliseconds != before.EffortMilliseconds+(10*time.Minute).Milliseconds() {
		t.Fatal("effort not carried", err)
	}
	// Child dispatch proceeds under the same campaign.
	if _, err = s.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch:c1", Kind: "worker-start", WorkerID: "child:c1", AttemptID: "attempt-c1"}); err != nil {
		t.Fatal("stalled campaign refused child dispatch", err)
	}
	// Validated progress moves the anchor and ends this signal; the record and failures stay.
	progress := apiv1.TaskBudgetEvent{Binding: b, ID: "p1", Kind: "progress", Evidence: apiv1.TaskBudgetEvidence{ID: "p1", Kind: "verified-result", Reference: "fixture:verified:p1"}}
	if l, err = s.Record(ctx, progress); err != nil || l.Status().Stall != nil || !l.StallAnchor.Equal(*now) || len(stallRecords(l)) != 1 || l.Status().Blockers[0].FailedAttempts != 1 {
		t.Fatalf("progress while stalled: %+v %v", l.Status(), err)
	}
	// Another quiet hour is a second, distinct signal, and the count carries.
	anchor := *now
	*now = now.Add(StallLimit)
	l, err = s.Observe(ctx, b)
	want := apiv1.TaskBudgetStall{ID: fmt.Sprintf("stall:%d", anchor.UnixNano()), Since: anchor, At: anchor.Add(StallLimit), Signals: 2}
	if err != nil || l.Latched || !sameStall(l.Status().Stall, want) || len(stallRecords(l)) != 2 || l.Binding != b {
		t.Fatalf("second stall window: %+v %v", l.Status().Stall, err)
	}
}

// 3, owner stop path: the one continuation past the stall limit keeps the original budget.
func TestContinueAfterOwnerStopPastTheStallLimitKeepsTheOriginalBudget(t *testing.T) {
	ctx := context.Background()
	s, _, now, b, _ := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(30 * time.Minute)
	stopped, err := s.Record(ctx, ownedStop(b))
	if err != nil || stopped.Reason != "NativeRootStopped" {
		t.Fatal("owner stop", stopped.Reason, err)
	}
	*now = created.Add(StallLimit + time.Minute)
	next, err := s.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "owner-once"}, "owner")
	if err != nil || next.Latched || next.Binding.Epoch != 2 || !next.Binding.Deadline.Equal(b.Deadline) || next.Binding.PodUID != b.PodUID ||
		next.EffortMilliseconds != stopped.EffortMilliseconds || next.EffortCeilingMilliseconds != stopped.EffortCeilingMilliseconds ||
		!next.StallAnchor.Equal(created) || next.Status().Stall == nil || next.Status().Stall.Signals != 1 || len(next.Status().Blockers) != 1 || len(next.Continuations) != 1 {
		t.Fatalf("continuation past the stall limit refused or reset: %+v %v", next, err)
	}
	if _, err = s.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: next.Binding, DecisionID: "owner-twice"}, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("second continuation", err)
	}
}

// 4. An exhausted budget still stops, and so does every barrier.
func TestStallNeverMasksExhaustionOrABarrier(t *testing.T) {
	wantReason := map[string]string{"effort": "EffortBudget", "overall": "OverallBudget", "checkpoint": "Checkpoint", "repeated-failure": "RepeatedFailure", "native-authority": "NativeAuthorityUnavailable"}
	for _, scenario := range []string{"effort", "overall", "checkpoint", "repeated-failure", "native-authority", "owner-stop", "no-validator", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			overall, effort, checkpoint := 3*time.Hour, 8*time.Hour, 2*time.Hour
			edge := StallLimit + 10*time.Minute
			switch scenario {
			case "effort":
				effort = edge
			case "overall":
				overall, checkpoint = edge, edge
			case "checkpoint":
				checkpoint = edge
			}
			s, _, now, b, auth := stalledCampaign(t, overall, effort, checkpoint)
			created := *now
			*now = created.Add(StallLimit)
			if l, err := s.Observe(ctx, b); err != nil || l.Latched || l.Status().Stall == nil {
				t.Fatalf("stall missing or stopped: %+v %v", l.Status(), err)
			}
			*now = created.Add(edge)
			switch scenario {
			case "repeated-failure":
				for i := 2; i <= 3; i++ {
					if _, err := s.Record(ctx, failureEvent(b, fmt.Sprintf("f%d", i), fmt.Sprintf("attempt-%d", i), "blocker")); err != nil {
						t.Fatal(err)
					}
				}
			case "native-authority":
				auth.continuation = ErrUnavailable
				if _, err := s.ObserveNative(ctx, b); !errors.Is(err, ErrUnavailable) {
					t.Fatal("lost native authority ignored", err)
				}
			case "owner-stop":
				if _, err := s.ReserveNativeControl(ctx, apiv1.NativeControlRequest{Binding: b, ID: "owner-stop", Operation: "host/stop"}, "owner"); err != nil {
					t.Fatal(err)
				}
			case "no-validator":
				s.Validator = nil
			case "permission":
				auth.admission = ErrDenied
			}
			l, err := s.Admit(ctx, b)
			switch scenario {
			case "owner-stop":
				if !errors.Is(err, ErrDenied) || !l.NativeStopRequested() {
					t.Fatal("owner stop did not hold", err)
				}
			case "no-validator":
				if !errors.Is(err, ErrUnavailable) {
					t.Fatal("unknown receipt authority admitted", err)
				}
			case "permission":
				if !errors.Is(err, ErrDenied) {
					t.Fatal("refused readiness admitted", err)
				}
			default:
				if !errors.Is(err, ErrDenied) || !l.Latched || l.Reason != wantReason[scenario] || l.Escalation == nil || l.Escalation.Reason != wantReason[scenario] {
					t.Fatalf("limit did not stop: latched=%t reason=%q %v", l.Latched, l.Reason, err)
				}
			}
			if l == nil || l.Status().Stall == nil {
				t.Fatal("stop hid the stall signal")
			}
			if scenario == "permission" {
				return // a readiness refusal gates admission only
			}
			if _, err = s.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch:late", Kind: "worker-start", WorkerID: "child:late", AttemptID: "late"}); err == nil {
				t.Fatal("child dispatched past a stop")
			}
		})
	}
}

// 5. A restart keeps the state; no other identity takes custody.
func TestStallSurvivesRestartAndCustodyStaysFenced(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(StallLimit)
	first, err := s.Observe(ctx, b)
	if err != nil || first.Status().Stall == nil {
		t.Fatal("stall missing", err)
	}
	later := created.Add(StallLimit + 15*time.Minute)
	restarted := &Service{Store: m, Now: func() time.Time { return later }, Validator: stallValidator{}, NativeInspector: &stallAuthority{}}
	l, err := restarted.Observe(ctx, b)
	if err != nil || l.Latched || !sameStall(l.Status().Stall, *first.Status().Stall) || len(stallRecords(l)) != 1 || l.EffortMilliseconds != first.EffortMilliseconds+(15*time.Minute).Milliseconds() {
		t.Fatalf("restart changed the stall or counters: %+v %v", l.Status(), err)
	}
	if l, err = restarted.Admit(ctx, b); err != nil || len(l.Workers) != 1 {
		t.Fatal("original owner refused after restart", err)
	}
	others := map[string]Binding{}
	for _, name := range []string{"pod", "host", "epoch", "deadline"} {
		o := b
		switch name {
		case "pod":
			o.PodUID = "replacement-pod"
		case "host":
			o.HostID = "host-b"
		case "epoch":
			o.Epoch = 2
		case "deadline":
			o.Deadline = o.Deadline.Add(time.Hour)
		}
		others[name] = o
	}
	for name, o := range others {
		if _, err = restarted.Admit(ctx, o); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s took over admission: %v", name, err)
		}
		if _, err = restarted.Observe(ctx, o); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s observed as the owner: %v", name, err)
		}
		if _, err = restarted.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: o, DecisionID: "takeover"}, "owner"); err == nil {
			t.Fatalf("%s continued the campaign", name)
		}
	}
	if _, err = restarted.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "takeover"}, ""); !errors.Is(err, ErrDenied) {
		t.Fatal("anonymous continuation", err)
	}
	stored, _, _ := m.Read(ctx, b.TaskUID)
	if stored.Binding != b || stored.Parent != "system/parent" || len(stored.Workers) != 1 || len(stored.Continuations) != 0 || len(stallRecords(stored)) != 1 {
		t.Fatalf("custody changed: %+v", stored)
	}
}

// 5, upgrade: a Stalled latch an older operator wrote stays a stop. Nothing is
// unlatched automatically; the old executor was already stopped by it.
func TestLegacyStalledLatchStaysStopped(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	l, rv, _ := m.Read(ctx, b.TaskUID)
	l.latch(now.Add(StallLimit), "Stalled")
	if err := m.Update(ctx, l, rv); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(StallLimit + time.Minute)
	got, err := s.Admit(ctx, b)
	if !errors.Is(err, ErrDenied) || !got.Latched || got.Reason != "Stalled" || got.Escalation == nil {
		t.Fatal("legacy stall latch released", err)
	}
	if _, err = s.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "legacy"}, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("legacy stall latch continued", err)
	}
}

// 6. Continuation never duplicates an executor or a dispatch.
func TestStalledContinuationNeverDuplicatesExecutorOrDispatch(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	*now = now.Add(StallLimit)
	// Two replicas observe the crossing at once: one record.
	s.Store = &concurrentReadStore{Store: m, ready: make(chan struct{})}
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, err := s.Observe(ctx, b); errs <- err }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s.Store = m
	stored, _, _ := m.Read(ctx, b.TaskUID)
	if stored.Latched || len(stallRecords(stored)) != 1 {
		t.Fatalf("concurrent observers: latched=%t records=%d", stored.Latched, len(stallRecords(stored)))
	}
	for range 2 {
		if l, err := s.Admit(ctx, b); err != nil || len(l.Workers) != 1 {
			t.Fatal("repeated admission duplicated the host", err)
		}
	}
	start := apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch:stable", Kind: "worker-start", WorkerID: "child:stable", AttemptID: "server-attempt-a"}
	for range 2 {
		if l, err := s.Record(ctx, start); err != nil || len(l.Workers) != 2 {
			t.Fatal("dispatch replay duplicated a worker", err)
		}
	}
	second := start
	second.AttemptID = "server-attempt-b"
	if _, err := s.Record(ctx, second); !errors.Is(err, ErrConflict) {
		t.Fatal("second attempt shared one reservation", err)
	}
	s.ManagedInspector = fixtureManagedAdmission{}
	if _, err := s.BindManagedWorker(ctx, b, "child:stable", ManagedWorkerRef{Namespace: "ns", Name: "child", SessionUID: "session-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReserveManagedLaunch(ctx, b, "child:stable"); err != nil {
		t.Fatal("stalled campaign refused its child's one launch", err)
	}
	if _, err := s.ReserveManagedLaunch(ctx, b, "child:stable"); !errors.Is(err, ErrDenied) {
		t.Fatal("second launch reserved", err)
	}
	stored, _, _ = m.Read(ctx, b.TaskUID)
	if len(stored.Workers) != 2 || len(stallRecords(stored)) != 1 {
		t.Fatalf("duplicate executor or signal: %+v", stored.Workers)
	}
}

// The record is written in the same operation that first sees the stall: a
// late progress receipt still leaves the quiet window on record.
func TestStallRecordedWhenProgressArrivesLate(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(StallLimit + time.Minute)
	progress := apiv1.TaskBudgetEvent{Binding: b, ID: "late", Kind: "progress", Evidence: apiv1.TaskBudgetEvidence{ID: "late", Kind: "verified-result", Reference: "fixture:verified:late"}}
	l, err := s.Record(ctx, progress)
	if err != nil || l.Latched || l.Status().Stall != nil || !l.StallAnchor.Equal(*now) {
		t.Fatalf("late progress: %+v %v", l.Status(), err)
	}
	stored, _, _ := m.Read(ctx, b.TaskUID)
	records := stallRecords(stored)
	if len(records) != 1 || records[0].Request.ID != fmt.Sprintf("stall:%d", created.UnixNano()) || stored.Events[len(stored.Events)-1].Request.ID != "late" {
		t.Fatalf("quiet window before late progress not recorded first: %+v", records)
	}
}

// A clock that crosses the limit during an independent probe is recorded in
// that same write, matching the status it returns.
func TestStallCrossedDuringAProbeIsRecordedInTheSameWrite(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, auth := defaultStalledCampaign(t)
	created := *now
	*now = created.Add(StallLimit - time.Second)
	auth.during = func() { *now = created.Add(StallLimit) }
	l, err := s.Admit(ctx, b)
	if err != nil || l.Latched || l.Status().Stall == nil {
		t.Fatalf("probe crossing the limit: %+v %v", l.Status(), err)
	}
	stored, _, _ := m.Read(ctx, b.TaskUID)
	if len(stallRecords(stored)) != 1 {
		t.Fatal("status showed a stall the write did not record")
	}
}

// A signal never takes the last history slots: an owner intent reserved at the
// edge stays consumable, and the stall is shown without a HistoryBound stop.
func TestStallSignalNeverTakesTheLastHistorySlots(t *testing.T) {
	ctx := context.Background()
	s, m, now, b, _ := defaultStalledCampaign(t)
	created := *now
	l, rv, _ := m.Read(ctx, b.TaskUID)
	for i := l.historyEvents(); i < MaxEvents-3; i++ {
		l.Events = append(l.Events, Event{At: created, Request: apiv1.TaskBudgetEvent{Binding: b, ID: fmt.Sprintf("filler-%d", i), Kind: "activity"}})
	}
	if err := m.Update(ctx, l, rv); err != nil {
		t.Fatal(err)
	}
	r := apiv1.NativeControlRequest{Binding: b, ID: "edge-steer", Operation: "turn/start", ThreadID: "thread-a", Text: "owner"}
	*now = created.Add(StallLimit - time.Minute)
	if _, err := s.ReserveNativeControl(ctx, r, "owner"); err != nil {
		t.Fatal("reserve at the edge", err)
	}
	*now = created.Add(StallLimit)
	l, err := s.Observe(ctx, b)
	if err != nil || l.Latched || l.Status().Stall == nil || l.Status().Stall.Signals != 0 || len(stallRecords(l)) != 0 || l.historyEvents() != MaxEvents-2 {
		t.Fatalf("stall at the history edge: latched=%t reason=%q stall=%+v events=%d %v", l.Latched, l.Reason, l.Status().Stall, l.historyEvents(), err)
	}
	if l, err = s.ConsumeNativeControl(ctx, r); err != nil || l.Latched {
		t.Fatal("stall took the reserved intent's consume slot", err)
	}
}

// evaluate returns early once latched. A policy-threshold latch (Checkpoint,
// RepeatedFailure, or a Stalled latch from an older operator) must not mask an
// authorized budget cap reached later: the stop is then recorded as the cap.
// A safety hold and a proven owner stop keep their reasons. Nothing unlatches,
// and no counter, deadline, epoch, worker or escalation changes.
func TestAdvisoryLatchNeverMasksAHardCap(t *testing.T) {
	cases := []struct {
		name                        string
		overall, effort, checkpoint time.Duration
		latchAt, capAt              time.Duration
		first, want                 string
	}{
		{"checkpoint-then-deadline", 30 * time.Minute, 8 * time.Hour, 10 * time.Minute, 10 * time.Minute, 30 * time.Minute, "Checkpoint", "OverallBudget"},
		{"checkpoint-then-effort", 3 * time.Hour, 20 * time.Minute, 10 * time.Minute, 10 * time.Minute, 20 * time.Minute, "Checkpoint", "EffortBudget"},
		{"repeated-failure-then-deadline", 30 * time.Minute, 8 * time.Hour, 20 * time.Minute, time.Minute, 30 * time.Minute, "RepeatedFailure", "OverallBudget"},
		{"legacy-stalled-then-effort", 3 * time.Hour, 70 * time.Minute, 2 * time.Hour, StallLimit, 70 * time.Minute, "Stalled", "EffortBudget"},
		{"safety-hold-keeps-its-reason", 30 * time.Minute, 8 * time.Hour, 20 * time.Minute, time.Minute, 30 * time.Minute, "NativeAuthorityUnavailable", "NativeAuthorityUnavailable"},
		{"owner-stop-keeps-its-reason", 30 * time.Minute, 8 * time.Hour, 20 * time.Minute, 5 * time.Minute, 30 * time.Minute, "NativeRootStopped", "NativeRootStopped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, m, now, b := setup(t, tc.overall, tc.effort, tc.checkpoint)
			auth := &stallAuthority{}
			s.NativeInspector, s.Validator = auth, stallValidator{}
			if _, err := s.Admit(ctx, b); err != nil {
				t.Fatal(err)
			}
			created := *now
			*now = created.Add(tc.latchAt)
			switch tc.first {
			case "RepeatedFailure":
				for i := 1; i <= 3; i++ {
					if _, err := s.Record(ctx, failureEvent(b, fmt.Sprintf("f%d", i), fmt.Sprintf("attempt-%d", i), "blocker")); err != nil {
						t.Fatal(err)
					}
				}
			case "Stalled":
				l, rv, _ := m.Read(ctx, b.TaskUID)
				l.latch(*now, "Stalled")
				if err := m.Update(ctx, l, rv); err != nil {
					t.Fatal(err)
				}
			case "NativeAuthorityUnavailable":
				auth.continuation = ErrUnavailable
				_, _ = s.ObserveNative(ctx, b)
			case "NativeRootStopped":
				if _, err := s.Record(ctx, ownedStop(b)); err != nil {
					t.Fatal(err)
				}
			}
			first, err := s.Observe(ctx, b)
			if err != nil || !first.Latched || first.Reason != tc.first || first.Escalation == nil {
				t.Fatalf("first latch: latched=%t reason=%q %v", first.Latched, first.Reason, err)
			}
			*now = created.Add(tc.capAt)
			l, err := s.Observe(ctx, b)
			if err != nil || !l.Latched || l.Reason != tc.want {
				t.Fatalf("after the cap: latched=%t reason=%q, want %q (%v)", l.Latched, l.Reason, tc.want, err)
			}
			if *l.Escalation != *first.Escalation || l.Binding != b || l.Spec != first.Spec || l.EffortCeilingMilliseconds != first.EffortCeilingMilliseconds ||
				!l.StallAnchor.Equal(first.StallAnchor) || !l.NextCheckpoint.Equal(first.NextCheckpoint) || len(l.Workers) != len(first.Workers) ||
				l.Workers[0].Active != first.Workers[0].Active || l.Status().StopConfirmed != first.Status().StopConfirmed || len(l.Status().Blockers) != len(first.Status().Blockers) {
				t.Fatal("recording the cap changed more than the latch reason")
			}
			if _, err = s.Admit(ctx, b); !errors.Is(err, ErrDenied) {
				t.Fatal("admitted past a cap", err)
			}
			if _, err = s.Continue(ctx, apiv1.ContinueTaskBudgetRequest{Binding: b, DecisionID: "after-cap"}, "owner"); !errors.Is(err, ErrDenied) {
				t.Fatal("continued past a cap", err)
			}
			stored, _, _ := m.Read(ctx, b.TaskUID)
			if stored.Reason != tc.want || stored.Binding != b || len(stored.Continuations) != 0 {
				t.Fatalf("stored reason %q", stored.Reason)
			}
		})
	}
}
