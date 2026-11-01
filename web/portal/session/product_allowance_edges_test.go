package session

import (
	"reflect"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// Duplicate measure ids and duplicate current receipts stay covered by
// TestMapProductAllowanceRefusesUntrustedValues and
// TestMapProductAllowanceMeasureIDsAreProviderWide. Reclaim coverage stays in
// the controller reclaim tests. This file does not add a registry.

func edgeStr(v string) *string { return &v }

func edgeTypedSurface() bool {
	_, ok := reflect.TypeOf(portal.ProductAllowanceWindow{}).FieldByName("PercentKnown")
	return ok
}

type edgeFacts struct {
	Known                                      bool
	Value                                      float64
	Direction, ResetAt, ObservedAt, WindowKind string
}

func edgeFactsOf(t *testing.T, w portal.ProductAllowanceWindow) (edgeFacts, bool) {
	t.Helper()
	v := reflect.ValueOf(w)
	known := v.FieldByName("PercentKnown")
	if !known.IsValid() {
		return edgeFacts{}, false
	}
	facts := edgeFacts{Known: known.Bool()}
	for _, name := range []string{"PercentValue", "Direction", "ResetAt", "ObservedAt", "WindowKind"} {
		field := v.FieldByName(name)
		if !field.IsValid() {
			t.Fatalf("PercentKnown is present but %s is not", name)
		}
		switch name {
		case "PercentValue":
			facts.Value = field.Float()
		case "Direction":
			facts.Direction = field.String()
		case "ResetAt":
			facts.ResetAt = field.String()
		case "ObservedAt":
			facts.ObservedAt = field.String()
		case "WindowKind":
			facts.WindowKind = field.String()
		}
	}
	return facts, true
}

func edgeAccountKey(w portal.ProductAllowanceWindow) (string, bool) {
	field := reflect.ValueOf(w).FieldByName("AccountKey")
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", false
	}
	return field.String(), true
}

type edgeMeasure struct {
	ID, Decimal    string
	Direction      pa.Direction
	Minutes        int64
	Reset          string
	Precision      pa.ResetPrecision
	Current        pa.CurrentState
	CurrentDecimal string
	Ref            string
}

func edgeItem(provider, scopeRef, bindingRef, observed string, fresh pa.FreshnessState, measures ...edgeMeasure) pa.ReadItem {
	item := pa.ReadItem{
		Observation: pa.Observation{
			Provider: provider, Availability: pa.AvailabilityAvailable,
			Scope: pa.Scope{BindingState: pa.BindingVerified}, Source: pa.Source{Kind: pa.SourceNativeRPC},
		},
		Validation: pa.ValidationRecord{State: pa.ValidationSourceFields},
		Binding:    pa.BindingRecord{State: pa.BindingVerified},
		Freshness:  pa.Freshness{State: fresh, Basis: pa.BasisProvider},
	}
	if scopeRef != "" {
		item.Observation.Scope.AccountRef = edgeStr(scopeRef)
	}
	if bindingRef != "" {
		item.Binding.AccountRef = edgeStr(bindingRef)
	}
	if observed != "" {
		item.Observation.ProviderObservedAt = edgeStr(observed)
	}
	for _, m := range measures {
		minutes := pa.Count(m.Minutes)
		ref := m.Ref
		if ref == "" {
			ref = provider + "-" + m.ID
		}
		precision := m.Precision
		if precision == "" {
			precision = pa.ResetInstant
		}
		measure := pa.Measure{
			ID: m.ID, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: m.Direction,
			Value:  pa.MeasureValue{State: pa.ValueKnown, Decimal: edgeStr(m.Decimal)},
			Window: pa.Window{Ref: ref, DurationMinutes: &minutes, ResetPrecision: precision},
		}
		if m.Reset != "" {
			measure.Window.ResetAt = edgeStr(m.Reset)
		}
		item.Observation.Measures = append(item.Observation.Measures, measure)
		current := pa.CurrentMeasure{
			MeasureID: m.ID, State: pa.CurrentKnown,
			Value: pa.MeasureValue{State: pa.ValueKnown, Decimal: edgeStr(m.Decimal)},
		}
		if m.Current != "" {
			current.State = m.Current
		}
		switch {
		case current.State == pa.CurrentExpired || current.State == pa.CurrentUnknown || current.State == pa.CurrentConflict:
			current.Value = pa.MeasureValue{State: pa.ValueUnknown}
		case m.CurrentDecimal != "":
			current.Value.Decimal = edgeStr(m.CurrentDecimal)
		}
		item.CurrentMeasures = append(item.CurrentMeasures, current)
	}
	return item
}

