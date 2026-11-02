package providerallowance

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAggregateTrustRefusesPartialAndEcho(t *testing.T) {
	canary := "SECRET-TRUST-CANARY"
	for _, raw := range []string{
		"",
		"{}",
		`{"maxAgeSeconds":1800}`,
		`{"maxAgeSeconds":0,"contexts":[],"registry":{"sources":[],"metrics":[],"accounts":[],"entitlements":[],"projections":[]}}`,
		`{"maxAgeSeconds":1801,"contexts":[],"registry":{"sources":[],"metrics":[],"accounts":[],"entitlements":[],"projections":[]}}`,
		`{"maxAgeSeconds":1800,"contexts":[],"registry":{"sources":[],"metrics":[],"accounts":[],"entitlements":[],"projections":[]},"note":"` + canary + `"}`,
		`{"maxAgeSeconds":1800,"contexts":[{"provider":"` + canary + `"}],"registry":{"sources":[],"metrics":[],"accounts":[],"entitlements":[],"projections":[]}}`,
	} {
		_, err := ParseAggregateTrust([]byte(raw))
		if !errors.Is(err, ErrAggregateTrust) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "maxAge") {
			t.Fatalf("raw refused as %v", err)
		}
	}
	trust, err := ParseAggregateTrust([]byte(aggregateTrustFixture()))
	if err != nil || trust.MaxAge != 1800 || trust.Registry == nil || len(trust.Contexts) != 1 || trust.Contexts[0].Provider != "openai" {
		t.Fatalf("fixture %v %+v", err, trust)
	}
	if trust.Contexts[0].AccountRef == nil || *trust.Contexts[0].AccountRef != "fixture-account" {
		t.Fatal("account was not caller owned")
	}
}

func TestParseAggregateTrustRejectsDuplicateFieldsAndRules(t *testing.T) {
	canary := "SECRET-FIELD-CANARY"
	registryCanary := "SECRET-REGISTRY-CANARY"
	fieldDup := strings.Replace(aggregateTrustFixture(), `"directFields":{}`, `"directFields":{"`+canary+`":{"originalType":"decimal","class":"inventory"},"`+canary+`":{"originalType":"string","class":"display"}}`, 1)
	escapedKey := `\u0053ECRET-FIELD-CANARY`
	escaped := strings.Replace(aggregateTrustFixture(), `"directFields":{}`, `"directFields":{"`+canary+`":{"originalType":"decimal","class":"inventory"},"`+escapedKey+`":{"originalType":"string","class":"display"}}`, 1)
	source := `{"provider":"openai","kind":"native_rpc","version":"` + registryCanary + `","capabilityRef":"synthetic:not-live","allowNormalizedMeasures":true,"allowDerivations":true,"directFields":{}}`
	registryDup := strings.Replace(aggregateTrustFixture(), `"sources":[{"provider":"openai","kind":"native_rpc","version":"fixture-v1","capabilityRef":"synthetic:not-live","allowNormalizedMeasures":true,"allowDerivations":true,"directFields":{}}]`, `"sources":[`+source+`,`+source+`]`, 1)
	projection := `{"id":"` + registryCanary + `","provider":"openai","sourceVersion":"fixture-v1","measureIds":["window"]}`
	projectionDup := strings.Replace(aggregateTrustFixture(), `"projections":[]`, `"projections":[`+projection+`,`+projection+`]`, 1)
	for _, raw := range []string{fieldDup, escaped, registryDup, projectionDup} {
		_, err := ParseAggregateTrust([]byte(raw))
		if !errors.Is(err, ErrAggregateTrust) || strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), registryCanary) || strings.Contains(err.Error(), "directFields") {
			t.Fatalf("duplicate refused as %v", err)
		}
	}
	unique := strings.Replace(aggregateTrustFixture(), `"directFields":{}`, `"directFields":{"credits":{"originalType":"decimal","class":"inventory"}}`, 1)
	trust, err := ParseAggregateTrust([]byte(unique))
	if err != nil || trust.Registry == nil {
		t.Fatal(err)
	}
	sources := trust.Registry.Sources()
	if len(sources) != 1 || sources[0].DirectFields["credits"].Class != ClassInventory || sources[0].DirectFields["credits"].OriginalType != TypeDecimal {
		t.Fatalf("unique field %+v", sources)
	}
	unknown := strings.Replace(aggregateTrustFixture(), `"bindingState":"verified"`, `"bindingState":"unknown"`, 1)
	held, err := ParseAggregateTrust([]byte(unknown))
	if err != nil || len(held.Contexts) != 1 || held.Contexts[0].BindingState != BindingUnknown {
		t.Fatalf("unknown binding promoted %v %+v", err, held.Contexts)
	}
}

func aggregateTrustFixture() string {
	return `{
		"maxAgeSeconds":1800,
		"contexts":[{"provider":"openai","producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1","bindingGeneration":0,"accountRef":"fixture-account","workspaceRef":"fixture-workspace","bindingState":"verified","sourceKind":"native_rpc","sourceVersion":"fixture-v1"}],
		"registry":{
			"sources":[{"provider":"openai","kind":"native_rpc","version":"fixture-v1","capabilityRef":"synthetic:not-live","allowNormalizedMeasures":true,"allowDerivations":true,"directFields":{}}],
			"metrics":[{"validationRef":"fixture:verified-field-v1","provider":"openai","sourceKind":"native_rpc","sourceVersion":"fixture-v1","category":"included_quota","unit":"percent","direction":"used","windows":["five-hour-window"],"currency":null,"min":"0","max":"100","allowOverLimit":false,"allowUncapped":false,"complementOf":"","historicalOnly":false,"modelScope":""}],
			"accounts":[{"accountRef":"fixture-account","workspaceRef":"fixture-workspace","generation":0,"producerRef":"fixture-producer","producerIncarnation":"fixture-incarnation-1"}],
			"entitlements":[],
			"projections":[]
		}
	}`
}
