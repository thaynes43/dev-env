package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// html/template drops CSS comments from template text, so the policy must hash
// what is actually rendered, not the file. This pins the two together.
func TestProductPolicyHashesTheRenderedStylesheet(t *testing.T) {
	body, err := RenderProduct(ProductPage{Title: "Sessions", Notice: "Sessions not connected"})
	if err != nil {
		t.Fatal(err)
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindSubmatch(body)
	if style == nil {
		t.Fatal("no stylesheet rendered")
	}
	digest := sha256.Sum256(style[1])
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
	if !strings.Contains(ProductPolicy, "style-src "+hash+";") {
		t.Fatalf("policy %q does not allow the rendered stylesheet %s", ProductPolicy, hash)
	}
	for _, want := range []string{"default-src 'none'", "base-uri 'none'", "form-action 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(ProductPolicy, want) {
			t.Errorf("product policy lacks %q", want)
		}
	}
	for _, unsafe := range []string{"unsafe-inline", "unsafe-eval", "script-src", "unsafe-hashes"} {
		if strings.Contains(ProductPolicy, unsafe) {
			t.Errorf("product policy contains %q", unsafe)
		}
	}
	if strings.Contains(string(body), "<script") || regexp.MustCompile(`\sstyle="`).Match(body) || regexp.MustCompile(`\son[a-z]+=`).Match(body) {
		t.Fatal("product page carries script, an inline style attribute or an event handler")
	}
}

