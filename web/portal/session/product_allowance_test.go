package session

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

func allowanceString(v string) *string      { return &v }
func allowanceMinutes(v pa.Count) *pa.Count { return &v }

func allowanceRead() pa.ReadResponse {
	return pa.ReadResponse{Schema: pa.SchemaRead, Items: []pa.ReadItem{{
		Observation: pa.Observation{Provider: "anthropic", Availability: pa.AvailabilityAvailable,
			Scope: pa.Scope{BindingState: pa.BindingVerified}, Source: pa.Source{Kind: pa.SourceNativeRPC}, ProviderObservedAt: allowanceString("2026-10-29T13:59:00Z"),
			Measures: []pa.Measure{{ID: "weekly", Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionRemaining,
				Value:  pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("62.00")},
				Window: pa.Window{Ref: "claude-primary-window", DurationMinutes: allowanceMinutes(10080), ResetAt: allowanceString("2026-11-02T14:00:00Z"), ResetPrecision: pa.ResetInstant}}}},
		Validation: pa.ValidationRecord{State: pa.ValidationSourceFields}, Binding: pa.BindingRecord{State: pa.BindingVerified},
		Freshness: pa.Freshness{State: pa.FreshFresh, Basis: pa.BasisProvider}, CurrentMeasures: []pa.CurrentMeasure{{MeasureID: "weekly", State: pa.CurrentKnown, Value: pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("62.00")}}},
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
	if got := MapProductAllowance(doc)[0].Windows[0].Reset; got != "Unknown" {
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
	doc.Items[1].Observation.Measures[0].ID = "daily"
	doc.Items[1].CurrentMeasures[0].MeasureID = "daily"
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceStale || len(got.Windows) != 2 {
		t.Fatalf("mixed freshness: %+v", got)
	}
	doc.Items[1].Binding.State = pa.BindingMismatch
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 {
		t.Fatalf("mixed trust: %+v", got)
	}
}

func TestMapProductAllowanceReadInterfaceExample(t *testing.T) {
	raw, err := os.ReadFile("../../../internal/providerallowance/testdata/read-interface-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		HTTP struct {
			Body json.RawMessage `json:"body"`
		} `json:"http"`
	}
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	doc, err := pa.DecodeReadResponse(example.HTTP.Body, pa.FixtureParseLimits)
	if err != nil {
		t.Fatalf("contract HTTP body cannot be strictly decoded: %v", err)
	}
	got := MapProductAllowance(doc)
	if got[0].Value != portal.AllowanceNotConnected || got[1].Value != portal.AllowanceUnknown || len(got[1].Windows) != 0 {
		t.Fatalf("original cached example: %+v", got)
	}
	// The original has unverified binding records and observation scope. Verify
	// that verifying both still cannot turn capture-only freshness into a measurement.
	for i := range doc.Items {
		if doc.Items[i].Observation.Provider == "openai" {
			doc.Items[i].Binding.State = pa.BindingVerified
			doc.Items[i].Observation.Scope.BindingState = pa.BindingVerified
		}
	}
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "openai" {
			doc.SourceStatuses[i].Binding.State = pa.BindingVerified
		}
	}
	if got := MapProductAllowance(doc)[1]; got.Value != portal.AllowanceUnknown || !strings.Contains(got.Reason, "freshness") || len(got.Windows) != 0 {
		t.Fatalf("capture is not provider freshness: %+v", got)
	}
	// Explicit synthetic trust/current-value inputs; no provider read occurred.
	for i := range doc.Items {
		item := &doc.Items[i]
		if item.Observation.Provider != "openai" {
			continue
		}
		item.Freshness.State = pa.FreshFresh
		item.Freshness.Basis = pa.BasisProvider
		item.Freshness.Reason = "Synthetic test only"
		item.CurrentMeasures[0].State = pa.CurrentKnown
		item.CurrentMeasures[0].Value = item.Observation.Measures[0].Value
		item.CurrentMeasures[1].State = pa.CurrentNotReported
		item.CurrentMeasures[1].Value = item.Observation.Measures[1].Value
	}
	for i := range doc.SourceStatuses {
		if doc.SourceStatuses[i].Provider == "openai" {
			doc.SourceStatuses[i].Freshness.State = pa.FreshFresh
			doc.SourceStatuses[i].Freshness.Basis = pa.BasisProvider
			doc.SourceStatuses[i].Freshness.Reason = "Synthetic test only"
		}
	}
	raw, err = json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	doc, err = pa.DecodeReadResponse(raw, pa.FixtureParseLimits)
	if err != nil {
		t.Fatalf("synthetic fresh variant cannot be strictly decoded: %v", err)
	}
	got = MapProductAllowance(doc)
	if got[0].Value != portal.AllowanceNotConnected || got[1].Value != portal.AllowanceMeasured || len(got[1].Windows) != 2 || got[1].Windows[0].Percent != "50% used" || got[1].Windows[0].Window != "7 days" || got[1].Windows[1].Percent != "Unknown" {
		t.Fatalf("fresh known plus unreported windows: %+v", got)
	}
	for _, usage := range []bool{false, true} {
		page := portal.ProductPage{Title: "Sessions", Connected: true, Allowance: got, Usage: &portal.ProductUsage{Allowance: got}}
		if usage {
			page.Filter.View = "usage"
		}
		body, err := portal.RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range []string{"codex-primary-window", "codex-secondary-window"} {
			if strings.Contains(string(body), ref) {
				t.Fatalf("opaque ref %q rendered on usage=%v", ref, usage)
			}
		}
	}
}

