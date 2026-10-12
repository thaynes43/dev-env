package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestUsageClaudePresenceAndReplay(t *testing.T) {
	for _, tc := range []struct{ name, cost, want string }{
		{"missing", "", "Unknown"}, {"null", `,"total_cost_usd":null`, "Unknown"},
		{"invalid", `,"total_cost_usd":"bad"`, "Unknown"}, {"negative", `,"total_cost_usd":-1`, "Unknown"},
		{"overflow", `,"total_cost_usd":1e999`, "Unknown"}, {"zero", `,"total_cost_usd":0`, "0"},
		{"nonzero", `,"total_cost_usd":1.25`, "1.25"}, {"small", `,"total_cost_usd":1e-7`, "0.0000001"},
		{"underflow", `,"total_cost_usd":1e-400`, "Unknown"}, {"precision", `,"total_cost_usd":0.123456789123456789`, "0.123456789123456789"},
		{"negative-zero", `,"total_cost_usd":-0.0`, "0"}, {"expanded", `,"total_cost_usd":120e-1`, "12"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var result streamResult
			var out bytes.Buffer
			line := []byte(`{"type":"result","subtype":"success","num_turns":1,"usage":{"input_tokens":0,"cache_read_input_tokens":5,"output_tokens":null,"cache_creation_input_tokens":"bad"}` + tc.cost + `}`)
			renderStreamLine(line, &out, &result)
			u := result.Observation
			if !result.Seen || u.InputTokens.State != protocol.UsageReported || u.InputTokens.Amount == nil || *u.InputTokens.Amount != 0 || u.OutputTokens.State != protocol.UsageUnknown || u.CacheReadInputTokens.Amount == nil || *u.CacheReadInputTokens.Amount != 5 || u.CacheCreationInputTokens.Amount != nil {
				t.Fatal("independent presence lost", u)
			}
			if tc.want == "Unknown" {
				if u.Cost.State != protocol.UsageUnknown || u.Cost.Amount != "" || !strings.Contains(out.String(), "cost Unknown") {
					t.Fatal("missing cost became zero", u.Cost, out.String())
				}
			} else if u.Cost.State != protocol.UsageReported || u.Cost.Amount != tc.want || u.Cost.Kind != protocol.UsageCLIEstimate || !strings.Contains(out.String(), tc.want+" USD (CLI equivalent estimate)") {
				t.Fatal("explicit estimate lost", u.Cost, out.String())
			}
			first := result.Observation
			renderStreamLine(line, io.Discard, &result)
			if result.UsageConflict || !reflect.DeepEqual(first, result.Observation) {
				t.Fatal("identical replay changed original")
			}
			renderStreamLine([]byte(`{"type":"result","subtype":"success","total_cost_usd":99}`), io.Discard, &result)
			if !result.UsageConflict || !reflect.DeepEqual(first, result.Observation) {
				t.Fatal("cumulative/conflicting result replaced original")
			}
			if strings.Contains(out.String(), "$0") {
				t.Fatal("unqualified dollars")
			}
		})
	}
}

func TestUsageClaudeCostSurvivesInvalidTokenMembers(t *testing.T) {
	for _, usage := range []string{`"invalid"`, `{"input_tokens":-1,"output_tokens":9223372036854775808,"cache_read_input_tokens":null,"cache_creation_input_tokens":"5"}`} {
		var result streamResult
		renderStreamLine([]byte(`{"type":"result","subtype":"error_during_execution","is_error":true,"total_cost_usd":0,"usage":`+usage+`}`), io.Discard, &result)
		u := result.Observation
		if !result.Seen || u.ResultState != "Partial" || u.Cost.State != protocol.UsageReported || u.Cost.Amount != "0" || u.InputTokens.Amount != nil || u.OutputTokens.Amount != nil || u.CacheReadInputTokens.Amount != nil || u.CacheCreationInputTokens.Amount != nil {
			t.Fatal("invalid tokens erased explicit partial cost or fabricated counters", u)
		}
	}
}

