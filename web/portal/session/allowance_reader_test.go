package session

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	allowanceObserved = "2026-11-01T03:30:00Z"
	allowanceAccount  = "fixture-account"
)

func TestNilAllowanceReaderLeavesDisconnected(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	calls := 0
	src := allowanceSessionSource(t, now, &calls)
	page, err := src.Read(t.Context(), portal.ProductFilter{})
	if err != nil || !page.Connected || page.Usage == nil {
		t.Fatalf("read: %+v %v", page, err)
	}
	want := disconnectedAllowances()
	if !allowanceEqual(page.Allowance, want) || !allowanceEqual(page.Usage.Allowance, want) {
		t.Fatalf("allowance %+v usage %+v", page.Allowance, page.Usage.Allowance)
	}
	if calls != 1 {
		t.Fatalf("operator calls = %d", calls)
	}
}

func TestCachedAllowanceReachesSessionAndUsage(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{
		syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T06:00:00Z", pa.ResetInstant),
	}, []pa.CurrentMeasure{syntheticCurrent("thirty-minute", "40")})
	reader := newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: time.Hour, Set: true})
	calls := 0
	var logs bytes.Buffer
	src := allowanceSessionSource(t, now, &calls)
	src.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	src.SetAllowanceReader(reader)

	page, err := src.Read(t.Context(), portal.ProductFilter{})
	if err != nil || !page.Connected || page.Usage == nil {
		t.Fatalf("read: %+v %v", page, err)
	}
	assertSameAllowance(t, page)
	claude := claudeAllowance(t, page.Allowance)
	if claude.Value != portal.AllowanceMeasured || claude.Reason != "" || len(claude.Windows) != 1 {
		t.Fatalf("claude %+v", claude)
	}
	window := claude.Windows[0]
	if window.Percent != "40% left" || window.Observed != "1 Nov 03:30 UTC" || window.ObservedAt != allowanceObserved || window.AccountKey != allowanceAccount || window.Provenance != "Provider API" {
		t.Fatalf("window %+v", window)
	}
	if len(page.Allowance) != 3 || page.Allowance[1].Provider != "Codex" || page.Allowance[1].Value != portal.AllowanceUnknown || page.Allowance[2].Provider != "Cursor" || page.Allowance[2].Value != portal.AllowanceUnknown {
		t.Fatalf("providers %+v", page.Allowance)
	}
	if strings.Contains(allowanceBlob(page.Allowance), "SECRET") {
		t.Fatalf("source text in allowance: %s", allowanceBlob(page.Allowance))
	}
	again, err := src.Read(t.Context(), portal.ProductFilter{})
	if err != nil || again.Allowance[0].Windows[0].Observed != window.Observed || again.Allowance[0].Windows[0].ObservedAt != allowanceObserved {
		t.Fatalf("reread changed observed time: %+v %v", again.Allowance, err)
	}
	src.SetAllowanceReader(nil)
	cleared, err := src.Read(t.Context(), portal.ProductFilter{})
	if err != nil || !allowanceEqual(cleared.Allowance, disconnectedAllowances()) || !allowanceEqual(cleared.Usage.Allowance, disconnectedAllowances()) {
		t.Fatalf("cleared %+v usage %+v %v", cleared.Allowance, cleared.Usage, err)
	}
	if calls != 3 || logs.Len() != 0 {
		t.Fatalf("calls=%d logs=%s", calls, logs.String())
	}
}

func TestMalformedCachedAllowanceDoesNotFailSessionRead(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	var logs bytes.Buffer
	src := allowanceSessionSource(t, now, new(int))
	src.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	src.SetAllowanceReader(NewCachedAllowanceReader([]byte(`{"SECRET-ALLOWANCE":true}`), pa.FixtureParseLimits, nil, nil, AllowanceReadPolicy{}))
	page, err := src.Read(t.Context(), portal.ProductFilter{})
	if err != nil || !page.Connected || page.Usage == nil {
		t.Fatalf("read: %+v %v", page, err)
	}
	assertSameAllowance(t, page)
	assertFixedRefusal(t, page.Allowance, "Unsupported allowance document")
	if strings.Contains(logs.String(), "SECRET") {
		t.Fatalf("log %s", logs.String())
	}
}

