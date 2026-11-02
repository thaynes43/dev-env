package session

import (
	"reflect"
	"testing"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// lastReportedRead is a verified Codex reading whose provider time is absent,
// so freshness is not established: the OP358 case.
func lastReportedRead() pa.ReadResponse {
	return pa.ReadResponse{Schema: pa.SchemaRead, Items: []pa.ReadItem{{
		Observation: pa.Observation{Provider: "openai", Availability: pa.AvailabilityAvailable,
			Scope: pa.Scope{BindingState: pa.BindingVerified, AccountRef: allowanceString("acct-1")}, BindingGeneration: 3,
			Source: pa.Source{Kind: pa.SourceNativeRPC},
			Measures: []pa.Measure{{ID: "weekly", Category: pa.CategoryRateLimit, Unit: pa.UnitPercent, Direction: pa.DirectionUsed,
				Value:  pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("0")},
				Window: pa.Window{Ref: "w", DurationMinutes: allowanceMinutes(10080), ResetAt: allowanceString("2026-11-11T09:22:09Z"), ResetPrecision: pa.ResetInstant}}}},
		ReceivedAt: "2026-11-02T13:40:00Z",
		Validation: pa.ValidationRecord{State: pa.ValidationSourceFields},
		Binding:    pa.BindingRecord{State: pa.BindingVerified, AccountRef: allowanceString("acct-1"), Generation: 3},
		Freshness:  pa.Freshness{State: pa.FreshUnknown, Basis: pa.BasisNone},
		CurrentMeasures: []pa.CurrentMeasure{{MeasureID: "weekly", State: pa.CurrentUnknown,
			Value: pa.MeasureValue{State: pa.ValueUnknown}}},
	}}}
}

func TestLastReportedKeepsReadingApartFromCurrent(t *testing.T) {
	got := MapProductAllowance(lastReportedRead())[1]
	if got.Provider != "Codex" || got.Value != portal.AllowanceUnknown || got.Reason != "Allowance freshness unknown or expired" || got.Windows != nil {
		t.Fatalf("current state changed: %+v", got)
	}
	want := []portal.ProductAllowanceLastReported{{
		Percent: "0% used", Window: "7 days", Reset: "Reported reset: 11 Nov 09:22 UTC.",
		ReadAt: "2 Nov 13:40 UTC", ReadAtExact: "2026-11-02T13:40:00Z",
		Source: "Provider API", Account: "Account verified",
		Why: "Not current: the provider did not report when it measured this.",
	}}
	if !reflect.DeepEqual(got.LastReported, want) {
		t.Fatalf("got %+v, want %+v", got.LastReported, want)
	}
}

func TestLastReportedReasons(t *testing.T) {
	doc := lastReportedRead()
	doc.Items[0].Observation.ProviderObservedAt = allowanceString("2026-11-02T13:39:00Z")
	doc.Items[0].Freshness.State = pa.FreshExpired
	if got := MapProductAllowance(doc)[1].LastReported; len(got) != 1 || got[0].Why != "Not current: this reading has expired." {
		t.Fatalf("expired: %+v", got)
	}
	doc.Items[0].Binding.State = pa.BindingUnknown
	if got := MapProductAllowance(doc)[1].LastReported; len(got) != 1 || got[0].Account != "Account not verified" || got[0].Why != "Not current: this reading's account is not verified." {
		t.Fatalf("unverified: %+v", got)
	}
}

func TestLastReportedRefusals(t *testing.T) {
	for name, change := range map[string]func(*pa.ReadResponse){
		"conflict current": func(d *pa.ReadResponse) { d.Items[0].CurrentMeasures[0].State = pa.CurrentConflict },
		"differing current": func(d *pa.ReadResponse) {
			d.Items[0].CurrentMeasures[0].Value = pa.MeasureValue{State: pa.ValueKnown, Decimal: allowanceString("5")}
		},
		"duplicate current": func(d *pa.ReadResponse) {
			d.Items[0].CurrentMeasures = append(d.Items[0].CurrentMeasures, d.Items[0].CurrentMeasures[0])
		},
		"unvalidated":      func(d *pa.ReadResponse) { d.Items[0].Validation.State = pa.ValidationUnverified },
		"not a measure":    func(d *pa.ReadResponse) { d.Items[0].Observation.Source.Kind = "manual" },
		"unavailable item": func(d *pa.ReadResponse) { d.Items[0].Observation.Availability = pa.AvailabilityUnavailable },
		"bad read time":    func(d *pa.ReadResponse) { d.Items[0].ReceivedAt = "yesterday" },
		"binding mismatch": func(d *pa.ReadResponse) { d.Items[0].Binding.State = pa.BindingMismatch },
		"scope mismatch":   func(d *pa.ReadResponse) { d.Items[0].Observation.Scope.BindingState = pa.BindingMismatch },
		"identity differs": func(d *pa.ReadResponse) { d.Items[0].Binding.AccountRef = allowanceString("acct-2") },
		"mixed accounts": func(d *pa.ReadResponse) {
			second := lastReportedRead().Items[0]
			second.Observation.Measures[0].ID = "five-hour"
			second.Observation.Scope.AccountRef, second.Binding.AccountRef = allowanceString("acct-2"), allowanceString("acct-2")
			second.CurrentMeasures[0].MeasureID = "five-hour"
			d.Items = append(d.Items, second)
		},
		"status another account": func(d *pa.ReadResponse) {
			d.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityUnavailable,
				Binding: pa.BindingRecord{State: pa.BindingVerified, AccountRef: allowanceString("acct-2"), Generation: 3}}}
		},
		"conflict status": func(d *pa.ReadResponse) {
			d.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityConflict}}
		},
		"partial read": func(d *pa.ReadResponse) { d.NextCursor = allowanceString("next") },
		"not percent":  func(d *pa.ReadResponse) { d.Items[0].Observation.Measures[0].Unit = "requests" },
		"out of range": func(d *pa.ReadResponse) { d.Items[0].Observation.Measures[0].Value.Decimal = allowanceString("101") },
		"value unknown": func(d *pa.ReadResponse) {
			d.Items[0].Observation.Measures[0].Value = pa.MeasureValue{State: pa.ValueUnknown}
		},
		"no direction":    func(d *pa.ReadResponse) { d.Items[0].Observation.Measures[0].Direction = "" },
		"credit category": func(d *pa.ReadResponse) { d.Items[0].Observation.Measures[0].Category = "credit" },
	} {
		t.Run(name, func(t *testing.T) {
			doc := lastReportedRead()
			change(&doc)
			if got := MapProductAllowance(doc)[1]; got.LastReported != nil || got.Value == portal.AllowanceMeasured {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// A later failed attempt does not hide the last accepted reading.
func TestLastReportedSurvivesUnavailableStatus(t *testing.T) {
	doc := lastReportedRead()
	doc.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityUnavailable,
		Binding: pa.BindingRecord{State: pa.BindingVerified, AccountRef: allowanceString("acct-1"), Generation: 3}}}
	got := MapProductAllowance(doc)[1]
	if got.Value != portal.AllowanceUnknown || got.Reason != "Allowance unavailable" || len(got.LastReported) != 1 {
		t.Fatalf("got %+v", got)
	}
}

// A trusted current reading never also carries a last-reported line.
func TestLastReportedAbsentWhenMeasured(t *testing.T) {
	for _, got := range MapProductAllowance(allowanceRead()) {
		if got.LastReported != nil {
			t.Fatalf("%s: %+v", got.Provider, got.LastReported)
		}
	}
}
