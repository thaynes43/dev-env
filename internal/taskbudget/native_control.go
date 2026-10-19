package taskbudget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

type NativeControlRecord struct {
	ID            string     `json:"id"`
	Operation     string     `json:"operation"`
	Epoch         uint64     `json:"epoch"`
	Owner         string     `json:"owner"`
	RequestSHA256 string     `json:"requestSHA256"`
	At            time.Time  `json:"at"`
	ConsumedAt    *time.Time `json:"consumedAt,omitempty"`
}

const nativeOwnerIntent = "native-owner-intent"
const nativeOwnerConsumed = "native-owner-consumed"

func nativeAuditID(kind, id string) string {
	sum := sha256.Sum256([]byte(id))
	return kind + ":" + hex.EncodeToString(sum[:20])
}

// Keep audit data in the existing Events wire fields. An older operator replica
// preserves these records when rewriting a ledger; a new top-level field would
// be silently discarded by its old JSON decoder during an ordinary upgrade.
func (l *Ledger) NativeControlRecords() []NativeControlRecord {
	var records []NativeControlRecord
	consumed := map[string]time.Time{}
	for _, e := range l.Events {
		if e.Request.Kind == "activity" && e.Request.Evidence.Kind == nativeOwnerConsumed {
			consumed[e.Request.ID] = e.At
		}
	}
	for _, e := range l.Events {
		if e.Request.Kind != "activity" || e.Request.Evidence.Kind != nativeOwnerIntent {
			continue
		}
		r := NativeControlRecord{ID: e.Request.Evidence.ID, Operation: e.Request.AttemptID, Epoch: e.Request.Binding.Epoch, Owner: e.Request.WorkerID, RequestSHA256: e.Request.Evidence.Reference, At: e.At}
		if at, ok := consumed[nativeAuditID(nativeOwnerConsumed, r.ID)]; ok {
			r.ConsumedAt = &at
		}
		records = append(records, r)
	}
	return records
}

func (l *Ledger) NativeStopRequested() bool {
	for _, r := range l.NativeControlRecords() {
		if r.Epoch == l.Binding.Epoch && r.Operation == "host/stop" {
			return true
		}
	}
	return false
}

// ReserveNativeControl commits the owner intent BEFORE dispatch. Every ID is
// consumed even if execution/acknowledgement is lost. Neither store errors nor
// repeated owner HTTP requests replay an inference RPC. No prompt is retained.
func (s *Service) ReserveNativeControl(ctx context.Context, r apiv1.NativeControlRequest, owner string) (*Ledger, error) {
	if !r.Valid() || owner == "" || len(owner) > 256 {
		return nil, ErrDenied
	}
	return s.mutate(ctx, r.Binding, func(l *Ledger, _ time.Time) error {
		if l.Latched || !l.NativeAdmitted() || l.NativeStopRequested() || l.historyEvents() >= MaxEvents-2 {
			return ErrDenied
		}
		for _, prior := range l.NativeControlRecords() {
			if prior.ID == r.ID {
				return ErrConflict
			}
		}
		if s.NativeInspector == nil {
			return ErrUnavailable
		}
		ready, ok := s.NativeInspector.(NativeContinuationInspector)
		if !ok {
			return ErrUnavailable
		}
		if err := ready.InspectNativeContinuation(ctx, l, r.Binding); err != nil {
			return err
		}
		// The independent probe may cross an original campaign limit.
		now, err := l.account(s.now())
		if err != nil {
			return err
		}
		l.evaluate(now)
		if l.Latched {
			return ErrDenied
		}
		l.Events = append(l.Events, Event{At: now, Request: apiv1.TaskBudgetEvent{Binding: r.Binding, ID: nativeAuditID(nativeOwnerIntent, r.ID), Kind: "activity", AttemptID: r.Operation, WorkerID: owner, Evidence: apiv1.TaskBudgetEvidence{ID: r.ID, Kind: nativeOwnerIntent, Reference: apiv1.NativeControlDigest(r)}}})
		return nil
	})
}

// ConsumeNativeControl is the immutable helper's independent off-pod check.
// The root SA cannot create owner authority: only an exact previously reserved
// human payload is consumable, once. Unknown CAS acknowledgement runs no RPC.
func (s *Service) ConsumeNativeControl(ctx context.Context, r apiv1.NativeControlRequest) (*Ledger, error) {
	if !r.Valid() || r.Operation == "thread/read" || r.Operation == "host/stop" {
		return nil, ErrDenied
	}
	return s.mutate(ctx, r.Binding, func(l *Ledger, now time.Time) error {
		if l.Latched || l.NativeStopRequested() || !l.NativeAdmitted() {
			return ErrDenied
		}
		if l.historyEvents() >= MaxEvents-1 {
			return ErrDenied
		}
		for _, prior := range l.NativeControlRecords() {
			if prior.ID != r.ID {
				continue
			}
			if prior.ConsumedAt != nil || prior.Owner == "" || prior.Epoch != r.Binding.Epoch || prior.Operation != r.Operation || prior.RequestSHA256 != apiv1.NativeControlDigest(r) {
				return ErrDenied
			}
			ready, ok := s.NativeInspector.(NativeContinuationInspector)
			if !ok {
				return ErrUnavailable
			}
			if err := ready.InspectNativeContinuation(ctx, l, r.Binding); err != nil {
				return err
			}
			now, err := l.account(s.now())
			if err != nil {
				return err
			}
			l.evaluate(now)
			if l.Latched {
				return ErrDenied
			}
			l.Events = append(l.Events, Event{At: now, Request: apiv1.TaskBudgetEvent{Binding: r.Binding, ID: nativeAuditID(nativeOwnerConsumed, r.ID), Kind: "activity", AttemptID: r.Operation, WorkerID: prior.Owner, Evidence: apiv1.TaskBudgetEvidence{ID: r.ID, Kind: nativeOwnerConsumed, Reference: apiv1.NativeControlDigest(r)}}})
			return nil
		}
		return ErrDenied
	})
}

// ControlTarget is independently measured; it never trusts a caller Pod/path.
func (i *OwnedRootInspector) ControlTarget(ctx context.Context, l *Ledger) (OwnedRootPolicy, error) {
	proof, p, err := i.observe(ctx, l, l.Binding)
	if err != nil {
		return p, err
	}
	if proof != hostexecutor.EvidenceReady || !l.NativeAdmitted() || l.Latched || l.NativeStopRequested() {
		return p, ErrDenied
	}
	return p, nil
}
