package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func convTokens(n int64) protocol.TokenObservation {
	return protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
}

func convFigure(at time.Time) *protocol.ConversationUsage {
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	return &protocol.ConversationUsage{Version: protocol.ConversationUsageVersion, Provider: protocol.AgentClaude, ObservedAt: at,
		Coverage: protocol.ConversationCoverage, CounterSemantics: protocol.ConversationCumulative, ResultState: protocol.ConversationFinal, Turns: 3,
		Models: []protocol.ConversationModelUsage{{Model: "claude-opus-5-5", InputTokens: convTokens(300), OutputTokens: convTokens(0), CacheReadInputTokens: convTokens(50), CacheCreationInputTokens: unknown}}}
}

// interactive makes the fixture's session a remote Claude one, which the real
// create route would have done at creation.
func interactive(f *fixture, name string, mode v1alpha1.SessionMode) {
	f.t.Helper()
	s := f.session(name)
	s.Spec.Mode, s.Spec.Prompt = mode, ""
	if err := f.c.Update(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

func TestHeartbeatWritesConversationUsageForTheReportingInteractiveSession(t *testing.T) {
	f := newFixture(t)
	const name = "conv-remote"
	token := f.sessionPod(name, "full", 0)
	interactive(f, name, v1alpha1.ModeRemote)
	st := status(name)
	st.ConversationUsage = convFigure(f.now.Add(-time.Minute))
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code, w.Body.String())
	}
	got := f.session(name).Status.ConversationUsage
	if got == nil || got.ResultState != "Final" || got.Turns != 3 || got.Provider != "claude" || got.Coverage != "RegisteredConversation" || got.CounterSemantics != "ConversationCumulative" ||
		len(got.Models) != 1 || got.Models[0].InputTokens.Amount == nil || *got.Models[0].InputTokens.Amount != 300 ||
		got.Models[0].OutputTokens.State != "Reported" || *got.Models[0].OutputTokens.Amount != 0 ||
		got.Models[0].CacheCreationInputTokens.State != "Unknown" || got.Models[0].CacheCreationInputTokens.Amount != nil {
		t.Fatalf("%+v", got)
	}
	// The headless task record is separate and untouched.
	if f.session(name).Status.UsageObservation != nil {
		t.Fatal("conversation usage created a task usage observation")
	}
	// An old agentd's heartbeat (the field absent) leaves the stored figure as it is.
	before := f.session(name).Status.ConversationUsage.DeepCopy()
	f.now = f.now.Add(time.Minute)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, status(name)); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if !apiequality.Semantic.DeepEqual(before, f.session(name).Status.ConversationUsage) {
		t.Fatal("a heartbeat without the field changed the stored figure")
	}
	// A newer figure replaces it; an older one does not.
	newer := convFigure(f.now)
	newer.Turns, newer.ResultState, newer.ReasonCode, newer.Skipped = 9, protocol.ConversationPartial, protocol.ReasonSkippedRecords, 2
	st = status(name)
	st.ConversationUsage = newer
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if got := f.session(name).Status.ConversationUsage; got.Turns != 9 || got.ResultState != "Partial" || got.ReasonCode != "skipped_records" || got.Skipped != 2 {
		t.Fatalf("%+v", got)
	}
	stale := convFigure(f.now.Add(-time.Hour))
	stale.Turns = 1
	st.ConversationUsage = stale
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if got := f.session(name).Status.ConversationUsage; got.Turns != 9 {
		t.Fatalf("an older observation replaced a newer one: %+v", got)
	}
	// Back to Final: the reason is dropped with the Partial state.
	final := convFigure(f.now.Add(time.Minute))
	final.Turns = 10
	st.ConversationUsage = final
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	if got := f.session(name).Status.ConversationUsage; got.ResultState != "Final" || got.ReasonCode != "" || got.Turns != 10 {
		t.Fatalf("%+v", got)
	}
}

