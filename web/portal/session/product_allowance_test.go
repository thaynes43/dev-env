package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

func allowanceString(v string) *string { return &v }

func allowanceRead() pa.ReadResponse {
	return pa.ReadResponse{Schema: pa.SchemaRead, Items: []pa.ReadItem{{
		Observation: pa.Observation{Provider: "anthropic", Availability: pa.AvailabilityAvailable,
			Source: pa.Source{Kind: pa.SourceNativeRPC}, ProviderObservedAt: allowanceString("2026-10-29T13:59:00Z"),
			Measures: []pa.Measure{{ID: "weekly", Unit: pa.UnitPercent, Direction: pa.DirectionRemaining,
				Value:  pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("62.00")},
				Window: pa.Window{Ref: "7 days", ResetAt: allowanceString("2026-11-02T14:00:00Z"), ResetPrecision: pa.ResetInstant}}}},
		Validation: pa.ValidationRecord{State: pa.ValidationSourceFields}, Binding: pa.BindingRecord{State: pa.BindingVerified},
		Freshness: pa.Freshness{State: pa.FreshFresh}, CurrentMeasures: []pa.CurrentMeasure{{MeasureID: "weekly", State: pa.CurrentKnown, Value: pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("62.00")}}},
	}}}
}

func TestMapProductAllowanceMeasuredAndStale(t *testing.T) {
	for _, source := range []struct {
		kind pa.SourceKind
		text string
	}{{pa.SourceNativeRPC, "Provider API"}, {pa.SourceNativeDisplay, "Provider display"}, {pa.SourcePassive, "Passive event"}} {
		for _, validation := range []pa.ValidationState{pa.ValidationSourceFields, pa.ValidationDisplayOnly} {
			for _, fresh := range []pa.FreshnessState{pa.FreshFresh, pa.FreshStale} {
				for _, direction := range []pa.Direction{pa.DirectionRemaining, pa.DirectionUsed} {
					t.Run(string(source.kind)+"/"+string(validation)+"/"+string(fresh)+"/"+string(direction), func(t *testing.T) {
						doc := allowanceRead()
						item := &doc.Items[0]
						item.Observation.Source.Kind, item.Validation.State, item.Freshness.State, item.Observation.Measures[0].Direction = source.kind, validation, fresh, direction
						got := MapProductAllowance(doc)
						state := portal.AllowanceMeasured
						if fresh == pa.FreshStale {
							state = portal.AllowanceStale
						}
						suffix := "% left"
						if direction == pa.DirectionUsed {
							suffix = "% used"
						}
						want := portal.ProductAllowance{Provider: "Claude", Value: state, Windows: []portal.ProductAllowanceWindow{{Percent: "62.00" + suffix, Window: "7 days", Reset: "2 Nov 14:00 UTC", Observed: "29 Oct 13:59 UTC", Provenance: source.text}}}
						if !reflect.DeepEqual(got[0], want) {
							t.Fatalf("got %+v, want %+v", got[0], want)
						}
						if got[1].Provider != "Codex" || got[1].Value != portal.AllowanceUnknown {
							t.Fatalf("missing provider: %+v", got)
						}
					})
				}
			}
		}
	}
}

