package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
	"github.com/thaynes43/dev-env/internal/taskbudget"
	"github.com/thaynes43/dev-env/internal/templates"
)

// DESIGN-054 S3c. A pod is never updated after create, and a change in a pod's
// shape drains idle sessions onto the new shape, so running sessions are
// sacred: a session without the scheduled-work start-deadline annotation must
// get exactly the pod it got before the deadline existed.
//
// This file uses only what main had before S3c, so the same file generates
// the golden on that commit and checks it on every later one.

// legacyPodsGolden holds the SHA-256 of each pod below, marshaled to JSON, as
// the controller built it before S3c (main ef2802b): equal digests are equal
// bytes. Regenerate it only for a pod change that is meant to reach every
// session, or to add a pod, on the commit before the change:
//
//	DEV_ENV_UPDATE_GOLDEN=1 go test ./internal/controller -run TestPodsWithoutStartDeadlineAreUnchanged
const legacyPodsGolden = "testdata/legacy-pods.sha256.json"

const legacyPodsAPI = "https://dev-env-operator.dev-env-system.svc:8443"

// legacyPods are pods of each kind the controller builds. The builder's own
// pods: every agent and mode pair, the managed Codex task, a session with
// other annotations and the coordinator opt-in, a pod without an API, and a
// rescue (hold) pod. Then the pods the reconciler creates through
// r.sessionPod (legacySessionPods).
func legacyPods(t *testing.T) map[string]*corev1.Pod {
	t.Helper()
	tmpl := exampleTemplates(t)
	build := func(pod *corev1.Pod, err error) *corev1.Pod {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return pod
	}
	interactive := func(agent v1alpha1.AgentKind, mode v1alpha1.SessionMode, model string) *corev1.Pod {
		s := taskSession()
		s.Spec.Agent, s.Spec.Mode, s.Spec.Model = agent, mode, model
		s.Spec.Prompt, s.Spec.Limits = "", nil
		return build(buildPod(s, tmpl, legacyPodsAPI))
	}
	codexTask := taskSession()
	codexTask.Spec.Agent, codexTask.Spec.Model = v1alpha1.AgentCodex, "gpt-6-astra"
	optIn := ordinaryOptInSession(t)
	optIn.Spec.CoordinatorInstructions = true
	pods := map[string]*corev1.Pod{
		"claude-task":        build(buildPod(taskSession(), tmpl, legacyPodsAPI)),
		"claude-task-no-api": build(buildPod(taskSession(), tmpl, "")),
		"claude-local":       interactive(v1alpha1.AgentClaude, v1alpha1.ModeLocal, "claude-opus-5-5"),
		"claude-remote":      interactive(v1alpha1.AgentClaude, v1alpha1.ModeRemote, "claude-opus-5-5"),
		"codex-remote":       interactive(v1alpha1.AgentCodex, v1alpha1.ModeRemote, "gpt-6-astra"),
		"codex-task-managed": build(buildPod(codexTask, tmpl, legacyPodsAPI, true)),
		"coordinator-opt-in": build(buildPod(optIn, tmpl, legacyPodsAPI)),
		"hold":               build(buildHoldPod(taskSession(), tmpl)),
	}
	for name, pod := range legacySessionPods(t) {
		pods[name] = pod
	}
	return pods
}

// taskPilotSession is an ordinary Codex task the task pilot selects.
func taskPilotSession(t *testing.T) *v1alpha1.AgentSession {
	t.Helper()
	s := taskSession()
	s.Spec.Repo, s.Spec.Base = "dev-env", "main"
	s.Spec.Agent, s.Spec.Model = v1alpha1.AgentCodex, "gpt-6.1-terra"
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
	return s
}

