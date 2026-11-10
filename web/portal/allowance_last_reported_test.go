package portal

import (
	"regexp"
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
		"This window is over: the reported reset has passed, so its percent is not shown.",
		"Last reading was 2 Nov 13:40 UTC.",
		`provider measured <time datetime="2026-11-01T09:00:00Z">1 Nov 09:00 UTC</time>`,
		`class="allowance-last-over"`} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if strings.Contains(body, "95% used") || strings.Contains(body, "No percent allowance reported") || strings.Contains(body, "Reported reset: 11 Nov") || strings.Contains(body, "<strong>") {
		t.Error("passed reset still shows a percent or a future reset")
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

// A passed reset keeps an unverified account visible in the reason, a future
// provider time is flagged, and the overview's +N link names its provider.
func TestAllowanceLastReportedUnverifiedFutureAndMoreLabel(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC)
	passed := lastReportedLine("95% used")
	passed.Account, passed.ResetAtExact = "Account not verified", "2026-11-02T09:22:00Z"
	future := lastReportedLine("5% used")
	future.ProviderAt, future.ProviderAtExact = "9 Nov 00:00 UTC", "2026-11-09T00:00:00Z"
	extra := lastReportedLine("6% used")
	page := ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{{Provider: "Codex", Value: AllowanceUnknown, Reason: "Allowance validation unavailable",
			LastReported: []ProductAllowanceLastReported{passed, future, extra}}}},
	}
	body := allowanceRender(t, page)
	codex := glanceTile(t, glanceSection(t, body), "Codex")
	for _, want := range []string{
		"This window is over: the reported reset has passed, so its percent is not shown.",
		"Last reading was 2 Nov 13:40 UTC.",
		"That observation time is in the future.",
		`aria-label="Codex: 1 more last-reported value"`,
	} {
		if !strings.Contains(codex, want) {
			t.Errorf("Codex tile lacks %q:\n%s", want, codex)
		}
	}
	if strings.Contains(codex, "95% used") || strings.Contains(body, "95% used") {
		t.Error("a passed reset still shows its percent")
	}
	if !strings.Contains(body, "Account not verified") {
		t.Error("an unverified account was dropped after its reset")
	}
	if strings.Contains(body, "a reset has happened") {
		t.Error("copy claims a reset happened")
	}
}