func TestCachedAllowanceRefusesBadDocuments(t *testing.T) {
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{
		syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T06:00:00Z", pa.ResetInstant),
	}, []pa.CurrentMeasure{syntheticCurrent("thirty-minute", "40")})
	valid := marshalAllowance(t, doc)
	wrongSchema := bytes.Replace(valid, []byte(pa.SchemaRead), []byte(pa.SchemaObservation), 1)
	tiny := pa.FixtureParseLimits
	tiny.MaxBytes = 1
	cursor := "SECRET-CURSOR"
	doc.NextCursor = &cursor
	partial := marshalAllowance(t, doc)
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	policy := AllowanceReadPolicy{MaxAge: time.Hour, Set: true}
	for _, tc := range []struct {
		name   string
		raw    []byte
		limits pa.ParseLimits
		reason string
	}{
		{name: "empty", raw: nil, limits: pa.FixtureParseLimits, reason: "Unsupported allowance document"},
		{name: "strict-unknown-field", raw: []byte(`{"schema":"providerallowance.read.v1","SECRET-ALLOWANCE":1}`), limits: pa.FixtureParseLimits, reason: "Unsupported allowance document"},
		{name: "schema", raw: wrongSchema, limits: pa.FixtureParseLimits, reason: "Unsupported allowance document"},
		{name: "oversized", raw: valid, limits: tiny, reason: "Unsupported allowance document"},
		{name: "zero-limits", raw: valid, limits: pa.ParseLimits{}, reason: "Unsupported allowance document"},
		{name: "partial", raw: partial, limits: pa.FixtureParseLimits, reason: "Partial allowance read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := NewCachedAllowanceReader(tc.raw, tc.limits, []pa.TrustedContext{trust}, reg, policy)
			if reader == nil {
				t.Fatal("nil reader")
			}
			assertFixedRefusal(t, reader.Allowances(now), tc.reason)
		})
	}
}

func TestCachedAllowanceRejectsTrustMismatch(t *testing.T) {
	now := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	policy := AllowanceReadPolicy{MaxAge: time.Hour, Set: true}
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{
		syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T06:00:00Z", pa.ResetInstant),
	}, []pa.CurrentMeasure{syntheticCurrent("thirty-minute", "40")})

	other := "other-account"
	mismatched := trust
	mismatched.AccountRef = &other
	assertClaudeRefused(t, projectSynthetic(t, doc, mismatched, reg, policy, now), "Account binding unverified")

	generation := trust
	generation.BindingGeneration = 8
	assertClaudeRefused(t, projectSynthetic(t, doc, generation, reg, policy, now), "Account binding unverified")

	openai := trust
	openai.Provider = "openai"
	assertClaudeRefused(t, projectSynthetic(t, doc, openai, reg, policy, now), "Account binding unverified")

	unconfigured := doc
	unconfigured.SourceStatuses = []pa.SourceStatus{forgedStatus(pa.AvailabilityUnconfigured, trust)}
	got := projectSynthetic(t, unconfigured, mismatched, reg, policy, now)
	row := claudeAllowance(t, got)
	if row.Value == portal.AllowanceNotConnected || row.Value != portal.AllowanceUnknown || row.Reason != "Allowance validation unavailable" || len(row.Windows) != 0 {
		t.Fatalf("unconfigured status overrode refusal: %+v", row)
	}

	conflict := doc
	conflict.SourceStatuses = []pa.SourceStatus{forgedStatus(pa.AvailabilityConflict, trust)}
	conflicted := claudeAllowance(t, projectSynthetic(t, conflict, mismatched, reg, policy, now))
	if conflicted.Value != portal.AllowanceUnknown || conflicted.Reason != "Conflicting allowance reports" || len(conflicted.Windows) != 0 {
		t.Fatalf("conflict precedence: %+v", conflicted)
	}
	if strings.Contains(allowanceBlob(got)+allowanceBlob([]portal.ProductAllowance{conflicted}), "other-account") || strings.Contains(allowanceBlob(got), "SECRET") {
		t.Fatal("mismatch text escaped")
	}
}

