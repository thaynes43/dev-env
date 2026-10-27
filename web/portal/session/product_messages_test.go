package session

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestSessionMessagesRouteFollowsDelivery(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	state := func(s string) string {
		return strings.Replace(unknownActivity, `"agentState":"idle"`, `"agentState":"`+s+`"`, 1)
	}
	local := strings.Replace(state("waiting"), `"mode":"remote"`, `"mode":"local"`, 1)
	modeUnknown := strings.Replace(unknownActivity, `"mode":"remote"`, `"mode":"Unknown"`, 1)
	decoded, err := decodeSessionPage([]byte(activityPage(
		activityRow("remote-idle", unknownActivity), activityRow("local-waiting", local), activityRow("remote-busy", state("busy")),
		activityRow("task-busy", reportedActivity), activityRowPhase("remote-suspended", "Suspended", unknownActivity),
		activityRow("remote-pending", state("pending")), activityRow("remote-exited", state("exited")),
		activityRow("remote-state-unknown", state("Unknown")), activityRow("mode-unknown", modeUnknown), legacyRow("legacy"),
	)), now)
	if err != nil {
		t.Fatal(err)
	}
	page := sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{})
	for _, tc := range []struct {
		name, route, note string
		draftable         bool
	}{
		{"remote-idle", "CLI route", "agentd decides delivery when it is sent", true},
		{"local-waiting", "CLI route", "this page cannot observe it", true},
		{"remote-busy", "CLI route", "from a session or the workbench", true},
		{"task-busy", "Takes no messages", "A headless task reads no messages.", false},
		{"remote-suspended", "Not running", "The session is Suspended.", false},
		{"remote-pending", "Starting", "until the agent starts", false},
		{"remote-exited", "Not running", "The agent is exited.", false},
		{"remote-state-unknown", "Route Unknown", "The agent's state is Unknown", false},
		{"mode-unknown", "Route Unknown", "How this session runs is Unknown", false},
		{"legacy", "Route Unknown", "How this session runs is Unknown", false},
	} {
		m := findRow(t, page, tc.name).Messages
		if m == nil {
			t.Fatalf("%s: no message box", tc.name)
		}
		if m.Route.Text != tc.route || !strings.Contains(m.RouteNote, tc.note) || m.Draftable != tc.draftable {
			t.Errorf("%s: route %q note %q draftable %v", tc.name, m.Route.Text, m.RouteNote, m.Draftable)
		}
		if m.History != messagesHistory || m.Gate != messagesGate {
			t.Errorf("%s: history or gate changed: %+v", tc.name, m)
		}
		wantCommand := ""
		if tc.draftable {
			wantCommand = `agent-run msg ` + tc.name + ` "<text>"`
		}
		wantPlaceholder := tc.route + ". Drafting is off for this session."
		if tc.draftable {
			wantPlaceholder = "Draft a follow-up for " + tc.name + "…"
		}
		if m.Command != wantCommand || m.Placeholder != wantPlaceholder {
			t.Errorf("%s: command %q placeholder %q", tc.name, m.Command, m.Placeholder)
		}
	}
	if m := findRow(t, page, "remote-idle").Messages; m.Agent != "provider Unknown · claude-opus-5-5" {
		t.Errorf("agent label %q", m.Agent)
	}
}

func TestSessionMessagesRenderNoSendPath(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	hostile := `<script>alert(1)</script>`
	decoded, err := decodeSessionPage([]byte(activityPage(activityRow("remote-idle", unknownActivity), activityRow("task-busy", reportedActivity), activityRow(`<script>alert(1)</script>`, unknownActivity))), now)
	if err != nil {
		t.Fatal(err)
	}
	body, err := portal.RenderProduct(sessionsPage(decoded.Sessions, true, "observed", now, portal.ProductFilter{}))
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	for _, banned := range []string{"<form", "<script", "formaction", "method=", "action=", hostile} {
		if strings.Contains(html, banned) {
			t.Fatalf("rendered page holds %q", banned)
		}
	}
	if n := strings.Count(html, `<button class="btn send" type="button" disabled`); n != 3 {
		t.Fatalf("%d disabled Send buttons, want 3", n)
	}
	if n := strings.Count(html, `<textarea `); n != 3 {
		t.Fatalf("%d draft boxes, want 3", n)
	}
	if n := strings.Count(html, `-route" disabled></textarea>`); n != 1 {
		t.Fatalf("task draft box is not disabled once (%d)", n)
	}
	if strings.Count(html, messagesGate) != 3 || strings.Count(html, "<p class=\"history\">"+strings.ReplaceAll(messagesHistory, "'", "&#39;")+"</p>") != 3 {
		t.Fatal("gate or history missing from a message box")
	}
	if !strings.Contains(html, `<code class="route-command">agent-run msg remote-idle &#34;&lt;text&gt;&#34;</code>`) || !strings.Contains(html, `agent-run msg &lt;script&gt;alert(1)&lt;/script&gt; &#34;&lt;text&gt;&#34;`) {
		t.Fatal("route command missing or unescaped")
	}
	if !regexp.MustCompile(`<label class="sr-only" for="(s[0-9]+)-draft">Draft a message to remote-idle</label><textarea id="(s[0-9]+)-draft"`).MatchString(html) {
		t.Fatal("draft box has no label")
	}
}
