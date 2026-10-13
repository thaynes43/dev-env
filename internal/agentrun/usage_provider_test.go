package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func providerTokens(n int64) protocol.TokenObservation {
	return protocol.TokenObservation{State: protocol.UsageReported, Amount: &n}
}

func providerSession() apiv1.Session {
	s := showOnceSession()
	u := *s.UsageObservation
	u.BillingBasis, u.ActualModelState = protocol.UsageSubscription, protocol.UsageProviderReported
	u.Cost = protocol.CostObservation{State: protocol.UsageReported, Amount: "0.75", Unit: "USD", Kind: protocol.UsageCLIEstimate}
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	u.ServedModels = []protocol.ModelUsage{
		{Model: "claude-haiku-4-5", InputTokens: providerTokens(10), OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: protocol.CostObservation{State: protocol.UsageReported, Amount: "0.05", Unit: "USD", Kind: protocol.UsageCLIEstimate}},
		{Model: "claude-opus-5-5", InputTokens: providerTokens(20), OutputTokens: providerTokens(5), CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}},
	}
	s.UsageObservation = &u
	s.UsageRollup = &apiv1.UsageRollup{Scope: apiv1.UsageRollupScope, Coverage: "Complete", Sessions: 2, Counted: 2,
		Invocations: []apiv1.UsageRollupKey{{SessionUID: "child-a", ObservationID: strings.Repeat("c", 64)}, {SessionUID: "child-b", ObservationID: strings.Repeat("d", 64)}},
		InputTokens: providerTokens(111), OutputTokens: providerTokens(222), CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
		Cost: protocol.CostObservation{State: protocol.UsageReported, Amount: "0.6", Unit: "USD", Kind: protocol.UsageCLIEstimate}, BillingBasis: protocol.UsageSubscription,
		ObservedModelState: protocol.UsageProviderReported, ObservedModels: []string{"claude-haiku-4-5", "claude-opus-5-5"}}
	return s
}

func TestUsageShowProviderReportedModelsBillingAndRollup(t *testing.T) {
	h := newHarness(t)
	s := providerSession()
	if err := s.UsageObservation.Validate(); err != nil {
		t.Fatal(err)
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get(apiv1.AttributionHeader) != apiv1.AttributionProvider {
			http.Error(w, "missing attribution header", http.StatusBadRequest)
			return
		}
		writeTestJSON(w, http.StatusOK, s)
	}
	h.mustRun(ExitOK, "show", s.Name)
	out := h.stdout.String()
	contains(t, "provider usage", out,
		"Actual model:", "claude-haiku-4-5, claude-opus-5-5 (provider reported)",
		"Usage provenance:", "Provider reported",
		"Billing basis:", "Subscription (plan-served; the cost is a list-price equivalent, not a charge)",
		"Initial task cost:", "0.75 USD (CLI equivalent estimate)",
		"claude-haiku-4-5: input 10, output Unknown, cache read Unknown, cache create Unknown, cost 0.05 USD (CLI equivalent estimate)",
		"claude-opus-5-5: input 20, output 5, cache read Unknown, cache create Unknown, cost Unknown",
		"Descendant usage:", "Complete over 2 retained descendant Sessions, 2 invocations counted once each; reaped descendants are not counted",
		"input tokens:", "111", "cost:", "0.6 USD (CLI equivalent estimate)", "models:", "claude-haiku-4-5, claude-opus-5-5 (provider reported)")
	if strings.Contains(out, "$") {
		t.Fatal("unqualified dollars", out)
	}
}

func TestUsageShowPartialRollupStaysUnknown(t *testing.T) {
	h := newHarness(t)
	s := providerSession()
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	s.UsageRollup = &apiv1.UsageRollup{Scope: apiv1.UsageRollupScope, Coverage: "Partial", Sessions: 3, Truncated: true,
		InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown,
		Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}, BillingBasis: protocol.UsageUnknown, ObservedModelState: protocol.UsageUnknown}
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, s) }
	h.mustRun(ExitOK, "show", s.Name)
	out := h.stdout.String()
	contains(t, "partial rollup", out, "Partial over 3 retained descendant Sessions, 0 invocations counted once each; walk stopped at the bound; reaped descendants are not counted")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "input tokens:") || strings.HasPrefix(strings.TrimSpace(line), "cost:") {
			if !strings.HasSuffix(strings.TrimSpace(line), "Unknown") {
				t.Fatal("partial rollup showed a total", line)
			}
		}
	}
}