func TestMapProductAllowanceMixedMeasureStates(t *testing.T) {
	type measureCase struct {
		name   string
		change func(*pa.ReadItem)
		rows   int
		refuse bool
	}
	tests := []measureCase{
		{"rate-limit", func(i *pa.ReadItem) { i.Observation.Measures[1].Category = pa.CategoryRateLimit }, 2, false},
		{"current-conflict", func(i *pa.ReadItem) { i.CurrentMeasures[1].State = pa.CurrentConflict }, 0, true},
		{"disagreement", func(i *pa.ReadItem) { i.CurrentMeasures[1].Value.Decimal = allowanceString("4") }, 0, true},
		{"F1-ignored-unit-conflict", func(i *pa.ReadItem) {
			i.Observation.Measures[1].Unit = pa.UnitMoney
			i.CurrentMeasures[1].State = pa.CurrentConflict
		}, 0, true},
		{"F1-ignored-category-conflict", func(i *pa.ReadItem) {
			i.Observation.Measures[1].Category = pa.CategorySpend
			i.CurrentMeasures[1].State = pa.CurrentConflict
		}, 0, true},
		{"F1-unmatched-current-conflict", func(i *pa.ReadItem) {
			i.CurrentMeasures[1].MeasureID = "unmatched"
			i.CurrentMeasures[1].State = pa.CurrentConflict
		}, 0, true},
		{"F1-money-decimal-disagreement", func(i *pa.ReadItem) {
			i.Observation.Measures[1].Unit = pa.UnitMoney
			i.CurrentMeasures[1].Value.Decimal = allowanceString("4")
		}, 0, true},
		// Money values outside the displayed percent grammar still conflict.
		{"F1-large-money-decimal-disagreement", func(i *pa.ReadItem) {
			i.Observation.Measures[1].Unit = pa.UnitMoney
			i.Observation.Measures[1].Value.Decimal = allowanceString("1500000.25")
			i.CurrentMeasures[1].Value.Decimal = allowanceString("1499999.75")
		}, 0, true},
		{"F1-negative-money-decimal-disagreement", func(i *pa.ReadItem) {
			i.Observation.Measures[1].Unit = pa.UnitMoney
			i.Observation.Measures[1].Value.Decimal = allowanceString("-2.5")
			i.CurrentMeasures[1].Value.Decimal = allowanceString("-3")
		}, 0, true},
		{"duplicate-measure", func(i *pa.ReadItem) { i.Observation.Measures[1].ID = "weekly" }, 0, true},
		{"duplicate-current", func(i *pa.ReadItem) { i.CurrentMeasures[1].MeasureID = "weekly" }, 0, true},
		{"missing-current", func(i *pa.ReadItem) { i.CurrentMeasures = i.CurrentMeasures[:1] }, 2, false},
	}
	for _, state := range []pa.CurrentState{"", pa.CurrentUnknown, pa.CurrentExpired, pa.CurrentNotReported, pa.CurrentNotApplicable, pa.CurrentUncapped} {
		tests = append(tests, measureCase{"current/" + string(state), func(i *pa.ReadItem) { i.CurrentMeasures[1].State = state }, 2, false})
	}
	for _, state := range []pa.ValueState{"", pa.ValueUnknown, pa.ValueNotReported, pa.ValueNotApplicable, pa.ValueUncapped} {
		tests = append(tests,
			measureCase{"current-value/" + string(state), func(i *pa.ReadItem) { i.CurrentMeasures[1].Value.State = state }, 2, false},
			measureCase{"measure-value/" + string(state), func(i *pa.ReadItem) { i.Observation.Measures[1].Value.State = state }, 2, false})
	}
	for _, direction := range []pa.Direction{"", pa.DirectionLimit, pa.DirectionNone, pa.DirectionUnknown, "future-direction"} {
		tests = append(tests, measureCase{"direction/" + string(direction), func(i *pa.ReadItem) { i.Observation.Measures[1].Direction = direction }, 2, false})
	}
	for _, unit := range []string{"", "tokens", "bytes", pa.UnitMoney} {
		tests = append(tests, measureCase{"ignored-unit/" + unit, func(i *pa.ReadItem) { i.Observation.Measures[1].Unit = unit }, 1, false})
	}
	for _, category := range []pa.Category{"", pa.CategorySpend, pa.CategoryLocal, pa.CategoryAdvisory} {
		tests = append(tests, measureCase{"ignored-category/" + string(category), func(i *pa.ReadItem) { i.Observation.Measures[1].Category = category }, 1, false})
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := allowanceRead()
			item := &doc.Items[0]
			item.Observation.Measures = append(item.Observation.Measures, item.Observation.Measures[0])
			item.Observation.Measures[1].ID = "secondary"
			item.CurrentMeasures = append(item.CurrentMeasures, item.CurrentMeasures[0])
			item.CurrentMeasures[1].MeasureID = "secondary"
			tc.change(item)
			for _, fresh := range []pa.FreshnessState{pa.FreshFresh, pa.FreshStale} {
				item.Freshness.State = fresh
				got := MapProductAllowance(doc)[0]
				if tc.refuse {
					if got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 || got.Reason != "Conflicting allowance values" {
						t.Fatalf("conflict did not refuse provider: %+v", got)
					}
					continue
				}
				wantState := portal.AllowanceMeasured
				if fresh == pa.FreshStale {
					wantState = portal.AllowanceStale
				}
				if got.Value != wantState || len(got.Windows) != tc.rows || got.Windows[0].Percent != "62.00% left" {
					t.Fatalf("valid percent lost: %+v", got)
				}
				if tc.rows == 2 && tc.name != "rate-limit" && got.Windows[1].Percent != "Unknown" {
					t.Fatalf("unknown window gained a number: %+v", got)
				}
			}
		})
	}
}

