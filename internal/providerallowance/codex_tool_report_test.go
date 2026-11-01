package providerallowance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const syntheticCodexToolBody = `{"provider":"codex","observedAt":"2026-01-08T00:00:00Z","usedPercent":40,"remainingPercent":60,"windowDurationMinutes":10080,"resetsAt":"2026-01-15T00:00:00Z","source":"Codex get_usage_limits tool","accountIdentity":"Unavailable","binding":"Unconfigured"}`

func syntheticCodexSelection() CurrentSourceSelection {
	var sel CurrentSourceSelection
	sel.PublisherRef = CurrentSourcePublisher
	sel.ProducerIncarnation = "synthetic-codex-1"
	sel.SourceGeneration = 1
	sel.MaxAge = 30 * time.Minute
	return sel
}

func syntheticCodexNow() time.Time {
	return time.Date(2026, 1, 8, 0, 5, 0, 0, time.UTC)
}

func withCodexToolFields(fields string) string {
	return syntheticCodexToolBody[:len(syntheticCodexToolBody)-1] + "," + fields + "}"
}

func TestCodexToolReportSanitizesRetainedObservation(t *testing.T) {
	now := syntheticCodexNow()
	sel := syntheticCodexSelection()
	var want CurrentSourceReport
	want.Schema = CurrentSourceSchema
	want.PublisherRef = CurrentSourcePublisher
	want.ProducerIncarnation = "synthetic-codex-1"
	want.SourceGeneration = 1
	want.RetrievedAt = "2026-01-08T00:00:00Z"
	want.UsedPercent = "40"
	want.RemainingPercent = "60"
	want.RemainingPercentBasis = CurrentSourceRemainingBasis
	want.WindowDurationMinutes = 10080
	want.ResetsAt = "2026-01-15T00:00:00Z"
	got, err := CodexToolReport([]byte(syntheticCodexToolBody), sel, now)
	if err != nil || got != want || got.ProviderUpdatedAt != nil {
		t.Fatalf("minimal report: %v %#v", err, got)
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
	optional := withCodexToolFields(`"retrievedAt":"2026-01-08T00:00:00Z","providerObservedAt":null,"automationCeilingPercent":90,"remainingToCeilingPercentagePoints":50,"bankedResetCredits":"Unknown"`)
	stripped, err := CodexToolReport([]byte(optional), sel, now)
	if err != nil || stripped != want {
		t.Fatalf("optional fields: %v %#v", err, stripped)
	}
	raw, _ := json.Marshal(stripped)
	for _, leak := range []string{"automationCeilingPercent", "remainingToCeilingPercentagePoints", "bankedResetCredits", "accountIdentity", "Unavailable", "Unconfigured", "Unknown", "providerObservedAt", "observedAt", "Codex get_usage_limits tool", "90", "50"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("emitted %s in %s", leak, raw)
		}
	}
	lexicalBody := strings.Replace(syntheticCodexToolBody, `"usedPercent":40,"remainingPercent":60`, `"usedPercent":40.0,"remainingPercent":60.0`, 1)
	lexical, err := CodexToolReport([]byte(lexicalBody), sel, now)
	if err != nil || lexical.UsedPercent != "40.0" || lexical.RemainingPercent != "60.0" || lexical.RetrievedAt != want.RetrievedAt {
		t.Fatalf("lexical percent: %v %#v", err, lexical)
	}
	offset, err := CodexToolReport([]byte(withCodexToolFields(`"retrievedAt":"2026-01-08T05:00:00+05:00"`)), sel, now)
	if err != nil || offset != want {
		t.Fatalf("same instant: %v %#v", err, offset)
	}
	fraction, err := CodexToolReport([]byte(withCodexToolFields(`"retrievedAt":"2026-01-08T00:00:00.0Z"`)), sel, now)
	if err != nil || fraction.RetrievedAt != "2026-01-08T00:00:00Z" {
		t.Fatalf("original retrieval: %v %#v", err, fraction)
	}
	window := strings.Replace(syntheticCodexToolBody, `"windowDurationMinutes":10080`, `"windowDurationMinutes":300`, 1)
	shortWindow, err := CodexToolReport([]byte(window), sel, now)
	if err != nil || shortWindow.WindowDurationMinutes != 300 || shortWindow.UsedPercent != "40" {
		t.Fatalf("window: %v %#v", err, shortWindow)
	}
	next := sel
	next.SourceGeneration = 2
	generated, err := CodexToolReport([]byte(syntheticCodexToolBody), next, now)
	if err != nil || generated.SourceGeneration != 2 || generated.ProducerIncarnation != sel.ProducerIncarnation || generated.PublisherRef != CurrentSourcePublisher {
		t.Fatalf("selection identity: %v %#v", err, generated)
	}
	day := sel
	day.MaxAge = 24 * time.Hour
	if _, err := CodexToolReport([]byte(syntheticCodexToolBody), day, now); err != nil {
		t.Fatal(err)
	}
	exact := syntheticCodexToolBody + strings.Repeat(" ", codexToolReportMaxBytes-len(syntheticCodexToolBody))
	if len(exact) != codexToolReportMaxBytes {
		t.Fatal("pad")
	}
	if _, err := CodexToolReport([]byte(exact), sel, now); err != nil {
		t.Fatal(err)
	}
}

