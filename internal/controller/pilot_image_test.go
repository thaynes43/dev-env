package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const pilotTestImage = "ghcr.io/thaynes43/dev-env:2.47.0@sha256:0000000000000000000000000000000000000000000000000000000000000009"

func pilotTestAgent() *PilotAgent {
	return &PilotAgent{Image: pilotTestImage, Parent: "dev-env-1027-111645", Key: "history-pilot-1"}
}

// pilotTestSession is a session the pilot selects.
func pilotTestSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Name = "dev-env-1027-120000"
	s.Spec.Repo = "dev-env"
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Prompt, s.Spec.Limits = "", nil // a prompt is for task mode only
	s.Spec.Parent = "dev-env-1027-111645"
	s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "history-pilot-1"}
	return s
}

// buildPilotPod builds the session's pod the way the reconciler does.
func buildPilotPod(t *testing.T, s *v1alpha1.AgentSession, p *PilotAgent) *corev1.Pod {
	t.Helper()
	pod, err := buildManagedPod(s, exampleTemplates(t), "https://api", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyPilotImage(pod, s, p)
	return pod
}

// With the pilot off, the pod of a session that would match is the same pod
// the builders give today.
func TestPilotAgentOffLeavesThePodUnchanged(t *testing.T) {
	s := pilotTestSession()
	want, err := buildPod(s, exampleTemplates(t), "https://api")
	if err != nil {
		t.Fatal(err)
	}
	got := buildPilotPod(t, s, nil)
	if !apiequality.Semantic.DeepEqual(want, got) {
		t.Fatal("a nil PilotAgent changed the pod")
	}
	if got.Spec.Containers[0].Image != exampleTemplates(t).Image {
		t.Fatalf("image %q, want the templates image", got.Spec.Containers[0].Image)
	}
	if empty := buildPilotPod(t, s, &PilotAgent{}); !apiequality.Semantic.DeepEqual(want, empty) {
		t.Fatal("an empty PilotAgent changed the pod")
	}
}

// The selected session's pod differs from the unselected pod in the image only.
func TestPilotAgentChangesOnlyTheImage(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := pilotTestSession()
	if got := pilotImageFor(s, pilotTestAgent()); got != pilotTestImage {
		t.Fatalf("pilotImageFor %q, want the pilot image", got)
	}
	plain := buildPilotPod(t, s, nil)
	pilot := buildPilotPod(t, s, pilotTestAgent())
	if len(pilot.Spec.Containers) != 1 || pilot.Spec.Containers[0].Image != pilotTestImage {
		t.Fatalf("containers %+v, want one on the pilot image", pilot.Spec.Containers)
	}
	if pilot.Labels[v1alpha1.LabelRevision] != tmpl.Revision() {
		t.Fatalf("revision label %q, want the templates revision %q", pilot.Labels[v1alpha1.LabelRevision], tmpl.Revision())
	}
	pilot.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	if !apiequality.Semantic.DeepEqual(plain, pilot) {
		t.Fatal("the pilot pod differs from the plain pod in more than the image")
	}
	// Local mode is selected too.
	s.Spec.Mode = v1alpha1.ModeLocal
	if pilotImageFor(s, pilotTestAgent()) != pilotTestImage {
		t.Fatal("a local session was not selected")
	}
}

// Every other session, and the hold pod, keeps the templates image.
func TestPilotAgentSelectionIsExact(t *testing.T) {
	tmpl := exampleTemplates(t)
	for name, mutate := range map[string]func(*v1alpha1.AgentSession){
		"other parent": func(s *v1alpha1.AgentSession) { s.Spec.Parent = "other-agent" },
		"no parent":    func(s *v1alpha1.AgentSession) { s.Spec.Parent = "" },
		"other key":    func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelIdempotencyKey] = "history-pilot-2" },
		"missing key":  func(s *v1alpha1.AgentSession) { delete(s.Labels, v1alpha1.LabelIdempotencyKey) },
		"no labels":    func(s *v1alpha1.AgentSession) { s.Labels = nil },
		"codex local": func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Mode, s.Spec.Model = v1alpha1.AgentCodex, v1alpha1.ModeLocal, "gpt-6-astra"
		},
		"codex task": func(s *v1alpha1.AgentSession) {
			s.Spec.Agent, s.Spec.Mode, s.Spec.Model, s.Spec.Prompt = v1alpha1.AgentCodex, v1alpha1.ModeTask, "gpt-6-astra", "fixture"
		},
		"unknown agent":  func(s *v1alpha1.AgentSession) { s.Spec.Agent = "unknown" },
		"task mode":      func(s *v1alpha1.AgentSession) { s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeTask, "fix the docs" },
		"other repo":     func(s *v1alpha1.AgentSession) { s.Spec.Repo = "haynes-ops" },
		"workspace":      func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "w"} },
		"project canary": func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} },
		"private pilot":  func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} },
	} {
		t.Run(name, func(t *testing.T) {
			s := pilotTestSession()
			mutate(s)
			if got := pilotImageFor(s, pilotTestAgent()); got != "" {
				t.Fatalf("pilotImageFor %q, want empty", got)
			}
			if s.Spec.Agent == "unknown" {
				if _, err := buildManagedPod(s, tmpl, "https://api", false, false, nil); err == nil {
					t.Fatal("unknown agent was accepted by pod builder")
				}
				return
			}
			if s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil {
				return // these specs need their own opt-ins to build a pod
			}
			if got := buildPilotPod(t, s, pilotTestAgent()).Spec.Containers[0].Image; got != tmpl.Image {
				t.Fatalf("image %q, want the templates image", got)
			}
		})
	}
	// An empty key or parent in the config selects nothing.
	s := pilotTestSession()
	s.Spec.Parent, s.Labels = "", nil
	if pilotImageFor(s, &PilotAgent{Image: pilotTestImage}) != "" {
		t.Fatal("an empty selector matched an unlabelled session")
	}
	if pilotImageFor(nil, pilotTestAgent()) != "" {
		t.Fatal("a nil session matched")
	}

	// The hold pod of the selected session stays on the templates image.
	hold, err := buildHoldPod(pilotTestSession(), tmpl)
	if err != nil {
		t.Fatal(err)
	}
	applyPilotImage(hold, pilotTestSession(), pilotTestAgent())
	if hold.Spec.Containers[0].Image != tmpl.Image {
		t.Fatalf("hold pod image %q, want the templates image", hold.Spec.Containers[0].Image)
	}
}

