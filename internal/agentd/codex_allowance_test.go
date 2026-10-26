package agentd

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCodexAllowanceReadPreservesViewsAndUnknownFreshness(t *testing.T) {
	fetched := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	read, err := codexAllowanceRead(true, codexRateLimits{RateLimits: &codexLimit{Primary: &codexWindow{Used: number("0"), Duration: number("15"), ResetsAt: number("1")}, Secondary: nil}, RateLimitsByLimitID: map[string]codexLimit{"codex": {Primary: &codexWindow{Used: number("25"), Duration: number("60"), ResetsAt: number("1793016000")}}}}, fetched)
	if err != nil {
		t.Fatal(err)
	}
	if read.Items[0].Observation.ProviderObservedAt != nil || read.Items[0].Freshness.State != providerallowance.FreshUnknown || read.Items[0].Freshness.Basis != providerallowance.BasisCapture {
		t.Fatal("fetch was promoted to provider observation")
	}
	if len(read.Items[0].Observation.Measures) != 4 {
		t.Fatalf("measures=%d, want legacy and bucket views without addition", len(read.Items[0].Observation.Measures))
	}
	if read.Items[0].Observation.Measures[0].Value.Decimal == nil || *read.Items[0].Observation.Measures[0].Value.Decimal != "25" {
		t.Fatal("explicit zero/bucket ordering was not preserved")
	}
	if _, err := providerallowance.DecodeReadResponse(mustJSON(t, read), providerallowance.FixtureParseLimits); err != nil {
		t.Fatalf("mapped read is not existing contract: %v", err)
	}
}
func number(s string) *json.Number { n := json.Number(s); return &n }
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