func TestUsageCodexTokenOnlyAndPartial(t *testing.T) {
	for _, terminal := range []string{"turn.completed", "turn.failed"} {
		t.Run(terminal, func(t *testing.T) {
			s, l := captureLaunch(t)
			l.CaptureOutput = false
			l.Provider = protocol.AgentCodex
			l.ConversationID = ""
			path := writeLaunch(t, s, l)
			stream := codexStream{}
			var result streamResult
			for _, line := range []string{`{"type":"thread.started","thread_id":"` + captureConversation + `"}`, `{"type":"turn.started"}`, `{"type":"` + terminal + `","usage":{"input_tokens":0,"cached_input_tokens":7}}`} {
				if err := stream.line([]byte(line), path, &l, io.Discard, &result); err != nil {
					t.Fatal(err)
				}
			}
			u := result.Observation
			if u.Cost.State != protocol.UsageUnknown || u.InputTokens.Amount == nil || *u.InputTokens.Amount != 0 || u.OutputTokens.Amount != nil || u.CacheReadInputTokens.Amount == nil || *u.CacheReadInputTokens.Amount != 7 || u.CacheCreationInputTokens.Amount != nil {
				t.Fatal("tokens fabricated or dropped", u)
			}
			before := result.Observation
			if stream.line([]byte(`{"type":"error"}`), path, &l, io.Discard, &result) == nil || !reflect.DeepEqual(before, result.Observation) {
				t.Fatal("second terminal erased original")
			}
			if terminal == "turn.failed" && u.ResultState != "Partial" {
				t.Fatal("error claimed final total")
			}
		})
	}
}

func TestUsageInitialSourceIndependentOfCaptureAndResume(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			s, sess, ws, now := managedCodexFixture(t)
			s.PodUID = "original-pod"
			sess.Agent = provider
			sess.CaptureOutput = false
			if provider == protocol.AgentClaude {
				sess.Model = "claude-opus-5-5"
			}
			first, err := BuildLaunch(s, sess, ws, "original", now)
			if err != nil || first.UsageSource == nil || !first.UsageSource.Valid() || first.UsageSource.RequestedModel != sess.Model || first.CaptureOutput {
				t.Fatal("missing independent source", first.UsageSource, err)
			}
			first.ConversationID, first.NativeThreadConfirmed = captureConversation, true
			resume, err := BuildResume(s, sess, ws, first, "later", now.Add(time.Hour))
			if err != nil || resume.UsageSource != nil || !resume.TUI || !resume.Resume {
				t.Fatal("resume asserted a new initial usage source", err)
			}
		})
	}
}

func TestUsagePrivateOriginalReceiptAndRetainedResume(t *testing.T) {
	s, l := captureLaunch(t)
	l.CaptureOutput = false
	sess := protocol.Session{SessionUID: l.SessionUID, Agent: l.Provider, Mode: protocol.ModeTask, Model: l.CaptureModel}
	if err := bindInitialUsage(&l, sess, l.PodUID); err != nil {
		t.Fatal(err)
	}
	if err := beginInitialUsage(l, s.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := beginInitialUsage(l, s.StateDir); err == nil {
		t.Fatal("original intent allowed replay")
	}
	var result streamResult
	renderStreamLine([]byte(`{"type":"result","subtype":"success","total_cost_usd":0,"usage":{"input_tokens":3}}`), io.Discard, &result)
	writeResult(s.StateDir, l, l.CreatedAt, 0, false, result)
	var tr taskResult
	if err := readJSONFile(s.statePath(resultFile), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.UsageObservation == nil || tr.UsageObservation.Validate() != nil || tr.UsageObservation.Cost.Amount != "0" {
		t.Fatal("explicit zero/source receipt lost", tr)
	}
	if err := writeJSONFile(s.statePath(launchFile), l); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "later", Boot: protocol.BootReady}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(resumeFile), Launch{BootID: "later", TUI: true, Resume: true, ConversationID: l.ConversationID}); err != nil {
		t.Fatal(err)
	}
	st := CollectStatus(context.Background(), &fakeRunner{}, s, protocol.Session{Name: l.Session}, l.CreatedAt.Add(time.Hour))
	if st.UsageObservation == nil || !reflect.DeepEqual(*st.UsageObservation, *tr.UsageObservation) || st.UsageObservation.Coverage != protocol.UsageInitial {
		t.Fatal("resume relabelled or lost initial snapshot")
	}
	before, _ := os.ReadFile(s.statePath(resultFile))
	writeResult(s.StateDir, l, l.CreatedAt.Add(time.Hour), 1, false, streamResult{})
	after, _ := os.ReadFile(s.statePath(resultFile))
	if !bytes.Equal(before, after) {
		t.Fatal("original result overwritten")
	}
	changed := *tr.UsageObservation
	changed.Source.PodUID = "foreign"
	if collectedInitialUsage(l, s.StateDir, &changed) != nil {
		t.Fatal("foreign result accepted")
	}
	intent := filepath.Join(s.StateDir, initialUsageDir, "intent.json")
	if err := os.Chmod(intent, 0o644); err != nil {
		t.Fatal(err)
	}
	if collectedInitialUsage(l, s.StateDir, tr.UsageObservation) != nil {
		t.Fatal("nonprivate receipt accepted")
	}
	var legacy taskResult
	if json.Unmarshal([]byte(`{"usage":{"costUSD":0,"inputTokens":5}}`), &legacy) != nil || legacy.UsageObservation != nil {
		t.Fatal("legacy zero migrated")
	}
}

