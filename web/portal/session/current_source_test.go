package session

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	currentSourceIncarnation = "synthetic-january-2026"
	currentSourceRetrieved   = "2026-01-15T12:00:00Z"
	currentSourceResets      = "2026-01-15T17:00:00Z"
	currentSourceUpdated     = "2026-01-15T11:45:00.500Z"
)

func TestCurrentSourceProjectsJanuaryReport(t *testing.T) {
	raw := januaryCurrentSourceRaw("40", "60", "")
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	assertCodexCurrentSource(t, NewCurrentSourceReader(raw, januarySelection()).Allowances(now), "40% used, 60% remaining (derived)", 40, now, "")

	explicit := januaryCurrentSourceRaw("40.0", "60.0", "")
	assertCodexCurrentSource(t, NewCurrentSourceReader(explicit, januarySelection()).Allowances(now), "40.0% used, 60.0% remaining (derived)", 40, now, "")

	updated := januaryCurrentSourceRaw("40", "60", currentSourceUpdated)
	assertCodexCurrentSource(t, NewCurrentSourceReader(updated, januarySelection()).Allowances(now), "40% used, 60% remaining (derived)", 40, now, currentSourceUpdated)

	equalNow := time.Date(2026, 1, 15, 12, 30, 0, 0, time.UTC)
	assertCodexCurrentSource(t, NewCurrentSourceReader(raw, januarySelection()).Allowances(equalNow), "40% used, 60% remaining (derived)", 40, equalNow, "")

	beforeReset := time.Date(2026, 1, 15, 16, 59, 59, 0, time.UTC)
	wide := januarySelection()
	wide.MaxAge = 6 * time.Hour
	assertCodexCurrentSource(t, NewCurrentSourceReader(raw, wide).Allowances(beforeReset), "40% used, 60% remaining (derived)", 40, beforeReset, "")
}

func TestCurrentSourceAcceptsReportedZeroAndFull(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	zero := januaryCurrentSourceRaw("0", "100", "")
	assertCodexCurrentSource(t, NewCurrentSourceReader(zero, januarySelection()).Allowances(now), "0% used, 100% remaining (derived)", 0, now, "")
	full := januaryCurrentSourceRaw("100", "0", "")
	assertCodexCurrentSource(t, NewCurrentSourceReader(full, januarySelection()).Allowances(now), "100% used, 0% remaining (derived)", 100, now, "")
}

func TestCurrentSourceRefusesInvalidSnapshot(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	raw := januaryCurrentSourceRaw("40", "60", "")
	retrieved := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 1, 15, 17, 0, 0, 0, time.UTC)
	wide := januarySelection()
	wide.MaxAge = 6 * time.Hour
	publisher := januarySelection()
	publisher.PublisherRef = "other-publisher"
	incarnation := januarySelection()
	incarnation.ProducerIncarnation = "other-incarnation"
	generation := januarySelection()
	generation.SourceGeneration = 2

	var bare *currentSourceReader
	if !allowanceEqual(bare.Allowances(now), disconnectedAllowances()) {
		t.Fatalf("nil reader %+v", bare.Allowances(now))
	}

	cases := []struct {
		name string
		raw  []byte
		sel  pa.CurrentSourceSelection
		now  time.Time
	}{
		{name: "empty", raw: nil, sel: januarySelection(), now: now},
		{name: "blank", raw: []byte{}, sel: januarySelection(), now: now},
		{name: "truncated", raw: []byte("{"), sel: januarySelection(), now: now},
		{name: "extra", raw: withExtraField(t, raw), sel: januarySelection(), now: now},
		{name: "bound-document", raw: []byte(`{"schema":"providerallowance.read.v1"}`), sel: januarySelection(), now: now},
		{name: "zero-now", raw: raw, sel: januarySelection(), now: time.Time{}},
		{name: "publisher", raw: raw, sel: publisher, now: now},
		{name: "incarnation", raw: raw, sel: incarnation, now: now},
		{name: "generation", raw: raw, sel: generation, now: now},
		{name: "future-retrieval", raw: bytes.Replace(raw, []byte(currentSourceRetrieved), []byte("2026-01-15T12:30:00Z"), 1), sel: januarySelection(), now: now},
		{name: "max-age-overflow", raw: raw, sel: januarySelection(), now: retrieved.Add(30*time.Minute + time.Nanosecond)},
		{name: "past-reset", raw: raw, sel: wide, now: reset},
		{name: "reset-after", raw: raw, sel: wide, now: reset.Add(time.Second)},
		{name: "percent-sum", raw: bytes.Replace(raw, []byte(`"remainingPercent":"60"`), []byte(`"remainingPercent":"50"`), 1), sel: januarySelection(), now: now},
		{name: "zero-duration", raw: bytes.Replace(raw, []byte(`"windowDurationMinutes":300`), []byte(`"windowDurationMinutes":0`), 1), sel: januarySelection(), now: now},
		{name: "fractional-duration", raw: bytes.Replace(raw, []byte(`"windowDurationMinutes":300`), []byte(`"windowDurationMinutes":300.5`), 1), sel: januarySelection(), now: now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertCurrentSourceRefused(t, NewCurrentSourceReader(tc.raw, tc.sel).Allowances(tc.now))
		})
	}
}