func TestRenderProductKeepsAnchorsTonesAndCountsSafe(t *testing.T) {
	page := ProductPage{Title: "Sessions", Notice: "Connected session summaries.", Connected: true, Complete: true, Quiet: true, ObservedAt: "2026-10-26T15:00:00Z",
		Groups: []ProductGroup{{Title: "Needs attention", Attention: true, Rows: []ProductRow{
			{ID: "s1", Title: "kept-row", Status: ProductStatus{Text: "Waiting", Tone: "wait"}},
			{ID: `s2" onclick="x`, Title: "dropped-row", Status: ProductStatus{Text: "Busy", Tone: "busy"}},
			{ID: "s3", Title: "toned-row", Status: ProductStatus{Text: "Odd", Tone: "evil"}},
		}}},
		Rail:   ProductRail{All: "7", Attention: "2", Projects: []ProductCount{{Name: "dev-env", Count: "5"}, {Name: "haynes-ops", Count: "2"}}},
		Filter: ProductFilter{Project: "haynes-ops"}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if strings.Contains(html, "dropped-row") || strings.Contains(html, "onclick") || strings.Contains(html, "tone-evil") || !strings.Contains(html, `class="badge tone-muted">Odd`) {
		t.Fatalf("unsafe id or tone rendered: %s", html)
	}
	if !strings.Contains(html, `href="#s1"`) || !strings.Contains(html, `id="s1"`) || !strings.Contains(html, `class="detail default" id="s1"`) || !strings.Contains(html, `<span class="attention-count">2</span>`) {
		t.Fatalf("row anchors or counts missing: %s", html)
	}
	// The rail filters by query and counts the whole read; the selected project is current.
	for _, want := range []string{
		`href="/portal/projects/dev-env/environments/internal/sessions">`,
		`href="/portal/projects/dev-env/environments/internal/sessions?show=attention">`,
		`href="/portal/projects/dev-env/environments/internal/sessions?project=dev-env">`,
		`href="/portal/projects/dev-env/environments/internal/sessions?project=haynes-ops" aria-current="page">`,
		`haynes-ops<span class="count">2</span>`, `All sessions<span class="count">7</span>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rail lacks %s", want)
		}
	}
	if strings.Contains(html, `class="rail-link" href="/portal/projects/dev-env/environments/internal/tasks"`) || strings.Contains(html, `class="rail-link" href="/portal/projects/dev-env/environments/internal/sessions#usage"`) {
		t.Error("the rail repeats the top navigation")
	}
	if strings.Contains(html, "Fixture preview") || !strings.Contains(html, `<span role="status">Connected session summaries.</span>`) || strings.Contains(html, `class="banner"`) {
		t.Fatalf("notice placement or preview label wrong: %s", html)
	}
	page.Complete, page.Preview, page.Quiet, page.Notice = false, true, false, "Sessions read is incomplete. This is not the full inventory."
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html = string(body)
	if strings.Contains(html, `<span class="attention-count">`) || strings.Contains(html, `<span class="count">`) || !strings.Contains(html, "Fixture preview") || !strings.Contains(html, `<p class="banner" role="status">Sessions read is incomplete.`) {
		t.Fatalf("incomplete read was counted or preview/banner missing: %s", html)
	}
	// Unbuilt features and an unconnected allowance source are not shown.
	for _, stub := range []string{"Automations", "Watchdog", "Planned", `id="planned"`, "Provider allowance", "allowance-bar"} {
		if strings.Contains(html, stub) {
			t.Errorf("rendered page shows the stub %q", stub)
		}
	}
}

func sendRow(id string, send *ProductSend, draftable bool) ProductRow {
	return ProductRow{ID: id, Title: "agent-" + id, Status: ProductStatus{Text: "Idle", Tone: "muted"},
		Messages: &ProductMessages{Agent: "claude · claude-opus-5-5", History: "history", Gate: "send gate", Route: ProductStatus{Text: "CLI route", Tone: "info"},
			Session: "agent-" + id, SessionUID: "uid-" + id, Draftable: draftable, Send: send}}
}

func TestSendFormRendersOnlyWhenValid(t *testing.T) {
	csrf := strings.Repeat("c", 43)
	good := &ProductSend{Action: ProductSessionMessagesHref, CSRF: csrf, RequestID: "request-0000000001"}
	page := func(rows ...ProductRow) ProductPage {
		return ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "In progress", Rows: rows}}}
	}
	none := page(sendRow("s1", nil, true), sendRow("s2", good, false),
		sendRow("s3", &ProductSend{Action: "https://evil.example/send", CSRF: csrf, RequestID: "request-0000000003"}, true),
		sendRow("s4", &ProductSend{Action: ProductSessionMessagesHref, CSRF: "short", RequestID: "request-0000000004"}, true),
		sendRow("s5", &ProductSend{Action: ProductSessionMessagesHref, CSRF: csrf, RequestID: "bad id"}, true))
	if ProductPolicyFor(none) != ProductPolicy {
		t.Fatal("a page without a valid form allows form posts")
	}
	body, err := RenderProduct(none)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if strings.Contains(html, "<form") || strings.Contains(html, "evil.example") || strings.Contains(html, `name="text"`) || strings.Contains(html, "form-action 'self'") || strings.Count(html, sendUnavailable) != 4 || !strings.Contains(html, ">Read-only</span>") {
		t.Fatalf("invalid send forms rendered: %s", html)
	}

	mixed := page(sendRow("s1", good, true), sendRow("s2", nil, true))
	if ProductPolicyFor(mixed) != ProductSendPolicy || !strings.Contains(ProductSendPolicy, "form-action 'self'") || strings.Contains(ProductSendPolicy, "form-action 'none'") || strings.Contains(ProductSendPolicy, "script-src") {
		t.Fatalf("send policy %q", ProductSendPolicy)
	}
	body, err = RenderProduct(mixed)
	if err != nil {
		t.Fatal(err)
	}
	html = string(body)
	for _, want := range []string{
		`base-uri &#39;none&#39;; form-action &#39;self&#39;">`,
		`<form class="composer" method="post" action="` + ProductSessionMessagesHref + `">`,
		`<input type="hidden" name="csrf" value="` + csrf + `">`,
		`<input type="hidden" name="requestID" value="request-0000000001">`,
		`<input type="hidden" name="session" value="agent-s1">`,
		`<input type="hidden" name="sessionUID" value="uid-s1">`,
		`<textarea id="s1-draft" name="text" required maxlength="16384" rows="3"`,
		`<button class="btn primary send" type="submit" aria-describedby="s1-gate">Send</button></div></form>`,
		`<button class="btn send" type="button" disabled aria-describedby="s2-gate">Send</button></div></div>`,
		">Read and message</span>", "Internal · read and message",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("send page lacks %q: %s", want, html)
		}
	}
	if strings.Count(html, "<form") != 1 || strings.Count(html, `name="csrf"`) != 1 {
		t.Fatal("a form rendered without its own valid send")
	}
}

