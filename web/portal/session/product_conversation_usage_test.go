package session

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

var convNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func convTokenJSON(reported bool, amount int) string {
	if !reported {
		return `{"state":"Unknown"}`
	}
	return `{"state":"Reported","amount":` + strconv.Itoa(amount) + `}`
}

// convFigure is a Final figure for one model: input in, output a Reported zero,
// cache read 50 and cache creation Unknown.
func convFigure(model string, in int, observed string) string {
	return `{"resultState":"Final","models":[{"model":"` + model + `","inputTokens":` + convTokenJSON(true, in) + `,"outputTokens":` + convTokenJSON(true, 0) +
		`,"cacheReadInputTokens":` + convTokenJSON(true, 50) + `,"cacheCreationInputTokens":` + convTokenJSON(false, 0) + `}],"turns":3,"skipped":0,"observedAt":"` + observed + `"}`
}

func convPartial(figure, reason string) string {
	return strings.Replace(strings.Replace(figure, `"Final"`, `"Partial"`, 1), `"turns":3`, `"turns":3,"reasonCode":"`+reason+`"`, 1)
}

// convRow is one session row carrying figure, or no key when figure is empty.
func convRow(name, mode, provider, figure string) string {
	activity := filterActivity(mode, provider, "", "2026-10-25T11:00:00Z")
	if figure != "" {
		activity = strings.Replace(activity, `"provider":"`+provider+`",`, `"provider":"`+provider+`","conversationUsage":`+figure+`,`, 1)
	}
	return filterRow(name, "dev-env", "Running", activity)
}

