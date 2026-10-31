package apiserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// This branch is selected ONLY by reserved:true, never by a failed policy
// lookup. The actor was authenticated by the same delegateOf as ordinary S2.
func (s *Server) decideReservedGrant(ctx context.Context, _ http.ResponseWriter, _ *http.Request, c *caller, d *grantDelegate, g *v1alpha1.AccessGrant, req apiv1.GrantDecisionRequest) (int, any, error) {
	if !grantattest.ValidRef(req.TomRef) {
		return 0, nil, invalid(fieldError("tomRef", "required nonsecret reference to a fresh Tom message; agent assertion, not verified human consent"))
	}
	if err := broker.ValidateReservedRequestShape(g, s.ReservedDecisionClasses, s.ReservedExcludedNamespaces); err != nil {
		return 0, nil, forbidden("%s", err)
	}
	if err := s.delegateSeparation(ctx, g, d); err != nil {
		return 0, nil, err
	}
	if req.GrantUID != string(g.UID) || req.Digest != grantattest.ScopeDigest(g) || !g.DeletionTimestamp.IsZero() || g.Spec.Release ||
		(g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending) || !s.now().Before(g.CreationTimestamp.Add(broker.PendingTimeout)) || g.Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "stale, replayed or mismatched reserved decision; read the grant")
	}
	ttl := g.Spec.TTL.Duration
	if req.TTL != "" {
		parsed, err := time.ParseDuration(req.TTL)
		if err != nil || parsed < broker.MinTTL || parsed > ttl || parsed > time.Hour {
			return 0, nil, invalid(fieldError("ttl", "reserved TTL is 10m to the requested TTL, maximum 1h"))
		}
		ttl = parsed
	}
	rec := broker.MaintenanceDecision{GrantUID: string(g.UID), Digest: req.Digest, By: broker.DelegateRef(d.ref), Approve: req.Approve, TTL: ttl, At: s.now().UTC().Truncate(time.Second), Ref: req.Ref, Reason: req.Reason,
		Delegate: &broker.DelegateIdentity{Form: d.form, PodName: d.pod.Name, PodUID: string(d.pod.UID)}}
	if d.form == 1 {
		rec.Delegate.HostID = d.pod.Annotations[CoordinatorHostAnnotation]
	}
	if d.form == 3 {
		rec.Delegate.ServiceAccountUID = c.identity.ServiceAccountUID
	}
	if d.session != nil {
		rec.Delegate.Session = d.session.Name
		rec.Delegate.SessionUID = string(d.session.UID)
	}
	binding, err := broker.NewReservedBinding(ctx, s.Live, s.Policy.SessionNamespace, g, rec, s.ReservedDecisionClasses, s.ReservedExcludedNamespaces, req.TomRef)
	if err != nil {
		if errors.Is(err, broker.ErrRefused) {
			return 0, nil, forbidden("%s", err)
		}
		return 0, nil, fromKubeError(err, "read reserved decision scope")
	}
	rec.Reserved = binding
	rec.Reach, err = broker.NewReservedDelegateReach(ctx, s.Live, s.Policy.SessionNamespace, g, rec.Delegate, rec.By, s.GrantPolicyDelegates, broker.ReservedAuthority(binding))
	if err != nil {
		if errors.Is(err, broker.ErrRefused) {
			return 0, nil, forbidden("%s", err)
		}
		return 0, nil, fromKubeError(err, "verify reserved actor reach")
	}
	if rec.Reach.RequesterPodUID != binding.RequesterPodUID {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "reserved requester pod changed during decision; read the grant")
	}
	return s.recordDelegateDecision(ctx, c, g, rec)
}
