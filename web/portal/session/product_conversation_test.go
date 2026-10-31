package session

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

// fakeConversations answers one fixed read and records what it was asked.
type fakeConversations struct {
	c         Conversation
	err       error
	asked     []string
	sawCancel bool
}

func (f *fakeConversations) ReadConversation(ctx context.Context, name, uid string) (Conversation, error) {
	f.asked = append(f.asked, name+"/"+uid)
	if _, ok := ctx.Deadline(); !ok {
		f.sawCancel = true
	}
	return f.c, f.err
}

func TestWithHistorySelectsOneSessionAndStaysHonest(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	page := sendTestPage(t)

	// No source: no links, and the selected session says exactly why.
	got := withHistory(context.Background(), page, "uid-remote-idle", nil, now)
	m := findRow(t, got, "remote-idle").Messages
	if m.Conversation == nil || m.Conversation.State != "Unavailable" || m.Conversation.Note != historyGap || len(m.Conversation.Turns) != 0 || m.HistoryHref != "" {
		t.Fatalf("no-source selection %+v", m)
	}
	if findRow(t, got, "task-busy").Messages.Conversation != nil {
		t.Fatal("an unselected session got a conversation")
	}

	src := &fakeConversations{c: Conversation{State: "partial", Turns: []ConversationTurn{
		{At: now.Add(-2 * time.Minute), Role: "user", Text: "status?"},
		{Role: "delivered-message", From: "dev-env-1019-205125", Text: "relayed"},
		{At: now.Add(-time.Minute), Role: "assistant", Text: "working", Truncated: true},
		{Role: "tool_result", Text: "odd"},
	}}}
	got = withHistory(context.Background(), page, "uid-remote-idle", src, now)
	if strings.Join(src.asked, ",") != "remote-idle/uid-remote-idle" || src.sawCancel {
		t.Fatalf("source asked %v, bounded %v", src.asked, !src.sawCancel)
	}
	m = findRow(t, got, "remote-idle").Messages
	c := m.Conversation
	if c == nil || c.State != "Partial" || c.Note != conversationNotes["partial"] || len(c.Turns) != 4 {
		t.Fatalf("selected conversation %+v", c)
	}
	if c.Turns[0].Role != "user" || !strings.HasPrefix(c.Turns[0].At, "2m ago · ") || c.Turns[1].Role != "message" || c.Turns[1].From != "dev-env-1019-205125" || c.Turns[1].At != "time Unknown" || !c.Turns[2].Truncated || c.Turns[3].Role != "" {
		t.Fatalf("turns %+v", c.Turns)
	}
	// The link names the UID only: a positional row fragment could open
	// another session once the list moves.
	for _, name := range []string{"remote-idle", "task-busy", "remote-busy", "remote-suspended"} {
		row := findRow(t, got, name)
		want := SessionsPath + "?session=" + url.QueryEscape("uid-"+name)
		if row.Messages.HistoryHref != want {
			t.Fatalf("%s history link %q, want %q", name, row.Messages.HistoryHref, want)
		}
		if row.Selected != (name == "remote-idle") {
			t.Fatalf("%s selected %v", name, row.Selected)
		}
	}
	if findRow(t, page, "remote-idle").Messages.Conversation != nil {
		t.Fatal("withHistory changed the input page")
	}

	for _, tc := range []struct {
		src  *fakeConversations
		note string
	}{
		{&fakeConversations{err: errors.New("exec failed: secret detail")}, "The conversation read failed. No turns are shown rather than a guess."},
		{&fakeConversations{c: Conversation{State: "unavailable", ReasonCode: "not_running", Turns: []ConversationTurn{{Role: "user", Text: "stale"}}}}, "The conversation is unavailable (not_running)."},
	} {
		c := findRow(t, withHistory(context.Background(), page, "uid-remote-idle", tc.src, now), "remote-idle").Messages.Conversation
		if c.State != "Unavailable" || c.Note != tc.note || len(c.Turns) != 0 {
			t.Fatalf("failed read rendered %+v", c)
		}
	}

	missing := withHistory(context.Background(), page, "uid-gone", src, now)
	if missing.Notice != "The selected session is not in this list." || missing.Quiet {
		t.Fatalf("missing selection notice %q", missing.Notice)
	}
	partial := page
	partial.Notice, partial.Quiet = "Sessions read is incomplete. This is not the full inventory.", false
	if got := withHistory(context.Background(), partial, "uid-gone", src, now).Notice; got != "Sessions read is incomplete. This is not the full inventory. The selected session is not in this list." {
		t.Fatalf("partial read lost its warning: %q", got)
	}
}

