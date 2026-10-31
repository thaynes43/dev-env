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
func (s *Server) grantTaskClaims(ctx context.Context, g *v1alpha1.AccessGrant, c *caller) *apiv1.SessionTaskClaims {
	unknown := func(reason string) *apiv1.SessionTaskClaims {
		return &apiv1.SessionTaskClaims{State: "Unknown", Reason: reason, Items: []apiv1.TaskClaim{}}
	}
	if s.claimsAvailable() != nil {
		return unknown("Unconfigured")
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
	return s.sessionTaskClaims(ctx, &sess, c)
}
