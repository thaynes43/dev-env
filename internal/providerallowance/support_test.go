package providerallowance_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func sp(s string) *string { return &s }

func loadJSON(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type exampleDoc struct {
	Examples []struct {
		Label       string          `json:"label"`
		Note        string          `json:"note"`
		Observation json.RawMessage `json:"observation"`
	} `json:"examples"`
}

type loadedExample struct {
	Label       string
	Note        string
	Raw         []byte
	Observation providerallowance.Observation
}

func loadExamples(t *testing.T) []loadedExample {
	t.Helper()
	var doc exampleDoc
	if err := json.Unmarshal(loadJSON(t, "examples.json"), &doc); err != nil {
		t.Fatal(err)
	}
	out := make([]loadedExample, len(doc.Examples))
	for i, item := range doc.Examples {
		obs, err := providerallowance.DecodeObservation(item.Observation, providerallowance.FixtureParseLimits)
		if err != nil {
			t.Fatalf("%s: %v", item.Label, err)
		}
		out[i] = loadedExample{Label: item.Label, Note: item.Note, Raw: item.Observation, Observation: obs}
	}
	return out
}

func byID(t *testing.T, id string) loadedExample {
	t.Helper()
	for _, item := range loadExamples(t) {
		if item.Observation.ObservationID == id {
			return item
		}
	}
	t.Fatalf("missing %s", id)
	return loadedExample{}
}

func hasCode(issues []providerallowance.Issue, code string) bool {
	for _, item := range issues {
		if item.Code == code {
			return true
		}
	}
	return false
}

func trustLike(obs providerallowance.Observation, binding providerallowance.BindingState) providerallowance.TrustedContext {
	var account, workspace *string
	if obs.Scope.AccountRef != nil {
		account = sp(*obs.Scope.AccountRef)
	}
	if obs.Scope.WorkspaceRef != nil {
		workspace = sp(*obs.Scope.WorkspaceRef)
	}
	return providerallowance.TrustedContext{
		ProducerRef:         obs.ProducerRef,
		ProducerIncarnation: obs.ProducerIncarnation,
		BindingGeneration:   int64(obs.BindingGeneration),
		AccountRef:          account,
		WorkspaceRef:        workspace,
		BindingState:        binding,
		Provider:            obs.Provider,
		SourceKind:          obs.Source.Kind,
		SourceVersion:       obs.Source.Version,
	}
}

func reviewedRegistry() *providerallowance.StaticRegistry {
	min0 := "0"
	max100 := "100"
	maxMoney := "1000"
	maxBytes := "1000000000000"
	usd := "USD"
	return providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: []providerallowance.SourceRule{
			{
				Provider: "openai", Kind: providerallowance.SourceNativeRPC, Version: "CLI-and-daemon0.160.1",
				CapabilityRef: "story188:codex-native-field-schema", AllowNormalizedMeasures: true, AllowDerivations: true,
				DirectFields: map[string]providerallowance.DirectFieldRule{
					"ordinaryUsageAllowed":        {OriginalType: providerallowance.TypeBoolean, Class: providerallowance.ClassSignal},
					"spendControlReached":         {OriginalType: providerallowance.TypeBoolean, Class: providerallowance.ClassSignal},
					"secondary":                   {OriginalType: providerallowance.TypeNull, Class: providerallowance.ClassDisplay},
					"resetCredits.detailsState":   {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassInventory},
					"resetCredits.availableCount": {OriginalType: providerallowance.TypeDecimal, Class: providerallowance.ClassInventory},
				},
			},
			{
				Provider: "cursor", Kind: providerallowance.SourceNativeDisplay, Version: "2026.10.22-27dcf5f",
				CapabilityRef: "story188:cursor-display-only", AllowNormalizedMeasures: false, AllowDerivations: false,
				DirectFields: map[string]providerallowance.DirectFieldRule{
					"plan":           {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"periodHeading":  {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"included":       {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"auto":           {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"api":            {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"onDemand":       {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"remainingLabel": {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"resetLabel":     {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
				},
			},
			{
				Provider: "anthropic", Kind: providerallowance.SourcePassive, Version: "2.1.296",
				CapabilityRef: "story188:no-configured-statusline-observed", AllowNormalizedMeasures: true,
			},
			{
				Provider: "synthetic-provider", Kind: providerallowance.SourceSynthetic, Version: "fixture-v1",
				CapabilityRef: "synthetic:not-live", AllowNormalizedMeasures: true, AllowDerivations: true,
			},
			{
				Provider: "openrouter", Kind: providerallowance.SourceSynthetic, Version: "fixture-v1",
				CapabilityRef: "synthetic:not-live", AllowNormalizedMeasures: true,
			},
			{
				Provider: "ollama", Kind: providerallowance.SourceSynthetic, Version: "fixture-v1",
				CapabilityRef: "synthetic:not-live", AllowNormalizedMeasures: true,
			},
			{
				Provider: "zai", Kind: providerallowance.SourceSynthetic, Version: "fixture-v1",
				CapabilityRef: "synthetic:not-live", AllowNormalizedMeasures: true,
				DirectFields: map[string]providerallowance.DirectFieldRule{
					"region": {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
				},
			},
			{
				Provider: "tibo", Kind: providerallowance.SourceAdvisory, Version: "fixture-v1",
				CapabilityRef: "synthetic:not-live",
				DirectFields: map[string]providerallowance.DirectFieldRule{
					"attribution": {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
					"reference":   {OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay},
				},
			},
		},
		Metrics: []providerallowance.MetricRule{
			{
				ValidationRef: "story188:codex0.160.1:primary-fields", Provider: "openai",
				SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "CLI-and-daemon0.160.1",
				Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
				Windows: []string{"codex-primary-window"}, Min: &min0, Max: &max100, HistoricalOnly: true,
			},
			{
				ValidationRef: "fixture:verified-field-v1", Provider: "synthetic-provider",
				SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
				Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
				Windows: []string{"five-hour-window", "seven-day-window"}, Min: &min0, Max: &max100, ComplementOf: "100",
			},
			{
				ValidationRef: "fixture:explicit-uncapped", Provider: "synthetic-provider",
				SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
				Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
				Windows: []string{"uncapped-window"}, AllowUncapped: true, Min: &min0, Max: &max100,
			},
			{
				ValidationRef: "fixture:explicit-overlimit-source", Provider: "synthetic-provider",
				SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
				Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed,
				Windows: []string{"over-limit-window"}, Min: &min0, AllowOverLimit: true,
			},
			{
				ValidationRef: "fixture:only", Provider: "openrouter",
				SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
				Category: providerallowance.CategorySpend, Unit: providerallowance.UnitMoney, Direction: providerallowance.DirectionRemaining,
				Windows: []string{"key-budget-window"}, Currency: &usd, Min: &min0, Max: &maxMoney,
			},
			{
				ValidationRef: "fixture:only", Provider: "ollama",
				SourceKind: providerallowance.SourceSynthetic, SourceVersion: "fixture-v1",
				Category: providerallowance.CategoryLocal, Unit: "bytes", Direction: providerallowance.DirectionRemaining,
				Windows: []string{"available-memory-window"}, Min: &min0, Max: &maxBytes,
			},
		},
		Accounts: []providerallowance.AccountRule{{
			AccountRef: "fixture-account", WorkspaceRef: "fixture-workspace", Generation: 0,
			ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1",
		}},
	})
}

func measureByID(obs providerallowance.Observation, id string) providerallowance.Measure {
	for _, measure := range obs.Measures {
		if measure.ID == id {
			return measure
		}
	}
	panic("missing measure " + id)
}

type readDoc struct {
	Status string `json:"status"`
	HTTP   struct {
		Body json.RawMessage `json:"body"`
	} `json:"http"`
	AgentRun struct {
		Command    string          `json:"command"`
		StdoutJSON json.RawMessage `json:"stdoutJSON"`
		Exit       int             `json:"exit"`
	} `json:"agentRun"`
}

func cloneObservation(t *testing.T, obs providerallowance.Observation) providerallowance.Observation {
	t.Helper()
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	out, err := providerallowance.DecodeObservation(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func snapshot(t *testing.T, obs providerallowance.Observation) []byte {
	t.Helper()
	raw, err := json.Marshal(obs)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertUnchanged(t *testing.T, before []byte, obs providerallowance.Observation) {
	t.Helper()
	if string(snapshot(t, obs)) != string(before) {
		t.Fatal("observation bytes changed")
	}
}

type blankRegistry struct{}

func (blankRegistry) Sources() []providerallowance.SourceRule           { return nil }
func (blankRegistry) Metrics() []providerallowance.MetricRule           { return nil }
func (blankRegistry) Accounts() []providerallowance.AccountRule         { return nil }
func (blankRegistry) Entitlements() []providerallowance.EntitlementRule { return nil }
func (blankRegistry) Projections() []providerallowance.ProjectionRule   { return nil }

func loadRead(t *testing.T) (readDoc, providerallowance.ReadResponse, providerallowance.ReadResponse) {
	t.Helper()
	var doc readDoc
	if err := json.Unmarshal(loadJSON(t, "read-interface-examples.json"), &doc); err != nil {
		t.Fatal(err)
	}
	httpBody, err := providerallowance.DecodeReadResponse(doc.HTTP.Body, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	cliBody, err := providerallowance.DecodeReadResponse(doc.AgentRun.StdoutJSON, providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	return doc, httpBody, cliBody
}