// This is the Status shape consumed at old source 24960dc. Its default JSON
// decoder ignores the additive usageObservation field, as the old API does.
type legacyUsageHeartbeat struct {
	Session    string                    `json:"session"`
	Agentd     string                    `json:"agentd"`
	BootID     string                    `json:"bootId"`
	BootedAt   time.Time                 `json:"bootedAt"`
	Boot       string                    `json:"boot"`
	Problems   []protocol.Step           `json:"problems,omitempty"`
	Workspace  *protocol.Workspace       `json:"workspace,omitempty"`
	Agent      protocol.AgentState       `json:"agent"`
	Usage      *protocol.Usage           `json:"usage,omitempty"`
	Decision   *protocol.DecisionOutcome `json:"decision,omitempty"`
	ObservedAt time.Time                 `json:"observedAt"`
}

func assertUsageHeartbeatWire(t *testing.T, st protocol.Status) {
	t.Helper()
	var wire []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		wire, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "synthetic body unavailable", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "synthetic-token")
	writeFile(t, token, "SYNTHETIC-ONLY")
	heartbeat := HeartbeatClient{URL: server.URL, Session: st.Session, TokenFile: token, HTTP: server.Client()}
	if err := heartbeat.Send(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	var old legacyUsageHeartbeat
	if err := json.NewDecoder(bytes.NewReader(wire)).Decode(&old); err != nil {
		t.Fatal("old heartbeat decoder rejected ordinary liveness", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["usage"]; present || old.Usage != nil {
		t.Fatal("new heartbeat supplied unqualified raw usage to old operator", string(wire))
	}
	want := legacyUsageHeartbeat{Session: st.Session, Agentd: st.Agentd, BootID: st.BootID, BootedAt: st.BootedAt, Boot: st.Boot, Problems: st.Problems, Workspace: st.Workspace, Agent: st.Agent, Decision: st.Decision, ObservedAt: st.ObservedAt}
	if !reflect.DeepEqual(old, want) {
		t.Fatalf("heartbeat lost liveness or task budget outcome: got=%+v want=%+v", old, want)
	}
	var current protocol.Status
	if err := json.Unmarshal(wire, &current); err != nil || !reflect.DeepEqual(current.UsageObservation, st.UsageObservation) {
		t.Fatal("typed observation changed at the outgoing wire boundary", err, string(wire))
	}
	if st.UsageObservation != nil {
		if _, present := fields["usageObservation"]; !present {
			t.Fatal("typed observation omitted from new heartbeat")
		}
	}
}

func TestUsageOutgoingHeartbeatMixedVersionPreservesPrivateEvidence(t *testing.T) {
	for _, tc := range []struct{ name, provider, cost, proof string }{
		{"codex-token-only", protocol.AgentCodex, "", "valid"},
		{"claude-explicit-zero", protocol.AgentClaude, `,"total_cost_usd":0`, "valid"},
		{"claude-explicit-nonzero", protocol.AgentClaude, `,"total_cost_usd":1.25`, "valid"},
		{"missing-original-proof", protocol.AgentCodex, "", "missing"},
		{"invalid-original-proof", protocol.AgentClaude, `,"total_cost_usd":1.25`, "invalid"},
		{"legacy-private-result", protocol.AgentClaude, `,"total_cost_usd":1.25`, "legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, launch := captureLaunch(t)
			launch.CaptureOutput, launch.Provider = false, tc.provider
			model := "claude-opus-5-5"
			if tc.provider == protocol.AgentCodex {
				model, launch.ConversationID = "gpt-6.1-sol", ""
			}
			if tc.proof != "legacy" {
				session := protocol.Session{SessionUID: launch.SessionUID, Agent: tc.provider, Mode: protocol.ModeTask, Model: model}
				if err := bindInitialUsage(&launch, session, launch.PodUID); err != nil {
					t.Fatal(err)
				}
				if tc.proof != "missing" {
					if err := beginInitialUsage(launch, s.StateDir); err != nil {
						t.Fatal(err)
					}
				}
			}
			path := writeLaunch(t, s, launch)
			var result streamResult
			if tc.provider == protocol.AgentCodex {
				stream := codexStream{}
				for _, line := range []string{`{"type":"thread.started","thread_id":"` + captureConversation + `"}`, `{"type":"turn.started"}`, `{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":7,"cached_input_tokens":3}}`} {
					if err := stream.line([]byte(line), path, &launch, io.Discard, &result); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				renderStreamLine([]byte(`{"type":"result","subtype":"error_max_turns","num_turns":3,"usage":{"input_tokens":5,"output_tokens":7}`+tc.cost+`}`), io.Discard, &result)
			}
			result.NumTurns = 3
			writeResult(s.StateDir, launch, launch.CreatedAt, 124, true, result)
			var private taskResult
			if err := readJSONFile(s.statePath(resultFile), &private); err != nil || private.Usage == nil || private.Usage.InputTokens != 5 {
				t.Fatal("private raw task evidence lost", err, private)
			}
			if tc.proof == "invalid" {
				if err := os.Chmod(filepath.Join(s.StateDir, initialUsageDir, "intent.json"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: launch.BootID, BootedAt: launch.CreatedAt, Boot: protocol.BootReady}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(s.statePath(resultFile))
			if err != nil {
				t.Fatal(err)
			}
			st := CollectStatus(context.Background(), &fakeRunner{}, s, protocol.Session{Name: launch.Session}, private.FinishedAt.Add(time.Minute))
			if st.Usage != nil || st.Agent.State != protocol.AgentExited || st.Agent.Task == nil || st.Agent.Task.ExitCode != 124 || !st.Agent.Task.TimedOut || st.Agent.Task.NumTurns != 3 {
				t.Fatal("raw usage escaped collection or task budget outcome was lost", st)
			}
			if tc.proof == "valid" {
				if st.UsageObservation == nil || st.UsageObservation.Validate() != nil || !reflect.DeepEqual(st.UsageObservation, private.UsageObservation) {
					t.Fatal("valid original typed observation lost", st.UsageObservation)
				}
			} else if st.UsageObservation != nil {
				t.Fatal("missing/invalid original proof was promoted", st.UsageObservation)
			}
			assertUsageHeartbeatWire(t, st)
			after, err := os.ReadFile(s.statePath(resultFile))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("status/send mutated private raw evidence", err)
			}
		})
	}
}

func TestUsageHeartbeatSendOmitsRawForDirectStatuses(t *testing.T) {
	for _, cost := range []float64{0, 1.25} {
		for _, typed := range []bool{false, true} {
			st := protocol.Status{Session: "synthetic-direct", Boot: protocol.BootReady, ObservedAt: time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC), Agent: protocol.AgentState{State: protocol.AgentBusy}, Usage: &protocol.Usage{CostUSD: cost, InputTokens: 5}}
			if typed {
				observation := protocol.UnknownUsage()
				st.UsageObservation = &observation
			}
			original := *st.Usage
			assertUsageHeartbeatWire(t, st)
			if st.Usage == nil || *st.Usage != original {
				t.Fatal("Send erased its caller's private raw evidence")
			}
		}
	}
}
