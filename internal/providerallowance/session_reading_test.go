package providerallowance

import (
	"encoding/json"
	"testing"
	"time"
)

// Session readings here are synthetic (DESIGN-059 4.1, 4.2): a weekly window
// and a five-hour window with made-up percents.

var sessionAt = time.Date(2026, 11, 9, 23, 42, 10, 0, time.UTC)

func sessionWindows() []SessionWindow {
	return []SessionWindow{
		{Minutes: 10080, UsedPercent: "34", ResetAt: sessionAt.Add(60 * time.Hour)},
		{Minutes: 300, UsedPercent: "12.5", ResetAt: sessionAt.Add(2 * time.Hour)},
	}
}

func sessionDoc(t *testing.T, provider string) ReadResponse {
	t.Helper()
	item, status, err := SessionReadingItem(provider, sessionAt, sessionWindows())
	if err != nil {
		t.Fatal(err)
	}
	return ReadResponse{Schema: SchemaRead, SnapshotRevision: "r", ServedAt: sessionAt.Format(time.RFC3339Nano), Items: []ReadItem{item}, SourceStatuses: []SourceStatus{status}}
}

func TestSessionReadingItem(t *testing.T) {
	doc := sessionDoc(t, "anthropic")
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeReadResponse(raw, FixtureParseLimits); err != nil {
		t.Fatalf("the served document does not decode: %v", err)
	}
	item := doc.Items[0]
	obs := item.Observation
	if obs.Source != (Source{Kind: SourcePassive, Version: SessionSourceVersion, CapabilityRef: "claude-statusline:session-reading"}) || obs.ProviderObservedAt != nil || obs.CapturedAt != item.ReceivedAt {
		t.Fatalf("observation %+v", obs)
	}
	if len(obs.Measures) != 2 || obs.Measures[0].ID != "five_hour" || obs.Measures[1].ID != "seven_day" || *obs.Measures[1].Value.Decimal != "34" || *obs.Measures[1].Window.ResetAt != "2026-11-12T11:42:10Z" {
		t.Fatalf("measures %+v", obs.Measures)
	}
	if item.Binding.State != BindingUnknown || item.Validation.State != ValidationDisplayOnly || item.Freshness.State != FreshUnknown || item.Freshness.Basis != BasisCapture {
		t.Fatalf("labels %+v %+v %+v", item.Binding, item.Validation, item.Freshness)
	}
	for _, c := range item.CurrentMeasures {
		if c.State != CurrentUnknown || c.Value.State != ValueUnknown {
			t.Fatalf("a current value %+v", c)
		}
	}
	if s := doc.SourceStatuses[0]; s.ProducerRef != SessionProducerRef || s.LastGood == nil || s.LastGood.ObservationID != obs.ObservationID || s.Binding.State != BindingUnknown {
		t.Fatalf("status %+v", s)
	}
	for name, w := range map[string][]SessionWindow{
		"no windows":    nil,
		"three windows": append(sessionWindows(), SessionWindow{Minutes: 60, UsedPercent: "1", ResetAt: sessionAt.Add(time.Hour)}),
		"over 100":      {{Minutes: 300, UsedPercent: "101", ResetAt: sessionAt.Add(time.Hour)}},
		"reset before":  {{Minutes: 300, UsedPercent: "1", ResetAt: sessionAt.Add(-time.Hour)}},
		"no length":     {{Minutes: 0, UsedPercent: "1", ResetAt: sessionAt.Add(time.Hour)}},
	} {
		if _, _, err := SessionReadingItem("openai", sessionAt, w); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := SessionReadingItem("cursor", sessionAt, sessionWindows()); err == nil {
		t.Error("cursor accepted")
	}
}

// The built-in rule: a session item keeps its values for display, its binding
// stays Unknown and nothing is current, whatever private contexts the caller
// holds and whatever labels the payload claims.
func TestProjectCachedReadSessionRule(t *testing.T) {
	trusted := TrustedContext{ProducerRef: SessionProducerRef, ProducerIncarnation: SessionSourceVersion, Provider: "anthropic", BindingState: BindingVerified, SourceKind: SourcePassive, SourceVersion: SessionSourceVersion}
	for name, contexts := range map[string][]TrustedContext{"no contexts": nil, "a context that names the session source": {trusted}} {
		t.Run(name, func(t *testing.T) {
			doc := sessionDoc(t, "anthropic")
			// A payload that claims more than the rule allows.
			doc.Items[0].Binding.State = BindingVerified
			doc.Items[0].Validation.State = ValidationSourceFields
			doc.Items[0].Freshness = Freshness{State: FreshFresh, AsOf: doc.Items[0].ReceivedAt, Basis: BasisProvider}
			doc.Items[0].CurrentMeasures[1] = CurrentMeasure{MeasureID: "seven_day", State: CurrentKnown, Value: doc.Items[0].Observation.Measures[1].Value}
			doc.SourceStatuses[0].Binding.State = BindingVerified
			ProjectCachedRead(&doc, contexts, nil, 30*time.Minute, true, sessionAt.Add(time.Minute))
			item, status := doc.Items[0], doc.SourceStatuses[0]
			if item.Binding.State != BindingUnknown || item.Observation.Scope.BindingState != BindingUnknown || item.Validation.State != ValidationDisplayOnly || item.Freshness.State != FreshUnknown || item.CollectionFreshness != nil {
				t.Fatalf("item %+v", item)
			}
			for _, c := range item.CurrentMeasures {
				if c.State == CurrentKnown || c.State == CurrentUncapped || c.Value.State != ValueUnknown {
					t.Fatalf("current %+v", c)
				}
			}
			if *item.Observation.Measures[1].Value.Decimal != "34" {
				t.Fatal("the displayed value was dropped")
			}
			if status.Binding.State != BindingUnknown || status.Validation.State != ValidationDisplayOnly || status.Freshness.State != FreshUnknown {
				t.Fatalf("status %+v", status)
			}
		})
	}
	t.Run("a passed reset is expired", func(t *testing.T) {
		doc := sessionDoc(t, "openai")
		ProjectCachedRead(&doc, nil, nil, 0, false, sessionAt.Add(3*time.Hour))
		if c := doc.Items[0].CurrentMeasures; c[0].State != CurrentExpired || c[1].State != CurrentUnknown {
			t.Fatalf("currents %+v", c)
		}
	})
}

func TestProjectCachedReadRefusesForgedSessionItems(t *testing.T) {
	account := "acct-1"
	observed := sessionAt.Format(time.RFC3339)
	for name, mutate := range map[string]func(*ReadResponse){
		"an account":            func(d *ReadResponse) { d.Items[0].Observation.Scope.AccountRef = &account },
		"a provider time":       func(d *ReadResponse) { d.Items[0].Observation.ProviderObservedAt = &observed },
		"another producer":      func(d *ReadResponse) { d.Items[0].Observation.ProducerRef = "someone" },
		"another capability":    func(d *ReadResponse) { d.Items[0].Observation.Source.CapabilityRef = "claude-sdk:usage-report" },
		"a binding generation":  func(d *ReadResponse) { d.Items[0].Observation.BindingGeneration = 2 },
		"a percent over 100":    func(d *ReadResponse) { v := "150"; d.Items[0].Observation.Measures[0].Value.Decimal = &v },
		"a remaining direction": func(d *ReadResponse) { d.Items[0].Observation.Measures[0].Direction = DirectionRemaining },
		"a direct field": func(d *ReadResponse) {
			v := "x"
			d.Items[0].Observation.DirectFields = []DirectField{{Field: "plan", OriginalType: TypeString, LexicalValue: &v}}
		},
		"a reset before capture": func(d *ReadResponse) {
			v := "2026-11-01T00:00:00Z"
			d.Items[0].Observation.Measures[0].Window.ResetAt = &v
		},
		"a conflict availability": func(d *ReadResponse) { d.Items[0].Observation.Availability = AvailabilityConflict },
		"a window over 31 days": func(d *ReadResponse) {
			m := Count(32 * 24 * 60)
			d.Items[0].Observation.Measures[0].Window.DurationMinutes = &m
		},
		"a reset past one window": func(d *ReadResponse) {
			v := "2026-11-20T00:00:00Z"
			d.Items[0].Observation.Measures[0].Window.ResetAt = &v
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc := sessionDoc(t, "anthropic")
			mutate(&doc)
			ProjectCachedRead(&doc, nil, nil, 0, false, sessionAt.Add(time.Minute))
			if doc.Items[0].Validation.State != ValidationRejected || doc.Items[0].Binding.State != BindingMismatch {
				t.Fatalf("item %+v", doc.Items[0])
			}
			if doc.SourceStatuses[0].Validation.State != ValidationRejected {
				t.Fatalf("status %+v", doc.SourceStatuses[0])
			}
		})
	}
}

