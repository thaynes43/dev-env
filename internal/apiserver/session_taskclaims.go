package apiserver

import (
	"context"
	"net/http"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// sessionDetail adds the claim view only after the existing Session ACL and
// only on negotiated detail reads. Lists and mutation responses do no claim IO.
func (s *Server) sessionDetail(ctx context.Context, sess *v1alpha1.AgentSession, c *caller, r *http.Request, capture bool) apiv1.Session {
	v := s.withUsageRollup(ctx, sess, c, capture)
	if r.Header.Get(apiv1.SessionTaskClaimsHeader) == apiv1.SessionTaskClaimsV1 {
		v.TaskClaims = s.sessionTaskClaims(ctx, sess, c)
	}
	return v
}

func (s *Server) sessionTaskClaims(ctx context.Context, sess *v1alpha1.AgentSession, c *caller) *apiv1.SessionTaskClaims {
	unknown := func(reason string) *apiv1.SessionTaskClaims {
		return &apiv1.SessionTaskClaims{State: "Unknown", Reason: reason, Items: []apiv1.TaskClaim{}}
	}
	if s.claimsAvailable() != nil {
		return unknown("Unconfigured")
	}
	if sess == nil || sess.UID == "" {
		return unknown("IdentityUnconfirmed")
	}
	snapshot, err := claimSnapshot(sess)
	if err != nil || s.claimReadScope(ctx, c, snapshot.Project()) != nil {
		// Broad legacy Session read rights never grant claim metadata access.
		return unknown("ScopeUnconfirmed")
	}
	claims, err := s.TaskClaims.ListOwned(ctx, snapshot.Project(), string(sess.UID))
	if err != nil {
		return unknown("Unavailable")
	}
	if len(claims) == 0 {
		return unknown("NoJoinedClaims")
	}
	items := make([]apiv1.TaskClaim, 0, len(claims))
	for i := range claims {
		v := claimMetadata(&claims[i])
		// Use the already-authorized Session observation, not one Get per claim.
		// Neither phase nor deletion is a stop receipt or transfer permission.
		if !sess.DeletionTimestamp.IsZero() {
			v.OwnerState = "Deleting"
		} else if sess.Status.Phase != "" {
			v.OwnerState = string(sess.Status.Phase)
		}
		items = append(items, v)
	}
	return &apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: items}
}