func edgeDoc(items ...pa.ReadItem) pa.ReadResponse {
	return pa.ReadResponse{Schema: pa.SchemaRead, Items: items}
}

func edgeRow(rows []portal.ProductAllowance, provider string) (portal.ProductAllowance, bool) {
	for _, row := range rows {
		if row.Provider == provider {
			return row, true
		}
	}
	return portal.ProductAllowance{}, false
}

func edgeMust(t *testing.T, rows []portal.ProductAllowance, provider string) portal.ProductAllowance {
	t.Helper()
	row, ok := edgeRow(rows, provider)
	if !ok {
		t.Fatalf("%s missing from %d rows", provider, len(rows))
	}
	return row
}

func edgePercents(row portal.ProductAllowance) []string {
	out := make([]string, len(row.Windows))
	for i, w := range row.Windows {
		out[i] = w.Percent
	}
	return out
}

func TestMapProductAllowanceEdgePercentDirectionAndRange(t *testing.T) {
	doc := edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
		edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080},
		edgeMeasure{ID: "five-hour", Decimal: "38", Direction: pa.DirectionUsed, Minutes: 300},
	))
	row := edgeMust(t, MapProductAllowance(doc), "Claude")
	if row.Value != portal.AllowanceMeasured || len(row.Windows) != 2 {
		t.Fatalf("directions: %+v", row)
	}
	if row.Windows[0].Percent != "62% left" || row.Windows[1].Percent != "38% used" {
		t.Fatalf("complement was stored in the percent: %q %q", row.Windows[0].Percent, row.Windows[1].Percent)
	}
	t.Run("typed values", func(t *testing.T) {
		if !edgeTypedSurface() {
			t.Skip("typed allowance fields pending integration")
		}
		left, ok := edgeFactsOf(t, row.Windows[0])
		used, okUsed := edgeFactsOf(t, row.Windows[1])
		if !ok || !okUsed || !left.Known || !used.Known {
			t.Fatalf("known flags: %+v %+v", left, used)
		}
		if left.Direction != "remaining" || left.Value != 62 {
			t.Fatalf("remaining was complemented or dropped: %+v", left)
		}
		if used.Direction != "used" || used.Value != 38 {
			t.Fatalf("used direction: %+v", used)
		}
	})

	t.Run("real zero stays zero", func(t *testing.T) {
		zero := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "seven-day", Decimal: "0", Direction: pa.DirectionRemaining, Minutes: 10080},
		))), "Claude")
		if zero.Value != portal.AllowanceMeasured || len(zero.Windows) != 1 || zero.Windows[0].Percent != "0% left" {
			t.Fatalf("real zero: %+v", zero)
		}
		t.Run("typed", func(t *testing.T) {
			if !edgeTypedSurface() {
				t.Skip("typed allowance fields pending integration")
			}
			facts, ok := edgeFactsOf(t, zero.Windows[0])
			if !ok || !facts.Known || facts.Value != 0 || facts.Direction != "remaining" {
				t.Fatalf("typed zero: %+v", facts)
			}
		})
	})

	t.Run("out of range is not clamped", func(t *testing.T) {
		for _, decimal := range []string{"101", "100.000001", "-1"} {
			got := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
				edgeMeasure{ID: "five-hour", Decimal: "40", Direction: pa.DirectionRemaining, Minutes: 300},
				edgeMeasure{ID: "seven-day", Decimal: decimal, Direction: pa.DirectionRemaining, Minutes: 10080},
			))), "Claude")
			if got.Value != portal.AllowanceMeasured || len(got.Windows) != 2 || got.Windows[0].Percent != "40% left" || got.Windows[1].Percent != "Unknown" {
				t.Fatalf("decimal %s: %+v", decimal, got)
			}
			if strings.Contains(got.Windows[1].Percent, "100%") || strings.HasPrefix(got.Windows[1].Percent, "0%") {
				t.Fatalf("decimal %s was clamped: %+v", decimal, got.Windows[1])
			}
			if !edgeTypedSurface() {
				continue
			}
			facts, ok := edgeFactsOf(t, got.Windows[1])
			if !ok || facts.Known || facts.Value != 0 {
				t.Fatalf("decimal %s typed: %+v", decimal, facts)
			}
		}
		if !edgeTypedSurface() {
			t.Log("typed allowance fields pending integration")
		}
	})
}

