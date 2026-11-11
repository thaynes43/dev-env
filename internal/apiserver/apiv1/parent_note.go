package apiv1

import (
	"strconv"
	"time"
)

const ParentNotesPath = "/v1/parent-notes"

// ParentNotesWaitAnyPath is the session's own held read for new notes.
// It is not the per-note wait at /v1/parent-notes/{message}/wait.
const ParentNotesWaitAnyPath = ParentNotesPath + "/wait"

const (
	ParentNoteQuestion       = "question"
	ParentNoteResult         = "result"
	ParentNoteBlocker        = "blocker"
	ParentNotesInbox         = "inbox"
	ParentNotesSent          = "sent"
	ParentNoteWaiting        = "waiting"
	ParentNoteAnswered       = "answered"
	ParentNoteArrived        = "note"
	ParentNoteResync         = "resync"
	ParentNoteMaxWaitSeconds = 900
	// ParentNoteMaxCursorBytes is the opaque cursor limit. A longer value is
	// refused; it is never treated as a resource version.
	ParentNoteMaxCursorBytes = 256
)

// ValidResourceVersion reports whether v is one Kubernetes resource version a
// parent wait can start from: a canonical decimal integer, at most
// ParentNoteMaxCursorBytes long. Empty is not a version. Zero is not one
// either: a watch from resourceVersion 0 replays objects that already exist.
// The caller does not guess a nearby value.
func ValidResourceVersion(v string) bool {
	if v == "" || v == "0" || len(v) > ParentNoteMaxCursorBytes {
		return false
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return false
	}
	return strconv.FormatUint(n, 10) == v
}

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

// ParentNoteWaitAnyResult is one held read for a new note addressed to the
// caller. It carries the note's name, child, kind and creation time, or
// waiting, or resync. It never carries the note's text or key.
type ParentNoteWaitAnyResult struct {
	State     string    `json:"state"`
	Name      string    `json:"name,omitempty"`
	Child     string    `json:"child,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
	Cursor    string    `json:"cursor,omitempty"`
}
