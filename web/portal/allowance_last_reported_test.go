package portal

import (
	"strings"
	"testing"
	"time"
)

func lastReportedLine(percent string) ProductAllowanceLastReported {
	return ProductAllowanceLastReported{
		Percent: percent, Window: "7 days", Reset: "Reported reset: 11 Nov 09:22 UTC.",
		ReadAt: "2 Nov 13:40 UTC", ReadAtExact: "2026-11-02T13:40:00Z",
		Source: "Provider API", Account: "Account verified",
		Why: "Not current: the provider did not report when it measured this.",
	}
}

// A last-reported value renders only on an Unknown account, apart from its
// state: no meter, no countdown, and the badge stays Unknown.
func TestAllowanceLastReportedStaysApartFromCurrent(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	measured := ProductAllowanceWindow{
		Percent: "24% used", Window: "7 days", Reset: "Unknown", Observed: "2 Nov 12:18 UTC", Provenance: "Provider API",
		PercentKnown: true, PercentValue: 24, Direction: "used",
	}
	page := ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{
			{Provider: "Codex", Value: AllowanceUnknown, Reason: "Allowance freshness unknown or expired",
				LastReported: []ProductAllowanceLastReported{lastReportedLine("0% used")}},
			{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{measured},
				LastReported: []ProductAllowanceLastReported{lastReportedLine("99% used")}},
			{Provider: "Cursor", Value: AllowanceNotConnected,
				LastReported: []ProductAllowanceLastReported{lastReportedLine("98% used")}},
		}},
	}
	body := allowanceRender(t, page)
	allowanceAssertPassive(t, body)
	codex := glanceTile(t, glanceSection(t, body), "Codex")
	for _, want := range []string{">Unknown</span>", "No current percent", "Last reported, not current", "<strong>0% used</strong>",
		"Not current: the provider did not report when it measured this.",
		"Allowance freshness unknown or expired. Nothing is estimated."} {
		if !strings.Contains(codex, want) {
			t.Errorf("Codex tile lacks %q:\n%s", want, codex)
		}
	}
	for _, refused := range []string{"meter-fill", "Resets in", "% used</strong></p><svg"} {
		if strings.Contains(codex, refused) {
			t.Errorf("Codex tile draws a current value %q:\n%s", refused, codex)
		}
	}
	// The account section carries the full line: receipt time, not a provider time.
	for _, want := range []string{
		`Received <time datetime="2026-11-02T13:40:00Z">2 Nov 13:40 UTC</time>, not a provider time · Provider API · Account verified`,
		"Reported reset: 11 Nov 09:22 UTC. Not current: the provider did not report when it measured this.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("account section lacks %q", want)
		}
	}
	if strings.Contains(body, "99% used") || strings.Contains(body, "98% used") {
		t.Fatal("a measured or not-connected account rendered a last-reported line")
	}
	if n := strings.Count(body, "<strong>0% used</strong>"); n != 2 {
		t.Fatalf("last-reported line should appear on the glance tile and the account section, got %d", n)
	}
}

// A reported reset at or before the render clock says the value predates it,
// and an account reason of "No percent" does not sit above a shown percent.
func TestAllowanceLastReportedAfterReset(t *testing.T) {
	line := lastReportedLine("95% used")
	line.ResetAtExact = "2026-11-02T09:22:00Z"
	line.ProviderAt, line.ProviderAtExact = "1 Nov 09:00 UTC", "2026-11-01T09:00:00Z"
	page := ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"},
		AsOf: time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC),
		Usage: &ProductUsage{Allowance: []ProductAllowance{{Provider: "Codex", Value: AllowanceUnknown,
			Reason: "No percent allowance reported", LastReported: []ProductAllowanceLastReported{line}}}},
	}
	body := allowanceRender(t, page)
	for _, want := range []string{"No current percent allowance. Nothing is estimated.", "Unknown · No current percent allowance",
		"The reported reset time has passed; this value is from before it. Not current: a reset has happened since.",
		`provider measured <time datetime="2026-11-01T09:00:00Z">1 Nov 09:00 UTC</time>`} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if strings.Contains(body, "No percent allowance reported") || strings.Contains(body, "Reported reset: 11 Nov") {
		t.Error("passed reset still reads as no percent or a future reset")
	}
}

// Stale, measured and downgraded measured accounts never carry a line.
func TestAllowanceLastReportedOnlyFromUnknown(t *testing.T) {
	noPercent := ProductAllowanceWindow{Percent: "Unknown", Window: "7 days"}
	in := []ProductAllowance{
		{Provider: "Claude", Value: AllowanceStale, Windows: []ProductAllowanceWindow{{Percent: "5% used", PercentKnown: true, PercentValue: 5, Direction: "used"}},
			LastReported: []ProductAllowanceLastReported{lastReportedLine("7% used")}},
		{Provider: "Codex", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{noPercent},
			LastReported: []ProductAllowanceLastReported{lastReportedLine("8% used")}},
	}
	for _, a := range allowanceViews(in) {
		if a.LastReported != nil || a.LastReportedMore != 0 {
			t.Fatalf("%s %s carries %+v", a.Provider, a.Value, a.LastReported)
		}
	}
}

func TestLastReportedViewsBoundAndFill(t *testing.T) {
	noTime := lastReportedLine("2% used")
	noTime.ReadAtExact = ""
	in := []ProductAllowanceLastReported{{Percent: ""}, {Percent: "Unknown"}, {Percent: " \u200b", ReadAtExact: "2026-11-02T13:40:00Z"}, noTime}
	for i := 0; i < 6; i++ {
		l := lastReportedLine("1% used")
		l.Source = " "
		l.ProviderAt = "spoofed"
		in = append(in, l)
	}
	got, more := lastReportedViews(in)
	if len(got) != allowanceLastReportedMax || more != 2 {
		t.Fatalf("got %d lines, %d more", len(got), more)
	}
	for _, l := range got {
		if l.Source != "Unknown" || l.Percent != "1% used" || l.ProviderAt != "" {
			t.Fatalf("line not filled: %+v", l)
		}
	}
	a := ProductAllowance{LastReported: got, LastReportedMore: more}
	if len(a.GlanceLastReported()) != allowanceGlanceLastMax || a.GlanceLastMore() != 4 {
		t.Fatalf("glance %d, more %d", len(a.GlanceLastReported()), a.GlanceLastMore())
	}
	if got, more := lastReportedViews(nil); got != nil || more != 0 {
		t.Fatal("empty input made lines")
	}
}
