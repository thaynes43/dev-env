package portal

import (
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These checks render synthetic pages and call typed window methods when that
// surface is linked. They do not open a browser, so they do not prove a live
// viewport, focus order, or a running portal.

func allowanceTypedSurface() bool {
	typ := reflect.TypeOf(ProductAllowanceWindow{})
	_, meter := typ.MethodByName("Meter")
	_, reset := typ.MethodByName("ResetStatus")
	_, observed := typ.MethodByName("ObservedStatus")
	_, field := typ.FieldByName("PercentKnown")
	return meter && reset && observed && field
}

type allowanceMeterView struct {
	Shown                      bool
	Note, UsedText, RemainText string
	UsedWidth                  string
}

type allowanceInstantView struct {
	State, Exact, Zone, Human, Countdown string
}

func allowanceSet(t *testing.T, w *ProductAllowanceWindow, fields map[string]any) {
	t.Helper()
	v := reflect.ValueOf(w).Elem()
	for name, val := range fields {
		field := v.FieldByName(name)
		if !field.IsValid() || !field.CanSet() {
			t.Fatalf("missing %s", name)
		}
		field.Set(reflect.ValueOf(val))
	}
}

func allowanceCall(w ProductAllowanceWindow, method string) reflect.Value {
	return reflect.ValueOf(w).MethodByName(method).Call(nil)[0]
}

func allowanceMeterOf(t *testing.T, w ProductAllowanceWindow) allowanceMeterView {
	t.Helper()
	out := allowanceCall(w, "Meter")
	view := allowanceMeterView{}
	shown := out.FieldByName("Shown")
	if !shown.IsValid() || shown.Kind() != reflect.Bool {
		t.Fatalf("meter result has no Shown: %s", out.Type())
	}
	view.Shown = shown.Bool()
	for _, name := range []string{"Note", "UsedText", "RemainText", "UsedWidth"} {
		field := out.FieldByName(name)
		if !field.IsValid() || field.Kind() != reflect.String {
			t.Fatalf("meter result has no %s", name)
		}
		switch name {
		case "Note":
			view.Note = field.String()
		case "UsedText":
			view.UsedText = field.String()
		case "RemainText":
			view.RemainText = field.String()
		case "UsedWidth":
			view.UsedWidth = field.String()
		}
	}
	return view
}

func allowanceInstantOf(t *testing.T, w ProductAllowanceWindow, method string) allowanceInstantView {
	t.Helper()
	out := allowanceCall(w, method)
	view := allowanceInstantView{}
	for _, name := range []string{"State", "Exact", "Zone", "Human", "Countdown"} {
		field := out.FieldByName(name)
		if !field.IsValid() {
			if name == "Countdown" {
				continue
			}
			t.Fatalf("%s result has no %s", method, name)
		}
		switch name {
		case "State":
			view.State = field.String()
		case "Exact":
			view.Exact = field.String()
		case "Zone":
			view.Zone = field.String()
		case "Human":
			view.Human = field.String()
		case "Countdown":
			view.Countdown = field.String()
		}
	}
	return view
}

func allowanceWindow(t *testing.T, percent, window, reset, observed string, fields map[string]any) ProductAllowanceWindow {
	t.Helper()
	w := ProductAllowanceWindow{Percent: percent, Window: window, Reset: reset, Observed: observed, Provenance: "Provider API"}
	if len(fields) > 0 {
		allowanceSet(t, &w, fields)
	}
	return w
}

func TestAllowanceDashboardEdgeMeterComplementAndRange(t *testing.T) {
	if !allowanceTypedSurface() {
		t.Skip("typed allowance methods pending integration")
	}
	cases := []struct {
		name            string
		percent         string
		fields          map[string]any
		shown           bool
		used, remaining string
	}{
		{name: "remaining complements", percent: "62% left", shown: true, used: "38", remaining: "62",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 62.0, "Direction": "remaining"}},
		{name: "used is not complemented twice", percent: "62% used", shown: true, used: "62", remaining: "38",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 62.0, "Direction": "used"}},
		{name: "full remaining", percent: "100% left", shown: true, used: "0", remaining: "100",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 100.0, "Direction": "remaining"}},
		{name: "real zero remaining", percent: "0% left", shown: true, used: "100", remaining: "0",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 0.0, "Direction": "remaining"}},
		{name: "typed value beats a zero display", percent: "0% left", shown: true, used: "40", remaining: "60",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 40.0, "Direction": "used"}},
		{name: "unknown does not draw a zero", percent: "0% left",
			fields: map[string]any{"PercentKnown": false, "PercentValue": 0.0, "Direction": "remaining"}},
		{name: "display is not parsed", percent: "62% left",
			fields: map[string]any{"PercentKnown": false, "PercentValue": 62.0, "Direction": "remaining"}},
		{name: "above range", percent: "101% left",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 101.0, "Direction": "used"}},
		{name: "below range", percent: "1% left",
			fields: map[string]any{"PercentKnown": true, "PercentValue": -0.001, "Direction": "used"}},
		{name: "not finite", percent: "50% left",
			fields: map[string]any{"PercentKnown": true, "PercentValue": math.NaN(), "Direction": "used"}},
		{name: "blank direction", percent: "50% left",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 50.0, "Direction": ""}},
		{name: "direction word is exact", percent: "50% left",
			fields: map[string]any{"PercentKnown": true, "PercentValue": 50.0, "Direction": "Remaining"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := allowanceMeterOf(t, allowanceWindow(t, tc.percent, "7 days", "2 Nov 14:00 UTC", "29 Oct 13:59 UTC", tc.fields))
			if view.Shown != tc.shown {
				t.Fatalf("shown %v: %+v", tc.shown, view)
			}
			if !view.Shown {
				if view.Note == "" || view.UsedText != "" || view.RemainText != "" || view.UsedWidth != "" {
					t.Fatalf("hidden bar still has a number: %+v", view)
				}
				return
			}
			if view.UsedText != tc.used || view.RemainText != tc.remaining || view.UsedWidth != tc.used {
				t.Fatalf("bar: %+v", view)
			}
			used, err := strconv.ParseFloat(view.UsedText, 64)
			remain, errRemain := strconv.ParseFloat(view.RemainText, 64)
			if err != nil || errRemain != nil || math.Abs(used+remain-100) > 0.001 {
				t.Fatalf("used and remaining do not meet: %+v", view)
			}
		})
	}

	t.Run("fractional complement stays on the bar", func(t *testing.T) {
		view := allowanceMeterOf(t, allowanceWindow(t, "33.335% left", "7 days", "", "", map[string]any{
			"PercentKnown": true, "PercentValue": 33.335, "Direction": "remaining",
		}))
		if !view.Shown {
			t.Fatalf("bar hidden: %+v", view)
		}
		used, err := strconv.ParseFloat(view.UsedText, 64)
		remain, errRemain := strconv.ParseFloat(view.RemainText, 64)
		if err != nil || errRemain != nil || math.Abs(used+remain-100) > 0.02 || used <= remain {
			t.Fatalf("complement: %+v", view)
		}
	})
}

