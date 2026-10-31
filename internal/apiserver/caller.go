package apiserver

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// callerKind is the class a caller's identity falls in (D-46). The class decides
// what the caller may do; nothing in a request body does.
type callerKind string

const (
	// kindHuman is Tom: the dev-env-human ServiceAccount, whose token his
	// laptop mints with his admin kubeconfig (D-05).
	kindHuman callerKind = "human"
	// kindClient is a trusted client that is not a session: the workbench, and
	// the v1 pod until cutover.
	kindClient callerKind = "client"
	// kindSession is a session pod, by its projected token.
	kindSession     callerKind = "session"
	kindCoordinator callerKind = "coordinator"
	// kindPortalReader is the default-off portal ServiceAccount. It is not a
	// human, client, session, or coordinator.
	kindPortalReader callerKind = "portal-reader"
	// kindPortalLaunch is never authenticated. portalLaunch derives it, after the
	// portal reader passes that route's checks, to run the one create path with
	// the parent "portal/<actorRef>", depth 1 and no profile. It is not a
	// human, client, session or coordinator, and no route accepts it.
	kindPortalLaunch callerKind = "portal-launch"
)

// maxSessionDepth is the deepest a session may sit (DESIGN-001 3.4, "two levels
// deep"): depth 0 is a session Tom or a client created, 1 its child, 2 a
// grandchild, which creates no children.
const maxSessionDepth = 2

// maxRunningChildrenPerAgent is how many unfinished children one session may have per canonical agent at a
// time (DESIGN-001 3.4).
const maxRunningChildrenPerAgent = 4

// Policy names the identities the API serves (D-46). Any other caller gets a
// 403: summoning callers, with their CallerPolicy, arrive in plan 10.
type Policy struct {
	// Human is Tom's ServiceAccount, "<namespace>/<name>".
	Human string
	// Clients are trusted ServiceAccounts that are not sessions,
	// "<namespace>/<name>" each: the workbench, the v1 pod.
	Clients []string
	// SessionNamespace holds the AgentSessions and their pods.
	SessionNamespace string
	// SessionServiceAccount is the ServiceAccount every session pod runs as, in
	// SessionNamespace.
	SessionServiceAccount string
	// WorkbenchServiceAccount is the second identity a session pod may run as,
	// in SessionNamespace (the workbench profile, templates.WorkbenchServiceAccount).
	// It is also a trusted client: a token bound to a session's pod speaks for that
	// session, any other token of it speaks for the client. Empty: no such pod.
	WorkbenchServiceAccount string
	// NamedCoordinatorIdentities lets a session pod also run as a named
	// coordinator identity, dev-env-coordinator-<name> in SessionNamespace
	// (templates.IsCoordinatorServiceAccount; DESIGN-042 6.4, S4). A token of such
	// an account is a session caller only when it is bound to a pod that runs as
	// it and that its session controls. It is never a client or a coordinator
	// host, and any other token of it is refused. Off: no such account calls.
	NamedCoordinatorIdentities bool
	// ActivityNamespace holds the Activity declarations (dev-env-system, D-66).
	// Empty answers /v1/activities with 503.
	ActivityNamespace string
	// Coordinators are separately configured host identities, never trusted Clients.
	CoordinatorEnabled bool
	Coordinators       []CoordinatorHost
	// PortalReaderEnabled accepts only the portal ServiceAccount, and only for
	// its session-summary read. Default off.
	PortalReaderEnabled bool
}

// caller is an authenticated, classified caller.
type caller struct {
	kind     callerKind
	identity Identity
	// parent is what a session this caller creates records as spec.parent,
	// and what "mine" matches: the session's name for a session, else the
	// ServiceAccount as "<namespace>/<name>".
	parent string
	// depth is the depth of a session this caller creates.
	depth int
	// session and pod are the calling session and its pod, for kindSession.
	session *v1alpha1.AgentSession
	pod     *corev1.Pod
	// profile is the calling session's profile, as its pod was built with:
	// the profile its children run on.
	profile string
}

// String names the caller in logs: never a token.
func (c *caller) String() string {
	if c.kind == kindSession {
		return "session/" + c.parent
	}
	return string(c.kind) + "/" + c.parent
}

