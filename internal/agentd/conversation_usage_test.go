package agentd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const usageConversation = "12345678-1234-1234-1234-123456789abc"

var usageNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// Placeholders the fixtures carry in the places a transcript holds text. None may
// ever appear in a result.
var usageFixtureText = []string{"FIXTURE-USER-TEXT-DO-NOT-LEAK", "FIXTURE-ASSISTANT-TEXT-DO-NOT-LEAK", "FIXTURE-TOOL-RESULT-DO-NOT-LEAK", "FIXTURE-CODEX-TEXT-DO-NOT-LEAK"}

func usageFixture(t *testing.T, name, conversation string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "conversation_usage", name))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(string(raw), "CONVERSATION_ID", conversation))
}

func amount(t *testing.T, m protocol.TokenObservation) int64 {
	t.Helper()
	if m.State != protocol.UsageReported || m.Amount == nil {
		t.Fatalf("counter %+v is not Reported", m)
	}
	return *m.Amount
}

func wantRow(t *testing.T, r protocol.ConversationModelUsage, model string, in, out int64, read, write *int64) {
	t.Helper()
	if r.Model != model || amount(t, r.InputTokens) != in || amount(t, r.OutputTokens) != out {
		t.Fatalf("row %+v, want %s in %d out %d", r, model, in, out)
	}
	for name, c := range map[string]struct {
		got  protocol.TokenObservation
		want *int64
	}{"cache read": {r.CacheReadInputTokens, read}, "cache creation": {r.CacheCreationInputTokens, write}} {
		if c.want == nil {
			if c.got.State != protocol.UsageUnknown || c.got.Amount != nil {
				t.Fatalf("%s %+v, want Unknown", name, c.got)
			}
		} else if amount(t, c.got) != *c.want {
			t.Fatalf("%s %+v, want %d", name, c.got, *c.want)
		}
	}
}

func n64(n int64) *int64 { return &n }

// assertNoText proves the serialized observation holds none of the fixtures' text.
func assertNoText(t *testing.T, u protocol.ConversationUsage) {
	t.Helper()
	raw, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range usageFixtureText {
		if strings.Contains(string(raw), text) {
			t.Fatalf("the observation carries conversation text %q: %s", text, raw)
		}
	}
	for _, key := range []string{"content", "text", "prompt", "message", "cwd", "uuid"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("the observation has a %q field: %s", key, raw)
		}
	}
}

func TestConversationUsageClaudeDedupesStreamedMessagesAndSumsPerModel(t *testing.T) {
	for _, fixture := range []string{"claude_main.jsonl", "claude_reordered.jsonl"} {
		t.Run(fixture, func(t *testing.T) {
			u := claudeConversationUsage(usageFixture(t, fixture, usageConversation), usageConversation, usageNow)
			if !u.Valid() || u.ResultState != protocol.ConversationFinal || u.ReasonCode != "" || u.Provider != protocol.AgentClaude || u.Coverage != protocol.ConversationCoverage || u.CounterSemantics != protocol.ConversationCumulative {
				t.Fatalf("%+v", u)
			}
			// msg_A streamed in three records (output 1, 40, 40) counts once, at its maximum.
			// msg_B has explicit zero cache counters. msg_C carries no cache fields at all.
			if u.Turns != 3 || u.Skipped != 0 || len(u.Models) != 2 {
				t.Fatalf("turns %d skipped %d models %d", u.Turns, u.Skipped, len(u.Models))
			}
			wantRow(t, u.Models[0], "claude-haiku-4-5", 10, 5, nil, nil)
			wantRow(t, u.Models[1], "claude-opus-5-5", 300, 100, n64(50), n64(10))
			assertNoText(t, u)
		})
	}
}

func TestConversationUsageClaudeCacheTokensAreTheirOwnCounters(t *testing.T) {
	u := claudeConversationUsage(usageFixture(t, "claude_main.jsonl", usageConversation), usageConversation, usageNow)
	opus := u.Models[1]
	// Input is the input counters alone: 100 + 200. Cache read (50) and creation (10)
	// are not folded into it, and an explicit zero is Reported, not Unknown.
	if amount(t, opus.InputTokens) != 300 || amount(t, opus.CacheReadInputTokens) != 50 || amount(t, opus.CacheCreationInputTokens) != 10 {
		t.Fatalf("%+v", opus)
	}
	only := []byte(`{"cwd":"/w","sessionId":"` + usageConversation + `","version":"2.1.296","message":{"id":"m1","model":"claude-opus-5-5","usage":{"input_tokens":5,"output_tokens":6,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}},"type":"assistant"}` + "\n")
	z := claudeConversationUsage(only, usageConversation, usageNow)
	wantRow(t, z.Models[0], "claude-opus-5-5", 5, 6, n64(0), n64(0))
}

