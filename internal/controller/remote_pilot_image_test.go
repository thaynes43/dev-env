package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

const remotePilotTestImage = "ghcr.io/thaynes43/dev-env:2.69.0@sha256:0000000000000000000000000000000000000000000000000000000000000005"

func remotePilotTestAgent() *PilotAgent {
	return &PilotAgent{Image: remotePilotTestImage, Parent: "dev/dev-env", Key: "remote-pilot-1"}
}

func remotePilotTestSession() *v1alpha1.AgentSession {
	s := pilotTestSession()
	s.Spec.Agent, s.Spec.Model = v1alpha1.AgentCodex, "gpt-6.1-sol"
	s.Spec.Parent = "dev/dev-env"
	s.Labels[v1alpha1.LabelIdempotencyKey] = "remote-pilot-1"
	return s
}

func assertRemotePilotPodBytes(t *testing.T, want, got *corev1.Pod) {
	t.Helper()
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w, g) {
		t.Fatal("pod JSON bytes changed beyond the expected image")
	}
}

func TestPilotRemoteExactMatchChangesOnlyImage(t *testing.T) {
	s := remotePilotTestSession()
	tmpl := exampleTemplates(t)
	r := &Reconciler{APIURL: "https://api"}
	plain, err := r.sessionPod(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	r.RemotePilotAgent = remotePilotTestAgent()
	pod, err := r.sessionPod(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	if pod.Spec.Containers[0].Image != remotePilotTestImage {
		t.Fatalf("remote pilot image %q", pod.Spec.Containers[0].Image)
	}
	pod.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	assertRemotePilotPodBytes(t, plain, pod)
}

func TestPilotRemoteSelectionExclusionsPreserveBytes(t *testing.T) {
	s := remotePilotTestSession()
	r := &Reconciler{APIURL: "https://api"}
	plain, err := r.sessionPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*v1alpha1.AgentSession){
		"parent mismatch": func(s *v1alpha1.AgentSession) { s.Spec.Parent = "other/dev-env" },
		"parent prefix":   func(s *v1alpha1.AgentSession) { s.Spec.Parent += "-other" },
		"empty parent":    func(s *v1alpha1.AgentSession) { s.Spec.Parent = "" },
		"key mismatch":    func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelIdempotencyKey] = "other" },
		"key prefix":      func(s *v1alpha1.AgentSession) { s.Labels[v1alpha1.LabelIdempotencyKey] += "-other" },
		"key missing":     func(s *v1alpha1.AgentSession) { delete(s.Labels, v1alpha1.LabelIdempotencyKey) },
		"labels missing":  func(s *v1alpha1.AgentSession) { s.Labels = nil },
		"Claude remote":   func(s *v1alpha1.AgentSession) { s.Spec.Agent = v1alpha1.AgentClaude },
		"Codex local":     func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeLocal },
		"Codex task":      func(s *v1alpha1.AgentSession) { s.Spec.Mode = v1alpha1.ModeTask },
		"other repo":      func(s *v1alpha1.AgentSession) { s.Spec.Repo = "haynes-ops" },
		"workspace":       func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "w"} },
		"canary":          func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} },
		"private pilot":   func(s *v1alpha1.AgentSession) { s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} },
		"task budget":     func(s *v1alpha1.AgentSession) { s.Annotations = map[string]string{taskbudget.UIDAnnotation: "budget"} },
		"missing UID":     func(s *v1alpha1.AgentSession) { s.UID = "" },
		"wrong UID":       func(s *v1alpha1.AgentSession) { s.UID = "other-uid" },
	} {
		t.Run(name, func(t *testing.T) {
			x := s.DeepCopy()
			mutate(x)
			pod := plain.DeepCopy()
			applyRemotePilotImage(pod, x, remotePilotTestAgent())
			assertRemotePilotPodBytes(t, plain, pod)
		})
	}
	for name, config := range map[string]*PilotAgent{
		"nil": nil, "empty": {},
		"empty parent": {Image: remotePilotTestImage, Key: "remote-pilot-1"},
		"empty key":    {Image: remotePilotTestImage, Parent: "dev/dev-env"},
		"empty image":  {Parent: "dev/dev-env", Key: "remote-pilot-1"},
	} {
		t.Run(name, func(t *testing.T) {
			pod := plain.DeepCopy()
			applyRemotePilotImage(pod, s, config)
			assertRemotePilotPodBytes(t, plain, pod)
		})
	}
	// Defensive calls cannot change a hold pod, a foreign pod or a nil session.
	hold, err := buildHoldPod(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	wantHold := hold.DeepCopy()
	applyRemotePilotImage(hold, s, remotePilotTestAgent())
	assertRemotePilotPodBytes(t, wantHold, hold)
	foreign := plain.DeepCopy()
	foreign.OwnerReferences[0].UID = "foreign-owner"
	wantForeign := foreign.DeepCopy()
	applyRemotePilotImage(foreign, s, remotePilotTestAgent())
	assertRemotePilotPodBytes(t, wantForeign, foreign)
	applyRemotePilotImage(plain, nil, remotePilotTestAgent())
	applyRemotePilotImage(nil, s, remotePilotTestAgent())
	if plain.Spec.Containers[0].Image != exampleTemplates(t).Image {
		t.Fatal("nil session selected")
	}
}