func convDecode(t *testing.T, rows ...string) apiv1.PortalSessionPage {
	t.Helper()
	d, err := decodeSessionPage([]byte(activityPage(rows...)), convNow)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func convSection(t *testing.T, page portal.ProductPage, name string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, s := range findRow(t, page, name).Sections {
		if s.Title == "Conversation usage" {
			for _, f := range s.Fields {
				out[f.Label] = f.Value
			}
			out["note"] = s.Note
		}
	}
	return out
}

func TestActivityDecodesWithAndWithoutConversationUsage(t *testing.T) {
	d := convDecode(t,
		convRow("with", "remote", "claude", convFigure("claude-opus-5-5", 1234, "2026-10-25T11:55:00Z")),
		convRow("without", "remote", "claude", ""))
	got, none := d.Sessions[0].Activity.ConversationUsage, d.Sessions[1].Activity.ConversationUsage
	if none != nil {
		t.Fatal("a view without the key produced a figure")
	}
	if got == nil || got.ResultState != "Final" || got.Turns != 3 || len(got.Models) != 1 || !got.ObservedAt.Equal(convNow.Add(-5*time.Minute)) {
		t.Fatalf("%+v", got)
	}
	m := got.Models[0]
	if *m.InputTokens.Amount != 1234 || m.OutputTokens.State != "Reported" || *m.OutputTokens.Amount != 0 || m.CacheCreationInputTokens.State != "Unknown" || m.CacheCreationInputTokens.Amount != nil {
		t.Fatalf("%+v", m)
	}
	// Measured conversation usage is its own field: a headless task's initial usage is untouched.
	if d.Sessions[0].Activity.Usage.Scope != protocol.UsageUnknown {
		t.Fatalf("%+v", d.Sessions[0].Activity.Usage)
	}
}

// The portal's decoder refuses an activity key it does not know. That is why the
// operator sends conversationUsage only behind --portal-conversation-usage: a
// portal from before this change would read the whole view as unavailable.
func TestActivityDecoderIsStrictAboutKeysItDoesNotKnow(t *testing.T) {
	row := strings.Replace(convRow("x", "remote", "claude", ""), `"provider":"claude",`, `"provider":"claude","conversationUsageV2":{},`, 1)
	if _, err := decodeSessionPage([]byte(activityPage(row)), convNow); err == nil {
		t.Fatal("an unknown activity key was accepted")
	}
}

func TestConversationUsageDecodeRefusesAnythingOutsideTheAllowlist(t *testing.T) {
	good := convFigure("claude-opus-5-5", 10, "2026-10-25T11:55:00Z")
	models := good[strings.Index(good, `"models":[`) : strings.Index(good, `],"turns"`)+1]
	for name, tc := range map[string]struct {
		mode, provider, figure string
	}{
		"extra key":              {"remote", "claude", strings.Replace(good, `"turns":3`, `"turns":3,"path":"/home/x"`, 1)},
		"missing observedAt":     {"remote", "claude", strings.Replace(good, `,"observedAt":"2026-10-25T11:55:00Z"`, ``, 1)},
		"missing models":         {"remote", "claude", `{"resultState":"Final","turns":1,"skipped":0,"observedAt":"2026-10-25T11:55:00Z"}`},
		"unknown result":         {"remote", "claude", strings.Replace(good, `"Final"`, `"Unknown"`, 1)},
		"other result":           {"remote", "claude", strings.Replace(good, `"Final"`, `"Complete"`, 1)},
		"final with a reason":    {"remote", "claude", strings.Replace(good, `"turns":3`, `"turns":3,"reasonCode":"skipped_records"`, 1)},
		"partial without reason": {"remote", "claude", strings.Replace(good, `"Final"`, `"Partial"`, 1)},
		"partial odd reason":     {"remote", "claude", convPartial(good, "<b>")},
		"partial unlisted":       {"remote", "claude", convPartial(good, "because")},
		"case alias":             {"remote", "claude", strings.Replace(good, `"resultState"`, `"ResultState"`, 1)},
		"null":                   {"remote", "claude", `null`},
		"a list":                 {"remote", "claude", `[]`},
		"empty models":           {"remote", "claude", strings.Replace(good, models, `"models":[]`, 1)},
		"reported without count": {"remote", "claude", strings.Replace(good, `"inputTokens":{"state":"Reported","amount":10}`, `"inputTokens":{"state":"Reported"}`, 1)},
		"unknown with a count":   {"remote", "claude", strings.Replace(good, `"cacheCreationInputTokens":{"state":"Unknown"}`, `"cacheCreationInputTokens":{"state":"Unknown","amount":0}`, 1)},
		"negative count":         {"remote", "claude", strings.Replace(good, `"amount":10`, `"amount":-1`, 1)},
		"model markup":           {"remote", "claude", strings.Replace(good, `claude-opus-5-5`, `<script>`, 1)},
		"bad time":               {"remote", "claude", strings.Replace(good, `2026-10-25T11:55:00Z`, `yesterday`, 1)},
		"negative turns":         {"remote", "claude", strings.Replace(good, `"turns":3`, `"turns":-3`, 1)},
		"a task session":         {"task", "claude", good},
		"unknown provider":       {"remote", "opencode", good},
	} {
		row := convRow("x", tc.mode, tc.provider, tc.figure)
		if _, err := decodeSessionPage([]byte(activityPage(row)), convNow); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// A time a little ahead of this portal's clock is clamped, not fatal.
	d := convDecode(t, convRow("skew", "local", "claude", strings.Replace(good, `2026-10-25T11:55:00Z`, `2026-10-25T12:00:30Z`, 1)))
	if at := d.Sessions[0].Activity.ConversationUsage.ObservedAt; !at.Equal(convNow) {
		t.Fatalf("not clamped: %s", at)
	}
}

func TestConversationUsageSectionKeepsUnknownApartFromZeroAndCarriesTheReason(t *testing.T) {
	final := convFigure("claude-opus-5-5", 10, "2026-10-25T11:55:00Z")
	d := convDecode(t, convRow("final", "remote", "claude", final), convRow("partial", "local", "claude", convPartial(final, "skipped_records")),
		convRow("odd", "remote", "claude", convPartial(final, "count_overflow")), convRow("none", "remote", "claude", ""))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	f := convSection(t, page, "final")
	if f["Result"] != "Final" || f["Freshness"] != "Current" || f["Provider calls counted"] != "3" || f["Records skipped"] != "0" {
		t.Fatalf("%v", f)
	}
	// An explicit zero reads 0 and an unmeasured counter reads Unknown, never 0.
	line := f["claude-opus-5-5"]
	if !strings.Contains(line, "Input 10 ") || !strings.Contains(line, "Output 0 ") || !strings.Contains(line, "Cache read 50 ") || !strings.HasSuffix(line, "Cache create Unknown") {
		t.Fatalf("%q", line)
	}
	if !strings.Contains(f["note"], "Not a cost, a charge or an allowance") || strings.Contains(f["note"], "USD") {
		t.Fatalf("%q", f["note"])
	}
	if p := convSection(t, page, "partial"); p["Result"] != "Partial: some records could not be read" {
		t.Fatalf("%v", p)
	}
	// A reason this portal has no wording for shows as itself.
	if o := convSection(t, page, "odd"); o["Result"] != "Partial: count_overflow" {
		t.Fatalf("%v", o)
	}
	// A session with no key has no panel at all: absent is not a zero.
	if n := convSection(t, page, "none"); len(n) != 0 {
		t.Fatalf("%v", n)
	}
}

func TestConversationUsageIsMarkedStaleAfterFifteenMinutes(t *testing.T) {
	at := func(age time.Duration) string { return convNow.Add(-age).Format(time.RFC3339) }
	d := convDecode(t,
		convRow("fresh", "remote", "claude", convFigure("claude-opus-5-5", 10, at(time.Minute))),
		convRow("edge", "remote", "claude", convFigure("claude-opus-5-5", 10, at(conversationUsageStaleAfter))),
		convRow("old", "remote", "claude", convFigure("claude-opus-5-5", 10, at(conversationUsageStaleAfter+time.Second))),
		convRow("ancient", "remote", "claude", convFigure("claude-opus-5-5", 10, at(26*time.Hour))))
	if conversationUsageStaleAfter != 15*time.Minute {
		t.Fatalf("the threshold is %s", conversationUsageStaleAfter)
	}
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	for name, stale := range map[string]bool{"fresh": false, "edge": false, "old": true, "ancient": true} {
		f := convSection(t, page, name)
		if got := strings.HasPrefix(f["Freshness"], "Stale"); got != stale {
			t.Errorf("%s: freshness %q", name, f["Freshness"])
		}
		if got := strings.HasSuffix(findRow(t, page, name).Meta, "stale"); got != stale {
			t.Errorf("%s: meta %q", name, findRow(t, page, name).Meta)
		}
	}
	// The same figure read later is stale: the clock decides, not the figure.
	later := sessionsPage(d.Sessions, true, "observed", convNow.Add(20*time.Minute), portal.ProductFilter{})
	if got := convSection(t, later, "fresh")["Freshness"]; !strings.HasPrefix(got, "Stale") {
		t.Fatalf("%q", got)
	}
}

func TestConversationUsageUsageTableIsPerProviderAndModelAndApart(t *testing.T) {
	codex := func(in int) string {
		return strings.Replace(convFigure("gpt-6-astra", in, "2026-10-25T11:50:00Z"), `"cacheReadInputTokens":`+convTokenJSON(true, 50), `"cacheReadInputTokens":`+convTokenJSON(true, 900), 1)
	}
	d := convDecode(t,
		convRow("claude-a", "remote", "claude", convFigure("claude-opus-5-5", 100, "2026-10-25T11:55:00Z")),
		convRow("claude-b", "local", "claude", convPartial(convFigure("claude-opus-5-5", 200, "2026-10-24T11:55:00Z"), "skipped_records")),
		convRow("claude-c", "remote", "claude", convFigure("claude-haiku-4-5", 7, "2026-10-25T11:55:00Z")),
		convRow("codex-a", "remote", "codex", codex(2500)),
		convRow("claude-none", "remote", "claude", ""))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	var table *portal.ProductUsageTable
	for i := range page.Usage.Breakdowns {
		if page.Usage.Breakdowns[i].Title == "Conversation usage by provider and served model" {
			table = &page.Usage.Breakdowns[i]
		}
	}
	if table == nil {
		t.Fatal("no conversation table")
	}
	cells := map[string]map[string]string{}
	for _, r := range table.Rows {
		cells[r.Name] = map[string]string{}
		for _, c := range r.Cells {
			cells[r.Name][c.Label] = c.Value
		}
	}
	// Providers never share a row, so Codex's input (which includes cached tokens)
	// is never added to Claude's; the two Claude opus sessions are summed, with
	// their stale and Partial counts, and an Unknown counter is not a zero.
	if len(cells) != 3 {
		t.Fatalf("%v", cells)
	}
	opus := cells["claude · claude-opus-5-5"]
	if opus["Sessions"] != "2" || opus["Input"] != "300" || opus["Output"] != "0" || opus["Cache read"] != "100" || opus["Cache create"] != "Unknown" || opus["Stale"] != "1" || opus["Partial"] != "1" {
		t.Fatalf("%v", opus)
	}
	if c := cells["codex · gpt-6-astra"]; c["Sessions"] != "1" || c["Input"] != "2,500" || c["Cache read"] != "900" {
		t.Fatalf("%v", c)
	}
	if c := cells["claude · claude-haiku-4-5"]; c["Input"] != "7" {
		t.Fatalf("%v", c)
	}
	// The headless table and stats do not count them.
	for _, st := range page.Usage.Stats {
		if st.Label == "Measured sessions" && !strings.HasPrefix(st.Value, "0 of") {
			t.Fatalf("a conversation figure was counted as an initial invocation: %+v", st)
		}
	}
	// With none reporting, there is no table and the original wording stands.
	none := sessionsPage(convDecode(t, convRow("a", "remote", "claude", "")).Sessions, true, "observed", convNow, portal.ProductFilter{})
	for _, b := range none.Usage.Breakdowns {
		if strings.HasPrefix(b.Title, "Conversation usage") {
			t.Fatal("a conversation table without a figure")
		}
	}
	if none.Usage.Scope != "Initial invocations only" {
		t.Fatalf("%q", none.Usage.Scope)
	}
}

// The existing template renders the panel and the table as they are; their text
// is a placeholder for the interface review. Everything is escaped.
func TestConversationUsageRendersEscapedInTheExistingTemplate(t *testing.T) {
	figure := convPartial(convFigure("claude-opus-5-5", 1234, "2026-10-25T11:55:00Z"), "skipped_records")
	d := convDecode(t, convRow("rendered", "remote", "claude", figure))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"Conversation usage", "Input 1,234", "Cache create Unknown", "Partial: some records could not be read", "Conversation usage by provider and served model", "claude · claude-opus-5-5"} {
		if !strings.Contains(s, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// A reason with markup cannot reach the page: the decoder refuses it, and the
	// view escapes whatever it shows.
	page.Groups[0].Rows = nil
	page.Usage.Breakdowns[len(page.Usage.Breakdowns)-1].Rows = []portal.ProductUsageRow{{Name: `<img src=x onerror=alert(1)>`}}
	if body, err = portal.RenderProduct(page); err != nil || strings.Contains(string(body), "<img") {
		t.Fatalf("not escaped: %v", err)
	}
}