// resolveCaller classifies an identity, or refuses it with a 403.
func (s *Server) resolveCaller(ctx context.Context, id Identity) (*caller, error) {
	ref := id.serviceAccountRef()
	if ref == "" {
		return nil, forbidden("only ServiceAccount tokens may call this API; %q is not one", id.Username)
	}
	if ref == portalReaderRef {
		if err := ValidatePortalCaller(s.Policy); err != nil {
			return nil, forbidden("the portal ServiceAccount overlaps another caller class")
		}
		if !s.Policy.PortalReaderEnabled {
			return nil, forbidden("the portal reader is disabled")
		}
		return s.resolvePortalReader(ctx, id)
	}
	switch {
	case ref == s.Policy.Human:
		return &caller{kind: kindHuman, identity: id, parent: ref}, nil
	case s.workbenchSessionPod(ctx, id):
		// Before the clients: a workbench session's pod must heartbeat and
		// act as the session it is. The pod's controller decides, not the token.
		return s.resolveSession(ctx, id)
	case slices.Contains(s.Policy.Clients, ref):
		return &caller{kind: kindClient, identity: id, parent: ref}, nil
	case id.Namespace == s.Policy.SessionNamespace && id.ServiceAccount == s.Policy.SessionServiceAccount:
		return s.resolveSession(ctx, id)
	case s.namedCoordinatorIdentity(id):
		// After the clients, so a configured client keeps its class, and
		// before the coordinator hosts, which may not name such an account
		// (ParseCoordinatorHosts). It is a session or nothing.
		return s.resolveNamedCoordinatorSession(ctx, id)
	}
	if s.Policy.CoordinatorEnabled {
		for _, host := range s.Policy.Coordinators {
			if host.ServiceAccount == ref {
				return s.resolveCoordinator(ctx, id, host)
			}
		}
	}
	return nil, forbidden("%s may not call this API: it is not Tom's, a client's or a session's ServiceAccount, and summoning callers (CallerPolicy) arrive in plan 10", ref)
}

// ValidatePortalCaller rejects a policy that also names the portal ServiceAccount
// as a human, client, session, workbench, or coordinator.
func ValidatePortalCaller(p Policy) error {
	if p.Human == portalReaderRef || slices.Contains(p.Clients, portalReaderRef) {
		return fmt.Errorf("portal service account overlaps another caller")
	}
	if p.SessionNamespace == portalReaderNamespace && (p.SessionServiceAccount == portalReaderAccount || p.WorkbenchServiceAccount == portalReaderAccount) {
		return fmt.Errorf("portal service account overlaps another caller")
	}
	for _, host := range p.Coordinators {
		if host.ServiceAccount == portalReaderRef {
			return fmt.Errorf("portal service account overlaps another caller")
		}
	}
	return nil
}

// sessionPodAccount reports whether a session pod may run as the named
// ServiceAccount: the ordinary session account, the workbench account when the
// policy names it, or a named coordinator identity when the policy allows them.
// The coordination routes recheck the live Pod with it, so a workbench or named
// coordinator session is the session it is there too, exactly as it is for the
// heartbeat (resolveCaller). The Pod's account must also be the one the token
// carries, so the fence stays self-contained. The account alone grants nothing:
// the live Pod must still be controlled by the caller's own AgentSession.
func (s *Server) sessionPodAccount(name string, c *caller) bool {
	return name == c.identity.ServiceAccount &&
		(name == s.Policy.SessionServiceAccount ||
			(s.Policy.WorkbenchServiceAccount != "" && name == s.Policy.WorkbenchServiceAccount) ||
			(s.Policy.NamedCoordinatorIdentities && templates.IsCoordinatorServiceAccount(name)))
}

// namedCoordinatorIdentity reports whether id is a named coordinator identity's
// token (DESIGN-042 6.4, S4): the policy allows them, and the account is
// dev-env-coordinator-<name> in the session namespace. Any other namespace, and
// any name that is not exactly that shape, is not one.
func (s *Server) namedCoordinatorIdentity(id Identity) bool {
	return s.Policy.NamedCoordinatorIdentities && s.Policy.SessionNamespace != "" &&
		id.Namespace == s.Policy.SessionNamespace && templates.IsCoordinatorServiceAccount(id.ServiceAccount)
}

// resolveNamedCoordinatorSession finds the session a named coordinator
// identity's token speaks for. It is resolveSession, the rule a workbench
// session's pod meets, plus one check: the live Pod runs as the token's own
// account. So the token speaks only for a session whose pod the operator built
// on a profile that names that account (controller.buildPod), and never for an
// ordinary session's pod. There is no client fallback: a token bound to no pod,
// or to a pod no session controls, is refused.
func (s *Server) resolveNamedCoordinatorSession(ctx context.Context, id Identity) (*caller, error) {
	c, err := s.resolveSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.pod.Spec.ServiceAccountName != id.ServiceAccount {
		return nil, forbidden("pod %s does not run as %s", c.pod.Name, id.serviceAccountRef())
	}
	return c, nil
}