func TestCodexToolReportRejectsUnsafeObservations(t *testing.T) {
	now := syntheticCodexNow()
	sel := syntheticCodexSelection()
	secret := "SECRET-CANARY"
	cases := []struct {
		name string
		body string
		sel  CurrentSourceSelection
		now  time.Time
	}{
		{"generation zero", syntheticCodexToolBody, selectionGeneration(sel, 0), now},
		{"generation negative", syntheticCodexToolBody, selectionGeneration(sel, -1), now},
		{"age zero", syntheticCodexToolBody, selectionAge(sel, 0), now},
		{"age negative", syntheticCodexToolBody, selectionAge(sel, -time.Minute), now},
		{"age over day", syntheticCodexToolBody, selectionAge(sel, 24*time.Hour+time.Second), now},
		{"stale", syntheticCodexToolBody, sel, time.Date(2026, 1, 8, 0, 31, 0, 0, time.UTC)},
		{"future", syntheticCodexToolBody, sel, time.Date(2026, 1, 7, 23, 0, 0, 0, time.UTC)},
		{"reset not after now", strings.Replace(syntheticCodexToolBody, `"resetsAt":"2026-01-15T00:00:00Z"`, `"resetsAt":"2026-01-08T00:05:00Z"`, 1), sel, now},
		{"reset not after retrieval", strings.Replace(syntheticCodexToolBody, `"resetsAt":"2026-01-15T00:00:00Z"`, `"resetsAt":"2026-01-08T00:00:00Z"`, 1), sel, now},
		{"provider time", withCodexToolFields(`"providerObservedAt":"2026-01-09T03:04:05Z"`), sel, now},
		{"spoofed source", strings.Replace(syntheticCodexToolBody, codexToolSource, "spoofed-source", 1), sel, now},
		{"spoofed account", strings.Replace(syntheticCodexToolBody, `"accountIdentity":"Unavailable"`, `"accountIdentity":"acct-`+secret+`"`, 1), sel, now},
		{"spoofed binding", strings.Replace(syntheticCodexToolBody, `"binding":"Unconfigured"`, `"binding":"Verified"`, 1), sel, now},
		{"spoofed publisher", syntheticCodexToolBody, selectionPublisher(sel, "spoofed-publisher"), now},
		{"empty incarnation", syntheticCodexToolBody, selectionIncarnation(sel, ""), now},
		{"unknown key", withCodexToolFields(`"note":"` + secret + `"`), sel, now},
		{"duplicate key", strings.Replace(syntheticCodexToolBody, `"provider":"codex"`, `"provider":"codex","provider":"codex"`, 1), sel, now},
		{"case fold", strings.Replace(syntheticCodexToolBody, `"usedPercent":`, `"UsedPercent":`, 1), sel, now},
		{"unicode fold", strings.Replace(syntheticCodexToolBody, `"usedPercent":`, `"uſedPercent":`, 1), sel, now},
		{"display percent", strings.Replace(syntheticCodexToolBody, `"usedPercent":40`, `"usedPercent":"40%"`, 1), sel, now},
		{"percent text", strings.Replace(syntheticCodexToolBody, `"usedPercent":40,"remainingPercent":60`, `"usedPercent":"40","remainingPercent":"60"`, 1), sel, now},
		{"percent exponent", strings.Replace(syntheticCodexToolBody, `"usedPercent":40,"remainingPercent":60`, `"usedPercent":4e1,"remainingPercent":6e1`, 1), sel, now},
		{"percent sum", strings.Replace(syntheticCodexToolBody, `"remainingPercent":60`, `"remainingPercent":50`, 1), sel, now},
		{"window fraction", strings.Replace(syntheticCodexToolBody, `"windowDurationMinutes":10080`, `"windowDurationMinutes":10080.0`, 1), sel, now},
		{"window text", strings.Replace(syntheticCodexToolBody, `"windowDurationMinutes":10080`, `"windowDurationMinutes":"10080"`, 1), sel, now},
		{"window zero", strings.Replace(syntheticCodexToolBody, `"windowDurationMinutes":10080`, `"windowDurationMinutes":0`, 1), sel, now},
		{"banked count", withCodexToolFields(`"bankedResetCredits":"0"`), sel, now},
		{"ceiling over", withCodexToolFields(`"automationCeilingPercent":101`), sel, now},
		{"points over", withCodexToolFields(`"remainingToCeilingPercentagePoints":100.5`), sel, now},
		{"retrieved mismatch", withCodexToolFields(`"retrievedAt":"2026-01-08T00:00:01Z"`), sel, now},
		{"retrieved null", withCodexToolFields(`"retrievedAt":null`), sel, now},
		{"null provider", strings.Replace(syntheticCodexToolBody, `"provider":"codex"`, `"provider":null`, 1), sel, now},
		{"spoofed generation", withCodexToolFields(`"sourceGeneration":2`), sel, now},
		{"native stdout", `{"plan":"pro","used_percent":40,"resets_at":"2026-01-15T00:00:00Z"}`, sel, now},
		{"empty", ``, sel, now},
		{"object", `{}`, sel, now},
		{"array", `[]`, sel, now},
		{"trailing", syntheticCodexToolBody + `{}`, sel, now},
		{"bom", string([]byte{0xEF, 0xBB, 0xBF}) + syntheticCodexToolBody, sel, now},
		{"utf8", "\xff", sel, now},
		{"oversize", syntheticCodexToolBody + strings.Repeat(" ", codexToolReportMaxBytes), sel, now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CodexToolReport([]byte(tc.body), tc.sel, tc.now)
			if err == nil || err.Error() != errCodexToolReport.Error() || got != (CurrentSourceReport{}) {
				t.Fatalf("got %#v %v", got, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "2026-01-09T03:04:05Z") || strings.Contains(err.Error(), "spoofed-source") || strings.Contains(err.Error(), "40%") {
				t.Fatal(err)
			}
		})
	}
}