func TestAllowanceWindowNeverUsesRef(t *testing.T) {
	for _, duration := range []*pa.Count{nil, allowanceMinutes(0), allowanceMinutes(300)} {
		window := pa.Window{Ref: "codex-primary-window", DurationMinutes: duration}
		want := "Unknown"
		if duration != nil && *duration == 300 {
			want = "5 hours"
		}
		if got := allowanceWindow(window); got != want {
			t.Fatalf("duration %v: got %q, want %q", duration, got, want)
		}
	}
}

func TestMapProductAllowanceNeedsAtLeastOnePercent(t *testing.T) {
	for _, fresh := range []pa.FreshnessState{pa.FreshFresh, pa.FreshStale} {
		for _, kind := range []string{"unknown", "unreported", "direction", "ignored-unit", "ignored-category"} {
			doc := allowanceRead()
			item := &doc.Items[0]
			item.Freshness.State = fresh
			switch kind {
			case "unknown":
				item.CurrentMeasures[0].State = pa.CurrentUnknown
			case "unreported":
				item.CurrentMeasures[0].State = pa.CurrentNotReported
			case "direction":
				item.Observation.Measures[0].Direction = pa.DirectionUnknown
			case "ignored-unit":
				item.Observation.Measures[0].Unit = "tokens"
			case "ignored-category":
				item.Observation.Measures[0].Category = pa.CategorySpend
			}
			got := MapProductAllowance(doc)[0]
			if got.Value != portal.AllowanceUnknown || len(got.Windows) != 0 || got.Reason != "No percent allowance reported" {
				t.Fatalf("%s/%s: %+v", fresh, kind, got)
			}
		}
	}
}

