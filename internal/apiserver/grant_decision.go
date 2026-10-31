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
//     Running AgentSession that Tom's human or a client caller launched.
//
// Form 2 activates with S4 (DESIGN-042 6.3), which lets resolveCaller classify
// a pod running as a named coordinator ServiceAccount as a session. Until then
// resolveCaller classifies only the shared session and workbench accounts as
// sessions, and ParseGrantDelegates refuses both, so no session caller is a
// delegate. delegateOf is the seam S4 reaches; it changes nothing in caller.go.

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
// worker the maintenance envelope names; a session that descends from the
// requester through any parent link; a session that shares a launching
// session with the requester (siblings and cousins); and, for a delegate
// session, one that Tom's human or a client caller did not launch. The last
// rule is S4's launch rule checked again at decision time: a session may not
// launch its own delegate. A delegate deciding for its own children stays
// allowed.
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
		// Two sessions on one delegate account would decide for each other
		// until S4 allows one Running session per coordinator account.
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
	if d.session == nil {
		return nil
	}
	// The delegate's launching sessions: none may be the requester.
	ancestors := map[string]bool{}
	parent := d.session.Spec.Parent
	for i := 0; parent != "" && !strings.Contains(parent, "/"); i++ {
		if parent == rq.Session {
			return forbidden("a delegate never decides a grant of its parent or another ancestor")
		}
		if i >= maxDelegateAncestry {
			return forbidden("the delegate's ancestry is deeper than sessions may sit")
		}
		var a v1alpha1.AgentSession
		if err := s.Live.Get(ctx, types.NamespacedName{Namespace: d.session.Namespace, Name: parent}, &a); err != nil {
			return forbidden("the delegate's ancestry cannot be verified")
		}
		if a.UID == rq.SessionUID {
			return forbidden("a delegate never decides a grant of its parent or another ancestor")
		}
		ancestors[a.Name] = true
		parent = a.Spec.Parent
	}
	// Siblings and cousins: a delegate and the requester that share a
	// launching session (anything but a human or client ServiceAccount,
	// which names no session) are one tree, and that session could route
	// its own children's grants through a delegate it launched. A delegate's
	// own descendants share no ancestor with it, so it still decides for them.
	if len(ancestors) > 0 {
		var r v1alpha1.AgentSession
		if err := s.Live.Get(ctx, types.NamespacedName{Namespace: s.Policy.SessionNamespace, Name: rq.Session}, &r); err != nil || r.UID != rq.SessionUID {
			return forbidden("the requesting session's ancestry cannot be verified")
		}
		parent := r.Spec.Parent
		for i := 0; parent != "" && !strings.Contains(parent, "/"); i++ {
			if ancestors[parent] {
				return forbidden("a delegate never decides a grant of a session it shares a launching session with")
			}
			if i >= maxDelegateAncestry {
				return forbidden("the requesting session's ancestry is deeper than sessions may sit")
			}
			var a v1alpha1.AgentSession
			if err := s.Live.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: parent}, &a); err != nil {
				return forbidden("the requesting session's ancestry cannot be verified")
			}
			parent = a.Spec.Parent
		}
	}
	if launcher := d.session.Spec.Parent; d.form == 2 &&
		(sessionDepth(d.session) != 0 || (launcher != s.Policy.Human && !slices.Contains(s.Policy.Clients, launcher))) {
		return forbidden("a delegate session is one Tom's human or a client caller launched")
	}
	return nil
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
		if g.Spec.Maintenance.ExtensionOf != "" {
			return 0, nil, forbidden("a maintenance extension needs the human route (ADR-012)")
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
	rec := broker.MaintenanceDecision{
		GrantUID: string(g.UID), Digest: req.Digest, By: broker.DelegateRef(d.ref), Approve: req.Approve, TTL: ttl,
		At: s.now().UTC().Truncate(time.Second), Ref: req.Ref, Reason: req.Reason,
		Delegate: &broker.DelegateIdentity{PodUID: string(d.pod.UID)},
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
