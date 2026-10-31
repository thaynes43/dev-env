package apiserver

// Delegate decisions (DESIGN-042 6.2, mode (b)). An allowlisted delegate
// identity, authenticated by TokenReview and bound to a live pod, decides
// another agent's grant. The route records the decision in the grant's
// set-once decision annotation, as the maintenance human route does, and never
// writes a status: the broker applies it, and only through a GrantPolicy whose
// spec.delegated covers the grant. A delegate decision is not a human decision.
//
// Two delegate forms, both on --grant-delegates:
//
//  1. a configured coordinator host, verified by resolveCoordinator (its own
//     ServiceAccount, its configured pod name, a live pod UID and the
//     coordinator-host annotation);
//  2. a session pod running as an allowlisted ServiceAccount, controlled by a
//     Running AgentSession that S4's launch rules admitted
//     (Policy.NamedCoordinatorSessionEligible).
//
// Reach is the delegate's own tree (lead ruling on #786): a coordinator host
// decides only for its own child sessions, as childForCoordinator defines them,
// and a delegate session only for its strict descendants. Its own grants, an
// ancestor's, a sibling's, a cousin's and an unrelated session's are refused.
// A top-level session uses mode (a), attestation: its delegate is Tom, by
// message. spec.delegated still caps the types and the scope.
//
// Form 2 uses S4's named coordinator caller classification and exported
// launch-eligibility rule, plus a live check that this session is the sole holder.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
	"github.com/thaynes43/dev-env/internal/templates"
)

// maxDecisionReason bounds GrantDecisionRequest.Reason, in characters.
const maxDecisionReason = 256

// maxDelegateAncestry bounds the walk up a delegate session's parents. Sessions
// sit at most two levels deep (maxSessionDepth), so a longer chain is refused
// rather than followed.
const maxDelegateAncestry = maxSessionDepth + 2

// ParseGrantDelegates parses the operator's --grant-delegates (DESIGN-042 6.2)
// with the broker's parser, and refuses every identity the API already serves
// in another class: Tom, a client, the shared session account, the workbench
// and the portal reader.
func ParseGrantDelegates(raw string, p Policy) ([]string, error) {
	refused := append([]string{p.Human, portalReaderRef}, p.Clients...)
	if p.SessionServiceAccount != "" {
		refused = append(refused, p.SessionNamespace+"/"+p.SessionServiceAccount)
	}
	if p.WorkbenchServiceAccount != "" {
		refused = append(refused, p.SessionNamespace+"/"+p.WorkbenchServiceAccount)
	}
	return broker.ParseGrantDelegates(raw, refused...)
}

// grantDelegate is an authenticated delegate: its ServiceAccount on the list,
// its live pod, and the AgentSession controlling that pod, when one does.
type grantDelegate struct {
	ref     string
	form    int
	pod     *corev1.Pod
	session *v1alpha1.AgentSession
}

// delegateOf returns the delegate caller c is, or refuses it. The class comes
// from resolveCaller; the list from GrantDelegates; nothing from the body.
//
// Identity classification is unchanged: resolveCaller still serves a session
// in any phase on its ordinary routes. Only this route then asks whether the
// delegate is live and eligible now, from fresh API server reads, never the
// cache: its pod is the token's pod UID, Running and not terminating, and
// still bound as it was classified (a coordinator host's name, account and
// host annotation; a session pod's account and controlling session); and
// any session that controls the pod is Running, not suspended or asked to
// suspend, not draining, archived or being reaped.
func (s *Server) delegateOf(ctx context.Context, c *caller) (*grantDelegate, error) {
	ref := c.identity.serviceAccountRef()
	if ref == "" || !slices.Contains(s.GrantDelegates, ref) {
		return nil, forbidden("%s is not a grant delegate (--grant-delegates)", c)
	}
	if s.Live == nil {
		return nil, forbidden("delegate decisions need live reads")
	}
	form := 0
	switch c.kind {
	case kindCoordinator:
		form = 1
	case kindSession:
		form = 2
	default:
		return nil, forbidden("%s is not a grant delegate: a delegate is a coordinator host or a session pod", c)
	}
	id := c.identity
	var pod corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.PodName}, &pod); err != nil {
		return nil, forbidden("the delegate's pod cannot be read live")
	}
	if id.PodUID == "" || string(pod.UID) != id.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Status.Phase != corev1.PodRunning ||
		pod.Spec.ServiceAccountName != id.ServiceAccount {
		return nil, forbidden("a delegate decides from its own live, Running pod, never a replaced or terminating one")
	}
	if form == 1 {
		// Form 1: the host binding, as resolveCoordinator checks it.
		bound := false
		for _, h := range s.Policy.Coordinators {
			if h.ServiceAccount == ref && h.PodName == pod.Name && pod.Annotations[CoordinatorHostAnnotation] == h.HostID {
				bound = true
			}
		}
		if !s.Policy.CoordinatorEnabled || !bound {
			return nil, forbidden("the delegate's coordinator host binding changed")
		}
	}
	sess, err := s.podSession(ctx, &pod)
	if err != nil {
		return nil, err
	}
	if form == 2 && (sess == nil || c.session == nil || sess.UID != c.session.UID) {
		// Form 2 (S4): the pod is still controlled by the session it was
		// classified as.
		return nil, forbidden("a delegate session decides from its own pod, controlled by its own session")
	}
	if sess != nil {
		mode := sess.Spec.OperatingMode
		if !sess.DeletionTimestamp.IsZero() || sess.Status.Phase != v1alpha1.PhaseRunning || sess.Status.ArchivedAt != nil ||
			(mode != "" && mode != v1alpha1.OperatingModeRunning) {
			return nil, forbidden("delegate session %s is not Running and eligible: suspended, draining, archived or being reaped", sess.Name)
		}
	}
	return &grantDelegate{ref: ref, form: form, pod: &pod, session: sess}, nil
}

