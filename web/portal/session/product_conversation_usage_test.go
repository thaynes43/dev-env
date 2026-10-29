package session

import (
	"os"
	"regexp"
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

// convPanel is one session's Conversation usage panel, collected for checks:
// its state lines by badge word, the badge tones, its fields by label, each
// served model's cells by label, and its note. found is false without a panel.
type convPanel struct {
	found         bool
	states, tones map[string]string
	fields        map[string]string
	models        map[string]map[string]portal.ProductUsageCell
	note          string
}

func convSection(t *testing.T, page portal.ProductPage, name string) convPanel {
	t.Helper()
	out := convPanel{states: map[string]string{}, tones: map[string]string{}, fields: map[string]string{}, models: map[string]map[string]portal.ProductUsageCell{}}
	for _, s := range findRow(t, page, name).Sections {
		if s.Title != "Conversation usage" {
			continue
		}
		out.found, out.note = true, s.Note
		for _, st := range s.States {
			out.states[st.Status.Text], out.tones[st.Status.Text] = st.Text, st.Status.Tone
		}
		for _, f := range s.Fields {
			out.fields[f.Label] = f.Value
		}
		if s.Table != nil {
			for _, r := range s.Table.Rows {
				out.models[r.Name] = map[string]portal.ProductUsageCell{}
				for _, c := range r.Cells {
					out.models[r.Name][c.Label] = c
				}
			}
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
		convRow("odd", "remote", "claude", convPartial(final, "count_overflow")), convRow("none", "remote", "claude", ""),
		convRow("codex", "local", "codex", convFigure("gpt-6-astra", 10, "2026-10-25T11:55:00Z")))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	f := convSection(t, page, "final")
	if f.states["Final"] != "Every complete usage record was counted, as of the measured time." || f.tones["Final"] != "muted" ||
		f.states["Current"] != "Measured within the last 15 minutes." || f.tones["Current"] != "muted" || len(f.states) != 2 {
		t.Fatalf("%v %v", f.states, f.tones)
	}
	if f.fields["Measured"] != "5m ago · 25 Oct 2026, 11:55 UTC" || f.fields["Provider calls counted"] != "3" || f.fields["Records skipped"] != "0" {
		t.Fatalf("%v", f.fields)
	}
	// An explicit zero reads 0 and an unmeasured counter reads Unknown, never 0.
	m := f.models["claude-opus-5-5"]
	if len(f.models) != 1 || m["Input"].Value != "10" || m["Input"].Unknown || m["Output"].Value != "0" || m["Output"].Unknown ||
		m["Cache read"].Value != "50" || m["Cache create"].Value != "Unknown" || !m["Cache create"].Unknown {
		t.Fatalf("%v", f.models)
	}
	// Token counts only: no price, no allowance, and the counters stand alone.
	if !strings.HasPrefix(f.note, "Provider-reported token counts for this session's registered conversation") || !strings.Contains(f.note, "none is added to another") ||
		!strings.Contains(f.note, "Not a cost or a charge.") || strings.Contains(f.note, "USD") || strings.Contains(f.note, "allowance") || strings.Contains(f.note, "Codex") {
		t.Fatalf("%q", f.note)
	}
	if c := convSection(t, page, "codex"); !strings.Contains(c.note, "Codex input already includes cache reads, so the two are never added.") || strings.Contains(c.note, "none is added") {
		t.Fatalf("%q", c.note)
	}
	p := convSection(t, page, "partial")
	if p.states["Partial"] != "Some records could not be read or counted. The counts shown are a lower bound." || p.tones["Partial"] != "wait" || p.states["Final"] != "" {
		t.Fatalf("%v", p.states)
	}
	// A code agentd sends only as Unknown still has its own sentence on a Partial.
	if o := convSection(t, page, "odd"); o.states["Partial"] != "A running total passed the largest count a figure holds. The counts shown are a lower bound." {
		t.Fatalf("%v", o.states)
	}
	// A code with no wording (the decoder admits none today) shows as itself.
	fallback := *d.Sessions[0].Activity.ConversationUsage
	fallback.ResultState, fallback.ReasonCode = protocol.ConversationPartial, "later_code"
	if got := conversationStates(&fallback, convNow)[0].Text; got != "Reason: later_code. The counts shown are a lower bound." {
		t.Fatalf("%q", got)
	}
	// A session with no key has no panel at all: absent is not a zero.
	if n := convSection(t, page, "none"); n.found || findRow(t, page, "none").Meta != "" {
		t.Fatalf("%+v", n)
	}
	if got := findRow(t, page, "final").Meta; got != "Conversation: In 10 · Out 0" {
		t.Fatalf("%q", got)
	}
	if got := findRow(t, page, "partial").Meta; got != "Conversation: In 10 · Out 0 · partial" {
		t.Fatalf("%q", got)
	}
}

func TestConversationUsageIsMarkedStaleAfterFifteenMinutes(t *testing.T) {
	at := func(age time.Duration) string { return convNow.Add(-age).Format(time.RFC3339) }
	d := convDecode(t,
		convRow("fresh", "remote", "claude", convFigure("claude-opus-5-5", 10, at(time.Minute))),
		convRow("edge", "remote", "claude", convFigure("claude-opus-5-5", 10, at(conversationUsageStaleAfter))),
		convRow("old", "remote", "claude", convFigure("claude-opus-5-5", 10, at(conversationUsageStaleAfter+time.Second))),
		convRow("ancient", "remote", "claude", convFigure("claude-opus-5-5", 10, at(26*time.Hour))))
	if conversationUsageStaleAfter != 15*time.Minute || staleMinutes != "15" {
		t.Fatalf("the threshold is %s", conversationUsageStaleAfter)
	}
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	for name, stale := range map[string]bool{"fresh": false, "edge": false, "old": true, "ancient": true} {
		f := convSection(t, page, name)
		if _, got := f.states["Stale"]; got != stale {
			t.Errorf("%s: states %v", name, f.states)
		}
		if stale && (f.states["Stale"] != "Older than 15 minutes, so it may be behind the conversation." || f.tones["Stale"] != "wait" || f.states["Current"] != "") {
			t.Errorf("%s: %v %v", name, f.states, f.tones)
		}
		// A stale figure is still shown, with the time it was measured.
		if f.models["claude-opus-5-5"]["Input"].Value != "10" || !strings.HasSuffix(f.fields["Measured"], " UTC") {
			t.Errorf("%s: figure hidden: %+v", name, f)
		}
		if got := strings.HasSuffix(findRow(t, page, name).Meta, " · stale"); got != stale {
			t.Errorf("%s: meta %q", name, findRow(t, page, name).Meta)
		}
	}
	if got := convSection(t, page, "ancient").fields["Measured"]; got != "26h ago · 24 Oct 2026, 10:00 UTC" {
		t.Fatalf("%q", got)
	}
	// The same figure read later is stale: the clock decides, not the figure.
	later := sessionsPage(d.Sessions, true, "observed", convNow.Add(20*time.Minute), portal.ProductFilter{})
	if _, ok := convSection(t, later, "fresh").states["Stale"]; !ok {
		t.Fatal("not stale 20 minutes later")
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
	u := page.Usage
	// The conversation table comes first, apart from the initial-invocation
	// tables, which each name their scope.
	table := u.Breakdowns[0]
	if table.Title != "Conversation usage by provider and served model" || strings.Join(table.Columns, "|") != "Provider · model|Sessions|Input|Output|Cache read|Cache create|Partial|Stale" {
		t.Fatalf("%+v", table)
	}
	var titles []string
	for _, b := range u.Breakdowns[1:] {
		titles = append(titles, b.Title)
	}
	if strings.Join(titles, "|") != "Initial invocations by how sessions ran|Initial invocations by provider and requested model|Initial invocations by project" || u.RowsTitle != "Initial invocations by session" {
		t.Fatalf("%v %q", titles, u.RowsTitle)
	}
	cells, subs := map[string]map[string]string{}, map[string]string{}
	for _, r := range table.Rows {
		cells[r.Name], subs[r.Name] = map[string]string{}, r.Sub
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
	if subs["claude · claude-opus-5-5"] != "Lower bound · May be behind" || subs["codex · gpt-6-astra"] != "" {
		t.Fatalf("%v", subs)
	}
	if c := cells["codex · gpt-6-astra"]; c["Sessions"] != "1" || c["Input"] != "2,500" || c["Cache read"] != "900" {
		t.Fatalf("%v", c)
	}
	if c := cells["claude · claude-haiku-4-5"]; c["Input"] != "7" {
		t.Fatalf("%v", c)
	}
	// No price on a conversation figure, and no allowance implied.
	if strings.Contains(strings.Join(table.Columns, " "), "cost") || !strings.Contains(table.Note, "Codex input already includes cache reads") || strings.Contains(table.Note, "USD") {
		t.Fatalf("%+v", table)
	}
	if u.Allowance[0].Value != "Not connected" || u.Allowance[1].Value != "Not connected" {
		t.Fatalf("%+v", u.Allowance)
	}
	coverage := map[string]string{}
	for _, f := range u.Coverage {
		coverage[f.Label] = f.Value
	}
	if !strings.HasSuffix(coverage["Cost"], "Conversations: no price is shown") || !strings.Contains(coverage["Cost"], "CLI-equivalent list-price estimate, not a charge") ||
		!strings.Contains(coverage["Conversations"], "Older than 15 minutes is marked stale and may be behind") || !strings.Contains(coverage["Totals"], "Claude and Codex are never added together") ||
		!strings.Contains(coverage["Not measured"], "interactive sessions that report no conversation figure") {
		t.Fatalf("%v", coverage)
	}
	if u.Scope != "Initial invocations and conversations" || !strings.HasSuffix(u.Stats[1].Note, ". 4 with conversation usage") || u.Stats[0].Note != "Headless tasks' first invocation only" {
		t.Fatalf("%q %+v", u.Scope, u.Stats)
	}
	// The headless table and stats do not count them.
	for _, st := range u.Stats {
		if st.Label == "Measured sessions" && !strings.HasPrefix(st.Value, "0 of") {
			t.Fatalf("a conversation figure was counted as an initial invocation: %+v", st)
		}
	}
	if partial := sessionsPage(d.Sessions, false, "observed", convNow, portal.ProductFilter{}); !strings.HasSuffix(partial.Usage.Stats[1].Note, ". 4 listed with conversation usage") {
		t.Fatalf("%q", partial.Usage.Stats[1].Note)
	}
	// With none reporting, there is no table and the original wording stands.
	none := sessionsPage(convDecode(t, convRow("a", "remote", "claude", "")).Sessions, true, "observed", convNow, portal.ProductFilter{})
	for _, b := range none.Usage.Breakdowns {
		if strings.HasPrefix(b.Title, "Conversation usage") || strings.HasPrefix(b.Title, "Initial invocations") {
			t.Fatalf("conversation wording without a figure: %q", b.Title)
		}
	}
	if none.Usage.Scope != "Initial invocations only" || none.Usage.RowsTitle != "" || none.Usage.Breakdowns[0].Title != "By how sessions ran" ||
		none.Usage.Stats[1].Note != "Remote or local; a person drives them. Not measured" {
		t.Fatalf("%+v", none.Usage)
	}
}

// The panel and the table render through the shared components, escaped, with
// the badge tones held to the fixed set and no inline style or script.
func TestConversationUsageRendersEscapedInTheTemplate(t *testing.T) {
	figure := convPartial(convFigure("claude-opus-5-5", 1234, "2026-10-25T11:55:00Z"), "skipped_records")
	d := convDecode(t, convRow("rendered", "remote", "claude", figure))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	body, err := portal.RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{"<h3>Conversation usage</h3>", `<ul class="panel-states"><li><span class="badge tone-wait">Partial</span><span>Some records could not be read or counted. The counts shown are a lower bound.</span></li>`,
		`<span class="badge tone-muted">Current</span>`, `<caption class="sr-only">Token counts by served model</caption>`, `<td>claude-opus-5-5</td><td data-label="Input">1,234</td>`,
		`<td data-label="Cache create" class="unknown">Unknown</td>`, "claude · claude-opus-5-5"} {
		if !strings.Contains(s, want) {
			t.Errorf("the Sessions view lacks %q", want)
		}
	}
	// The by-provider table and the by-session table are the Usage view's.
	usage := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{View: "usage"})
	ubody, err := portal.RenderProduct(usage)
	if err != nil {
		t.Fatal(err)
	}
	u := string(ubody)
	for _, want := range []string{"Conversation usage by provider and served model", "claude · claude-opus-5-5",
		`<h3 id="usage-rows">Initial invocations by session</h3>`, "Recorded token usage is not account consumption."} {
		if !strings.Contains(u, want) {
			t.Errorf("the Usage view lacks %q", want)
		}
		if want != "claude · claude-opus-5-5" && strings.Contains(s, want) {
			t.Errorf("the Sessions view embeds %q", want)
		}
	}
	for _, rendered := range []string{s, u} {
		if strings.Contains(rendered, "<script") || regexp.MustCompile(`\sstyle="`).MatchString(rendered) || regexp.MustCompile(`\son[a-z]+=`).MatchString(rendered) {
			t.Fatal("inline script, style or handler")
		}
	}
	// Whatever reaches the panel and the table is escaped, and a tone outside
	// the set renders muted.
	var row *portal.ProductRow
	for g := range page.Groups {
		for r := range page.Groups[g].Rows {
			if page.Groups[g].Rows[r].Title == "rendered" {
				row = &page.Groups[g].Rows[r]
			}
		}
	}
	if row == nil || row.Sections[0].Title != "Conversation usage" {
		t.Fatalf("%+v", row)
	}
	row.Sections[0].States[0].Status.Tone = `wait" onclick="x`
	row.Sections[0].States[0].Text = `<img src=x onerror=alert(1)>`
	row.Sections[0].Table.Rows[0].Name = `<script>x</script>`
	if body, err = portal.RenderProduct(page); err != nil || strings.Contains(string(body), "<img") || strings.Contains(string(body), "<script>x") ||
		strings.Contains(string(body), "onclick") || !strings.Contains(string(body), `<span class="badge tone-muted">Partial</span>`) {
		t.Fatalf("not escaped: %v", err)
	}
	// What reaches the Usage view's tables is escaped too.
	usage.Usage.Breakdowns[0].Rows = []portal.ProductUsageRow{{Name: `<img src=x onerror=alert(1)>`}}
	if body, err = portal.RenderProduct(usage); err != nil || strings.Contains(string(body), "<img") || strings.Contains(string(body), "onerror=alert(1)>") ||
		!strings.Contains(string(body), "&lt;img src=x onerror=alert(1)&gt;") {
		t.Fatalf("Usage view not escaped: %v", err)
	}
	// The renderer copied what it sanitized: the page model is unchanged.
	if row.Sections[0].States[0].Status.Tone != `wait" onclick="x` {
		t.Fatal("the renderer changed the caller's page")
	}
}

// Every reason code the protocol's Valid() accepts on a Partial result has a
// plain sentence. The candidates are every Reason constant in the protocol's
// conversation-usage source, so a code added there without wording here fails.
func TestEveryPartialReasonValidAcceptsHasWording(t *testing.T) {
	src, err := os.ReadFile("../../../internal/agentd/protocol/conversation_usage.go")
	if err != nil {
		t.Fatal(err)
	}
	codes := regexp.MustCompile(`(?m)^\s*Reason[A-Za-z]+\s*=\s*"([a-z_]+)"`).FindAllSubmatch(src, -1)
	accepted := 0
	for _, m := range codes {
		code := string(m[1])
		partial := protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: protocol.AgentClaude, ObservedAt: convNow,
			Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: protocol.ConversationPartial, ReasonCode: code,
			Models: []protocol.ConversationModelUsage{{Model: "claude-opus-5-5", InputTokens: protocol.TokenObservation{State: protocol.UsageUnknown},
				OutputTokens: protocol.TokenObservation{State: protocol.UsageUnknown}, CacheReadInputTokens: protocol.TokenObservation{State: protocol.UsageUnknown},
				CacheCreationInputTokens: protocol.TokenObservation{State: protocol.UsageUnknown}}}}
		if !partial.Valid() {
			continue
		}
		accepted++
		words := conversationReasons[code]
		if words == "" || !strings.HasSuffix(words, ".") || strings.Contains(words, "_") || strings.Contains(words, "lower bound") || words[0] < 'A' || words[0] > 'Z' {
			t.Errorf("%s: %q", code, words)
		}
	}
	// All fourteen codes are accepted on a Partial today; fewer means the pattern
	// above stopped finding them.
	if accepted != 14 || len(conversationReasons) != accepted {
		t.Fatalf("accepted %d codes, worded %d", accepted, len(conversationReasons))
	}
}

// A Partial says its counts are a lower bound only when it shows a count: a
// model split, whose counters are all Unknown, does not.
func TestPartialLowerBoundOnlyWithAReportedCount(t *testing.T) {
	unknown := `{"state":"Unknown"}`
	split := `{"resultState":"Partial","reasonCode":"model_split_unknown","models":[` +
		`{"model":"gpt-6-astra","inputTokens":` + unknown + `,"outputTokens":` + unknown + `,"cacheReadInputTokens":` + unknown + `,"cacheCreationInputTokens":` + unknown + `},` +
		`{"model":"gpt-6-luna","inputTokens":` + unknown + `,"outputTokens":` + unknown + `,"cacheReadInputTokens":` + unknown + `,"cacheCreationInputTokens":` + unknown + `}],` +
		`"turns":4,"skipped":0,"observedAt":"2026-10-25T11:55:00Z"}`
	d := convDecode(t, convRow("split", "remote", "codex", split),
		convRow("reset", "remote", "codex", convPartial(convFigure("gpt-6-astra", 10, "2026-10-25T11:55:00Z"), "counter_reset")))
	page := sessionsPage(d.Sessions, true, "observed", convNow, portal.ProductFilter{})
	if got := convSection(t, page, "split").states["Partial"]; got != "Several models share one provider total, so each model's counts are Unknown." {
		t.Fatalf("%q", got)
	}
	if got := convSection(t, page, "reset").states["Partial"]; got != "The provider's counters restarted, so only the latest run is counted. The counts shown are a lower bound." {
		t.Fatalf("%q", got)
	}
}
