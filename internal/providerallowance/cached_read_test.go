package providerallowance

import (
	"strings"
	"testing"
	"time"
)

func TestProjectCachedReadDegradesFutureStatusWithoutMovingAsOf(t *testing.T) {
	asOf := "2099-01-01T00:00:00Z"
	doc := ReadResponse{
		Schema: SchemaRead, SnapshotRevision: "cached", ServedAt: "2026-11-02T00:00:00Z",
		Items: []ReadItem{},
		SourceStatuses: []SourceStatus{{
			Provider: "openai", Availability: AvailabilityUnconfigured,
			Freshness: Freshness{State: FreshFresh, AsOf: asOf, Basis: BasisProvider},
		}},
	}
	ProjectCachedRead(&doc, nil, nil, time.Hour, true, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))
	if doc.SourceStatuses[0].Freshness.State != FreshExpired || doc.SourceStatuses[0].Freshness.AsOf != asOf {
		t.Fatalf("status %+v", doc.SourceStatuses[0].Freshness)
	}
	paged := doc
	cursor := "next"
	paged.NextCursor = &cursor
	before := paged.SourceStatuses[0].Freshness.State
	ProjectCachedRead(&paged, nil, nil, time.Hour, true, time.Time{})
	if paged.SourceStatuses[0].Freshness.State != before || paged.NextCursor == nil || *paged.NextCursor != "next" {
		t.Fatal("paged document was rewritten")
	}
	if strings.Contains(asOf, "secret") {
		t.Fatal("fixture")
	}
}

func TestProjectCachedReadDegradesFutureReceiptWithoutRewritingHistory(t *testing.T) {
	obs, ctx, reg := repairAllowanceFixture(t)
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	observed := *obs.ProviderObservedAt
	observedAt, err := time.Parse(time.RFC3339Nano, observed)
	if err != nil || observedAt.After(now) {
		t.Fatal("provider time is after the serving clock")
	}
	future := "2099-01-01T00:00:00Z"
	reset := "2099-06-01T00:00:00Z"
	obs.Measures[0].Window.ResetAt = &reset
	planted := "77"
	doc := repairRead(obs, "2026-11-02T03:00:00Z", future, observed, CurrentMeasure{
		MeasureID: "window", State: CurrentUncapped, Value: MeasureValue{State: ValueUncapped, Decimal: &planted},
	})
	doc.SourceStatuses[0].Binding = BindingRecord{State: BindingVerified, Generation: 9, AccountRef: repairStr("other-account"), WorkspaceRef: repairStr("other-workspace")}
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, now)
	item := doc.Items[0]
	if item.ReceivedAt != future || item.Observation.CapturedAt != "2026-11-02T03:00:00Z" || item.Observation.ProviderObservedAt == nil || *item.Observation.ProviderObservedAt != observed {
		t.Fatalf("original times moved received=%s captured=%s observed=%v", item.ReceivedAt, item.Observation.CapturedAt, item.Observation.ProviderObservedAt)
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "55" || item.Observation.Measures[0].Window.ResetAt == nil || *item.Observation.Measures[0].Window.ResetAt != reset {
		t.Fatal("raw observation was rewritten")
	}
	if item.Freshness.State == FreshFresh || item.Freshness.State == FreshStale || item.Freshness.AsOf != observed || item.Freshness.Basis != BasisProvider {
		t.Fatalf("future receipt stayed current %+v", item.Freshness)
	}
	if item.CurrentMeasures[0].State != CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil || item.CurrentMeasures[0].Value.State != ValueUnknown {
		t.Fatalf("uncapped current remained %+v", item.CurrentMeasures[0])
	}
	status := doc.SourceStatuses[0]
	if status.Binding.State != BindingVerified || status.Binding.Generation != 0 || status.Binding.AccountRef == nil || *status.Binding.AccountRef != "fixture-account" || status.Binding.WorkspaceRef == nil || *status.Binding.WorkspaceRef != "fixture-workspace" || status.ProducerRef != "fixture-producer" {
		t.Fatalf("status binding stayed payload authored %+v producer=%s", status.Binding, status.ProducerRef)
	}
}