// podSession returns the AgentSession that controls pod, read live, or nil when
// no session does. A controller that cannot be read is not guessed at.
func (s *Server) podSession(ctx context.Context, pod *corev1.Pod) (*v1alpha1.AgentSession, error) {
	owner := metav1.GetControllerOf(pod)
	if owner == nil || owner.Kind != "AgentSession" {
		return nil, nil
	}
	var sess v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: owner.Name}, &sess); err != nil || sess.UID != owner.UID ||
		owner.APIVersion != v1alpha1.GroupVersion.String() {
		return nil, forbidden("the delegate's pod belongs to a session that cannot be verified")
	}
	return &sess, nil
}

// delegateSeparation refuses a delegate that is the requester or speaks for
// it (DESIGN-042 6.2): the requesting session, a pod it controls, its current
// pod, or a requester running as the delegate's own account; a coordinator or
// worker the maintenance envelope names; any requester outside the delegate's
// own tree (delegateReach); and, for a delegate session, one S4's launch
// rules did not admit (Policy.NamedCoordinatorSessionEligible).
func (s *Server) delegateSeparation(ctx context.Context, g *v1alpha1.AccessGrant, d *grantDelegate) error {
	rq := g.Spec.Requester
	if owner := metav1.GetControllerOf(d.pod); owner != nil && owner.Kind == "AgentSession" &&
		(owner.Name == rq.Session || owner.UID == rq.SessionUID) {
		return forbidden("a delegate never decides its own session's grant")
	}
	if d.session != nil && (d.session.Name == rq.Session || d.session.UID == rq.SessionUID) {
		return forbidden("a delegate never decides its own session's grant")
	}
	var rp corev1.Pod
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: rq.Session}, &rp); err == nil {
		if rp.UID == d.pod.UID {
			return forbidden("a delegate never decides its own session's grant")
		}
		// Refuse same-account decisions independently of S4's sole-holder
		// check, including a cross-replica launch race.
		if rp.Namespace+"/"+rp.Spec.ServiceAccountName == d.ref {
			return forbidden("a delegate never decides a grant whose requester runs as its own delegate account")
		}
	} else if !apierrors.IsNotFound(err) {
		return fromKubeError(err, "the requesting session's pod")
	}
	if m := g.Spec.Maintenance; m != nil {
		for _, id := range append([]v1alpha1.MaintenanceIdentity{m.Coordinator}, m.Workers...) {
			if id.PodUID == string(d.pod.UID) || (d.session != nil && (id.Name == d.session.Name || id.UID == string(d.session.UID))) {
				return forbidden("a coordinator or worker named in the maintenance envelope never decides it")
			}
		}
	}
	if err := s.delegateReach(ctx, g, d); err != nil {
		return err
	}
	if d.form == 2 {
		if !s.Policy.NamedCoordinatorSessionEligible(d.session) || d.session.Labels[templates.CoordinatorIdentityLabel] != d.pod.Spec.ServiceAccountName {
			return forbidden("delegate session %s is not launch-eligible", d.session.Name)
		}
		if err := s.namedCoordinatorFree(ctx, d.pod.Spec.ServiceAccountName, d.session.UID); err != nil {
			return forbidden("delegate session %s is not the account's sole holder: %s", d.session.Name, err)
		}
	}
	return nil
}