func TestCachedAllowanceKeepsObservedTime(t *testing.T) {
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{
		syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T06:00:00Z", pa.ResetInstant),
	}, []pa.CurrentMeasure{syntheticCurrent("thirty-minute", "40")})
	observed := time.Date(2026, 11, 1, 3, 30, 0, 0, time.UTC)
	reader := newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: time.Hour, Set: true})
	raw := append([]byte(nil), reader.(*cachedAllowanceReader).raw...)

	early := claudeAllowance(t, reader.Allowances(observed.Add(30*time.Minute)))
	if early.Value != portal.AllowanceMeasured || len(early.Windows) != 1 || early.Windows[0].Observed != "1 Nov 03:30 UTC" || early.Windows[0].Percent != "40% left" {
		t.Fatalf("early %+v", early)
	}
	late := claudeAllowance(t, reader.Allowances(observed.Add(2*time.Hour)))
	if late.Value != portal.AllowanceStale || len(late.Windows) != 1 || late.Windows[0].Observed != "1 Nov 03:30 UTC" || late.Windows[0].ObservedAt != allowanceObserved || late.Windows[0].Percent != "40% left" {
		t.Fatalf("late %+v", late)
	}
	again := claudeAllowance(t, reader.Allowances(observed.Add(30*time.Minute)))
	if again.Value != portal.AllowanceMeasured || again.Windows[0].Observed != early.Windows[0].Observed || again.Windows[0].Percent != "40% left" {
		t.Fatalf("reread reset freshness: %+v", again)
	}
	if !bytes.Equal(raw, reader.(*cachedAllowanceReader).raw) {
		t.Fatal("cached bytes changed")
	}

	unset := newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{})
	refused := claudeAllowance(t, unset.Allowances(observed.Add(time.Second)))
	if refused.Value != portal.AllowanceUnknown || refused.Reason != "Allowance freshness unknown or expired" || len(refused.Windows) != 0 {
		t.Fatalf("unset policy %+v", refused)
	}
	negative := newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: -time.Minute, Set: true})
	if row := claudeAllowance(t, negative.Allowances(observed.Add(time.Second))); row.Reason != "Allowance freshness unknown or expired" || len(row.Windows) != 0 {
		t.Fatalf("negative policy %+v", row)
	}
	if row := claudeAllowance(t, reader.Allowances(time.Time{})); row.Reason != "Allowance freshness unknown or expired" || len(row.Windows) != 0 {
		t.Fatalf("zero clock %+v", row)
	}
	if row := claudeAllowance(t, reader.Allowances(observed.Add(-time.Minute))); row.Reason != "Allowance freshness unknown or expired" || len(row.Windows) != 0 {
		t.Fatalf("future observed %+v", row)
	}

	uncertain := doc
	uncertain.SourceStatuses = []pa.SourceStatus{forgedStatus(pa.AvailabilityUnconfigured, trust)}
	hidden := claudeAllowance(t, projectSynthetic(t, uncertain, trust, reg, AllowanceReadPolicy{}, observed.Add(time.Minute)))
	if hidden.Value == portal.AllowanceNotConnected || hidden.Reason != "Allowance freshness unknown or expired" || len(hidden.Windows) != 0 {
		t.Fatalf("unconfigured hid uncertain snapshot: %+v", hidden)
	}
}

func TestCachedAllowanceExpiresOnlyPastInstantWindow(t *testing.T) {
	doc, trust, reg := syntheticAllowanceDoc([]pa.Measure{
		syntheticMeasure("thirty-minute", "thirty-minute", 30, "40", "2026-11-01T04:30:00Z", pa.ResetInstant),
		syntheticMeasure("one-hour", "one-hour", 60, "70", "2026-11-01T06:00:00Z", pa.ResetInstant),
		syntheticMeasure("fifteen-minute", "fifteen-minute", 15, "15", "2026-11-01T03:00:00Z", pa.ResetDisplayOnly),
	}, []pa.CurrentMeasure{
		syntheticCurrent("thirty-minute", "40"),
		syntheticCurrent("one-hour", "70"),
		syntheticCurrent("fifteen-minute", "15"),
	})
	reader := newSyntheticReader(t, doc, trust, reg, AllowanceReadPolicy{MaxAge: 3 * time.Hour, Set: true})
	raw := append([]byte(nil), reader.(*cachedAllowanceReader).raw...)
	earlyNow := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	lateNow := time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)

	early := claudeAllowance(t, reader.Allowances(earlyNow))
	assertResetWindows(t, early, "40% left", "70% left", "15% left")
	late := claudeAllowance(t, reader.Allowances(lateNow))
	assertResetWindows(t, late, "Unknown", "70% left", "15% left")
	if late.Windows[0].PercentKnown || late.Windows[0].PercentValue != 0 || strings.Contains(late.Windows[0].Percent, "0%") {
		t.Fatalf("past reset manufactured a value: %+v", late.Windows[0])
	}
	if late.Windows[1].Percent != "70% left" || late.Windows[2].Reset != "1 Nov 03:00 UTC (display only)" || late.Windows[2].Percent != "15% left" {
		t.Fatalf("sibling windows %+v", late.Windows)
	}
	restored := claudeAllowance(t, reader.Allowances(earlyNow))
	assertResetWindows(t, restored, "40% left", "70% left", "15% left")
	if !bytes.Equal(raw, reader.(*cachedAllowanceReader).raw) {
		t.Fatal("cached bytes changed")
	}
	decoded, err := pa.DecodeReadResponse(raw, pa.FixtureParseLimits)
	if err != nil || decoded.Items[0].Observation.Measures[0].Value.Decimal == nil || *decoded.Items[0].Observation.Measures[0].Value.Decimal != "40" {
		t.Fatalf("historical decimal %v %v", decoded.Items[0].Observation.Measures[0].Value.Decimal, err)
	}
}

