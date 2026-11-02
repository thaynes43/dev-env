package providerallowance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func captureFixture(t *testing.T) []captureEntry {
	t.Helper()
	report := CurrentSourceReport{Schema: CurrentSourceSchema, PublisherRef: CurrentSourcePublisher, ProducerIncarnation: "native-codex-fixture", SourceGeneration: 1, RetrievedAt: "2026-11-02T03:07:00Z", UsedPercent: "6.25", RemainingPercent: "93.75", WindowDurationMinutes: 10080, ResetsAt: "2026-11-09T03:07:00Z", RemainingPercentBasis: CurrentSourceRemainingBasis}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var entries []captureEntry
	for _, p := range []struct {
		provider string
		payload  []byte
	}{
		{"cursor", cursorDisplayDoc(t, map[string]any{"resetDisplayDate": nil, "resetDisplayText": "December 1"})},
		{"anthropic", []byte(`{"rate_limits":{"five_hour":null,"seven_day":{"used_percentage":12.5,"resets_at":null}}}`)},
		{"openai", raw},
	} {
		entries = append(entries, captureEntry{Provider: p.provider, ProducerRef: p.provider + "-capture", ProducerIncarnation: "capture-fixture-1", Sequence: 1, CapturedAt: "2026-11-02T03:07:00Z", ReceivedAt: "2026-11-02T03:07:10Z", Payload: p.payload})
	}
	return entries
}

func captureBytes(t *testing.T, entries []captureEntry) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func captureTrust() AggregateTrust {
	return AggregateTrust{MaxAge: 1800, Registry: NewStaticRegistry(RegistryParts{})}
}
func captureNow() time.Time { return time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC) }

func TestCaptureBundlePreservesThreeSourcesAndUnknownCurrent(t *testing.T) {
	entries := captureFixture(t)
	doc, err := AssembleCaptureBundle(captureBytes(t, entries), captureTrust(), captureNow())
	if err != nil || len(doc.Items) != 3 || len(doc.SourceStatuses) != 3 {
		t.Fatalf("assemble %+v %v", doc, err)
	}
	for _, item := range doc.Items {
		if item.Observation.CapturedAt != entries[0].CapturedAt || item.ReceivedAt != entries[0].ReceivedAt || item.Observation.ProviderObservedAt != nil || item.Binding.State != BindingUnknown || item.Freshness.State != FreshUnknown {
			t.Fatalf("fabricated binding/time %+v", item)
		}
		for _, m := range item.CurrentMeasures {
			if m.State == CurrentKnown || m.State == CurrentUncapped || m.Value.Decimal != nil {
				t.Fatal("unverified current value", m)
			}
		}
		switch item.Observation.Provider {
		case "cursor":
			w := item.Observation.Measures[0].Window
			if w.ResetAt != nil || w.ResetDisplayText == nil || *w.ResetDisplayText != "December 1" || *item.Observation.Measures[0].Value.Decimal != "12.5" {
				t.Fatal("display precision lost")
			}
		case "anthropic":
			if item.Observation.Measures[0].Value.State != ValueUnknown || *item.Observation.Measures[1].Value.Decimal != "12.5" {
				t.Fatal("null window became zero")
			}
		case "openai":
			if len(item.Observation.Measures) != 1 || *item.Observation.Measures[0].Value.Decimal != "6.25" || *item.Observation.Derivations[0].Value.Decimal != "93.75" {
				t.Fatal("Codex direction/derived complement lost")
			}
		}
	}
	later, err := AssembleCaptureBundle(captureBytes(t, entries), captureTrust(), captureNow().Add(time.Hour))
	if err != nil || later.SnapshotRevision != doc.SnapshotRevision {
		t.Fatal("serving clock manufactured a capture", err)
	}
	for i, item := range later.Items {
		if item.Observation.ObservationID != doc.Items[i].Observation.ObservationID || item.ReceivedAt != doc.Items[i].ReceivedAt {
			t.Fatal("reread changed identity/receipt")
		}
	}
}