func TestReceiptKeepsTransportApartFromReplies(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 5, 0, time.UTC)
	for status, want := range map[string]string{"Delivered": "Delivered to the agent", "Refused": "Not delivered", "Pending": "Not confirmed yet", "Unknown": "Outcome Unknown", "Completed": "Outcome Unknown", "": "Outcome Unknown"} {
		body, err := RenderReceipt(ProductReceipt{RequestID: "request-0000000001", Session: "agent-a", SessionUID: "uid-a", ResolvedPodUID: "pod-a", TransportStatus: status, ReasonCode: "refused", ObservedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		html := string(body)
		if !strings.Contains(html, "<h1>"+strings.ReplaceAll(want, "'", "&#39;")) || strings.Contains(html, "<form") || strings.Contains(html, "<script") || !strings.Contains(html, "form-action &#39;none&#39;") {
			t.Fatalf("%q receipt: %s", status, html)
		}
		if !strings.Contains(html, `href="`+ProductSessionMessagesHref+`/request-0000000001">Check again</a>`) || !strings.Contains(html, "12:00:05 UTC") || !strings.Contains(html, `href="`+productSessionsHref+`?session=uid-a">Back to the session</a>`) || !strings.Contains(html, "The session took no message") {
			t.Fatalf("%q receipt lacks its link or time", status)
		}
	}
	body, err := RenderReceipt(ProductReceipt{TransportStatus: "Delivered", RequestID: "../../evil", SessionUID: "../x", ReasonCode: `<img src=x onerror=steal()>`})
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "does not mean the agent has read it, answered it or acted on it") || strings.Contains(html, "Check again") || strings.Contains(html, "img") || !strings.Contains(html, "Reason not recorded.") || !strings.Contains(html, "<dt>Observed</dt><dd>Unknown</dd>") || !strings.Contains(html, `href="`+productSessionsHref+`">Back to the session</a>`) {
		t.Fatalf("delivered receipt: %s", html)
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindSubmatch(body)
	digest := sha256.Sum256(style[1])
	if !strings.Contains(ProductPolicy, "'sha256-"+base64.StdEncoding.EncodeToString(digest[:])+"'") {
		t.Fatal("receipt stylesheet is not the product policy's")
	}
}

func TestConversationRendersBoundedEscapedTurns(t *testing.T) {
	long := strings.Repeat("é", maxTurnBytes) // 2 bytes each: twice the bound
	turns := []ProductTurn{{Role: "user", At: "1m ago", Text: "line one\nline two <script>x</script>\u202e"}, {Role: "assistant", Text: long}, {Role: "message", From: "dev-env-1019-205125", Text: "relayed"}, {Role: "tool\" onclick=\"x", Text: "odd role"}}
	row := sendRow("s1", nil, true)
	row.Messages.Conversation = &ProductConversation{State: "Complete", Note: "every turn", Turns: turns}
	row.Messages.HistoryHref = "https://evil.example/"
	other := sendRow("s2", nil, true)
	other.Messages.HistoryHref = productSessionsHref + "?session=uid-s2#s2"
	body, err := RenderProduct(ProductPage{Title: "Sessions", Connected: true, Groups: []ProductGroup{{Title: "x", Rows: []ProductRow{row, other}}}})
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{
		`<span class="badge tone-wait">Truncated</span>`, // the long turn was cut
		`<li class="turn turn-user"><p class="turn-meta"><strong>User</strong><span>1m ago</span></p><p class="turn-text">line one` + "\n" + `line two &lt;script&gt;x&lt;/script&gt;</p></li>`,
		`<li class="turn turn-assistant"><p class="turn-meta"><strong>Agent</strong><span>Unknown</span><span>cut to fit</span></p>`,
		`<strong>Message from dev-env-1019-205125</strong>`,
		`<li class="turn turn-event"><p class="turn-meta"><strong>Event</strong>`,
		`<a href="` + productSessionsHref + `?session=uid-s2#s2">Load this session's conversation</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("conversation lacks %q", want)
		}
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "evil.example") || strings.Contains(html, "onclick") || strings.Contains(html, "\u202e") || strings.Count(html, "é") > maxTurnBytes/2 {
		t.Fatal("conversation rendered unsafe or unbounded text")
	}
	many := make([]ProductTurn, maxTurns+5)
	for i := range many {
		many[i] = ProductTurn{Role: "user", Text: "turn-" + strconv.Itoa(i)}
	}
	if c := boundConversation(ProductConversation{State: "Complete", Turns: many}); c.State != "Truncated" || len(c.Turns) != maxTurns || c.Turns[0].Text != "turn-5" {
		t.Fatalf("turn bound kept %d from %q, state %s", len(c.Turns), c.Turns[0].Text, c.State)
	}
	if c := boundConversation(ProductConversation{State: "Live", Note: "n", Turns: many}); c.State != "Unavailable" || len(c.Turns) != 0 {
		t.Fatal("an unknown state kept its turns")
	}
}

func TestTrimmingKeepsTheSelectedSessionAndItsPolicy(t *testing.T) {
	csrf := strings.Repeat("c", 43)
	selected := sendRow("s30", nil, true)
	selected.Selected = true
	selected.Messages.Conversation = &ProductConversation{State: "Complete", Turns: []ProductTurn{{Role: "assistant", Text: "selected-turn"}}}
	rows := []ProductRow{}
	for i := 1; i < 30; i++ {
		rows = append(rows, sendRow("s"+strconv.Itoa(i), &ProductSend{Action: ProductSessionMessagesHref, CSRF: csrf, RequestID: "request-00000000" + strconv.Itoa(10+i)}, true))
	}
	rows = append(rows, selected)
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "x", Rows: rows}}}
	alone, err := RenderProduct(ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "x", Rows: []ProductRow{selected}}}})
	if err != nil {
		t.Fatal(err)
	}
	body, policy, err := RenderProductPolicyWithin(page, len(alone)+600)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if !strings.Contains(html, "selected-turn") || strings.Contains(html, "<form") || policy != ProductPolicy || !strings.Contains(html, "form-action &#39;none&#39;") {
		t.Fatalf("trimmed page lost the selected session or kept a looser policy %q", policy)
	}
	if _, policy, err = RenderProductPolicyWithin(page, 1<<20); err != nil || policy != ProductSendPolicy {
		t.Fatalf("untrimmed page policy %q, %v", policy, err)
	}
	many := make([]ProductTurn, maxTurns)
	for i := range many {
		many[i] = ProductTurn{Role: "user", Text: strings.Repeat("<", maxTurnBytes)}
	}
	c := boundConversation(ProductConversation{State: "Complete", Turns: many})
	total := 0
	for _, turn := range c.Turns {
		total += len(turn.Text) + len(turn.From) + len(turn.At)
	}
	if c.State != "Truncated" || total > maxConversationBytes || len(c.Turns) == 0 {
		t.Fatalf("conversation budget kept %d bytes in %d turns, state %s", total, len(c.Turns), c.State)
	}
}

// The selected row is the default detail and the list's highlight wherever it
// sits, and it marks the shell so a phone opens that detail, not the list.
// Without a selection, or with one on a row that does not render, the first
// row is the default and a phone opens the list.
func TestSelectedRowIsTheDefaultDetailOnEveryViewport(t *testing.T) {
	page := func(ids []string, selected ...string) ProductPage {
		var rows []ProductRow
		for _, id := range ids {
			r := sendRow(id, nil, true)
			r.Selected = slices.Contains(selected, id)
			rows = append(rows, r)
		}
		return ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "Idle", Rows: rows[:1]}, {Title: "Later", Rows: rows[1:]}}}
	}
	ids := []string{"s1", "s2", "s3"}
	for _, tc := range []struct {
		name  string
		page  ProductPage
		want  string
		shell bool
	}{
		{"no selection", page(ids), "s1", false},
		{"third row", page(ids, "s3"), "s3", true},
		{"first of two", page(ids, "s2", "s3"), "s2", true},
		{"selected row dropped", page([]string{"s1", "s2", "bad"}, "bad"), "s1", false},
	} {
		body, err := RenderProduct(tc.page)
		if err != nil {
			t.Fatal(err)
		}
		html := string(body)
		if strings.Count(html, `<section class="detail default"`) != 1 || !strings.Contains(html, `<section class="detail default" id="`+tc.want+`"`) {
			t.Errorf("%s: default detail is not %s", tc.name, tc.want)
		}
		if strings.Count(html, `class="item default"`) != 1 || !strings.Contains(html, `<a class="item default" href="#`+tc.want+`">`) {
			t.Errorf("%s: list highlight is not %s", tc.name, tc.want)
		}
		if strings.Contains(html, `<div class="shell selected">`) != tc.shell {
			t.Errorf("%s: shell selected %v", tc.name, !tc.shell)
		}
	}
	// The stylesheet opens the default detail on a wide screen, and the
	// selected one on a phone until the reader follows a link in the page.
	for _, rule := range []string{
		".workspace:not(:has(.detail:target)) .detail.default, .detail:target { display: flex; }",
		".shell.selected:not(:has(:target)) .workspace { display: block; }",
		".shell.selected:not(:has(:target)) .list { display: none; }",
		".shell:not(:has(.detail:target)) .item.default {",
	} {
		if !strings.Contains(viewsStyle, rule) {
			t.Errorf("stylesheet lacks %s", rule)
		}
	}
}

func TestValidLaunchDropsWhatIsNotAFixedWellFormedForm(t *testing.T) {
	good := func() *ProductLaunch {
		return &ProductLaunch{Action: ProductSessionLaunchesHref, CSRF: strings.Repeat("c", 43), RequestID: strings.Repeat("r", 43), Repos: []string{"dev-env", "haynes-ops"}, Models: []string{"claude-opus-5-5"}, Efforts: []string{"low", "xhigh"}}
	}
	if !validLaunch(good()) {
		t.Fatal("a good form was refused")
	}
	for name, edit := range map[string]func(*ProductLaunch){
		"other action":      func(l *ProductLaunch) { l.Action = "https://evil.example/x" },
		"relative action":   func(l *ProductLaunch) { l.Action = ProductSessionMessagesHref },
		"short csrf":        func(l *ProductLaunch) { l.CSRF = "short" },
		"markup csrf":       func(l *ProductLaunch) { l.CSRF = strings.Repeat("<", 43) },
		"short request id":  func(l *ProductLaunch) { l.RequestID = "short" },
		"no repos":          func(l *ProductLaunch) { l.Repos = nil },
		"repo with a slash": func(l *ProductLaunch) { l.Repos = []string{"a/b"} },
		"repo dot dot":      func(l *ProductLaunch) { l.Repos = []string{".."} },
		"repo markup":       func(l *ProductLaunch) { l.Repos = []string{`"><script>`} },
		"duplicate repo":    func(l *ProductLaunch) { l.Repos = []string{"dev-env", "dev-env"} },
		"seventeen repos": func(l *ProductLaunch) {
			l.Repos = nil
			for i := 0; i < 17; i++ {
				l.Repos = append(l.Repos, fmt.Sprintf("repo%d", i))
			}
		},
		"no models":      func(l *ProductLaunch) { l.Models = nil },
		"alias model":    func(l *ProductLaunch) { l.Models = []string{"opus"} },
		"latest model":   func(l *ProductLaunch) { l.Models = []string{"claude-opus-latest"} },
		"a good and bad": func(l *ProductLaunch) { l.Models = []string{"claude-opus-5-5", "sonnet"} },
		"unknown effort": func(l *ProductLaunch) { l.Efforts = []string{"turbo"} },
	} {
		l := good()
		edit(l)
		if validLaunch(l) {
			t.Errorf("%s was accepted", name)
		}
	}
	if validLaunch(nil) {
		t.Error("nil accepted")
	}
}

func TestLaunchFormRendersOnlyWhenValidOnSessionsAndEscapes(t *testing.T) {
	launch := &ProductLaunch{Action: ProductSessionLaunchesHref, CSRF: strings.Repeat("c", 43), RequestID: strings.Repeat("r", 43), Repos: []string{"dev-env"}, Models: []string{"claude-opus-5-5"}, Efforts: []string{"high"}}
	render := func(title string, l *ProductLaunch) (string, string) {
		body, policy, err := RenderProductPolicyWithin(ProductPage{Title: title, Notice: "n", Launch: l}, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		return string(body), policy
	}
	body, policy := render("Sessions", launch)
	if policy != ProductSendPolicy || !strings.Contains(body, `<form class="launch" method="post" action="`+ProductSessionLaunchesHref+`">`) || !strings.Contains(body, `id="launch"`) || !strings.Contains(body, `href="#launch"`) {
		t.Fatalf("valid launch form not rendered with the send policy: %q", policy)
	}
	for _, name := range []string{`name="csrf"`, `name="requestID"`, `name="repo"`, `name="mode"`, `name="model"`, `name="effort"`, `name="size"`, `name="prompt"`} {
		if !strings.Contains(body, name) {
			t.Errorf("form lacks %s", name)
		}
	}
	for _, label := range []string{`for="launch-repo"`, `for="launch-mode"`, `for="launch-model"`, `for="launch-effort"`, `for="launch-size"`, `for="launch-prompt"`} {
		if !strings.Contains(body, label) {
			t.Errorf("form lacks label %s", label)
		}
	}
	// Tasks never carries it, and an invalid form is dropped with the read-only policy.
	if body, policy := render("Tasks", launch); policy != ProductPolicy || strings.Contains(body, "<form") {
		t.Error("the Tasks page rendered a launch form")
	}
	bad := *launch
	bad.Models = []string{"opus"}
	if body, policy := render("Sessions", &bad); policy != ProductPolicy || strings.Contains(body, "<form") || strings.Contains(body, `id="launch"`) {
		t.Error("an invalid launch form was rendered")
	}
	if body, policy := render("Sessions", nil); policy != ProductPolicy || strings.Contains(body, `id="launch"`) {
		t.Error("a page without a launch form changed")
	}
}

func TestRenderLaunchResultIsFixedWordingEscapedAndFormless(t *testing.T) {
	for outcome, want := range map[string]string{
		"limit": "Too many launches are running", "unavailable": "Launching is unavailable", "invalid": "The launch was not valid", "conflict": "This form already started a session",
		"unknown": "Outcome Unknown", "something else": "Outcome Unknown", "": "Outcome Unknown",
	} {
		body, err := RenderLaunchResult(ProductLaunchResult{Outcome: outcome, Back: productSessionsHref + "#launch"})
		if err != nil || !strings.Contains(string(body), want) || strings.Contains(string(body), "<form") || strings.Contains(string(body), `href="`+productSessionsHref+`#launch"`) == false {
			t.Fatalf("%q: %v", outcome, err)
		}
		if strings.Contains(string(body), "<script") || !strings.Contains(string(body), "form-action &#39;none&#39;") {
			t.Fatalf("%q: policy or script", outcome)
		}
	}
	body, err := RenderLaunchResult(ProductLaunchResult{Outcome: "invalid", Back: "https://evil.example/", Fields: []ProductField{{Label: "<b>model</b>", Value: `"><img src=x onerror=alert(1)>`}, {Label: "ok", Value: strings.Repeat("x", 600)}}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "<img") || strings.Contains(s, "<b>model") || strings.Contains(s, "evil.example") || strings.Contains(s, strings.Repeat("x", 600)) || !strings.Contains(s, "&lt;img") || !strings.Contains(s, `href="`+productSessionsHref+`"`) {
		t.Fatalf("not escaped or an unsafe back link survived: %s", s)
	}
}

// A panel's state lines and table render through the shared components: tones
// held to the fixed set, text escaped, a table caption for its name, Unknown
// cells dimmed, and the caller's page left as it was.
func TestPanelStatesAndTableRenderEscaped(t *testing.T) {
	section := ProductSection{Title: "Panel", States: []ProductState{{Status: ProductStatus{Text: "Stale", Tone: "wait"}, Text: "Older."}, {Status: ProductStatus{Text: "<b>x</b>", Tone: `ok" onclick="x`}, Text: "<i>y</i>"}},
		Table: &ProductUsageTable{Title: "Rows <caption>", Columns: []string{"Name", "Count"}, Rows: []ProductUsageRow{{Name: "<script>z</script>", Cells: []ProductUsageCell{{Label: "Count", Value: "Unknown", Unknown: true}}}}}}
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "In progress", Rows: []ProductRow{{ID: "s1", Title: "one", Sections: []ProductSection{section}}}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{`<ul class="panel-states"><li><span class="badge tone-wait">Stale</span><span>Older.</span></li><li><span class="badge tone-muted">&lt;b&gt;x&lt;/b&gt;</span><span>&lt;i&gt;y&lt;/i&gt;</span></li></ul>`,
		`<div class="table-wrap panel-table"><table><caption class="sr-only">Rows &lt;caption&gt;</caption>`, `<td>&lt;script&gt;z&lt;/script&gt;</td><td data-label="Count" class="unknown">Unknown</td>`} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page lacks %q", want)
		}
	}
	if strings.Contains(html, "onclick") || strings.Contains(html, "<script>") || page.Groups[0].Rows[0].Sections[0].States[1].Status.Tone != `ok" onclick="x` {
		t.Fatal("not escaped, or the caller's page changed")
	}
}

func TestUsageIsAnIndependentProductView(t *testing.T) {
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true,
		Groups: []ProductGroup{{Title: "Running", Rows: []ProductRow{{ID: "s1", Title: "agent-one"}}}},
		Usage:  &ProductUsage{Scope: "Measured usage", Rows: []ProductUsageRow{{ID: "s1", SessionUID: "uid-one", Name: "agent-one"}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `href="`+productSessionsHref+`?view=usage"`) {
		t.Fatal("Usage navigation has no durable view URL")
	}
	if strings.Contains(string(body), `id="usage"`) || strings.Contains(string(body), "Measured usage") {
		t.Fatal("Sessions carries an embedded Usage section")
	}
	page.Filter = ProductFilter{View: "usage"}
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, want := range []string{`<title>Usage · dev-env</title>`, `class="shell usage-view"`, `class="detail default" id="usage"`, `href="` + productSessionsHref + `?session=uid-one"`, `href="#main">Skip to content`, `<h1 id="usage-title">Usage</h1>`} {
		if !strings.Contains(html, want) {
			t.Errorf("Usage lacks %s", want)
		}
	}
	if strings.Count(html, "<h1") != 1 {
		t.Fatal("Usage must have exactly one h1")
	}
	if strings.Count(html, `?view=usage" aria-current="page"`) != 2 || strings.Contains(html, `class="rail"`) || strings.Contains(html, `class="list"`) || strings.Contains(html, `id="s1"`) || strings.Contains(html, `id="empty"`) || strings.Contains(html, "listed on the left") {
		t.Fatal("Usage includes session browser or wrong active navigation")
	}
}

func TestReadFailureHidesDataAndKeepsSafeDiagnostics(t *testing.T) {
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Groups: []ProductGroup{{Title: "current", Rows: []ProductRow{{ID: "s1", Title: "SECRET-CACHED-ROW"}}}}, Usage: &ProductUsage{Scope: "SECRET-CACHED-USAGE"}, ReadFailure: &ProductReadFailure{ReasonCode: `<script>SECRET-REASON</script>`, ObservedAt: "SECRET-TIME", LastSuccessAt: "SECRET-LAST", Page: 1000, RowIndex: 1000}}
	body, policy, err := RenderProductPolicyWithin(page, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if strings.Contains(html, "SECRET-") || strings.Contains(html, "Page 1000") || !strings.Contains(html, "reason is Unknown") || policy != ProductPolicy {
		t.Fatal("failure rendered unsafe diagnostics or cached data")
	}
	if !strings.Contains(html, `href="`+productSessionsHref+`">Retry</a>`) || !strings.Contains(html, "Last successful read in this process: Unknown") {
		t.Fatal("failure lacks safe retry or Unknown last success")
	}
}

func TestUsageSessionHrefIsCanonicalAndEscaped(t *testing.T) {
	uid := `uid &"<>#/?session=other`
	page := ProductPage{Title: "Sessions", Connected: true, Filter: ProductFilter{View: "usage"},
		Usage: &ProductUsage{Rows: []ProductUsageRow{{ID: "s1", SessionUID: uid, Name: `<agent>`}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	link := regexp.MustCompile(`<a href="([^"]+)">&lt;agent&gt;</a>`).FindSubmatch(body)
	if len(link) != 2 {
		t.Fatal("no escaped session link")
	}
	u, err := url.Parse(html.UnescapeString(string(link[1])))
	if err != nil || u.Path != productSessionsHref || u.Fragment != "" || len(u.Query()) != 1 || u.Query().Get("session") != uid || u.RawQuery != u.Query().Encode() {
		t.Fatalf("non-canonical or unsafe session href: %s, %v", link[1], err)
	}
}

func TestReadFailureRendersOnceAndKeepsLaunchForm(t *testing.T) {
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Notice: "Sessions unavailable",
		ReadFailure: &ProductReadFailure{ReasonCode: "operator_unreachable", TimedOut: true, RowIndex: -1},
		Launch:      &ProductLaunch{Action: ProductSessionLaunchesHref, CSRF: strings.Repeat("c", 43), RequestID: strings.Repeat("r", 43), Repos: []string{"dev-env"}, Models: []string{"claude-opus-5-5"}}}
	body, policy, err := RenderProductPolicyWithin(page, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if policy != ProductSendPolicy || !strings.Contains(s, `form-action &#39;self&#39;`) || strings.Count(s, `<form class="launch"`) != 1 || !strings.Contains(s, `href="#launch">New agent</a>`) {
		t.Fatal("read failure removed the independent launch form or its CSP")
	}
	if strings.Count(s, `class="read-failure"`) != 1 || strings.Count(s, ">Retry</a>") != 1 || !strings.Contains(s, `class="shell read-failed"`) {
		t.Fatal("failure is duplicated or missing its responsive state")
	}
	// The single failure lives in the workspace; keep that pane visible on phones.
	for _, rule := range []string{"@media (max-width: 700px)", ".shell.read-failed .workspace,", ".shell.read-failed .list,"} {
		if !strings.Contains(viewsStyle, rule) {
			t.Errorf("failure lacks responsive rule %s", rule)
		}
	}
	page.Filter = ProductFilter{View: "usage"}
	body, policy, err = RenderProductPolicyWithin(page, 1<<20)
	if err != nil || policy != ProductPolicy || strings.Contains(string(body), "<form") || strings.Count(string(body), "<h1") != 1 || !strings.Contains(string(body), `<h1 id="usage-title">Usage</h1>`) || strings.Count(string(body), ">Retry</a>") != 1 {
		t.Fatal("failed Usage has wrong heading, duplicated failure or a launch form")
	}
}

func TestEmptyUsageKeepsZeroStatsAndOnePlainEmptyState(t *testing.T) {
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"},
		Usage: &ProductUsage{Stats: []ProductStat{{Label: "Interactive", Value: "0"}, {Label: "Headless", Value: "0"}},
			Breakdowns: []ProductUsageTable{{Title: "Unused empty table"}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Count(s, "No sessions in this read.") != 1 || strings.Count(s, "<dd>0</dd>") != 2 || strings.Contains(s, "<table>") || strings.Contains(s, "Unused empty table") || strings.Count(s, "<h1") != 1 {
		t.Fatal("empty Usage lost its zeros or rendered empty tables")
	}
}