// legacySessionPods are pods built the way the reconciler's ensure builds
// them, through r.sessionPod with the reconciler's feature settings: a shared
// workspace task, a task-budget worker in it, a managed child-decision task, a
// private-history task under terminal task reclaim, an original-HOME task with
// its init verifier, and each image selection (pilot, task pilot, remote
// pilot, Codex Remote Control, instruction receiver and publisher). Not here:
// a project canary or private pilot pod, whose ensure path needs the canary's
// live directory and retention reads (validateProjectCanary,
// validatePrivatePilot) and its common-rules binding, so no unit fixture
// builds one; and ensure's later edits of a budget pod (its active deadline
// and checkpoint), which need the budget ledger.
func legacySessionPods(t *testing.T) map[string]*corev1.Pod {
	t.Helper()
	workspaceTemplates := func() *templates.Templates {
		tmpl := exampleTemplates(t)
		tmpl.Workspace = &templates.Workspace{Enabled: true, Claim: "retained-projects", ID: "projects-v2"}
		return tmpl
	}
	build := func(r *Reconciler, s *v1alpha1.AgentSession, tmpl *templates.Templates) *corev1.Pod {
		t.Helper()
		if r.APIURL == "" {
			r.APIURL = legacyPodsAPI
		}
		pod, err := r.sessionPod(s, tmpl, r.resolveCodexHost(s, tmpl))
		if err != nil {
			t.Fatal(err)
		}
		return pod
	}
	pods := map[string]*corev1.Pod{}

	workspace := taskSession()
	workspace.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	pods["session-workspace"] = build(&Reconciler{}, workspace, workspaceTemplates())

	budget := taskSession()
	budget.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	budget.Spec.Parent = "system/parent"
	budget.Annotations = map[string]string{
		taskbudget.UIDAnnotation: "budget-task", taskbudget.EpochAnnotation: "1",
		taskbudget.DeadlineAnnotation: "2026-11-04T10:45:00Z", taskbudget.HostAnnotation: "budget-host",
		taskbudget.PodAnnotation: "root-pod", taskbudget.WorkerAnnotation: "child-1",
	}
	pods["session-task-budget"] = build(&Reconciler{}, budget, workspaceTemplates())

	decision := taskSession()
	decision.Spec.Agent, decision.Spec.Model, decision.Spec.Effort, decision.Spec.Limits = v1alpha1.AgentCodex, "gpt-6.1-sol", "xhigh", nil
	decision.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects-v2"}
	decision.Spec.Parent = "dev/configured-coordinator"
	pods["session-child-decision"] = build(&Reconciler{ManagedCodexTasks: true, ManagedChildDecisions: true, ManagedChildDecisionParents: []string{decision.Spec.Parent}}, decision, workspaceTemplates())

	history := taskSession()
	history.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(history.UID), HomeClaim: HomeClaimName(history.Name), HomeUID: "home-uid", BindingGeneration: 1}
	pods["session-private-history"] = build(&Reconciler{TerminalTaskPodReclaim: true, PrivateTerminalRecord: true, PrivateExecutorStop: true}, history, exampleTemplates(t))

	home := taskSession()
	homePod := build(&Reconciler{}, home, exampleTemplates(t))
	addOriginalHomeVerifier(homePod, homeguard.Binding{Namespace: home.Namespace, Session: home.Name, SessionUID: string(home.UID), PVCUID: "home-uid", PV: "original-pv", PVUID: "pv-uid"})
	pods["session-original-home"] = homePod

	pods["session-pilot"] = build(&Reconciler{PilotAgent: pilotTestAgent()}, pilotTestSession(), exampleTemplates(t))
	pods["session-task-pilot"] = build(&Reconciler{ManagedCodexTasks: true, TaskPilotAgent: pilotTestAgent()}, taskPilotSession(t), exampleTemplates(t))
	pods["session-remote-pilot"] = build(&Reconciler{RemotePilotAgent: remotePilotTestAgent()}, remotePilotTestSession(), exampleTemplates(t))
	rc := remotePilotTestSession()
	rc.Spec.Repo = "haynes-ops"
	pods["session-codex-rc"] = build(&Reconciler{CodexRCAgent: &CodexRCAgent{Image: remotePilotTestImage, Name: rc.Name, UID: string(rc.UID)}}, rc, exampleTemplates(t))

	const digest = "@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := ordinaryOptInSession(t)
	receiver.Spec.CoordinatorInstructions = true
	pods["session-instruction-receiver"] = build(&Reconciler{CoordinatorInstructionAgentImage: "ghcr.io/thaynes43/dev-env:2.86.0" + digest}, receiver, exampleTemplates(t))
	publisher := publisherSession(t)
	publisher.Spec.CoordinatorInstructionPublisher = true
	pods["session-instruction-publisher"] = build(&Reconciler{CoordinatorInstructionAgentImage: "ghcr.io/thaynes43/dev-env:2.87.0" + digest}, publisher, exampleTemplates(t))
	return pods
}

func TestPodsWithoutStartDeadlineAreUnchanged(t *testing.T) {
	got := map[string]string{}
	pods := legacyPods(t)
	for name, pod := range pods {
		b, err := json.Marshal(pod)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		got[name] = hex.EncodeToString(sum[:])
	}
	if os.Getenv("DEV_ENV_UPDATE_GOLDEN") == "1" {
		b, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyPodsGolden, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(legacyPodsGolden)
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]string
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d pods, the test builds %d", len(want), len(got))
	}
	for name, sum := range got {
		if want[name] != sum {
			t.Errorf("%s: the pod is not byte-identical to its pre-S3c golden; a session without the start-deadline annotation must keep its pod", name)
		}
	}
}

// Each reconciler pod in the golden exercises the feature it is named for, so
// a fixture that drifted to an ordinary pod cannot pass as coverage.
func TestLegacySessionPodsExerciseTheirFeature(t *testing.T) {
	pods := legacySessionPods(t)
	env := func(name, key string) string {
		t.Helper()
		pod, ok := pods[name]
		if !ok {
			t.Fatalf("no %s pod", name)
		}
		for _, e := range pod.Spec.Containers[0].Env {
			if e.Name == key {
				return e.Value
			}
		}
		return ""
	}
	if env("session-workspace", templates.WorkspaceIDEnv) != "projects-v2" {
		t.Error("session-workspace is not a shared workspace pod")
	}
	if env("session-task-budget", protocol.TaskBudgetEnv) == "" {
		t.Error("session-task-budget carries no task budget")
	}
	if env("session-child-decision", "AGENTD_ENABLE_CHILD_DECISIONS") != "true" {
		t.Error("session-child-decision does not enable child decisions")
	}
	sess, err := protocol.ParseSession([]byte(env("session-private-history", protocol.SessionEnv)))
	if err != nil || !sess.PrivateHistoryRetained {
		t.Errorf("session-private-history does not retain its history: %v", err)
	}
	if init := pods["session-original-home"].Spec.InitContainers; len(init) != 1 || init[0].Name != "original-home-verify" || env("session-original-home", homeguard.Env) == "" {
		t.Error("session-original-home has no original-HOME verifier")
	}
	for name, image := range map[string]string{
		"session-pilot":                 pilotTestImage,
		"session-task-pilot":            pilotTestImage,
		"session-remote-pilot":          remotePilotTestImage,
		"session-codex-rc":              remotePilotTestImage,
		"session-instruction-receiver":  "ghcr.io/thaynes43/dev-env:2.86.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"session-instruction-publisher": "ghcr.io/thaynes43/dev-env:2.87.0@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		if got := pods[name].Spec.Containers[0].Image; got != image {
			t.Errorf("%s runs %s, not its selected image", name, got)
		}
	}
}
