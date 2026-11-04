package providerallowance

import (
	"encoding/json"
	"testing"
	"time"
)

// A failed attempt for a provider that has no retained capture goes through
// AssembleCaptureAttempts, not statusFor. Its overlay records the attempt but must
// not leave the unconfigured freshness reason beside a real failed attempt.
func TestFailedAttemptWithoutCaptureDoesNotClaimSourceNotConfigured(t *testing.T) {
	used := 61
	_, trust, received := cursorCLIFixture(t, &used)
	empty, err := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": []captureEntry{}})
	if err != nil {
		t.Fatal(err)
	}
	attempt := CaptureAttempt{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", StartedAt: received, CompletedAt: received.Add(time.Second)}
	doc, err := AssembleCaptureAttempts(empty, trust, []CaptureAttempt{attempt}, attempt.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	var status *SourceStatus
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "cursor" {
			status = &doc.SourceStatuses[i]
		}
	}
	if status == nil || status.LastAttempt.Result != AttemptError || status.Availability != AvailabilityUnavailable {
		t.Fatal("failed attempt not recorded", status)
	}
	if status.Freshness.Reason != "acquisition failed" || status.Freshness.State != FreshUnknown {
		t.Fatal("failed attempt kept a stale freshness reason: " + status.Freshness.Reason)
	}
	if status.Freshness.Basis != BasisNone || status.Freshness.AsOf != attempt.CompletedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatal("failed attempt freshness must have basis none and asOf at attempt completion", status.Freshness)
	}
}

// A held capture followed by a later failed attempt keeps the freshness and last-good
// the held observation already carries; only the unconfigured default is replaced.
func TestFailedAttemptAfterHeldCaptureKeepsFreshnessAndLastGood(t *testing.T) {
	used := 61
	raw, trust, received := cursorCLIFixture(t, &used)
	before, err := AssembleCaptureBundle(raw, trust, received.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	attempt := CaptureAttempt{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", StartedAt: received.Add(time.Hour), CompletedAt: received.Add(time.Hour + time.Second)}
	after, err := AssembleCaptureAttempts(raw, trust, []CaptureAttempt{attempt}, attempt.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	pick := func(doc ReadResponse) SourceStatus {
		for _, status := range doc.SourceStatuses {
			if status.Provider == "cursor" {
				return status
			}
		}
		t.Fatal("no cursor status")
		return SourceStatus{}
	}
	left, right := pick(before), pick(after)
	if right.LastAttempt.Result != AttemptError || right.LastGood == nil || left.LastGood == nil || *right.LastGood != *left.LastGood {
		t.Fatal("failed attempt replaced last-good history", right)
	}
	if right.Freshness != left.Freshness || right.Freshness.Reason == "acquisition failed" || right.Freshness.Reason == "source not configured" {
		t.Fatal("failed attempt rewrote the held observation's freshness", left.Freshness, right.Freshness)
	}
}
