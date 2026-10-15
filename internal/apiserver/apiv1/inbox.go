package apiv1

import "time"

const InboxPath = "/v1/inbox"

// Requests contain intent only; the server derives all sender attribution.
type PublishInboxRequest struct {
	Project   string         `json:"project"`
	Recipient TaskClaimOwner `json:"recipient"`
	Key       string         `json:"key"`
	Text      string         `json:"text"`
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