func TestMapProductAllowanceMeasureIDsAreProviderWide(t *testing.T) {
	for _, decimal := range []string{"62.00", "18"} {
		doc := allowanceRead()
		other := allowanceRead().Items[0]
		other.Observation.Measures[0].Value.Decimal = allowanceString(decimal)
		other.CurrentMeasures[0].Value.Decimal = allowanceString(decimal)
		doc.Items = append(doc.Items, other)
		if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || got.Reason != "Conflicting allowance values" || len(got.Windows) != 0 {
			t.Fatalf("duplicate with decimal %q: %+v", decimal, got)
		}
		// The same ID belongs to a separate namespace for each provider.
		doc.Items[1].Observation.Provider = "openai"
		for _, got := range MapProductAllowance(doc) {
			if got.Value != portal.AllowanceMeasured || len(got.Windows) != 1 {
				t.Fatalf("separate providers: %+v", got)
			}
		}
	}
}

func TestMapProductAllowanceRequiresProviderFreshnessBasis(t *testing.T) {
	for _, basis := range []pa.FreshnessBasis{"", pa.BasisCapture, pa.BasisNone} {
		for _, fresh := range []pa.FreshnessState{pa.FreshFresh, pa.FreshStale} {
			for _, asStatus := range []bool{false, true} {
				doc := allowanceRead()
				if asStatus {
					i := doc.Items[0]
					doc.SourceStatuses = []pa.SourceStatus{{Provider: "anthropic", Availability: pa.AvailabilityAvailable, Validation: i.Validation, Binding: i.Binding, Freshness: pa.Freshness{State: fresh, Basis: basis}}}
				} else {
					doc.Items[0].Freshness = pa.Freshness{State: fresh, Basis: basis}
				}
				if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceUnknown || !strings.Contains(got.Reason, "freshness") || len(got.Windows) != 0 {
					t.Fatalf("basis=%q fresh=%s status=%v: %+v", basis, fresh, asStatus, got)
				}
			}
		}
	}
}

func TestMapProductAllowancePartialRead(t *testing.T) {
	for _, cursor := range []string{"", "synthetic-next-page"} {
		doc := allowanceRead()
		doc.NextCursor = allowanceString(cursor)
		doc.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityUnconfigured}}
		for _, got := range MapProductAllowance(doc) {
			if got.Value != portal.AllowanceUnknown || got.Reason != "Partial allowance read" || len(got.Windows) != 0 {
				t.Fatalf("cursor %q: %+v", cursor, got)
			}
		}
	}
}

func TestMapProductAllowanceRequiresBothBindings(t *testing.T) {
	for _, scope := range []pa.BindingState{"", pa.BindingUnknown, pa.BindingMismatch, pa.BindingVerified} {
		for _, binding := range []pa.BindingState{"", pa.BindingUnknown, pa.BindingMismatch, pa.BindingVerified} {
			doc := allowanceRead()
			doc.Items[0].Observation.Scope.BindingState = scope
			doc.Items[0].Binding.State = binding
			got := MapProductAllowance(doc)[0]
			if scope == pa.BindingVerified && binding == pa.BindingVerified {
				if got.Value != portal.AllowanceMeasured {
					t.Fatalf("verified bindings: %+v", got)
				}
			} else if got.Value != portal.AllowanceUnknown || got.Reason != "Account binding unverified" || len(got.Windows) != 0 {
				t.Fatalf("scope=%q binding=%q: %+v", scope, binding, got)
			}
		}
	}
}

func TestMapProductAllowanceAvailabilityPrecedence(t *testing.T) {
	// Exercise both orders within and across the status and item collections.
	for _, levels := range []string{"statuses", "items", "status-item", "item-status"} {
		for _, first := range []pa.Availability{pa.AvailabilityConflict, pa.AvailabilityUnconfigured} {
			for _, second := range []pa.Availability{pa.AvailabilityConflict, pa.AvailabilityUnconfigured, pa.AvailabilityUnavailable, pa.AvailabilityAvailable} {
				for _, reverse := range []bool{false, true} {
					doc := allowanceRead()
					one, two := first, second
					if reverse {
						one, two = two, one
					}
					status := func(a pa.Availability) pa.SourceStatus {
						i := allowanceRead().Items[0]
						return pa.SourceStatus{Provider: "anthropic", Availability: a, Validation: i.Validation, Binding: i.Binding, Freshness: i.Freshness}
					}
					item := func(a pa.Availability, id string) pa.ReadItem {
						i := allowanceRead().Items[0]
						i.Observation.Availability = a
						i.Observation.Measures[0].ID, i.CurrentMeasures[0].MeasureID = id, id
						return i
					}
					switch levels {
					case "statuses":
						doc.SourceStatuses = []pa.SourceStatus{status(one), status(two)}
					case "items":
						doc.Items = []pa.ReadItem{item(one, "one"), item(two, "two")}
					case "status-item":
						doc.SourceStatuses, doc.Items = []pa.SourceStatus{status(one)}, []pa.ReadItem{item(two, "one")}
					case "item-status":
						doc.SourceStatuses, doc.Items = []pa.SourceStatus{status(two)}, []pa.ReadItem{item(one, "one")}
					}
					got := MapProductAllowance(doc)[0]
					want, reason := portal.AllowanceNotConnected, ""
					if first == pa.AvailabilityConflict || second == pa.AvailabilityConflict {
						want, reason = portal.AllowanceUnknown, "Conflicting allowance reports"
					}
					if got.Value != want || got.Reason != reason || len(got.Windows) != 0 {
						t.Fatalf("%s %s/%s reverse=%v: %+v", levels, first, second, reverse, got)
					}
				}
			}
		}
	}
}

