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
)

// maxSessionDepth is the deepest a session may sit (DESIGN-001 3.4, "two levels
// deep"): depth 0 is a session Tom or a client created, 1 its child, 2 a
// grandchild, which creates no children.
const maxSessionDepth = 2

// maxRunningChildren is how many unfinished children one session may have at a
// time (DESIGN-001 3.4).
const maxRunningChildren = 4

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
	// ActivityNamespace holds the Activity declarations (dev-env-system, D-66).
	// Empty answers /v1/activities with 503.
	ActivityNamespace string
	// Coordinators are separately configured host identities, never trusted Clients.
	CoordinatorEnabled bool
	Coordinators       []CoordinatorHost
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
	switch {
	case ref == s.Policy.Human:
		return &caller{kind: kindHuman, identity: id, parent: ref}, nil
	case slices.Contains(s.Policy.Clients, ref):
		return &caller{kind: kindClient, identity: id, parent: ref}, nil
	case id.Namespace == s.Policy.SessionNamespace && id.ServiceAccount == s.Policy.SessionServiceAccount:
		return s.resolveSession(ctx, id)
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
