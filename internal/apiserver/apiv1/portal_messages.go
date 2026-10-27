package apiv1

import "time"

// PortalSessionMessagesPath is a distinct command endpoint; listing session
// summaries does not grant access to it.
const PortalSessionMessagesPath = "/v1/portal/session-messages"

// PortalMessageRequest is supplied only by the authenticated portal backend.
// ActorRef is an opaque backend-derived attribution, never a browser identity.
type PortalMessageRequest struct {
	RequestID   string `json:"requestID"`
	Project     string `json:"project"`
	SessionName string `json:"sessionName"`
	SessionUID  string `json:"sessionUID"`
	Text        string `json:"text"`
	ActorRef    string `json:"actorRef"`
}

// Redact requests when accidentally formatted; Text is never receipt data.
func (PortalMessageRequest) String() string     { return "[redacted portal message]" }
func (r PortalMessageRequest) GoString() string { return r.String() }

type PortalTransportStatus string

const (
	PortalTransportDelivered PortalTransportStatus = "TransportDelivered"
	PortalTransportRefused   PortalTransportStatus = "Refused"
	PortalTransportPending   PortalTransportStatus = "Pending"
	PortalTransportUnknown   PortalTransportStatus = "Unknown"
)

// PortalMessageReceipt reports transport state only; it carries no text or
// provider response. Reason is a static code, never an executor error.
type PortalMessageReceipt struct {
	RequestID       string                `json:"requestID"`
	SessionName     string                `json:"sessionName"`
	SessionUID      string                `json:"sessionUID"`
	ResolvedPodUID  string                `json:"resolvedPodUID,omitempty"`
	ObservedAt      time.Time             `json:"observedAt"`
	TransportStatus PortalTransportStatus `json:"transportStatus"`
	Reason          string                `json:"reason"`
}
