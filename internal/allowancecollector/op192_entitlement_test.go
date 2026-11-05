package allowancecollector

import (
	"encoding/json"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
)

// OP192: the production collector config must refuse every shared-pool
// (EntitlementRule) trust, verified or not, so Cursor and Grok cannot be
// deduped before an authoritative proof standard and a reviewed config change
// exist (research R-07, D2). Synthetic input only.
func TestOP192CollectorConfigRefusesAnySharedPoolRule(t *testing.T) {
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	cfg := collectorConfig(t, now)
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Control: the same config and trust without a shared-pool rule parse, so
	// each refusal below is caused by the rule alone.
	if _, err := ParseConfig(raw, cfg.trust, hostUID, "dev-env-workbench", now); err != nil {
		t.Fatalf("control config refused: %v", err)
	}
	var cursor pa.AccountRule
	for _, account := range cfg.trust.Registry.Accounts() {
		if account.ProducerRef == "collector-cursor" {
			cursor = account
		}
	}
	if cursor.AccountRef == "" {
		t.Fatal("fixture has no cursor account")
	}
	for _, tc := range []struct {
		name     string
		verified bool
		products []string
	}{
		{name: "unverified-cursor-grok", products: []string{"cursor", "grok"}},
		{name: "verified-cursor-grok", verified: true, products: []string{"cursor", "grok"}},
		{name: "verified-cursor-xai", verified: true, products: []string{"cursor", "xai"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := make([]pa.EntitlementBinding, 0, len(tc.products))
			for _, product := range tc.products {
				bindings = append(bindings, pa.EntitlementBinding{AccountRef: cursor.AccountRef, WorkspaceRef: cursor.WorkspaceRef, Generation: cursor.Generation, ProducerRef: cursor.ProducerRef, ProducerIncarnation: cursor.ProducerIncarnation, Product: product})
			}
			trust := cfg.trust
			trust.Registry = pa.NewStaticRegistry(pa.RegistryParts{
				Sources: cfg.trust.Registry.Sources(), Metrics: cfg.trust.Registry.Metrics(), Accounts: cfg.trust.Registry.Accounts(),
				Entitlements: []pa.EntitlementRule{{EntitlementRef: "op192-shared-pool", Products: tc.products, Verified: tc.verified, Bindings: bindings}},
			})
			if _, err := ParseConfig(raw, trust, hostUID, "dev-env-workbench", now); err == nil {
				t.Fatal("collector config accepted a shared-pool rule")
			}
		})
	}
}
