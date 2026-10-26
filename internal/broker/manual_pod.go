package broker

import (
	"context"
	"encoding/json"
	"errors"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/grantdecision"
)

// labelManualPod changes only the reserved UID label, with both pod UID and
// resourceVersion preconditions. An admission guard must constrain this patch.
func (b *Broker) labelManualPod(ctx context.Context, pod *corev1.Pod) error {
	if pod == nil || pod.UID == "" || pod.ResourceVersion == "" {
		return errors.New("manual grant pod identity is unavailable")
	}
	if value, exists := pod.Labels[grantdecision.PodUIDLabel]; exists {
		if value == string(pod.UID) {
			return nil
		}
		return errors.New("manual grant pod UID label conflicts")
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"uid": string(pod.UID), "resourceVersion": pod.ResourceVersion, "labels": map[string]string{grantdecision.PodUIDLabel: string(pod.UID)}}})
	if err != nil {
		return errors.New("could not encode pod identity patch")
	}
	return b.Client.Patch(ctx, pod, client.RawPatch(types.MergePatchType, patch))
}
