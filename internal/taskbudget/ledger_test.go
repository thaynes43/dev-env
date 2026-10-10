package taskbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type memoryStore struct {
	mu           sync.Mutex
	data         []byte
	rv           int
	conflictOnce bool
	unknownOnce  bool
	updates      int
}

func (m *memoryStore) Create(_ context.Context, l *Ledger) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data != nil {
		return ErrConflict
	}
	m.data, _ = json.Marshal(l)
	m.rv = 1
	return nil
}
func (m *memoryStore) Read(_ context.Context, uid string) (*Ledger, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var l Ledger
	if json.Unmarshal(m.data, &l) != nil || l.Binding.TaskUID != uid {
		return nil, "", ErrUnavailable
	}
	return &l, fmt.Sprint(m.rv), nil
}
func (m *memoryStore) Update(_ context.Context, l *Ledger, rv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates++
	if m.conflictOnce {
		m.conflictOnce = false
		return ErrConflict
	}
	if rv != fmt.Sprint(m.rv) {
		return ErrConflict
	}
	m.data, _ = json.Marshal(l)
	m.rv++
	if m.unknownOnce {
		m.unknownOnce = false
		return ErrUnavailable
	}
	return nil
}

// This validator represents independent fixture receipts; production has no
// permissive validator and refuses every evidence/extension claim by default.
type receiptValidator struct{ owner string }

func failureEvent(b Binding, id, attempt, blocker string) apiv1.TaskBudgetEvent {
	return apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: "failure", AttemptID: attempt, BlockerID: blocker,
		Evidence: apiv1.TaskBudgetEvidence{ID: "receipt:" + attempt, Kind: "supervised-failure", Reference: "fixture:failure:" + blocker + ":" + attempt}}
}

type failedReceipt struct {
	attempt string
	blocker string
}

// A bounded independent receipt catalog acts as the trusted execution source.
// Relabeling the event cannot change the failure found at the same reference.
var failureReceipts = map[string]failedReceipt{
	"fixture:failure:blocker:attempt":                {"attempt", "blocker"},
	"fixture:failure:blocker:attempt-1":              {"attempt-1", "blocker"},
	"fixture:failure:blocker:attempt-2":              {"attempt-2", "blocker"},
	"fixture:failure:blocker:attempt-3":              {"attempt-3", "blocker"},
	"fixture:failure:same-storage-blocker:attempt-1": {"attempt-1", "same-storage-blocker"},
	"fixture:failure:same-storage-blocker:attempt-2": {"attempt-2", "same-storage-blocker"},
	"fixture:failure:same-storage-blocker:attempt-3": {"attempt-3", "same-storage-blocker"},
}

func (v receiptValidator) Validate(_ context.Context, _ *Ledger, e apiv1.TaskBudgetEvent) error {
	if e.Kind == "failure" {
		r, ok := failureReceipts[e.Evidence.Reference]
		if !ok || r.attempt != e.AttemptID || r.blocker != e.BlockerID {
			return ErrDenied
		}
		return nil
	}
	if !strings.HasPrefix(e.Evidence.Reference, "fixture:verified") {
		return ErrDenied
	}
	return nil
}
func (v receiptValidator) ValidateOwnerDecision(_ context.Context, l *Ledger, e apiv1.TaskBudgetExtension, owner string) error {
	if owner != v.owner || e.Evidence.Reference != "fixture:phone-decision" || e.Evidence.ID != l.Escalation.ID || e.Binding != l.Binding {
		return ErrDenied
	}
	return nil
}

