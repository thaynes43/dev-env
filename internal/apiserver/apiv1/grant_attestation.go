package apiv1

import "time"

// GrantAttestationPath is the route of a grant's attestation (default off).
func GrantAttestationPath(name string) string { return GrantPath(name) + "/attestation" }

// AttestGrantRequest is the body of POST /v1/grants/{name}/attestation. Only the
// grant's own requesting session, from its own pod, may send it. The attestor is
// taken from the token, never from this body.
type AttestGrantRequest struct {
	// ApprovedBy is who the session claims approved: "user" or "coordinator:<ref>".
	// It is an unverified claim; the reference is checked for syntax only.
	ApprovedBy string `json:"approvedBy"`
	// Ref is a nonsecret reference to the approval, such as a message or journal id.
	Ref string `json:"ref"`
	// ScopeDigest is the Grant.ScopeDigest the session showed the approver. A
	// different digest is a conflict.
	ScopeDigest string `json:"scopeDigest"`
}

// GrantAttestation is a recorded attestation: the requesting session's claim,
// agent attestation, not verified human identity.
type GrantAttestation struct {
	Session    string    `json:"session"`
	ApprovedBy string    `json:"approvedBy"`
	Ref        string    `json:"ref"`
	AttestedAt time.Time `json:"attestedAt"`
}
