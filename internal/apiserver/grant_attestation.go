package apiserver

import (
	"context"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/grantattest"
)

// attestGrant serves POST /v1/grants/{name}/attestation (default off). The grant's
// requesting session records its claim that it obtained the user's or a
// coordinator's approval for exactly this grant. The claim is agent attestation,
// not verified human identity. The attestor (session, session UID, pod UID) comes
// from the caller's token; the body carries only the claimed approver, a nonsecret
// reference and the scope digest the session showed. The route writes one
// annotation and never a status: the broker decides, and only through a policy
// that names attestation.
func (s *Server) attestGrant(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	if c.kind != kindSession {
		return 0, nil, forbidden("only the grant's requesting session records an attestation; %s is not a session", c)
	}
	if !c.session.DeletionTimestamp.IsZero() {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "session %s is being reaped", c.parent)
	}
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.AttestGrantRequest
	if err := decodeJSON(w, r, 4<<10, &req, true); err != nil {
		return 0, nil, err
	}
	approver, err := grantattest.ParseApprover(req.ApprovedBy)
	if err != nil {
		return 0, nil, invalid(fieldError("approvedBy", "%s", err))
	}
	if !grantattest.ValidRef(req.Ref) {
		return 0, nil, invalid(fieldError("ref", "a nonsecret reference of up to 128 letters, digits and . _ : / # -"))
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	if g.Spec.Kube != nil && (g.Spec.Kube.Reserved || g.Spec.Kube.AllNamespaces || len(g.Spec.Kube.StorageTargets) > 0) {
		return 0, nil, forbidden("reserved scope cannot use requester attestation")
	}
	if g.Spec.Requester.SessionUID == "" || !grantRequestedBy(&g, c.parent, c.session.UID) {
		return 0, nil, forbidden("grant %s belongs to another requesting session; a session attests only its own grants", g.Name)
	}
	now := s.now()
	if !g.DeletionTimestamp.IsZero() || g.Spec.Release || (g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending) || !now.Before(g.CreationTimestamp.Add(grantattest.PendingWindow)) {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "grant %s is no longer waiting for an answer", g.Name)
	}
	digest := grantattest.ScopeDigest(&g)
	if req.ScopeDigest != digest {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "scopeDigest does not match the grant's scope and TTL; read the grant again and attest what you showed the approver")
	}
	st := grantattest.Statement{
		V:        grantattest.Version,
		Attestor: grantattest.Attestor{Session: c.session.Name, SessionUID: string(c.session.UID), PodUID: string(c.pod.UID)},
		Approver: approver, Ref: req.Ref, GrantUID: string(g.UID), ScopeDigest: digest,
		TTL: g.Spec.TTL.Duration.String(), AttestedAt: now.UTC().Truncate(time.Second),
	}
	if raw := g.Annotations[grantattest.Annotation]; raw != "" {
		// One statement per grant. The same claim again is an idempotent retry.
		if prior, err := grantattest.Parse(raw); err == nil && grantattest.SameClaim(prior, st) {
			return http.StatusOK, s.grantView(&g), nil
		}
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "grant %s already carries an attestation; there is one per grant", g.Name)
	}
	value, err := st.Encode()
	if err != nil {
		return 0, nil, internal("encode the attestation")
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[grantattest.Annotation] = value
	if err := s.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	s.Log.Info("grant attested (agent attestation, not verified human identity)", "grant", g.Name, "caller", c.String(),
		"approvedBy", approver.String(), "ref", req.Ref, "type", g.Spec.Type)
	return http.StatusCreated, s.grantView(&g), nil
}