func setup(t *testing.T, overall, effort, checkpoint time.Duration) (*Service, *memoryStore, *time.Time, Binding) {
	t.Helper()
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	m := &memoryStore{}
	s := &Service{Store: m, Now: func() time.Time { return now }, Validator: receiptValidator{}}
	l, err := s.Create(context.Background(), "task-a", "host-a", "pod-a", "system/parent", Spec{SuccessCondition: "verified result", OverallSeconds: int64(overall / time.Second), EffortSeconds: int64(effort / time.Second), CheckpointSeconds: int64(checkpoint / time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return s, m, &now, l.Binding
}

func record(t *testing.T, s *Service, b Binding, id, kind string) (*Ledger, error) {
	t.Helper()
	return s.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: kind})
}

func TestStallDespiteHeartbeatsAndActivity(t *testing.T) {
	s, _, now, b := setup(t, 2*time.Hour, 2*time.Hour, 2*time.Hour)
	base := *now
	for i, d := range []time.Duration{30 * time.Minute, 59 * time.Minute, 60 * time.Minute} {
		*now = base.Add(d)
		l, err := record(t, s, b, fmt.Sprintf("heartbeat-%d", i), "heartbeat")
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 && l.Latched {
			t.Fatal("latched early")
		}
		if i == 2 && (!l.Latched || l.Reason != "Stalled" || l.Escalation == nil) {
			t.Fatalf("stall missing: %+v", l.Status())
		}
	}
	l, err := s.Admit(context.Background(), b)
	if !errors.Is(err, ErrDenied) || !l.Latched {
		t.Fatal("stalled task admitted")
	}
}

type concurrentReadStore struct {
	Store
	mu    sync.Mutex
	reads int
	ready chan struct{}
}

func (s *concurrentReadStore) Read(ctx context.Context, uid string) (*Ledger, string, error) {
	l, rv, err := s.Store.Read(ctx, uid)
	s.mu.Lock()
	s.reads++
	n := s.reads
	if n == 2 {
		close(s.ready)
	}
	s.mu.Unlock()
	if n <= 2 {
		<-s.ready
	}
	return l, rv, err
}

func TestConcurrentWritersKeepBothFailures(t *testing.T) {
	s, m, _, b := setup(t, time.Hour, time.Hour, time.Hour)
	s.Store = &concurrentReadStore{Store: m, ready: make(chan struct{})}
	errs := make(chan error, 2)
	for i := 1; i <= 2; i++ {
		go func(i int) {
			_, err := s.Record(context.Background(), failureEvent(b, fmt.Sprintf("event-%d", i), fmt.Sprintf("attempt-%d", i), "blocker"))
			errs <- err
		}(i)
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	l, _, err := m.Read(context.Background(), b.TaskUID)
	if err != nil || len(l.Events) != 2 || m.updates != 3 {
		t.Fatalf("concurrent CAS lost history: %d updates, %v", m.updates, err)
	}
}

func TestResolvedBlockerAndReusedEvidenceRemainHistory(t *testing.T) {
	s, _, _, b := setup(t, time.Hour, time.Hour, time.Hour)
	s.Validator = receiptValidator{}
	for i := 1; i <= 2; i++ {
		_, err := s.Record(context.Background(), failureEvent(b, fmt.Sprintf("event-%d", i), fmt.Sprintf("attempt-%d", i), "blocker"))
		if err != nil {
			t.Fatal(err)
		}
	}
	e := apiv1.TaskBudgetEvent{Binding: b, ID: "resolved", Kind: "resolved", BlockerID: "blocker", Evidence: apiv1.TaskBudgetEvidence{ID: "receipt", Kind: "completion", Reference: "fixture:verified:receipt"}}
	l, err := s.Record(context.Background(), e)
	if err != nil || len(l.Events) != 3 {
		t.Fatal("resolution erased failures")
	}
	e.ID = "new-id"
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrConflict) {
		t.Fatal("same evidence advanced progress twice")
	}
	l, err = s.Record(context.Background(), failureEvent(b, "event-3", "attempt-3", "blocker"))
	if err != nil || !l.Latched || len(l.Events) != 4 {
		t.Fatal("recurrent same blocker reset failures")
	}
}

func TestFailureClassificationCannotBeAgentSelected(t *testing.T) {
	s, _, _, b := setup(t, time.Hour, time.Hour, time.Hour)
	e := failureEvent(b, "event-1", "attempt-1", "blocker")
	e.BlockerID = "new-technique-label"
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrDenied) {
		t.Fatal("caller relabeled supervised blocker")
	}
	s.Validator = nil
	e = failureEvent(b, "event-1", "attempt-1", "blocker")
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing failure authority accepted agent assertion")
	}
}

func TestMissingValidatorCannotAdmitWork(t *testing.T) {
	s, _, _, b := setup(t, time.Hour, time.Hour, time.Hour)
	s.Validator = nil
	if _, err := s.Admit(context.Background(), b); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown receipt authority admitted host")
	}
	if _, err := s.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch", Kind: "worker-start", WorkerID: "child"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unknown receipt authority admitted child")
	}
	l, err := s.Observe(context.Background(), b)
	if err != nil || len(l.Workers) != 0 {
		t.Fatal("failed admission created executor accounting", err)
	}
}