func TestMapProductAllowanceEdgeMissingStaleAndConflictDoNotZero(t *testing.T) {
	t.Run("expired historical zero", func(t *testing.T) {
		row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "five-hour", Decimal: "40", Direction: pa.DirectionRemaining, Minutes: 300},
			edgeMeasure{ID: "seven-day", Decimal: "0", Direction: pa.DirectionRemaining, Minutes: 10080, Current: pa.CurrentExpired},
		))), "Claude")
		if row.Value != portal.AllowanceMeasured || len(row.Windows) != 2 || row.Windows[0].Percent != "40% left" || row.Windows[1].Percent != "Unknown" {
			t.Fatalf("expired neighbor: %+v", row)
		}
		if strings.HasPrefix(row.Windows[1].Percent, "0%") {
			t.Fatalf("expired historical zero was published: %+v", row.Windows[1])
		}
		if edgeTypedSurface() {
			facts, ok := edgeFactsOf(t, row.Windows[1])
			if !ok || facts.Known {
				t.Fatalf("expired became known: %+v", facts)
			}
		}
	})

	t.Run("stale keeps the last percent", func(t *testing.T) {
		row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2020-01-01T00:00:00Z", pa.FreshStale,
			edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Reset: "2020-06-01T00:00:00Z"},
		))), "Claude")
		if row.Value != portal.AllowanceStale || len(row.Windows) != 1 || row.Windows[0].Percent != "62% left" {
			t.Fatalf("stale: %+v", row)
		}
		if row.Windows[0].Reset != "1 Jun 00:00 UTC" || row.Windows[0].Observed != "1 Jan 00:00 UTC" {
			t.Fatalf("past times were cleared: %+v", row.Windows[0])
		}
		if edgeTypedSurface() {
			facts, ok := edgeFactsOf(t, row.Windows[0])
			if !ok || !facts.Known || facts.Value != 62 || facts.ResetAt != "2020-06-01T00:00:00Z" {
				t.Fatalf("stale typed: %+v", facts)
			}
		}
	})

	t.Run("conflict with zero publishes nothing", func(t *testing.T) {
		row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, CurrentDecimal: "0"},
		))), "Claude")
		if row.Value != portal.AllowanceUnknown || len(row.Windows) != 0 || row.Reason != "Conflicting allowance values" {
			t.Fatalf("conflict: %+v", row)
		}
	})

	t.Run("direct field cannot replace the measure", func(t *testing.T) {
		item := edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Ref: "weekly-plan"},
		)
		item.Observation.DirectFields = []pa.DirectField{{
			Field: "plan", OriginalType: pa.TypeString, LexicalValue: edgeStr("Weekly plan 0% left"),
		}}
		row := edgeMust(t, MapProductAllowance(edgeDoc(item)), "Claude")
		if row.Value != portal.AllowanceMeasured || len(row.Windows) != 1 || row.Windows[0].Percent != "62% left" || row.Windows[0].Window != "7 days" {
			t.Fatalf("direct field changed the window: %+v", row)
		}
		if strings.Contains(strings.ToLower(row.Windows[0].Window), "week") {
			t.Fatalf("window ref became a week: %+v", row.Windows[0])
		}
	})
}