// Every other item keeps the private trust contexts: adding a session item
// for one provider leaves another provider's projection exactly as it was.
func TestProjectCachedReadSessionRuleLeavesOthers(t *testing.T) {
	other := sessionDoc(t, "openai")
	other.Items[0].Observation.Source = Source{Kind: SourceNativeRPC, Version: CodexRPCSourceVersion, CapabilityRef: CodexRPCCapability}
	other.Items[0].Observation.ProducerRef = "collector"
	other.SourceStatuses[0].ProducerRef = "collector"
	alone := ReadResponse{Schema: SchemaRead, ServedAt: other.ServedAt, Items: []ReadItem{other.Items[0]}, SourceStatuses: []SourceStatus{other.SourceStatuses[0]}}
	mixed := sessionDoc(t, "anthropic")
	mixed.Items = append(mixed.Items, other.Items[0])
	mixed.SourceStatuses = append(mixed.SourceStatuses, other.SourceStatuses[0])
	raw, _ := json.Marshal(alone)
	var aloneCopy ReadResponse
	_ = json.Unmarshal(raw, &aloneCopy)
	now := sessionAt.Add(time.Minute)
	ProjectCachedRead(&aloneCopy, nil, nil, 0, false, now)
	ProjectCachedRead(&mixed, nil, nil, 0, false, now)
	a, _ := json.Marshal(aloneCopy.Items[0])
	b, _ := json.Marshal(mixed.Items[1])
	sa, _ := json.Marshal(aloneCopy.SourceStatuses[0])
	sb, _ := json.Marshal(mixed.SourceStatuses[1])
	if string(a) != string(b) || string(sa) != string(sb) {
		t.Fatalf("the other provider changed:\n%s\n%s\n%s\n%s", a, b, sa, sb)
	}
	if aloneCopy.Items[0].Validation.State != ValidationRejected {
		t.Fatal("an untrusted ordinary item was not rejected as before")
	}
}