func TestThirdFailureAcrossChildrenAndServiceRestart(t *testing.T) {
	s, m, _, b := setup(t, 2*time.Hour, 2*time.Hour, 2*time.Hour)
	for i := 1; i <= 3; i++ {
		// New service instance models controller/worker replacement; task and
		// stable blocker identity remain the same across differing techniques.
		s = &Service{Store: m, Now: s.Now, Validator: s.Validator}
		e := failureEvent(b, fmt.Sprintf("child-%d", i), fmt.Sprintf("attempt-%d", i), "same-storage-blocker")
		l, err := s.Record(context.Background(), e)
		if err != nil {
			t.Fatal(err)
		}
		if l.Latched != (i == 3) {
			t.Fatalf("failure %d latch=%v", i, l.Latched)
		}
		before := len(l.Events)
		l, err = s.Record(context.Background(), e)
		if err != nil || len(l.Events) != before {
			t.Fatal("duplicate counted")
		}
	}
	l, _ := s.Observe(context.Background(), b)
	if l.Reason != "RepeatedFailure" || l.Escalation.ID != "task-a:1" {
		t.Fatalf("wrong escalation: %+v", l.Status())
	}
	changed := failureEvent(b, "new-event", "attempt-1", "same-storage-blocker")
	changed.BlockerID = "other-blocker"
	if _, err := s.Record(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatal("stable attempt identity reassigned")
	}
	if _, err := s.Create(context.Background(), b.TaskUID, b.HostID, "replacement-pod", "system/parent", Spec{SuccessCondition: "again", OverallSeconds: 60, EffortSeconds: 60, CheckpointSeconds: 60}); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement erased task")
	}
	replacement := b
	replacement.PodUID = "replacement-pod"
	if _, err := s.Admit(context.Background(), replacement); !errors.Is(err, ErrDenied) {
		t.Fatal("unproven replacement admitted")
	}
}

func TestParallelChildEffortAndOverallDeadline(t *testing.T) {
	t.Run("aggregate", func(t *testing.T) {
		s, _, now, b := setup(t, 45*time.Minute, 60*time.Minute, 45*time.Minute)
		if _, err := s.Admit(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"child-a", "child-b"} {
			if _, err := s.Record(context.Background(), apiv1.TaskBudgetEvent{Binding: b, ID: id, Kind: "worker-start", WorkerID: id}); err != nil {
				t.Fatal(err)
			}
		}
		*now = now.Add(20 * time.Minute)
		l, err := s.Observe(context.Background(), b)
		if err != nil || !l.Latched || l.Reason != "EffortBudget" || l.EffortMilliseconds != int64(60*time.Minute/time.Millisecond) {
			t.Fatalf("combined effort: %+v %v", l.Status(), err)
		}
	})
	t.Run("overall-even-with-progress", func(t *testing.T) {
		s, _, now, b := setup(t, 45*time.Minute, 60*time.Minute, 10*time.Minute)
		s.Validator = receiptValidator{}
		for i := 1; i <= 5; i++ {
			*now = now.Add(9 * time.Minute)
			e := apiv1.TaskBudgetEvent{Binding: b, ID: fmt.Sprintf("proof-%d", i), Kind: "progress", Evidence: apiv1.TaskBudgetEvidence{ID: fmt.Sprintf("proof-%d", i), Kind: "completion", Reference: fmt.Sprintf("fixture:verified:%d", i)}}
			l, err := s.Record(context.Background(), e)
			if i < 5 && err != nil {
				t.Fatal(err)
			}
			if i == 5 && (!errors.Is(err, ErrDenied) || !l.Latched || l.Reason != "OverallBudget") {
				t.Fatalf("overall deadline bypassed: %+v %v", l.Status(), err)
			}
		}
	})
}

func TestProgressNeedsIndependentEvidence(t *testing.T) {
	s, _, now, b := setup(t, 2*time.Hour, 2*time.Hour, 10*time.Minute)
	s.Validator = nil
	*now = now.Add(9 * time.Minute)
	e := apiv1.TaskBudgetEvent{Binding: b, ID: "proof", Kind: "progress", Evidence: apiv1.TaskBudgetEvidence{ID: "proof", Kind: "assertion", Reference: "agent-says-progress"}}
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrUnavailable) {
		t.Fatal("nil validator accepted progress")
	}
	s.Validator = receiptValidator{}
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrDenied) {
		t.Fatal("assertion accepted")
	}
	*now = now.Add(time.Minute)
	l, err := s.Observe(context.Background(), b)
	if err != nil || !l.Latched || l.Reason != "Checkpoint" {
		t.Fatal("checkpoint silently extended")
	}
}

