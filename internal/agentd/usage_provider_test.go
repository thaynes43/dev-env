package agentd

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// assertNoZeroForUnknown walks every token and cost observation: an Unknown
// metric never carries an amount, so no reader can mistake it for zero.
func assertNoZeroForUnknown(t *testing.T, u protocol.UsageObservation) {
	t.Helper()
	tokens := []protocol.TokenObservation{u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens}
	costs := []protocol.CostObservation{u.Cost}
	for _, m := range u.ServedModels {
		tokens = append(tokens, m.InputTokens, m.OutputTokens, m.CacheReadInputTokens, m.CacheCreationInputTokens)
		costs = append(costs, m.Cost)
	}
	for _, m := range tokens {
		if m.State != protocol.UsageReported && m.Amount != nil {
			t.Fatal("unknown token count carries an amount", m)
		}
	}
	for _, c := range costs {
		if c.State != protocol.UsageReported && (c.Amount != "" || c.Unit != "") {
			t.Fatal("unknown cost carries an amount", c)
		}
	}
	if err := u.Validate(); err != nil && u.Source.Valid() {
		t.Fatal("parsed observation does not validate", err)
	}
}

func claudeResult(t *testing.T, modelUsage string) (streamResult, string) {
	t.Helper()
	var result streamResult
	var out bytes.Buffer
	line := `{"type":"result","subtype":"success","num_turns":2,"total_cost_usd":0.75,"usage":{"input_tokens":30,"output_tokens":40}` + modelUsage + `}`
	renderStreamLine([]byte(line), &out, &result)
	if !result.Seen {
		t.Fatal("result not seen")
	}
	return result, out.String()
}

