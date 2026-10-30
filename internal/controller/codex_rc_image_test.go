package controller

import (
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestCodexRCImageExactIdentityAndExclusions(t *testing.T) {
	s := remotePilotTestSession()
	s.Spec.Repo = "haynes-ops"
	target := &CodexRCAgent{Image: remotePilotTestImage, Name: s.Name, UID: string(s.UID)}
	r := &Reconciler{APIURL: "https://api"}
	plain, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	r.CodexRCAgent = target
	pod, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Containers[0].Image != target.Image {
		t.Fatal("target image not selected")
	}
	pod.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	assertRemotePilotPodBytes(t, plain, pod)
	for name, mutate := range map[string]func(*v1alpha1.AgentSession){
		"same name new UID":   func(s *v1alpha1.AgentSession) { s.UID = "replacement" },
		"same UID other name": func(s *v1alpha1.AgentSession) { s.Name += "-other" },
		"no UID":              func(s *v1alpha1.AgentSession) { s.UID = "" },
		"Claude":              func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentClaude },
		"task":                func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeTask },
		"local":               func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal },
		"workspace":           func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "w"} },
		"canary":              func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} },
		"private pilot":       func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} },
		"budget":              func(s *v1alpha1.AgentSession) { s.Annotations = map[string]string{taskbudget.UIDAnnotation: "budget"} },
	} {
		t.Run(name, func(t *testing.T) {
			x := s.DeepCopy()
			mutate(x)
			got := plain.DeepCopy()
			applyCodexRCImage(got, x, target)
			assertRemotePilotPodBytes(t, plain, got)
		})
	}
	for _, target := range []*CodexRCAgent{nil, {}, {Image: remotePilotTestImage, Name: s.Name}, {Image: remotePilotTestImage, UID: string(s.UID)}, {Name: s.Name, UID: string(s.UID)}} {
		got := plain.DeepCopy()
		applyCodexRCImage(got, s, target)
		assertRemotePilotPodBytes(t, plain, got)
	}
	for _, mutate := range []func(){func() { pod.Labels[v1alpha1.LabelHold] = "true" }, func() { pod.OwnerReferences = nil }} {
		pod = plain.DeepCopy()
		mutate()
		want := pod.DeepCopy()
		applyCodexRCImage(pod, s, target)
		assertRemotePilotPodBytes(t, want, pod)
	}
}

func TestCodexRCImagePreservesExistingRemotePilot(t *testing.T) {
	s := remotePilotTestSession()
	r := &Reconciler{RemotePilotAgent: remotePilotTestAgent()}
	want, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	r.CodexRCAgent = &CodexRCAgent{Image: "other-image", Name: s.Name, UID: string(s.UID)}
	got, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	assertRemotePilotPodBytes(t, want, got)
	r.CodexRCAgent.Name = "different-target"
	got, err = r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	assertRemotePilotPodBytes(t, want, got)
}