// The four Usage states of DESIGN-059 section 8. The template does not branch
// on color scheme: both themes paint allowance-last-fresh with --ink and
// allowance-last-stale with --muted.
func TestAllowanceReadingAgeFourStates(t *testing.T) {
	asOf := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	line := func(percent string, read, reset time.Time) ProductAllowanceLastReported {
		return ProductAllowanceLastReported{
			Percent: percent, Window: "7 days",
			Reset:        "Reported reset: " + reset.UTC().Format("2 Jan 15:04 UTC") + ".",
			ReadAt:       read.UTC().Format("2 Jan 15:04 UTC"),
			ReadAtExact:  read.UTC().Format(time.RFC3339Nano),
			ResetAtExact: reset.UTC().Format(time.RFC3339Nano),
			Source:       "Passive event", Account: "Account not verified",
			Why:     "Not current or trusted: this reading's account is not verified.",
			Session: true,
		}
	}
	order := func(tile string, parts ...string) {
		t.Helper()
		at := -1
		for _, part := range parts {
			i := strings.Index(tile, part)
			if i < 0 || i <= at {
				t.Errorf("headline order broke at %q:\n%s", part, tile)
			}
			at = i
		}
	}
	page := func(rows []ProductAllowance) string {
		t.Helper()
		return allowanceRender(t, ProductPage{
			Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
			Usage: &ProductUsage{Allowance: rows},
		})
	}
	tile := func(body, provider string) string {
		t.Helper()
		return glanceTile(t, glanceSection(t, body), provider)
	}

	freshRead := asOf.Add(-12 * time.Minute)
	freshReset := asOf.Add(48 * time.Hour)
	fresh := page([]ProductAllowance{{Provider: "Claude", Value: AllowanceUnknown, Reason: "Allowance freshness unknown or expired",
		LastReported: []ProductAllowanceLastReported{line("34% used", freshRead, freshReset)}}})
	claude := tile(fresh, "Claude")
	order(claude,
		"<strong>34% of the week used</strong>",
		"resets "+freshReset.UTC().Format("Monday 2 Jan 15:04 UTC"),
		"reported 12 minutes ago",
		sessionReadingCaveat,
	)
	for _, want := range []string{
		"Passive event", "Account not verified",
		`Received <time datetime="2026-11-10T11:48:00Z">10 Nov 11:48 UTC</time>`,
		"Reported reset: 12 Nov 12:00 UTC.",
		"Not current or trusted: this reading&#39;s account is not verified.",
		`<details class="session-detail">`,
	} {
		if !strings.Contains(claude, want) {
			t.Errorf("fresh tile lacks %q:\n%s", want, claude)
		}
	}
	for _, banned := range []string{">Unknown<", "No current percent", "Last reported, not current", "session-stale", allowanceWindowOver} {
		if strings.Contains(claude, banned) {
			t.Errorf("fresh tile shows %q:\n%s", banned, claude)
		}
	}
	if !strings.Contains(fresh, "Last reported, not current") || !strings.Contains(fresh, "Passive event") {
		t.Error("account section dropped the trust lines")
	}

	// Exactly 30 minutes is still a plain percent. Older than that is greyed.
	boundary := page([]ProductAllowance{{Provider: "Codex", Value: AllowanceUnknown, Reason: "Allowance freshness unknown or expired",
		LastReported: []ProductAllowanceLastReported{
			line("10% used", asOf.Add(-allowanceReportedStaleAfter), asOf.Add(24*time.Hour)),
			line("11% used", asOf.Add(-allowanceReportedStaleAfter-time.Second), asOf.Add(24*time.Hour)),
		}}})
	codex := tile(boundary, "Codex")
	if !strings.Contains(codex, `<p class="session-headline"><strong>10% of the week used</strong>`) || !strings.Contains(codex, "reported 30 minutes ago") {
		t.Errorf("a 30 minute reading was greyed or lost its age:\n%s", codex)
	}
	if strings.Contains(codex, `class="session-headline session-stale"`) {
		t.Error("a 30 minute reading was greyed")
	}
	if !strings.Contains(codex, `class="session-secondary session-stale">11% of the week used`) || !strings.Contains(codex, `class="session-age session-stale">reported 31 minutes ago`) {
		t.Errorf("a reading older than 30 minutes lacks its grey age:\n%s", codex)
	}

	old := page([]ProductAllowance{{Provider: "Claude", Value: AllowanceUnknown, Reason: "Allowance freshness unknown or expired",
		LastReported: []ProductAllowanceLastReported{line("65% used", asOf.Add(-3*time.Hour), asOf.Add(36*time.Hour))}}})
	claude = tile(old, "Claude")
	for _, want := range []string{`class="session-headline session-stale"><strong>65% of the week used</strong>`, "reported 3 hours ago", sessionReadingCaveat} {
		if !strings.Contains(claude, want) {
			t.Errorf("stale tile lacks %q:\n%s", want, claude)
		}
	}
	if strings.Contains(claude, allowanceWindowOver) || strings.Contains(claude, ">Unknown<") || strings.Contains(claude, "No current percent") {
		t.Errorf("stale tile dropped its percent or restored the old words:\n%s", claude)
	}

	past := page([]ProductAllowance{{Provider: "Codex", Value: AllowanceUnknown, Reason: "No percent allowance reported",
		LastReported: []ProductAllowanceLastReported{line("95% used", asOf.Add(-2*time.Hour), asOf.Add(-time.Minute))}}})
	codex = tile(past, "Codex")
	order(codex, allowanceWindowOver, "Last reading was 10 Nov 10:00 UTC.", sessionReadingCaveat)
	if strings.Contains(past, "95% used") || strings.Contains(past, "<strong>") || strings.Contains(codex, ">Unknown<") || strings.Contains(codex, "No current percent") {
		t.Errorf("past reset still shows a percent or the old words:\n%s", codex)
	}

	none := page(nil)
	for _, provider := range []string{"Claude", "Codex"} {
		got := tile(none, provider)
		if !strings.Contains(got, `tone-muted">No reading<`) || !strings.Contains(got, allowanceNoReading) {
			t.Errorf("%s tile lacks the no-reading sentence:\n%s", provider, got)
		}
		if strings.Contains(got, ">Unknown<") || strings.Contains(got, "Unknown ·") || strings.Contains(got, "%") {
			t.Errorf("%s tile still says Unknown or shows a percent:\n%s", provider, got)
		}
	}
	cursor := tile(none, "Cursor")
	for _, want := range []string{
		cursorPlanSentence,
		`href="` + cursorPlanURL + `"`,
		`target="_blank"`,
		`rel="noopener noreferrer"`,
		"cursor.com/dashboard",
		"opens in a new tab",
	} {
		if !strings.Contains(cursor, want) && !strings.Contains(cursor, strings.ReplaceAll(want, "'", "&#39;")) {
			t.Errorf("Cursor tile lacks %q:\n%s", want, cursor)
		}
	}
	if strings.Contains(cursor, "Not connected") || strings.Contains(cursor, "No percent reported") || strings.Contains(cursor, allowanceNoReading) {
		t.Errorf("Cursor tile kept the old gap:\n%s", cursor)
	}
	grok := tile(none, "Grok")
	if !strings.Contains(grok, `tone-muted">Not connected<`) || strings.Contains(grok, allowanceNoReading) || strings.Contains(grok, cursorPlanSentence) {
		t.Errorf("Grok should stay not connected:\n%s", grok)
	}
}

