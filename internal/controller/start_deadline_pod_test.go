package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// DESIGN-054 S3c. A pod is never updated after create, and a change in a pod's
// shape drains idle sessions onto the new shape, so running sessions are
// sacred: a session without the scheduled-work start-deadline annotation must
// get exactly the pod it got before the deadline existed.

// legacyPodsGolden holds the SHA-256 of each pod below, marshaled to JSON, as
// the controller built it before S3c (main ef2802b): equal digests are equal
// bytes. Regenerate it only for a pod change that is meant to reach every
// session:
//
//	DEV_ENV_UPDATE_GOLDEN=1 go test ./internal/controller -run TestPodsWithoutStartDeadlineAreUnchanged
const legacyPodsGolden = "testdata/legacy-pods.sha256.json"

// legacyPods are pods of each kind the controller builds: every agent and mode
// pair, the managed Codex task, a session with other annotations and the
// coordinator opt-in, a pod without an API, and a rescue (hold) pod.
func legacyPods(t *testing.T) map[string]*corev1.Pod {
	t.Helper()
	tmpl := exampleTemplates(t)
	const api = "https://dev-env-operator.dev-env-system.svc:8443"
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
		return build(buildPod(s, tmpl, api))
	}
	codexTask := taskSession()
	codexTask.Spec.Agent, codexTask.Spec.Model = v1alpha1.AgentCodex, "gpt-6-astra"
	optIn := ordinaryOptInSession(t)
	optIn.Spec.CoordinatorInstructions = true
	return map[string]*corev1.Pod{
		"claude-task":        build(buildPod(taskSession(), tmpl, api)),
		"claude-task-no-api": build(buildPod(taskSession(), tmpl, "")),
		"claude-local":       interactive(v1alpha1.AgentClaude, v1alpha1.ModeLocal, "claude-opus-5-5"),
		"claude-remote":      interactive(v1alpha1.AgentClaude, v1alpha1.ModeRemote, "claude-opus-5-5"),
		"codex-remote":       interactive(v1alpha1.AgentCodex, v1alpha1.ModeRemote, "gpt-6-astra"),
		"codex-task-managed": build(buildPod(codexTask, tmpl, api, true)),
		"coordinator-opt-in": build(buildPod(optIn, tmpl, api)),
		"hold":               build(buildHoldPod(taskSession(), tmpl)),
	}
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