func selectionGeneration(sel CurrentSourceSelection, n int64) CurrentSourceSelection {
	sel.SourceGeneration = n
	return sel
}

func selectionAge(sel CurrentSourceSelection, age time.Duration) CurrentSourceSelection {
	sel.MaxAge = age
	return sel
}

func selectionPublisher(sel CurrentSourceSelection, publisher string) CurrentSourceSelection {
	sel.PublisherRef = publisher
	return sel
}

func selectionIncarnation(sel CurrentSourceSelection, id string) CurrentSourceSelection {
	sel.ProducerIncarnation = id
	return sel
}

func TestCodexToolReportDerivesNativeUsedComplement(t *testing.T) {
	base := strings.Replace(syntheticCodexToolBody, `,"remainingPercent":60`, "", 1)
	base = strings.Replace(base, "windowDurationMinutes", "windowDurationMins", 1)
	for _, tc := range []struct{ used, remaining string }{{"0", "100"}, {"100", "0"}, {"40", "60"}, {"33.333", "66.667"}, {"0.0001", "99.9999"}} {
		body := strings.Replace(base, `"usedPercent":40`, `"usedPercent":`+tc.used, 1)
		got, err := CodexToolReport([]byte(body), syntheticCodexSelection(), syntheticCodexNow())
		if err != nil || got.UsedPercent != tc.used || got.RemainingPercent != tc.remaining || got.RemainingPercentBasis != CurrentSourceRemainingBasis {
			t.Fatalf("used=%s report=%+v err=%v", tc.used, got, err)
		}
	}
	marked := withCodexToolFields(`"remainingPercentBasis":"derived_complement_of_used_percent","toolProvidesRemainingPercent":false`)
	if _, err := CodexToolReport([]byte(marked), syntheticCodexSelection(), syntheticCodexNow()); err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{`"remainingPercentBasis":"provider_reported"`, `"toolProvidesRemainingPercent":true`, `"windowDurationMins":300`} {
		if _, err := CodexToolReport([]byte(withCodexToolFields(extra)), syntheticCodexSelection(), syntheticCodexNow()); err == nil {
			t.Fatalf("accepted conflicting provenance %s", extra)
		}
	}
}

func TestCodexToolReportBoundsSanitizedExpansion(t *testing.T) {
	body := strings.Replace(syntheticCodexToolBody, `,"remainingPercent":60`, "", 1)
	body = strings.Replace(body, `"usedPercent":40`, `"usedPercent":0.`+strings.Repeat("1", 9000), 1)
	if len(body) > codexToolReportMaxBytes {
		t.Fatal("test input exceeds raw bound")
	}
	if _, err := CodexToolReport([]byte(body), syntheticCodexSelection(), syntheticCodexNow()); err == nil {
		t.Fatal("accepted normalized document over output bound")
	}
}
