package controller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
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

// Pilot image selection does not make remote sessions eligible for automatic
// drain. An idle local pilot may still drain when its template revision changes.
func TestPilotAgentPodDrainRespectsRemoteMode(t *testing.T) {
	tmpl := exampleTemplates(t)
	for _, tc := range []struct {
		name     string
		agent    v1alpha1.AgentKind
		mode     v1alpha1.SessionMode
		outdated bool
	}{
		{"claude remote", v1alpha1.AgentClaude, v1alpha1.ModeRemote, false},
		{"codex remote", v1alpha1.AgentCodex, v1alpha1.ModeRemote, false},
		{"claude local", v1alpha1.AgentClaude, v1alpha1.ModeLocal, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := pilotTestSession()
			s.Spec.Agent, s.Spec.Mode = tc.agent, tc.mode
			if tc.agent == v1alpha1.AgentCodex {
				s.Spec.Model = "gpt-6-astra"
			}
			s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle}
			pod := buildPilotPod(t, s, pilotTestAgent())
			if pod.Spec.Containers[0].Image != pilotTestImage {
				t.Fatal("pilot image not selected")
			}
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			if drainEligible(s, pod, tmpl.Revision()) {
				t.Fatal("current pilot revision was eligible")
			}
			if got := drainEligible(s, pod, "next-revision"); got != tc.outdated {
				t.Fatalf("outdated eligible %v, want %v", got, tc.outdated)
			}
		})
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

func TestTaskPilotSelectionAndOwnership(t *testing.T) {
	s := taskSession()
	s.Spec.Repo = "dev-env"
	s.Spec.Base = "main"
	s.Spec.Agent = v1alpha1.AgentCodex
	s.Spec.Model = "gpt-6.1-terra"
	s.Spec.Parent = "dev-env-1027-111645"
	s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "history-pilot-1"}
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"dev-env":{"github":"thaynes43/dev-env"}},"projects":{"sample":{"repositories":[{"name":"dev-env"}],"rules":"fixture"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "dev-env")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(raw)}
	tmpl := exampleTemplates(t)
	r := &Reconciler{APIURL: "https://api", ManagedCodexTasks: true, TaskPilotAgent: pilotTestAgent()}
	pod, err := r.sessionPod(s, tmpl)
	if err != nil || pod.Spec.Containers[0].Image != pilotTestImage || pod.Labels[v1alpha1.LabelRevision] != tmpl.Revision() {
		t.Fatal("selected task pod", err)
	}
	off := &Reconciler{APIURL: "https://api", ManagedCodexTasks: true}
	plain, err := off.sessionPod(s, tmpl)
	if err != nil {
		t.Fatal(err)
	}
	pod.Spec.Containers[0].Image = plain.Spec.Containers[0].Image
	if !apiequality.Semantic.DeepEqual(pod, plain) {
		t.Fatal("changed more than image")
	}
	for name, mutate := range map[string]func(*v1alpha1.AgentSession){
		"missing parent": func(x *v1alpha1.AgentSession) { x.Spec.Parent = "" },
		"wrong parent":   func(x *v1alpha1.AgentSession) { x.Spec.Parent = "other" },
		"missing key":    func(x *v1alpha1.AgentSession) { delete(x.Labels, v1alpha1.LabelIdempotencyKey) },
		"wrong key":      func(x *v1alpha1.AgentSession) { x.Labels[v1alpha1.LabelIdempotencyKey] = "other" },
		"provider":       func(x *v1alpha1.AgentSession) { x.Spec.Agent = v1alpha1.AgentClaude },
		"mode":           func(x *v1alpha1.AgentSession) { x.Spec.Mode = v1alpha1.ModeRemote },
		"repo":           func(x *v1alpha1.AgentSession) { x.Spec.Repo = "other" },
		"workspace":      func(x *v1alpha1.AgentSession) { x.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "x"} },
		"canary":         func(x *v1alpha1.AgentSession) { x.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} },
		"private pilot":  func(x *v1alpha1.AgentSession) { x.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} },
		"missing UID":    func(x *v1alpha1.AgentSession) { x.UID = "" },
		"wrong UID":      func(x *v1alpha1.AgentSession) { x.UID = "other-uid" },
		"budget":         func(x *v1alpha1.AgentSession) { x.Annotations[taskbudget.UIDAnnotation] = "budget" },
	} {
		t.Run(name, func(t *testing.T) {
			x := s.DeepCopy()
			mutate(x)
			p := plain.DeepCopy()
			applyTaskPilotImage(p, x, pilotTestAgent())
			if p.Spec.Containers[0].Image != tmpl.Image {
				t.Fatal("excluded task selected")
			}
		})
	}
	for _, name := range []string{"wrong owner", "hold"} {
		t.Run(name, func(t *testing.T) {
			p := plain.DeepCopy()
			if name == "wrong owner" {
				p.OwnerReferences[0].UID = "other"
			} else {
				p.Labels[v1alpha1.LabelHold] = "true"
			}
			applyTaskPilotImage(p, s, pilotTestAgent())
			if p.Spec.Containers[0].Image != tmpl.Image {
				t.Fatal("foreign or hold pod selected")
			}
		})
	}
}