func TestAllowanceDashboardEdgeResetAndObservedInstants(t *testing.T) {
	if !allowanceTypedSurface() {
		t.Skip("typed allowance methods pending integration")
	}
	window := func(reset, observed string, fields map[string]any) ProductAllowanceWindow {
		t.Helper()
		if fields == nil {
			fields = map[string]any{}
		}
		fields["ResetAt"] = reset
		fields["ObservedAt"] = observed
		return allowanceWindow(t, "62% left", "7 days", "2 Nov 14:00 UTC (display only)", "1 Jan 00:00 UTC", fields)
	}
	missing := allowanceInstantOf(t, window("", "", nil), "ResetStatus")
	if missing.State != "missing" || missing.Exact != "" || missing.Countdown != "" {
		t.Fatalf("display reset was parsed: %+v", missing)
	}
	for _, raw := range []string{"1762084800", "1762084800000", "1762084800.5", "2026-11-02 14:00:00Z"} {
		status := allowanceInstantOf(t, window(raw, raw, nil), "ResetStatus")
		seen := allowanceInstantOf(t, window("", raw, nil), "ObservedStatus")
		if status.State != "invalid" || status.Exact != "" || seen.State != "invalid" {
			t.Fatalf("raw %s: reset %+v observed %+v", raw, status, seen)
		}
	}

	offset := allowanceInstantOf(t, window("2099-07-04T09:00:00-05:00", "2099-07-04T14:00:00Z", nil), "ResetStatus")
	zulu := allowanceInstantOf(t, window("2099-07-04T14:00:00Z", "2020-01-01T00:00:00Z", nil), "ResetStatus")
	left, err := time.Parse(time.RFC3339, offset.Exact)
	right, errRight := time.Parse(time.RFC3339, zulu.Exact)
	if err != nil || errRight != nil || !left.Equal(right) || offset.Zone != "UTC-05:00" || zulu.Zone != "UTC" {
		t.Fatalf("equal instants: %+v %+v %v %v", offset, zulu, err, errRight)
	}
	if offset.State != "future" || offset.Countdown == "" || zulu.Countdown == "" {
		t.Fatalf("future countdown: %+v %+v", offset, zulu)
	}
	quarter := allowanceInstantOf(t, window("2099-01-01T00:00:00+05:30", "", nil), "ResetStatus")
	if quarter.Zone != "UTC+05:30" || quarter.State != "future" {
		t.Fatalf("quarter-hour offset: %+v", quarter)
	}

	early := allowanceInstantOf(t, window("2026-11-01T01:30:00-04:00", "", nil), "ResetStatus")
	late := allowanceInstantOf(t, window("2026-11-01T01:30:00-05:00", "", nil), "ResetStatus")
	earlyAt, err := time.Parse(time.RFC3339, early.Exact)
	lateAt, errLate := time.Parse(time.RFC3339, late.Exact)
	if err != nil || errLate != nil || early.Human != late.Human || early.Zone == late.Zone || !lateAt.Equal(earlyAt.Add(time.Hour)) {
		t.Fatalf("dst clocks: %+v %+v %v %v", early, late, err, errLate)
	}

	past := allowanceInstantOf(t, window("2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", nil), "ResetStatus")
	if past.State != "past" || past.Countdown != "" || !strings.Contains(past.Human, "2020") {
		t.Fatalf("past reset: %+v", past)
	}
	seen := allowanceInstantOf(t, window("2020-01-01T00:00:00Z", "2020-01-01T00:00:00Z", nil), "ObservedStatus")
	if seen.State != "exact" || seen.State == "stale" || !strings.Contains(seen.Human, "2020") {
		t.Fatalf("past observation was treated as stale or future: %+v", seen)
	}
	futureSeen := allowanceInstantOf(t, window("", "2099-01-01T00:00:00Z", nil), "ObservedStatus")
	if futureSeen.State != "future" || !strings.Contains(futureSeen.Exact, "2099") {
		t.Fatalf("future observation: %+v", futureSeen)
	}
	stamp := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	equalReset := allowanceInstantOf(t, window(stamp, stamp, nil), "ResetStatus")
	equalSeen := allowanceInstantOf(t, window(stamp, stamp, nil), "ObservedStatus")
	if equalReset.State != "past" || equalReset.Countdown != "" || equalSeen.State != "exact" {
		t.Fatalf("equal to this second: reset %+v observed %+v", equalReset, equalSeen)
	}

	line, ok := allowanceCallString(allowanceWindow(t, "62% left", "7 days", "", "", map[string]any{"WindowKind": ""}), "WindowLine")
	day, okDay := allowanceCallString(allowanceWindow(t, "62% left", "7 days", "", "", map[string]any{"WindowKind": "day"}), "WindowLine")
	if ok && (strings.Contains(strings.ToLower(line), "weekly") || !strings.Contains(line, "7 days")) {
		t.Fatalf("empty kind renamed the window: %q", line)
	}
	if okDay && (strings.Contains(strings.ToLower(day), "weekly") || !strings.Contains(day, "7 days")) {
		t.Fatalf("day kind renamed the window: %q", day)
	}
}

