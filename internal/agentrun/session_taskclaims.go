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
// An unknown or empty observation cannot carry Observed/Exact or claim identifiers.
// Available items are returned as reported; grant show reconciles a positive binding.
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
		out.ClaimBinding = claimBindingWithoutMatch(claims.ClaimBinding, reason)
	}
	return out
}

// normalizeMaintenanceGrantClaims confirms grant-show and MCP grant_status metadata.
// Observed/Exact survives only when the explicit maintenance.taskClaim is canonical
// and exactly one item matches its name, UID, and project. The published session
// name and coordinator identity must agree with that item's owner. Items are not
// rewritten. This is not approval, permission, stop proof, or custody.
func normalizeMaintenanceGrantClaims(g apiv1.Grant) *apiv1.SessionTaskClaims {
	claims := sessionTaskClaimsOrUnknown(g.TaskClaims)
	if claims == nil || claims.ClaimBinding == nil {
		return claims
	}
	if !claimBindingAdvertisesExact(claims.ClaimBinding) {
		if claims.ClaimBinding.Name == "" && claims.ClaimBinding.UID == "" && claims.ClaimBinding.Project == "" {
			return claims
		}
		copied := *claims
		copied.ClaimBinding = &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: firstOf(claims.ClaimBinding.Reason, "Unavailable")}
		return &copied
	}
	if claims.State != "Available" {
		copied := *claims
		copied.ClaimBinding = &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: firstOf(claims.Reason, "Unavailable")}
		return &copied
	}
	reason, ok := confirmedExactGrantBinding(g, claims)
	if ok {
		return claims
	}
	copied := *claims
	copied.ClaimBinding = &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: reason}
	return &copied
}

func claimBindingAdvertisesExact(b *apiv1.MaintenanceClaimBinding) bool {
	return b != nil && (b.State == "Observed" || b.Reason == "Exact")
}

// claimBindingWithoutMatch drops a positive association from an unknown observation.
// A binding that was already Unknown and carried no claim identifiers is preserved.
func claimBindingWithoutMatch(b *apiv1.MaintenanceClaimBinding, reason string) *apiv1.MaintenanceClaimBinding {
	if b == nil {
		return nil
	}
	if b.State == "Observed" || b.Reason == "Exact" || b.Name != "" || b.UID != "" || b.Project != "" {
		return &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: reason}
	}
	return b
}

// confirmedExactGrantBinding reports whether a positive binding matches one
// requester-owned item. The reason is an existing observation reason, never Exact,
// when the advertisement is inconsistent. Namespace policy is not re-derived here.
func confirmedExactGrantBinding(g apiv1.Grant, claims *apiv1.SessionTaskClaims) (string, bool) {
	var ref *apiv1.MaintenanceTaskClaim
	if g.Maintenance != nil {
		ref = g.Maintenance.TaskClaim
	}
	if ref == nil {
		return "NotObserved", false
	}
	if !apiv1.ValidMaintenanceTaskClaim(ref.Name, ref.UID, ref.Project) {
		return "Invalid", false
	}
	coord := g.Maintenance.Coordinator
	if g.Session == "" || coord.Name != g.Session || coord.UID == "" {
		return "IdentityUnconfirmed", false
	}
	b := claims.ClaimBinding
	if b.State != "Observed" || b.Reason != "Exact" || b.Name != ref.Name || b.UID != ref.UID || b.Project != ref.Project {
		if b.Name == ref.Name && b.UID == ref.UID && b.Project != ref.Project {
			return "ProjectMismatch", false
		}
		return "Stale", false
	}
	matched := 0
	partial := false
	projectPartial := false
	var owner apiv1.TaskClaimOwner
	for _, item := range claims.Items {
		nameMatch := item.Name == ref.Name
		uidMatch := item.UID == ref.UID
		if nameMatch && uidMatch && item.Identity.Project == ref.Project {
			matched++
			owner = item.Owner
			continue
		}
		if nameMatch && uidMatch {
			projectPartial = true
			continue
		}
		if nameMatch || uidMatch {
			partial = true
		}
	}
	switch matched {
	case 1:
		if owner.Name != g.Session || owner.UID != coord.UID {
			return "OwnerMismatch", false
		}
		return "Exact", true
	case 0:
		if projectPartial {
			return "ProjectMismatch", false
		}
		if partial {
			return "Stale", false
		}
		return "NotObserved", false
	default:
		return "Ambiguous", false
	}
}
