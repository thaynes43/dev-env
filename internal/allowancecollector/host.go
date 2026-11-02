package allowancecollector

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// CheckHost uses uncached reads, fixed exact identities and a current heartbeat.
// A replacement pod or changed template is never an automatic continuation.
func CheckHost(ctx context.Context, live client.Reader, host Host, now time.Time) error {
	if live == nil || now.IsZero() {
		return ErrCollector
	}
	key := types.NamespacedName{Namespace: Namespace, Name: host.Name}
	var session v1alpha1.AgentSession
	if live.Get(ctx, key, &session) != nil || string(session.UID) != host.SessionUID || session.Generation != host.Generation || !session.DeletionTimestamp.IsZero() || session.Spec.Agent != v1alpha1.AgentClaude || session.Spec.Mode != v1alpha1.ModeRemote || session.Spec.OperatingMode != v1alpha1.OperatingModeRunning || session.Spec.Limits != nil || session.Spec.Workspace != nil || session.Spec.Restore != "" || session.Status.PodName != host.Name || session.Status.Phase != v1alpha1.PhaseRunning && session.Status.Phase != v1alpha1.PhaseIdle || session.Status.Agent == nil || session.Status.Agent.LastHeartbeat == nil {
		return ErrCollector
	}
	beat := session.Status.Agent.LastHeartbeat.Time
	if beat.After(now) || now.Sub(beat) > 2*time.Minute {
		return ErrCollector
	}
	var pod corev1.Pod
	if live.Get(ctx, key, &pod) != nil || string(pod.UID) != host.PodUID || !pod.DeletionTimestamp.IsZero() || pod.Spec.ServiceAccountName != host.ServiceAccount || pod.Status.Phase != corev1.PodRunning || !metav1.IsControlledBy(&pod, &session) {
		return ErrCollector
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != "agent" {
			continue
		}
		if container.Image != host.Image {
			return ErrCollector
		}
		// Existing keeper access projections qualify the selected normal HOME;
		// no Secret values, auth files or tokens are read by this check.
		claude, codex := false, false
		secrets := map[string]string{}
		for _, volume := range pod.Spec.Volumes {
			if volume.Secret != nil && (volume.Secret.Optional == nil || !*volume.Secret.Optional) {
				secrets[volume.Name] = volume.Secret.SecretName
			}
		}
		for _, mount := range container.VolumeMounts {
			if mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == "" && mount.MountPath == "/opt/dev-env/claude-access" && secrets[mount.Name] == "dev-env-claude-live" {
				claude = true
			}
			if mount.ReadOnly && mount.SubPath == "" && mount.SubPathExpr == "" && mount.MountPath == "/opt/dev-env/codex-access" && secrets[mount.Name] == "dev-env-codex-live" {
				codex = true
			}
		}
		if !claude || !codex {
			return ErrCollector
		}
		return nil
	}
	return ErrCollector
}