func TestProjectCachedReadClearsUncappedWhenResetPasses(t *testing.T) {
	obs, ctx, reg := repairAllowanceFixture(t)
	reset := "2026-11-02T03:00:00Z"
	obs.Measures[0].Window.ResetAt = &reset
	planted := "77"
	observed := *obs.ProviderObservedAt
	doc := repairRead(obs, obs.CapturedAt, "2026-11-02T03:05:00Z", observed, CurrentMeasure{
		MeasureID: "window", State: CurrentUncapped, Value: MeasureValue{State: ValueUncapped, Decimal: &planted},
	})
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	item := doc.Items[0]
	if item.Freshness.State != FreshFresh || item.Freshness.AsOf != observed {
		t.Fatalf("fresh view was expired %+v", item.Freshness)
	}
	if item.CurrentMeasures[0].State != CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("uncapped survived the reset %+v", item.CurrentMeasures[0])
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "55" || item.Observation.Measures[0].Window.ResetAt == nil || *item.Observation.Measures[0].Window.ResetAt != reset {
		t.Fatal("observation history moved")
	}
}

func TestProjectCachedReadDerivesStatusAndKeepsTrustedCurrent(t *testing.T) {
	obs, ctx, reg := repairAllowanceFixture(t)
	observed := *obs.ProviderObservedAt
	doc := repairRead(obs, obs.CapturedAt, "2026-11-02T03:05:00Z", observed, CurrentMeasure{
		MeasureID: "window", State: CurrentKnown, Value: MeasureValue{State: ValueKnown, Decimal: repairStr("55")},
	})
	doc.SourceStatuses[0].Binding = BindingRecord{State: BindingVerified, Generation: 3, AccountRef: repairStr("other-account"), WorkspaceRef: repairStr("other-workspace")}
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	item := doc.Items[0]
	if item.Freshness.State != FreshFresh || item.CurrentMeasures[0].State != CurrentKnown || item.CurrentMeasures[0].Value.Decimal == nil || *item.CurrentMeasures[0].Value.Decimal != "55" {
		t.Fatalf("trusted current was withheld %+v %+v", item.Freshness, item.CurrentMeasures[0])
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "55" || item.ReceivedAt != "2026-11-02T03:05:00Z" {
		t.Fatal("trusted history moved")
	}
	status := doc.SourceStatuses[0]
	if status.Binding.State != BindingVerified || status.Binding.Generation != 0 || status.Binding.AccountRef == nil || *status.Binding.AccountRef != "fixture-account" {
		t.Fatalf("foreign status account remained %+v", status.Binding)
	}
}

func TestProjectCachedReadWithholdsUntrustedCurrentDecimal(t *testing.T) {
	decimal := "55"
	doc := ReadResponse{
		Schema: SchemaRead, SnapshotRevision: "cached", ServedAt: "2026-11-02T03:10:00Z",
		Items: []ReadItem{{
			ReceivedAt: "2026-11-02T03:05:00Z",
			Observation: Observation{
				Provider: "openai",
				Measures: []Measure{{ID: "window", Value: MeasureValue{State: ValueKnown, Decimal: &decimal}}},
			},
			Freshness:       Freshness{State: FreshFresh, AsOf: "2026-11-02T02:59:00Z", Basis: BasisProvider},
			CurrentMeasures: []CurrentMeasure{{MeasureID: "window", State: CurrentKnown, Value: MeasureValue{State: ValueKnown, Decimal: &decimal}}},
		}},
		SourceStatuses: []SourceStatus{{
			Provider: "openai", Availability: AvailabilityConflict, ProducerRef: "payload-producer",
			Binding:   BindingRecord{State: BindingVerified, Generation: 4, AccountRef: repairStr("other-account")},
			Freshness: Freshness{State: FreshFresh, AsOf: "2026-11-02T02:59:00Z", Basis: BasisProvider},
		}},
	}
	ProjectCachedRead(&doc, nil, nil, time.Hour, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	item := doc.Items[0]
	if item.CurrentMeasures[0].State != CurrentUnknown || item.CurrentMeasures[0].Value.Decimal != nil || item.CurrentMeasures[0].Value.State != ValueUnknown {
		t.Fatalf("untrusted current %+v", item.CurrentMeasures[0])
	}
	if item.Observation.Measures[0].Value.Decimal == nil || *item.Observation.Measures[0].Value.Decimal != "55" {
		t.Fatal("raw observation decimal dropped")
	}
	if doc.SourceStatuses[0].Availability != AvailabilityConflict || doc.SourceStatuses[0].Binding.State == BindingVerified || doc.SourceStatuses[0].Binding.AccountRef != nil || doc.SourceStatuses[0].Binding.Generation != 0 {
		t.Fatalf("conflict kept a verified binding %+v", doc.SourceStatuses[0].Binding)
	}
}

func TestProjectCachedReadLeavesUnknownGap(t *testing.T) {
	_, ctx, reg := repairAllowanceFixture(t)
	asOf := "1970-01-01T00:00:00Z"
	doc := ReadResponse{
		Schema: SchemaRead, SnapshotRevision: "gap", ServedAt: "2026-11-02T03:10:00Z",
		Items: []ReadItem{},
		SourceStatuses: []SourceStatus{{
			Provider: "openai", ProducerRef: "aggregate-unconfigured", Availability: AvailabilityUnconfigured,
			Binding:   BindingRecord{State: BindingUnknown},
			Freshness: Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisNone, Reason: "unconfigured"},
		}},
	}
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	status := doc.SourceStatuses[0]
	if status.Binding.State != BindingUnknown || status.Binding.AccountRef != nil || status.Availability != AvailabilityUnconfigured || status.Freshness.State != FreshUnknown || status.Freshness.AsOf != asOf || status.ProducerRef != "aggregate-unconfigured" {
		t.Fatalf("unknown gap was promoted %+v", status)
	}
	foreign := "other-account"
	doc.SourceStatuses[0].Binding = BindingRecord{State: BindingVerified, Generation: 2, AccountRef: &foreign}
	doc.SourceStatuses[0].ProducerRef = "payload-producer"
	ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC))
	status = doc.SourceStatuses[0]
	if status.Binding.State == BindingVerified || status.Binding.AccountRef != nil || status.Binding.Generation != 0 || status.Availability != AvailabilityUnconfigured || status.ProducerRef != "payload-producer" {
		t.Fatalf("verified gap stayed authored %+v producer=%s", status.Binding, status.ProducerRef)
	}
}

