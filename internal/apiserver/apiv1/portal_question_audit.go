package apiv1

import "time"

// Portal question-audit metadata (DESIGN-014, 2026-11-01). This is a separate
// data class from inbox content: one selected session's message metadata and
// the existing derived thread. It carries no message text, idempotency key,
// prompt, provider error or raw history. Default-off source does not grant
// the portal reader any inbox route.
const (
	PortalQuestionAuditPath = "/v1/portal/question-audit"
	// PortalQuestionAuditVersion is the only response version a consumer accepts.
	PortalQuestionAuditVersion = "portal-question-audit.v1"
	PortalQuestionBoxInbox     = "inbox"
	PortalQuestionBoxSent      = "sent"
	// One page. The consumer does not follow NextCursor.
	PortalQuestionAuditDefaultLimit = 20
	PortalQuestionAuditMaxLimit     = 50
	// PortalQuestionAuditMaxBytes is the response cap, including the encoder's trailing newline.
	PortalQuestionAuditMaxBytes = 128 << 10
	// PortalQuestionAuditStaleAfterSeconds is inbox.StaleAfter. The producer test
	// refuses a drift between this constant and that duration.
	PortalQuestionAuditStaleAfterSeconds int64 = 1800

	PortalQuestionOriginUnknown          = "Unknown"
	PortalQuestionApprovalNotRecorded    = "not recorded"
	PortalQuestionConsumptionNotRecorded = "not recorded"
	PortalQuestionServedModelUnknown     = "Unknown"

	PortalQuestionAckRecorded    = "recorded"
	PortalQuestionAckUnavailable = "unavailable"
)

// PortalQuestionAuditIdentity is an exact session name and UID. It is the live
// object the operator read, not a catalog snapshot.
type PortalQuestionAuditIdentity struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

// PortalQuestionAuditAck is an API acknowledgement's allowlisted evidence.
// Status unavailable has no actor and no time. Recorded copies those fields
// only after the linked reply's name, UID, project and recipient match.
// Neither status is provider consumption or approval.
type PortalQuestionAuditAck struct {
	Status    string          `json:"status"`
	Actor     *TaskClaimActor `json:"actor,omitempty"`
	CreatedAt *time.Time      `json:"createdAt,omitempty"`
}

// PortalQuestionAuditItem is one message's metadata. Kind plain has no thread.
// Origin, approval, provider consumption and served model are fixed literals:
// this schema does not record them, and requested model is not served model.
type PortalQuestionAuditItem struct {
	Name                 string                `json:"name"`
	UID                  string                `json:"uid"`
	Kind                 string                `json:"kind"`
	Sender               TaskClaimOwner        `json:"sender"`
	Recipient            TaskClaimOwner        `json:"recipient"`
	Actor                TaskClaimActor        `json:"actor"`
	Provenance           TaskClaimProvenance   `json:"provenance"`
	CreatedAt            time.Time             `json:"createdAt"`
	Origin               string                `json:"origin"`
	Approval             string                `json:"approval"`
	ProviderConsumption  string                `json:"providerConsumption"`
	ServedModel          string                `json:"servedModel"`
	Thread               *InboxThread          `json:"thread,omitempty"`
	Acknowledgement      PortalQuestionAuditAck `json:"acknowledgement"`
}

// PortalQuestionAudit is one box's page. Truncated with NextCursor means the
// store has another page; this response does not contain it.
type PortalQuestionAudit struct {
	Version              string                      `json:"version"`
	Project              string                      `json:"project"`
	Session              PortalQuestionAuditIdentity `json:"session"`
	Box                  string                      `json:"box"`
	Limit                int                         `json:"limit"`
	Truncated            bool                        `json:"truncated"`
	NextCursor           string                      `json:"nextCursor,omitempty"`
	Origin               string                      `json:"origin"`
	Approval             string                      `json:"approval"`
	ProviderConsumption  string                      `json:"providerConsumption"`
	ServedModel          string                      `json:"servedModel"`
	Items                []PortalQuestionAuditItem   `json:"items"`
}