func TestCurrentSourceKeepsRetrievalAndDropsExpiredPercent(t *testing.T) {
	raw := januaryCurrentSourceRaw("40", "60", "")
	reader := NewCurrentSourceReader(raw, januarySelection())
	early := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	expired := early.Add(31 * time.Minute)

	first := reader.Allowances(early)
	assertCodexCurrentSource(t, first, "40% used, 60% remaining (derived)", 40, early, "")
	assertCurrentSourceRefused(t, reader.Allowances(expired))
	third := reader.Allowances(early)
	assertCodexCurrentSource(t, third, "40% used, 60% remaining (derived)", 40, early, "")
	if first[1].Windows[0].RetrievedAt != third[1].Windows[0].RetrievedAt || third[1].Windows[0].RetrievedAt != currentSourceRetrieved {
		t.Fatalf("retrieval changed: %+v %+v", first[1].Windows[0], third[1].Windows[0])
	}

	updatedRaw := januaryCurrentSourceRaw("40", "60", currentSourceUpdated)
	updated := NewCurrentSourceReader(updatedRaw, januarySelection())
	later := early.Add(5 * time.Minute)
	seen := updated.Allowances(early)
	again := updated.Allowances(later)
	assertCodexCurrentSource(t, seen, "40% used, 60% remaining (derived)", 40, early, currentSourceUpdated)
	assertCodexCurrentSource(t, again, "40% used, 60% remaining (derived)", 40, later, currentSourceUpdated)
	if seen[1].Windows[0].RetrievedAt != again[1].Windows[0].RetrievedAt || seen[1].Windows[0].ObservedAt != again[1].Windows[0].ObservedAt || again[1].Windows[0].RetrievedAt != currentSourceRetrieved || again[1].Windows[0].ObservedAt != currentSourceUpdated {
		t.Fatalf("times moved: %+v %+v", seen[1].Windows[0], again[1].Windows[0])
	}
	if !again[1].Windows[0].AsOf.Equal(later) || again[1].Windows[0].AsOf.Equal(early) {
		t.Fatalf("as of %+v", again[1].Windows[0].AsOf)
	}
}

func TestCurrentSourceIgnoresCallerMutation(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	raw := januaryCurrentSourceRaw("40", "60", "")
	original := append([]byte(nil), raw...)
	sel := januarySelection()
	reader := NewCurrentSourceReader(raw, sel)
	for i := range raw {
		raw[i] = 'x'
	}
	sel.PublisherRef = "other-publisher"
	sel.ProducerIncarnation = "other-incarnation"
	sel.SourceGeneration = 44
	sel.MaxAge = time.Nanosecond

	assertCodexCurrentSource(t, reader.Allowances(now), "40% used, 60% remaining (derived)", 40, now, "")
	held := reader.(*currentSourceReader)
	if !bytes.Equal(held.raw, original) || bytes.Equal(held.raw, raw) {
		t.Fatal("reader kept the caller buffer")
	}
	if held.selection.PublisherRef != pa.CurrentSourcePublisher || held.selection.ProducerIncarnation != currentSourceIncarnation || held.selection.SourceGeneration != 1 || held.selection.MaxAge != 30*time.Minute {
		t.Fatalf("selection aliased: %+v", held.selection)
	}
}

