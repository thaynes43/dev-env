package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const CoordinatorHostAnnotation = "dev-env.haynesops.com/coordinator-host"

// CoordinatorHost binds one dedicated ServiceAccount to a declared logical host
// and stable Pod name. Neither the request body nor a different Pod can name it.
type CoordinatorHost struct {
	ServiceAccount string `json:"serviceAccount"`
	PodName        string `json:"podName"`
	HostID         string `json:"hostID"`
}

// ParseCoordinatorHosts parses explicit configuration; an empty value configures
// no hosts. The class is disabled independently of this list.
func ParseCoordinatorHosts(raw string, policy Policy) ([]CoordinatorHost, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 16384 {
		return nil, errors.New("coordinator configuration exceeds its bound")
	}
	var hosts []CoordinatorHost
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&hosts) != nil || d.Decode(&struct{}{}) != io.EOF || len(hosts) > 32 {
		return nil, errors.New("coordinator configuration is not a bounded host list")
	}
	seenSA, seenHost, seenPod := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, h := range hosts {
		ns, name, ok := strings.Cut(h.ServiceAccount, "/")
		if !ok || len(validation.IsDNS1123Label(ns)) != 0 || len(validation.IsDNS1123Subdomain(name)) != 0 ||
			len(validation.IsDNS1123Subdomain(h.PodName)) != 0 || len(validation.IsDNS1123Label(h.HostID)) != 0 ||
			h.ServiceAccount == policy.Human || slices.Contains(policy.Clients, h.ServiceAccount) ||
			h.ServiceAccount == policy.SessionNamespace+"/"+policy.SessionServiceAccount ||
			seenSA[h.ServiceAccount] || seenHost[h.HostID] || seenPod[ns+"/"+h.PodName] {
			return nil, errors.New("coordinator configuration has an invalid, overlapping or repeated identity")
		}
		seenSA[h.ServiceAccount], seenHost[h.HostID], seenPod[ns+"/"+h.PodName] = true, true, true
	}
	return hosts, nil
}

func coordinatorDenied() error {
	return forbidden("coordinator access is limited to its direct task children")
}

func (s *Server) resolveCoordinator(ctx context.Context, id Identity, host CoordinatorHost) (*caller, error) {
	if s.Live == nil || id.PodName != host.PodName || id.PodUID == "" {
		return nil, coordinatorDenied()
	}
	var pod corev1.Pod
	if s.Live.Get(ctx, types.NamespacedName{Namespace: id.Namespace, Name: id.PodName}, &pod) != nil ||
		string(pod.UID) != id.PodUID || !pod.DeletionTimestamp.IsZero() ||
		pod.Spec.ServiceAccountName != id.ServiceAccount ||
		pod.Annotations[CoordinatorHostAnnotation] != host.HostID {
		return nil, coordinatorDenied()
	}
	return &caller{kind: kindCoordinator, identity: id, parent: id.serviceAccountRef(), pod: &pod}, nil
}

// childForCoordinator uses an uncached read and a uniform refusal for missing,
// foreign and replaced children. An optional expected UID fences a second read.
func (s *Server) childForCoordinator(ctx context.Context, key types.NamespacedName, c *caller, expected types.UID) (*v1alpha1.AgentSession, error) {
	if s.Live == nil {
		return nil, coordinatorDenied()
	}
	var sess v1alpha1.AgentSession
	if s.Live.Get(ctx, key, &sess) != nil || sess.UID == "" || sess.Spec.Parent != c.parent ||
		(expected != "" && sess.UID != expected) {
		return nil, coordinatorDenied()
	}
	return &sess, nil
}

// authorizeChildMutation checks the current Session and any live executor Pod
// before mutation. A missing Pod is normal for pending or suspended children.
// A present Pod must belong to this exact Session, never a previous UID.
func (s *Server) authorizeChildMutation(ctx context.Context, sess *v1alpha1.AgentSession, c *caller) error {
	if c.kind != kindCoordinator {
		return nil
	}
	fresh, err := s.childForCoordinator(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, c, sess.UID)
	if err != nil || fresh.ResourceVersion != sess.ResourceVersion {
		return coordinatorDenied()
	}
	var pod corev1.Pod
	err = s.Live.Get(ctx, types.NamespacedName{Namespace: sess.Namespace, Name: sess.Name}, &pod)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil || pod.UID == "" || !metav1.IsControlledBy(&pod, sess) ||
		pod.Spec.ServiceAccountName != s.Policy.SessionServiceAccount {
		return coordinatorDenied()
	}
	return nil
}

func (s *Server) authorizeChildExec(ctx context.Context, sess *v1alpha1.AgentSession, pod *corev1.Pod, c *caller) error {
	if c.kind != kindCoordinator {
		return nil
	}
	if err := s.authorizeChildMutation(ctx, sess, c); err != nil {
		return err
	}
	var fresh corev1.Pod
	if s.Live.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, &fresh) != nil ||
		fresh.UID != pod.UID || !fresh.DeletionTimestamp.IsZero() || !metav1.IsControlledBy(&fresh, sess) {
		return coordinatorDenied()
	}
	return nil
}

// CoordinatorAllowed is explicit on a route; a future global route is denied by
// default until its authorization contract is deliberately added.
func coordinatorAllowed(c *caller, allowed bool) error {
	if c.kind == kindCoordinator && !allowed {
		return coordinatorDenied()
	}
	return nil
}

func coordinatorLogPath(r *http.Request, c *caller) string {
	if c != nil && c.kind == kindCoordinator && r.PathValue("name") != "" {
		return "/v1/sessions/{child}"
	}
	return r.URL.Path
}
