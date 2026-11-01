package providerallowance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Fixtures are synthetic and same-shaped. They are not a retained live report.

const rawSyntheticUsed = "12"
const rawSyntheticWindow = "10080"
const rawSyntheticReset = "1775649600"
const rawSyntheticObserved = "2026-04-01T12:00:00Z"
const rawSyntheticResetAt = "2026-04-08T12:00:00Z"

func rawSyntheticSelection() CurrentSourceSelection {
	return CurrentSourceSelection{
		PublisherRef:        CurrentSourcePublisher,
		ProducerIncarnation: "synthetic-raw-1",
		SourceGeneration:    1,
		MaxAge:              1800 * time.Second,
	}
}

func rawSyntheticNow() time.Time {
	return time.Date(2026, 4, 1, 12, 10, 0, 0, time.UTC)
}

func rawLimitJSON(used, window, reset string) string {
	return `{"limitId":"codex","limitName":null,"normalModelSlug":null,"primary":{"usedPercent":` + used + `,"windowDurationMins":` + window + `,"resetsAt":` + reset + `},"secondary":null,"credits":{"hasCredits":false,"unlimited":false,"balance":"0"},"individualLimit":null,"spendControlReached":null,"planType":"pro","rateLimitReachedType":null}`
}

func rawEnvelope(legacy, byID string) string {
	parts := make([]string, 0, 6)
	if legacy != "" {
		parts = append(parts, `"rateLimits":`+legacy)
	}
	parts = append(parts, `"ordinaryUsageAllowed":null`)
	if byID != "" {
		parts = append(parts, `"rateLimitsByLimitId":`+byID)
	}
	parts = append(parts, `"rateLimitResetCredits":null`, `"accountId":null`, `"rateLimitUpsell":null`)
	return "{" + strings.Join(parts, ",") + "}"
}

func rawBoth(limit string) string {
	return rawEnvelope(limit, `{"codex":`+limit+`}`)
}

