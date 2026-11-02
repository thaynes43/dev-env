package homeretention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

const (
	// OwnerDispositionPending is the only disposition a projection may assign.
	OwnerDispositionPending = "pending"
	// OwnerDispositionRetain keeps the HOME past this projection.
	OwnerDispositionRetain = "retain"
	// OwnerDispositionApproveExactCleanup records an exact-UID approval. It is
	// not deletion authority and is not copied onto a new binding.
	OwnerDispositionApproveExactCleanup = "approve_exact_cleanup"
	// OwnerDispositionWithdraw removes a previous explicit decision.
	OwnerDispositionWithdraw = "withdraw"
)

var (
	// ErrDecisionConflict means the caller lost a compare-and-swap.
	ErrDecisionConflict = errors.New("decision store revision conflict")
	// ErrDecisionNotFound means no open record exists for the exact binding.
	ErrDecisionNotFound = errors.New("open decision record not found")
)

// DecisionRecord is one durable warning or owner-decision row. StoreRevision
// is the caller's compare-and-swap token. BindingDigest identifies the exact
// executor and HOME revision. A different digest starts a new row.
type DecisionRecord struct {
	ID                          string      `json:"id"`
	UID                         string      `json:"uid"`
	Binding                     Binding     `json:"binding"`
	BindingDigest               string      `json:"bindingDigest"`
	HomeRevision                string      `json:"homeRevision"`
	Provenance                  *Provenance `json:"provenance,omitempty"`
	State                       string      `json:"state"`
	Hold                        string      `json:"hold"`
	RecordDisposition           string      `json:"recordDisposition"`
	Status                      string      `json:"status"`
	ClockSource                 string      `json:"clockSource"`
	CompletedAt                 string      `json:"completedAt,omitempty"`
	WarningAt                   string      `json:"warningAt,omitempty"`
	Deadline                    string      `json:"deadline,omitempty"`
	Retention                   string      `json:"retention"`
	WarningLead                 string      `json:"warningLead"`
	DecisionRequired            bool        `json:"decisionRequired"`
	DispositionDecisionRequired bool        `json:"dispositionDecisionRequired"`
	Reasons                     []string    `json:"reasons"`
	OwnerDisposition            string      `json:"ownerDisposition"`
	OwnerDispositionAt          string      `json:"ownerDispositionAt,omitempty"`
	OpenedAt                    string      `json:"openedAt"`
	UpdatedAt                   string      `json:"updatedAt"`
	SupersedesID                string      `json:"supersedesID,omitempty"`
	SupersededByID              string      `json:"supersededByID,omitempty"`
	Superseded                  bool        `json:"superseded"`
	StoreRevision               string      `json:"storeRevision"`
	ProjectionDigest            string      `json:"projectionDigest"`
}

// DecisionMutation is one compare-and-swap write. An empty ExpectedRevision
// creates a record. Apply is atomic for the whole slice.
type DecisionMutation struct {
	Record           DecisionRecord
	ExpectedRevision string
}

// DecisionStore is supplied by the caller. This package does not open a
// service, send a notification, or run a background loop.
type DecisionStore interface {
	List(ctx context.Context) ([]DecisionRecord, error)
	Apply(ctx context.Context, mutations []DecisionMutation) error
}

type decisionView struct {
	UID                         string
	BindingDigest               string
	State                       string
	Hold                        string
	RecordDisposition           string
	Status                      string
	ClockSource                 string
	CompletedAt                 string
	WarningAt                   string
	Deadline                    string
	Retention                   string
	WarningLead                 string
	DecisionRequired            bool
	DispositionDecisionRequired bool
	Reasons                     []string
	Provenance                  *Provenance
}