func TestTaskPilotCannotBypassBudgetTemplateImageAdmission(t *testing.T) {
	svc, binding, _, _ := privateLaunchFixture(t, "ready")
	inspector, ok := svc.ManagedInspector.(ManagedPodAdmissionInspector)
	if !ok {
		t.Fatal("unexpected budget inspector")
	}
	inspector.Image = pilotTestImage
	svc.ManagedInspector = inspector
	if _, err := svc.ReserveManagedLaunch(context.Background(), binding, "child:finite"); !errors.Is(err, taskbudget.ErrDenied) {
		t.Fatal("template and worker mismatch accepted", err)
	}
}

func TestTaskPilotCannotBypassConstructedBudgetImageCheck(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	f.s.Spec.Parent = "system/parent"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.PodName = ""
	c := f.client()
	svc, binding := budgetService(t, c, f.now)
	svc.ManagedInspector = managedLaunchFixture{}
	f.s.Annotations = budgetAnnotations(binding, "child")
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: binding, ID: "dispatch", Kind: "worker-start", WorkerID: "child"}); err != nil {
		t.Fatal(err)
	}
	r, obs := workspaceFixtureController(t, f, c)
	r.TaskBudgets = svc
	r.TaskPilotAgent = pilotTestAgent()
	r.TaskBudgetWorkerImage = pilotTestImage
	obs.pod = owned[*corev1.Pod]{missing: true}
	if obs.templates.Image == pilotTestImage {
		t.Fatal("fixture needs different images")
	}
	if err := r.ensure(ctx, f.s, obs.templates, &obs); err != nil || obs.removalBlocked == nil {
		t.Fatal("mismatched constructed worker image accepted", err)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.s), &pod); !apierrors.IsNotFound(err) {
		t.Fatal("mismatched worker pod not confirmed absent", err)
	}
}

func TestTaskPilotReconcileRetainedFirstExecutor(t *testing.T) {
	s := taskSession()
	s.Generation = 1
	s.Spec.Repo = "dev-env"
	s.Spec.Base = "main"
	s.Spec.Agent = v1alpha1.AgentCodex
	s.Spec.Model = "gpt-6.1-terra"
	s.Spec.Parent = "dev-env-1027-111645"
	s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "history-pilot-1"}
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"dev-env":{"github":"thaynes43/dev-env"}},"projects":{"sample":{"repositories":[{"name":"dev-env"}],"rules":"fixture"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "dev-env")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	s.Annotations = map[string]string{v1alpha1.AnnotationProjectSnapshot: string(raw)}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
	base := privateHistoryClient(s, cm)
	creates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		switch obj := o.(type) {
		case *corev1.PersistentVolumeClaim:
			obj.UID = "original-home"
		case *corev1.Pod:
			var current v1alpha1.AgentSession
			if err := c.Get(ctx, client.ObjectKeyFromObject(s), &current); err != nil {
				t.Fatal(err)
			}
			h := current.Status.PrivateHistory
			if h == nil || h.HomeUID != "original-home" || !h.ExecutorAttempted || h.ExecutorIntent == "" || obj.Annotations[privateHistoryHomeAnnotation] != "original-home" || obj.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent {
				t.Fatal("missing retained admission or executor intent")
			}
			creates++
			obj.UID = "original-executor"
		}
		return c.Create(ctx, o, opts...)
	}})
	r := &Reconciler{Client: c, APIReader: base, Templates: templatesKey, PrivateHistoryRetention: true, ManagedCodexTasks: true, TaskPilotAgent: pilotTestAgent()}
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); err != nil {
		t.Fatal(err)
	}
	if creates != 1 || pod.Spec.Containers[0].Image != pilotTestImage {
		t.Fatalf("retained first executor did not select pilot: creates=%d image=%s", creates, pod.Spec.Containers[0].Image)
	}
	original := pod.DeepCopy()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); err != nil || !apiequality.Semantic.DeepEqual(original, &pod) {
		t.Fatal("existing executor changed", err)
	}
	if err := base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
			t.Fatal(err)
		}
	}
	var retained v1alpha1.AgentSession
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &retained); err != nil {
		t.Fatal(err)
	}
	h := retained.Status.PrivateHistory
	if creates != 1 || h == nil || h.HomeUID != "original-home" || !h.ExecutorAttempted || h.ExecutorIntent == "" || h.LastExecutor == nil || h.LastExecutor.PodUID != "original-executor" || h.StopState != "Unknown" {
		t.Fatal("retained executor was replaced or history lost")
	}
	var home corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKey{Namespace: s.Namespace, Name: HomeClaimName(s.Name)}, &home); err != nil || home.UID != "original-home" || !containsFinalizer(home.Finalizers, Finalizer) {
		t.Fatal("HOME protection changed", err)
	}
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &pod); !apierrors.IsNotFound(err) {
		t.Fatal("replacement executor exists", err)
	}
}

