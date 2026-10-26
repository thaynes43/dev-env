package grantdecision

// HandoffAnnotation carries an operator-authenticated decision to the broker.
// Admission must restrict changes to this key to the operator and broker. The
// value is metadata only; it never contains credential material.
const HandoffAnnotation = "dev-env.haynesops.com/grant-decision"

// ManualApproverAnnotation marks a GrantPolicy as a delegated decision scope,
// not a standing approval. Its value is the exact coordinator principal.
const ManualApproverAnnotation = "dev-env.haynesops.com/decision-approver"

// Handoff is not an authentication mechanism. The operator must derive the
// principal from TokenReview and the broker must recheck the complete binding
// on the same grant version it updates.
type Handoff struct {
	Binding   Binding `json:"binding"`
	Principal string  `json:"principal"`
	Decision  string  `json:"decision"`
	TTL       string  `json:"ttl,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}
