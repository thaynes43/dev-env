package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// DESIGN-054 S3c: the scheduled-work start deadline in the pod. The pods of
// sessions without it are pinned by legacy_pods_golden_test.go.

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

// A bad value refuses the pod as a session agentd would refuse: the spec and
// its annotation are fixed, so the session could never start, and it fails
// (SessionInvalid) instead of staying Pending, which would never end its
// occurrence. A Codex remote session records no first launch (its agent is the
// phone daemon), so agentd could not tell a late first boot from a later one.
// The API's precheck refuses both before a create.
func TestAnInvalidStartDeadlineRefusesThePod(t *testing.T) {
	tmpl := exampleTemplates(t)
	codex := remoteSession()
	codex.Spec.Agent, codex.Spec.Model = v1alpha1.AgentCodex, "gpt-6-astra"
	cases := map[string]struct {
		session *v1alpha1.AgentSession
		want    string
	}{
		"codex remote": {scheduledSession(codex, "2026-11-04T10:05:00Z"), "a scheduled-work start deadline needs a first launch record, which a Codex remote session does not keep"},
	}
	for _, deadline := range []string{"", "tomorrow", "2026-11-04T10:05:00", "2026-11-04T11:05:00+01:00", "2026-11-04T10:05:00+00:00", " 2026-11-04T10:05:00Z"} {
		cases["value "+deadline] = struct {
			session *v1alpha1.AgentSession
			want    string
		}{scheduledSession(taskSession(), deadline), "invalid scheduled-work start deadline: start deadline is not an RFC 3339 UTC time"}
	}
	for name, c := range cases {
		pod, err := buildPod(c.session, tmpl, "https://api")
		if err == nil || pod != nil || err.Error() != c.want || !errors.As(err, new(sessionInvalidError)) {
			t.Errorf("%s: built %v, %v; want the session refused as invalid with %q", name, pod != nil, err, c.want)
		}
		if err := CheckAgentdSession(c.session); err == nil || err.Error() != c.want || !errors.As(err, new(sessionInvalidError)) {
			t.Errorf("%s: the API precheck: %v; want %q", name, err, c.want)
		}
	}
	if err := CheckAgentdSession(scheduledSession(taskSession(), "2026-11-04T10:05:00Z")); err != nil {
		t.Errorf("the API precheck refused a valid start deadline: %v", err)
	}
}

// The reconciler fails such a session at once, with no pod and no volume.
func TestASessionWithAnInvalidStartDeadlineFails(t *testing.T) {
	startOperator(t)
	s := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Annotations = map[string]string{v1alpha1.AnnotationScheduledWorkStartDeadline: "tomorrow"}
	})
	st := waitStatus(t, s.Name, "Failed", phaseIs(v1alpha1.PhaseFailed)).Status
	if c := condition(&st, ConditionPodReady); c == nil || c.Reason != "SessionInvalid" || !strings.Contains(c.Message, "start deadline") {
		t.Errorf("PodReady %+v", c)
	}
	if _, err := get(t, sessionNS, s.Name, &corev1.Pod{}); !apierrors.IsNotFound(err) {
		t.Errorf("an invalid session got a pod: %v", err)
	}
	if _, err := get(t, sessionNS, HomeClaimName(s.Name), &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Errorf("an invalid session got a volume: %v", err)
	}
}

// Only the controller sets the deadline: template and profile env cannot name
// it, so no template change puts a deadline on every session.
func TestTheStartDeadlineIsReserved(t *testing.T) {
	if !slices.Contains(templates.ReservedEnv, protocol.StartDeadlineEnv) {
		t.Fatalf("%s is not reserved", protocol.StartDeadlineEnv)
	}
}

// A pilot or instruction image may predate S3c and so lack agentd's deadline
// check. A scheduled session runs on the templates image, which the rollout
// rule keeps at S3c or later; an override that would select another image for
// it refuses the session as invalid instead.
func TestAScheduledSessionKeepsTheTemplatesImage(t *testing.T) {
	tmpl := exampleTemplates(t)
	const deadline = "2026-11-04T10:05:00Z"
	instructionImage := "ghcr.io/thaynes43/dev-env:2.86.0@sha256:" + strings.Repeat("a", 64)
	for name, c := range map[string]struct {
		r       *Reconciler
		session func() *v1alpha1.AgentSession
	}{
		"pilot":      {&Reconciler{APIURL: "https://api", PilotAgent: pilotTestAgent()}, pilotTestSession},
		"task pilot": {&Reconciler{APIURL: "https://api", ManagedCodexTasks: true, TaskPilotAgent: pilotTestAgent()}, func() *v1alpha1.AgentSession { return taskPilotSession(t) }},
	} {
		t.Run(name, func(t *testing.T) {
			if pod, err := c.r.sessionPod(c.session(), tmpl); err != nil || pod.Spec.Containers[0].Image == tmpl.Image {
				t.Fatalf("the fixture does not select an override: %v", err)
			}
			// Every build, the first and any rebuild after a launch, succeeds
			// on the templates image: a rebuild never fails the session.
			for build := 0; build < 2; build++ {
				pod, err := c.r.sessionPod(scheduledSession(c.session(), deadline), tmpl)
				if err != nil || pod == nil || pod.Spec.Containers[0].Image != tmpl.Image || len(startDeadlineValues(pod)) != 1 {
					t.Fatalf("build %d of a scheduled session: pod %v, %v", build, pod != nil, err)
				}
			}
		})
	}
	for _, publisher := range []bool{false, true} {
		s := scheduledSession(ordinaryOptInSession(t), deadline)
		s.Spec.CoordinatorInstructions, s.Spec.CoordinatorInstructionPublisher = !publisher, publisher
		if err := CheckAgentdSession(s); err == nil || !strings.Contains(err.Error(), "no coordinator instructions") {
			t.Fatalf("the API precheck took a scheduled session with coordinator instructions (publisher %v): %v", publisher, err)
		}
		// A creator that skips the API precheck is refused by the reconciler,
		// before any launch, as invalid.
		r := &Reconciler{APIURL: "https://api", CoordinatorInstructionAgentImage: instructionImage}
		if pod, err := r.sessionPod(s, tmpl); err == nil || pod != nil || !errors.As(err, new(sessionInvalidError)) {
			t.Fatalf("the reconciler built a scheduled session with coordinator instructions (publisher %v): pod %v, %v", publisher, pod != nil, err)
		}
	}
	// Not selected: the scheduled session builds on the templates image.
	other := scheduledSession(pilotTestSession(), deadline)
	other.Labels[v1alpha1.LabelIdempotencyKey] = "another-key"
	pod, err := (&Reconciler{APIURL: "https://api", PilotAgent: pilotTestAgent()}).sessionPod(other, tmpl)
	if err != nil || pod.Spec.Containers[0].Image != tmpl.Image || len(startDeadlineValues(pod)) != 1 {
		t.Fatalf("an unselected scheduled session: %v", err)
	}
}
