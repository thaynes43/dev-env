package controller

import (
	"errors"
	"regexp"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

var instructionAgentImage = regexp.MustCompile(`^ghcr\.io/thaynes43/dev-env:2\.[0-9]+\.[0-9]+@sha256:[a-f0-9]{64}$`)

// ValidateInstructionAgentImage validates a release reference, not its signature.
// Publication and signature verification are separate delivery prerequisites.
func ValidateInstructionAgentImage(image string) error {
	if !instructionAgentImage.MatchString(image) {
		return errors.New("qualified instruction receiver image is unavailable")
	}
	return nil
}

// applyInstructionAgentImage never touches a live Pod. Only a new ordinary
// explicitly enrolled receiver is eligible. It cannot fall back to a template
// or pilot image when qualification is missing or overlapping.
func (r *Reconciler) applyInstructionAgentImage(pod *corev1.Pod, s *v1alpha1.AgentSession) error {
	if s == nil || !s.Spec.CoordinatorInstructions {
		return nil
	}
	if pod == nil || s.UID == "" || !metav1.IsControlledBy(pod, s) || isHoldPod(pod) ||
		!coordinatorInstructionEnvEnabled(s, false) || ValidateInstructionAgentImage(r.CoordinatorInstructionAgentImage) != nil {
		return errors.New("enrolled instruction receiver needs an ordinary owned Pod and qualified agent image")
	}
	snapshot, err := projectcatalog.ParseSnapshot([]byte(s.Annotations[v1alpha1.AnnotationProjectSnapshot]))
	if err != nil || snapshot.Selected().Name != s.Spec.Repo {
		return errors.New("enrolled instruction receiver project binding is invalid")
	}
	// A legacy parent/key selection is not this feature's delivery route, even
	// if it happens to select the same bytes. Refuse ambiguity rather than
	// overriding another mapping or treating it as qualification.
	for _, target := range []*PilotAgent{r.PilotAgent, r.TaskPilotAgent, r.RemotePilotAgent} {
		if target != nil && target.Parent == s.Spec.Parent && target.Key != "" && target.Key == s.Labels[v1alpha1.LabelIdempotencyKey] {
			return errors.New("enrolled instruction receiver overlaps a pilot image selection")
		}
	}
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == ContainerName {
			pod.Spec.Containers[i].Image = r.CoordinatorInstructionAgentImage
			return nil
		}
	}
	return errors.New("enrolled instruction receiver agent container is absent")
}
