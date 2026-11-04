package providerallowance

import (
	"encoding/json"
	"time"
)

// CaptureAttempt describes an actual bounded acquisition by a selected producer.
// It is separate from retained captures: a failed check never refreshes LastGood.
// Success may only refer to an observation received inside this attempt interval.
type CaptureAttempt struct {
	Provider            string
	ProducerRef         string
	ProducerIncarnation string
	StartedAt           time.Time
	CompletedAt         time.Time
	Success             bool
}

// AssembleCaptureAttempts is pure local assembly for a caller-owned collector.
// It retains the original bounded capture bundle and overlays only actual attempt
// outcomes. It neither registers trust nor performs acquisition/publication.
func AssembleCaptureAttempts(raw []byte, trust AggregateTrust, attempts []CaptureAttempt, now time.Time) (ReadResponse, error) {
	fail := func() (ReadResponse, error) { return ReadResponse{}, ErrCaptureBundle }
	if len(attempts) == 0 || len(attempts) > 8 {
		return fail()
	}
	doc, err := AssembleCaptureBundle(raw, trust, now)
	if err != nil {
		return fail()
	}
	// Assembly may retain an older usable observation when the newest envelope
	// is empty. Check successful attempt chronology against the actual latest
	// supplied capture, never against that retained historical receipt.
	var supplied struct {
		Captures []captureEntry `json:"captures"`
	}
	if json.Unmarshal(raw, &supplied) != nil {
		return fail()
	}
	latest := map[string]captureEntry{}
	for _, capture := range supplied.Captures {
		latest[capture.Provider] = capture
	}
	seen := map[string]bool{}
	for _, attempt := range attempts {
		if seen[attempt.Provider] || attempt.StartedAt.IsZero() || attempt.CompletedAt.Before(attempt.StartedAt) || attempt.CompletedAt.After(now) || attempt.CompletedAt.Sub(attempt.StartedAt) > 2*time.Minute {
			return fail()
		}
		seen[attempt.Provider] = true
		matched := false
		for _, ctx := range trust.Contexts {
			if ctx.Provider == attempt.Provider && ctx.ProducerRef == attempt.ProducerRef && ctx.ProducerIncarnation == attempt.ProducerIncarnation {
				matched = true
			}
		}
		if !matched {
			return fail()
		}
		var held *ReadItem
		for i := range doc.Items {
			if doc.Items[i].Observation.Provider == attempt.Provider {
				held = &doc.Items[i]
			}
		}
		if attempt.Success {
			if held == nil {
				return fail()
			}
			capture, ok := latest[attempt.Provider]
			received, e := time.Parse(time.RFC3339Nano, capture.ReceivedAt)
			if !ok || capture.ProducerRef != attempt.ProducerRef || capture.ProducerIncarnation != attempt.ProducerIncarnation || e != nil || received.Before(attempt.StartedAt) || received.After(attempt.CompletedAt) {
				return fail()
			}
		}
		var status *SourceStatus
		for i := range doc.SourceStatuses {
			if doc.SourceStatuses[i].Provider == attempt.Provider {
				status = &doc.SourceStatuses[i]
			}
		}
		if status == nil {
			return fail()
		}
		availability := status.Availability
		status.ProducerRef = attempt.ProducerRef
		started, completed := attempt.StartedAt.UTC().Format(time.RFC3339Nano), attempt.CompletedAt.UTC().Format(time.RFC3339Nano)
		status.LastAttempt = LastAttempt{AttemptedAt: started, CompletedAt: &completed, Result: AttemptError, Code: codePtr(codeAcquisitionUnavailable)}
		status.Availability = AvailabilityUnavailable
		if status.Freshness.Reason == reasonUnconfigured {
			// A real failed attempt is not an unconfigured source. Only the freshness
			// default for a provider with no retained capture is replaced; a held
			// observation keeps the freshness it already carries.
			status.Freshness = Freshness{State: FreshUnknown, AsOf: completed, Basis: BasisNone, Reason: reasonAttempt}
		}
		if attempt.Success {
			status.LastAttempt.Result, status.LastAttempt.Code = AttemptSuccess, nil
			status.Availability = availability
		}
	}
	doc.SnapshotRevision = collectionRevision(doc.Items, doc.SourceStatuses)
	body, err := json.Marshal(doc)
	if err != nil {
		return fail()
	}
	if _, err := DecodeReadResponse(body, FixtureParseLimits); err != nil {
		return fail()
	}
	return doc, nil
}
