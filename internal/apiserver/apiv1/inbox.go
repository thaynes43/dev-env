package apiv1

import "time"

const InboxPath = "/v1/inbox"

// Ask and reply (DESIGN-014, 2026-10-31). A list or read with
// InboxViewQuery=InboxViewThread adds the kind, the correlation and the
// question's derived state; without it the response keeps the older shape,
// so an older strict client still decodes it. InboxBoxQuery=InboxBoxSent lists
// the caller's own sent messages and the response echoes Box.
const (
	InboxViewQuery  = "view"
	InboxViewThread = "thread"
	InboxBoxQuery   = "box"
	InboxBoxSent    = "sent"

	InboxKindPlain    = "plain"
	InboxKindQuestion = "question"
	InboxKindReply    = "reply"

	InboxStateUnanswered     = "Unanswered"
	InboxStateAnswered       = "Answered"
	InboxStateAcknowledged   = "Acknowledged"
	InboxStateStale          = "Stale"
	InboxStateRecipientEnded = "RecipientEnded"
	InboxStateUnknown        = "Unknown"
)

// Requests contain intent only; the server derives all sender attribution.
// A plain message names its recipient exactly. A question names a recipient
// Session by name (namespace and UID optional, checked when given) or sets
// ToParent, and the server resolves the caller's recorded parent Session. A
// reply names the question in InReplyTo (UID optional, checked when given);
// the server sends it to the question's sender.
type PublishInboxRequest struct {
	Project   string           `json:"project"`
	Recipient TaskClaimOwner   `json:"recipient,omitzero"`
	Key       string           `json:"key"`
	Text      string           `json:"text"`
	Kind      string           `json:"kind,omitempty"`
	InReplyTo *InboxMessageRef `json:"inReplyTo,omitempty"`
	ToParent  bool             `json:"toParent,omitempty"`
}

// InboxMessageRef names one exact message.
type InboxMessageRef struct {
	Name string `json:"name"`
	UID  string `json:"uid,omitempty"`
}

// InboxThread is a question's derived state as the API observed it at
// ObservedAt. It is computed on each read and never stored. WaitingOn is the
// Session that owes the next step: the recipient to reply, or the asker to
// acknowledge the reply. Replies counts live replies when more than one raced.
type InboxThread struct {
	State             string           `json:"state"`
	Question          InboxMessageRef  `json:"question"`
	Reply             *InboxMessageRef `json:"reply,omitempty"`
	Replies           int              `json:"replies,omitempty"`
	WaitingOn         *TaskClaimOwner  `json:"waitingOn,omitempty"`
	StaleAfterSeconds int64            `json:"staleAfterSeconds"`
	ObservedAt        time.Time        `json:"observedAt"`
}

type InboxMetadata struct {
	Name           string              `json:"name"`
	UID            string              `json:"uid"`
	Project        string              `json:"project"`
	Key            string              `json:"key"`
	Sender         TaskClaimOwner      `json:"sender"`
	Recipient      TaskClaimOwner      `json:"recipient"`
	Actor          TaskClaimActor      `json:"actor"`
	Provenance     TaskClaimProvenance `json:"provenance"`
	Repository     string              `json:"repository"`
	Provider       string              `json:"provider"`
	RequestedModel string              `json:"requestedModel"`
	CreatedAt      time.Time           `json:"createdAt"`
	Archived       bool                `json:"archived,omitempty"`
	ArchivedAt     *time.Time          `json:"archivedAt,omitempty"`
	// Set only in the thread view and on ask/reply receipts.
	Kind      string           `json:"kind,omitempty"`
	InReplyTo *InboxMessageRef `json:"inReplyTo,omitempty"`
	Thread    *InboxThread     `json:"thread,omitempty"`
}
type InboxMessage struct {
	InboxMetadata
	Text string `json:"text"`
}
type InboxResult struct {
	Outcome string        `json:"outcome"`
	Message InboxMetadata `json:"message"`
}
type InboxList struct {
	Items    []InboxMetadata `json:"items"`
	Continue string          `json:"continue,omitempty"`
	// Box and View echo the requested sent box and thread view; an older
	// server that ignores those queries leaves them empty.
	Box  string `json:"box,omitempty"`
	View string `json:"view,omitempty"`
}
type InboxReadAck struct {
	Name        string         `json:"name"`
	UID         string         `json:"uid"`
	Project     string         `json:"project"`
	MessageName string         `json:"messageName"`
	MessageUID  string         `json:"messageUID"`
	Recipient   TaskClaimOwner `json:"recipient"`
	Actor       TaskClaimActor `json:"actor"`
	CreatedAt   time.Time      `json:"createdAt"`
	Archived    bool           `json:"archived,omitempty"`
	ArchivedAt  *time.Time     `json:"archivedAt,omitempty"`
}
type InboxAckResult struct {
	Outcome string       `json:"outcome"`
	Ack     InboxReadAck `json:"ack"`
}