func TestConversationUsageClaudeMalformedAndUnqualifiedRecordsMakeItPartial(t *testing.T) {
	raw := usageFixture(t, "claude_malformed.jsonl", usageConversation)
	// An assistant line over the line bound cannot be read; it is skipped.
	raw = append(raw, []byte(`{"type":"assistant","pad":"`+strings.Repeat("x", portalProjectionLineLimit)+`"}`+"\n")...)
	u := claudeConversationUsage(raw, usageConversation, usageNow)
	if !u.Valid() || u.ResultState != protocol.ConversationPartial || u.ReasonCode != protocol.ReasonSkippedRecords || u.Skipped != 7 || u.Turns != 1 {
		t.Fatalf("%+v", u)
	}
	wantRow(t, u.Models[0], "claude-opus-5-5", 100, 40, n64(50), n64(10))
	assertNoText(t, u)
}

func TestConversationUsageClaudeNoUsageIsUnknownNeverZero(t *testing.T) {
	for name, raw := range map[string][]byte{
		"only synthetic and error records": usageFixture(t, "claude_nousage.jsonl", usageConversation),
		"empty":                            nil,
		"user lines only":                  []byte(`{"type":"user","sessionId":"` + usageConversation + `","message":{"role":"user","content":"FIXTURE-USER-TEXT-DO-NOT-LEAK"}}` + "\n"),
	} {
		u := claudeConversationUsage(raw, usageConversation, usageNow)
		if !u.Valid() || u.ResultState != protocol.ConversationUnknown || u.ReasonCode != protocol.ReasonNoUsage || len(u.Models) != 0 || u.Turns != 0 {
			t.Fatalf("%s: %+v", name, u)
		}
		assertNoText(t, u)
	}
	// Records that all fail are Unknown with the skipped count, not an empty total.
	bad := []byte(`{"type":"assistant","message":{"id":"m","model":"claude-opus-5-5"}}` + "\n")
	if u := claudeConversationUsage(bad, usageConversation, usageNow); !u.Valid() || u.ResultState != protocol.ConversationUnknown || u.ReasonCode != protocol.ReasonSkippedRecords || u.Skipped != 1 {
		t.Fatalf("%+v", u)
	}
}

