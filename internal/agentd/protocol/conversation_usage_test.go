package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func convTokens(n int64) TokenObservation { return TokenObservation{State: UsageReported, Amount: &n} }

func convValid() ConversationUsage {
	unknown := TokenObservation{State: UsageUnknown}
	return ConversationUsage{Version: ConversationUsageVersion, Provider: AgentClaude, ObservedAt: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		Coverage: ConversationCoverage, CounterSemantics: ConversationCumulative, ResultState: ConversationFinal, Turns: 2,
		Models: []ConversationModelUsage{
			{Model: "claude-haiku-4-5", InputTokens: convTokens(10), OutputTokens: convTokens(0), CacheReadInputTokens: convTokens(5), CacheCreationInputTokens: unknown},
			{Model: "claude-opus-5-5", InputTokens: convTokens(100), OutputTokens: convTokens(20), CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown},
		}}
}

func TestConversationUsageValid(t *testing.T) {
	if !convValid().Valid() {
		t.Fatal("a valid observation was refused")
	}
	if u := UnknownConversationUsage(AgentCodex, ReasonUnbound, time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)); !u.Valid() || u.Models == nil {
		t.Fatalf("Unknown observation %+v", u)
	}
	over := MaxUsageTokens + 1
	negative := int64(-1)
	for name, edit := range map[string]func(*ConversationUsage){
		"version":              func(u *ConversationUsage) { u.Version = 2 },
		"provider":             func(u *ConversationUsage) { u.Provider = "opencode" },
		"no time":              func(u *ConversationUsage) { u.ObservedAt = time.Time{} },
		"coverage is the task": func(u *ConversationUsage) { u.Coverage = UsageInitial },
		"semantics":            func(u *ConversationUsage) { u.CounterSemantics = UsageFinalTotal },
		"result word":          func(u *ConversationUsage) { u.ResultState = "Complete" },
		"negative turns":       func(u *ConversationUsage) { u.Turns = -1 },
		"too many turns":       func(u *ConversationUsage) { u.Turns = MaxConversationCount + 1 },
		"negative skipped":     func(u *ConversationUsage) { u.Skipped = -1 },
		"final with a reason":  func(u *ConversationUsage) { u.ReasonCode = ReasonSkippedRecords },
		"final with skipped":   func(u *ConversationUsage) { u.Skipped = 1 },
		"final without models": func(u *ConversationUsage) { u.Models = nil },
		"partial no reason":    func(u *ConversationUsage) { u.ResultState = ConversationPartial },
		"partial bad reason":   func(u *ConversationUsage) { u.ResultState, u.ReasonCode = ConversationPartial, "because" },
		"partial markup":       func(u *ConversationUsage) { u.ResultState, u.ReasonCode = ConversationPartial, "<b>" },
		"unknown with models":  func(u *ConversationUsage) { u.ResultState, u.ReasonCode = ConversationUnknown, ReasonNoUsage },
		"unknown no reason": func(u *ConversationUsage) {
			u.ResultState, u.Models, u.Turns = ConversationUnknown, nil, 0
		},
		"unknown with turns": func(u *ConversationUsage) {
			u.ResultState, u.ReasonCode, u.Models, u.Turns = ConversationUnknown, ReasonNoUsage, nil, 3
		},
		"duplicate model":  func(u *ConversationUsage) { u.Models[1].Model = u.Models[0].Model },
		"unordered models": func(u *ConversationUsage) { u.Models[0], u.Models[1] = u.Models[1], u.Models[0] },
		"bad model id":     func(u *ConversationUsage) { u.Models[0].Model = "<synthetic>" },
		"long model id":    func(u *ConversationUsage) { u.Models[0].Model = "m" + strings.Repeat("x", MaxUsageModelID) },
		"nine models": func(u *ConversationUsage) {
			u.Models = nil
			for i := 0; i < 9; i++ {
				u.Models = append(u.Models, ConversationModelUsage{Model: "claude-opus-" + string(rune('a'+i)), InputTokens: convTokens(1), OutputTokens: convTokens(1), CacheReadInputTokens: convTokens(1), CacheCreationInputTokens: convTokens(1)})
			}
		},
		"over-limit tokens": func(u *ConversationUsage) {
			u.Models[0].InputTokens = TokenObservation{State: UsageReported, Amount: &over}
		},
		"negative tokens": func(u *ConversationUsage) {
			u.Models[0].InputTokens = TokenObservation{State: UsageReported, Amount: &negative}
		},
		"reported no amount": func(u *ConversationUsage) { u.Models[0].OutputTokens = TokenObservation{State: UsageReported} },
		"unknown with amount": func(u *ConversationUsage) {
			zero := int64(0)
			u.Models[0].CacheReadInputTokens = TokenObservation{State: UsageUnknown, Amount: &zero}
		},
	} {
		u := convValid()
		edit(&u)
		if u.Valid() {
			t.Errorf("%s was accepted", name)
		}
	}
	// Partial with a reason is valid, and an explicit zero stays distinct from Unknown.
	p := convValid()
	p.ResultState, p.ReasonCode, p.Skipped = ConversationPartial, ReasonSkippedRecords, 2
	if !p.Valid() {
		t.Error("a Partial observation was refused")
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"outputTokens":{"state":"Reported","amount":0}`) || !strings.Contains(string(raw), `"cacheCreationInputTokens":{"state":"Unknown"}`) {
		t.Errorf("zero and Unknown are not distinct on the wire: %s", raw)
	}
}