func TestUsageClaudeServedModelsFromResult(t *testing.T) {
	t.Run("one model", func(t *testing.T) {
		result, out := claudeResult(t, `,"modelUsage":{"claude-opus-5-5":{"inputTokens":30,"outputTokens":40,"cacheReadInputTokens":0,"cacheCreationInputTokens":5,"webSearchRequests":0,"costUSD":0.75,"contextWindow":200000}}`)
		u := result.Observation
		assertNoZeroForUnknown(t, u)
		if u.Version != protocol.UsageVersion || u.Provenance != protocol.UsageProviderReported || u.ActualModelState != protocol.UsageProviderReported || len(u.ServedModels) != 1 {
			t.Fatal("served model not recorded", u)
		}
		m := u.ServedModels[0]
		if m.Model != "claude-opus-5-5" || *m.InputTokens.Amount != 30 || *m.OutputTokens.Amount != 40 || *m.CacheReadInputTokens.Amount != 0 || *m.CacheCreationInputTokens.Amount != 5 || m.Cost.Amount != "0.75" || m.Cost.Kind != protocol.UsageCLIEstimate {
			t.Fatal("per-model counters or cost lost", m)
		}
		if u.Cost.Amount != "0.75" || *u.InputTokens.Amount != 30 {
			t.Fatal("invocation totals lost", u)
		}
		if !strings.Contains(out, "served model claude-opus-5-5 (provider reported)") || strings.Contains(out, "actual model Unknown") {
			t.Fatal("log line does not name the served model", out)
		}
	})
	t.Run("several models sorted", func(t *testing.T) {
		result, out := claudeResult(t, `,"modelUsage":{"claude-opus-5-5":{"inputTokens":20,"outputTokens":30,"costUSD":0.7},"claude-haiku-4-5":{"inputTokens":10,"outputTokens":10,"costUSD":0.05}}`)
		u := result.Observation
		assertNoZeroForUnknown(t, u)
		if len(u.ServedModels) != 2 || u.ServedModels[0].Model != "claude-haiku-4-5" || u.ServedModels[1].Model != "claude-opus-5-5" || u.ServedModels[0].Cost.Amount != "0.05" {
			t.Fatal("multi-model order or values lost", u.ServedModels)
		}
		if u.ServedModels[0].CacheReadInputTokens.State != protocol.UsageUnknown || u.ServedModels[0].CacheReadInputTokens.Amount != nil {
			t.Fatal("absent per-model cache counter became a value")
		}
		if !strings.Contains(out, "served models claude-haiku-4-5, claude-opus-5-5 (provider reported)") {
			t.Fatal("log line", out)
		}
	})
	t.Run("absent", func(t *testing.T) {
		result, out := claudeResult(t, ``)
		u := result.Observation
		assertNoZeroForUnknown(t, u)
		if u.ActualModelState != protocol.UsageUnknown || u.ServedModels != nil || !strings.Contains(out, "actual model Unknown") {
			t.Fatal("absent modelUsage invented a model", u, out)
		}
		if u.Cost.Amount != "0.75" || u.Provenance != protocol.UsageProviderReported {
			t.Fatal("totals lost without modelUsage", u)
		}
	})
	for name, raw := range map[string]string{
		"null":           `,"modelUsage":null`,
		"string":         `,"modelUsage":"claude-opus-5-5"`,
		"array":          `,"modelUsage":[{"model":"claude-opus-5-5"}]`,
		"empty":          `,"modelUsage":{}`,
		"entry number":   `,"modelUsage":{"claude-opus-5-5":7}`,
		"entry null":     `,"modelUsage":{"claude-opus-5-5":null}`,
		"duplicate key":  `,"modelUsage":{"claude-opus-5-5":{"inputTokens":1},"claude-opus-5-5":{"inputTokens":2}}`,
		"invalid id":     `,"modelUsage":{"claude opus":{"inputTokens":1}}`,
		"overlong id":    `,"modelUsage":{"` + strings.Repeat("m", 257) + `":{"inputTokens":1}}`,
		"one bad of two": `,"modelUsage":{"claude-opus-5-5":{"inputTokens":1},"bad id":{"inputTokens":2}}`,
		"too many": func() string {
			var b strings.Builder
			b.WriteString(`,"modelUsage":{`)
			for i := 0; i <= protocol.MaxServedModels; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				b.WriteString(`"model-` + string(rune('a'+i)) + `":{"inputTokens":1}`)
			}
			b.WriteString("}")
			return b.String()
		}(),
	} {
		t.Run("malformed "+name, func(t *testing.T) {
			result, out := claudeResult(t, raw)
			u := result.Observation
			assertNoZeroForUnknown(t, u)
			if u.ActualModelState != protocol.UsageUnknown || len(u.ServedModels) != 0 || !strings.Contains(out, "actual model Unknown") {
				t.Fatal("malformed modelUsage kept a partial or invented model list", u.ServedModels)
			}
			if u.Cost.Amount != "0.75" || *u.InputTokens.Amount != 30 {
				t.Fatal("malformed modelUsage erased reported totals", u)
			}
		})
	}
	t.Run("invalid per-model members stay unknown", func(t *testing.T) {
		result, _ := claudeResult(t, `,"modelUsage":{"claude-opus-5-5":{"inputTokens":-1,"outputTokens":null,"cacheReadInputTokens":"5","cacheCreationInputTokens":1e999,"costUSD":"0.1"}}`)
		u := result.Observation
		assertNoZeroForUnknown(t, u)
		m := u.ServedModels[0]
		if m.InputTokens.Amount != nil || m.OutputTokens.Amount != nil || m.CacheReadInputTokens.Amount != nil || m.CacheCreationInputTokens.Amount != nil || m.Cost.State != protocol.UsageUnknown {
			t.Fatal("invalid per-model member became a value", m)
		}
	})
	t.Run("replay with changed models is a conflict", func(t *testing.T) {
		var result streamResult
		line := []byte(`{"type":"result","subtype":"success","modelUsage":{"claude-opus-5-5":{"inputTokens":1}}}`)
		renderStreamLine(line, io.Discard, &result)
		first := result.Observation
		renderStreamLine(line, io.Discard, &result)
		if result.UsageConflict || !reflect.DeepEqual(first, result.Observation) {
			t.Fatal("identical replay changed original")
		}
		renderStreamLine([]byte(`{"type":"result","subtype":"success","modelUsage":{"claude-opus-5-5":{"inputTokens":2}}}`), io.Discard, &result)
		if !result.UsageConflict || !reflect.DeepEqual(first, result.Observation) {
			t.Fatal("cumulative result replaced the original served models")
		}
	})
}

