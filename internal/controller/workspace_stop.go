package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// WorkspaceStopVerifier always reads through the APIReader, never the manager
// cache. Node/lease reads deliberately fail closed until GitOps grants them.
type WorkspaceStopVerifier interface {
	Verify(context.Context, *v1alpha1.AgentSession, types.UID) (*protocol.WorkspaceStopProof, *corev1.Pod, error)
}

type PodWorkspaceStopVerifier struct {
	Reader client.Reader
	Now    func() time.Time
}

func (v PodWorkspaceStopVerifier) Verify(ctx context.Context, s *v1alpha1.AgentSession, uid types.UID) (*protocol.WorkspaceStopProof, *corev1.Pod, error) {
	if v.Reader == nil || s.Spec.Workspace == nil || uid == "" {
		return nil, nil, errors.New("workspace stop proof requires an uncached reader and exact old Pod UID")
	}
	var pod corev1.Pod
	if err := v.Reader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &pod); err != nil {
		return nil, nil, fmt.Errorf("retained executor: %w", err)
	}
	if pod.UID != uid || !controlledBySession(&pod, s) || !pod.DeletionTimestamp.IsZero() || pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return nil, nil, errors.New("retained executor UID, owner, deletion state or restart policy cannot prove stop")
	}
	if err := allContainersTerminated(&pod); err != nil {
		return nil, nil, err
	}
	if pod.Spec.NodeName == "" {
		return nil, nil, errors.New("terminated executor lacks its admitted node identity")
	}
	now := time.Now().UTC()
	if v.Now != nil {
		now = v.Now().UTC()
	}
	node, lease, err := executorNodeLive(ctx, v.Reader, pod.Spec.NodeName, now)
	if err != nil {
		return nil, nil, err
	}
	proof := &protocol.WorkspaceStopProof{Version: 1, Workspace: s.Spec.Workspace.ID, Task: s.Name, SessionUID: string(s.UID), PodName: pod.Name, PodUID: string(pod.UID),
		PodResourceVersion: pod.ResourceVersion, NodeName: node.Name, NodeUID: string(node.UID), LeaseResourceVersion: lease.ResourceVersion,
		LeaseRenewedAt: lease.Spec.RenewTime.Time, VerifiedAt: now}
	if err := proof.Validate(s.Spec.Workspace.ID, s.Name, string(s.UID), string(uid)); err != nil {
		return nil, nil, err
	}
	return proof, &pod, nil
}

// executorNodeLive checks that a live kubelet wrote a Pod's terminated status
// (DESIGN-025 3.2 points 2 and 3), for the shared stop verifier and the private
// terminal record alike. The node exists, is not deleting and has exactly one
// Ready condition, which is True; its lease in kube-node-lease is held by that
// node, owned by that node's UID, renewed within the last 40 seconds and not in
// the future. When the Pod garbage collector marks a Pod Failed for a lost node,
// that node is not Ready, so its status never counts. Reads go through reader,
// which must be uncached. A read the operator has no rights for (Nodes and
// Leases until haynes-ops grants them) is an error: it fails closed.
func executorNodeLive(ctx context.Context, reader client.Reader, nodeName string, now time.Time) (*corev1.Node, *coordinationv1.Lease, error) {
	var node corev1.Node
	if err := reader.Get(ctx, client.ObjectKey{Name: nodeName}, &node); err != nil {
		return nil, nil, fmt.Errorf("executor node: %w", err)
	}
	ready, readyConditions := false, 0
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			readyConditions++
			ready = c.Status == corev1.ConditionTrue
		}
	}
	if node.Name != nodeName || node.UID == "" || !node.DeletionTimestamp.IsZero() || readyConditions != 1 || !ready {
		return nil, nil, errors.New("executor node is missing, deleting, unready or unknown")
	}
	var lease coordinationv1.Lease
	if err := reader.Get(ctx, client.ObjectKey{Namespace: "kube-node-lease", Name: nodeName}, &lease); err != nil {
		return nil, nil, fmt.Errorf("executor node lease: %w", err)
	}
	if !lease.DeletionTimestamp.IsZero() || lease.Spec.RenewTime == nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != node.Name ||
		lease.Spec.RenewTime.After(now) || now.Sub(lease.Spec.RenewTime.Time) > 40*time.Second {
		return nil, nil, errors.New("executor node lease is missing, stale, future or foreign; stop proof refused")
	}
	matchingNode := false
	for _, owner := range lease.OwnerReferences {
		if owner.APIVersion == "v1" && owner.Kind == "Node" && owner.Name == node.Name && owner.UID == node.UID {
			matchingNode = true
		}
	}
	if !matchingNode {
		return nil, nil, errors.New("executor node lease belongs to a different node UID")
	}
	return &node, &lease, nil
}

func controlledBySession(p *corev1.Pod, s *v1alpha1.AgentSession) bool {
	owner := metav1.GetControllerOf(p)
	return owner != nil && owner.APIVersion == v1alpha1.GroupVersion.String() && owner.Kind == "AgentSession" && owner.Name == s.Name && owner.UID == s.UID
}

func allContainersTerminated(p *corev1.Pod) error {
	check := func(names []string, statuses []corev1.ContainerStatus) error {
		if len(names) != len(statuses) {
			return errors.New("executor has missing or extra admitted container status")
		}
		seen := map[string]bool{}
		for _, status := range statuses {
			if seen[status.Name] || status.State.Terminated == nil || status.State.Terminated.FinishedAt.IsZero() || status.State.Running != nil || status.State.Waiting != nil {
				return errors.New("executor has a duplicate, uncertain or nonterminated current container")
			}
			seen[status.Name] = true
		}
		for _, name := range names {
			if name == "" || !seen[name] {
				return errors.New("executor lacks termination of a named admitted container")
			}
		}
		return nil
	}
	var regular, init, ephemeral []string
	policy := func(p *corev1.ContainerRestartPolicy, rules []corev1.ContainerRestartRule) bool {
		return (p == nil || *p == corev1.ContainerRestartPolicyNever) && len(rules) == 0
	}
	for _, c := range p.Spec.Containers {
		if !policy(c.RestartPolicy, c.RestartPolicyRules) {
			return errors.New("executor container may restart despite Pod RestartPolicyNever")
		}
		regular = append(regular, c.Name)
	}
	if len(regular) == 0 {
		return errors.New("executor has no admitted regular container")
	}
	for _, c := range p.Spec.InitContainers {
		if !policy(c.RestartPolicy, c.RestartPolicyRules) {
			return errors.New("executor init container may restart")
		}
		init = append(init, c.Name)
	}
	for _, c := range p.Spec.EphemeralContainers {
		if !policy(c.RestartPolicy, c.RestartPolicyRules) {
			return errors.New("executor ephemeral container may restart")
		}
		ephemeral = append(ephemeral, c.Name)
	}
	for _, group := range []struct {
		names    []string
		statuses []corev1.ContainerStatus
	}{{regular, p.Status.ContainerStatuses}, {init, p.Status.InitContainerStatuses}, {ephemeral, p.Status.EphemeralContainerStatuses}} {
		if err := check(group.names, group.statuses); err != nil {
			return err
		}
	}
	return nil
}
