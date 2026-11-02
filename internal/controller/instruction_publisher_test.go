package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func publisherSession(t *testing.T) *v1alpha1.AgentSession {
	s := ordinaryOptInSession(t)
	s.Spec.Parent = "client/ordinary"
	s.Spec.CoordinatorInstructionParentUID = ""
	s.Labels = map[string]string{v1alpha1.LabelDepth: "0"}
	return s
}

func TestInstructionPublisherImageChangesOnlyProspectiveImage(t *testing.T) {
	r := &Reconciler{CoordinatorInstructionAgentImage: "ghcr.io/thaynes43/dev-env:2.87.0@sha256:" + strings.Repeat("a", 64)}
	s := publisherSession(t)
	plain, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Spec.CoordinatorInstructionPublisher = true
	got, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Containers[0].Image != r.CoordinatorInstructionAgentImage || len(instructionValues(got)) != 0 {
		t.Fatal("publisher image or receiver isolation failed")
	}
	got.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	if !apiequality.Semantic.DeepEqual(got.Spec, plain.Spec) {
		t.Fatal("publisher changed mounts, account, provider command or environment")
	}
}

func TestInstructionPublisherImageFailsClosedForOtherScopes(t *testing.T) {
	for name, edit := range map[string]func(*v1alpha1.AgentSession, *Reconciler, *corev1.Pod){
		"old image": func(_ *v1alpha1.AgentSession, r *Reconciler, _ *corev1.Pod) {
			r.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
		},
		"missing image":  func(_ *v1alpha1.AgentSession, r *Reconciler, _ *corev1.Pod) { r.CoordinatorInstructionAgentImage = "" },
		"too deep":       func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) { s.Labels[v1alpha1.LabelDepth] = "2" },
		"receiver":       func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) { s.Spec.CoordinatorInstructions = true },
		"session parent": func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) { s.Spec.Parent = "another-session" },
		"shared": func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"}
		},
		"hold":          func(_ *v1alpha1.AgentSession, _ *Reconciler, p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" },
		"foreign owner": func(_ *v1alpha1.AgentSession, _ *Reconciler, p *corev1.Pod) { p.OwnerReferences[0].UID = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			s := publisherSession(t)
			s.Spec.CoordinatorInstructionPublisher = true
			p, err := buildPod(s, exampleTemplates(t), "https://api")
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{CoordinatorInstructionAgentImage: "ghcr.io/thaynes43/dev-env:2.87.0@sha256:" + strings.Repeat("a", 64)}
			edit(s, r, p)
			before := p.DeepCopy()
			if r.applyInstructionAgentImage(p, s) == nil || !apiequality.Semantic.DeepEqual(before, p) {
				t.Fatal("unsafe publisher selected or refusal mutated pod")
			}
		})
	}
}