func TestUsageCodexServedModel(t *testing.T) {
	for _, tc := range []struct{ name, model, want string }{
		{"absent", ``, ""},
		{"present", `,"model":"gpt-6.1-sol"`, "gpt-6.1-sol"},
		{"null", `,"model":null`, ""},
		{"number", `,"model":7`, ""},
		{"empty", `,"model":""`, ""},
		{"invalid", `,"model":"gpt 6"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, l := captureLaunch(t)
			l.CaptureOutput, l.Provider, l.ConversationID = false, protocol.AgentCodex, ""
			path := writeLaunch(t, s, l)
			stream := codexStream{}
			var result streamResult
			for _, line := range []string{`{"type":"thread.started","thread_id":"` + captureConversation + `"}`, `{"type":"turn.started"}`, `{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":7,"cached_input_tokens":3}` + tc.model + `}`} {
				if err := stream.line([]byte(line), path, &l, io.Discard, &result); err != nil {
					t.Fatal("served model field refused the turn", err)
				}
			}
			u := result.Observation
			assertNoZeroForUnknown(t, u)
			if u.Cost.State != protocol.UsageUnknown || u.Version != protocol.UsageVersion || u.Provenance != protocol.UsageProviderReported {
				t.Fatal("codex token-only shape lost", u)
			}
			if tc.want == "" {
				if u.ActualModelState != protocol.UsageUnknown || u.ServedModels != nil {
					t.Fatal("codex model invented", u.ServedModels)
				}
				return
			}
			if u.ActualModelState != protocol.UsageProviderReported || len(u.ServedModels) != 1 || u.ServedModels[0].Model != tc.want || *u.ServedModels[0].InputTokens.Amount != 5 || *u.ServedModels[0].CacheReadInputTokens.Amount != 3 || u.ServedModels[0].CacheCreationInputTokens.Amount != nil || u.ServedModels[0].Cost.State != protocol.UsageUnknown {
				t.Fatal("codex served model counters", u.ServedModels)
			}
		})
	}
}

func TestUsageBillingBasisFollowsLaunchPolicy(t *testing.T) {
	claude := func(l *Launch) { l.Unset = append([]string(nil), unsetForAgent...) }
	codex := func(l *Launch) {
		l.Provider, l.ConversationID = protocol.AgentCodex, ""
		l.Unset = append(append([]string(nil), unsetForAgent...), codexUnset...)
		l.Argv = []string{"codex", "exec", "--json", "-c", `forced_login_method="chatgpt"`}
	}
	for _, tc := range []struct {
		name   string
		setup  func(*Launch)
		mutate func(*Launch)
		want   string
	}{
		{"claude plan", claude, func(*Launch) {}, protocol.UsageSubscription},
		{"claude key kept", claude, func(l *Launch) { l.Unset = []string{"ANTHROPIC_AUTH_TOKEN"} }, protocol.UsageUnknown},
		{"claude key set", claude, func(l *Launch) { l.Env = []string{"ANTHROPIC_API_KEY=synthetic"} }, protocol.UsageUnknown},
		{"codex chatgpt", codex, func(*Launch) {}, protocol.UsageSubscription},
		{"codex no forced login", codex, func(l *Launch) { l.Argv = []string{"codex", "exec", "--json"} }, protocol.UsageUnknown},
		{"codex key kept", codex, func(l *Launch) { l.Unset = unsetForAgent }, protocol.UsageUnknown},
		{"unknown provider", claude, func(l *Launch) { l.Provider = "opencode" }, protocol.UsageUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, l := captureLaunch(t)
			tc.setup(&l)
			tc.mutate(&l)
			if got := launchBillingBasis(l); got != tc.want {
				t.Fatal("billing basis", got, tc.want)
			}
		})
	}
}

func TestUsageRetainedObservationCarriesBillingBasis(t *testing.T) {
	s, l := captureLaunch(t)
	l.CaptureOutput, l.Unset = false, append([]string(nil), unsetForAgent...)
	sess := protocol.Session{SessionUID: l.SessionUID, Agent: l.Provider, Mode: protocol.ModeTask, Model: l.CaptureModel}
	if err := bindInitialUsage(&l, sess, l.PodUID); err != nil {
		t.Fatal(err)
	}
	if err := beginInitialUsage(l, s.StateDir); err != nil {
		t.Fatal(err)
	}
	var result streamResult
	renderStreamLine([]byte(`{"type":"result","subtype":"success","total_cost_usd":0.5,"usage":{"input_tokens":3},"modelUsage":{"claude-opus-5-5":{"inputTokens":3,"costUSD":0.5}}}`), io.Discard, &result)
	writeResult(s.StateDir, l, l.CreatedAt, 0, false, result)
	var tr taskResult
	if err := readJSONFile(s.statePath(resultFile), &tr); err != nil {
		t.Fatal(err)
	}
	u := tr.UsageObservation
	if u == nil || u.Validate() != nil || u.BillingBasis != protocol.UsageSubscription || u.ActualModelState != protocol.UsageProviderReported || u.ServedModels[0].Model != "claude-opus-5-5" {
		t.Fatal("retained observation lost served model or billing basis", u)
	}
	raw, _ := json.Marshal(u)
	if collected := collectedInitialUsage(l, s.StateDir, u); collected == nil || !reflect.DeepEqual(*collected, *u) {
		t.Fatal("collected observation differs from retained", string(raw))
	}
}
