package apiserver

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// op192TrustedNoncurrent returns a store trusting one provider and a
// non-current item for it that the store accepts as a new capture: it passes
// the caller-owned provenance and clock checks, and its freshness and current
// values are withheld. Synthetic only.
func op192TrustedNoncurrent(t *testing.T, provider string) (*AggregateAllowanceReportStore, *slotMem, providerallowance.ReadItem) {
	t.Helper()
	min, max := "0", "100"
	account, workspace := "fixture-account", "fixture-workspace"
	reg := providerallowance.NewStaticRegistry(providerallowance.RegistryParts{
		Sources: []providerallowance.SourceRule{{Provider: provider, Kind: providerallowance.SourceNativeRPC, Version: "fixture-v1", CapabilityRef: "synthetic:not-live", AllowNormalizedMeasures: true, AllowDerivations: true}},
		Metrics: []providerallowance.MetricRule{{ValidationRef: "fixture:verified-field-v1", Provider: provider, SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
			Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed, Windows: []string{"five-hour-window"}, Min: &min, Max: &max}},
		Accounts: []providerallowance.AccountRule{{AccountRef: account, WorkspaceRef: workspace, Generation: 0, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1"}},
	})
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"keep": "yes"}}}
	store := &AggregateAllowanceReportStore{
		Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey,
		Contexts: []providerallowance.TrustedContext{{
			Provider: provider, ProducerRef: "fixture-producer", ProducerIncarnation: "fixture-incarnation-1", BindingGeneration: 0,
			AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified,
			SourceKind: providerallowance.SourceNativeRPC, SourceVersion: "fixture-v1",
		}},
		Registry: reg, MaxAge: 30 * time.Minute,
	}
	obs, err := providerallowance.DecodeObservation(assemblyObservation(t, "obs-"+provider, 1, "40"), providerallowance.FixtureParseLimits)
	if err != nil {
		t.Fatal(err)
	}
	obs.Provider, obs.Scope.Products = provider, []string{provider}
	item := providerallowance.ReadItem{
		Observation: obs, ReceivedAt: "2026-11-02T03:00:01Z",
		Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
		Binding:    providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		Freshness:  providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: "2026-11-02T03:00:01Z", Basis: providerallowance.BasisNone, Reason: "freshness withheld"},
		CurrentMeasures: []providerallowance.CurrentMeasure{{MeasureID: "window", State: providerallowance.CurrentUnknown,
			Value: providerallowance.MeasureValue{State: providerallowance.ValueUnknown}, Reason: "freshness withheld"}},
	}
	return store, mem, item
}

// TestOP192RequiredProviderNeedsStatus pins the rule OP192 S1 kept when it
// removed the exact status count: each required provider must carry its own
// source status, also when it has a non-current item and a Grok row is
// present. Without the rule the item would be judged against a zero status.
func TestOP192RequiredProviderNeedsStatus(t *testing.T) {
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		for _, grok := range []bool{false, true} {
			name := provider + "/three-row"
			if grok {
				name = provider + "/four-row"
			}
			t.Run(name, func(t *testing.T) {
				store, mem, item := op192TrustedNoncurrent(t, provider)
				withStatus := op192Doc("2026-11-02T03:05:00Z", grok)
				withStatus.Items = []providerallowance.ReadItem{item}
				// Control: the same document with the provider's status is accepted.
				if err := store.Put(t.Context(), canonicalAllowanceFixture(t, withStatus), now); err != nil || mem.updates != 1 {
					t.Fatalf("control refused: %v", err)
				}
				missing := op192Doc("2026-11-02T03:06:00Z", grok)
				missing.Items = []providerallowance.ReadItem{item}
				kept := missing.SourceStatuses[:0]
				for _, status := range missing.SourceStatuses {
					if status.Provider != provider {
						kept = append(kept, status)
					}
				}
				missing.SourceStatuses = kept
				// Refused over the stored control and into an empty slot.
				refuseAllowancePut(t, store, mem, missing, now, mem.cm.Data[AggregateAllowanceDataKey])
				empty, emptyMem, _ := op192TrustedNoncurrent(t, provider)
				refuseAllowancePut(t, empty, emptyMem, missing, now, "")
			})
		}
	}
}
