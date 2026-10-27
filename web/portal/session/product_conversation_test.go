package session

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

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
	for _, name := range []string{"remote-idle", "task-busy", "remote-busy", "remote-suspended"} {
		row := findRow(t, got, name)
		want := SessionsPath + "?session=" + url.QueryEscape("uid-"+name) + "#" + row.ID
		if row.Messages.HistoryHref != want {
			t.Fatalf("%s history link %q, want %q", name, row.Messages.HistoryHref, want)
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
