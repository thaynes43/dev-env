package grantdecision

import (
	"encoding/json"
	"strings"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// HandoffAnnotation carries an operator-authenticated decision to the broker.
// Admission must restrict changes to this key to the operator and broker. The
// value is metadata only; it never contains credential material.
const HandoffAnnotation = "dev-env.haynesops.com/grant-decision"

// ManualApproverAnnotation marks a GrantPolicy as a delegated decision scope,
// not a standing approval. Its value is the exact coordinator or parent-session
// principal, including the current session and pod UIDs for a parent session.
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
	RelayRef  string  `json:"relayRef,omitempty"`
}

// PodUIDLabel is the reserved pod label used only by manual egress grants.
const PodUIDLabel = "dev-env.haynesops.com/grant-pod-uid"

// ApprovedHandoff returns the recorded handoff for a manual decision. Release
// changes spec, so revocation needs the recorded UID even after the digest no
// longer matches. The annotation itself is admission-protected.
func ApprovedHandoff(g *v1alpha1.AccessGrant) (Handoff, bool) {
	var h Handoff
	if g == nil || g.Status.ApprovedBy == "" {
		return h, false
	}
	raw := g.Annotations[HandoffAnnotation]
	if len(raw) == 0 || len(raw) > 4096 || json.Unmarshal([]byte(raw), &h) != nil {
		return h, false
	}
	if h.Decision != "approve" || h.Principal != g.Status.ApprovedBy || h.Binding.GrantUID != g.UID || h.Binding.SessionUID != g.Spec.Requester.SessionUID || h.Binding.PodUID == "" || (!strings.HasPrefix(h.Principal, "human/") && !strings.HasPrefix(h.Principal, "coordinator/") && !strings.HasPrefix(h.Principal, "session/")) {
		return h, false
	}
	return h, true
}

// ValidRelayRef accepts a bounded reference only; it is audit metadata, not
// proof that a human answered.
func ValidRelayRef(s string) bool {
	if len(s) == 0 || len(s) > 160 {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && !strings.ContainsRune("-._:/", c) {
			return false
		}
	}
	return true
}
