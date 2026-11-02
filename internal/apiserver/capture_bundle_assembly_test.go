package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal/session"
)

func TestCaptureBundleStoreRefusesPromotedHistoricalCurrent(t *testing.T) {
	trust := captureAssemblyTrust()
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	doc, err := providerallowance.AssembleCaptureBundle(captureAssemblyBundle(t, 1), trust, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range doc.Items {
		item.Observation.ProviderObservedAt = &item.Observation.CapturedAt
		for j, measure := range item.Observation.Measures {
			if measure.Value.State == providerallowance.ValueKnown {
				item.CurrentMeasures[j] = providerallowance.CurrentMeasure{MeasureID: measure.ID, State: providerallowance.CurrentKnown, Value: measure.Value}
			}
		}
		ctx := trust.Contexts[i]
		if err := acceptCurrentItem(item, ctx, trust.Registry, now); !errors.Is(err, ErrAggregateRejected) {
			t.Fatalf("%s historical metric became current: %v", item.Observation.Provider, err)
		}
		parts := providerallowance.RegistryParts{Sources: trust.Registry.Sources(), Metrics: trust.Registry.Metrics(), Accounts: trust.Registry.Accounts(), Entitlements: trust.Registry.Entitlements(), Projections: trust.Registry.Projections()}
		for j := range parts.Metrics {
			parts.Metrics[j].HistoricalOnly = false
		}
		if err := acceptCurrentItem(item, ctx, providerallowance.NewStaticRegistry(parts), now); err != nil {
			t.Fatalf("positive source-semantic control refused %s: %v", item.Observation.Provider, err)
		}
	}
}

// Synthetic account/source rules qualify only these isolated fixtures. This
// proves the local assembly/store/projection seams, not a provider read or
// authenticated native/UI acceptance.
func TestCaptureBundleAssemblyStoresThreeHistoricalSources(t *testing.T) {
	trust := captureAssemblyTrust()
	now := time.Date(2026, 11, 2, 3, 10, 0, 0, time.UTC)
	mem := &slotMem{cm: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, ResourceVersion: "1"}, Data: map[string]string{"unrelated": "kept"}}}
	store := &AggregateAllowanceReportStore{Writer: mem, Live: mem, Namespace: AggregateAllowanceNamespace, Name: AggregateAllowanceName, DataKey: AggregateAllowanceDataKey, Contexts: trust.Contexts, Registry: trust.Registry, MaxAge: 30 * time.Minute}
	for sequence := 1; sequence <= 2; sequence++ {
		doc, err := providerallowance.AssembleCaptureBundle(captureAssemblyBundle(t, sequence), trust, now)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Put(context.Background(), raw, now); err != nil {
			t.Fatalf("capture %d rejected: %v", sequence, err)
		}
		if err := store.Put(context.Background(), raw, now.Add(time.Second)); err != nil || mem.updates != sequence {
			t.Fatal("identical replay wrote or failed", err)
		}
		rows := session.NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, trust.Contexts, trust.Registry, session.AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true}).Allowances(now)
		seen := map[string]bool{}
		for _, row := range rows {
			if row.Provider != "Cursor" && row.Provider != "Claude" && row.Provider != "Codex" {
				continue
			}
			seen[row.Provider] = true
			if len(row.LastReported) == 0 {
				t.Fatalf("%s lost its historical values: %+v", row.Provider, row)
			}
			for _, w := range row.Windows {
				if w.PercentKnown {
					t.Fatal("missing provider time became a live meter")
				}
			}
		}
		if len(seen) != 3 || len(doc.Items) != 3 || mem.cm.Data["unrelated"] != "kept" {
			t.Fatal("aggregate coverage or unrelated data lost")
		}
		for _, item := range doc.Items {
			if item.Observation.Sequence != providerallowance.Count(sequence) || item.Observation.ProviderObservedAt != nil || item.Freshness.State != providerallowance.FreshUnknown {
				t.Fatal("capture sequence/time changed")
			}
		}
	}
}