func TestCurrentSourceSessionUsageAgrees(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	raw := januaryCurrentSourceRaw("40", "60", "")
	assertClosedCurrentSource(t, raw)
	var src SessionSource
	src.SetAllowanceReader(NewCurrentSourceReader(raw, januarySelection()))
	page := applyCachedAllowance(portal.ProductPage{Usage: &portal.ProductUsage{}}, src.allowance, func() time.Time { return now })
	assertSameAllowance(t, page)
	assertCodexCurrentSource(t, page.Allowance, "40% used, 60% remaining (derived)", 40, now, "")
	assertCodexCurrentSource(t, page.Usage.Allowance, "40% used, 60% remaining (derived)", 40, now, "")
}

func januarySelection() pa.CurrentSourceSelection {
	return pa.CurrentSourceSelection{
		PublisherRef:        pa.CurrentSourcePublisher,
		ProducerIncarnation: currentSourceIncarnation,
		SourceGeneration:    1,
		MaxAge:              30 * time.Minute,
	}
}

func januaryCurrentSourceRaw(used, remaining, updated string) []byte {
	provider := "null"
	if updated != "" {
		provider = `"` + updated + `"`
	}
	body := `{"schema":"` + pa.CurrentSourceSchema + `",` +
		`"publisherRef":"` + pa.CurrentSourcePublisher + `",` +
		`"producerIncarnation":"` + currentSourceIncarnation + `",` +
		`"sourceGeneration":1,` +
		`"retrievedAt":"` + currentSourceRetrieved + `",` +
		`"providerUpdatedAt":` + provider + `,` +
		`"usedPercent":"` + used + `",` +
		`"remainingPercent":"` + remaining + `",` +
		`"remainingPercentBasis":"derived_complement_of_used_percent",` +
		`"windowDurationMinutes":300,` +
		`"resetsAt":"` + currentSourceResets + `"}`
	return []byte(body)
}

func withExtraField(t *testing.T, raw []byte) []byte {
	t.Helper()
	if len(raw) == 0 || raw[len(raw)-1] != '}' {
		t.Fatalf("raw %s", raw)
	}
	out := make([]byte, 0, len(raw)+len(`,"extra":true`))
	out = append(out, raw[:len(raw)-1]...)
	out = append(out, `,"extra":true`...)
	return append(out, '}')
}

