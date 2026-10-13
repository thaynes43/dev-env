package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func providerUsage(provider string) UsageObservation {
	u := UnknownUsage()
	u.Source = UsageSource{SessionUID: "session", PodUID: "pod", Provider: provider, RequestedModel: "requested-model", AttemptID: strings.Repeat("a", 64)}
	u.ID, u.ObservedAt, u.ResultState = UsageObservationID(u.Source), time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC), "Final"
	return u
}

func reported(n int64) TokenObservation { return TokenObservation{State: UsageReported, Amount: &n} }

func unknownModel(model string) ModelUsage {
	unknown := TokenObservation{State: UsageUnknown}
	return ModelUsage{Model: model, InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: CostObservation{State: UsageUnknown, Kind: UsageUnknown}}
}

func TestUsageVersionTwoProviderReportedShape(t *testing.T) {
	u := providerUsage(AgentClaude)
	if u.Version != UsageVersion || u.Provenance != UsageProviderReported || u.BillingBasis != UsageUnknown || u.ActualModelState != UsageUnknown || u.Validate() != nil {
		t.Fatal("new observations are not version 2 with ProviderReported provenance", u, u.Validate())
	}
	opus := unknownModel("claude-opus-5-5")
	opus.InputTokens, opus.Cost = reported(0), CostObservation{State: UsageReported, Amount: "0.5", Unit: "USD", Kind: UsageCLIEstimate}
	u.ServedModels = []ModelUsage{unknownModel("claude-haiku-4-5"), opus}
	u.ActualModelState, u.BillingBasis = UsageProviderReported, UsageSubscription
	if err := u.Validate(); err != nil {
		t.Fatal("valid served models refused", err)
	}
	raw, _ := json.Marshal(u)
	for _, field := range []string{`"version":2`, `"provenance":"ProviderReported"`, `"billingBasis":"Subscription"`, `"servedModels":[{"model":"claude-haiku-4-5"`, `"actualModelState":"ProviderReported"`} {
		if !strings.Contains(string(raw), field) {
			t.Fatal("wire shape lost", field, string(raw))
		}
	}
	var decoded UsageObservation
	if json.Unmarshal(raw, &decoded) != nil || decoded.Validate() != nil || len(decoded.ServedModels) != 2 || decoded.ServedModels[1].InputTokens.Amount == nil || *decoded.ServedModels[1].InputTokens.Amount != 0 || decoded.ServedModels[0].InputTokens.Amount != nil {
		t.Fatal("served model presence lost in round trip", decoded)
	}
}