// isWorkbenchSession reports whether c is a session whose pod runs as the
// workbench ServiceAccount. The ServiceAccount comes from the TokenReview, and
// resolveCaller only treats a pod-bound token of that account as a session when
// an AgentSession owns the pod, so a session on the ordinary account never
// passes. Such a caller is that session for heartbeats and the workbench for the
// Codex remote create rule.
func (s *Server) isWorkbenchSession(c *caller) bool {
	return c.kind == kindSession && s.Policy.WorkbenchServiceAccount != "" &&
		c.identity.Namespace == s.Policy.SessionNamespace &&
		c.identity.ServiceAccount == s.Policy.WorkbenchServiceAccount
}

// workbenchSessionPod reports whether id is the workbench ServiceAccount's token
// bound to a pod that an AgentSession owns. A token bound to no pod, or to a
// pod no session owns, is the client's. A pod that cannot be read is not
// guessed at: it counts as a session pod, so resolveSession refuses it.
func (s *Server) workbenchSessionPod(ctx context.Context, id Identity) bool {
	if s.Policy.WorkbenchServiceAccount == "" || id.Namespace != s.Policy.SessionNamespace ||
		id.ServiceAccount != s.Policy.WorkbenchServiceAccount || id.PodName == "" {
		return false
	}
	var pod corev1.Pod
	if err := s.get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.PodName}, &pod); err != nil {
		return true
	}
	owner := metav1.GetControllerOf(&pod)
	return owner != nil && owner.Kind == "AgentSession"
}

// resolveSession finds the session a session pod's token speaks for. The token
// must be bound to a pod (a projected token): the API server has checked that
// the pod exists with the claimed UID, and the pod's controller must be a
// session of the same name.
func (s *Server) resolveSession(ctx context.Context, id Identity) (*caller, error) {
	if id.PodName == "" || id.PodUID == "" {
		return nil, forbidden("a session calls with its pod's projected token; this %s token is bound to no pod", id.serviceAccountRef())
	}
	ns := s.Policy.SessionNamespace
	var pod corev1.Pod
	if err := s.get(ctx, types.NamespacedName{Namespace: ns, Name: id.PodName}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, forbidden("the token's pod %s is gone", id.PodName)
		}
		return nil, fromKubeError(err, "the caller's pod")
	}
	if string(pod.UID) != id.PodUID {
		return nil, forbidden("the token is bound to an earlier pod named %s", id.PodName)
	}
	owner := metav1.GetControllerOf(&pod)
	if owner == nil || owner.Kind != "AgentSession" || owner.APIVersion != v1alpha1.GroupVersion.String() {
		return nil, forbidden("pod %s is not a session's pod", pod.Name)
	}
	var sess v1alpha1.AgentSession
	if err := s.get(ctx, types.NamespacedName{Namespace: ns, Name: owner.Name}, &sess); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, forbidden("pod %s belongs to session %s, which is gone", pod.Name, owner.Name)
		}
		return nil, fromKubeError(err, "the caller's session")
	}
	if sess.UID != owner.UID {
		return nil, forbidden("pod %s belongs to an earlier session named %s", pod.Name, owner.Name)
	}
	profile := pod.Labels[v1alpha1.LabelProfile]
	if profile == "" {
		profile = sess.Spec.Profile
	}
	return &caller{
		kind:     kindSession,
		identity: id,
		parent:   sess.Name,
		depth:    sessionDepth(&sess) + 1,
		session:  &sess,
		pod:      &pod,
		profile:  profile,
	}, nil
}

// sessionDepth reads a session's depth label. A session with no label, which
// only a human with kubectl can create, is at depth 0.
func sessionDepth(s *v1alpha1.AgentSession) int {
	d, err := strconv.Atoi(s.Labels[v1alpha1.LabelDepth])
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// get reads from the manager's cache and falls back to the API server when the
// cache has not seen the object yet: a session created a moment ago, or the pod
// that just started.
func (s *Server) get(ctx context.Context, key types.NamespacedName, obj client.Object) error {
	err := s.Client.Get(ctx, key, obj)
	if apierrors.IsNotFound(err) && s.Live != nil {
		err = s.Live.Get(ctx, key, obj)
	}
	if err != nil {
		return fmt.Errorf("get %s: %w", key.Name, err)
	}
	return nil
}