func TestCodexRawToolReportAdaptsNestedEnvelope(t *testing.T) {
	sel := rawSyntheticSelection()
	now := rawSyntheticNow()
	limit := rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, rawSyntheticReset)
	both := rawBoth(limit)
	want := CurrentSourceReport{
		Schema:                CurrentSourceSchema,
		PublisherRef:          CurrentSourcePublisher,
		ProducerIncarnation:   "synthetic-raw-1",
		SourceGeneration:      1,
		RetrievedAt:           rawSyntheticObserved,
		UsedPercent:           "12",
		RemainingPercent:      "88",
		RemainingPercentBasis: CurrentSourceRemainingBasis,
		WindowDurationMinutes: 10080,
		ResetsAt:              rawSyntheticResetAt,
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "preferred and legacy", body: both},
		{name: "preferred only", body: rawEnvelope("", `{"codex":`+limit+`}`)},
		{name: "legacy only", body: rawEnvelope(limit, "")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CodexRawToolReport([]byte(tc.body), rawSyntheticObserved, sel, now)
			if err != nil || got != want || got.ProviderUpdatedAt != nil {
				t.Fatalf("report: %v %#v", err, got)
			}
			if err := ValidateCurrentSource(got, sel, now); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			back, err := DecodeCurrentSource(encoded)
			if err != nil || back != got {
				t.Fatalf("round trip: %v %#v", err, back)
			}
			// Success does not authenticate the bytes or bind an account.
			// accountId null stays unattributed: the canonical report has no account.
			// Quoted tokens keep planType "pro" distinct from names such as providerUpdatedAt.
			for _, leak := range []string{`"accountId"`, `"accountIdentity"`, `"Unavailable"`, `"Unconfigured"`, `"balance"`, `"credits"`, `"planType"`, `"pro"`, `"rateLimits"`, `"secondary"`, `"ordinaryUsageAllowed"`, `"rateLimitResetCredits"`, `"automationCeiling"`, `"banked"`, `"TOKEN"`, rawSyntheticReset} {
				if strings.Contains(string(encoded), leak) {
					t.Fatalf("emitted %s in %s", leak, encoded)
				}
			}
			if !strings.Contains(string(encoded), `"providerUpdatedAt":null`) {
				t.Fatalf("provider time: %s", encoded)
			}
		})
	}

	offset, err := CodexRawToolReport([]byte(both), "2026-04-01T17:00:00+05:00", sel, now)
	if err != nil || offset.RetrievedAt != "2026-04-01T17:00:00+05:00" || offset.ResetsAt != rawSyntheticResetAt || offset.ProviderUpdatedAt != nil {
		t.Fatalf("original offset spelling: %v %#v", err, offset)
	}
	fraction, err := CodexRawToolReport([]byte(both), "2026-04-01T12:00:00.5Z", sel, now)
	if err != nil || fraction.RetrievedAt != "2026-04-01T12:00:00.5Z" {
		t.Fatalf("original fraction spelling: %v %#v", err, fraction)
	}

	// Caller-supplied original time is preserved exactly. Date, offset, and reset below are independently synthetic.
	preservedLimit := rawLimitJSON("12", "300", "1782144245")
	preservedBody := rawBoth(preservedLimit)
	preservedNow := time.Date(2026, 6, 15, 16, 9, 5, 0, time.UTC)
	preserved, err := CodexRawToolReport([]byte(preservedBody), "2026-06-15T09:04:05-07:00", sel, preservedNow)
	if err != nil || preserved.RetrievedAt != "2026-06-15T09:04:05-07:00" || preserved.UsedPercent != "12" || preserved.RemainingPercent != "88" || preserved.WindowDurationMinutes != 300 || preserved.ResetsAt != "2026-06-22T16:04:05Z" || preserved.ProviderUpdatedAt != nil {
		t.Fatalf("original observation: %v %#v", err, preserved)
	}

	next := sel
	next.SourceGeneration = 3
	generated, err := CodexRawToolReport([]byte(both), rawSyntheticObserved, next, now)
	if err != nil || generated.SourceGeneration != 3 || generated.ProducerIncarnation != sel.ProducerIncarnation {
		t.Fatalf("selection: %v %#v", err, generated)
	}
	atObserved, err := CodexRawToolReport([]byte(both), rawSyntheticObserved, sel, time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	if err != nil || atObserved.RetrievedAt != rawSyntheticObserved {
		t.Fatalf("observed instant: %v %#v", err, atObserved)
	}
	atMaxAge, err := CodexRawToolReport([]byte(both), rawSyntheticObserved, sel, time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC))
	if err != nil || atMaxAge.RetrievedAt != rawSyntheticObserved {
		t.Fatalf("max age boundary: %v %#v", err, atMaxAge)
	}

	for _, tc := range []struct {
		used, remaining string
	}{
		{used: "0", remaining: "100"},
		{used: "100", remaining: "0"},
		{used: "33.333", remaining: "66.667"},
		{used: "40.0", remaining: "60"},
		{used: "100.0", remaining: "0"},
	} {
		body := rawBoth(rawLimitJSON(tc.used, rawSyntheticWindow, rawSyntheticReset))
		got, err := CodexRawToolReport([]byte(body), rawSyntheticObserved, sel, now)
		if err != nil || got.UsedPercent != tc.used || got.RemainingPercent != tc.remaining || got.RemainingPercentBasis != CurrentSourceRemainingBasis || got.WindowDurationMinutes != 10080 {
			t.Fatalf("used %s report=%#v err=%v", tc.used, got, err)
		}
	}

	short := rawBoth(rawLimitJSON("12", "1", rawSyntheticReset))
	shortWindow, err := CodexRawToolReport([]byte(short), rawSyntheticObserved, sel, now)
	if err != nil || shortWindow.WindowDurationMinutes != 1 || shortWindow.ResetsAt != rawSyntheticResetAt || shortWindow.UsedPercent != "12" {
		t.Fatalf("window is not a reset: %v %#v", err, shortWindow)
	}

	creditLimit := strings.Replace(limit, `"hasCredits":false,"unlimited":false,"balance":"0"`, `"hasCredits":true,"unlimited":true,"balance":"TOKEN-77"`, 2)
	if !strings.Contains(creditLimit, "TOKEN-77") {
		t.Fatal("fixture")
	}
	credited, err := CodexRawToolReport([]byte(rawBoth(creditLimit)), rawSyntheticObserved, sel, now)
	if err != nil || credited.UsedPercent != "12" || credited.RemainingPercent != "88" {
		t.Fatalf("credits became allowance: %v %#v", err, credited)
	}
	creditRaw, err := json.Marshal(credited)
	if err != nil || strings.Contains(string(creditRaw), "TOKEN") || strings.Contains(string(creditRaw), "77") {
		t.Fatalf("credit leak: %v %s", err, creditRaw)
	}
}

