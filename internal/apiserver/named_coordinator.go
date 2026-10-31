package apiserver

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The launch rules of a named coordinator identity (DESIGN-042 6.4, S4). A
// profile that names dev-env-coordinator-<name> runs its sessions as that account,
// and only these rules decide which sessions those are:
//
//   - only Tom, a client or the workbench launches one; a session, a coordinator
//     host or the portal does not, so no session launches its own delegate;
//   - a child of a session on such a profile runs on the default profile, which may
//     not name one (templates.Validate), so the identity is never inherited;
//   - at most one session at a time holds each account.
//
// The API records an admitted launch with templates.CoordinatorIdentityLabel, and
// the controller runs a session as the account only with that record.

// ValidateNamedCoordinatorPolicy rejects a policy whose human or client
// ServiceAccount is a named coordinator identity: such an account is a session's,
// and a client entry would make its session pods clients.
func ValidateNamedCoordinatorPolicy(p Policy) error {
	if !p.NamedCoordinatorIdentities {
		return nil
	}
	shaped := func(ref string) bool {
		ns, name, ok := strings.Cut(ref, "/")
		return ok && ns == p.SessionNamespace && templates.IsCoordinatorServiceAccount(name)
	}
	if shaped(p.Human) || slices.ContainsFunc(p.Clients, shaped) {
		return fmt.Errorf("a named coordinator identity (%s<name> in %s) is a session's account, never a human's or a client's",
			templates.CoordinatorServiceAccountPrefix, p.SessionNamespace)
	}
	return nil
}

// profileIdentity returns the named coordinator identity profile runs its
// sessions as, or "" when it names none or the templates cannot say. An empty
// profile is the default one.
func (s *Server) profileIdentity(ctx context.Context, profile string) string {
	if !s.Policy.NamedCoordinatorIdentities || s.Templates == nil {
		return ""
	}
	t, err := s.Templates(ctx)
	if err != nil {
		return ""
	}
	_, p, err := t.Profile(profile)
	if err != nil || !templates.IsCoordinatorServiceAccount(p.ServiceAccount) {
		return ""
	}
	return p.ServiceAccount
}

// mayLaunchNamedCoordinator reports whether c may launch a session that runs as
// a named coordinator identity: Tom, a trusted client, or a workbench session,
// which is the workbench (as for a Codex host). Never an ordinary session, a
// named coordinator session, a coordinator host or the portal.
func (s *Server) mayLaunchNamedCoordinator(c *caller) bool {
	return c.kind == kindHuman || c.kind == kindClient || s.isWorkbenchSession(c)
}

// admitNamedCoordinator applies the one-holder rule to a session newSession
// labelled. Each create path calls it under the create lock, after an
// idempotent repeat has returned its session.
func (s *Server) admitNamedCoordinator(ctx context.Context, sess *v1alpha1.AgentSession) error {
	if account := sess.Labels[templates.CoordinatorIdentityLabel]; account != "" {
		return s.namedCoordinatorFree(ctx, account, sess.UID)
	}
	return nil
}

// namedCoordinatorFree refuses a launch or resume while another session holds
// the account: at most one session runs as each named coordinator identity. A
// session holds it from its admission until it is Suspended, Archived or Failed,
// whatever its operating mode says, because a pod may still run while a suspend
// or a reap rescues it. The read is live, never cached. Two launches that race
// each other through two API replicas can both pass; the delegate decision route
// (S2) must recheck that one session holds the account before it trusts it.
func (s *Server) namedCoordinatorFree(ctx context.Context, account string, self types.UID) error {
	if s.Live == nil {
		return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable, "the sessions holding %s cannot be read", account)
	}
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace),
		client.MatchingLabels{templates.CoordinatorIdentityLabel: account}); err != nil {
		return fromKubeError(err, "the sessions holding "+account)
	}
	for i := range list.Items {
		o := &list.Items[i]
		if self != "" && o.UID == self {
			continue
		}
		switch o.Status.Phase {
		case v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed:
			continue
		}
		return newError(http.StatusConflict, apiv1.CodeConflict,
			"session %s already runs as %s/%s; one session at a time may (DESIGN-042 6.4)", o.Name, s.Policy.SessionNamespace, account)
	}
	return nil
}
