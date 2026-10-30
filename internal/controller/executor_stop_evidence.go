package controller

import (
	"context"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// VerifyExecutorStopped reuses the existing node/lease and all-container stop
// proof for a maintenance executor. Absence, PodGC Failed and stale node status
// are not termination evidence. Callers must first bind the executor Pod UID.
func VerifyExecutorStopped(ctx context.Context, reader client.Reader, pod *corev1.Pod, nodeUID string, now func() time.Time) error {
	if pod == nil || !podPhaseTerminal(pod) {
		return errors.New("executor phase is not terminal")
	}
	if err := allContainersTerminated(pod); err != nil {
		return err
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.DisruptionTarget && condition.Reason == podGCDeletionReason {
			return errors.New("PodGC failure is not node termination proof")
		}
	}
	node, _, _, err := executorNodeLive(ctx, reader, pod.Spec.NodeName, now)
	if err != nil {
		return err
	}
	if string(node.UID) != nodeUID {
		return errors.New("executor target node UID was replaced")
	}
	return nil
}