func TestHeartbeatIgnoresConversationUsageItWillNotKeepWithoutRefusingTheHeartbeat(t *testing.T) {
	valid := func(f *fixture) *protocol.ConversationUsage { return convFigure(f.now) }
	for name, tc := range map[string]struct {
		mode  v1alpha1.SessionMode
		agent string
		edit  func(*protocol.ConversationUsage)
	}{
		"a task session":   {v1alpha1.ModeTask, "claude", func(*protocol.ConversationUsage) {}},
		"another provider": {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.Provider = protocol.AgentCodex }},
		"unknown": {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) {
			*u = protocol.UnknownConversationUsage(protocol.AgentClaude, protocol.ReasonUnbound, u.ObservedAt)
		}},
		"initial coverage":    {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.Coverage = protocol.UsageInitial }},
		"task semantics":      {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.CounterSemantics = protocol.UsageFinalTotal }},
		"final with a reason": {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.ReasonCode = protocol.ReasonSkippedRecords }},
		"bad model id":        {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.Models[0].Model = "<script>" }},
		"over-limit tokens": {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) {
			over := protocol.MaxUsageTokens + 1
			u.Models[0].InputTokens = protocol.TokenObservation{State: protocol.UsageReported, Amount: &over}
		}},
		"no models": {v1alpha1.ModeRemote, "claude", func(u *protocol.ConversationUsage) { u.Models = nil }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			const session = "conv-ignored"
			token := f.sessionPod(session, "full", 0)
			if tc.mode != v1alpha1.ModeTask {
				interactive(f, session, tc.mode)
			}
			st := status(session)
			st.ConversationUsage = valid(f)
			tc.edit(st.ConversationUsage)
			if w := f.do(http.MethodPost, protocol.HeartbeatPath(session), token, st); w.Code != http.StatusNoContent {
				t.Fatalf("the heartbeat was refused over its usage field: %d", w.Code)
			}
			got := f.session(session)
			if got.Status.ConversationUsage != nil || got.Status.Agent == nil || got.Status.Agent.Status != protocol.AgentExited {
				t.Fatalf("status %+v", got.Status)
			}
		})
	}
}

// A type error inside the field decodes to a value that is not Valid. It must
// not cost the heartbeat its liveness, and a later good figure still lands.
func TestHeartbeatMalformedConversationUsageKeepsLivenessAndTheStoredFigure(t *testing.T) {
	f := newFixture(t)
	const name = "conv-malformed"
	token := f.sessionPod(name, "full", 0)
	interactive(f, name, v1alpha1.ModeLocal)
	st := status(name)
	st.ConversationUsage = convFigure(f.now)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	stored := f.session(name).Status.ConversationUsage.DeepCopy()
	for _, malformed := range []string{`"text"`, `{"turns":"many"}`, `{"version":1,"models":"x"}`, `[]`, `{"resultState":{"a":1}}`, `{"version":1,"provider":"claude","turns":-1}`} {
		f.now = f.now.Add(time.Minute)
		body := `{"session":"` + name + `","agent":{"state":"busy"},"conversationUsage":` + malformed + `}`
		w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, body)
		got := f.session(name)
		if w.Code != http.StatusNoContent || got.Status.Agent.Status != protocol.AgentBusy || !apiequality.Semantic.DeepEqual(stored, got.Status.ConversationUsage) {
			t.Fatalf("%s: %d %+v", malformed, w.Code, got.Status.ConversationUsage)
		}
	}
}