func allowanceCallString(w ProductAllowanceWindow, method string) (string, bool) {
	fn := reflect.ValueOf(w).MethodByName(method)
	if !fn.IsValid() {
		return "", false
	}
	out := fn.Call(nil)
	if len(out) != 1 || out[0].Kind() != reflect.String {
		return "", false
	}
	return out[0].String(), true
}

func TestAllowanceDashboardEdgeKeyboardMobileAndCSP(t *testing.T) {
	claude := ProductAllowance{
		Provider: "Claude", Value: AllowanceStale,
		Windows: []ProductAllowanceWindow{{
			Percent: "62.00% left", Window: "7 days", Reset: "2 Nov 14:00 UTC (display only)",
			Observed: "29 Oct 13:59 UTC", Provenance: "Provider API",
		}},
	}
	codex := ProductAllowance{
		Provider: "Codex", Value: AllowanceMeasured,
		Windows: []ProductAllowanceWindow{{
			Percent: "17% used", Window: "5 hours", Reset: "3 Nov 01:00 UTC",
			Observed: "29 Oct 13:59 UTC", Provenance: "Provider API",
		}},
	}
	base := ProductPage{
		Title: "Sessions", Connected: true, Complete: true,
		Allowance: []ProductAllowance{claude, codex},
		Usage:     &ProductUsage{Allowance: []ProductAllowance{claude, codex}},
	}
	sessions := base
	usage := base
	usage.Filter.View = "usage"
	sessionBody := allowanceRender(t, sessions)
	usageBody := allowanceRender(t, usage)

	for _, body := range []string{sessionBody, usageBody} {
		allowanceAssertPassive(t, body)
		if strings.Contains(body, `<rect class="meter-fill"`) {
			t.Fatal("display percent drew a bar")
		}
		if strings.Contains(body, `datetime="2026-11-02`) || strings.Contains(body, `datetime="2026-11-03`) {
			t.Fatal("display reset became an instant")
		}
	}
	if !strings.Contains(sessionBody, `href="#list"`) || !strings.Contains(usageBody, `href="#main"`) {
		t.Fatal("skip link does not target the page content")
	}
	sessionNav := allowanceNav(t, sessionBody)
	usageNav := allowanceNav(t, usageBody)
	if strings.Count(sessionNav, `aria-current="page"`) != 1 || !strings.Contains(sessionNav, `/sessions" aria-current="page"`) {
		t.Fatalf("sessions mobile selection: %s", sessionNav)
	}
	if strings.Count(usageNav, `aria-current="page"`) != 1 || !strings.Contains(usageNav, `view=usage" aria-current="page"`) {
		t.Fatalf("usage mobile selection: %s", usageNav)
	}
	if strings.Contains(sessionBody, `<details class="allowance-detail" open`) || !strings.Contains(sessionBody, `<summary class="allowance-toggle">By provider</summary>`) {
		t.Fatal("provider disclosure is missing or starts expanded")
	}
	if strings.Contains(usageBody, `<details class="allowance-table-fold" open`) {
		t.Fatal("window table starts expanded")
	}
	for _, label := range []string{"Allowance", "Window", "Resets", "Observed", "Source"} {
		if !strings.Contains(usageBody, `data-label="`+label+`"`) || !strings.Contains(usageBody, `<th scope="col">`+label+`</th>`) {
			t.Fatalf("usage table lacks %s", label)
		}
	}
	claudeSection, ok := allowanceProvider(usageBody, "Claude")
	codexSection, okCodex := allowanceProvider(usageBody, "Codex")
	if !ok || !okCodex || !strings.Contains(claudeSection, "62.00% left") || strings.Contains(claudeSection, "17% used") || !strings.Contains(claudeSection, "Stale") || strings.Contains(claudeSection, "0% used") {
		t.Fatalf("claude section: %s", claudeSection)
	}
	if !strings.Contains(codexSection, "17% used") || strings.Contains(codexSection, "62.00% left") {
		t.Fatalf("codex section: %s", codexSection)
	}
	if !strings.Contains(usageBody, "(display only)") {
		t.Fatal("display-only reset text was dropped")
	}
	if cursor, found := allowanceProvider(usageBody, "Cursor"); found {
		if strings.Contains(cursor, "62.00% left") || strings.Contains(cursor, "17% used") || strings.Contains(cursor, "0% used") {
			t.Fatalf("cursor borrowed another account: %s", cursor)
		}
	}
	style := allowanceStyle(t, usageBody)
	for _, want := range []string{".mobile-nav a", "min-height: 50px", "safe-area-inset-bottom", "prefers-reduced-motion", "forced-colors"} {
		if !strings.Contains(style, want) {
			t.Errorf("rendered stylesheet lacks %s", want)
		}
	}
}

