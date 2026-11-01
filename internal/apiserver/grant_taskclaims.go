package apiserver

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// grantTaskClaims joins only the immutable authenticated requester UID. The
// coordinator, workers, grant TTL and free-form audit text grant no custody.
// An explicit envelope taskClaim is compared with that same owned observation.
// It is not permission, approval, stop proof or grant custody.
func (s *Server) grantTaskClaims(ctx context.Context, g *v1alpha1.AccessGrant, c *caller) *apiv1.SessionTaskClaims {
	claims, sess := s.requesterTaskClaims(ctx, g, c)
	if claims == nil {
		claims = &apiv1.SessionTaskClaims{State: "Unknown", Reason: "Unavailable", Items: []apiv1.TaskClaim{}}
	}
	if g == nil || g.Spec.Maintenance == nil || g.Spec.Maintenance.TaskClaim == nil {
		return claims
	}
	claims.ClaimBinding = maintenanceClaimBinding(g, sess, s.Policy.SessionNamespace, claims)
	return claims
}

func (s *Server) requesterTaskClaims(ctx context.Context, g *v1alpha1.AccessGrant, c *caller) (*apiv1.SessionTaskClaims, *v1alpha1.AgentSession) {
	unknown := func(reason string) (*apiv1.SessionTaskClaims, *v1alpha1.AgentSession) {
		return &apiv1.SessionTaskClaims{State: "Unknown", Reason: reason, Items: []apiv1.TaskClaim{}}, nil
	}
	if s.claimsAvailable() != nil {
		return unknown("Unconfigured")
	}
	if g == nil {
		return unknown("IdentityUnconfirmed")
	}
	requester := g.Spec.Requester
	if requester.Session == "" || requester.SessionUID == "" {
		return unknown("IdentityUnconfirmed")
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: requester.Session}, &sess); err != nil {
		if apierrors.IsNotFound(err) {
			return unknown("IdentityUnconfirmed")
		}
		return unknown("Unavailable")
	}
	if sess.UID != requester.SessionUID {
		return unknown("IdentityUnconfirmed")
	}
	// Reuse the session's saved project snapshot and the independent caller ACL,
	// then one bounded uncached ListOwned. No owner Get per claim or TTL inference.
	return s.sessionTaskClaims(ctx, &sess, c), &sess
}

// maintenanceClaimBinding matches one explicit name, UID and project. Text,
// reported HEAD and list order never select a claim. More than one exact match
// stays Unknown.
func maintenanceClaimBinding(g *v1alpha1.AccessGrant, sess *v1alpha1.AgentSession, namespace string, obs *apiv1.SessionTaskClaims) *apiv1.MaintenanceClaimBinding {
	unknown := func(reason string) *apiv1.MaintenanceClaimBinding {
		return &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: reason}
	}
	ref := g.Spec.Maintenance.TaskClaim
	if !apiv1.ValidMaintenanceTaskClaim(ref.Name, ref.UID, ref.Project) {
		return unknown("Invalid")
	}
	if obs == nil || obs.State != "Available" {
		reason := "Unavailable"
		if obs != nil && obs.Reason != "" {
			reason = obs.Reason
		}
		return unknown(reason)
	}
	if sess == nil {
		return unknown("IdentityUnconfirmed")
	}
	snapshot, err := claimSnapshot(sess)
	if err != nil {
		return unknown("ScopeUnconfirmed")
	}
	if ref.Project != snapshot.Project() {
		return unknown("ProjectMismatch")
	}
	matched := make([]apiv1.TaskClaim, 0, 1)
	partial := false
	for _, item := range obs.Items {
		nameMatch := item.Name == ref.Name
		uidMatch := item.UID == ref.UID
		if nameMatch && uidMatch && item.Identity.Project == ref.Project {
			matched = append(matched, item)
			continue
		}
		if nameMatch || uidMatch {
			partial = true
		}
	}
	switch len(matched) {
	case 0:
		if partial {
			return unknown("Stale")
		}
		return unknown("NotObserved")
	case 1:
		item := matched[0]
		req := g.Spec.Requester
		if namespace == "" || item.Owner.Namespace != namespace || item.Owner.Name != req.Session || item.Owner.UID != string(req.SessionUID) {
			return unknown("OwnerMismatch")
		}
		return &apiv1.MaintenanceClaimBinding{State: "Observed", Reason: "Exact", Name: item.Name, UID: item.UID, Project: item.Identity.Project}
	default:
		return unknown("Ambiguous")
	}
}