func TestShowOnceAcceptsProviderUsageShape(t *testing.T) {
	h := newHarness(t)
	s := providerSession()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strictSessionJSON(raw) {
		t.Fatal("strict walker refused the provider usage shape", string(raw))
	}
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(raw) }
	h.mustRun(ExitOK, "show", s.Name, "--json", "--once")
	for name, bad := range map[string]string{
		"unknown model key": strings.Replace(string(raw), `"servedModels":[`, `"x":1,"servedModels":[`, 1),
		"unknown rollup":    strings.Replace(string(raw), `"usageRollup":{`, `"usageRollup":{"extra":1,`, 1),
		"duplicate scope":   strings.Replace(string(raw), `"scope":"RetainedDescendants"`, `"scope":"RetainedDescendants","scope":"RetainedDescendants"`, 1),
	} {
		if bad == string(raw) {
			t.Fatal("mutation did not apply", name)
		}
		if strictSessionJSON([]byte(bad)) {
			t.Fatal("strict walker accepted", name)
		}
	}
}

// A null served-model list is a structurally valid wire value (the walker
// allows null for any list), so show --once passes it through unchanged. The
// observation it leaves, ProviderReported with no models, fails validation,
// so show prints the served model and usage as Unknown.
func TestShowNullServedModelsIsUnknown(t *testing.T) {
	var doc map[string]any
	raw, _ := json.Marshal(providerSession())
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc["usageObservation"].(map[string]any)["servedModels"] = nil
	delete(doc, "usageRollup") // the descendants' own models are not this Session's
	nulled, _ := json.Marshal(doc)
	if !strings.Contains(string(nulled), `"servedModels":null`) || !strictSessionJSON(nulled) {
		t.Fatal("null list is not a structural wire value", string(nulled))
	}
	var s apiv1.Session
	if err := json.Unmarshal(nulled, &s); err != nil || s.UsageObservation == nil || s.UsageObservation.Validate() == nil {
		t.Fatal("ProviderReported without models validated", err)
	}
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(nulled) }
	h.mustRun(ExitOK, "show", s.Name)
	out := h.stdout.String()
	if strings.Contains(out, "provider reported") || strings.Contains(out, "Usage provenance") || strings.Contains(out, "0.75 USD") {
		t.Fatal("an invalid observation was shown as provider reported", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Actual model:") && !strings.HasSuffix(strings.TrimSpace(line), "Unknown") {
			t.Fatal("served model not Unknown", line)
		}
	}
}

func TestCanaryResultProviderAttribution(t *testing.T) {
	p := originalCanaryResult(claudeCanaryID)
	a := &p.Context.Metadata.Attribution
	a.ObservedModelState, a.UsageState, a.CostState = "ProviderReported", "ProviderReported", "ProviderReported"
	a.ObservedModels = []string{"claude-opus-5-5"}
	unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
	a.Usage = &apiv1.ContextUsage{ObservationID: strings.Repeat("e", 64), BillingBasis: protocol.UsageSubscription, InputTokens: providerTokens(30), OutputTokens: providerTokens(0),
		CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: protocol.CostObservation{State: protocol.UsageReported, Amount: "0.75", Unit: "USD", Kind: protocol.UsageCLIEstimate}}
	good, _ := json.Marshal(p)
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get(apiv1.AttributionHeader) != apiv1.AttributionProvider {
			http.Error(w, "missing attribution header", http.StatusBadRequest)
			return
		}
		_, _ = w.Write(good)
	}
	h.mustRun(ExitOK, canaryResultArgs(p)...)
	for name, mutate := range map[string]func(*apiv1.ContextAttribution){
		"state without usage":  func(a *apiv1.ContextAttribution) { a.Usage = nil },
		"models without state": func(a *apiv1.ContextAttribution) { a.ObservedModelState = "Unknown" },
		"cost without amount": func(a *apiv1.ContextAttribution) {
			a.Usage.Cost = protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}
		},
		"zero for unknown":      func(a *apiv1.ContextAttribution) { zero := int64(0); a.Usage.CacheReadInputTokens.Amount = &zero },
		"unsorted models":       func(a *apiv1.ContextAttribution) { a.ObservedModels = []string{"z-model", "a-model"} },
		"invented billing":      func(a *apiv1.ContextAttribution) { a.Usage.BillingBasis = "APIBilling" },
		"too many models":       func(a *apiv1.ContextAttribution) { a.ObservedModels = strings.Split("a,b,c,d,e,f,g,h,i", ",") },
		"invented state":        func(a *apiv1.ContextAttribution) { a.UsageState = "Attested" },
		"observation id format": func(a *apiv1.ContextAttribution) { a.Usage.ObservationID = "not-hex" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := originalCanaryResult(claudeCanaryID)
			bad.Context.Metadata.Attribution = *a
			u := *a.Usage
			bad.Context.Metadata.Attribution.Usage = &u
			bad.Context.Metadata.Attribution.ObservedModels = append([]string(nil), a.ObservedModels...)
			mutate(&bad.Context.Metadata.Attribution)
			raw, _ := json.Marshal(bad)
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write(raw) }
			h.mustRun(ExitFailed, canaryResultArgs(bad)...)
		})
	}
	nullModels := strings.Replace(string(good), `"observedModels":["claude-opus-5-5"]`, `"observedModels":null`, 1)
	h = newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { _, _ = w.Write([]byte(nullModels)) }
	h.mustRun(ExitFailed, canaryResultArgs(p)...)
}
