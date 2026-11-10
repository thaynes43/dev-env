package apiv1

const ParentNotesPath = "/v1/parent-notes"

const (
	ParentNoteQuestion = "question"
	ParentNoteResult   = "result"
	ParentNoteBlocker  = "blocker"
	ParentNotesInbox   = "inbox"
	ParentNotesSent    = "sent"
)

// TellParentRequest contains intent only. The server derives the project and
// recipient from the authenticated child session, never from the request.
// Receipts, lists and reads use the existing InboxResult, InboxList and
// InboxMessage shapes, including kind, without the peer inbox thread view.
type TellParentRequest struct {
	Key  string `json:"key"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}
