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

// TellParentRequest contains intent only. A child omits Session: the server
// derives the project and the parent from that session. A policy caller sets
// Session to the child it started and omits Kind: that stores one plain
// message. Receipts, lists and reads use the existing InboxResult, InboxList
// and InboxMessage shapes, including kind, without the peer inbox thread view.
type TellParentRequest struct {
	Key     string `json:"key"`
	Kind    string `json:"kind"`
	Text    string `json:"text"`
	Session string `json:"session,omitempty"`
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