func TestMapProductAllowanceEdgeResetOffsetDSTAndDisplayOnly(t *testing.T) {
	const observed = "2026-10-29T13:59:00Z"
	measure := func(reset string, precision pa.ResetPrecision) edgeMeasure {
		return edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Reset: reset, Precision: precision, Ref: "weekly-plan"}
	}
	at := func(reset string, precision pa.ResetPrecision) portal.ProductAllowanceWindow {
		t.Helper()
		row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", observed, pa.FreshFresh, measure(reset, precision)))), "Claude")
		if row.Value != portal.AllowanceMeasured || len(row.Windows) != 1 || row.Windows[0].Percent != "62% left" || row.Windows[0].Window != "7 days" {
			t.Fatalf("reset %s: %+v", reset, row)
		}
		return row.Windows[0]
	}

	equal := []portal.ProductAllowanceWindow{
		at("2026-11-02T09:00:00-05:00", pa.ResetInstant),
		at("2026-11-02T14:00:00Z", pa.ResetInstant),
	}
	for _, w := range equal {
		if w.Reset != "2 Nov 14:00 UTC" || strings.Contains(w.Reset, "display only") {
			t.Fatalf("equal instant display: %+v", w)
		}
	}
	dst := []portal.ProductAllowanceWindow{
		at("2026-11-01T01:30:00-04:00", pa.ResetInstant),
		at("2026-11-01T01:30:00-05:00", pa.ResetInstant),
	}
	if dst[0].Reset != "1 Nov 05:30 UTC" || dst[1].Reset != "1 Nov 06:30 UTC" || dst[0].Reset == dst[1].Reset {
		t.Fatalf("same civil clock collapsed: %q %q", dst[0].Reset, dst[1].Reset)
	}
	displayOnly := at("2026-11-02T09:00:00-05:00", pa.ResetDisplayOnly)
	if displayOnly.Reset != "2 Nov 14:00 UTC (display only)" {
		t.Fatalf("display only: %+v", displayOnly)
	}

	t.Run("typed instants", func(t *testing.T) {
		if !edgeTypedSurface() {
			t.Skip("typed allowance fields pending integration")
		}
		var instants []time.Time
		for _, w := range equal {
			facts, ok := edgeFactsOf(t, w)
			if !ok {
				t.Fatal("typed surface disappeared")
			}
			parsed, err := time.Parse(time.RFC3339Nano, facts.ResetAt)
			if err != nil || !parsed.Equal(time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)) {
				t.Fatalf("ResetAt %q: %v", facts.ResetAt, err)
			}
			instants = append(instants, parsed)
			if facts.WindowKind == "weekly" || facts.WindowKind == "week" || strings.Contains(facts.WindowKind, "month") {
				t.Fatalf("duration became %q", facts.WindowKind)
			}
		}
		if !instants[0].Equal(instants[1]) {
			t.Fatalf("offset and Z diverged: %s %s", instants[0], instants[1])
		}
		var clocks []time.Time
		for _, w := range dst {
			facts, _ := edgeFactsOf(t, w)
			parsed, err := time.Parse(time.RFC3339Nano, facts.ResetAt)
			if err != nil {
				t.Fatalf("dst ResetAt %q: %v", facts.ResetAt, err)
			}
			clocks = append(clocks, parsed)
		}
		if clocks[0].Equal(clocks[1]) || clocks[1].Sub(clocks[0]) != time.Hour {
			t.Fatalf("dst instants: %s %s", clocks[0], clocks[1])
		}
		facts, _ := edgeFactsOf(t, displayOnly)
		if facts.ResetAt != "" {
			t.Fatalf("display-only reset became authoritative: %q", facts.ResetAt)
		}
	})
}