func TestCASConflictAndUnknownWrite(t *testing.T) {
	s, m, _, b := setup(t, time.Hour, time.Hour, time.Hour)
	m.conflictOnce = true
	e := failureEvent(b, "failure", "attempt", "blocker")
	l, err := s.Record(context.Background(), e)
	if err != nil || len(l.Events) != 1 || m.updates != 2 {
		t.Fatal("typed CAS conflict lost/duplicated event")
	}
	m.unknownOnce = true
	before := m.updates
	e.ID = "failure-2"
	e.AttemptID = "attempt-2"
	e.Evidence = failureEvent(b, "failure-2", "attempt-2", "blocker").Evidence
	if _, err := s.Record(context.Background(), e); !errors.Is(err, ErrUnavailable) || m.updates != before+1 {
		t.Fatal("unknown write replayed")
	}
	l, _, _ = m.Read(context.Background(), b.TaskUID)
	if len(l.Events) != 2 {
		t.Fatal("unknown write lost history")
	}
	// Caller retries the same stable event only; it is idempotent after read.
	l, err = s.Record(context.Background(), e)
	if err != nil || len(l.Events) != 2 {
		t.Fatal("external idempotent retry duplicated")
	}
}

func TestStickyNotificationAndUncertainStop(t *testing.T) {
	s, _, now, b := setup(t, time.Minute, time.Hour, time.Minute)
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	s.Validator = nil
	*now = now.Add(time.Minute)
	l, err := record(t, s, b, "notification-1", "notification-failed")
	if err != nil || l.Escalation.Delivery != "Failed" {
		t.Fatal("notification failure not retained")
	}
	if _, err := record(t, s, b, "notification-2", "notification-failed"); !errors.Is(err, ErrDenied) {
		t.Fatal("second pending escalation allowed")
	}
	stop := apiv1.TaskBudgetEvent{Binding: b, ID: "stop", Kind: "worker-stop", WorkerID: "host:host-a:1", Evidence: apiv1.TaskBudgetEvidence{ID: "stop", Kind: "missing-pod", Reference: "pod-absent"}}
	if _, err := s.Record(context.Background(), stop); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing Pod became stop proof")
	}
	if l.Status().StopConfirmed {
		t.Fatal("uncertain stop confirmed")
	}
	if _, err := s.Admit(context.Background(), b); !errors.Is(err, ErrDenied) {
		t.Fatal("uncertain stop allowed another writer")
	}
}

func TestOwnerExtensionRetainsAllHistory(t *testing.T) {
	s, _, now, b := setup(t, time.Minute, time.Hour, time.Minute)
	s.Validator = receiptValidator{owner: "owner"}
	if _, err := s.Admit(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	l, _ := s.Observe(context.Background(), b)
	decision := apiv1.TaskBudgetExtension{Binding: b, DecisionID: "decision-a", NextStep: "one bounded alternative", OverallSeconds: 120, EffortSeconds: 120, CheckpointSeconds: 60, Evidence: apiv1.TaskBudgetEvidence{ID: l.Escalation.ID, Kind: "owner-answer", Reference: "fixture:phone-decision"}}
	if _, err := s.Extend(context.Background(), decision, "agent"); !errors.Is(err, ErrDenied) {
		t.Fatal("self extension accepted")
	}
	if _, err := s.Extend(context.Background(), decision, "owner"); !errors.Is(err, ErrDenied) {
		t.Fatal("uncertain stop/undelivered answer accepted")
	}
	for _, e := range []apiv1.TaskBudgetEvent{
		{Binding: b, ID: "stop", Kind: "worker-stop", WorkerID: "host:host-a:1", Evidence: apiv1.TaskBudgetEvidence{ID: "stop", Kind: "supervised-tree-stop", Reference: "fixture:verified"}},
		{Binding: b, ID: "phone", Kind: "notification-delivered", Evidence: apiv1.TaskBudgetEvidence{ID: "phone", Kind: "phone-delivery", Reference: "fixture:verified"}},
	} {
		if _, err := s.Record(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	l, err := s.Extend(context.Background(), decision, "owner")
	if err != nil || l.Latched || l.Binding.Epoch != 2 || len(l.Workers) != 1 || len(l.Events) != 2 || len(l.Extensions) != 1 || len(l.Escalations) != 1 || !l.LastProgress.Equal(l.CreatedAt) {
		t.Fatalf("extension erased history: %+v %v", l, err)
	}
	if _, err := s.Admit(context.Background(), b); !errors.Is(err, ErrDenied) {
		t.Fatal("old epoch admitted")
	}
	if _, err := s.Admit(context.Background(), l.Binding); err != nil {
		t.Fatal("verified bounded next epoch refused", err)
	}
}