func TestCaptureBundleCallerTrustCannotSupplyProviderTime(t *testing.T) {
	e := captureFixture(t)[2]
	a, w, min, max := "account-fixture", "workspace-fixture", "0", "100"
	ctx := TrustedContext{Provider: "openai", ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation, BindingState: BindingVerified, AccountRef: &a, WorkspaceRef: &w, SourceKind: SourceNativeRPC, SourceVersion: CodexCaptureSourceVersion}
	reg := NewStaticRegistry(RegistryParts{Sources: []SourceRule{{Provider: "openai", Kind: SourceNativeRPC, Version: CodexCaptureSourceVersion, CapabilityRef: CodexCaptureCapability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: map[string]DirectFieldRule{}}}, Metrics: []MetricRule{{ValidationRef: CodexCaptureValidation, Provider: "openai", SourceKind: SourceNativeRPC, SourceVersion: CodexCaptureSourceVersion, Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{"codex-primary"}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true}}, Accounts: []AccountRule{{AccountRef: a, WorkspaceRef: w, ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation}}})
	doc, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{e}), AggregateTrust{MaxAge: 1800, Contexts: []TrustedContext{ctx}, Registry: reg}, captureNow())
	if err != nil {
		t.Fatal(err)
	}
	if doc.Items[0].Binding.State != BindingVerified || doc.Items[0].Observation.ProviderObservedAt != nil || doc.Items[0].Freshness.State != FreshUnknown || doc.Items[0].CurrentMeasures[0].State != CurrentUnknown {
		t.Fatalf("caller binding invented provider time %+v", doc.Items[0])
	}
	if doc.SourceStatuses[0].LastGood == nil {
		t.Fatal("validated historical capture was not retained")
	}
	if len(doc.SourceStatuses) != 3 || doc.SourceStatuses[1].LastGood != nil || doc.SourceStatuses[2].LastGood != nil {
		t.Fatal("missing providers gained observations")
	}
}

func TestCaptureBundlePassiveWindowsHaveCallerValidatedSemantics(t *testing.T) {
	e := captureFixture(t)[1]
	a, w, min, max := "account-fixture", "workspace-fixture", "0", "100"
	ctx := TrustedContext{Provider: "anthropic", ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation, BindingState: BindingVerified, AccountRef: &a, WorkspaceRef: &w, SourceKind: SourcePassive, SourceVersion: ClaudeCaptureSourceVersion}
	parts := RegistryParts{Sources: []SourceRule{{Provider: "anthropic", Kind: SourcePassive, Version: ClaudeCaptureSourceVersion, CapabilityRef: "claude-statusline:documented-windows", AllowNormalizedMeasures: true, DirectFields: map[string]DirectFieldRule{}}}, Accounts: []AccountRule{{AccountRef: a, WorkspaceRef: w, ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation}}}
	for _, window := range []string{"five_hour", "seven_day"} {
		parts.Metrics = append(parts.Metrics, MetricRule{ValidationRef: ClaudeCaptureValidationPrefix + window + "-used-percent", Provider: "anthropic", SourceKind: SourcePassive, SourceVersion: ClaudeCaptureSourceVersion, Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{window}, Min: &min, Max: &max, HistoricalOnly: true})
	}
	reg := NewStaticRegistry(parts)
	doc, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{e}), AggregateTrust{MaxAge: 1800, Contexts: []TrustedContext{ctx}, Registry: reg}, captureNow())
	if err != nil {
		t.Fatal(err)
	}
	assessment := ValidateObservation(doc.Items[0].Observation, ctx, reg)
	if len(assessment.Issues) != 0 || doc.SourceStatuses[0].LastGood == nil || doc.Items[0].CurrentMeasures[1].Value.Decimal != nil {
		t.Fatalf("passive history cannot be validated without promotion: %+v %+v", assessment, doc)
	}
}

