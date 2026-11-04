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
}