func allowanceSessionSource(t *testing.T, now time.Time, calls *int) *SessionSource {
	t.Helper()
	return sessionReadFixture(t, func() time.Time { return now }, func(r *http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {mediaJSON}}, Body: io.NopCloser(strings.NewReader(activityPage())), Request: r}, nil
	})
}

func syntheticAllowanceDoc(measures []pa.Measure, currents []pa.CurrentMeasure) (pa.ReadResponse, pa.TrustedContext, *pa.StaticRegistry) {
	account := allowanceAccount
	workspace := "fixture-workspace"
	observed := allowanceObserved
	min, max := "0", "100"
	trust := pa.TrustedContext{
		ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation", BindingGeneration: 7,
		AccountRef: &account, WorkspaceRef: &workspace, BindingState: pa.BindingVerified,
		Provider: "anthropic", SourceKind: pa.SourceNativeRPC, SourceVersion: "fixture-v1",
	}
	reg := pa.NewStaticRegistry(pa.RegistryParts{
		Sources: []pa.SourceRule{{
			Provider: "anthropic", Kind: pa.SourceNativeRPC, Version: "fixture-v1",
			CapabilityRef: "fixture:synthetic", AllowNormalizedMeasures: true,
		}},
		Metrics: []pa.MetricRule{{
			ValidationRef: "fixture:synthetic-allowance", Provider: "anthropic",
			SourceKind: pa.SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionRemaining,
			Windows: []string{"thirty-minute", "one-hour", "fifteen-minute"}, Min: &min, Max: &max,
		}},
		Accounts: []pa.AccountRule{{
			AccountRef: account, WorkspaceRef: workspace, Generation: 7,
			ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation",
		}},
	})
	doc := pa.ReadResponse{
		Schema: pa.SchemaRead, SnapshotRevision: "fixture-snapshot", ServedAt: observed,
		Items: []pa.ReadItem{{
			Observation: pa.Observation{
				Schema: pa.SchemaObservation, ObservationID: "fixture-obs-1",
				ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation",
				Sequence: 1, BindingGeneration: 7, Provider: "anthropic",
				Source:     pa.Source{Kind: pa.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "fixture:synthetic"},
				Scope:      pa.Scope{AccountRef: &account, WorkspaceRef: &workspace, BindingState: pa.BindingVerified},
				CapturedAt: observed, ProviderObservedAt: &observed,
				Availability: pa.AvailabilityAvailable, Completeness: pa.CompletenessComplete,
				Measures: measures,
			},
			ReceivedAt:      observed,
			Validation:      pa.ValidationRecord{State: pa.ValidationUnverified},
			Binding:         pa.BindingRecord{State: pa.BindingUnknown},
			Freshness:       pa.Freshness{State: pa.FreshFresh, AsOf: observed, Basis: pa.BasisProvider, Reason: "SECRET-FRESHNESS"},
			CurrentMeasures: currents,
		}},
	}
	return doc, trust, reg
}

func syntheticMeasure(id, window string, minutes int64, percent, reset string, precision pa.ResetPrecision) pa.Measure {
	count := pa.Count(minutes)
	return pa.Measure{
		ID: id, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionRemaining,
		Value:         pa.MeasureValue{State: pa.ValueKnown, Decimal: sp(percent)},
		Window:        pa.Window{Ref: window, DurationMinutes: &count, ResetAt: sp(reset), ResetPrecision: precision},
		ValidationRef: sp("fixture:synthetic-allowance"),
	}
}

func syntheticCurrent(id, percent string) pa.CurrentMeasure {
	return pa.CurrentMeasure{MeasureID: id, State: pa.CurrentKnown, Value: pa.MeasureValue{State: pa.ValueKnown, Decimal: sp(percent)}}
}