func TestUsageVersionTwoRefusesInventedOrInconsistentModels(t *testing.T) {
	valid := func() UsageObservation {
		u := providerUsage(AgentClaude)
		u.ServedModels = []ModelUsage{unknownModel("claude-haiku-4-5"), unknownModel("claude-opus-5-5")}
		u.ActualModelState = UsageProviderReported
		return u
	}
	if err := valid().Validate(); err != nil {
		t.Fatal(err)
	}
	many := make([]ModelUsage, 0, MaxServedModels+1)
	for i := 0; i <= MaxServedModels; i++ {
		many = append(many, unknownModel("model-"+string(rune('a'+i))))
	}
	negative := int64(-1)
	for name, mutate := range map[string]func(*UsageObservation){
		"state without models": func(u *UsageObservation) { u.ServedModels = nil },
		"models without state": func(u *UsageObservation) { u.ActualModelState = UsageUnknown },
		"invented state, no models": func(u *UsageObservation) {
			u.ServedModels, u.ActualModelState = nil, "Bogus"
		},
		"empty state, no models": func(u *UsageObservation) { u.ServedModels, u.ActualModelState = nil, "" },
		"unsorted":               func(u *UsageObservation) { u.ServedModels[0], u.ServedModels[1] = u.ServedModels[1], u.ServedModels[0] },
		"duplicate":              func(u *UsageObservation) { u.ServedModels[1].Model = u.ServedModels[0].Model },
		"too many":               func(u *UsageObservation) { u.ServedModels = many },
		"empty id":               func(u *UsageObservation) { u.ServedModels[0].Model = "" },
		"whitespace id":          func(u *UsageObservation) { u.ServedModels[0].Model = "claude haiku" },
		"overlong id":            func(u *UsageObservation) { u.ServedModels[1].Model = "m" + strings.Repeat("x", 256) },
		"negative model counter": func(u *UsageObservation) {
			u.ServedModels[0].OutputTokens = TokenObservation{State: UsageReported, Amount: &negative}
		},
		"zero for unknown": func(u *UsageObservation) {
			zero := int64(0)
			u.ServedModels[0].OutputTokens = TokenObservation{State: UsageUnknown, Amount: &zero}
		},
		"model cost not estimate": func(u *UsageObservation) {
			u.ServedModels[0].Cost = CostObservation{State: UsageReported, Amount: "1", Unit: "USD", Kind: "APIBilling"}
		},
		"provenance missing":   func(u *UsageObservation) { u.Provenance = "" },
		"provenance invented":  func(u *UsageObservation) { u.Provenance = "Attested" },
		"billing invented":     func(u *UsageObservation) { u.BillingBasis = "APIBilling" },
		"billing missing":      func(u *UsageObservation) { u.BillingBasis = "" },
		"quota invented":       func(u *UsageObservation) { u.SubscriptionQuotaState = UsageProviderReported },
		"api billing invented": func(u *UsageObservation) { u.APIBillingState = UsageProviderReported },
		"version three":        func(u *UsageObservation) { u.Version = 3 },
	} {
		u := valid()
		mutate(&u)
		if u.Validate() == nil {
			t.Fatal("accepted", name)
		}
	}
	codex := providerUsage(AgentCodex)
	codex.ServedModels = []ModelUsage{unknownModel("gpt-6.1-sol")}
	codex.ActualModelState = UsageProviderReported
	if err := codex.Validate(); err != nil {
		t.Fatal("codex served model refused", err)
	}
	codex.ServedModels[0].Cost = CostObservation{State: UsageReported, Amount: "0", Unit: "USD", Kind: UsageCLIEstimate}
	if codex.Validate() == nil {
		t.Fatal("codex per-model cost asserted")
	}
}

func TestUsageVersionOneKeepsOriginalRules(t *testing.T) {
	u := providerUsage(AgentClaude)
	u.Version, u.Provenance, u.BillingBasis = 1, "", ""
	if err := u.Validate(); err != nil {
		t.Fatal("legacy version 1 observation refused", err)
	}
	for name, mutate := range map[string]func(*UsageObservation){
		"provenance": func(u *UsageObservation) { u.Provenance = UsageProviderReported },
		"billing":    func(u *UsageObservation) { u.BillingBasis = UsageSubscription },
		"models": func(u *UsageObservation) {
			u.ServedModels, u.ActualModelState = []ModelUsage{unknownModel("claude-opus-5-5")}, UsageProviderReported
		},
		"actual model": func(u *UsageObservation) { u.ActualModelState = UsageProviderReported },
	} {
		v := u
		mutate(&v)
		if v.Validate() == nil {
			t.Fatal("version 1 accepted version 2 field", name)
		}
	}
}

func TestUsageModelIDBounds(t *testing.T) {
	for _, ok := range []string{"claude-opus-5-5", "claude-opus-4-1-20250805", "gpt-6.1-sol", "us.anthropic.claude-sonnet-4-5-v1:0", "claude-sonnet-5-5[1m]", strings.Repeat("m", 256)} {
		if !ValidUsageModelID(ok) {
			t.Fatal("refused", ok)
		}
	}
	for _, bad := range []string{"", " lead", "has space", "tab\tid", "quote\"", "-lead", strings.Repeat("m", 257), "nul\x00", "ünicode"} {
		if ValidUsageModelID(bad) {
			t.Fatal("accepted", bad)
		}
	}
}
