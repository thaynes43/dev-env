package controller

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestInstructionAgentImageOrdinaryEnrollmentAndIsolation(t *testing.T) {
	image := "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
	r := &Reconciler{CoordinatorInstructionAgentImage: image}
	s := ordinaryOptInSession(t)
	plain, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Spec.CoordinatorInstructions = true
	got, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Containers[0].Image != image || len(instructionValues(got)) != 1 {
		t.Fatal("receiver image and enrollment diverged")
	}
	got.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	var env []corev1.EnvVar
	for _, e := range got.Spec.Containers[0].Env {
		if e.Name != coordinatorInstructionsEnv {
			env = append(env, e)
		}
	}
	got.Spec.Containers[0].Env = env
	if !apiequality.Semantic.DeepEqual(got.Spec, plain.Spec) {
		t.Fatal("receiver selection changed account, command, mounts or another pod setting")
	}
	// Builders produce prospective objects. No update/patch of an existing Pod
	// occurs here, and no UID is fabricated as evidence of installation.
	if got.UID != "" || got.OwnerReferences[0].UID != s.UID {
		t.Fatal("receiver identity changed")
	}
	plainOff, err := (&Reconciler{}).sessionPod(ordinaryOptInSession(t), exampleTemplates(t))
	if err != nil || !apiequality.Semantic.DeepEqual(plain.Spec, plainOff.Spec) {
		t.Fatal("configuration changed a non-enrolled task", err)
	}
}

func TestInstructionAgentImageRefusesMissingQualificationAndForeignScope(t *testing.T) {
	image := "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession, *Reconciler, *corev1.Pod)
	}{
		{"off", func(_ *v1alpha1.AgentSession, r *Reconciler, _ *corev1.Pod) { r.CoordinatorInstructionAgentImage = "" }},
		{"unsigned-tag", func(_ *v1alpha1.AgentSession, r *Reconciler, _ *corev1.Pod) {
			r.CoordinatorInstructionAgentImage = "ghcr.io/thaynes43/dev-env:2.86.0"
		}},
		{"foreign-owner", func(_ *v1alpha1.AgentSession, _ *Reconciler, p *corev1.Pod) { p.OwnerReferences[0].UID = "foreign" }},
		{"hold", func(_ *v1alpha1.AgentSession, _ *Reconciler, p *corev1.Pod) { p.Labels[v1alpha1.LabelHold] = "true" }},
		{"missing-parent", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Spec.CoordinatorInstructionParentUID = ""
		}},
		{"snapshot", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Annotations[v1alpha1.AnnotationProjectSnapshot] = "{}"
		}},
		{"foreign-repository", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) { s.Spec.Repo = "foreign" }},
		{"shared", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"}
		}},
		{"canary", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{}
		}},
		{"pilot", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Annotations[projectcanary.PilotAnnotation] = "marked"
		}},
		{"budget", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) {
			s.Annotations[taskbudget.UIDAnnotation] = "budget"
		}},
		{"remote", func(s *v1alpha1.AgentSession, _ *Reconciler, _ *corev1.Pod) { s.Spec.Mode = v1alpha1.ModeRemote }},
		{"pilot-overlap", func(s *v1alpha1.AgentSession, r *Reconciler, _ *corev1.Pod) {
			s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "ordinary"}
			r.TaskPilotAgent = &PilotAgent{Image: image, Parent: s.Spec.Parent, Key: "ordinary"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ordinaryOptInSession(t)
			s.Spec.CoordinatorInstructions = true
			p, err := buildPod(s, exampleTemplates(t), "https://api")
			if err != nil {
				t.Fatal(err)
			}
			r := &Reconciler{CoordinatorInstructionAgentImage: image}
			tc.edit(s, r, p)
			before := p.DeepCopy()
			if err := r.applyInstructionAgentImage(p, s); err == nil {
				t.Fatal("accepted unsafe receiver")
			}
			if !apiequality.Semantic.DeepEqual(before, p) {
				t.Fatal("refused selection mutated Pod")
			}
		})
	}
}