func TestTaskPilotRetainedHistoryRefusesReplacement(t *testing.T) {
	for _, scenario := range []string{"unknown", "previous attempt", "foreign home", "existing executor", "foreign pod"} {
		t.Run(scenario, func(t *testing.T) {
			s, home := privateHistoryFixture()
			s.Spec.Agent = v1alpha1.AgentCodex
			s.Spec.Model = "gpt-6.1-terra"
			s.Spec.Mode = v1alpha1.ModeTask
			s.Spec.Repo = "dev-env"
			s.Spec.Base = "main"
			s.Spec.Parent = "dev-env-1027-111645"
			s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
			s.Labels = map[string]string{v1alpha1.LabelIdempotencyKey: "history-pilot-1"}
			s.Status.SuspendedAt = nil
			s.Status.Rescue = nil
			h := s.Status.PrivateHistory
			h.Provider = "codex"
			h.ExecutorAttempted = true
			h.ExecutorIntent = "original-intent"
			if scenario == "unknown" {
				h.Admission = "Unknown"
				h.LastExecutor = nil
				h.ExecutorAttempted = false
				h.ExecutorIntent = ""
			}
			if scenario == "foreign home" {
				home.OwnerReferences[0].UID = "foreign-home-owner"
				h.LastExecutor = nil
				h.ExecutorAttempted = false
				h.ExecutorIntent = ""
			}
			var pod *corev1.Pod
			if scenario == "existing executor" || scenario == "foreign pod" {
				pod = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "original-executor", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}, Annotations: map[string]string{privateHistoryExecutorAnnotation: h.ExecutorIntent, privateHistoryHomeAnnotation: h.HomeUID}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: ContainerName, Image: "original-image"}}}}
				if scenario == "foreign pod" {
					pod.OwnerReferences[0].UID = "foreign"
				}
			}
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
			objs := []client.Object{s, home, cm}
			if pod != nil {
				objs = append(objs, pod)
			}
			base := privateHistoryClient(objs...)
			creates := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				if _, ok := o.(*corev1.Pod); ok {
					creates++
				}
				return c.Create(ctx, o, opts...)
			}})
			r := &Reconciler{Client: c, APIReader: base, Templates: templatesKey, PrivateHistoryRetention: true, ManagedCodexTasks: true, TaskPilotAgent: pilotTestAgent()}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
			var got v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
				t.Fatal(err)
			}
			if creates != 0 || got.Status.PrivateHistory == nil || got.Status.PrivateHistory.HomeUID != h.HomeUID || got.Status.PrivateHistory.ExecutorIntent != h.ExecutorIntent || got.Status.PrivateHistory.ExecutorAttempted != h.ExecutorAttempted || !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.LastExecutor, h.LastExecutor) || !apiequality.Semantic.DeepEqual(got.Finalizers, s.Finalizers) {
				t.Fatal("retained intent changed or executor rearmed")
			}
			var actualHome corev1.PersistentVolumeClaim
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(home), &actualHome); err != nil || actualHome.UID != home.UID || !apiequality.Semantic.DeepEqual(actualHome.Finalizers, home.Finalizers) {
				t.Fatal("HOME changed", err)
			}
			var actualPod corev1.Pod
			err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &actualPod)
			if pod == nil {
				if !apierrors.IsNotFound(err) {
					t.Fatal("unexpected pod", err)
				}
			} else if err != nil || actualPod.UID != pod.UID || actualPod.Spec.Containers[0].Image != "original-image" {
				t.Fatal("existing pod changed", err)
			}
		})
	}
}
