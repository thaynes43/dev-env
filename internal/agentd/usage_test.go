package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	st := CollectStatus(context.Background(), &fakeRunner{}, s, l.Session, l.CreatedAt.Add(time.Hour))
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