// ProjectDecisions evaluates the snapshot at the caller's clock and writes
// warning, deadline, and decision rows. The same binding and projection is a
// resume and does not write again. A binding or HOME revision change
// supersedes the old row and starts at pending; the old owner disposition is
// kept on the superseded row and is not copied forward.
func ProjectDecisions(ctx context.Context, store DecisionStore, snapshot Snapshot, now time.Time, policy Policy, trust EvidenceVerifier) ([]DecisionRecord, error) {
	if ctx == nil || store == nil {
		return nil, errors.New("decision store and context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report, err := Evaluate(snapshot, now, policy, trust)
	if err != nil {
		return nil, err
	}
	byUID := map[string]Entry{}
	for _, entry := range report.ImmediateCandidates {
		byUID[entry.UID] = entry
	}
	for _, entry := range report.StoppedWorkQueue {
		byUID[entry.UID] = entry
	}
	open, err := openByUID(ctx, store)
	if err != nil {
		return nil, err
	}
	records := append([]Record(nil), snapshot.Records...)
	sort.Slice(records, func(i, j int) bool { return records[i].UID < records[j].UID })
	var mutations []DecisionMutation
	for _, record := range records {
		entry, ok := byUID[record.UID]
		if !ok {
			return nil, errors.New("decision entry missing")
		}
		digest, err := bindingDigest(record.Binding)
		if err != nil {
			return nil, err
		}
		id, err := decisionID(record.UID, digest)
		if err != nil {
			return nil, err
		}
		view := decisionView{
			UID: record.UID, BindingDigest: digest, State: record.State, Hold: record.Hold, RecordDisposition: record.Disposition,
			Status: entry.Status, ClockSource: entry.ClockSource, CompletedAt: entry.CompletedAt, WarningAt: entry.WarningAt, Deadline: entry.Deadline,
			Retention: Retention.String(), WarningLead: policy.WarningLead.String(), DecisionRequired: entry.DecisionRequired,
			DispositionDecisionRequired: entry.DispositionDecisionRequired, Reasons: append([]string(nil), entry.Reasons...), Provenance: cloneProvenance(entry.Provenance),
		}
		projection, err := projectionDigest(view)
		if err != nil {
			return nil, err
		}
		current, exists := open[record.UID]
		if exists && current.BindingDigest == digest {
			if current.ID != id {
				return nil, errors.New("decision identity does not match its binding")
			}
			if current.ProjectionDigest == projection && current.OwnerDisposition != "" {
				continue
			}
			updated := current
			fillDecision(&updated, record, view, projection, formatTime(now))
			mutations = append(mutations, DecisionMutation{Record: updated, ExpectedRevision: current.StoreRevision})
			continue
		}
		created := DecisionRecord{ID: id, UID: record.UID, OwnerDisposition: OwnerDispositionPending, OpenedAt: formatTime(now)}
		fillDecision(&created, record, view, projection, formatTime(now))
		if exists {
			if current.Binding == record.Binding {
				return nil, errors.New("decision identity changed without a binding change")
			}
			created.SupersedesID = current.ID
			previous := current
			previous.Superseded = true
			previous.SupersededByID = created.ID
			previous.UpdatedAt = formatTime(now)
			mutations = append(mutations, DecisionMutation{Record: created, ExpectedRevision: ""}, DecisionMutation{Record: previous, ExpectedRevision: current.StoreRevision})
			continue
		}
		mutations = append(mutations, DecisionMutation{Record: created, ExpectedRevision: ""})
	}
	if len(mutations) > 0 {
		if err := store.Apply(ctx, mutations); err != nil {
			return nil, err
		}
	}
	open, err = openByUID(ctx, store)
	if err != nil {
		return nil, err
	}
	result := make([]DecisionRecord, 0, len(open))
	for _, source := range records {
		record, ok := open[source.UID]
		if !ok {
			return nil, errors.New("open decision missing after projection")
		}
		result = append(result, record)
	}
	return result, nil
}

// RecordOwnerDecision attaches an explicit disposition to the open row for
// this exact binding. A superseded row, a different revision, or an unknown
// disposition is refused and does not change the store.
func RecordOwnerDecision(ctx context.Context, store DecisionStore, uid string, binding Binding, disposition string, now time.Time) (DecisionRecord, error) {
	switch disposition {
	case OwnerDispositionRetain, OwnerDispositionApproveExactCleanup, OwnerDispositionWithdraw:
	default:
		return DecisionRecord{}, errors.New("owner disposition is not explicit")
	}
	if ctx == nil || store == nil || now.IsZero() || !validText(uid) {
		return DecisionRecord{}, errors.New("owner decision requires exact identity and time")
	}
	if err := ctx.Err(); err != nil {
		return DecisionRecord{}, err
	}
	open, err := openByUID(ctx, store)
	if err != nil {
		return DecisionRecord{}, err
	}
	current, ok := open[uid]
	if !ok {
		return DecisionRecord{}, ErrDecisionNotFound
	}
	if current.UID != uid || current.Binding != binding {
		return DecisionRecord{}, errors.New("owner decision is not bound to the open record")
	}
	updated := current
	updated.OwnerDisposition = disposition
	updated.OwnerDispositionAt = formatTime(now)
	updated.UpdatedAt = formatTime(now)
	if err := store.Apply(ctx, []DecisionMutation{{Record: updated, ExpectedRevision: current.StoreRevision}}); err != nil {
		return DecisionRecord{}, err
	}
	open, err = openByUID(ctx, store)
	if err != nil {
		return DecisionRecord{}, err
	}
	recorded, ok := open[uid]
	if !ok || recorded.Binding != binding || recorded.OwnerDisposition != disposition {
		return DecisionRecord{}, errors.New("owner decision was not retained")
	}
	return recorded, nil
}

func openByUID(ctx context.Context, store DecisionStore) (map[string]DecisionRecord, error) {
	listed, err := store.List(ctx)
	if err != nil {
		return nil, err
	}
	open := map[string]DecisionRecord{}
	for _, record := range listed {
		if record.Superseded {
			continue
		}
		if _, exists := open[record.UID]; exists {
			return nil, errors.New("ambiguous open decision")
		}
		open[record.UID] = record
	}
	return open, nil
}

func fillDecision(record *DecisionRecord, source Record, view decisionView, projection, now string) {
	record.Binding = source.Binding
	record.BindingDigest = view.BindingDigest
	record.HomeRevision = source.Binding.HomeRevision
	record.Provenance = cloneProvenance(view.Provenance)
	record.State = view.State
	record.Hold = view.Hold
	record.RecordDisposition = view.RecordDisposition
	record.Status = view.Status
	record.ClockSource = view.ClockSource
	record.CompletedAt = view.CompletedAt
	record.WarningAt = view.WarningAt
	record.Deadline = view.Deadline
	record.Retention = view.Retention
	record.WarningLead = view.WarningLead
	record.DecisionRequired = view.DecisionRequired
	record.DispositionDecisionRequired = view.DispositionDecisionRequired
	record.Reasons = append([]string(nil), view.Reasons...)
	record.ProjectionDigest = projection
	record.UpdatedAt = now
}

func bindingDigest(binding Binding) (string, error) {
	raw, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func decisionID(uid, digest string) (string, error) {
	sum := sha256.Sum256([]byte(uid + "\n" + digest))
	return "decision-" + hex.EncodeToString(sum[:16]), nil
}

func projectionDigest(view decisionView) (string, error) {
	raw, err := json.Marshal(view)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func cloneProvenance(in *Provenance) *Provenance {
	if in == nil {
		return nil
	}
	out := *in
	if in.Exceptions != nil {
		out.Exceptions = append([]string(nil), in.Exceptions...)
	}
	return &out
}
