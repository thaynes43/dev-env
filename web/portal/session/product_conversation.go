package session

import (
	"context"
	"net/url"
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

// Conversation is one session's conversation as a ConversationSource read it,
// oldest turn first (DESIGN-029, proposed backend history contract). State is
// complete, partial, truncated or unavailable; ReasonCode explains an
// unavailable read.
type Conversation struct {
	State, ReasonCode string
	Turns             []ConversationTurn
}

// ConversationTurn is one turn. Role is user, assistant or delivered-message;
// From names a delivered message's sender. Text is plaintext.
type ConversationTurn struct {
	At         time.Time
	Role, From string
	Text       string
	Truncated  bool
}

// ConversationSource reads the conversation agentd recorded for one Session,
// fenced by its name and UID. No supported route serves one yet: nil keeps
// history unavailable, and the portal never reads provider files itself.
type ConversationSource interface {
	ReadConversation(ctx context.Context, sessionName, sessionUID string) (Conversation, error)
}

// UseConversations connects the Sessions read to a conversation source.
func (s *SessionSource) UseConversations(c ConversationSource) {
	if s != nil {
		s.conversations = c
	}
}

const conversationTimeout = 5 * time.Second

var sessionUIDQuery = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

var conversationNotes = map[string]string{
	"complete":  "Every turn the session's agent recorded, as plain text.",
	"partial":   "Some turns are missing from this read.",
	"truncated": "Older or long turns were cut to fit. The newest are shown.",
}

// historyGap is the selected session's history when no source is connected.
const historyGap = "No supported route reads a live session's conversation yet. The portal does not read provider files directly."

// withHistory links each session to its own selected view and, for the one
// session filter.Session selects, attaches its conversation or the exact
// reason it is unavailable. Nothing is invented: a failed read shows no turns.
func withHistory(ctx context.Context, page portal.ProductPage, selected string, src ConversationSource, now time.Time) portal.ProductPage {
	found := false
	page.Groups = append([]portal.ProductGroup(nil), page.Groups...)
	for i := range page.Groups {
		rows := append([]portal.ProductRow(nil), page.Groups[i].Rows...)
		for j, row := range rows {
			if row.Messages == nil || !sessionUIDQuery.MatchString(row.Messages.SessionUID) {
				continue
			}
			m := *row.Messages
			if src != nil {
				m.HistoryHref = SessionsPath + "?session=" + url.QueryEscape(m.SessionUID) + "#" + row.ID
			}
			if selected != "" && m.SessionUID == selected {
				found = true
				m.Conversation = readConversation(ctx, src, m.Session, m.SessionUID, now)
			}
			rows[j].Messages = &m
		}
		page.Groups[i].Rows = rows
	}
	if selected != "" && !found {
		// A warning already on the page (an incomplete read) may be why the
		// session is missing: keep it ahead of this notice.
		missing := "The selected session is not in this list."
		if page.Notice != "" && !page.Quiet {
			missing = page.Notice + " " + missing
		}
		page.Notice, page.Quiet = missing, false
	}
	return page
}

func readConversation(ctx context.Context, src ConversationSource, name, uid string, now time.Time) *portal.ProductConversation {
	if src == nil {
		return &portal.ProductConversation{State: "Unavailable", Note: historyGap}
	}
	ctx, cancel := context.WithTimeout(ctx, conversationTimeout)
	defer cancel()
	c, err := src.ReadConversation(ctx, name, uid)
	if err != nil {
		return &portal.ProductConversation{State: "Unavailable", Note: "The conversation read failed. No turns are shown rather than a guess."}
	}
	note, ok := conversationNotes[c.State]
	if !ok {
		return &portal.ProductConversation{State: "Unavailable", Note: "The conversation is unavailable (" + shown(c.ReasonCode) + ")."}
	}
	out := &portal.ProductConversation{State: map[string]string{"complete": "Complete", "partial": "Partial", "truncated": "Truncated"}[c.State], Note: note}
	for _, t := range c.Turns {
		role := map[string]string{"user": "user", "assistant": "assistant", "delivered-message": "message"}[t.Role]
		at := "time Unknown"
		if !t.At.IsZero() {
			at = portal.Ago(t.At, now) + " · " + t.At.UTC().Format("2 Jan 15:04 UTC")
		}
		out.Turns = append(out.Turns, portal.ProductTurn{Role: role, From: t.From, At: at, Text: t.Text, Truncated: t.Truncated})
	}
	if len(out.Turns) == 0 && c.State == "complete" {
		out.Note = "The session's agent recorded no turns yet."
	}
	return out
}