func assertClosedCurrentSource(t *testing.T, raw []byte) {
	t.Helper()
	if !bytes.Contains(raw, []byte(`"providerUpdatedAt":null`)) {
		t.Fatalf("raw %s", raw)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	want := []string{"schema", "publisherRef", "producerIncarnation", "sourceGeneration", "retrievedAt", "providerUpdatedAt", "usedPercent", "remainingPercent", "windowDurationMinutes", "resetsAt", "remainingPercentBasis"}
	if len(keys) != len(want) {
		t.Fatalf("key count %d raw %s", len(keys), raw)
	}
	for _, key := range want {
		if _, ok := keys[key]; !ok {
			t.Fatalf("missing %s in %s", key, raw)
		}
	}
}

func assertCodexCurrentSource(t *testing.T, rows []portal.ProductAllowance, percent string, used float64, now time.Time, updated string) {
	t.Helper()
	assertOtherProvidersDisconnected(t, rows)
	row := rows[1]
	if row.Value != portal.AllowanceCurrentSource || row.Value == portal.AllowanceMeasured || row.Reason != currentSourceIdentityReason || row.Provenance != currentSourceProvenance || len(row.Windows) != 1 {
		t.Fatalf("codex %+v", row)
	}
	w := row.Windows[0]
	retrieved := currentSourceRetrieved
	if w.Percent != percent || strings.Contains(w.Percent, "left") || !w.PercentKnown || w.PercentValue != used || w.Direction != "used" || w.Window != "5 hours" || w.WindowKind != "" || w.AccountKey != "" || w.ResetPrecision != "instant" || w.Reset != "15 Jan 17:00 UTC" || w.ResetAt != currentSourceResets || w.RetrievedAt != currentSourceRetrieved || !w.AsOf.Equal(now) || w.Provenance != currentSourceProvenance {
		t.Fatalf("window %+v", w)
	}
	if w.Observed == allowanceTime(&retrieved) || w.ObservedAt == currentSourceRetrieved || w.RetrievedAt == now.Format(time.RFC3339) {
		t.Fatalf("retrieval used as observed or as of: %+v", w)
	}
	if updated == "" {
		if w.Observed != "Unknown" || w.ObservedAt != "" {
			t.Fatalf("missing provider time %+v", w)
		}
	} else if w.ObservedAt != updated || w.Observed != allowanceTime(&updated) || w.Observed == "Unknown" {
		t.Fatalf("provider time %+v", w)
	}
	for _, other := range rows {
		if other.Value == portal.AllowanceMeasured || other.Value == portal.AllowanceStale {
			t.Fatalf("measured %+v", other)
		}
	}
	blob := allowanceBlob(rows)
	if strings.Contains(blob, "Measured") || strings.Contains(blob, currentSourceIncarnation) || strings.Contains(blob, pa.CurrentSourcePublisher) {
		t.Fatalf("identity in allowance %s", blob)
	}
}

func assertCurrentSourceRefused(t *testing.T, rows []portal.ProductAllowance) {
	t.Helper()
	assertOtherProvidersDisconnected(t, rows)
	row := rows[1]
	if row.Value != portal.AllowanceUnknown || row.Reason != currentSourceUnavailableReason || row.Provenance != "" || len(row.Windows) != 0 {
		t.Fatalf("codex %+v", row)
	}
	blob := allowanceBlob(rows)
	if strings.ContainsAny(blob, "0123456789") || strings.Contains(blob, currentSourceIncarnation) || strings.Contains(blob, pa.CurrentSourcePublisher) || strings.Contains(blob, "Measured") || strings.Contains(blob, "providerallowance") || strings.Contains(blob, "extra") || strings.Contains(blob, "other-publisher") || strings.Contains(blob, "other-incarnation") {
		t.Fatalf("refusal leaked %s", blob)
	}
}

func assertOtherProvidersDisconnected(t *testing.T, rows []portal.ProductAllowance) {
	t.Helper()
	if len(rows) != 3 || rows[0].Provider != "Claude" || rows[1].Provider != "Codex" || rows[2].Provider != "Cursor" {
		t.Fatalf("providers %+v", rows)
	}
	for _, row := range []portal.ProductAllowance{rows[0], rows[2]} {
		if row.Value != portal.AllowanceNotConnected || row.Reason != "" || row.Provenance != "" || len(row.Windows) != 0 {
			t.Fatalf("neighbor %+v", row)
		}
	}
}

func TestCurrentSourceSanitizerToRenderedUsage(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 10, 0, 0, time.UTC)
	raw := []byte(`{"provider":"codex","observedAt":"2026-01-15T12:00:00Z","usedPercent":40,"windowDurationMins":10080,"resetsAt":"2026-01-22T12:00:00Z","source":"Codex get_usage_limits tool","accountIdentity":"Unavailable","binding":"Unconfigured"}`)
	report, err := pa.CodexToolReport(raw, januarySelection(), now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	page := portal.ProductPage{Title: "Sessions", Connected: true, Preview: true, AsOf: now, Filter: portal.ProductFilter{View: "usage"}, Usage: &portal.ProductUsage{}}
	page = applyCachedAllowance(page, NewCurrentSourceReader(snapshot, januarySelection()), func() time.Time { return now })
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"Current source", "40% used", "60% remaining (derived)", "Stable account identity is unavailable", `Retrieved at <time datetime="2026-01-15T12:00:00Z">`, "Exact observation time not reported", "Window: 7 days", "Fixture preview"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(text, "This is a measured provider report") {
		t.Fatal("selected report promoted to measured")
	}
}