func repairAllowanceFixture(t *testing.T) (Observation, TrustedContext, *StaticRegistry) {
	t.Helper()
	account, workspace := "fixture-account", "fixture-workspace"
	observed := "2026-11-02T02:59:00Z"
	reset := "2026-11-02T06:00:00Z"
	decimal := "55"
	minutes := Count(300)
	ref := "fixture:verified-field-v1"
	min, max := "0", "100"
	obs := Observation{
		Schema: SchemaObservation, ObservationID: "obs-repair", ProducerRef: "fixture-producer",
		ProducerIncarnation: "fixture-incarnation-1", Sequence: 1, BindingGeneration: 0,
		Provider:   "openai",
		Source:     Source{Kind: SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live"},
		Scope:      Scope{AccountRef: &account, WorkspaceRef: &workspace, Products: []string{"openai"}, BindingState: BindingVerified},
		CapturedAt: "2026-11-02T03:00:00Z", ProviderObservedAt: &observed, Availability: AvailabilityAvailable, Completeness: CompletenessComplete,
		DirectFields: []DirectField{}, Derivations: []Derivation{}, EvidenceRefs: []string{},
		Measures: []Measure{{
			ID: "window", Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed,
			Window:        Window{Ref: "five-hour-window", DurationMinutes: &minutes, ResetAt: &reset, ResetPrecision: ResetInstant},
			Value:         MeasureValue{State: ValueKnown, Decimal: &decimal},
			ValidationRef: &ref, EvidenceFields: []string{},
		}},
	}
	ctx := TrustedContext{
		Provider: "openai", ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
		AccountRef: &account, WorkspaceRef: &workspace, BindingState: BindingVerified,
		SourceKind: SourceNativeRPC, SourceVersion: "fixture-v1",
	}
	reg := NewStaticRegistry(RegistryParts{
		Sources: []SourceRule{{
			Provider: "openai", Kind: SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live",
			AllowNormalizedMeasures: true, AllowDerivations: true,
		}},
		Metrics: []MetricRule{{
			ValidationRef: "fixture:verified-field-v1", Provider: "openai", SourceKind: SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed,
			Windows: []string{"five-hour-window"}, Min: &min, Max: &max,
		}},
		Accounts: []AccountRule{{
			AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0,
			ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
		}},
	})
	if !cachedObservationTrusted(obs, ctx, reg) {
		t.Fatal("fixture is not trusted")
	}
	return obs, ctx, reg
}

func repairRead(obs Observation, captured, received, asOf string, current CurrentMeasure) ReadResponse {
	obs.CapturedAt = captured
	return ReadResponse{
		Schema: SchemaRead, SnapshotRevision: "cached", ServedAt: "2026-11-02T03:10:00Z",
		Items: []ReadItem{{
			Observation: obs, ReceivedAt: received,
			Validation:      ValidationRecord{State: ValidationSourceFields, ValidationRefs: []string{}},
			Binding:         BindingRecord{State: BindingVerified, AccountRef: obs.Scope.AccountRef, WorkspaceRef: obs.Scope.WorkspaceRef},
			Freshness:       Freshness{State: FreshFresh, AsOf: asOf, Basis: BasisProvider},
			CurrentMeasures: []CurrentMeasure{current},
		}},
		SourceStatuses: []SourceStatus{{
			Provider: "openai", ProducerRef: "fixture-producer", Availability: AvailabilityAvailable,
			Binding:   BindingRecord{State: BindingVerified, AccountRef: obs.Scope.AccountRef, WorkspaceRef: obs.Scope.WorkspaceRef},
			Freshness: Freshness{State: FreshFresh, AsOf: asOf, Basis: BasisProvider},
		}},
	}
}

func repairStr(value string) *string { return &value }

func TestProjectCachedReadDegradesConfiguredStatusWithItem(t *testing.T) {
	for _, availability := range []Availability{AvailabilityAvailable, AvailabilityUnavailable, AvailabilityConflict} {
		for _, futureReceipt := range []bool{false, true} {
			name := string(availability) + "/stale"
			if futureReceipt {
				name = string(availability) + "/expired"
			}
			t.Run(name, func(t *testing.T) {
				obs, ctx, reg := repairAllowanceFixture(t)
				received := "2026-11-02T03:05:00Z"
				want := FreshStale
				if futureReceipt {
					received, want = "2099-01-01T00:00:00Z", FreshExpired
				}
				doc := repairRead(obs, obs.CapturedAt, received, *obs.ProviderObservedAt, CurrentMeasure{
					MeasureID: "window", State: CurrentKnown, Value: MeasureValue{State: ValueKnown, Decimal: repairStr("55")},
				})
				doc.SourceStatuses[0].Availability = availability
				asOf := doc.SourceStatuses[0].Freshness.AsOf
				ProjectCachedRead(&doc, []TrustedContext{ctx}, reg, 30*time.Minute, true, time.Date(2026, 11, 2, 3, 40, 0, 0, time.UTC))
				status := doc.SourceStatuses[0]
				if doc.Items[0].Freshness.State != want || status.Freshness.State != want || status.Freshness.AsOf != asOf || status.Availability != availability {
					t.Fatalf("status drifted from item or moved original metadata: item=%+v status=%+v", doc.Items[0].Freshness, status)
				}
				if doc.Items[0].Observation.Measures[0].Value.Decimal == nil || *doc.Items[0].Observation.Measures[0].Value.Decimal != "55" {
					t.Fatal("reported history was removed")
				}
			})
		}
	}
}
