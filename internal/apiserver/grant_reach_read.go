package apiserver

import (
	"context"
	"errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/broker"
)

// policyDelegateGrantRead authorizes only the extra designated host read needed
// by S3 preflight. Existing tree reads remain unchanged. No policy failure ever
// falls through to reserved authority; S7 must add its explicit validated union.
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
	if _, err := broker.NewDelegateReach(ctx, s.Live, s.Policy.SessionNamespace, s.PolicyNamespace, g, actor, broker.DelegateRef(d.ref), s.GrantPolicyDelegates); err != nil {
		if errors.Is(err, broker.ErrRefused) {
			return forbidden("%s", err)
		}
		return fromKubeError(err, "verify designated grant read")
	}
	return nil
}
