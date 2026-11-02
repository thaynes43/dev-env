package controller

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
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

// Publisher needs the per-launch receiver-create client shipped after 2.86.
// The format/version floor is not artifact/signature acceptance.
func ValidateInstructionPublisherImage(image string) error {
	if ValidateInstructionAgentImage(image) != nil {
		return errors.New("qualified instruction publisher image is unavailable")
	}
	version := strings.TrimPrefix(image, "ghcr.io/thaynes43/dev-env:2.")
	minorText, _, _ := strings.Cut(version, ".")
	minor, err := strconv.Atoi(minorText)
	if err != nil || minor < 87 {
		return errors.New("instruction publisher image must contain the per-launch receiver control client (2.87 or newer)")
	}
	return nil
}

func instructionPublisherImageEligible(s *v1alpha1.AgentSession) bool {
	if s == nil || !s.Spec.CoordinatorInstructionPublisher || s.Spec.CoordinatorInstructions ||
		s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Profile != "dev" ||
		(s.Spec.Agent != v1alpha1.AgentClaude && s.Spec.Agent != v1alpha1.AgentCodex) ||
		s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil || projectcanary.IsPilot(s) || budgetBound(s) ||
		s.Spec.Restore != "" || len(s.Spec.Tools) != 0 || s.Spec.LLM != nil || s.Spec.Caller != "" || s.Spec.Lane != "" ||
		s.Spec.CoordinatorInstructionParentUID != "" || strings.Count(s.Spec.Parent, "/") != 1 {
		return false
	}
	return s.Labels[v1alpha1.LabelDepth] == "0" || s.Labels[v1alpha1.LabelDepth] == "1"
}

// applyInstructionAgentImage never touches a live Pod. Only a new ordinary
// explicitly selected publisher or enrolled receiver is eligible. It cannot fall back to a template
// or pilot image when qualification is missing or overlapping.
func (r *Reconciler) applyInstructionAgentImage(pod *corev1.Pod, s *v1alpha1.AgentSession) error {
	if s == nil || (!s.Spec.CoordinatorInstructions && !s.Spec.CoordinatorInstructionPublisher) {
		return nil
	}
	eligible := coordinatorInstructionEnvEnabled(s, false)
	if s.Spec.CoordinatorInstructionPublisher {
		eligible = instructionPublisherImageEligible(s) && ValidateInstructionPublisherImage(r.CoordinatorInstructionAgentImage) == nil
	}
	if pod == nil || s.UID == "" || !metav1.IsControlledBy(pod, s) || isHoldPod(pod) ||
		!eligible || ValidateInstructionAgentImage(r.CoordinatorInstructionAgentImage) != nil {
		return errors.New("selected instruction task needs an ordinary owned Pod and qualified agent image")
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
