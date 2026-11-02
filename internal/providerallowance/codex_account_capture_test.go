package providerallowance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func personalCaptureFixture(t *testing.T) ([]byte, AggregateTrust, time.Time) {
	t.Helper()
	at := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
	account := "openai-email-sha256:" + strings.Repeat("a", 64)
	used, duration, reset := 12, int64(10080), int64(1794224930)
	report := CodexAccountCapture{Schema: CodexRPCSchema, AccountRef: account, IdentityBasis: "provider_email_sha256", PlanType: "pro", ReceivedAt: at.Format(time.RFC3339Nano), Primary: &CodexWindow{UsedPercent: used, WindowDurationMins: &duration, ResetsAt: &reset}}
	payload, _ := json.Marshal(report)
	entry := captureEntry{Provider: "openai", ProducerRef: "own-account", ProducerIncarnation: "own-1", Sequence: 1, CapturedAt: report.ReceivedAt, ReceivedAt: report.ReceivedAt, Payload: payload}
	bundle, _ := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": []captureEntry{entry}})
	ctx := TrustedContext{Provider: "openai", ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation, SourceKind: SourceNativeRPC, SourceVersion: CodexRPCSourceVersion, AccountRef: &account, BindingState: BindingVerified}
	min, max := "0", "100"
	registry := RegistryParts{Sources: []SourceRule{{Provider: "openai", Kind: SourceNativeRPC, Version: CodexRPCSourceVersion, CapabilityRef: CodexRPCCapability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: map[string]DirectFieldRule{}}}, Accounts: []AccountRule{{AccountRef: account, WorkspaceNotApplicable: true, ProducerRef: entry.ProducerRef, ProducerIncarnation: entry.ProducerIncarnation}}}
	for _, name := range []string{"primary", "secondary"} {
		registry.Metrics = append(registry.Metrics, MetricRule{ValidationRef: "codex-app-server:" + name + "-used-percent", Provider: "openai", SourceKind: SourceNativeRPC, SourceVersion: CodexRPCSourceVersion, Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{"codex-" + name}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true})
	}
	return bundle, AggregateTrust{Contexts: []TrustedContext{ctx}, Registry: NewStaticRegistry(registry), MaxAge: 1800}, at
}

func TestOfficialAccountCapturePersonalBindingPreservesCheckedReading(t *testing.T) {
	raw, trust, at := personalCaptureFixture(t)
	doc, err := AssembleCaptureBundle(raw, trust, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	item := doc.Items[0]
	if item.CollectionFreshness == nil || item.CollectionFreshness.State != FreshFresh || item.CollectionFreshness.Basis != BasisCapture {
		t.Fatal("local check recency absent")
	}
	if item.Binding.State != BindingVerified || item.Binding.WorkspaceRef != nil || item.Observation.ProviderObservedAt != nil || item.Freshness.State != FreshUnknown || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatalf("scope/clock/capacity forged %+v", item)
	}
	if *item.Observation.Measures[0].Value.Decimal != "12" || *item.Observation.Derivations[0].Value.Decimal != "88" {
		t.Fatal("actual reading hidden or changed")
	}
	ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(2*time.Minute))
	item = doc.Items[0]
	if item.Binding.State != BindingVerified || item.Validation.State != ValidationSourceFields || item.ReceivedAt != at.Format(time.RFC3339Nano) || item.CurrentMeasures[0].Value.Decimal != nil || *item.Observation.Measures[0].Value.Decimal != "12" {
		t.Fatal("cached personal historical reading lost or promoted")
	}
	later := doc
	ProjectCachedRead(&later, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(40*time.Minute))
	if later.Items[0].CollectionFreshness == nil || later.Items[0].CollectionFreshness.State != FreshStale {
		t.Fatal("collection recency did not stale")
	}
	if later.Items[0].Observation.CapturedAt != item.Observation.CapturedAt || later.Items[0].Observation.ProviderObservedAt != nil || later.Items[0].CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("serving clock fabricated provider refresh")
	}
}
func TestPersonalBindingDoesNotBypassWorkspaceOrSourceChecks(t *testing.T) {
	raw, trust, at := personalCaptureFixture(t)
	doc, err := AssembleCaptureBundle(raw, trust, at)
	if err != nil {
		t.Fatal(err)
	}
	obs := doc.Items[0].Observation
	for _, variant := range []string{"no-personal-rule", "foreign-account", "foreign-workspace", "wrong-source", "wrong-provider", "fake-workspace-rule", "duplicate-rule"} {
		t.Run(variant, func(t *testing.T) {
			o := obs
			ctx := trust.Contexts[0]
			parts := snapshotRegistry(trust.Registry)
			switch variant {
			case "no-personal-rule":
				parts.Accounts[0].WorkspaceNotApplicable = false
			case "foreign-account":
				ctx.AccountRef = stringRef("different-account")
			case "foreign-workspace":
				ctx.WorkspaceRef = stringRef("workspace-not-in-provider")
			case "wrong-source":
				o.Source.Version = CodexCaptureSourceVersion
				ctx.SourceVersion = o.Source.Version
			case "wrong-provider":
				o.Provider = "cursor"
				ctx.Provider = "cursor"
			case "fake-workspace-rule":
				parts.Accounts[0].WorkspaceRef = "invented-workspace"
			case "duplicate-rule":
				parts.Accounts = append(parts.Accounts, parts.Accounts[0])
			}
			assessed := ValidateObservation(o, ctx, NewStaticRegistry(parts))
			if len(assessed.Issues) == 0 {
				t.Fatal("unqualified personal binding trusted")
			}
		})
	}
	// Unconfigured trust must not self-authenticate a provider's hash.
	unbound, err := AssembleCaptureBundle(raw, AggregateTrust{Registry: NewStaticRegistry(RegistryParts{}), MaxAge: 1800}, at)
	if err != nil || unbound.Items[0].Binding.State == BindingVerified {
		t.Fatal("source created its own account trust", err)
	}
}
func TestPersonalTrustConfigRequiresExplicitNullableScope(t *testing.T) {
	fixture := aggregateTrustFixture()
	fixture = strings.ReplaceAll(fixture, "fixture-v1", CodexRPCSourceVersion)
	fixture = strings.ReplaceAll(fixture, `"workspaceRef":"fixture-workspace"`, `"workspaceRef":null`)
	// Only the independently supplied account registry gets this explicit fact.
	fixture = strings.Replace(fixture, `"accounts":[{"accountRef":"fixture-account","workspaceRef":null,`, `"accounts":[{"accountRef":"fixture-account","workspaceRef":null,"workspaceNotApplicable":true,`, 1)
	if _, err := ParseAggregateTrust([]byte(fixture)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(fixture, `"workspaceNotApplicable":true,`, "", 1), strings.Replace(fixture, `"workspaceNotApplicable":true`, `"workspaceNotApplicable":false`, 1), strings.Replace(fixture, `"workspaceNotApplicable":true`, `"workspaceNotApplicable":null`, 1), strings.Replace(fixture, `"workspaceNotApplicable":true`, `"workspaceNotApplicable":true,"workspaceNotApplicable":true`, 1), strings.ReplaceAll(fixture, CodexRPCSourceVersion, "unqualified-source")} {
		if _, err := ParseAggregateTrust([]byte(bad)); err == nil {
			t.Fatal("missing/unsupported personal scope accepted")
		}
	}
}