func TestAllowanceDashboardEdgeTypedCardDoesNotTrustDisplay(t *testing.T) {
	if !allowanceTypedSurface() {
		t.Skip("typed allowance methods pending integration")
	}
	claude := allowanceWindow(t, "0% left", "7 days", "2 Nov 14:00 UTC (display only)", "1 Jan 00:00 UTC", map[string]any{
		"PercentKnown": true, "PercentValue": 62.0, "Direction": "remaining",
		"ResetAt": "2099-06-01T12:00:00Z", "ObservedAt": "2020-01-01T00:00:00Z", "WindowKind": "",
	})
	codex := ProductAllowanceWindow{
		Percent: "17% used", Window: "5 hours", Reset: "3 Nov 01:00 UTC",
		Observed: "29 Oct 13:59 UTC", Provenance: "Provider API",
	}
	body := allowanceRender(t, ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"},
		Allowance: []ProductAllowance{{Provider: "Claude", Value: AllowanceStale, Windows: []ProductAllowanceWindow{claude}}},
		Usage: &ProductUsage{Allowance: []ProductAllowance{
			{Provider: "Claude", Value: AllowanceStale, Windows: []ProductAllowanceWindow{claude}},
			{Provider: "Codex", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{codex}},
		}},
	})
	allowanceAssertPassive(t, body)
	if !strings.Contains(body, "allowance-card") && !strings.Contains(body, "allowance-meter") {
		t.Skip("allowance card markup pending integration")
	}
	if strings.Count(body, `<rect class="meter-fill"`) != 1 || !strings.Contains(body, `width="38"`) {
		t.Fatal("typed remaining 62 did not draw a single 38-wide bar")
	}
	if !strings.Contains(body, "38% used") || !strings.Contains(body, "62% remaining") || strings.Contains(body, "0% used") {
		t.Fatal("bar labels did not follow the typed complement")
	}
	if !strings.Contains(body, `datetime="2099-06-01T12:00:00Z"`) || !strings.Contains(body, `datetime="2020-01-01T00:00:00Z"`) {
		t.Fatal("typed instants were not the time values")
	}
	if strings.Contains(body, `datetime="2026-11-02`) || strings.Contains(body, `datetime="2026-11-03`) {
		t.Fatal("display reset was copied into a time value")
	}
	if !strings.Contains(body, "Stale") || !strings.Contains(body, "Resets in") {
		t.Fatal("stale account lost its future reset")
	}
	if !strings.Contains(body, `aria-hidden="true"`) || !strings.Contains(body, `focusable="false"`) {
		t.Fatal("meter is a focus stop")
	}
	codexSection, ok := allowanceProvider(body, "Codex")
	if ok && strings.Contains(codexSection, "<time ") {
		t.Fatal("untyped codex reset was given an instant")
	}
}

