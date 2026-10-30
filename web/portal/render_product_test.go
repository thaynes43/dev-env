package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"html/template"
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

func TestTabletShellTracksMatchVisibleCells(t *testing.T) {
	tablet := regexp.MustCompile(`(?s)@media \(max-width: 960px\) \{(.*?)\n\}`).FindStringSubmatch(viewsStyle)
	if tablet == nil {
		t.Fatal("missing 960px media block")
	}
	if !strings.Contains(tablet[1], ".rail { display: none; }") {
		t.Fatal("tablet rail is not hidden")
	}
	columns := regexp.MustCompile(`\.shell\s*\{\s*grid-template-columns:\s*([^;]+);`).FindStringSubmatch(tablet[1])
	if columns == nil {
		t.Fatal("missing tablet shell columns")
	}
	// Spaces inside minmax(0, 1fr) do not separate grid tracks.
	tracks := regexp.MustCompile(`\([^)]*\)`).ReplaceAllString(columns[1], "")
	// With the rail hidden, only .list and .workspace occupy the shell.
	if got := len(strings.Fields(tracks)); got != 2 {
		t.Fatalf("tablet shell has %d tracks for 2 visible cells (.list and .workspace): %s", got, columns[1])
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

func TestSessionsFooterRequiresSuccessfulReadTime(t *testing.T) {
	const renderedAt = "2026-10-30T08:12:00Z"
	const completedAt = "2026-10-30T08:10:00.123Z"
	const updated = `Updated <time datetime="2026-10-30T08:10:00Z">30 Oct 2026, 08:10 UTC</time>`
	for _, tc := range []struct {
		name, view, completed string
		failure               *ProductReadFailure
		incomplete            bool
		want                  bool
	}{
		{name: "success", completed: completedAt, want: true},
		{name: "nanoseconds and offset truncated to UTC seconds", completed: "2026-10-30T10:10:00.123456789+02:00", want: true},
		{name: "capped successful read", completed: completedAt, incomplete: true, want: true},
		{name: "usage success", view: "usage", completed: completedAt, want: true},
		{name: "usage unknown", view: "usage"},
		{name: "usage failure", view: "usage", completed: completedAt, failure: &ProductReadFailure{ReasonCode: "operator_unreachable"}},
		{name: "failed after success", completed: completedAt, failure: &ProductReadFailure{ReasonCode: "operator_unreachable", LastSuccessAt: completedAt}},
		{name: "unknown"},
		{name: "invalid", completed: "Unknown"},
		{name: "zero", completed: time.Time{}.Format(time.RFC3339Nano)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := ProductPage{Title: "Sessions", Connected: true, Complete: !tc.incomplete,
				ObservedAt: renderedAt, ReadCompletedAt: tc.completed, ReadFailure: tc.failure, Filter: ProductFilter{View: tc.view}}
			body, err := RenderProduct(page)
			if err != nil {
				t.Fatal(err)
			}
			footer := regexp.MustCompile(`<footer class="statusbar">(.*?)</footer>`).FindString(string(body))
			if footer == "" {
				t.Fatal("statusbar missing")
			}
			if strings.Contains(footer, "Updated") != tc.want {
				t.Fatalf("unexpected footer freshness: %s", footer)
			}
			if tc.want && !strings.Contains(footer, updated) {
				t.Fatalf("footer did not use read completion: %s", footer)
			}
			if strings.Contains(footer, renderedAt) {
				t.Fatalf("footer exposed render time as freshness: %s", footer)
			}
			listFoot := regexp.MustCompile(`<p class="list-foot">(.*?)</p>`).FindString(string(body))
			if tc.want && tc.view != "usage" {
				if listFoot != `<p class="list-foot">`+updated+`</p>` {
					t.Fatalf("list foot did not match footer completion: %s", listFoot)
				}
			} else if listFoot != "" {
				t.Fatalf("unexpected list foot: %s", listFoot)
			}
		})
	}
}

func TestTasksFooterRequiresSuccessfulReadTime(t *testing.T) {
	const renderedAt = "2026-10-30T08:12:00.123Z"
	const completedAt = "2026-10-30T08:10:00.123456789Z"
	const updated = `Updated <time datetime="2026-10-30T08:10:00Z">30 Oct 2026, 08:10 UTC</time>`
	for _, tc := range []struct {
		name, completed      string
		failure              *ProductReadFailure
		disconnected, capped bool
		want                 bool
	}{
		{name: "success", completed: completedAt, want: true},
		{name: "offset converted to UTC seconds", completed: "2026-10-30T10:10:00.123456789+02:00", want: true},
		{name: "capped successful read", completed: completedAt, capped: true, want: true},
		{name: "disconnected with completion time", completed: completedAt, disconnected: true},
		{name: "read failure", completed: completedAt, failure: &ProductReadFailure{ReasonCode: "operator_unreachable"}},
		{name: "unknown"},
		{name: "disconnected without completion time", disconnected: true},
		{name: "invalid", completed: "Unknown"},
		{name: "zero", completed: time.Time{}.Format(time.RFC3339Nano)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := RenderProduct(ProductPage{Title: "Tasks", Connected: !tc.disconnected, Complete: !tc.capped,
				ObservedAt: renderedAt, ReadCompletedAt: tc.completed, ReadFailure: tc.failure})
			if err != nil {
				t.Fatal(err)
			}
			footer := regexp.MustCompile(`<footer class="statusbar">(.*?)</footer>`).FindString(string(body))
			listFoot := regexp.MustCompile(`<p class="list-foot">(.*?)</p>`).FindString(string(body))
			if footer == "" || strings.Contains(footer, "Updated") != tc.want || strings.Contains(footer, renderedAt) {
				t.Fatalf("unexpected Tasks footer freshness: %s", footer)
			}
			if tc.want {
				if !strings.Contains(footer, updated) || listFoot != `<p class="list-foot">`+updated+`</p>` {
					t.Fatalf("Tasks freshness did not use read completion: %s %s", footer, listFoot)
				}
			} else if listFoot != "" {
				t.Fatalf("unexpected Tasks list foot: %s", listFoot)
			}
		})
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

func TestUsageSessionHrefValidatesUID(t *testing.T) {
	for _, uid := range []string{"a", "ABC-123", strings.Repeat("a", 64), "", strings.Repeat("a", 65), "uid_with_underscore", "uid/other", "uid\n", `uid &"<>#/?session=other`} {
		t.Run(uid, func(t *testing.T) {
			valid := regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`).MatchString(uid)
			row := ProductUsageRow{ID: "s1", SessionUID: uid, Name: `<agent>`}
			page := ProductPage{Title: "Sessions", Connected: true, Filter: ProductFilter{View: "usage"}, Usage: &ProductUsage{Rows: []ProductUsageRow{row}}}
			body, err := RenderProduct(page)
			if err != nil {
				t.Fatal(err)
			}
			cell := regexp.MustCompile(`(?s)<tr><td>(.*?)<small>`).FindSubmatch(body)
			if len(cell) != 2 {
				t.Fatal("missing Usage session cell")
			}
			link := regexp.MustCompile(`<a href="([^"]*)">&lt;agent&gt;</a>`).FindSubmatch(cell[1])
			if !valid {
				if row.SessionHref() != "" || strings.Contains(string(cell[1]), "<a") || string(cell[1]) != "&lt;agent&gt;" {
					t.Fatal("invalid UID was linked or name was lost")
				}
				return
			}
			if len(link) != 2 {
				t.Fatal("valid UID was not linked")
			}
			u, err := url.Parse(html.UnescapeString(string(link[1])))
			if err != nil || u.Path != productSessionsHref || u.Fragment != "" || len(u.Query()) != 1 || u.Query().Get("session") != uid || u.RawQuery != u.Query().Encode() {
				t.Fatal("non-canonical session link")
			}
		})
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
	if strings.Count(s, `class="failure-launch"`) != 1 || !strings.Contains(viewsStyle, ".failure-launch { display: none; }") || !strings.Contains(viewsStyle, "  .failure-launch { display: block; }") {
		t.Fatal("failure launch button is not limited to the mobile viewport")
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

func TestAttentionLinksAndConversationGap(t *testing.T) {
	link := "https://claude.ai/code/session_fixture-247"
	for _, tc := range []struct {
		name, header, answer, state string
		wantLink, wantGap           bool
	}{
		{"same link", link, link, "", true, true},
		{"different session", link, "https://claude.ai/code/session_other", "", false, true},
		{"non-Claude URL", "https://evil.example/", "https://evil.example/", "", false, true},
		{"unsafe URL", "javascript:alert(1)", "javascript:alert(1)", "", false, true},
		{"unavailable history", link, link, "Unavailable", true, false},
		{"unknown history", link, link, "unexpected", true, false},
		{"available history", link, link, "Complete", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := ProductRow{ID: "s1", Title: "synthetic", Link: tc.header, LinkLabel: "Open in Claude", Attention: &ProductAttention{Reason: `<script>reason</script>`, Link: tc.answer, Commands: []string{`agent-run show <name>`}}, Messages: &ProductMessages{History: "old gap", Gate: "send disabled"}}
			if tc.state != "" {
				row.Messages.Conversation = &ProductConversation{State: tc.state, Note: "old reason", Turns: []ProductTurn{{Role: "user", Text: "synthetic question"}}}
			}
			page := ProductPage{Title: "Sessions", Connected: true, Groups: []ProductGroup{{Title: "Needs attention", Rows: []ProductRow{row}}}}
			body, err := RenderProduct(page)
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if strings.Contains(text, `>Answer in Claude`) != tc.wantLink {
				t.Fatal("unsafe or mismatched primary link, or valid link removed")
			}
			wantHeader := strings.HasPrefix(tc.header, "https://") && !tc.wantLink
			if strings.Contains(text, `>Open in Claude<svg`) != wantHeader || (tc.wantLink && strings.Count(text, `href="`+link+`"`) != 1) {
				t.Fatal("header link duplicates Answer or is hidden without a primary link")
			}
			if (strings.Count(text, "The portal cannot show this session&#39;s conversation.") == 1) != tc.wantGap {
				t.Fatal("incorrect conversation availability wording")
			}
			if strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;reason&lt;/script&gt;") || !strings.Contains(text, "agent-run show &lt;name&gt;") {
				t.Fatal("attention content was not escaped")
			}
			if row.Attention.Link != tc.answer || row.Messages.History != "old gap" || (tc.state != "" && row.Messages.Conversation.Note != "old reason") {
				t.Fatal("renderer mutated its input")
			}
		})
	}
}

func TestAttentionPreservesLoadableHistory(t *testing.T) {
	href := productSessionsHref + "?session=uid-attention"
	row := ProductRow{ID: "s1", Title: "attention", Attention: &ProductAttention{Reason: "Waiting for your input"},
		Messages: &ProductMessages{History: "Load the conversation to read its turns.", HistoryHref: href}}
	body, err := RenderProduct(ProductPage{Title: "Sessions", Connected: true, Groups: []ProductGroup{{Title: "Needs attention", Rows: []ProductRow{row}}}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `href="`+href+`">Load this session's conversation</a>`) || !strings.Contains(text, row.Messages.History) || strings.Contains(text, "The portal cannot show") {
		t.Fatal("loadable history was replaced with an unavailable message")
	}
	if row.Messages.HistoryHref != href || row.Messages.History != "Load the conversation to read its turns." || row.Messages.Conversation != nil {
		t.Fatal("renderer mutated loadable history")
	}
}

func TestAttentionPreservesUnavailableConversationReason(t *testing.T) {
	for _, note := range []string{
		"The conversation read failed. No turns are shown rather than a guess.",
		"The conversation is unavailable (not_running).",
		"The conversation is unavailable (<reason_code>).",
	} {
		t.Run(note, func(t *testing.T) {
			conversation := &ProductConversation{State: "Unavailable", Note: note, Turns: []ProductTurn{{Role: "user", Text: "UNAVAILABLE-TURN"}}}
			row := ProductRow{ID: "s1", Title: "attention", Attention: &ProductAttention{Reason: "Waiting for your input"}, Messages: &ProductMessages{Conversation: conversation}}
			body, err := RenderProduct(ProductPage{Title: "Sessions", Connected: true, Groups: []ProductGroup{{Title: "Needs attention", Rows: []ProductRow{row}}}})
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			if strings.Count(text, html.EscapeString(note)) != 1 || strings.Contains(text, "The portal cannot show") || strings.Contains(text, "UNAVAILABLE-TURN") {
				t.Fatal("specific unavailable reason was lost or unavailable turns were shown")
			}
			if conversation.Note != note || len(conversation.Turns) != 1 {
				t.Fatal("renderer mutated unavailable conversation")
			}
		})
	}
}

// Tom's live feedback (2026-10-29): the Claude conversation links must open in
// a new tab and leave the portal in place. Both links point off the portal.
func TestClaudeConversationLinksOpenInNewTab(t *testing.T) {
	link := "https://claude.ai/code/session_fixture-247"
	render := func(row ProductRow) string {
		t.Helper()
		body, err := RenderProduct(ProductPage{Title: "Sessions", Connected: true, Groups: []ProductGroup{{Title: "Needs attention", Rows: []ProductRow{row}}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	anchor := func(text, label string) string {
		t.Helper()
		m := regexp.MustCompile(`<a [^>]*>`+regexp.QuoteMeta(label)+`<svg`).FindAllString(text, -1)
		if len(m) != 1 {
			t.Fatalf("want exactly one %q link, got %d", label, len(m))
		}
		return m[0]
	}
	check := func(a string) {
		t.Helper()
		rel := regexp.MustCompile(`rel="([^"]*)"`).FindStringSubmatch(a)
		if !strings.Contains(a, `href="`+link+`"`) || !strings.Contains(a, ` target="_blank"`) || rel == nil || !slices.Contains(strings.Fields(rel[1]), "noopener") || !slices.Contains(strings.Fields(rel[1]), "noreferrer") {
			t.Fatalf("link does not open the Claude conversation in a new tab safely: %s", a)
		}
	}
	// Answer in Claude, from a waiting row with an attention link.
	check(anchor(render(ProductRow{ID: "s1", Title: "waiting", Attention: &ProductAttention{Reason: "Waiting for your input", Link: link}, Link: link, LinkLabel: "Open in Claude"}), "Answer in Claude"))
	// Header Open in Claude, from a row with no attention link.
	check(anchor(render(ProductRow{ID: "s2", Title: "running", Link: link, LinkLabel: "Open in Claude"}), "Open in Claude"))
}

func TestReceiptAndLaunchResultCarryTheFullNavigation(t *testing.T) {
	receipt, err := RenderReceipt(ProductReceipt{RequestID: "request-0000000001", Session: "agent-a", SessionUID: "uid-a", ResolvedPodUID: "pod-a", TransportStatus: "Delivered", ObservedAt: time.Date(2026, 10, 30, 1, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	launch, err := RenderLaunchResult(ProductLaunchResult{Outcome: "limit", Back: productSessionsHref + "#launch"})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"receipt": string(receipt), "launch result": string(launch)} {
		for _, href := range []string{
			`href="/portal/projects/dev-env/environments/internal/sessions?view=usage"`,
			`href="/portal/projects/dev-env/environments/internal/asks"`,
			`href="/portal/projects/dev-env/environments/internal/pages"`,
		} {
			if strings.Count(body, href) != 2 {
				t.Errorf("%s: top and mobile navigation lack %s", name, href)
			}
		}
		if strings.Count(body, `href="/portal/projects/dev-env/environments/internal/sessions" aria-current="page"`) != 2 {
			t.Errorf("%s: Agents is not marked current in both navigations", name)
		}
	}
	for _, target := range []*template.Template{receiptTemplate, launchResultTemplate} {
		for _, name := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
			if viewsTemplate.Lookup(name).Tree == target.Lookup(name).Tree {
				t.Fatalf("%s shares a mutable parse tree with %s", name, target.Name())
			}
		}
	}
}

func TestTaskReadFailureHidesDataAndKeepsSafeDiagnostics(t *testing.T) {
	for _, attention := range []bool{false, true} {
		page := ProductPage{Title: "Tasks", Connected: true, Complete: true, ReadCompletedAt: "2026-10-30T12:00:00Z", Filter: ProductFilter{Attention: attention, Project: "SECRET-PROJECT", Session: "SECRET-SESSION", View: "usage"}, Groups: []ProductGroup{{Rows: []ProductRow{{ID: "t1", Title: "SECRET-CACHED-ROW"}}}}, ReadFailure: &ProductReadFailure{ReasonCode: "SECRET-REASON", ObservedAt: "SECRET-TIME", LastSuccessAt: "SECRET-LAST", Page: 1000, RowIndex: 1000, HTTPStatus: 1000}}
		body, err := RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		href := productTasksHref
		if attention {
			href += "?show=attention"
		}
		for _, want := range []string{"reason is Unknown", "Observed: Unknown", "Last successful read in this process: Unknown", `href="` + href + `">Retry</a>`} {
			if !strings.Contains(string(body), want) {
				t.Errorf("missing %s", want)
			}
		}
		if strings.Contains(string(body), "SECRET-") || strings.Contains(string(body), "1000") || strings.Contains(string(body), "Updated <time") {
			t.Fatal("unsafe Tasks diagnostics")
		}
		page.ReadFailure.ReasonCode = "operator_refused"
		body, err = RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "The Tasks source refused the read.") || strings.Contains(string(body), "HTTP 1000") {
			t.Fatal("invalid HTTP status rendered")
		}
	}
}

func allowanceBlock(t *testing.T, body []byte, usage bool) string {
	t.Helper()
	pattern := `(?s)<div class="allowance-strip">(.*?)</ul></div>`
	if usage {
		pattern = `(?s)<section class="allowance".*?</section>\s*<p class="note">Plan limits.*?</section>`
	}
	match := regexp.MustCompile(pattern).Find(body)
	if match == nil {
		t.Fatalf("allowance missing (usage=%v)", usage)
	}
	return string(match)
}

func TestRenderAccountAllowanceStatesOnBothPages(t *testing.T) {
	for _, state := range []ProductAllowanceState{AllowanceNotConnected, AllowanceUnknown, AllowanceStale, AllowanceMeasured} {
		for _, usage := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/usage=%v", state, usage), func(t *testing.T) {
				a := []ProductAllowance{{Provider: "Claude", Value: state, Reason: "Account binding unverified", Windows: []ProductAllowanceWindow{{Percent: "62.00% left", Window: "7 days", Reset: "2 Nov 14:00 UTC (display only)", Observed: "29 Oct 13:59 UTC", Provenance: "Provider API"}}}, {Provider: "Codex", Value: AllowanceNotConnected}}
				page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Allowance: a, Usage: &ProductUsage{Allowance: a}}
				if usage {
					page.Filter.View = "usage"
				}
				body, err := RenderProduct(page)
				if err != nil {
					t.Fatal(err)
				}
				block := allowanceBlock(t, body, usage)
				for _, want := range []string{"Claude", "Codex", "Not connected"} {
					if !strings.Contains(block, want) {
						t.Errorf("missing %q: %s", want, block)
					}
				}
				if (usage || state != AllowanceMeasured) && !strings.Contains(block, string(state)) {
					t.Errorf("missing state %q: %s", state, block)
				}
				measured := state == AllowanceStale || state == AllowanceMeasured
				if strings.Contains(block, "62.00% left") != measured || strings.Contains(block, "%") != measured {
					t.Fatalf("percent leaked or missing: %s", block)
				}
				if measured {
					for _, want := range []string{"7 days", "2 Nov 14:00 UTC (display only)", "29 Oct 13:59 UTC", "Provider API"} {
						if !strings.Contains(block, want) {
							t.Errorf("missing %q", want)
						}
					}
				}
				if state == AllowanceUnknown && !strings.Contains(block, "Account binding unverified") {
					t.Error("Unknown lacks its safe reason")
				}
				if strings.Contains(block, "tone-wait") != (state == AllowanceStale) {
					t.Errorf("wrong stale tone: %s", block)
				}
				if usage {
					if strings.Contains(block, `<table aria-label="Claude account allowance windows">`) != measured {
						t.Error("only measured or stale providers may render a table")
					}
					if strings.Contains(string(body), `class="allowance-strip"`) || !strings.Contains(block, `id="usage-allowance"`) || !strings.Contains(string(body), "No sessions in this read.") {
						t.Error("empty Usage or allowance anchor missing, or strip leaked")
					}
					if measured && (!strings.Contains(block, `<table aria-label="Claude account allowance windows">`) || !strings.Contains(block, `<th scope="col">Observed</th>`) || !strings.Contains(block, `data-label="Observed"`)) {
						t.Error("windows lack semantic labels")
					}
				} else if !strings.Contains(block, `href="?view=usage#usage-allowance">Account allowance</a>`) || !strings.Contains(block, `<ul role="list"`) {
					t.Error("missing keyboard-reachable Usage link")
				}
			})
		}
	}
}

func TestRenderAllowanceMissingMetadataAndWindowLimit(t *testing.T) {
	a := []ProductAllowance{{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{{Percent: "62% left"}, {Percent: "38% used"}, {Percent: "91% left"}, {Percent: "12% used"}}}}
	page := ProductPage{Title: "Sessions", Connected: true, Allowance: a, Usage: &ProductUsage{Allowance: a}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	strip := allowanceBlock(t, body, false)
	if !strings.Contains(strip, `>+2 more</a>`) || strings.Contains(strip, "91% left") || strings.Contains(strip, "12% used") || strings.Count(strip, "Unknown") != 6 {
		t.Fatalf("limit or missing metadata: %s", strip)
	}
	page.Filter.View = "usage"
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	section := allowanceBlock(t, body, true)
	if strings.Contains(section, "more</a>") || strings.Count(section, `class="allowance-window"`) != 4 || strings.Count(section, ">Unknown</td>") != 16 {
		t.Fatalf("full windows or missing metadata: %s", section)
	}
}

func TestRenderCompactAllowanceSelectsPercentsAndSharesMetadata(t *testing.T) {
	window := func(percent string) ProductAllowanceWindow {
		return ProductAllowanceWindow{Percent: percent, Window: "7 days", Reset: "2 Nov 14:00 UTC", Observed: "29 Oct 13:59 UTC", Provenance: "Provider API"}
	}
	for _, state := range []ProductAllowanceState{AllowanceMeasured, AllowanceStale} {
		windows := []ProductAllowanceWindow{window("Unknown"), window("62% left"), window("Unknown"), window("38% used"), window("91% left")}
		page := ProductPage{Title: "Sessions", Connected: true, Allowance: []ProductAllowance{{Provider: "Claude", Value: state, Windows: windows}}}
		body, err := RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		strip := allowanceBlock(t, body, false)
		for _, want := range []string{"62% left", "38% used", ">+3 more</a>", `<ul role="list"`, `<li class="allowance-provider">`} {
			if !strings.Contains(strip, want) {
				t.Errorf("missing %q: %s", want, strip)
			}
		}
		if strings.Contains(strip, "91% left") || strings.Contains(strip, "Unknown") || strings.Count(strip, "29 Oct 13:59 UTC") != 1 || strings.Count(strip, "Provider API") != 1 {
			t.Fatalf("compact windows or shared metadata: %s", strip)
		}
		if strings.Count(strip, "tone-wait") != map[bool]int{true: 1, false: 0}[state == AllowanceStale] || (state == AllowanceStale && !strings.Contains(strip, `<span class="allowance-state tone-wait">Stale</span>`)) {
			t.Fatalf("only Stale may use wait tone: %s", strip)
		}
	}
}

func TestRenderCompactAllowanceDoesNotPickVaryingMetadata(t *testing.T) {
	for _, field := range []string{"observed", "source"} {
		a := ProductAllowance{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{
			{Percent: "62% left", Observed: "29 Oct 13:59 UTC", Provenance: "Provider API"},
			{Percent: "Unknown", Observed: "29 Oct 13:59 UTC", Provenance: "Provider API"},
		}}
		if field == "observed" {
			a.Windows[1].Observed = "28 Oct 13:59 UTC"
		} else {
			a.Windows[1].Provenance = "Provider display"
		}
		page := ProductPage{Title: "Sessions", Connected: true, Allowance: []ProductAllowance{a}, Usage: &ProductUsage{Allowance: []ProductAllowance{a}}}
		body, err := RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		strip := allowanceBlock(t, body, false)
		if !strings.Contains(strip, "Seen varies · see Usage") || strings.Contains(strip, "Provider API") || strings.Contains(strip, "Provider display") || strings.Contains(strip, "29 Oct") {
			t.Fatalf("%s disagreement hidden: %s", field, strip)
		}
		page.Filter.View = "usage"
		body, err = RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		section := allowanceBlock(t, body, true)
		if !strings.Contains(section, a.Windows[1].Observed) || !strings.Contains(section, a.Windows[1].Provenance) || strings.Count(section, `class="allowance-window"`) != 2 {
			t.Fatalf("Usage lost per-window metadata: %s", section)
		}
	}
}

func TestRenderAllowanceWithoutAnyPercentIsUnknown(t *testing.T) {
	for _, state := range []ProductAllowanceState{AllowanceMeasured, AllowanceStale} {
		for _, usage := range []bool{false, true} {
			a := []ProductAllowance{{Provider: "Claude", Value: state, Windows: []ProductAllowanceWindow{{Percent: "Unknown"}, {}}}}
			page := ProductPage{Title: "Sessions", Connected: true, Allowance: a, Usage: &ProductUsage{Allowance: a}}
			if usage {
				page.Filter.View = "usage"
			}
			body, err := RenderProduct(page)
			if err != nil {
				t.Fatal(err)
			}
			block := allowanceBlock(t, body, usage)
			if !strings.Contains(block, "Unknown · No percent allowance reported") || strings.Contains(block, "<table") || strings.Contains(block, "Stale") || strings.Contains(block, "%") {
				t.Fatalf("empty measurement rendered: %s", block)
			}
		}
	}
}

func TestOtherProductPagesHaveNoAllowanceStrip(t *testing.T) {
	for _, title := range []string{"Tasks", "Asks", "Pages"} {
		body, err := RenderProduct(ProductPage{Title: title, Connected: true, Allowance: []ProductAllowance{{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{{Percent: "62% left"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), `class="allowance-strip"`) {
			t.Errorf("%s renders the strip", title)
		}
	}
	asks, _, err := RenderAsks(AsksPage{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := RenderPages(PagesWorkspace{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{asks, pages} {
		if strings.Contains(string(body), `class="allowance-strip"`) {
			t.Error("standalone page renders strip")
		}
	}
}

func TestReadFailureShowsNoAllowance(t *testing.T) {
	for _, usage := range []bool{false, true} {
		page := ProductPage{Title: "Sessions", ReadFailure: &ProductReadFailure{ReasonCode: "operator_unreachable"},
			Allowance: []ProductAllowance{{Provider: "Claude", Value: AllowanceMeasured, Windows: []ProductAllowanceWindow{{Percent: "62% left"}}}}}
		if usage {
			page.Filter.View = "usage"
		}
		body, err := RenderProduct(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{`class="allowance-strip"`, "#usage-allowance", `id="usage-allowance"`, "62% left"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("failed read usage=%v contains %q", usage, forbidden)
			}
		}
	}
}

func TestAllowanceWindowListsStackAndLinksHaveProviderNames(t *testing.T) {
	windows := []ProductAllowanceWindow{{Percent: "62% left"}, {Percent: "38% used"}, {Percent: "91% left"}}
	page := ProductPage{Title: "Sessions", Connected: true, Allowance: []ProductAllowance{
		{Provider: "Claude", Value: AllowanceMeasured, Windows: windows},
		{Provider: "Codex", Value: AllowanceMeasured, Windows: append(append([]ProductAllowanceWindow(nil), windows...), windows[0])},
	}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	strip := allowanceBlock(t, body, false)
	for _, want := range []string{`aria-label="Claude: 1 more allowance window"`, `aria-label="Codex: 2 more allowance windows"`} {
		if !strings.Contains(strip, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Count(strip, `<ul class="allowance-windows"`) != 2 || strings.Count(strip, `<li>`) != 4 {
		t.Fatal("windows must remain nested lists with two visible windows per provider")
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindSubmatch(body)
	if style == nil {
		t.Fatal("stylesheet missing")
	}
	css := string(style[1])
	if strings.Contains(css, `.allowance-strip ul:not(:has(.allowance-windows))`) || !strings.Contains(css, `.allowance-strip > ul:not(:has(.allowance-windows))`) {
		t.Fatal("horizontal provider layout must target only the outer list")
	}
	if !strings.Contains(css, `.allowance-link { display: flex; align-items: center; min-height: 24px;`) {
		t.Fatal("allowance link text must be vertically centered in its target")
	}
	if !strings.Contains(css, `.allowance h2 { margin-top: 10px; }`) {
		t.Fatal("Usage allowance heading needs top spacing")
	}
	page.Filter.View = "usage"
	page.Usage = &ProductUsage{Allowance: page.Allowance}
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	section := allowanceBlock(t, body, true)
	for _, provider := range []string{"Claude", "Codex"} {
		if !strings.Contains(section, `<table aria-label="`+provider+` account allowance windows">`) {
			t.Errorf("%s allowance table lacks its accessible name", provider)
		}
	}
	if strings.Contains(section, "<caption") {
		t.Fatal("allowance tables must use aria-label instead of offscreen captions")
	}
}
