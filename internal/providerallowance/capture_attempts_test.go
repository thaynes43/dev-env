package providerallowance

import (
	"encoding/json"
	"testing"
	"time"
)

func TestFailedAcquisitionRetainsOriginalCaptureAndLastGoodAcrossAssembly(t *testing.T) {
	used := 61
	raw, trust, received := cursorCLIFixture(t, &used)
	before, err := AssembleCaptureBundle(raw, trust, received)
	if err != nil {
		t.Fatal(err)
	}
	attempt := CaptureAttempt{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", StartedAt: received.Add(time.Hour), CompletedAt: received.Add(time.Hour + time.Second)}
	after, err := AssembleCaptureAttempts(raw, trust, []CaptureAttempt{attempt}, attempt.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(before.Items[0].Observation)
	right, _ := json.Marshal(after.Items[0].Observation)
	if string(left) != string(right) || before.Items[0].ReceivedAt != after.Items[0].ReceivedAt || after.Items[0].CollectionFreshness.State != FreshStale {
		t.Fatal("failed acquisition refreshed original observation/receipt")
	}
	for _, status := range after.SourceStatuses {
		if status.Provider != "cursor" {
			continue
		}
		if status.Availability != AvailabilityUnavailable || status.LastAttempt.Result != AttemptError || status.LastGood == nil || status.LastGood.ReceivedAt != received.Format(time.RFC3339Nano) {
			t.Fatal("failure replaced last-good history")
		}
	}
	// A restart reassembles the same durable original bundle. It does not mint a
	// new sample from the old last-good body or from a later serving clock.
	restarted, err := AssembleCaptureAttempts(raw, trust, []CaptureAttempt{attempt}, attempt.CompletedAt.Add(time.Minute))
	if err != nil || restarted.Items[0].ReceivedAt != after.Items[0].ReceivedAt || restarted.SnapshotRevision != after.SnapshotRevision {
		t.Fatal("restart changed the sample identity", err)
	}
}

func TestCaptureAttemptCannotLabelOldReplayAsNewSuccessfulCheck(t *testing.T) {
	used := 61
	raw, trust, at := cursorCLIFixture(t, &used)
	a := CaptureAttempt{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", StartedAt: at.Add(time.Minute), CompletedAt: at.Add(2 * time.Minute), Success: true}
	if _, err := AssembleCaptureAttempts(raw, trust, []CaptureAttempt{a}, a.CompletedAt); err == nil {
		t.Fatal("old receipt became a successful new check")
	}
	a.StartedAt, a.CompletedAt = at.Add(-time.Second), at
	if _, err := AssembleCaptureAttempts(raw, trust, []CaptureAttempt{a}, at); err != nil {
		t.Fatal("actual successful interval refused", err)
	}
	for _, variant := range []string{"foreign-producer", "foreign-incarnation", "future", "negative-interval", "unbounded", "duplicate"} {
		b := a
		switch variant {
		case "foreign-producer":
			b.ProducerRef = "foreign"
		case "foreign-incarnation":
			b.ProducerIncarnation = "foreign"
		case "future":
			b.CompletedAt = at.Add(time.Second)
		case "negative-interval":
			b.StartedAt = at.Add(time.Second)
		case "unbounded":
			b.StartedAt = at.Add(-3 * time.Minute)
		}
		attempts := []CaptureAttempt{b}
		if variant == "duplicate" {
			attempts = append(attempts, b)
		}
		if _, err := AssembleCaptureAttempts(raw, trust, attempts, at); err == nil {
			t.Fatal("invalid attempt accepted", variant)
		}
	}
}
