package controller

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
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

func scheduledSession(s *v1alpha1.AgentSession, deadline string) *v1alpha1.AgentSession {
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = deadline
	return s
}

func startDeadlineValues(pod *corev1.Pod) []string {
	var got []string
	for _, e := range pod.Spec.Containers[0].Env {
		if e.Name == protocol.StartDeadlineEnv {
			got = append(got, e.Value)
		}
	}
	return got
}

func remoteSession() *v1alpha1.AgentSession {
	s := taskSession()
	s.Spec.Mode, s.Spec.Prompt, s.Spec.Limits = v1alpha1.ModeRemote, "", nil
	return s
}

// The annotation reaches agentd as one env var, and nothing else in the pod
// changes: not its metadata, volumes, image or any other variable.
func TestStartDeadlinePassesIntoThePod(t *testing.T) {
	tmpl := exampleTemplates(t)
	const deadline = "2026-11-04T10:05:00Z"
	for name, session := range map[string]func() *v1alpha1.AgentSession{"task": taskSession, "remote": remoteSession} {
		t.Run(name, func(t *testing.T) {
			plain, err := buildPod(session(), tmpl, "https://api")
			if err != nil {
				t.Fatal(err)
			}
			pod, err := buildPod(scheduledSession(session(), deadline), tmpl, "https://api")
			if err != nil {
				t.Fatal(err)
			}
			if got := startDeadlineValues(pod); len(got) != 1 || got[0] != deadline {
				t.Fatalf("start deadline env %q, want exactly [%s]", got, deadline)
			}
			filtered := pod.DeepCopy()
			filtered.Spec.Containers[0].Env = slices.DeleteFunc(filtered.Spec.Containers[0].Env, func(e corev1.EnvVar) bool { return e.Name == protocol.StartDeadlineEnv })
			if !apiequality.Semantic.DeepEqual(filtered, plain) {
				t.Fatal("the start deadline changed more of the pod than its one env var")
			}
		})
	}
}

// A rescue pod runs no agent, so it gets no deadline, and a bad value never
// keeps a volume from its rescue.
func TestStartDeadlineStaysOutOfTheHoldPod(t *testing.T) {
	tmpl := exampleTemplates(t)
	plain, err := buildHoldPod(taskSession(), tmpl)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	for _, deadline := range []string{"2026-11-04T10:05:00Z", "tomorrow"} {
		pod, err := buildHoldPod(scheduledSession(taskSession(), deadline), tmpl)
		if err != nil {
			t.Fatalf("%q: %v", deadline, err)
		}
		got, err := json.Marshal(pod)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%q: the hold pod changed", deadline)
		}
	}
}

// A bad value refuses the pod, as an invalid task budget binding does: the
// session stays Pending (PodSpecInvalid) and never boots without its deadline.
// A Codex remote session records no first launch (its agent is the phone
// daemon), so agentd could not tell a late first boot from a later one.
func TestAnInvalidStartDeadlineRefusesThePod(t *testing.T) {
	tmpl := exampleTemplates(t)
	for _, deadline := range []string{"", "tomorrow", "2026-11-04T10:05:00", "2026-11-04T11:05:00+01:00", "2026-11-04T10:05:00+00:00", " 2026-11-04T10:05:00Z"} {
		pod, err := buildPod(scheduledSession(taskSession(), deadline), tmpl, "https://api")
		if err == nil || pod != nil || !strings.Contains(err.Error(), "start deadline") || errors.As(err, new(sessionInvalidError)) {
			t.Errorf("%q: built %v, %v", deadline, pod != nil, err)
		}
	}
	codex := remoteSession()
	codex.Spec.Agent, codex.Spec.Model = v1alpha1.AgentCodex, "gpt-6-astra"
	if pod, err := buildPod(scheduledSession(codex, "2026-11-04T10:05:00Z"), tmpl, "https://api"); err == nil || pod != nil {
		t.Errorf("a Codex remote session got a start deadline: %v", err)
	}
}

// Only the controller sets the deadline: template and profile env cannot name
// it, so no template change puts a deadline on every session.
func TestTheStartDeadlineIsReserved(t *testing.T) {
	if !slices.Contains(templates.ReservedEnv, protocol.StartDeadlineEnv) {
		t.Fatalf("%s is not reserved", protocol.StartDeadlineEnv)
	}
}