func forgedStatus(availability pa.Availability, trust pa.TrustedContext) pa.SourceStatus {
	return pa.SourceStatus{
		Provider: "anthropic", ProducerRef: trust.ProducerRef, Availability: availability,
		LastAttempt: pa.LastAttempt{AttemptedAt: allowanceObserved, Result: pa.AttemptUnknown},
		Freshness:   pa.Freshness{State: pa.FreshFresh, AsOf: allowanceObserved, Basis: pa.BasisProvider, Reason: "SECRET-STATUS"},
		Validation:  pa.ValidationRecord{State: pa.ValidationSourceFields},
		Binding: pa.BindingRecord{
			State: pa.BindingVerified, Generation: pa.Count(trust.BindingGeneration),
			AccountRef: cloneAllowanceString(trust.AccountRef), WorkspaceRef: cloneAllowanceString(trust.WorkspaceRef),
		},
	}
}

func newSyntheticReader(t *testing.T, doc pa.ReadResponse, trust pa.TrustedContext, reg pa.Registry, policy AllowanceReadPolicy) AllowanceReader {
	t.Helper()
	return NewCachedAllowanceReader(marshalAllowance(t, doc), pa.FixtureParseLimits, []pa.TrustedContext{trust}, reg, policy)
}

func projectSynthetic(t *testing.T, doc pa.ReadResponse, trust pa.TrustedContext, reg pa.Registry, policy AllowanceReadPolicy, now time.Time) []portal.ProductAllowance {
	t.Helper()
	return newSyntheticReader(t, doc, trust, reg, policy).Allowances(now)
}

func marshalAllowance(t *testing.T, doc pa.ReadResponse) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pa.DecodeReadResponse(raw, pa.FixtureParseLimits); err != nil {
		t.Fatalf("synthetic fixture: %v", err)
	}
	return raw
}

func claudeAllowance(t *testing.T, rows []portal.ProductAllowance) portal.ProductAllowance {
	t.Helper()
	for _, row := range rows {
		if row.Provider == "Claude" {
			return row
		}
	}
	t.Fatalf("no Claude row: %+v", rows)
	return portal.ProductAllowance{}
}

func assertSameAllowance(t *testing.T, page portal.ProductPage) {
	t.Helper()
	if page.Usage == nil || len(page.Allowance) == 0 || len(page.Usage.Allowance) == 0 || &page.Allowance[0] != &page.Usage.Allowance[0] {
		t.Fatalf("views are not the same allowance: %+v %+v", page.Allowance, page.Usage)
	}
}

func assertFixedRefusal(t *testing.T, rows []portal.ProductAllowance, reason string) {
	t.Helper()
	blob := allowanceBlob(rows)
	if strings.Contains(blob, "SECRET") || strings.Contains(blob, "bytes") || strings.Contains(blob, "limit") || strings.Contains(blob, "invalid_") {
		t.Fatalf("source text in allowance: %s", blob)
	}
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	for _, row := range rows {
		if row.Value != portal.AllowanceUnknown || row.Reason != reason || len(row.Windows) != 0 {
			t.Fatalf("reason %q row %+v", reason, row)
		}
	}
}

func assertClaudeRefused(t *testing.T, rows []portal.ProductAllowance, reason string) {
	t.Helper()
	row := claudeAllowance(t, rows)
	if strings.Contains(allowanceBlob(rows), "SECRET") || strings.Contains(allowanceBlob(rows), "other-account") {
		t.Fatalf("source text %s", allowanceBlob(rows))
	}
	if row.Value != portal.AllowanceUnknown || row.Reason != reason || len(row.Windows) != 0 {
		t.Fatalf("%+v", row)
	}
}

func assertResetWindows(t *testing.T, row portal.ProductAllowance, first, second, third string) {
	t.Helper()
	if row.Value != portal.AllowanceMeasured || len(row.Windows) != 3 {
		t.Fatalf("%+v", row)
	}
	got := []string{row.Windows[0].Percent, row.Windows[1].Percent, row.Windows[2].Percent}
	want := []string{first, second, third}
	for i := range want {
		if got[i] != want[i] || row.Windows[i].Observed != "1 Nov 03:30 UTC" {
			t.Fatalf("windows %+v", row.Windows)
		}
	}
	if row.Windows[0].Window != "30 minutes" || row.Windows[1].Window != "1 hour" || row.Windows[2].Window != "15 minutes" {
		t.Fatalf("durations %+v", row.Windows)
	}
}

func allowanceEqual(got, want []portal.ProductAllowance) bool {
	rawGot, errGot := json.Marshal(got)
	rawWant, errWant := json.Marshal(want)
	return errGot == nil && errWant == nil && bytes.Equal(rawGot, rawWant)
}

func allowanceBlob(rows []portal.ProductAllowance) string {
	raw, _ := json.Marshal(rows)
	return string(raw)
}

func sp(s string) *string { return &s }