func TestConversationUsageClaudeEdgeCases(t *testing.T) {
	line := func(id, model, usage string, extra string) string {
		return `{"cwd":"/w","sessionId":"` + usageConversation + `","version":"2.1.296","message":{"id":"` + id + `","model":"` + model + `","usage":` + usage + `},"type":"assistant"` + extra + `}` + "\n"
	}
	ok := `{"input_tokens":10,"output_tokens":2}`
	// One message id seen with two models is not countable.
	two := claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", ok, "")+line("m1", "claude-haiku-4-5", ok, "")+line("m2", "claude-opus-5-5", ok, "")), usageConversation, usageNow)
	if two.ResultState != protocol.ConversationPartial || two.Skipped != 1 || two.Turns != 1 || len(two.Models) != 1 {
		t.Fatalf("conflicting models: %+v", two)
	}
	// A cache counter some messages carried and others did not is a lower bound.
	mixed := claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", `{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":9}`, "")+line("m2", "claude-opus-5-5", ok, "")), usageConversation, usageNow)
	if mixed.ResultState != protocol.ConversationPartial || mixed.ReasonCode != protocol.ReasonMissingCounters || amount(t, mixed.Models[0].CacheReadInputTokens) != 9 || !mixed.Valid() {
		t.Fatalf("missing counters: %+v", mixed)
	}
	// A case alias of an interpreted key is refused rather than guessed at.
	alias := claudeConversationUsage([]byte(strings.Replace(line("m1", "claude-opus-5-5", ok, ""), `"usage"`, `"Usage"`, 1)), usageConversation, usageNow)
	if alias.ResultState != protocol.ConversationUnknown || alias.Skipped != 1 {
		t.Fatalf("alias: %+v", alias)
	}
	// A duplicate key is not strict JSON.
	dup := claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", `{"input_tokens":1,"input_tokens":2,"output_tokens":1}`, "")), usageConversation, usageNow)
	if dup.ResultState != protocol.ConversationUnknown || dup.Skipped != 1 {
		t.Fatalf("duplicate: %+v", dup)
	}
	// A counter over the bound is skipped; totals past it are Unknown.
	big := fmt.Sprintf(`{"input_tokens":%d,"output_tokens":1}`, protocol.MaxUsageTokens)
	over := claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", big, "")+line("m2", "claude-opus-5-5", big, "")), usageConversation, usageNow)
	if over.ResultState != protocol.ConversationUnknown || over.ReasonCode != protocol.ReasonCountOverflow {
		t.Fatalf("overflow: %+v", over)
	}
	// More served models than the bound keeps is Unknown, not a part of the list.
	var many strings.Builder
	for i := 0; i < protocol.MaxConversationModels+1; i++ {
		many.WriteString(line(fmt.Sprintf("m%d", i), fmt.Sprintf("claude-model-%d", i), ok, ""))
	}
	if u := claudeConversationUsage([]byte(many.String()), usageConversation, usageNow); u.ReasonCode != protocol.ReasonTooManyModels || u.ResultState != protocol.ConversationUnknown {
		t.Fatalf("many models: %+v", u)
	}
	// Another conversation id, a bad conversation id and no clock are unbound.
	for _, u := range []protocol.ConversationUsage{
		claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", ok, "")), "aaaaaaaa-1234-1234-1234-123456789abc", usageNow),
		claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", ok, "")), "not-a-thread", usageNow),
		claudeConversationUsage([]byte(line("m1", "claude-opus-5-5", ok, "")), usageConversation, time.Time{}),
	} {
		if u.ResultState != protocol.ConversationUnknown {
			t.Fatalf("%+v", u)
		}
	}
}

func TestConversationUsageCodexRepeatedCumulativeSnapshotsAreNotSummed(t *testing.T) {
	u := codexConversationUsage(usageFixture(t, "codex_main.jsonl", usageConversation), usageNow)
	// Snapshots: null info (ignored), (1000,50,200) twice, (2500,120,900) three times.
	// last_token_usage differs from the totals and is never read.
	if !u.Valid() || u.ResultState != protocol.ConversationFinal || u.Provider != protocol.AgentCodex || u.Turns != 2 || u.Skipped != 0 || len(u.Models) != 1 {
		t.Fatalf("%+v", u)
	}
	wantRow(t, u.Models[0], "gpt-6-astra", 2500, 120, n64(900), nil)
	assertNoText(t, u)
}

func TestConversationUsageCodexCounterResetIsPartialAndNeverAddedAcross(t *testing.T) {
	u := codexConversationUsage(usageFixture(t, "codex_reset.jsonl", usageConversation), usageNow)
	// 1000,2500 then 300 (a reset) then 400. The figure is the latest run alone.
	if !u.Valid() || u.ResultState != protocol.ConversationPartial || u.ReasonCode != protocol.ReasonCounterReset || u.Turns != 4 {
		t.Fatalf("%+v", u)
	}
	wantRow(t, u.Models[0], "gpt-6-astra", 400, 15, n64(60), nil)
	// A drop in the cache counter alone is a reset too.
	cache := []byte(`{"type":"turn_context","payload":{"model":"gpt-6-astra"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":5,"cached_input_tokens":9}}}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":20,"output_tokens":6,"cached_input_tokens":3}}}}` + "\n")
	if u := codexConversationUsage(cache, usageNow); u.ReasonCode != protocol.ReasonCounterReset {
		t.Fatalf("%+v", u)
	}
}

func TestConversationUsageCodexModelRules(t *testing.T) {
	two := codexConversationUsage(usageFixture(t, "codex_two_models.jsonl", usageConversation), usageNow)
	if !two.Valid() || two.ResultState != protocol.ConversationPartial || two.ReasonCode != protocol.ReasonModelSplit || len(two.Models) != 2 {
		t.Fatalf("%+v", two)
	}
	for _, m := range two.Models {
		if m.InputTokens.State != protocol.UsageUnknown || m.OutputTokens.State != protocol.UsageUnknown || m.CacheReadInputTokens.State != protocol.UsageUnknown {
			t.Fatalf("a split model carries a guessed share: %+v", m)
		}
	}
	none := codexConversationUsage(usageFixture(t, "codex_no_model.jsonl", usageConversation), usageNow)
	if !none.Valid() || none.ResultState != protocol.ConversationUnknown || none.ReasonCode != protocol.ReasonModelUnknown {
		t.Fatalf("%+v", none)
	}
}

func TestConversationUsageCodexMalformedAndNoUsage(t *testing.T) {
	u := codexConversationUsage(usageFixture(t, "codex_malformed.jsonl", usageConversation), usageNow)
	// Two unreadable token_count records are skipped; the valid snapshot counts.
	if !u.Valid() || u.ResultState != protocol.ConversationPartial || u.ReasonCode != protocol.ReasonSkippedRecords || u.Skipped != 2 || u.Turns != 1 {
		t.Fatalf("%+v", u)
	}
	wantRow(t, u.Models[0], "gpt-6-astra", 10, 5, nil, nil)
	none := codexConversationUsage(usageFixture(t, "codex_no_usage.jsonl", usageConversation), usageNow)
	if !none.Valid() || none.ResultState != protocol.ConversationUnknown || none.ReasonCode != protocol.ReasonNoUsage || len(none.Models) != 0 {
		t.Fatalf("%+v", none)
	}
	assertNoText(t, u)
	assertNoText(t, none)
}

// registeredUsageFixture builds a registered interactive session like the
// history tests do, and writes a transcript for it.
func registeredUsageFixture(t *testing.T, agent, mode string) (Settings, protocol.Session, Launch, string) {
	t.Helper()
	s, sess, ws := launchFixture(t)
	s.PodUID = "fixture-pod-uid"
	sess.SessionUID = "fixture-session-uid"
	sess.Repo = "elsewhere" // usage is not limited to the history's project
	sess.Agent = agent
	sess.Mode = mode
	if mode != protocol.ModeTask {
		sess.Prompt, sess.Limits = "", nil
	}
	var l Launch
	if agent == protocol.AgentClaude {
		built, err := BuildLaunch(s, sess, ws, "fixture-boot", usageNow)
		if err != nil {
			t.Fatal("launch fixture")
		}
		l = built
	} else {
		id := "12345678-1234-1234-1234-123456789abc"
		l = Launch{Session: sess.Name, SessionUID: sess.SessionUID, PodUID: s.PodUID, Provider: protocol.AgentCodex, Dir: ws.Worktree, ConversationID: id, BootID: "fixture-boot", TUI: true, NativeThreadConfirmed: true, CreatedAt: usageNow, Env: []string{"CODEX_HOME=" + filepath.Join(s.Home, ".codex")}}
	}
	if os.MkdirAll(s.StateDir, 0700) != nil || writeJSONFile(s.statePath(bootFile), bootRecord{BootID: l.BootID}) != nil || writeJSONFile(s.statePath(launchFile), l) != nil {
		t.Fatal("registration fixture")
	}
	var path string
	if agent == protocol.AgentClaude {
		dir := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(l.Dir))
		if os.MkdirAll(dir, 0700) != nil {
			t.Fatal("project fixture")
		}
		path = filepath.Join(dir, l.ConversationID+".jsonl")
	} else {
		dir := filepath.Join(s.Home, ".codex", "sessions", "2026", "10", "27")
		if os.MkdirAll(dir, 0700) != nil {
			t.Fatal("date fixture")
		}
		path = filepath.Join(dir, "rollout-2026-10-27T12-00-00-"+l.ConversationID+".jsonl")
	}
	return s, sess, l, path
}

func writeUsageTranscript(t *testing.T, path, fixture, conversation, worktree string) {
	t.Helper()
	data := usageFixture(t, fixture, conversation)
	data = []byte(strings.ReplaceAll(string(data), "/work/fixture", worktree))
	if os.WriteFile(path, data, 0600) != nil {
		t.Fatal("transcript fixture")
	}
}

func TestConversationUsageRegisteredClaudeConversationThroughTheFence(t *testing.T) {
	for _, mode := range []string{protocol.ModeLocal, protocol.ModeRemote} {
		t.Run(mode, func(t *testing.T) {
			s, sess, l, path := registeredUsageFixture(t, protocol.AgentClaude, mode)
			writeUsageTranscript(t, path, "claude_main.jsonl", l.ConversationID, l.Dir)
			u := MeasureConversationUsage(s, sess, sess.SessionUID, s.PodUID, usageNow)
			if !u.Valid() || u.ResultState != protocol.ConversationFinal || u.Turns != 3 || len(u.Models) != 2 {
				t.Fatalf("%+v", u)
			}
			wantRow(t, u.Models[1], "claude-opus-5-5", 300, 100, n64(50), n64(10))
			assertNoText(t, u)
		})
	}
}

func TestConversationUsageRegisteredCodexConversationThroughTheFence(t *testing.T) {
	s, sess, l, path := registeredUsageFixture(t, protocol.AgentCodex, protocol.ModeLocal)
	writeUsageTranscript(t, path, "codex_main.jsonl", l.ConversationID, l.Dir)
	u := MeasureConversationUsage(s, sess, sess.SessionUID, s.PodUID, usageNow)
	if !u.Valid() || u.ResultState != protocol.ConversationFinal || u.Provider != protocol.AgentCodex || u.Turns != 2 {
		t.Fatalf("%+v", u)
	}
	wantRow(t, u.Models[0], "gpt-6-astra", 2500, 120, n64(900), nil)
	// A rollout from another CLI version is not qualified.
	data, _ := os.ReadFile(path)
	if os.WriteFile(path, []byte(strings.Replace(string(data), managedCodexVersion, "0.0.1", 1)), 0600) != nil {
		t.Fatal("rewrite")
	}
	if u := MeasureConversationUsage(s, sess, sess.SessionUID, s.PodUID, usageNow); u.ResultState != protocol.ConversationUnknown || u.ReasonCode != protocol.ReasonUnqualified {
		t.Fatalf("%+v", u)
	}
}

func TestConversationUsageFenceRefusesWhatIsNotBound(t *testing.T) {
	for _, mode := range []string{"old launch without registration", "wrong session", "wrong pod", "no session uid", "worktree moved", "no transcript", "other file only", "hardlink", "over the read bound"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, l, path := registeredUsageFixture(t, protocol.AgentClaude, protocol.ModeLocal)
			writeUsageTranscript(t, path, "claude_main.jsonl", l.ConversationID, l.Dir)
			expectedSession, expectedPod := sess.SessionUID, s.PodUID
			want := protocol.ReasonUnbound
			switch mode {
			case "old launch without registration":
				// A launch from before registration carries no owner, pod or provider.
				legacy := l
				legacy.SessionUID, legacy.PodUID, legacy.Provider = "", "", ""
				if writeJSONFile(s.statePath(launchFile), legacy) != nil {
					t.Fatal("legacy fixture")
				}
			case "wrong session":
				expectedSession = "someone-else"
			case "wrong pod":
				expectedPod = "other-pod"
			case "no session uid":
				expectedSession = ""
			case "worktree moved":
				moved := l
				moved.Dir = "/unrelated"
				if writeJSONFile(s.statePath(launchFile), moved) != nil {
					t.Fatal("moved fixture")
				}
			case "no transcript":
				_ = os.Remove(path)
			case "other file only":
				// No fallback: another conversation's file in the same directory is not read.
				if os.Rename(path, filepath.Join(filepath.Dir(path), "aaaaaaaa-1234-1234-1234-123456789abc.jsonl")) != nil {
					t.Fatal("rename")
				}
			case "hardlink":
				if os.Link(path, filepath.Join(filepath.Dir(path), "extra")) != nil {
					t.Fatal("link")
				}
			case "over the read bound":
				big := append(usageFixture(t, "claude_main.jsonl", l.ConversationID), []byte(strings.Repeat("\n", portalProjectionInputLimit))...)
				if os.WriteFile(path, big, 0600) != nil {
					t.Fatal("big fixture")
				}
				want = protocol.ReasonSourceLimit
			}
			u := MeasureConversationUsage(s, sess, expectedSession, expectedPod, usageNow)
			if !u.Valid() || u.ResultState != protocol.ConversationUnknown || u.ReasonCode != want || len(u.Models) != 0 || u.Turns != 0 {
				t.Fatalf("%+v", u)
			}
			assertNoText(t, u)
		})
	}
}

func TestConversationUsageTaskSessionsAreExcluded(t *testing.T) {
	s, sess, l, path := registeredUsageFixture(t, protocol.AgentClaude, protocol.ModeLocal)
	writeUsageTranscript(t, path, "claude_main.jsonl", l.ConversationID, l.Dir)
	// The same registered conversation, asked for as a task session, is not read at all.
	sess.Mode = protocol.ModeTask
	sess.Prompt = "FIXTURE-USER-TEXT-DO-NOT-LEAK"
	u := MeasureConversationUsage(s, sess, sess.SessionUID, s.PodUID, usageNow)
	if !u.Valid() || u.ResultState != protocol.ConversationUnknown || u.ReasonCode != protocol.ReasonNotInteractive || len(u.Models) != 0 || u.Turns != 0 {
		t.Fatalf("%+v", u)
	}
	// Coverage is its own word: never the task's InitialManagedInvocation.
	if u.Coverage == protocol.UsageInitial || u.CounterSemantics == protocol.UsageFinalTotal {
		t.Fatalf("conversation usage shares the initial invocation's words: %+v", u)
	}
	assertNoText(t, u)
	// An agent this measures nothing for is Unknown and not publishable.
	sess.Mode, sess.Agent = protocol.ModeLocal, "opencode"
	if u := MeasureConversationUsage(s, sess, sess.SessionUID, s.PodUID, usageNow); u.ResultState != protocol.ConversationUnknown || u.Valid() {
		t.Fatalf("%+v", u)
	}
}