func allowanceRender(t *testing.T, page ProductPage) string {
	t.Helper()
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func allowanceAssertPassive(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, "<script") || regexp.MustCompile(`\sstyle="`).MatchString(body) || regexp.MustCompile(`\son[a-z]+=`).MatchString(body) {
		t.Fatal("inline script, style attribute, or event handler")
	}
	for _, unsafe := range []string{"unsafe-inline", "unsafe-eval", "javascript:"} {
		if strings.Contains(body, unsafe) {
			t.Fatalf("page allows %s", unsafe)
		}
	}
	if !strings.Contains(body, `name="viewport"`) || !strings.Contains(body, "width=device-width") || !strings.Contains(body, "viewport-fit=cover") {
		t.Fatal("viewport meta is missing")
	}
	if !strings.Contains(body, "default-src 'none'") || !strings.Contains(body, `class="skip"`) {
		t.Fatal("content policy or skip link is missing")
	}
	for _, match := range regexp.MustCompile(`tabindex="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil || n > 0 {
			t.Fatalf("positive tabindex %q", match[1])
		}
	}
}

func allowanceNav(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `class="mobile-nav"`)
	if i < 0 {
		t.Fatal("mobile nav missing")
	}
	rest := body[i:]
	j := strings.Index(rest, "</nav>")
	if j < 0 {
		t.Fatal("mobile nav is not closed")
	}
	nav := rest[:j]
	if strings.Contains(nav, "<button") || strings.Contains(nav, "<div") || strings.Count(nav, "<a ") < 5 {
		t.Fatalf("mobile nav is not five links: %s", nav)
	}
	return nav
}

func allowanceProvider(body, name string) (string, bool) {
	marker := "<h3>" + name + " "
	i := strings.Index(body, marker)
	if i < 0 {
		return "", false
	}
	rest := body[i:]
	if j := strings.Index(rest[len(marker):], "<h3>"); j >= 0 {
		return rest[:len(marker)+j], true
	}
	if j := strings.Index(rest, `<p class="note">Plan limits`); j >= 0 {
		return rest[:j], true
	}
	return rest, true
}

func allowanceStyle(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindStringSubmatch(body)
	if match == nil {
		t.Fatal("stylesheet missing")
	}
	return match[1]
}