func TestMapProductAllowancePercentDecimalGrammar(t *testing.T) {
	for _, tc := range []struct{ decimal, want string }{
		{"0", "0% left"}, {"100", "100% left"}, {"100.000001", "100.000001% left"},
		{"999999.999999", "999999.999999% left"}, {"000001.000001", "000001.000001% left"},
		{"-1", "Unknown"}, {"", "Unknown"}, {"1000000", "Unknown"}, {"1.0000001", "Unknown"},
		{".5", "Unknown"}, {"1.", "Unknown"}, {"+1", "Unknown"}, {" 1", "Unknown"}, {"1 ", "Unknown"},
		{"1e2", "Unknown"}, {"NaN", "Unknown"}, {"1\n", "Unknown"}, {"１", "Unknown"},
	} {
		for _, operand := range []string{"both", "measure", "current"} {
			if tc.want != "Unknown" && operand != "both" {
				continue
			}
			t.Run(tc.decimal+"/"+operand, func(t *testing.T) {
				doc := allowanceRead()
				// Keep one known window so an invalid percent remains visible as Unknown.
				i := &doc.Items[0]
				m, c := i.Observation.Measures[0], i.CurrentMeasures[0]
				m.ID, c.MeasureID = "secondary", "secondary"
				if operand != "current" {
					m.Value.Decimal = allowanceString(tc.decimal)
				}
				if operand != "measure" {
					c.Value.Decimal = allowanceString(tc.decimal)
				}
				i.Observation.Measures = append(i.Observation.Measures, m)
				i.CurrentMeasures = append(i.CurrentMeasures, c)
				got := MapProductAllowance(doc)[0]
				if got.Value != portal.AllowanceMeasured || len(got.Windows) != 2 || got.Windows[1].Percent != tc.want {
					t.Fatalf("decimal %q on %s: %+v", tc.decimal, operand, got)
				}
			})
		}
	}
}

func TestMapProductAllowancePartialAvailabilityAndResetMetadata(t *testing.T) {
	doc := allowanceRead()
	doc.Items[0].Observation.Availability = pa.AvailabilityPartial
	i := doc.Items[0]
	doc.SourceStatuses = []pa.SourceStatus{{Provider: "anthropic", Availability: pa.AvailabilityPartial, Validation: i.Validation, Binding: i.Binding, Freshness: i.Freshness}}
	if got := MapProductAllowance(doc)[0]; got.Value != portal.AllowanceMeasured || len(got.Windows) != 1 {
		t.Fatalf("partial availability: %+v", got)
	}
	for _, precision := range []pa.ResetPrecision{pa.ResetNotApplicable, pa.ResetInstant, pa.ResetDisplayOnly} {
		for _, at := range []*string{nil, allowanceString("not-a-time")} {
			doc.Items[0].Observation.Measures[0].Window.ResetPrecision = precision
			doc.Items[0].Observation.Measures[0].Window.ResetAt = at
			doc.Items[0].Observation.ProviderObservedAt = allowanceString("not-a-time")
			got := MapProductAllowance(doc)[0]
			if got.Value != portal.AllowanceMeasured || got.Windows[0].Reset != "Unknown" || got.Windows[0].Observed != "Unknown" {
				t.Fatalf("precision %s: %+v", precision, got)
			}
		}
	}
	// Not applicable remains Unknown even if a time is supplied.
	doc.Items[0].Observation.Measures[0].Window = pa.Window{ResetPrecision: pa.ResetNotApplicable, ResetAt: allowanceString("2026-11-02T14:00:00Z")}
	if got := MapProductAllowance(doc)[0].Windows[0].Reset; got != "Unknown" {
		t.Fatal(got)
	}
}
