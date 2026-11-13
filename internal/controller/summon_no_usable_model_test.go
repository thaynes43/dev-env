package controller

// Story 791 (DESIGN-064 6.9 and 6.6): a summoned task whose model pre-flight
// found no usable model closes once, by its onUnreported, with reason
// no-usable-model, read from the typed status.agent.start record and never
// from message text. The session document carries the fallback model only
// for a summoned session.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestSummonNoUsableModelClosesFromTheTypedRecordOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		agent  v1alpha1.AgentStatus
		reason string // "" is no close
	}{
		{"a refused model", v1alpha1.AgentStatus{Status: "failed", Start: &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureModelRefused}}, "no-usable-model"},
		{"a quota refusal", v1alpha1.AgentStatus{Status: "failed", Start: &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureQuotaRefused}}, "no-usable-model"},
		{"a rejected login", v1alpha1.AgentStatus{Status: "failed", Start: &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureLoginRejected}}, "no-usable-model"},
		{"no answer", v1alpha1.AgentStatus{Status: "failed", Start: &v1alpha1.AgentStartStatus{Failure: protocol.StartFailureNoAnswer}}, "no-usable-model"},
		{"a message that says so, failed", v1alpha1.AgentStatus{Status: "failed", Message: "no usable model: the model pre-flight ended NoAnswer; no-usable-model"}, "agent-failed"},
		{"a message that says so, busy", v1alpha1.AgentStatus{Status: "busy", Message: "no usable model; no-usable-model"}, ""},
		{"the start fence's refusal", v1alpha1.AgentStatus{Status: "failed", Message: "run-agent refused to start the provider: start deadline passed"}, "agent-failed"},
		{"started on the fallback", v1alpha1.AgentStatus{Status: "busy", Start: &v1alpha1.AgentStartStatus{Model: "claude-opus-5", FallbackCause: protocol.StartFailureQuotaRefused}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, r, s, _ := summonOutcomeFixture(t)
			agent := tc.agent
			s.Status.Agent = &agent
			if err := c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			key := client.ObjectKeyFromObject(s)
			run := func() {
				t.Helper()
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatal(err)
				}
				if err := c.Get(context.Background(), key, s); err != nil {
					t.Fatal(err)
				}
			}
			run()
			if tc.reason == "" {
				if s.Status.Outcome == nil || s.Status.Outcome.State != v1alpha1.OutcomeRunning {
					t.Fatalf("outcome %+v, want running and no close", s.Status.Outcome)
				}
				return
			}
			want := "unreported: " + tc.reason
			if s.Status.Outcome == nil || s.Status.Outcome.Note != want || s.Status.Outcome.State != v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported) {
				t.Fatalf("outcome %+v, want %s by onUnreported %s", s.Status.Outcome, want, s.Spec.Summon.OnUnreported)
			}
			if s.Status.Summon == nil || s.Status.Summon.Closed == nil || s.Status.Summon.Closed.Reason != tc.reason {
				t.Fatalf("closed record %+v", s.Status.Summon)
			}
			first := *s.Status.Outcome.DeepCopy()
			closed := *s.Status.Summon.Closed.DeepCopy()
			run()
			if s.Status.Outcome.Note != first.Note || !s.Status.Outcome.At.Equal(first.At) || s.Status.Summon.Closed.Reason != closed.Reason || !s.Status.Summon.Closed.At.Equal(&closed.At) {
				t.Fatalf("the second pass changed the close: %+v %+v", s.Status.Outcome, s.Status.Summon.Closed)
			}
		})
	}
}

// The fallback model reaches agentd only in a summoned session's document;
// an ordinary session's document has no such key.
func TestSessionDocumentCarriesTheFallbackOnlyForASummonedSession(t *testing.T) {
	read := func(s *v1alpha1.AgentSession) map[string]any {
		t.Helper()
		doc, err := sessionDocument(s, false)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	ordinary := taskSession()
	if m := read(ordinary); m["fallbackModel"] != nil {
		t.Fatalf("an ordinary session's document carries a fallback: %v", m)
	}
	summoned := taskSession()
	summoned.Spec.Caller, summoned.Spec.Lane, summoned.Spec.Summon = "upgrade-agent.alert-responder", v1alpha1.LaneRemediation, remediationSummon()
	if m := read(summoned); m["fallbackModel"] != "claude-opus-5" || m["caller"] != summoned.Spec.Caller {
		t.Fatalf("a summoned task's document: %v", m)
	}
	doc, _ := sessionDocument(ordinary, false)
	if strings.Contains(doc, "fallback") {
		t.Fatalf("ordinary document %s", doc)
	}
}
