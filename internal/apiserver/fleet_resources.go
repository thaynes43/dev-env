package apiserver

import (
	"context"
	"encoding/json"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const fleetResourcesMaxContainers = 32

// fleetPodListTimeout bounds the Pod list. Tests shorten it. Only the caller's
// own cancellation fails the fleet; this deadline does not.
var fleetPodListTimeout = 10 * time.Second

// fleetResourcesMaxBytes bounds one opted-in resources payload. Past it, every
// observation in that response is unavailable. A prefix of the sessions is not
// returned as a complete result.
var fleetResourcesMaxBytes = 1 << 20

// fleetPodResources lists Pods in the session namespace once and joins them to
// the sessions fleet will show. Cancellation of the caller fails the fleet.
// The local read deadline, or any other list error while the caller is still
// active, is an Unknown observation, not an empty resource spec.
func (s *Server) fleetPodResources(parent context.Context, sessions []v1alpha1.AgentSession) (map[string]*apiv1.FleetResources, error) {
	if s.Live == nil {
		return unknownFleetResources(sessions, apiv1.FleetResourcesUnreadable), nil
	}
	readCtx, cancel := context.WithTimeout(parent, fleetPodListTimeout)
	defer cancel()
	var pods corev1.PodList
	err := s.Live.List(readCtx, &pods, client.InNamespace(s.Policy.SessionNamespace))
	if parent.Err() != nil {
		return nil, parent.Err()
	}
	if err != nil {
		return unknownFleetResources(sessions, apiv1.FleetResourcesUnreadable), nil
	}
	return bindFleetResources(s.now(), s.Policy.SessionNamespace, sessions, pods.Items), nil
}

func bindFleetResources(now time.Time, namespace string, sessions []v1alpha1.AgentSession, pods []corev1.Pod) map[string]*apiv1.FleetResources {
	now = now.UTC().Truncate(time.Second)
	out := make(map[string]*apiv1.FleetResources, len(sessions))
	for i := range sessions {
		sess := &sessions[i]
		out[fleetSessionKey(sess)] = bindOneFleetPod(now, namespace, sess, pods)
	}
	return capFleetResources(out)
}

func bindOneFleetPod(now time.Time, namespace string, sess *v1alpha1.AgentSession, pods []corev1.Pod) *apiv1.FleetResources {
	var otherNamespace bool
	var exact []corev1.Pod
	for i := range pods {
		pod := &pods[i]
		if pod.Name != sess.Name {
			continue
		}
		if pod.Namespace != namespace || pod.Namespace != sess.Namespace {
			otherNamespace = true
			continue
		}
		exact = append(exact, *pod)
	}
	if len(exact) == 0 {
		if otherNamespace {
			return unknownFleetResource(apiv1.FleetResourcesForeign)
		}
		return unknownFleetResource(apiv1.FleetResourcesMissing)
	}
	executor, haveExecutor := recordedExecutorUID(sess)
	var bound []corev1.Pod
	replaced := false
	for i := range exact {
		pod := &exact[i]
		if pod.UID == "" || !ownedBySession(pod, sess) {
			continue
		}
		if haveExecutor && string(pod.UID) != executor {
			replaced = true
			continue
		}
		bound = append(bound, *pod)
	}
	switch len(bound) {
	case 1:
		return observeFleetPod(now, &bound[0])
	default:
		if len(bound) > 1 {
			return unknownFleetResource(apiv1.FleetResourcesAmbiguous)
		}
		if replaced {
			return unknownFleetResource(apiv1.FleetResourcesReplaced)
		}
		return unknownFleetResource(apiv1.FleetResourcesForeign)
	}
}

func ownedBySession(pod *corev1.Pod, sess *v1alpha1.AgentSession) bool {
	if sess.UID == "" {
		return false
	}
	apiVersion := v1alpha1.GroupVersion.String()
	for i := range pod.OwnerReferences {
		ref := &pod.OwnerReferences[i]
		if ref.APIVersion == apiVersion && ref.Kind == "AgentSession" && ref.UID == sess.UID && ref.Name == sess.Name && ref.Controller != nil && *ref.Controller {
			return true
		}
	}
	return false
}

func recordedExecutorUID(sess *v1alpha1.AgentSession) (string, bool) {
	history := sess.Status.PrivateHistory
	if history == nil || history.SessionUID == "" || history.SessionUID != string(sess.UID) || history.LastExecutor == nil || history.LastExecutor.PodUID == "" {
		return "", false
	}
	return history.LastExecutor.PodUID, true
}

func observeFleetPod(now time.Time, pod *corev1.Pod) *apiv1.FleetResources {
	if len(pod.Spec.Containers) > fleetResourcesMaxContainers || len(pod.Spec.InitContainers) > fleetResourcesMaxContainers {
		return unknownFleetResource(apiv1.FleetResourcesOversized)
	}
	observed := now.UTC().Truncate(time.Second)
	node := &apiv1.FleetResourceNode{State: apiv1.FleetNodeUnscheduled}
	if pod.Spec.NodeName != "" {
		node = &apiv1.FleetResourceNode{State: apiv1.FleetNodeSet, Name: pod.Spec.NodeName}
	}
	out := &apiv1.FleetResources{
		State:          apiv1.FleetResourcesObserved,
		ObservedAt:     &observed,
		PodUID:         string(pod.UID),
		Node:           node,
		Containers:     make([]apiv1.FleetContainerResources, 0, len(pod.Spec.Containers)),
		InitContainers: make([]apiv1.FleetContainerResources, 0, len(pod.Spec.InitContainers)),
	}
	for i := range pod.Spec.Containers {
		out.Containers = append(out.Containers, observeFleetContainer(apiv1.FleetContainerOrdinary, &pod.Spec.Containers[i]))
	}
	for i := range pod.Spec.InitContainers {
		out.InitContainers = append(out.InitContainers, observeFleetContainer(apiv1.FleetContainerInit, &pod.Spec.InitContainers[i]))
	}
	return out
}

func observeFleetContainer(kind string, container *corev1.Container) apiv1.FleetContainerResources {
	return apiv1.FleetContainerResources{
		Kind:          kind,
		Name:          container.Name,
		CPURequest:    observeQuantity(container.Resources.Requests, corev1.ResourceCPU),
		CPULimit:      observeQuantity(container.Resources.Limits, corev1.ResourceCPU),
		MemoryRequest: observeQuantity(container.Resources.Requests, corev1.ResourceMemory),
		MemoryLimit:   observeQuantity(container.Resources.Limits, corev1.ResourceMemory),
	}
}

func observeQuantity(list corev1.ResourceList, name corev1.ResourceName) apiv1.FleetQuantity {
	quantity, ok := list[name]
	if !ok {
		return apiv1.FleetQuantity{State: apiv1.FleetQuantityAbsent}
	}
	number, suffix := quantity.CanonicalizeBytes(nil)
	buf := make([]byte, 0, len(number)+len(suffix))
	buf = append(buf, number...)
	buf = append(buf, suffix...)
	return apiv1.FleetQuantity{State: apiv1.FleetQuantitySet, Value: string(buf)}
}

func capFleetResources(obs map[string]*apiv1.FleetResources) map[string]*apiv1.FleetResources {
	raw, err := json.Marshal(obs)
	if err != nil || len(raw) > fleetResourcesMaxBytes {
		for key := range obs {
			obs[key] = unknownFleetResource(apiv1.FleetResourcesOversized)
		}
	}
	return obs
}

func unknownFleetResources(sessions []v1alpha1.AgentSession, reason string) map[string]*apiv1.FleetResources {
	out := make(map[string]*apiv1.FleetResources, len(sessions))
	for i := range sessions {
		out[fleetSessionKey(&sessions[i])] = unknownFleetResource(reason)
	}
	return out
}

func unknownFleetResource(reason string) *apiv1.FleetResources {
	return &apiv1.FleetResources{State: apiv1.FleetResourcesUnknown, Reason: reason}
}

func fleetSessionKey(sess *v1alpha1.AgentSession) string {
	if sess.UID != "" {
		return string(sess.UID)
	}
	return "name:" + sess.Namespace + "/" + sess.Name
}