func TestSessionTilePutsAShorterWindowUnderTheWeek(t *testing.T) {
	asOf := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	weekReset := asOf.Add(48 * time.Hour)
	hourReset := asOf.Add(3 * time.Hour)
	line := func(percent, window string, reset time.Time) ProductAllowanceLastReported {
		return ProductAllowanceLastReported{
			Percent: percent, Window: window, Session: true,
			Reset:        "Reported reset: " + reset.UTC().Format("2 Jan 15:04 UTC") + ".",
			ReadAt:       asOf.Add(-2 * time.Minute).UTC().Format("2 Jan 15:04 UTC"),
			ReadAtExact:  asOf.Add(-2 * time.Minute).UTC().Format(time.RFC3339),
			ResetAtExact: reset.UTC().Format(time.RFC3339),
			Source:       "Passive event", Account: "Account not verified",
			Why: "Not current or trusted: this reading's account is not verified.",
		}
	}
	body := allowanceRender(t, ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, AsOf: asOf,
		Usage: &ProductUsage{Allowance: []ProductAllowance{{Provider: "Codex", Value: AllowanceUnknown, Reason: "Allowance freshness unknown or expired",
			LastReported: []ProductAllowanceLastReported{
				line("12% used", "5 hours", hourReset),
				line("34% used", "7 days", weekReset),
			}}}},
	})
	codex := glanceTile(t, glanceSection(t, body), "Codex")
	weekAt := strings.Index(codex, "<strong>34% of the week used</strong>")
	resetAt := strings.Index(codex, "resets Thursday 12 Nov 12:00 UTC")
	ageAt := strings.Index(codex, "reported 2 minutes ago")
	hourAt := strings.Index(codex, `class="session-secondary">12% of the 5 hours used`)
	caveatAt := strings.Index(codex, sessionReadingCaveat)
	if weekAt < 0 || resetAt < weekAt || ageAt < resetAt || hourAt < ageAt || caveatAt < hourAt {
		t.Fatalf("shorter window is not a smaller line under the week:\n%s", codex)
	}
	if strings.Count(codex, sessionReadingCaveat) != 1 {
		t.Fatalf("caveat count %d:\n%s", strings.Count(codex, sessionReadingCaveat), codex)
	}
	if strings.Contains(codex, ">Unknown<") || strings.Contains(codex, "No current percent") {
		t.Fatalf("shorter window restored the old words:\n%s", codex)
	}
	if !strings.Contains(codex, "Reported reset: 10 Nov 15:00 UTC.") {
		t.Fatal("the 5 hour reset left the details")
	}
}

