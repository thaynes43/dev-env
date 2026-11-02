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
