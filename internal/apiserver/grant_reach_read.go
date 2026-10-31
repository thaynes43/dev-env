package apiserver

import (
	"context"
	"errors"
	"slices"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/broker"
)

// policyDelegateGrantRead authorizes only the extra designated host read needed
// by S3 preflight. Existing tree reads remain unchanged. S7 adds an explicit
// enabled-class validated union; policy failure never selects reserved authority.
func (s *Server) policyDelegateGrantRead(ctx context.Context, g *v1alpha1.AccessGrant, c *caller) error {
	d, err := s.delegateOf(ctx, c)
	if err != nil {
		return err
	}
	if err := s.delegateSeparation(ctx, g, d); err != nil {
		return err
	}
	actor := &broker.DelegateIdentity{Form: d.form, PodName: d.pod.Name, PodUID: string(d.pod.UID), HostID: d.pod.Annotations[CoordinatorHostAnnotation]}
	if d.session != nil {
		actor.Session, actor.SessionUID = d.session.Name, string(d.session.UID)
	}
	// Explicit S7 union, selected by enabled class and exact scope validation.
	// Never select reserved reach because an ordinary policy lookup failed.
	class := broker.ReservedClass(g)
	if class != "" && slices.Contains(s.ReservedDecisionClasses, class) {
		if err := broker.ValidateReservedRequestShape(g, s.ReservedDecisionClasses, s.ReservedExcludedNamespaces); err != nil {
			return forbidden("%s", err)
		}
		_, err = broker.NewDelegateReachIdentity(ctx, s.Live, s.Policy.SessionNamespace, g, actor, broker.DelegateRef(d.ref), s.GrantPolicyDelegates)
	} else {
		_, err = broker.NewDelegateReach(ctx, s.Live, s.Policy.SessionNamespace, s.PolicyNamespace, g, actor, broker.DelegateRef(d.ref), s.GrantPolicyDelegates)
	}
	if err != nil {
		if errors.Is(err, broker.ErrRefused) {
			return forbidden("%s", err)
		}
		return fromKubeError(err, "verify designated grant read")
	}
	return nil
}