// Both fields live side by side and are never combined: a task session keeps
// its initial-invocation observation, and nothing writes the conversation figure
// into it.
func TestHeartbeatNeverOverwritesTheInitialUsageObservation(t *testing.T) {
	f := newFixture(t)
	const name = "conv-both"
	token := f.sessionPod(name, "full", 0)
	u := fixtureUsage(f, name, token)
	st := status(name)
	st.UsageObservation = &u
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	original := f.session(name).Status.UsageObservation.DeepCopy()
	if original == nil {
		t.Fatal("no usage observation recorded")
	}
	// A task session offering a conversation figure keeps only its own observation.
	st = status(name)
	st.ConversationUsage = convFigure(f.now)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	got := f.session(name).Status
	if got.ConversationUsage != nil || !apiequality.Semantic.DeepEqual(original, got.UsageObservation) {
		t.Fatalf("%+v", got)
	}
	// An interactive session's figure leaves any observation exactly as it is.
	interactive(f, name, v1alpha1.ModeRemote)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st); w.Code != http.StatusNoContent {
		t.Fatal(w.Code)
	}
	got = f.session(name).Status
	if got.ConversationUsage == nil || !apiequality.Semantic.DeepEqual(original, got.UsageObservation) || got.ConversationUsage.Coverage == original.Coverage {
		t.Fatalf("%+v", got)
	}
}

// Only the session's own pod posts its heartbeat, so only it can write its figure.
func TestConversationUsageCannotBeWrittenForAnotherSession(t *testing.T) {
	f := newFixture(t)
	mine := f.sessionPod("conv-mine", "full", 0)
	f.sessionPod("conv-other", "full", 0)
	interactive(f, "conv-other", v1alpha1.ModeRemote)
	st := status("conv-other")
	st.ConversationUsage = convFigure(f.now)
	if w := f.do(http.MethodPost, protocol.HeartbeatPath("conv-other"), mine, st); w.Code == http.StatusNoContent {
		t.Fatal("a session wrote another session's heartbeat")
	}
	if f.session("conv-other").Status.ConversationUsage != nil {
		t.Fatal("the other session gained a figure")
	}
}

// Wire compatibility of the optional field, in both directions.
func TestConversationUsageHeartbeatFieldIsOptionalOnTheWire(t *testing.T) {
	// A new agentd's status carries the field only when it has one.
	raw, err := json.Marshal(status("wire"))
	if err != nil || strings.Contains(string(raw), "conversationUsage") {
		t.Fatalf("an unset field is serialized: %s %v", raw, err)
	}
	st := status("wire")
	st.ConversationUsage = convFigure(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC))
	raw, _ = json.Marshal(st)
	// An old operator or agentd decodes the new status without error: the body
	// decode is not strict, and a status type that lacks the field ignores it.
	var old struct {
		Session string `json:"session"`
		Agent   protocol.AgentState
	}
	if json.Unmarshal(raw, &old) != nil || old.Session != "wire" {
		t.Fatal("a status type that lacks the field cannot read the new status")
	}
	// A new operator decodes an old heartbeat, which has no field, to nil.
	var back protocol.Status
	oldRaw, _ := json.Marshal(status("wire"))
	if json.Unmarshal(oldRaw, &back) != nil || back.ConversationUsage != nil {
		t.Fatal("an old heartbeat did not decode to an absent field")
	}
	// And the new status round-trips.
	if json.Unmarshal(raw, &back) != nil || back.ConversationUsage == nil || !back.ConversationUsage.Valid() || back.ConversationUsage.Turns != 3 {
		t.Fatalf("round trip %+v", back.ConversationUsage)
	}
}

// The heartbeat body is not decoded strictly, so a newer agentd's extra fields
// never fail an older operator; the operator ignores what it does not know.
func TestHeartbeatBodyAcceptsUnknownFieldsFromANewerAgent(t *testing.T) {
	f := newFixture(t)
	const name = "conv-future"
	token := f.sessionPod(name, "full", 0)
	body := `{"session":"` + name + `","agent":{"state":"busy"},"conversationUsageV2":{"a":1},"futureThing":[1,2]}`
	if w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, body); w.Code != http.StatusNoContent {
		t.Fatalf("an unknown field refused the heartbeat: %d %s", w.Code, w.Body.String())
	}
}