// RejectSessionReadings labels only session-v1 items rejected, without
// writing the caller's backing array; the built-in rule then keeps the item
// and its status rejected, and a second projection does not lift it.
func TestRejectSessionReadings(t *testing.T) {
	doc := sessionDoc(t, "anthropic")
	other := sessionDoc(t, "openai").Items[0]
	other.Observation.Source = Source{Kind: SourceNativeRPC, Version: CodexRPCSourceVersion, CapabilityRef: CodexRPCCapability}
	doc.Items = append(doc.Items, other)
	original := doc.Items
	RejectSessionReadings(&doc)
	if original[0].Validation.State != ValidationDisplayOnly {
		t.Fatal("the caller's items were written")
	}
	if doc.Items[0].Validation.State != ValidationRejected || doc.Items[1].Validation.State != other.Validation.State {
		t.Fatalf("labels %v %v", doc.Items[0].Validation.State, doc.Items[1].Validation.State)
	}
	for i := 0; i < 2; i++ {
		ProjectCachedRead(&doc, nil, nil, 0, false, sessionAt.Add(time.Minute))
		if doc.Items[0].Validation.State != ValidationRejected || doc.Items[0].Binding.State != BindingMismatch || doc.SourceStatuses[0].Validation.State != ValidationRejected {
			t.Fatalf("projection %d: item %+v status %+v", i, doc.Items[0].Validation, doc.SourceStatuses[0].Validation)
		}
	}
	RejectSessionReadings(nil)
}