func TestMapProductAllowanceEdgeEpochStringsAndYearCollision(t *testing.T) {
	for _, raw := range []string{"1762084800", "1762084800000", "1762084800.5"} {
		t.Run(raw, func(t *testing.T) {
			row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-claude", "acct-claude", raw, pa.FreshFresh,
				edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Reset: raw},
			))), "Claude")
			w := row.Windows
			if row.Value != portal.AllowanceMeasured || len(w) != 1 || w[0].Percent != "62% left" || w[0].Reset != "Unknown" || w[0].Observed != "Unknown" {
				t.Fatalf("epoch string: %+v", row)
			}
			if edgeTypedSurface() {
				facts, ok := edgeFactsOf(t, w[0])
				if !ok || facts.ResetAt != "" || facts.ObservedAt != "" {
					t.Fatalf("epoch string became an instant: %+v", facts)
				}
			}
		})
	}

	const display = "2 Nov 14:00 UTC"
	whole := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "", "", "2020-11-02T14:00:00Z", pa.FreshFresh,
		edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Reset: "2020-11-02T14:00:00Z"},
	))), "Claude").Windows[0]
	fraction := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "", "", "2099-11-02T14:00:00.500Z", pa.FreshFresh,
		edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Reset: "2099-11-02T14:00:00.500Z"},
	))), "Claude").Windows[0]
	if whole.Reset != display || fraction.Reset != display || whole.Observed != "2 Nov 14:00 UTC" || fraction.Observed != "2 Nov 14:00 UTC" {
		t.Fatalf("display collided incorrectly: %+v %+v", whole, fraction)
	}
	t.Run("typed fraction and year", func(t *testing.T) {
		if !edgeTypedSurface() {
			t.Skip("typed allowance fields pending integration")
		}
		older, ok := edgeFactsOf(t, whole)
		newer, okNewer := edgeFactsOf(t, fraction)
		if !ok || !okNewer {
			t.Fatal("typed surface disappeared")
		}
		oldReset, err := time.Parse(time.RFC3339Nano, older.ResetAt)
		newReset, errNew := time.Parse(time.RFC3339Nano, newer.ResetAt)
		oldSeen, errSeen := time.Parse(time.RFC3339Nano, older.ObservedAt)
		newSeen, errNewSeen := time.Parse(time.RFC3339Nano, newer.ObservedAt)
		if err != nil || errNew != nil || errSeen != nil || errNewSeen != nil {
			t.Fatalf("parse: %v %v %v %v (%q %q %q %q)", err, errNew, errSeen, errNewSeen, older.ResetAt, newer.ResetAt, older.ObservedAt, newer.ObservedAt)
		}
		if oldReset.Equal(newReset) || oldSeen.Equal(newSeen) || oldReset.Year() != 2020 || newReset.Year() != 2099 {
			t.Fatalf("display collision was treated as the same instant: %+v %+v", older, newer)
		}
		if !newReset.Equal(time.Date(2099, 11, 2, 14, 0, 0, 500000000, time.UTC)) {
			t.Fatalf("millisecond instant: %s", newReset)
		}
	})
}

