package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestDepth3Readers is the section 8.3 row for the instruction publisher.
// The same session at depth 1 is eligible, so the refusal is the depth label.
func TestDepth3Readers(t *testing.T) {
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{v1alpha1.LabelDepth: "1"}},
		Spec: v1alpha1.AgentSessionSpec{
			CoordinatorInstructionPublisher: true,
			Mode:                            v1alpha1.ModeTask,
			Profile:                         "dev",
			Agent:                           v1alpha1.AgentClaude,
			Parent:                          "dev-env-system/dev-env-human",
		},
	}
	if !instructionPublisherImageEligible(s) {
		t.Fatal("depth 1 was not eligible")
	}
	s.Labels[v1alpha1.LabelDepth] = "3"
	if instructionPublisherImageEligible(s) {
		t.Fatal("depth 3 was eligible")
	}
}
