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
		ReadAt: "2 Nov 13:40 UTC", ReadAtExact: "2026-11-02T13:40:00Z", ResetAtExact: "2026-11-11T09:22:09Z",
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
	if got := MapProductAllowance(doc)[1].LastReported; len(got) != 1 || got[0].Account != "Account not verified" || got[0].Why != "Not current or trusted: this reading's account is not verified." {
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
		"status binding mismatch": func(d *pa.ReadResponse) {
			d.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityAvailable,
				Binding: pa.BindingRecord{State: pa.BindingMismatch, AccountRef: allowanceString("acct-1"), Generation: 3}}}
		},
		"status validation conflict": func(d *pa.ReadResponse) {
			d.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityAvailable, Validation: pa.ValidationRecord{State: pa.ValidationConflict},
				Binding: pa.BindingRecord{State: pa.BindingVerified, AccountRef: allowanceString("acct-1"), Generation: 3}}}
		},
		"status validation rejected": func(d *pa.ReadResponse) {
			d.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityAvailable, Validation: pa.ValidationRecord{State: pa.ValidationRejected},
				Binding: pa.BindingRecord{State: pa.BindingVerified, AccountRef: allowanceString("acct-1"), Generation: 3}}}
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

// A status whose binding is unverified makes the line unverified too.
func TestLastReportedStatusBindingLowersConfidence(t *testing.T) {
	doc := lastReportedRead()
	doc.SourceStatuses = []pa.SourceStatus{{Provider: "openai", Availability: pa.AvailabilityAvailable,
		Binding: pa.BindingRecord{State: pa.BindingUnknown, AccountRef: allowanceString("acct-1"), Generation: 3}}}
	got := MapProductAllowance(doc)[1]
	if len(got.LastReported) != 1 || got.LastReported[0].Account != "Account not verified" {
		t.Fatalf("got %+v", got)
	}
}

// Aliases and uncapped meters are not their own reading; remaining keeps
// its direction; a display-only reset stays a label; the provider time is
// carried only when the provider reported one.
func TestLastReportedMeasureShapes(t *testing.T) {
	doc := lastReportedRead()
	item := &doc.Items[0]
	item.Observation.ProviderObservedAt = allowanceString("2026-11-02T13:39:00Z")
	base := item.Observation.Measures[0]
	left := base
	left.ID, left.Direction, left.Value.Decimal = "left", pa.DirectionRemaining, allowanceString("62")
	left.Window = pa.Window{Ref: "l", DurationMinutes: allowanceMinutes(300), ResetAt: allowanceString("2026-11-02T18:00:00Z"), ResetPrecision: pa.ResetDisplayOnly}
	alias, uncapped := base, base
	alias.ID, uncapped.ID = "alias", "uncapped"
	item.Observation.Measures = append(item.Observation.Measures, left, alias, uncapped)
	item.CurrentMeasures = append(item.CurrentMeasures,
		pa.CurrentMeasure{MeasureID: "alias", State: pa.CurrentNotApplicable, Value: pa.MeasureValue{State: pa.ValueUnknown}},
		pa.CurrentMeasure{MeasureID: "uncapped", State: pa.CurrentUncapped, Value: pa.MeasureValue{State: pa.ValueUnknown}})
	got := MapProductAllowance(doc)[1].LastReported
	if len(got) != 2 || got[0].Percent != "0% used" || got[1].Percent != "62% left" {
		t.Fatalf("got %+v", got)
	}
	if got[0].ResetAtExact != "2026-11-11T09:22:09Z" || got[0].ProviderAtExact != "2026-11-02T13:39:00Z" || got[0].ProviderAt != "2 Nov 13:39 UTC" {
		t.Fatalf("instant line %+v", got[0])
	}
	if got[1].ResetAtExact != "" || got[1].Reset != "Reported reset: 2 Nov 18:00 UTC (display only)." {
		t.Fatalf("display-only line %+v", got[1])
	}
}

// More than four windows pass through the mapper; the view bounds them.
func TestLastReportedManyWindowsReachTheView(t *testing.T) {
	doc := lastReportedRead()
	item := &doc.Items[0]
	for i := 0; i < 5; i++ {
		m := item.Observation.Measures[0]
		m.ID = "extra-" + string(rune('a'+i))
		item.Observation.Measures = append(item.Observation.Measures, m)
	}
	if got := MapProductAllowance(doc)[1].LastReported; len(got) != 6 {
		t.Fatalf("mapper kept %d", len(got))
	}
}