func TestCodexRawToolReportRejectsClosed(t *testing.T) {
	sel := rawSyntheticSelection()
	now := rawSyntheticNow()
	secret := "SECRET-CANARY"
	limit := rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, rawSyntheticReset)
	body := rawBoth(limit)
	otherWindow := rawLimitJSON(rawSyntheticUsed, "300", rawSyntheticReset)
	otherUsed := rawLimitJSON("11", rawSyntheticWindow, rawSyntheticReset)
	otherReset := rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "1775650000")
	otherPlan := strings.Replace(limit, `"planType":"pro"`, `"planType":"other"`, 1)
	otherBalance := strings.Replace(limit, `"balance":"0"`, `"balance":"1"`, 1)
	cases := []struct {
		name     string
		body     string
		observed string
		sel      CurrentSourceSelection
		now      time.Time
	}{
		{name: "conflicting used", body: rawEnvelope(otherUsed, `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "conflicting window", body: rawEnvelope(otherWindow, `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "conflicting reset", body: rawEnvelope(otherReset, `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "conflicting lexical percent", body: rawEnvelope(rawLimitJSON("12.0", rawSyntheticWindow, rawSyntheticReset), `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "conflicting plan", body: rawEnvelope(otherPlan, `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "conflicting credit balance", body: rawEnvelope(otherBalance, `{"codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing both limits", body: rawEnvelope("", ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing primary", body: strings.ReplaceAll(body, `"primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1775649600}`, `"note":1`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing used", body: strings.ReplaceAll(body, `"usedPercent":12,`, ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing window", body: strings.ReplaceAll(body, `"windowDurationMins":10080,`, ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing reset", body: strings.ReplaceAll(body, `,"resetsAt":1775649600`, ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "missing account", body: strings.ReplaceAll(body, `,"accountId":null`, ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null primary", body: strings.ReplaceAll(body, `"primary":{"usedPercent":12,"windowDurationMins":10080,"resetsAt":1775649600}`, `"primary":null`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null used", body: strings.ReplaceAll(body, `"usedPercent":12`, `"usedPercent":null`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null window", body: strings.ReplaceAll(body, `"windowDurationMins":10080`, `"windowDurationMins":null`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null reset", body: strings.ReplaceAll(body, `"resetsAt":1775649600`, `"resetsAt":null`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null legacy channel", body: strings.Replace(body, `"rateLimits":`+limit, `"rateLimits":null`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null preferred channel", body: strings.Replace(body, `"rateLimitsByLimitId":{"codex":`+limit+`}`, `"rateLimitsByLimitId":null`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "null secondary required", body: strings.ReplaceAll(body, `"secondary":null`, `"secondary":{"usedPercent":1,"windowDurationMins":300,"resetsAt":1775649600}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent negative", body: rawBoth(rawLimitJSON("-1", rawSyntheticWindow, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent over", body: rawBoth(rawLimitJSON("100.01", rawSyntheticWindow, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent 101", body: rawBoth(rawLimitJSON("101", rawSyntheticWindow, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent exponent", body: rawBoth(rawLimitJSON("1e2", rawSyntheticWindow, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent text", body: rawBoth(rawLimitJSON(`"12"`, rawSyntheticWindow, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "percent remaining smuggled", body: strings.ReplaceAll(body, `"resetsAt":1775649600`, `"resetsAt":1775649600,"remainingPercent":88`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "window zero", body: rawBoth(rawLimitJSON(rawSyntheticUsed, "0", rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "window fraction", body: rawBoth(rawLimitJSON(rawSyntheticUsed, "10080.0", rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "window text", body: rawBoth(rawLimitJSON(rawSyntheticUsed, `"10080"`, rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "window negative", body: rawBoth(rawLimitJSON(rawSyntheticUsed, "-5", rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "window over year", body: rawBoth(rawLimitJSON(rawSyntheticUsed, "525601", rawSyntheticReset)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "flattened window name", body: strings.ReplaceAll(body, "windowDurationMins", "windowDurationMinutes"), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset fraction", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "1775649600.0")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset text", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, `"2026-04-08T12:00:00Z"`)), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset zero", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "0")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset negative", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "-1")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset milliseconds", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "1775649600000")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset not after now", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "1775045400")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset not after observed", body: rawBoth(rawLimitJSON(rawSyntheticUsed, rawSyntheticWindow, "1775044800")), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "future observation", body: body, observed: rawSyntheticObserved, sel: sel, now: time.Date(2026, 4, 1, 11, 59, 59, 0, time.UTC)},
		{name: "stale observation", body: body, observed: rawSyntheticObserved, sel: sel, now: time.Date(2026, 4, 1, 12, 30, 1, 0, time.UTC)},
		{name: "zero now", body: body, observed: rawSyntheticObserved, sel: sel, now: time.Time{}},
		{name: "blank observed", body: body, observed: "", sel: sel, now: now},
		{name: "observed date only", body: body, observed: "2026-04-01", sel: sel, now: now},
		{name: "observed secret", body: body, observed: secret, sel: sel, now: now},
		{name: "body time ignored", body: strings.Replace(body, "{", `{"observedAt":"1999-01-01T00:00:00Z",`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "unknown account string", body: strings.Replace(body, `"accountId":null`, `"accountId":"acct-`+secret+`"`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "unknown account unavailable", body: strings.Replace(body, `"accountId":null`, `"accountId":"Unavailable"`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "account string null", body: strings.Replace(body, `"accountId":null`, `"accountId":"null"`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "other preferred id", body: rawEnvelope("", `{"other":`+strings.Replace(limit, `"limitId":"codex"`, `"limitId":"other"`, 1)+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "extra preferred id", body: rawEnvelope(limit, `{"codex":`+limit+`,"other":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "legacy other id", body: rawEnvelope(strings.Replace(limit, `"limitId":"codex"`, `"limitId":"other"`, 1), ""), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "map key case", body: rawEnvelope("", `{"Codex":`+limit+`}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "empty preferred map", body: rawEnvelope("", `{}`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "automation ceiling", body: strings.Replace(body, "{", `{"automationCeilingPercent":90,`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "ceiling in primary", body: strings.ReplaceAll(body, `"usedPercent":12`, `"usedPercent":12,"automationCeilingPercent":90`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "reset credit receipt", body: strings.Replace(body, `"rateLimitResetCredits":null`, `"rateLimitResetCredits":{"availableCount":12}`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "ordinary usage flag", body: strings.Replace(body, `"ordinaryUsageAllowed":null`, `"ordinaryUsageAllowed":true`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "upsell object", body: strings.Replace(body, `"rateLimitUpsell":null`, `"rateLimitUpsell":{"balance":"12"}`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "credit used smuggled", body: strings.ReplaceAll(body, `"balance":"0"`, `"balance":"0","usedPercent":1`), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "duplicate account", body: strings.Replace(body, `"accountId":null`, `"accountId":null,"accountId":null`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "duplicate preferred", body: strings.Replace(body, `"rateLimitsByLimitId":`, `"rateLimitsByLimitId":{"codex":`+limit+`},"rateLimitsByLimitId":`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "spoofed publisher", body: body, observed: rawSyntheticObserved, sel: selectionPublisher(sel, "spoofed-publisher"), now: now},
		{name: "age over day", body: body, observed: rawSyntheticObserved, sel: selectionAge(sel, 24*time.Hour+time.Second), now: now},
		{name: "empty", body: ``, observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "object", body: `{}`, observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "array", body: `[]`, observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "trailing", body: body + `{}`, observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "bom", body: string([]byte{0xEF, 0xBB, 0xBF}) + body, observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "utf8", body: "\xff", observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "oversize", body: body + strings.Repeat(" ", codexToolReportMaxBytes), observed: rawSyntheticObserved, sel: sel, now: now},
		{name: "secret field", body: strings.Replace(body, "{", `{"note":"`+secret+`",`, 1), observed: rawSyntheticObserved, sel: sel, now: now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CodexRawToolReport([]byte(tc.body), tc.observed, tc.sel, tc.now)
			if err == nil || !errors.Is(err, errCodexRawToolReport) || got != (CurrentSourceReport{}) {
				t.Fatalf("got %#v %v", got, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), rawSyntheticReset) || strings.Contains(err.Error(), "1775649600000") || strings.Contains(err.Error(), "TOKEN") || strings.Contains(err.Error(), "acct-") {
				t.Fatal(err)
			}
		})
	}
}
