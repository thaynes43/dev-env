package homeretention

import (
	"testing"
)

// This finite fixture represents exact payloads already authenticated by a
// caller's transport/remote checks. Snapshot fields cannot populate this map.
type authenticatedFixture map[string]Authority

func (a authenticatedFixture) Verify(domain string, payload any, _ Signature) (Authority, bool) {
	b, err := SigningBytes(domain, payload)
	if err != nil {
		return Authority{}, false
	}
	scope, ok := a[string(b)]
	return scope, ok
}

func TestExistingAuthenticatedBoundaryNeedsNoSigningInfrastructure(t *testing.T) {
	f := newFixture(t)
	authenticated := authenticatedFixture{}
	register := func(domain string, payload any, scope Authority) {
		b, err := SigningBytes(domain, payload)
		if err != nil {
			t.Fatal(err)
		}
		authenticated[string(b)] = scope
	}
	for i := range f.record.Proof.Payload.Inventory.Repositories {
		r := &f.record.Proof.Payload.Inventory.Repositories[i]
		for j := range r.Refs {
			for k := range r.Refs[j].Receipts {
				p := &r.Refs[j].Receipts[k]
				p.Signature = Signature{}
				register("publication", p.Payload, Authority{Repository: r.Identity, Remote: r.Identity})
			}
		}
	}
	for i := range f.record.Proof.Payload.Inventory.Artifacts {
		for j := range f.record.Proof.Payload.Inventory.Artifacts[i].Receipts {
			p := &f.record.Proof.Payload.Inventory.Artifacts[i].Receipts[j]
			p.Signature = Signature{}
			register("durable-copy", p.Payload, Authority{Destination: "retained:results"})
		}
	}
	f.record.Proof.Signature, f.record.Observation.Signature = Signature{}, Signature{}
	register("proof", f.record.Proof.Payload, Authority{})
	register("observation", f.record.Observation.Payload, Authority{})
	report, err := Evaluate(Snapshot{Version: Version, Records: []Record{f.record}}, f.now, DefaultPolicy(), authenticated)
	if err != nil || len(report.ImmediateCandidates) != 1 {
		t.Fatalf("authenticated caller boundary refused: %+v %v", report, err)
	}
	// The same unsigned file cannot authenticate itself through the offline API.
	report, err = Evaluate(Snapshot{Version: Version, Records: []Record{f.record}}, f.now, DefaultPolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	requireReason(t, report, "proof_signature_untrusted")
}

func TestStoppedSpecialWorkRequiresDispositionAtDeadline(t *testing.T) {
	for _, marker := range []string{"canary", "pilot", "fixture", "rescue_hold", "owner_hold"} {
		for _, clockless := range []bool{false, true} {
			f := newFixture(t)
			if marker == "owner_hold" {
				f.record.Hold = "owner-review"
			} else {
				f.record.Disposition = marker
			}
			f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-Retention))
			if clockless {
				f.record.TaskCompletion = nil
			}
			o := &f.record.Observation.Payload
			o.Hold, o.Disposition = f.record.Hold, f.record.Disposition
			f.resign(t)
			e := requireReason(t, f.evaluate(t), "owner_disposition_required")
			if e.Status != "protected" || !e.DecisionRequired || !e.DispositionDecisionRequired {
				t.Fatalf("special stopped work hidden: %+v", e)
			}
			f.record.State = "active"
			f.record.Observation.Payload.State = "active"
			f.resign(t)
			e = requireReason(t, f.evaluate(t), "active_session")
			if e.Status != "protected" || e.DecisionRequired || e.DispositionDecisionRequired {
				t.Fatalf("active work got expiry action: %+v", e)
			}
		}
	}
}

func TestUnverifiedActivityCannotHideExpiredWork(t *testing.T) {
	f := newFixture(t)
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-Retention))
	f.record.State = "active"
	e := requireReason(t, f.evaluate(t), "protection_unverified")
	if e.Status != "protected" || !e.DecisionRequired || e.DispositionDecisionRequired {
		t.Fatalf("unverified activity hid owner verification decision: %+v", e)
	}
}

func TestUnsignedClockIsExplicitAuditEvidence(t *testing.T) {
	f := newFixture(t)
	f.record.Proof = nil
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-Retention))
	e := requireReason(t, f.evaluate(t), "completion_clock_unverified")
	if !e.DecisionRequired {
		t.Fatal("expired recorded clock lost its decision")
	}
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-1))
	e = requireReason(t, f.evaluate(t), "completion_clock_unverified")
	if e.CompletedAt == "" || e.Status != "retained" {
		t.Fatal("recorded unsigned age was erased instead of marked unverified")
	}
}
