package homeretention

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDecisionProjectionWarningDedupAndSupersession(t *testing.T) {
	f := newFixture(t)
	f.record.Provenance = &Provenance{Source: "synthetic-decision", SnapshotSHA256: strings.Repeat("ab", 32), CapturedAt: formatTime(f.now), BindingStatus: "exact"}
	store := &memDecisions{}
	snapshot := Snapshot{Version: Version, Records: []Record{f.record}}
	first, err := ProjectDecisions(context.Background(), store, snapshot, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].OwnerDisposition != OwnerDispositionPending || first[0].Status != "immediate_preserved" || first[0].Retention != Retention.String() || first[0].WarningLead != DefaultWarningLead.String() {
		t.Fatalf("projection invented an approval: %+v", first)
	}
	if first[0].Binding != f.record.Binding || first[0].HomeRevision != f.record.Binding.HomeRevision || first[0].Provenance == nil || first[0].Provenance.Source != "synthetic-decision" {
		t.Fatalf("binding or provenance was not exact: %+v", first[0])
	}
	again, err := ProjectDecisions(context.Background(), store, snapshot, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if store.applies != 1 || again[0].ID != first[0].ID || again[0].StoreRevision != first[0].StoreRevision {
		t.Fatalf("resume wrote again: applies=%d %+v", store.applies, again)
	}
	if _, err := RecordOwnerDecision(context.Background(), store, f.record.UID, f.record.Binding, "approve", f.now); err == nil {
		t.Fatal("inexplicit disposition was stored")
	}
	other := f.record.Binding
	other.HomeRevision = "revision-other"
	if _, err := RecordOwnerDecision(context.Background(), store, f.record.UID, other, OwnerDispositionApproveExactCleanup, f.now); err == nil {
		t.Fatal("foreign revision inherited a decision")
	}
	retained, err := RecordOwnerDecision(context.Background(), store, f.record.UID, f.record.Binding, OwnerDispositionRetain, f.now)
	if err != nil || retained.OwnerDisposition != OwnerDispositionRetain {
		t.Fatal(err)
	}
	warningRecord := f.record
	warningRecord.TaskCompletion = &Completion{Executor: f.record.Binding.ExecutorBinding, FinishedAt: formatTime(f.now.Add(-Retention + DefaultWarningLead))}
	warningSnapshot := Snapshot{Version: Version, Records: []Record{warningRecord}}
	warned, err := ProjectDecisions(context.Background(), store, warningSnapshot, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	warningAt, err1 := time.Parse(time.RFC3339Nano, warned[0].WarningAt)
	deadline, err2 := time.Parse(time.RFC3339Nano, warned[0].Deadline)
	completed, err3 := time.Parse(time.RFC3339Nano, warned[0].CompletedAt)
	if err1 != nil || err2 != nil || err3 != nil || warned[0].Status != "warning" || !warningAt.Equal(f.now) || !deadline.Equal(completed.Add(Retention)) || !warningAt.Equal(deadline.Add(-DefaultWarningLead)) || warned[0].OwnerDisposition != OwnerDispositionRetain {
		t.Fatalf("24h/168h projection: %+v %v %v %v", warned[0], err1, err2, err3)
	}
	expiredRecord := warningRecord
	expiredRecord.TaskCompletion = &Completion{Executor: f.record.Binding.ExecutorBinding, FinishedAt: formatTime(f.now.Add(-Retention))}
	expired, err := ProjectDecisions(context.Background(), store, Snapshot{Version: Version, Records: []Record{expiredRecord}}, f.now, DefaultPolicy(), f.trust)
	if err != nil || expired[0].Status != "decision_required" || !expired[0].DecisionRequired || expired[0].OwnerDisposition != OwnerDispositionRetain {
		t.Fatalf("168h equality: %+v %v", expired, err)
	}
	moved := expiredRecord
	moved.Binding.HomeRevision = "revision-8"
	moved.Binding.WriterGeneration = 2
	superseded, err := ProjectDecisions(context.Background(), store, Snapshot{Version: Version, Records: []Record{moved}}, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if superseded[0].OwnerDisposition != OwnerDispositionPending || superseded[0].SupersedesID != retained.ID || superseded[0].Binding != moved.Binding {
		t.Fatalf("new revision inherited the old decision: %+v", superseded[0])
	}
	var old DecisionRecord
	for _, record := range store.records {
		if record.ID == retained.ID {
			old = record
		}
	}
	if !old.Superseded || old.OwnerDisposition != OwnerDispositionRetain || old.SupersededByID != superseded[0].ID {
		t.Fatalf("old decision was rewritten: %+v", old)
	}
}

type memDecisions struct {
	records []DecisionRecord
	seq     int
	applies int
}

func (m *memDecisions) List(ctx context.Context) ([]DecisionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]DecisionRecord, 0, len(m.records))
	for _, record := range m.records {
		out = append(out, cloneDecision(record))
	}
	return out, nil
}

func (m *memDecisions) Apply(ctx context.Context, mutations []DecisionMutation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	next := make([]DecisionRecord, 0, len(m.records))
	for _, record := range m.records {
		next = append(next, cloneDecision(record))
	}
	seq := m.seq
	for _, mutation := range mutations {
		if mutation.Record.ID == "" {
			return errors.New("decision id missing")
		}
		found := -1
		for i := range next {
			if next[i].ID == mutation.Record.ID {
				found = i
				break
			}
		}
		if mutation.ExpectedRevision == "" {
			if found >= 0 {
				return ErrDecisionConflict
			}
			seq++
			record := cloneDecision(mutation.Record)
			record.StoreRevision = strconv.Itoa(seq)
			next = append(next, record)
			continue
		}
		if found < 0 {
			return ErrDecisionNotFound
		}
		if next[found].StoreRevision != mutation.ExpectedRevision {
			return ErrDecisionConflict
		}
		seq++
		record := cloneDecision(mutation.Record)
		record.StoreRevision = strconv.Itoa(seq)
		next[found] = record
	}
	m.records, m.seq = next, seq
	m.applies++
	return nil
}

func cloneDecision(in DecisionRecord) DecisionRecord {
	out := in
	if in.Reasons != nil {
		out.Reasons = append([]string(nil), in.Reasons...)
	}
	out.Provenance = cloneProvenance(in.Provenance)
	return out
}