func captureAssemblyBundle(t *testing.T, sequence int) []byte {
	t.Helper()
	at := "2026-11-02T03:07:00Z"
	received := "2026-11-02T03:07:10Z"
	if sequence == 2 {
		at, received = "2026-11-02T03:08:00Z", "2026-11-02T03:08:10Z"
	}
	cursor := map[string]any{"schema": providerallowance.CursorDisplayInputSchema, "capturedAt": at, "route": "cursor_plan_usage", "product": "grok", "plan": "fixture-tier", "accountRef": "cursor-account", "availability": "partial", "errorCode": nil, "windowLabel": "weekly", "durationMinutes": nil, "usedPercent": "12.5", "resetDisplayDate": nil, "resetDisplayText": "December 1", "spendAmount": nil, "spendCurrency": nil}
	claude := map[string]any{"rate_limits": map[string]any{"five_hour": nil, "seven_day": map[string]any{"used_percentage": 22.5, "resets_at": nil}}}
	codex := providerallowance.CurrentSourceReport{Schema: providerallowance.CurrentSourceSchema, PublisherRef: providerallowance.CurrentSourcePublisher, ProducerIncarnation: "fixture-native-codex", SourceGeneration: 1, RetrievedAt: at, UsedPercent: "6.25", RemainingPercent: "93.75", WindowDurationMinutes: 10080, ResetsAt: "2026-11-09T03:07:00Z", RemainingPercentBasis: providerallowance.CurrentSourceRemainingBasis}
	var entries []any
	for _, input := range []struct {
		provider string
		payload  any
	}{{"cursor", cursor}, {"anthropic", claude}, {"openai", codex}} {
		entries = append(entries, map[string]any{"provider": input.provider, "producerRef": input.provider + "-capture", "producerIncarnation": "capture-fixture-1", "sequence": sequence, "capturedAt": at, "receivedAt": received, "payload": input.payload})
	}
	raw, err := json.Marshal(map[string]any{"schema": providerallowance.CaptureBundleSchema, "captures": entries})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func captureAssemblyTrust() providerallowance.AggregateTrust {
	var contexts []providerallowance.TrustedContext
	parts := providerallowance.RegistryParts{}
	min, max := "0", "100"
	for _, source := range []struct {
		provider            string
		kind                providerallowance.SourceKind
		version, capability string
		windows             []string
		refs                []string
	}{
		{"cursor", providerallowance.SourceNativeDisplay, providerallowance.CursorDisplaySourceVersion, providerallowance.CursorDisplayCapability, []string{providerallowance.CursorDisplayIncludedWindow}, []string{providerallowance.CursorDisplayIncludedValidation}},
		{"anthropic", providerallowance.SourcePassive, providerallowance.ClaudeCaptureSourceVersion, "claude-statusline:documented-windows", []string{"five_hour", "seven_day"}, []string{providerallowance.ClaudeCaptureValidationPrefix + "five_hour-used-percent", providerallowance.ClaudeCaptureValidationPrefix + "seven_day-used-percent"}},
		{"openai", providerallowance.SourceNativeRPC, providerallowance.CodexCaptureSourceVersion, providerallowance.CodexCaptureCapability, []string{"codex-primary"}, []string{providerallowance.CodexCaptureValidation}},
	} {
		account, workspace := source.provider+"-account", source.provider+"-workspace"
		contexts = append(contexts, providerallowance.TrustedContext{Provider: source.provider, ProducerRef: source.provider + "-capture", ProducerIncarnation: "capture-fixture-1", AccountRef: &account, WorkspaceRef: &workspace, BindingState: providerallowance.BindingVerified, SourceKind: source.kind, SourceVersion: source.version})
		direct := map[string]providerallowance.DirectFieldRule{}
		if source.provider == "cursor" {
			for _, field := range []string{"plan", "windowLabel", "resetDisplayText"} {
				direct[field] = providerallowance.DirectFieldRule{OriginalType: providerallowance.TypeString, Class: providerallowance.ClassDisplay}
			}
		}
		parts.Sources = append(parts.Sources, providerallowance.SourceRule{Provider: source.provider, Kind: source.kind, Version: source.version, CapabilityRef: source.capability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: direct})
		for i, window := range source.windows {
			parts.Metrics = append(parts.Metrics, providerallowance.MetricRule{ValidationRef: source.refs[i], Provider: source.provider, SourceKind: source.kind, SourceVersion: source.version, Category: providerallowance.CategoryIncluded, Unit: providerallowance.UnitPercent, Direction: providerallowance.DirectionUsed, Windows: []string{window}, Min: &min, Max: &max, HistoricalOnly: true, ComplementOf: "100"})
		}
		parts.Accounts = append(parts.Accounts, providerallowance.AccountRule{AccountRef: account, WorkspaceRef: workspace, ProducerRef: source.provider + "-capture", ProducerIncarnation: "capture-fixture-1"})
	}
	return providerallowance.AggregateTrust{MaxAge: 1800, Contexts: contexts, Registry: providerallowance.NewStaticRegistry(parts)}
}