func TestMapProductAllowanceRefusesUntrustedValues(t *testing.T) {
	tests := []struct {
		name   string
		change func(*pa.ReadItem)
	}{}
	for _, state := range []pa.ValidationState{"", pa.ValidationUnverified, pa.ValidationPartial, pa.ValidationRejected, pa.ValidationConflict} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"validation/" + string(state), func(i *pa.ReadItem) { i.Validation.State = state }})
	}
	for _, state := range []pa.BindingState{"", pa.BindingUnknown, pa.BindingMismatch} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"binding/" + string(state), func(i *pa.ReadItem) { i.Binding.State = state }})
	}
	for _, state := range []pa.CurrentState{"", pa.CurrentUnknown, pa.CurrentExpired, pa.CurrentNotReported, pa.CurrentNotApplicable, pa.CurrentUncapped, pa.CurrentConflict} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"current/" + string(state), func(i *pa.ReadItem) { i.CurrentMeasures[0].State = state }})
	}
	for _, state := range []pa.ValueState{"", pa.ValueUnknown, pa.ValueNotReported, pa.ValueNotApplicable, pa.ValueUncapped} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"current-value/" + string(state), func(i *pa.ReadItem) { i.CurrentMeasures[0].Value.State = state }})
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"measure-value/" + string(state), func(i *pa.ReadItem) { i.Observation.Measures[0].Value.State = state }})
	}
	for _, state := range []pa.FreshnessState{"", pa.FreshUnknown, pa.FreshExpired} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"freshness/" + string(state), func(i *pa.ReadItem) { i.Freshness.State = state }})
	}
	for _, source := range []pa.SourceKind{"", pa.SourceSynthetic, pa.SourceAdvisory, "future-source"} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"source/" + string(source), func(i *pa.ReadItem) { i.Observation.Source.Kind = source }})
	}
	for _, direction := range []pa.Direction{"", pa.DirectionLimit, pa.DirectionNone, pa.DirectionUnknown, "future-direction"} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"direction/" + string(direction), func(i *pa.ReadItem) { i.Observation.Measures[0].Direction = direction }})
	}
	for _, unit := range []string{"", pa.UnitMoney, "tokens", "bytes", "Percent"} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"unit/" + unit, func(i *pa.ReadItem) { i.Observation.Measures[0].Unit = unit }})
	}
	for _, availability := range []pa.Availability{"", pa.AvailabilityUnavailable, pa.AvailabilityAuthNeeded, pa.AvailabilityThrottled, pa.AvailabilityUnsupported, pa.AvailabilityConflict} {
		tests = append(tests, struct {
			name   string
			change func(*pa.ReadItem)
		}{"availability/" + string(availability), func(i *pa.ReadItem) { i.Observation.Availability = availability }})
	}
	tests = append(tests, []struct {
		name   string
		change func(*pa.ReadItem)
	}{
		{"missing-current", func(i *pa.ReadItem) { i.CurrentMeasures = nil }},
		{"wrong-current-id", func(i *pa.ReadItem) { i.CurrentMeasures[0].MeasureID = "different" }},
		{"nil-measure-decimal", func(i *pa.ReadItem) { i.Observation.Measures[0].Value.Decimal = nil }},
		{"mismatched-values", func(i *pa.ReadItem) { i.Observation.Measures[0].Value.Decimal = allowanceString("12") }},
		{"nil-current-decimal", func(i *pa.ReadItem) { i.CurrentMeasures[0].Value.Decimal = nil }},
		{"empty-current-decimal", func(i *pa.ReadItem) { i.CurrentMeasures[0].Value.Decimal = allowanceString("") }},
		{"duplicate-current", func(i *pa.ReadItem) { i.CurrentMeasures = append(i.CurrentMeasures, i.CurrentMeasures[0]) }},
		{"duplicate-measure", func(i *pa.ReadItem) {
			i.Observation.Measures = append(i.Observation.Measures, i.Observation.Measures[0])
		}},
		{"no-measures", func(i *pa.ReadItem) { i.Observation.Measures = nil }},
	}...)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := allowanceRead()
			tc.change(&doc.Items[0])
			doc.Items[0].Freshness.Reason = "PRIVATE-REASON"
			doc.Items[0].CurrentMeasures = append([]pa.CurrentMeasure(nil), doc.Items[0].CurrentMeasures...)
			got := MapProductAllowance(doc)[0]
			if got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 || got.Reason == "" || strings.Contains(got.Reason, "PRIVATE") {
				t.Fatalf("untrusted value escaped: %+v", got)
			}
		})
	}
}

func TestMapProductAllowanceDocumentAndAvailability(t *testing.T) {
	for _, schema := range []string{"", pa.SchemaObservation, "future-schema"} {
		doc := allowanceRead()
		doc.Schema = schema
		doc.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityUnconfigured}}
		for _, a := range MapProductAllowance(doc) {
			if a.Value != portal.AllowanceUnknown || len(a.Windows) != 0 {
				t.Fatalf("schema %q: %+v", schema, a)
			}
		}
	}
	for _, provider := range []string{"anthropic", "openai"} {
		for _, asStatus := range []bool{false, true} {
			doc := allowanceRead()
			doc.Items[0].Observation.Provider = provider
			if asStatus {
				doc.SourceStatuses = []pa.SourceStatus{{Provider: provider, Availability: pa.AvailabilityUnconfigured}}
			} else {
				doc.Items[0].Observation.Availability = pa.AvailabilityUnconfigured
			}
			got := MapProductAllowance(doc)
			i := 0
			if provider == "openai" {
				i = 1
			}
			if got[i].Value != portal.AllowanceNotConnected || len(got[i].Windows) != 0 {
				t.Fatalf("unconfigured: %+v", got)
			}
		}
	}
	doc := allowanceRead()
	doc.Items[0].Observation.Provider = "other-provider"
	got := MapProductAllowance(doc)
	if len(got) != 2 || got[0].Value != portal.AllowanceUnknown || got[1].Value != portal.AllowanceUnknown {
		t.Fatalf("provider set: %+v", got)
	}
	for _, availability := range []pa.Availability{pa.AvailabilityConflict, pa.AvailabilityUnavailable, pa.AvailabilityAuthNeeded, pa.AvailabilityThrottled, pa.AvailabilityUnsupported} {
		doc := allowanceRead()
		doc.SourceStatuses = []pa.SourceStatus{{Provider: "anthropic", Availability: availability}}
		if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 {
			t.Fatalf("source availability: %+v", got)
		}
	}
}