func TestReportedAgeWords(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "reported just now"},
		{time.Minute, "reported 1 minute ago"},
		{30*time.Minute + time.Second, "reported 31 minutes ago"},
		{31 * time.Minute, "reported 31 minutes ago"},
		{time.Hour, "reported 1 hour ago"},
		{3 * time.Hour, "reported 3 hours ago"},
		{26 * time.Hour, "reported 1 day ago"},
		{3 * 24 * time.Hour, "reported 3 days ago"},
	}
	for _, tc := range cases {
		if got := reportedAgo(tc.d); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestAllowanceReadingColorsExistInBothThemes(t *testing.T) {
	lightAt := strings.Index(viewsStyle, "@media (prefers-color-scheme: light)")
	if lightAt < 0 {
		t.Fatal("light theme missing")
	}
	dark := viewsStyle[:lightAt]
	light := viewsStyle[lightAt:]
	for name, block := range map[string]string{"dark": dark, "light": light} {
		if !strings.Contains(block, "--ink:") || !strings.Contains(block, "--muted:") {
			t.Errorf("%s theme lacks --ink or --muted", name)
		}
	}
	for _, rule := range []string{
		".allowance-last-fresh .allowance-last-value strong { color: var(--ink);",
		".allowance-last-stale .allowance-last-value, .allowance-last-stale .allowance-last-value strong, .allowance-last-stale .allowance-last-age { color: var(--muted); }",
		".allowance-last-over-line, .glance-gap { color: var(--ink); }",
		".session-headline { margin: 0; color: var(--ink);",
		".session-headline.session-stale, .session-headline.session-stale strong, .session-secondary.session-stale, .session-age.session-stale { color: var(--muted); }",
		".cursor-plan { margin: 0; color: var(--ink);",
	} {
		if !strings.Contains(viewsStyle, rule) {
			t.Errorf("stylesheet lacks %q", rule)
		}
	}
}

func TestAllowanceReadingCopyWrapsOnAPhone(t *testing.T) {
	for _, rule := range []string{
		".allowance-last-age, .allowance-last-over-line, .glance-gap { margin: 2px 0 0; max-width: 100%; font-size: 13px; font-weight: 400; letter-spacing: 0; line-height: 1.45; overflow-wrap: anywhere; }",
		".glance-gap { margin: 0; }",
		".glance-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 220px), 1fr));",
		".session-headline { margin: 0; color: var(--ink); font-size: 20px; font-weight: 650; letter-spacing: -.4px; line-height: 1.25; overflow-wrap: anywhere; }",
		".session-reset, .session-age, .session-when, .session-caveat, .session-secondary { margin: 2px 0 0; max-width: 100%; letter-spacing: 0; line-height: 1.45; overflow-wrap: anywhere; }",
		".cursor-plan { margin: 0; color: var(--ink); font-size: 13px; font-weight: 400; letter-spacing: 0; line-height: 1.45; overflow-wrap: anywhere; }",
		".cursor-plan-link a { overflow-wrap: anywhere; }",
	} {
		if !strings.Contains(viewsStyle, rule) {
			t.Errorf("phone layout lacks %q", rule)
		}
	}
	phone := regexp.MustCompile(`(?s)@media \(max-width: 700px\) \{.*?\n\}`).FindString(viewsStyle)
	if phone == "" || !strings.Contains(phone, "overflow: visible") {
		t.Fatal("phone layout is missing or clips the page")
	}
	for _, sel := range []string{".glance-gap", ".allowance-last-age", ".allowance-last-over-line", ".session-headline", ".session-secondary", ".cursor-plan"} {
		if strings.Contains(viewsStyle, sel+" {") && strings.Contains(viewsStyle, sel+" { white-space: nowrap") {
			t.Errorf("%s does not wrap", sel)
		}
	}
}