func TestProductFilterSelectsOneSession(t *testing.T) {
	for raw, want := range map[string]bool{
		SessionsPath + "?session=uid-remote-idle":                      true,
		SessionsPath + "?session=0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0": true,
		TasksPath + "?session=uid-remote-idle":                         false,
		SessionsPath + "?session=uid%2Fx":                              false,
		SessionsPath + "?session=":                                     false,
		SessionsPath + "?session=a&project=dev-env":                    false,
		SessionsPath + "?session=a&session=b":                          false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		f, ok := productFilter(u)
		if ok != want || (ok && f.Session != u.Query().Get("session")) {
			t.Errorf("%s: ok %v filter %+v", raw, ok, f)
		}
	}
	if _, ok := productFilter(&url.URL{Path: SessionsPath, RawQuery: "session=" + strings.Repeat("a", 65)}); ok {
		t.Error("an overlong UID was accepted")
	}
}

func TestSessionReadAttachesSelectedConversation(t *testing.T) {
	var nilSource *SessionSource
	nilSource.UseConversations(&fakeConversations{}) // a nil source ignores it
	page := withHistory(context.Background(), portal.ProductPage{Connected: true}, "", nil, time.Now())
	if page.Notice != "" {
		t.Fatal("an unselected page got a notice")
	}
}

// cappedInventory is a complete read of n sessions in name order, paged as the
// operator pages them. Every session sorts into one group, so the last names
// fall past maxListedSessions.
func cappedInventory(n int) []string {
	var rows []string
	for i := 0; i < n; i++ {
		rows = append(rows, fmt.Sprintf(`{"name":"agent-%03d","uid":"uid-agent-%03d","repo":"dev-env","phase":"Running","observedAt":"2026-10-23T16:00:00Z","freshness":"observed",%s}`, i, i, unknownActivity))
	}
	return rows
}

func countListed(page portal.ProductPage) (int, map[string]portal.ProductRow) {
	byName := map[string]portal.ProductRow{}
	for _, g := range page.Groups {
		for _, row := range g.Rows {
			byName[row.Title] = row
		}
	}
	return len(byName), byName
}