func TestCaptureBundleReplayConflictAndOrdering(t *testing.T) {
	first := captureFixture(t)[2]
	replay := first
	replay.ReceivedAt = "2026-11-02T03:08:10Z"
	doc, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{first, replay}), captureTrust(), captureNow())
	if err != nil || doc.Items[0].ReceivedAt != first.ReceivedAt {
		t.Fatal("replay refreshed original receipt", err)
	}
	conflict := first
	conflict.Payload = json.RawMessage(strings.Replace(string(first.Payload), `"6.25"`, `"7.25"`, 1))
	conflict.Payload = json.RawMessage(strings.Replace(string(conflict.Payload), `"93.75"`, `"92.75"`, 1))
	doc, err = AssembleCaptureBundle(captureBytes(t, []captureEntry{first, conflict}), captureTrust(), captureNow())
	if err != nil || doc.SourceStatuses[0].Availability != AvailabilityConflict {
		t.Fatal("same capture accepted different bytes", err)
	}
	for _, edit := range []func(*captureEntry){func(e *captureEntry) { e.Sequence = 2 }, func(e *captureEntry) { e.ProducerIncarnation = "other" }, func(e *captureEntry) { e.CapturedAt = "2026-11-02T03:06:00Z" }} {
		next := first
		edit(&next)
		if _, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{first, next}), captureTrust(), captureNow()); !errors.Is(err, ErrCaptureBundle) {
			t.Fatal("bad ordering accepted", err)
		}
	}
}

func TestCaptureBundleDistinctCapturesAdvanceWithoutRefreshClaims(t *testing.T) {
	first := captureFixture(t)[2]
	first.CapturedAt = "2026-11-02T03:07:00+00:00"
	first.ReceivedAt = "2026-11-02T03:07:10+00:00"
	first.Payload = json.RawMessage(strings.Replace(string(first.Payload), "2026-11-02T03:07:00Z", first.CapturedAt, 1))
	next := first
	next.Sequence = 2
	next.CapturedAt = "2026-11-02T03:08:00+00:00"
	next.ReceivedAt = "2026-11-02T03:08:10+00:00"
	next.Payload = json.RawMessage(strings.Replace(string(first.Payload), first.CapturedAt, next.CapturedAt, 1))
	next.Payload = json.RawMessage(strings.Replace(string(next.Payload), `"6.25"`, `"7.25"`, 1))
	next.Payload = json.RawMessage(strings.Replace(string(next.Payload), `"93.75"`, `"92.75"`, 1))
	doc, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{first, next}), captureTrust(), captureNow())
	if err != nil {
		t.Fatal(err)
	}
	item := doc.Items[0]
	if item.Observation.Sequence != 2 || item.Observation.CapturedAt != next.CapturedAt || item.ReceivedAt != next.ReceivedAt || *item.Observation.Measures[0].Value.Decimal != "7.25" {
		t.Fatalf("distinct capture did not advance intact: %+v", item)
	}
	if item.Observation.ProviderObservedAt != nil || item.Freshness.State != FreshUnknown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("independent local capture was promoted to provider refresh")
	}
}

func TestCaptureBundleMalformedAndCredentialsAreRedacted(t *testing.T) {
	entries := captureFixture(t)
	for _, edit := range []func(*captureEntry){
		func(e *captureEntry) { e.Provider = "foreign" }, func(e *captureEntry) { e.Sequence = 0 }, func(e *captureEntry) { e.CapturedAt = "2026-11-03T00:00:00Z" }, func(e *captureEntry) { e.ReceivedAt = "2026-11-02T03:06:00Z" },
		func(e *captureEntry) {
			e.Payload = []byte(`{"rate_limits":{"five_hour":null,"seven_day":null},"token":"SECRET-CAPTURE-CANARY"}`)
		},
	} {
		e := entries[1]
		edit(&e)
		_, err := AssembleCaptureBundle(captureBytes(t, []captureEntry{e}), captureTrust(), captureNow())
		if !errors.Is(err, ErrCaptureBundle) || strings.Contains(err.Error(), "SECRET") {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{`{}`, `{"schema":"providerallowance.captures.v1","captures":null}`, `{"schema":"providerallowance.captures.v1","captures":[],"captures":[]}`, `{"schema":"providerallowance.captures.v1","captures":[],"token":"SECRET"}`} {
		if _, err := AssembleCaptureBundle([]byte(raw), captureTrust(), captureNow()); !errors.Is(err, ErrCaptureBundle) {
			t.Fatal("invalid bundle accepted", err)
		}
	}
}