func TestPilotRemoteUnsetPreservesPodBytes(t *testing.T) {
	tmpl := exampleTemplates(t)
	for _, s := range []*v1alpha1.AgentSession{taskSession(), remotePilotTestSession()} {
		plain, err := buildPod(s, tmpl, "https://api")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []*PilotAgent{nil, {}} {
			r := &Reconciler{APIURL: "https://api", RemotePilotAgent: p}
			pod, err := r.sessionPod(s, tmpl)
			if err != nil {
				t.Fatal(err)
			}
			assertRemotePilotPodBytes(t, plain, pod)
		}
	}
}

func TestPilotRemoteCoexistenceAndPrecedence(t *testing.T) {
	tmpl := exampleTemplates(t)
	legacy := pilotTestAgent()
	task := &PilotAgent{Image: "task-image", Parent: legacy.Parent, Key: "task-pilot-1"}
	r := &Reconciler{APIURL: "https://api", PilotAgent: legacy, TaskPilotAgent: task}
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeLocal, v1alpha1.ModeRemote} {
		s := pilotTestSession()
		s.Spec.Mode = mode
		plain, err := r.sessionPod(s, tmpl)
		if err != nil {
			t.Fatal(err)
		}
		r.RemotePilotAgent = remotePilotTestAgent()
		pod, err := r.sessionPod(s, tmpl)
		if err != nil {
			t.Fatal(err)
		}
		assertRemotePilotPodBytes(t, plain, pod)
	}
	// A legacy Codex remote selector wins even when the new tuple also matches.
	s := pilotTestSession()
	s.Spec.Agent, s.Spec.Model = v1alpha1.AgentCodex, "gpt-6.1-sol"
	r.RemotePilotAgent = &PilotAgent{Image: remotePilotTestImage, Parent: legacy.Parent, Key: legacy.Key}
	pod, err := r.sessionPod(s, tmpl)
	if err != nil || pod.Spec.Containers[0].Image != legacy.Image {
		t.Fatalf("legacy overlap precedence changed: %v", err)
	}
	// The task pilot still wins for its task, even with all tuples identical.
	s.Spec.Mode, s.Spec.Prompt = v1alpha1.ModeTask, "fixture"
	r.ManagedCodexTasks = true
	r.TaskPilotAgent = &PilotAgent{Image: task.Image, Parent: legacy.Parent, Key: legacy.Key}
	r.RemotePilotAgent = nil
	plain, err := r.sessionPod(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	r.RemotePilotAgent = &PilotAgent{Image: remotePilotTestImage, Parent: legacy.Parent, Key: legacy.Key}
	pod, err = r.sessionPod(s, tmpl)
	if err != nil || pod.Spec.Containers[0].Image != task.Image {
		t.Fatalf("task pilot image changed: %v", err)
	}
	assertRemotePilotPodBytes(t, plain, pod)
	// All flags remain set; a separate exact remote target gets its own image.
	r.RemotePilotAgent = remotePilotTestAgent()
	pod, err = r.sessionPod(remotePilotTestSession(), tmpl)
	if err != nil || pod.Spec.Containers[0].Image != remotePilotTestImage {
		t.Fatalf("remote pilot displaced by nonmatching existing pilots: %v", err)
	}
}

func TestPilotRemoteOriginalHomeVerifierUsesSelectedImage(t *testing.T) {
	s, pvc, pv, binding := originalFixture()
	s.Spec.Repo = "dev-env"
	s.Spec.Parent = "dev/dev-env"
	s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "remote-pilot-1"}
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, pvc, pv).Build()
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: cl, APIReader: cl, OriginalHome: &binding, RemotePilotAgent: remotePilotTestAgent()}
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: pvc}}
	if err := r.ensure(context.Background(), s, exampleTemplates(t), obs); err != nil {
		t.Fatal(err)
	}
	if obs.pod.obj == nil {
		t.Fatalf("guarded pod refused: blocked=%v build=%v", obs.removalBlocked, obs.buildErr)
	}
	pod := obs.pod.obj
	if pod.Spec.Containers[0].Image != remotePilotTestImage || len(pod.Spec.InitContainers) != 1 {
		t.Fatal("guarded remote pilot lacks selected image or init verifier")
	}
	init := pod.Spec.InitContainers[0]
	if init.Name != "original-home-verify" || init.Image != remotePilotTestImage || len(init.Args) != 1 || init.Args[0] != "home-verify" || !init.VolumeMounts[0].ReadOnly {
		t.Fatal("original HOME verifier did not inherit the remote pilot image and read-only mount")
	}
}
