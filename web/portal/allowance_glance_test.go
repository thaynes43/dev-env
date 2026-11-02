package portal

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// These checks render synthetic Usage pages. They do not open a browser.

func glanceSection(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<section class="glance" aria-labelledby="usage-glance">(.*?)</section>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("Usage has no At a glance section")
	}
	return m[1]
}

func glanceTile(t *testing.T, section, provider string) string {
	t.Helper()
	for _, tile := range strings.Split(section, `<li class="glance-tile">`)[1:] {
		if strings.Contains(tile, `<h3 class="glance-name">`+provider+`</h3>`) {
			return tile
		}
	}
	t.Fatalf("no %s tile in %s", provider, section)
	return ""
}

func TestAllowanceGlanceKeepsProvidersFixedAndGapsUnknown(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	claude := ProductAllowanceWindow{
		Percent: "24% used", Window: "7 days", Reset: "Unknown", Observed: "2 Nov 12:18 UTC", Provenance: "Provider API",
		PercentKnown: true, PercentValue: 24, Direction: "used",
		ResetAt: "2026-11-04T09:30:00Z", ResetPrecision: "instant", ObservedAt: "2026-11-02T12:18:00Z",
	}
	codexLabel := ProductAllowanceWindow{
		Percent: "0% used", Window: "7 days", Reset: "Nov 9", Observed: "2 Nov 12:18 UTC", Provenance: "Provider API",
		PercentKnown: true, PercentValue: 0, Direction: "used", ResetPrecision: "display_only",
	}
	codexDisplay := ProductAllowanceWindow{Percent: "3% used", Window: "5 hours", Reset: "Unknown", Observed: "2 Nov 12:18 UTC", Provenance: "Provider API"}
	page := ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{
			{Provider: "Codex", Value: AllowanceStale, Windows: []ProductAllowanceWindow{codexLabel, codexDisplay}},
			{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{claude}},
		}},
	}
	body := allowanceRender(t, page)
	allowanceAssertPassive(t, body)
	section := glanceSection(t, body)

	order := regexp.MustCompile(`<h3 class="glance-name">([^<]+)</h3>`).FindAllStringSubmatch(section, -1)
	if len(order) != 3 || order[0][1] != "Claude" || order[1][1] != "Codex" || order[2][1] != "Cursor" {
		t.Fatalf("glance providers are not Claude, Codex, Cursor: %v", order)
	}

	c := glanceTile(t, section, "Claude")
	for _, want := range []string{`tone-ok">Measured<`, "24% used", `width="24"`, `preserveAspectRatio="none"`, "Resets in 1 day 20 hours 30 minutes · <time datetime=\"2026-11-04T09:30:00Z\">4 Nov 2026, 09:30:00</time> UTC, as of 2 Nov 2026, 13:00:00 UTC", `datetime="2026-11-04T09:30:00Z"`, "Seen 2 Nov 12:18 UTC."} {
		if !strings.Contains(c, want) {
			t.Errorf("Claude tile lacks %q: %s", want, c)
		}
	}

	x := glanceTile(t, section, "Codex")
	if !strings.Contains(x, `tone-wait">Stale<`) || !strings.Contains(x, "Last trusted reading, not live.") {
		t.Errorf("stale Codex tile does not say so: %s", x)
	}
	if !strings.Contains(x, "Resets Nov 9. Provider label, not an exact time.") || strings.Contains(x, "<time ") || strings.Contains(x, "Resets in") {
		t.Errorf("display-only reset became an instant or countdown: %s", x)
	}
	if strings.Count(x, `<rect class="meter-fill"`) != 1 || !strings.Contains(x, `width="0"`) || !strings.Contains(x, "Provider text: 3% used") || strings.Contains(x, "<strong>3% used</strong>") || !strings.Contains(x, "Numeric percent not reported, so no bar is drawn.") {
		t.Errorf("Codex: typed 0%% needs one bar, display-only 3%% needs text and no bar: %s", x)
	}

	k := glanceTile(t, section, "Cursor")
	if !strings.Contains(k, `tone-muted">Not connected<`) || !strings.Contains(k, "No percent reported") || !strings.Contains(k, "Nothing is estimated.") {
		t.Errorf("absent Cursor is not shown as an honest gap: %s", k)
	}
	if strings.Contains(k, "meter-fill") || strings.Contains(k, "% used") {
		t.Errorf("Cursor borrowed a figure: %s", k)
	}
}