// delegateReach refuses a grant outside the delegate's own tree (lead ruling on
// #786). A coordinator host reaches only its own child sessions, the sessions
// whose parent is its ServiceAccount (childForCoordinator). A delegate session
// reaches only its strict descendants: the requester's chain of session parents,
// read live, must reach the delegate's session by name and UID within the depth
// limit. So its own grants, an ancestor's, a sibling's, a cousin's and an
// unrelated session's are refused; a top-level session's delegate is Tom.
func (s *Server) delegateReach(ctx context.Context, g *v1alpha1.AccessGrant, d *grantDelegate) error {
	rq := g.Spec.Requester
	const outside = "a delegate decides only for its own descendants: this requester is not one"
	var r v1alpha1.AgentSession
	if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: rq.Session}, &r); err != nil || r.UID != rq.SessionUID {
		return forbidden("the requesting session cannot be verified")
	}
	if d.form == 1 {
		if r.Spec.Parent != d.ref {
			return forbidden("%s", outside)
		}
		return nil
	}
	if d.session == nil {
		return forbidden("%s", outside)
	}
	parent := r.Spec.Parent
	for i := 0; parent != "" && !strings.Contains(parent, "/"); i++ {
		if i >= maxDelegateAncestry {
			return forbidden("the requesting session's ancestry is deeper than sessions may sit")
		}
		var a v1alpha1.AgentSession
		if err := s.Live.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: parent}, &a); err != nil {
			return forbidden("the requesting session's ancestry cannot be verified")
		}
		if a.Name == d.session.Name && a.UID == d.session.UID {
			return nil
		}
		parent = a.Spec.Parent
	}
	return forbidden("%s", outside)
}

// validDecisionReason: at most maxDecisionReason characters of valid UTF-8,
// no control or format characters (so no bidirectional overrides in the
// status message, the Event or the CLI).
func validDecisionReason(r string) bool {
	return utf8.ValidString(r) && utf8.RuneCountInString(r) <= maxDecisionReason &&
		!strings.ContainsFunc(r, func(c rune) bool { return unicode.In(c, unicode.Cc, unicode.Cf) })
}

