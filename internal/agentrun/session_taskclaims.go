package agentrun

import (
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func printSessionTaskClaims(claims *apiv1.SessionTaskClaims, row func(string, string, ...any)) {
	claims = sessionTaskClaimsOrUnknown(claims)
	if claims.ClaimBinding != nil {
		row("Task claim association", "%s (%s); explicit metadata, not approval, permission, stop proof, or custody", claims.ClaimBinding.State, claims.ClaimBinding.Reason)
		if claims.ClaimBinding.State == "Observed" {
			row("  associated claim", "%s (UID %s) project %s", claims.ClaimBinding.Name, claims.ClaimBinding.UID, claims.ClaimBinding.Project)
		}
	}
	if claims.State != "Available" {
		row("Task claims", "Unknown (%s)", claims.Reason)
		return
	}
	row("Task claims", "Available (%d); identity is not approval or transfer permission", len(claims.Items))
	for _, claim := range claims.Items {
		row("  identity", "%s/%s/%s", claim.Identity.Project, claim.Identity.Repository, claim.Identity.TaskKey)
		row("  claim", "%s (UID %s)", claim.Name, claim.UID)
		row("  created", "%s", claim.CreatedAt.UTC().Format(time.RFC3339Nano))
		row("  actor", "%s %s, pod %s (UID %s)", claim.Actor.Kind, claim.Actor.ServiceAccount, claim.Actor.PodName, claim.Actor.PodUID)
		row("  reported head", "%s", firstOf(claim.Provenance.ReportedHead, "Unknown"))
		if claim.Provenance.ReportedAt != nil {
			row("  reported at", "%s", claim.Provenance.ReportedAt.UTC().Format(time.RFC3339Nano))
		}
		row("  owner", "%s/%s (UID %s), observed %s, stop %s", claim.Owner.Namespace, claim.Owner.Name, claim.Owner.UID, claim.OwnerState, claim.StopState)
	}
}

// Normal show JSON and text agree on omitted observations from older operators.
// show --json --once deliberately keeps the original wire bytes unchanged.
func sessionTaskClaimsOrUnknown(claims *apiv1.SessionTaskClaims) *apiv1.SessionTaskClaims {
	if claims != nil && claims.State == "Available" && len(claims.Items) > 0 {
		return claims
	}
	reason := "NotReported"
	if claims != nil {
		reason = firstOf(claims.Reason, "Unavailable")
		if claims.State == "Available" {
			reason = "NoJoinedClaims"
		}
	}
	out := &apiv1.SessionTaskClaims{State: "Unknown", Reason: reason, Items: []apiv1.TaskClaim{}}
	if claims != nil {
		out.ClaimBinding = claims.ClaimBinding
	}
	return out
}
