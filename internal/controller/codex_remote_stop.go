package controller

import (
	"errors"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Only construction calls these helpers. A flag change never updates, relabels,
// replaces or restarts a running pod. An explicit UID overrides envFrom.
func (r *Reconciler) applyCodexRemoteStop(pod *corev1.Pod, s *v1alpha1.AgentSession) {
	if !r.CodexRemoteStopHosts.Names(s) {
		if r.CodexRemoteStopHosts.Lists(s) {
			log.Log.Info("codex-remote-stop-hosts name has no effect on this session: it is not a Codex remote host", "session", s.Name)
		}
		return
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == ContainerName {
			pod.Spec.Containers[i].Env = append(pod.Spec.Containers[i].Env, corev1.EnvVar{Name: protocol.CodexRemoteStopEnv, Value: string(s.UID)})
		}
	}
}

func (r *Reconciler) holdPod(s *v1alpha1.AgentSession, t *templates.Templates) (*corev1.Pod, error) {
	pod, err := buildHoldPod(s, t)
	if err != nil {
		return nil, err
	}
	if r.CodexRemoteStopHosts.Names(s) && s.UID == "" {
		return nil, errors.New("codex remote stop requires its Session UID")
	}
	r.applyCodexRemoteStop(pod, s)
	return pod, nil
}