// The pilot pod carries the current templates revision, so a templates edit is
// the only thing that drains it.
func TestPilotAgentPodDrainsOnlyOnATemplatesChange(t *testing.T) {
	tmpl := exampleTemplates(t)
	s := pilotTestSession()
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle}
	pod := buildPilotPod(t, s, pilotTestAgent())
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if drainEligible(s, pod, tmpl.Revision()) {
		t.Fatal("a pilot pod on the current revision was eligible to drain")
	}
	if !drainEligible(s, pod, "next-revision") {
		t.Fatal("a pilot pod on an outdated revision was not eligible to drain")
	}
}

// The reconciler's own pod builder carries the pilot wiring: the selected
// session gets the pilot image, any other session the templates image, and a
// nil PilotAgent changes nothing.
func TestReconcilerSessionPodUsesThePilotImage(t *testing.T) {
	tmpl := exampleTemplates(t)
	r := &Reconciler{APIURL: "https://api", PilotAgent: pilotTestAgent()}
	selected := pilotTestSession()
	pod, err := r.sessionPod(selected, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if got := pod.Spec.Containers[0].Image; got != pilotTestImage {
		t.Fatalf("selected session image %q, want the pilot image", got)
	}
	other := pilotTestSession()
	other.Labels[v1alpha1.LabelIdempotencyKey] = "another-key"
	pod, err = r.sessionPod(other, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if got := pod.Spec.Containers[0].Image; got != tmpl.Image {
		t.Fatalf("other session image %q, want the templates image", got)
	}

	off := &Reconciler{APIURL: "https://api"}
	got, err := off.sessionPod(selected, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	want, err := buildManagedPod(selected, tmpl, off.APIURL, off.ManagedCodexTasks, off.ManagedChildDecisions, off.ManagedChildDecisionParents)
	if err != nil {
		t.Fatal(err)
	}
	if !apiequality.Semantic.DeepEqual(want, got) {
		t.Fatal("a Reconciler without PilotAgent changed the pod")
	}
}

func TestPilotAgentCodexRemoteChangesOnlyImage(t *testing.T) {
	s := pilotTestSession()
	s.Spec.Agent, s.Spec.Model = v1alpha1.AgentCodex, "gpt-6-astra"
	if got := pilotImageFor(s, pilotTestAgent()); got != pilotTestImage {
		t.Fatalf("Codex remote selector returned %q", got)
	}
	plain := buildPilotPod(t, s, nil)
	selected := buildPilotPod(t, s, pilotTestAgent())
	if len(selected.Spec.Containers) != 1 || selected.Spec.Containers[0].Image != pilotTestImage {
		t.Fatal("Codex remote pod lacks pilot image")
	}
	selected.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	if !apiequality.Semantic.DeepEqual(plain, selected) {
		t.Fatal("Codex remote pod changed beyond image")
	}
	r := &Reconciler{APIURL: "https://api", PilotAgent: pilotTestAgent()}
	pod, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil || pod.Spec.Containers[0].Image != pilotTestImage {
		t.Fatalf("reconciler did not select Codex remote: %v", err)
	}
}
