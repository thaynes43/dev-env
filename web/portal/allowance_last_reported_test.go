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
		`Read <time datetime="2026-11-02T13:40:00Z">2 Nov 13:40 UTC</time> · Provider API · Account verified`,
		"Reported reset: 11 Nov 09:22 UTC. Not current: the provider did not report when it measured this.",
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
	if strings.Contains(body, "99% used") || strings.Contains(body, "98% used") {
		t.Fatal("a measured or not-connected account rendered a last-reported line")
	}
	if n := strings.Count(body, "<strong>0% used</strong>"); n != 2 {
		t.Fatalf("last-reported line should appear on the glance tile and the account section, got %d", n)
	}
}

func TestLastReportedViewsBoundAndFill(t *testing.T) {
	in := []ProductAllowanceLastReported{{Percent: ""}, {Percent: "Unknown"}}
	for i := 0; i < 6; i++ {
		l := lastReportedLine("1% used")
		l.Source = " "
		in = append(in, l)
	}
	got := lastReportedViews(in)
	if len(got) != allowanceLastReportedMax {
		t.Fatalf("got %d lines", len(got))
	}
	for _, l := range got {
		if l.Source != "Unknown" || l.Percent != "1% used" {
			t.Fatalf("line not filled: %+v", l)
		}
	}
	if lastReportedViews(nil) != nil {
		t.Fatal("empty input made lines")
	}
}