// decideGrant serves POST /v1/grants/{name}/decision (DESIGN-042 6.2), default
// off: registered only when GrantDelegates names a delegate. It answers with
// the Grant and 202 Accepted, or the maintenance decision route's refusals.
func (s *Server) decideGrant(ctx context.Context, w http.ResponseWriter, r *http.Request, c *caller) (int, any, error) {
	d, err := s.delegateOf(ctx, c)
	if err != nil {
		return 0, nil, err
	}
	var req apiv1.GrantDecisionRequest
	if err := decodeJSON(w, r, 4096, &req, true); err != nil {
		return 0, nil, err
	}
	if !grantattest.ValidRef(req.Ref) {
		return 0, nil, invalid(fieldError("ref", "required: a nonsecret reference of up to 128 letters, digits and . _ : / # -"))
	}
	if !validDecisionReason(req.Reason) {
		return 0, nil, invalid(fieldError("reason", "at most %d characters, no control characters", maxDecisionReason))
	}
	key, err := s.grantKey(r)
	if err != nil {
		return 0, nil, err
	}
	var g v1alpha1.AccessGrant
	if err := s.Live.Get(ctx, key, &g); err != nil {
		return 0, nil, fromGrantKubeError(err, "grant "+key.Name)
	}
	switch g.Spec.Type {
	case v1alpha1.GrantKube:
		if g.Spec.Kube == nil || broker.DelegateRefusedRole(g.Spec.Kube.Role) {
			return 0, nil, forbidden("a delegate never decides role %s: breakglass, secrets-read and storage stay Tom's decision", grantRole(&g))
		}
	case v1alpha1.GrantEgress:
	case v1alpha1.GrantMaintenance:
		if !s.MaintenanceEnabled {
			return 0, nil, forbidden("maintenance decisions need the maintenance API (--enable-maintenance-api)")
		}
		if err := broker.ValidateMaintenance(&g); err != nil {
			return 0, nil, forbidden("%s", err)
		}
	default:
		return 0, nil, forbidden("a delegate never decides a %s grant", g.Spec.Type)
	}
	if g.Spec.Requester.SessionUID == "" {
		return 0, nil, forbidden("grant %s has no authenticated requesting session UID", g.Name)
	}
	if err := s.delegateSeparation(ctx, &g, d); err != nil {
		return 0, nil, err
	}
	if req.GrantUID != string(g.UID) || req.Digest != grantattest.ScopeDigest(&g) || !g.DeletionTimestamp.IsZero() || g.Spec.Release ||
		(g.Status.Phase != "" && g.Status.Phase != v1alpha1.GrantPending) || !s.now().Before(g.CreationTimestamp.Add(broker.PendingTimeout)) ||
		g.Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "stale, replayed, mismatched or already decided request: read the grant again and decide what you reviewed")
	}
	ttl := g.Spec.TTL.Duration
	if req.TTL != "" {
		ttl, err = time.ParseDuration(req.TTL)
		if err != nil || ttl < broker.MinTTL || ttl > g.Spec.TTL.Duration {
			return 0, nil, invalid(fieldError("ttl", "approve only the requested TTL or less, at least 10m"))
		}
	}
	if req.Approve && g.Spec.Type == v1alpha1.GrantMaintenance {
		if err := broker.CheckMaintenanceIdentities(ctx, s.Live, s.Policy.SessionNamespace, &g); err != nil {
			return 0, nil, forbidden("%s", err)
		}
	}
	// Both approvals and denials consume the shared decision slot. Read live:
	// an uncovered delegate must leave it available for a legitimate decision.
	if s.PolicyNamespace == "" {
		return 0, nil, forbidden("delegate decisions need a configured policy namespace")
	}
	var policies v1alpha1.GrantPolicyList
	if err := s.Live.List(ctx, &policies, client.InNamespace(s.PolicyNamespace)); err != nil {
		return 0, nil, fromKubeError(err, "read delegated grant policies")
	}
	if broker.MatchDelegated(&g, policies.Items) == nil {
		return 0, nil, forbidden("no policy with spec.delegated covers this grant's scope and requester")
	}
	rec := broker.MaintenanceDecision{
		GrantUID: string(g.UID), Digest: req.Digest, By: broker.DelegateRef(d.ref), Approve: req.Approve, TTL: ttl,
		At: s.now().UTC().Truncate(time.Second), Ref: req.Ref, Reason: req.Reason,
		Delegate: &broker.DelegateIdentity{Form: d.form, PodName: d.pod.Name, PodUID: string(d.pod.UID)},
	}
	if d.form == 1 {
		rec.Delegate.HostID = d.pod.Annotations[CoordinatorHostAnnotation]
	}
	if d.session != nil {
		rec.Delegate.Session, rec.Delegate.SessionUID = d.session.Name, string(d.session.UID)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return 0, nil, err
	}
	// The broker reads no record over its bound, and the slot is set once:
	// refuse here rather than record a decision nobody can apply.
	if len(raw) > broker.MaxDecisionBytes {
		return 0, nil, invalid(fieldError("reason", "the recorded decision would exceed %d bytes; shorten the reason", broker.MaxDecisionBytes))
	}
	orig := g.DeepCopy()
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations[broker.MaintenanceDecisionAnnotation] = string(raw)
	// The slot is set once, and every replica serves this route: the patch
	// carries the resourceVersion the checks above read, so of two concurrent
	// decisions exactly one lands and the other is a conflict.
	if err := s.Client.Patch(ctx, &g, client.MergeFromWithOptions(orig, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) {
			var now v1alpha1.AccessGrant
			if s.Live.Get(ctx, key, &now) == nil && now.Annotations[broker.MaintenanceDecisionAnnotation] != "" {
				return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "grant %s was decided by another request", key.Name)
			}
			return 0, nil, newError(http.StatusConflict, apiv1.CodeConflict, "grant %s changed while it was decided: read it again", key.Name)
		}
		return 0, nil, fromGrantKubeError(err, "record the delegate decision")
	}
	s.Log.Info("delegate decision recorded ("+broker.DelegateWording+")", "grant", g.Name, "caller", c.String(), "by", rec.By,
		"approve", rec.Approve, "ref", rec.Ref, "type", g.Spec.Type)
	return http.StatusAccepted, s.grantView(&g), nil
}

// grantRole is a kube grant's role, for messages.
func grantRole(g *v1alpha1.AccessGrant) string {
	if g.Spec.Kube == nil {
		return ""
	}
	return g.Spec.Kube.Role
}
