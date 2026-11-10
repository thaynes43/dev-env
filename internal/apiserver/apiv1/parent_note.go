package apiv1

const ParentNotesPath = "/v1/parent-notes"

const (
	ParentNoteQuestion       = "question"
	ParentNoteResult         = "result"
	ParentNoteBlocker        = "blocker"
	ParentNotesInbox         = "inbox"
	ParentNotesSent          = "sent"
	ParentNoteWaiting        = "waiting"
	ParentNoteAnswered       = "answered"
	ParentNoteMaxWaitSeconds = 900
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

// AnswerParentRequest has no correlation or recipient fields. The server
// binds them to the live note named by the route.
type AnswerParentRequest struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// A waiting result contains no text. An answered result carries the reply,
// whose inReplyTo names the note the caller waited on.
type ParentNoteWaitResult struct {
	State string        `json:"state"`
	Reply *InboxMessage `json:"reply,omitempty"`
}
