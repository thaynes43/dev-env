package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
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
	turns := []ProductTurn{{Role: "user", At: "1m ago", Text: "line one\nline two <script>x</script>‮"}, {Role: "assistant", Text: long}, {Role: "message", From: "dev-env-1019-205125", Text: "relayed"}, {Role: "tool\" onclick=\"x", Text: "odd role"}}
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
	if strings.Contains(html, "<script>") || strings.Contains(html, "evil.example") || strings.Contains(html, "onclick") || strings.Contains(html, "‮") || strings.Count(html, "é") > maxTurnBytes/2 {
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