func TestMapProductAllowanceMissingMetadataAndWindows(t *testing.T) {
	doc := allowanceRead()
	item := &doc.Items[0]
	item.Observation.ProviderObservedAt = nil
	item.Observation.Measures[0].Window = pa.Window{ResetPrecision: pa.ResetUnknown}
	got := MapProductAllowance(doc)[0]
	if got.Value != portal.AllowanceMeasured || got.Windows[0].Window != "Unknown" || got.Windows[0].Reset != "Unknown" || got.Windows[0].Observed != "Unknown" {
		t.Fatalf("missing metadata: %+v", got)
	}
	for _, tc := range []struct {
		minutes pa.Count
		want    string
	}{{10080, "7 days"}, {300, "5 hours"}, {60, "1 hour"}, {1, "1 minute"}, {90, "90 minutes"}, {0, "Unknown"}} {
		item.Observation.Measures[0].Window.DurationMinutes = &tc.minutes
		if got := MapProductAllowance(doc)[0].Windows[0].Window; got != tc.want {
			t.Fatalf("duration %d: %q", tc.minutes, got)
		}
	}
	item.Observation.Measures[0].Window.ResetAt = allowanceString("2026-11-02T09:00:00-05:00")
	item.Observation.Measures[0].Window.ResetPrecision = pa.ResetDisplayOnly
	if got := MapProductAllowance(doc)[0].Windows[0].Reset; got != "2 Nov 14:00 UTC (display only)" {
		t.Fatal(got)
	}
	item.Observation.Measures[0].Window.ResetAt = nil
	if got := MapProductAllowance(doc)[0].Windows[0].Reset; got != "Unknown (display only)" {
		t.Fatal(got)
	}
	// Several independent windows remain intact, in the contract's order.
	item.Observation.Measures = append(item.Observation.Measures, item.Observation.Measures[0])
	item.Observation.Measures[1].ID = "daily"
	item.CurrentMeasures = append(item.CurrentMeasures, item.CurrentMeasures[0])
	item.CurrentMeasures[1].MeasureID = "daily"
	if got := MapProductAllowance(doc)[0]; len(got.Windows) != 2 {
		t.Fatalf("windows: %+v", got)
	}
}

func TestMapProductAllowanceIgnoresDerivationsAndDoesNotMutate(t *testing.T) {
	doc := allowanceRead()
	doc.Items[0].Observation.Derivations = []pa.Derivation{{Value: pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("99.9")}, Formula: pa.FormulaComplement}}
	before, _ := json.Marshal(doc)
	got := MapProductAllowance(doc)[0]
	after, _ := json.Marshal(doc)
	if string(before) != string(after) || got.Windows[0].Percent != "62.00% left" {
		t.Fatalf("input mutated or derived: %+v", got)
	}
	doc.Items[0].Observation.Measures = nil
	doc.Items[0].CurrentMeasures = nil
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 {
		t.Fatalf("derivation became allowance: %+v", got)
	}
}

func TestMapProductAllowanceSourceStatusTrustAndMixedItems(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*pa.SourceStatus)
		want   portal.ProductAllowanceState
	}{
		{"validated", func(*pa.SourceStatus) {}, portal.AllowanceMeasured},
		{"stale", func(s *pa.SourceStatus) { s.Freshness.State = pa.FreshStale }, portal.AllowanceStale},
		{"expired", func(s *pa.SourceStatus) { s.Freshness.State = pa.FreshExpired }, portal.AllowanceUnknown},
		{"unknown", func(s *pa.SourceStatus) { s.Freshness.State = pa.FreshUnknown }, portal.AllowanceUnknown},
		{"rejected", func(s *pa.SourceStatus) { s.Validation.State = pa.ValidationRejected }, portal.AllowanceUnknown},
		{"conflict", func(s *pa.SourceStatus) { s.Validation.State = pa.ValidationConflict }, portal.AllowanceUnknown},
		{"mismatch", func(s *pa.SourceStatus) { s.Binding.State = pa.BindingMismatch }, portal.AllowanceUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := allowanceRead()
			doc.SourceStatuses = []pa.SourceStatus{{Provider: "anthropic", Availability: pa.AvailabilityAvailable, Validation: doc.Items[0].Validation, Binding: doc.Items[0].Binding, Freshness: doc.Items[0].Freshness}}
			tc.change(&doc.SourceStatuses[0])
			got := MapProductAllowance(doc)[0]
			if got.Value != tc.want || (tc.want == portal.AllowanceUnknown && len(got.Windows) != 0) {
				t.Fatalf("source status: %+v", got)
			}
		})
	}
	doc := allowanceRead()
	doc.Items = append(doc.Items, allowanceRead().Items[0])
	doc.Items[1].Freshness.State = pa.FreshStale
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceStale || len(got.Windows) != 2 {
		t.Fatalf("mixed freshness: %+v", got)
	}
	doc.Items[1].Binding.State = pa.BindingMismatch
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 {
		t.Fatalf("mixed trust: %+v", got)
	}
}