func TestMapProductAllowanceEdgeAccountsDoNotBlend(t *testing.T) {
	shared := "shared-window-ref"
	rows := MapProductAllowance(edgeDoc(
		edgeItem("anthropic", "acct-claude", "acct-claude", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080, Ref: shared}),
		edgeItem("openai", "acct-codex", "acct-codex", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "five-hour", Decimal: "17", Direction: pa.DirectionUsed, Minutes: 300, Ref: shared}),
	))
	claude, codex := edgeMust(t, rows, "Claude"), edgeMust(t, rows, "Codex")
	if claude.Value != portal.AllowanceMeasured || codex.Value != portal.AllowanceMeasured {
		t.Fatalf("providers: %+v %+v", claude, codex)
	}
	if edgePercents(claude)[0] != "62% left" || claude.Windows[0].Window != "7 days" || len(claude.Windows) != 1 {
		t.Fatalf("claude window: %+v", claude)
	}
	if edgePercents(codex)[0] != "17% used" || codex.Windows[0].Window != "5 hours" || len(codex.Windows) != 1 {
		t.Fatalf("codex window: %+v", codex)
	}
	if cursor, ok := edgeRow(rows, "Cursor"); ok && (cursor.Value == portal.AllowanceMeasured || len(cursor.Windows) != 0) {
		t.Fatalf("cursor gained a provider window: %+v", cursor)
	}
	if key, ok := edgeAccountKey(claude.Windows[0]); ok && key != "acct-claude" {
		t.Fatalf("claude account key: %q", key)
	}
	if key, ok := edgeAccountKey(codex.Windows[0]); ok && key != "acct-codex" {
		t.Fatalf("codex account key: %q", key)
	}

	t.Run("same account keeps both windows", func(t *testing.T) {
		row := edgeMust(t, MapProductAllowance(edgeDoc(edgeItem("anthropic", "acct-one", "acct-one", "2026-10-29T13:59:00Z", pa.FreshFresh,
			edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080},
			edgeMeasure{ID: "five-hour", Decimal: "11", Direction: pa.DirectionRemaining, Minutes: 300},
		))), "Claude")
		if len(row.Windows) != 2 || row.Windows[0].Percent != "62% left" || row.Windows[1].Percent != "11% left" {
			t.Fatalf("windows blended: %+v", row)
		}
	})

	t.Run("distinct accounts are not averaged", func(t *testing.T) {
		row := edgeMust(t, MapProductAllowance(edgeDoc(
			edgeItem("anthropic", "acct-one", "acct-one", "2026-10-29T13:59:00Z", pa.FreshFresh,
				edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080}),
			edgeItem("anthropic", "acct-two", "acct-two", "2026-10-29T13:59:00Z", pa.FreshFresh,
				edgeMeasure{ID: "five-hour", Decimal: "17", Direction: pa.DirectionRemaining, Minutes: 300}),
		)), "Claude")
		switch len(row.Windows) {
		case 0:
			if row.Value == portal.AllowanceMeasured {
				t.Fatalf("measured empty blend: %+v", row)
			}
		case 1:
			if row.Windows[0].Percent != "62% left" && row.Windows[0].Percent != "17% left" {
				t.Fatalf("single window is not one of the accounts: %+v", row)
			}
		case 2:
			if row.Windows[0].Percent != "62% left" || row.Windows[1].Percent != "17% left" {
				t.Fatalf("accounts blended: %+v", row)
			}
			if key, ok := edgeAccountKey(row.Windows[0]); ok {
				other, _ := edgeAccountKey(row.Windows[1])
				if key == "" || other == "" || key == other || (key != "acct-one" && key != "acct-two") {
					t.Fatalf("account keys: %q %q", key, other)
				}
			}
		default:
			t.Fatalf("unexpected windows: %+v", row)
		}
	})

	t.Run("mismatched refs do not join accounts", func(t *testing.T) {
		got := MapProductAllowance(edgeDoc(
			edgeItem("anthropic", "acct-a", "acct-b", "2026-10-29T13:59:00Z", pa.FreshFresh,
				edgeMeasure{ID: "seven-day", Decimal: "62", Direction: pa.DirectionRemaining, Minutes: 10080}),
			edgeItem("openai", "acct-c", "acct-c", "2026-10-29T13:59:00Z", pa.FreshFresh,
				edgeMeasure{ID: "five-hour", Decimal: "17", Direction: pa.DirectionUsed, Minutes: 300}),
		))
		claude, codex := edgeMust(t, got, "Claude"), edgeMust(t, got, "Codex")
		for _, w := range claude.Windows {
			if w.Percent != "62% left" {
				t.Fatalf("mismatched claude window: %+v", claude)
			}
			if key, ok := edgeAccountKey(w); ok && key != "" {
				t.Fatalf("mismatched refs invented account key %q", key)
			}
		}
		if len(codex.Windows) != 1 || codex.Windows[0].Percent != "17% used" {
			t.Fatalf("codex changed: %+v", codex)
		}
		if key, ok := edgeAccountKey(codex.Windows[0]); ok && key != "acct-c" {
			t.Fatalf("codex key: %q", key)
		}
	})
}
