package apiserver

// Story 791 (DESIGN-064 6.9): the start record of a summoned task's model
// pre-flight reaches status.agent.start through the heartbeat. The record
// comes from the session's own pod, so the API copies only the fixed set,
// for a summoned task only, and once stored it never changes.

import (
	"context"
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const startFallback = "claude-opus-5"

// summonedTaskSession makes the fixture's session a summoned task.
func summonedTaskSession(s *v1alpha1.AgentSession) {
	s.Spec.Caller, s.Spec.Lane = "alert-responder", v1alpha1.LaneRemediation
	s.Spec.Summon = &v1alpha1.SessionSummon{DispatchID: "d-1", Priority: v1alpha1.SummonPriorityUrgent, FallbackModel: startFallback, OnUnreported: v1alpha1.SummonOnUnreportedEscalated}
}

// agentStartStatus copies a summoned task's record only when it is one of
// the fixed shapes; anything else is dropped, and a stored record is kept.
func TestAgentStartStatusCopiesOnlyTheFixedSet(t *testing.T) {
	task := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Agent: "claude", Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5"}}
	summonedTaskSession(task)
	remote := task.DeepCopy()
	remote.Spec.Mode = v1alpha1.ModeRemote
	ordinary := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Agent: "claude", Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5"}}
	for _, tc := range []struct {
		name string
		sess *v1alpha1.AgentSession
		rec  *protocol.StartRecord
		want *v1alpha1.AgentStartStatus
	}{
		{"spec.model", task, &protocol.StartRecord{Model: "claude-opus-5-5"}, &v1alpha1.AgentStartStatus{Model: "claude-opus-5-5"}},
		{"the fallback beside a quota refusal", task, &protocol.StartRecord{Model: startFallback, FallbackCause: protocol.StartFailureQuotaRefused}, &v1alpha1.AgentStartStatus{Model: startFallback, FallbackCause: protocol.StartFailureQuotaRefused}},
		{"the fallback beside no answer", task, &protocol.StartRecord{Model: startFallback, FallbackCause: protocol.StartFailureNoAnswer}, &v1alpha1.AgentStartStatus{Model: startFallback, FallbackCause: protocol.StartFailureNoAnswer}},
		{"each failure: a refused model", task, &protocol.StartRecord{Failure: protocol.StartFailureModelRefused}, &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureModelRefused}},
		{"each failure: a quota refusal", task, &protocol.StartRecord{Failure: protocol.StartFailureQuotaRefused}, &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureQuotaRefused}},
		{"each failure: a rejected login", task, &protocol.StartRecord{Failure: protocol.StartFailureLoginRejected}, &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureLoginRejected}},
		{"each failure: no answer", task, &protocol.StartRecord{Failure: protocol.StartFailureNoAnswer}, &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureNoAnswer}},
		{"no record", task, nil, nil},
		{"a model that is neither of the session's", task, &protocol.StartRecord{Model: "claude-fable-5-2"}, nil},
		{"the fallback without its cause", task, &protocol.StartRecord{Model: startFallback}, nil},
		{"spec.model beside a cause", task, &protocol.StartRecord{Model: "claude-opus-5-5", FallbackCause: protocol.StartFailureNoAnswer}, nil},
		{"the fallback beside a rejected login", task, &protocol.StartRecord{Model: startFallback, FallbackCause: protocol.StartFailureLoginRejected}, nil},
		{"the fallback beside an unknown cause", task, &protocol.StartRecord{Model: startFallback, FallbackCause: "Exploded"}, nil},
		{"a failure outside the set", task, &protocol.StartRecord{Failure: "Exploded"}, nil},
		{"a model and a failure", task, &protocol.StartRecord{Model: "claude-opus-5-5", Failure: protocol.StartFailureNoAnswer}, nil},
		{"a failure beside a cause", task, &protocol.StartRecord{Failure: protocol.StartFailureNoAnswer, FallbackCause: protocol.StartFailureNoAnswer}, nil},
		{"an empty record", task, &protocol.StartRecord{}, nil},
		{"an ordinary task", ordinary, &protocol.StartRecord{Model: "claude-opus-5-5"}, nil},
		{"a summoned remote session", remote, &protocol.StartRecord{Model: "claude-opus-5-5"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := agentStartStatus(tc.sess, nil, tc.rec)
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	stored := &v1alpha1.AgentStatus{Start: &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureNoAnswer}}
	for _, rec := range []*protocol.StartRecord{nil, {Model: "claude-opus-5-5"}, {Failure: protocol.StartFailureLoginRejected}} {
		if got := agentStartStatus(task, stored, rec); got == nil || *got != *stored.Start {
			t.Fatalf("a stored record changed to %+v on %+v", got, rec)
		}
	}
}

// End to end: the first heartbeat that carries a valid record stores it;
// later heartbeats that carry another record, or none, leave it as it is;
// an invalid record is dropped and the heartbeat still answers. (200 here:
// since dev-env PR 1127 the test fixture lets every session take a title, so
// the route answers with the title body, as main's own heartbeat tests expect.)
func TestHeartbeatStoresTheStartRecordOnce(t *testing.T) {
	f := newFixture(t)
	const name = "rem-responder-1113-030000"
	tok := f.sessionPod(name, "ops", 0)
	s := f.session(name)
	summonedTaskSession(s)
	if err := f.c.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	path := protocol.HeartbeatPath(name)
	beat := func(rec *protocol.StartRecord) *v1alpha1.AgentStartStatus {
		t.Helper()
		st := status(name)
		st.Agent.State, st.Agent.Task, st.Usage = protocol.AgentBusy, nil, nil
		st.Start = rec
		if w := f.do(http.MethodPost, path, tok, st); w.Code != http.StatusOK {
			t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
		}
		return f.session(name).Status.Agent.Start
	}
	if got := beat(&protocol.StartRecord{Model: "claude-fable-5-2"}); got != nil {
		t.Fatalf("a model that is neither of the session's was stored: %+v", got)
	}
	if got := beat(&protocol.StartRecord{Failure: "Exploded"}); got != nil {
		t.Fatalf("a failure outside the set was stored: %+v", got)
	}
	if got := beat(nil); got != nil {
		t.Fatalf("no record stored %+v", got)
	}
	want := v1alpha1.AgentStartStatus{Model: startFallback, FallbackCause: protocol.StartFailureModelRefused}
	if got := beat(&protocol.StartRecord{Model: startFallback, FallbackCause: protocol.StartFailureModelRefused}); got == nil || *got != want {
		t.Fatalf("stored %+v, want %+v", got, want)
	}
	for _, rec := range []*protocol.StartRecord{nil, {Model: "claude-opus-5-5"}, {Failure: protocol.StartFailureNoAnswer}} {
		if got := beat(rec); got == nil || *got != want {
			t.Fatalf("a later heartbeat with %+v changed the record to %+v", rec, got)
		}
	}
}
