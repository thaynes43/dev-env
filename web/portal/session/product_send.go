package session

import (
	"github.com/thaynes43/dev-env/web/portal"
)

// SessionMessagesPath is the portal's same-origin send route (DESIGN-029). The
// route and its receipts belong to the send handler; the Sessions page only
// posts to it.
const SessionMessagesPath = portal.ProductSessionMessagesHref

// sendGate is a working form's gate: what Send does and what a receipt means.
const sendGate = "Send asks the operator to deliver this text to the agent's terminal and opens a receipt. Delivered means agentd accepted it, not that the agent read or answered it."

// SendForms connects the Sessions page to the portal send route. A nil
// SendForms keeps every Send disabled. NewRequestID returns a fresh request ID
// in the route's format, one per rendered draft box, so a resubmitted form
// maps to its existing receipt instead of a second send.
type SendForms interface {
	NewRequestID() (string, error)
}

// withSendForms turns every Draftable message box on page into a form bound to
// the caller's CSRF value and its own request ID. Any failure, or a repeated
// ID, returns page unchanged: a page never mixes working and guessed forms.
func withSendForms(page portal.ProductPage, csrf string, forms SendForms) portal.ProductPage {
	if forms == nil || csrf == "" || !page.Connected {
		return page
	}
	out := page
	out.Groups = make([]portal.ProductGroup, len(page.Groups))
	seen := map[string]struct{}{}
	for i, g := range page.Groups {
		g.Rows = append([]portal.ProductRow(nil), g.Rows...)
		for j, row := range g.Rows {
			if row.Messages == nil || !row.Messages.Draftable {
				continue
			}
			id, err := forms.NewRequestID()
			if _, dup := seen[id]; err != nil || dup {
				return page
			}
			seen[id] = struct{}{}
			m := *row.Messages
			m.Send = &portal.ProductSend{Action: SessionMessagesPath, CSRF: csrf, RequestID: id}
			m.Gate = sendGate
			m.Route.Text = "Portal and CLI"
			m.RouteNote = "From a session or the workbench, the same delivery runs as:"
			g.Rows[j].Messages = &m
		}
		out.Groups[i] = g
	}
	return out
}