func TestAllowanceGlanceUnknownReasonAndPastReset(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	past := ProductAllowanceWindow{
		Percent: "90% used", Window: "5 hours", Reset: "Unknown", Observed: "Unknown", Provenance: "Provider API",
		PercentKnown: true, PercentValue: 90, Direction: "used", ResetAt: "2026-11-02T12:00:00Z", ResetPrecision: "instant",
	}
	body := allowanceRender(t, ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{
			{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{past}},
			{Provider: "Cursor", Value: AllowanceUnknown, Reason: "No quota counters in the CLI"},
		}},
	})
	section := glanceSection(t, body)
	c := glanceTile(t, section, "Claude")
	if !strings.Contains(c, "Reset time has passed. Waiting for a new report.") || strings.Contains(c, "Resets in") {
		t.Errorf("past reset was not called out: %s", c)
	}
	if !strings.Contains(c, "Observation time not reported.") {
		t.Errorf("missing observation time was hidden: %s", c)
	}
	k := glanceTile(t, section, "Cursor")
	if !strings.Contains(k, `tone-muted">Unknown<`) || !strings.Contains(k, "No quota counters in the CLI. Nothing is estimated.") {
		t.Errorf("Unknown Cursor lost its reason: %s", k)
	}
	if !strings.Contains(glanceTile(t, section, "Codex"), "Not connected") {
		t.Error("absent Codex is not Not connected")
	}
}

func TestAllowanceMetersStretchToTheirBox(t *testing.T) {
	for _, class := range []string{"allowance-meter", "usage-meter", "glance-meter"} {
		re := regexp.MustCompile(`<svg class="` + class + `" viewBox="0 0 100 8" preserveAspectRatio="none"`)
		if !re.MatchString(viewsSource) {
			t.Errorf("%s keeps the default letterboxed aspect ratio, so a full-width bar renders 100px wide", class)
		}
	}
}

func TestAllowanceGlanceEdgeStates(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	typed := func(pct float64, dir, resetAt, precision, observedAt string) ProductAllowanceWindow {
		return ProductAllowanceWindow{
			Percent: "x", Window: "5 hours", Reset: "Unknown", Observed: "2 Nov 12:00 UTC", Provenance: "Provider API",
			PercentKnown: true, PercentValue: pct, Direction: dir, ResetAt: resetAt, ResetPrecision: precision, ObservedAt: observedAt,
		}
	}
	claudeWindows := []ProductAllowanceWindow{
		typed(10, "used", "", "", "2026-11-02T12:00:00Z"),
		typed(20, "used", "not-a-time", "instant", "2026-11-02T12:00:00Z"),
		typed(98, "used", "2026-11-02T14:00:00Z", "instant", "2026-11-02T12:00:00Z"),
	}
	claudeWindows[1].Provenance = "CLI"
	codexOdd := typed(150, "used", "2026-11-02T14:00:00Z", "weekly", "2026-11-03T00:00:00Z")
	codexOdd.Percent = "150% x"
	codexNoDir := typed(40, "", "", "", "2026-11-02T12:00:00Z")
	codexNoDir.Percent, codexNoDir.Reset = "40% x", "Tue 9am"
	body := allowanceRender(t, ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{
			{Provider: "Claude", Value: AllowanceMeasured, Windows: claudeWindows},
			{Provider: "Codex", Value: AllowanceCurrentSource, Windows: []ProductAllowanceWindow{codexOdd, codexNoDir}},
		}},
	})
	allowanceAssertPassive(t, body)
	section := glanceSection(t, body)

	c := glanceTile(t, section, "Claude")
	for _, want := range []string{"10% used", "20% used", "Reset time not reported.", "Reset time is not usable.", "+1 more", `href="#usage-allowance"`, "Observation time or source varies by window."} {
		if !strings.Contains(c, want) {
			t.Errorf("Claude tile lacks %q: %s", want, c)
		}
	}
	if strings.Contains(c, "98% used") {
		t.Errorf("third window shown in a two-window tile: %s", c)
	}

	x := glanceTile(t, section, "Codex")
	for _, want := range []string{`tone-wait">Current source<`, "Selected source report.", "Provider text: 150% x", "Percent is outside 0–100, so no bar is drawn.", "Provider text: 40% x", "Direction not reported, so no bar is drawn.", "Reset time is not usable.", "Resets Tue 9am. Not an exact time.", "An observation time is in the future."} {
		if !strings.Contains(x, want) {
			t.Errorf("Codex tile lacks %q: %s", want, x)
		}
	}
	if strings.Contains(x, "meter-fill") || strings.Contains(x, "<strong>") || strings.Contains(x, "<time ") {
		t.Errorf("refused percent or reset drew a bar, a figure or a time: %s", x)
	}

	unclocked := allowanceRender(t, ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"},
		Usage: &ProductUsage{Allowance: []ProductAllowance{{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{typed(5, "used", "2026-11-04T09:30:00Z", "instant", "")}}}},
	})
	u := glanceTile(t, glanceSection(t, unclocked), "Claude")
	if !strings.Contains(u, `Resets <time datetime="2026-11-04T09:30:00Z">`) || strings.Contains(u, "Resets in") {
		t.Errorf("no page clock must mean no countdown: %s", u)
	}
}
