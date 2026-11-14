package v1alpha1_test

// Story 791 (DESIGN-064 6.9): status.agent.start, a summoned task's start
// after its model pre-flight. Each rule accepts and refuses.

import (
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// withStart creates s and returns it with status.agent.start set to start.
func withStart(t *testing.T, s *v1alpha1.AgentSession, start *v1alpha1.AgentStartStatus) *v1alpha1.AgentSession {
	t.Helper()
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	got := get(t, s)
	got.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", Start: start}
	return got
}

func TestAgentStartRecordShape(t *testing.T) {
	fallback := remediationSummon().FallbackModel
	for _, tc := range []struct {
		name  string
		start v1alpha1.AgentStartStatus
		want  string // "" accepts
	}{
		{"spec.model", v1alpha1.AgentStartStatus{Model: "claude-opus-5-5"}, ""},
		{"the fallback beside its cause", v1alpha1.AgentStartStatus{Model: fallback, FallbackCause: "QuotaRefused"}, ""},
		{"a failure: a refused model", v1alpha1.AgentStartStatus{Failure: "ModelRefused"}, ""},
		{"a failure: a quota refusal", v1alpha1.AgentStartStatus{Failure: "QuotaRefused"}, ""},
		{"a failure: a rejected login", v1alpha1.AgentStartStatus{Failure: "LoginRejected"}, ""},
		{"a failure: no answer", v1alpha1.AgentStartStatus{Failure: "NoAnswer"}, ""},
		{"a model and a failure", v1alpha1.AgentStartStatus{Model: "claude-opus-5-5", Failure: "NoAnswer"}, "either the model the task started on or why"},
		{"neither", v1alpha1.AgentStartStatus{}, "either the model the task started on or why"},
		{"a cause beside a failure", v1alpha1.AgentStartStatus{Failure: "NoAnswer", FallbackCause: "NoAnswer"}, "a fallback's cause goes only beside the model"},
		{"a failure outside the set", v1alpha1.AgentStartStatus{Failure: "Exploded"}, "status.agent.start.failure"},
		{"a rejected login as a fallback's cause", v1alpha1.AgentStartStatus{Model: fallback, FallbackCause: "LoginRejected"}, "status.agent.start.fallbackCause"},
		{"a model longer than 128", v1alpha1.AgentStartStatus{Model: "claude-" + strings.Repeat("a", 122)}, "status.agent.start.model"},
		{"a model with a space", v1alpha1.AgentStartStatus{Model: "claude opus"}, "status.agent.start.model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := tc.start
			s := withStart(t, summonedTask(), &start)
			err := k8s.Status().Update(ctx(t), s)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got := get(t, s).Status.Agent.Start; got == nil || *got != tc.start {
					t.Fatalf("stored %+v, want %+v", got, tc.start)
				}
				return
			}
			wantInvalid(t, err, tc.want)
		})
	}
}

// Once recorded, the record never changes: the same record is accepted
// again, and a change, a removal of the record or of status.agent is
// refused. Other status.agent fields still change.
func TestAgentStartRecordNeverChanges(t *testing.T) {
	s := withStart(t, summonedTask(), &v1alpha1.AgentStartStatus{Failure: "QuotaRefused"})
	if err := k8s.Status().Update(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	same := get(t, s)
	same.Status.Agent.Status, same.Status.Agent.Message = "failed", "no usable model"
	if err := k8s.Status().Update(ctx(t), same); err != nil {
		t.Fatalf("the same record with another state: %v", err)
	}
	for name, edit := range map[string]func(*v1alpha1.AgentSession){
		"another failure": func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Start = &v1alpha1.AgentStartStatus{Failure: "NoAnswer"}
		},
		"a model in its place": func(s *v1alpha1.AgentSession) {
			s.Status.Agent.Start = &v1alpha1.AgentStartStatus{Model: "claude-opus-5-5"}
		},
		"the record removed":   func(s *v1alpha1.AgentSession) { s.Status.Agent.Start = nil },
		"status.agent removed": func(s *v1alpha1.AgentSession) { s.Status.Agent = nil },
	} {
		t.Run(name, func(t *testing.T) {
			bad := get(t, s)
			edit(bad)
			wantInvalid(t, k8s.Status().Update(ctx(t), bad), "never changes once recorded")
		})
	}
	// A session without a record may gain one.
	fresh := withStart(t, summonedTask(), nil)
	if err := k8s.Status().Update(ctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	fresh = get(t, fresh)
	fresh.Status.Agent.Start = &v1alpha1.AgentStartStatus{Model: "claude-opus-5-5"}
	if err := k8s.Status().Update(ctx(t), fresh); err != nil {
		t.Fatalf("a first record: %v", err)
	}
}