// A receipt's Back link, or any query-only ?session= URL, opens the selected
// session even when it is neither the first row nor within the first
// maxListedSessions: its row replaces the last listed one, it is the default
// detail with its composer, the phone view opens it rather than the list, and
// only that session's name and UID reach the history source.
func TestSelectedSessionPastTheListingCapOpensFromAReceipt(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	now := f.now
	const total = maxListedSessions + 2
	rows := cappedInventory(total)
	cert, ca := mintNamed(t, operatorPortalHost)
	srv := serveTLS(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := 0
		if c := r.URL.Query().Get("cursor"); c != "" {
			n, _ = strconv.Atoi(strings.TrimPrefix(c, "p"))
		}
		end, next := min((n+1)*productPageSize, total), ""
		if end < total {
			next = `,"nextCursor":"p` + strconv.Itoa(n+1) + `"`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"sessions":[%s],"limit":50,"complete":%t,"freshness":"observed","observedAt":"2026-10-23T16:00:00Z"%s}`, strings.Join(rows[n*productPageSize:end], ","), end == total, next)
	}))
	token := filepath.Join(t.TempDir(), "operator")
	if err := os.WriteFile(token, []byte(fixtureOperatorToken("dev-env-operator", now.Add(time.Hour))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := newSessionSource(token, ca, func() time.Time { return now }, routeDial(map[string]string{operatorPortalHost: srv}))
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeConversations{c: Conversation{State: "complete", Turns: []ConversationTurn{{At: now.Add(-time.Minute), Role: "assistant", Text: "selected-agent-turn"}}}}
	sessions.UseConversations(src)

	selected := fmt.Sprintf("agent-%03d", total-1)
	page, err := sessions.Read(t.Context(), portal.ProductFilter{Session: "uid-" + selected})
	if err != nil {
		t.Fatal(err)
	}
	listed, byName := countListed(page)
	dropped := fmt.Sprintf("agent-%03d", maxListedSessions-1)
	kept := fmt.Sprintf("agent-%03d", maxListedSessions-2)
	if listed != maxListedSessions || len(page.Usage.Rows) != maxListedSessions || !byName[selected].Selected || byName[kept].Title == "" || byName[dropped].Title != "" {
		t.Fatalf("listed %d usage %d: selected %+v kept %q dropped %q", listed, len(page.Usage.Rows), byName[selected].Selected, byName[kept].Title, byName[dropped].Title)
	}
	if page.Rail.All != strconv.Itoa(total) || page.Groups[groupOf(t, page, selected)].Total != total || page.ListCount != "Showing 150 of 152 sessions · all projects" || page.Usage.Stats[0].Value != "0 of 152" {
		t.Fatalf("totals changed: rail %s list %q stats %+v", page.Rail.All, page.ListCount, page.Usage.Stats)
	}
	if strings.Join(src.asked, ",") != selected+"/uid-"+selected {
		t.Fatalf("history source asked %v", src.asked)
	}
	if c := byName[selected].Messages.Conversation; c == nil || c.State != "Complete" {
		t.Fatalf("selected conversation %+v", c)
	}

	// Return from the session's receipt.
	receipt, err := portal.RenderReceipt(portal.ProductReceipt{RequestID: strings.Repeat("r", 43), Session: selected, SessionUID: "uid-" + selected, TransportStatus: "Delivered", ReasonCode: "delivered", ObservedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	back := regexp.MustCompile(`<a [^>]*href="(` + regexp.QuoteMeta(SessionsPath) + `\?session=[^"#]*)"`).FindSubmatch(receipt)
	if back == nil || string(back[1]) != SessionsPath+"?session=uid-"+selected {
		t.Fatalf("receipt Back link %q", back)
	}
	authorizer := currentFixture(f, func(*http.Request) (*http.Response, error) { return userInfoResponse(currentAllow), nil })
	handler := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Send: &countingForms{}, FreshFor: time.Minute, Now: func() time.Time { return now }})
	src.asked = nil
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, f.request(http.MethodGet, string(back[1]), ""))
	body := w.Body.String()
	if w.Code != http.StatusOK || w.Header().Get("Content-Security-Policy") != portal.ProductSendPolicy || strings.Join(src.asked, ",") != selected+"/uid-"+selected {
		t.Fatalf("status %d policy %q asked %v", w.Code, w.Header().Get("Content-Security-Policy"), src.asked)
	}
	id := byName[selected].ID
	section := detailSection(t, body, id)
	if !strings.HasPrefix(section, `<section class="detail default" id="`+id+`"`) || strings.Count(body, `<section class="detail default"`) != 1 {
		t.Fatalf("the selected session is not the default detail:\n%.300s", section)
	}
	for _, want := range []string{`>` + selected + `</h2>`, `<form class="composer" method="post"`, `name="sessionUID" value="uid-` + selected + `"`, `name="session" value="` + selected + `"`, "selected-agent-turn"} {
		if !strings.Contains(section, want) {
			t.Fatalf("selected detail lacks %s", want)
		}
	}
	if first := detailSection(t, body, "s1"); !strings.HasPrefix(first, `<section class="detail" id="s1"`) {
		t.Fatalf("the first row is still a default: %.80s", first)
	}
	if !strings.Contains(body, `<div class="shell selected">`) || strings.Count(body, `class="item default"`) != 1 || strings.Count(body, ` aria-current="page"><span class="item-top">`) != 1 ||
		!strings.Contains(body, `<a class="item default" href="`+SessionsPath+`?session=uid-`+selected+`" aria-current="page">`) {
		t.Fatal("the list does not mark the selected session, or the phone view would open the list")
	}

	// A partial read keeps its warning and still lists the selection.
	var all []apiv1.PortalSessionSummary
	for _, chunk := range [][]string{rows[:50], rows[50:100], rows[100:150], rows[150:]} {
		d, err := decodeSessionPage([]byte(`{"sessions":[`+strings.Join(chunk, ",")+`],"limit":50,"complete":true,"freshness":"observed","observedAt":"2026-10-23T16:00:00Z"}`), now)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, d.Sessions...)
	}
	partial := withHistory(t.Context(), sessionsPage(all, false, "observed", now, portal.ProductFilter{Session: "uid-" + selected}), "uid-"+selected, nil, now)
	if n, names := countListed(partial); n != maxListedSessions || !names[selected].Selected || partial.Notice != "Sessions read is incomplete. This is not the full inventory." || partial.ListCount != "Partial list" {
		t.Fatalf("partial read: %d listed, notice %q, count %q", n, partial.Notice, partial.ListCount)
	}
}

func groupOf(t *testing.T, page portal.ProductPage, title string) int {
	t.Helper()
	for i, g := range page.Groups {
		for _, row := range g.Rows {
			if row.Title == title {
				return i
			}
		}
	}
	t.Fatalf("%s is not listed", title)
	return -1
}

// detailSection is one row's rendered detail, up to the next detail.
func detailSection(t *testing.T, body, id string) string {
	t.Helper()
	at := regexp.MustCompile(`<section class="detail[a-z ]*" id="` + id + `"`).FindStringIndex(body)
	if at == nil {
		t.Fatalf("no detail %s", id)
	}
	rest := body[at[0]:]
	if end := strings.Index(rest[1:], `<section class="detail`); end >= 0 {
		return rest[:end+1]
	}
	return rest
}
