package providerallowance

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func cursorCLIFixture(t *testing.T, used *int) ([]byte, AggregateTrust, time.Time) {
	t.Helper()
	start := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	received := start.Add(30 * time.Second)
	account := "cursor-email-sha256:" + strings.Repeat("b", 64)
	reset := "Nov 6"
	report := CursorCLIUsageCapture{Schema: CursorCLICaptureSchema, RendererVersion: CursorCLIQualifiedVersion, CollectionStartedAt: start.Format(time.RFC3339Nano), ReceivedAt: received.Format(time.RFC3339Nano), AccountRef: &account, Plan: "fixture-tier", IncludedPercentDisplay: used, PercentPrecision: "rounded_integer_display", ResetMonthDay: &reset, Period: "not_exposed"}
	payload, err := MarshalCursorCLIUsage(report)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"schema": CaptureBundleSchema, "captures": []captureEntry{{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", Sequence: 1, CapturedAt: report.CollectionStartedAt, ReceivedAt: report.ReceivedAt, Payload: payload}}})
	ctx := TrustedContext{Provider: "cursor", ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1", AccountRef: &account, BindingState: BindingVerified, SourceKind: SourceNativeDisplay, SourceVersion: CursorCLISourceVersion}
	fields := map[string]DirectFieldRule{}
	for _, name := range []string{"plan", "accountLabel", "billingResetDisplayText", "rendererVersion", "percentPrecision", "captureTimeBasis", "period", "includedPercentDisplay"} {
		fields[name] = DirectFieldRule{OriginalType: TypeString, Class: ClassDisplay}
	}
	min, max := "0", "100"
	parts := RegistryParts{Sources: []SourceRule{{Provider: "cursor", Kind: SourceNativeDisplay, Version: CursorCLISourceVersion, CapabilityRef: CursorCLICapability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: fields}}, Metrics: []MetricRule{{ValidationRef: CursorDisplayIncludedValidation, Provider: "cursor", SourceKind: SourceNativeDisplay, SourceVersion: CursorCLISourceVersion, Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Windows: []string{CursorDisplayIncludedWindow}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true}}, Accounts: []AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: "normal-cli", ProducerIncarnation: "own-ui-1"}}}
	return raw, AggregateTrust{Contexts: []TrustedContext{ctx}, Registry: NewStaticRegistry(parts), MaxAge: 1800}, received
}
func TestCursorNormalCLIAccountScopeKeepsRoundedDisplayAndUnknownPeriod(t *testing.T) {
	used := 61
	raw, trust, at := cursorCLIFixture(t, &used)
	doc, err := AssembleCaptureBundle(raw, trust, at)
	if err != nil {
		t.Fatal(err)
	}
	item := doc.Items[0]
	obs := item.Observation
	m := obs.Measures[0]
	if item.Binding.State != BindingVerified || item.Binding.WorkspaceRef != nil || obs.ProviderObservedAt != nil || m.Window.ResetAt != nil || m.Window.DurationMinutes != nil || m.Window.ResetDisplayText != nil || m.Window.ResetPrecision != ResetUnknown || obs.Scope.EntitlementRef != nil || len(obs.Scope.Products) != 1 || obs.Scope.Products[0] != "cursor" {
		t.Fatal("invented source scope/time/period/shared pool")
	}
	if cursorDisplayLexical(obs, "billingResetDisplayText") != "Nov 6" {
		t.Fatal("original billing label lost")
	}
	if obs.Source.Version != CursorCLISourceVersion || *m.Value.Decimal != "61" || *obs.Derivations[0].Value.Decimal != "39" || item.CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("display lost or promoted")
	}
	if obs.CapturedAt != at.Add(-30*time.Second).Format(time.RFC3339Nano) || item.ReceivedAt != at.Format(time.RFC3339Nano) {
		t.Fatal("acquisition interval collapsed")
	}
	ProjectCachedRead(&doc, trust.Contexts, trust.Registry, 30*time.Minute, true, at.Add(time.Minute))
	if doc.Items[0].Validation.State != ValidationSourceFields || doc.Items[0].Binding.State != BindingVerified || doc.Items[0].CurrentMeasures[0].Value.Decimal != nil {
		t.Fatal("caller-qualified reported display lost or made capacity")
	}
	parts := snapshotRegistry(trust.Registry)
	parts.Accounts[0].AccountScopeOnly = false
	assessment := ValidateObservation(obs, trust.Contexts[0], NewStaticRegistry(parts))
	if len(assessment.Issues) == 0 {
		t.Fatal("missing explicit account-scope qualification accepted")
	}
	foreign := trust.Contexts[0]
	foreign.AccountRef = stringRef("different-account")
	if len(ValidateObservation(obs, foreign, trust.Registry).Issues) == 0 {
		t.Fatal("foreign account accepted")
	}
	unknownWorkspace := trust.Contexts[0]
	unknownWorkspace.WorkspaceRef = stringRef("invented-workspace")
	if len(ValidateObservation(obs, unknownWorkspace, trust.Registry).Issues) == 0 {
		t.Fatal("invented workspace accepted")
	}
}
func TestCursorNormalCLIZeroMayBeFallbackAndNeverBecomesKnownQuota(t *testing.T) {
	zero := 0
	for _, value := range []*int{nil, &zero} {
		raw, trust, at := cursorCLIFixture(t, value)
		doc, err := AssembleCaptureBundle(raw, trust, at)
		if err != nil {
			t.Fatal(err)
		}
		item := doc.Items[0]
		if item.Observation.Measures[0].Value.State != ValueUnknown || len(item.Observation.Derivations) != 0 || item.CurrentMeasures[0].Value.Decimal != nil {
			t.Fatal("UI fallback zero became quota")
		}
	}
}
func TestAccountScopeQualificationDoesNotAuthorizeOtherSourceFamilies(t *testing.T) {
	used := 61
	raw, trust, at := cursorCLIFixture(t, &used)
	doc, err := AssembleCaptureBundle(raw, trust, at)
	if err != nil {
		t.Fatal(err)
	}
	obs := doc.Items[0].Observation
	ctx := trust.Contexts[0]
	for _, variant := range []string{"generic-display", "wrong-capability", "other-provider", "workspace-populated"} {
		o := obs
		c := ctx
		switch variant {
		case "generic-display":
			o.Source.Version = CursorDisplaySourceVersion
			c.SourceVersion = o.Source.Version
		case "wrong-capability":
			o.Source.CapabilityRef = CursorDisplayXAICapability
		case "other-provider":
			o.Provider = "grok"
			c.Provider = "grok"
		case "workspace-populated":
			o.Scope.WorkspaceRef = stringRef("unqualified-workspace")
			c.WorkspaceRef = o.Scope.WorkspaceRef
		}
		if len(ValidateObservation(o, c, trust.Registry).Issues) == 0 {
			t.Fatal("unsupported account scope accepted", variant)
		}
	}
}

func TestCursorCLIRefusesUnqualifiedRendererPrecisionAndClockClaims(t *testing.T) {
	used := 61
	raw, _, _ := cursorCLIFixture(t, &used)
	var bundle struct {
		Captures []captureEntry `json:"captures"`
	}
	if json.Unmarshal(raw, &bundle) != nil {
		t.Fatal("fixture")
	}
	var payload map[string]any
	if json.Unmarshal(bundle.Captures[0].Payload, &payload) != nil {
		t.Fatal("payload")
	}
	for _, tc := range []struct {
		name  string
		value any
	}{{"rendererVersion", "unqualified-new-version"}, {"percentPrecision", "raw_api_exact"}, {"period", "monthly"}, {"resetMonthDay", "2026-11-06"}, {"includedPercentDisplay", 101}, {"includedPercentDisplay", "61"}, {"providerObservedAt", "2026-11-02T19:00:00Z"}, {"collectionStartedAt", "2026-11-02T20:00:00Z"}, {"receivedAt", "2026-11-02T19:10:00Z"}, {"accountRef", "not-qualified-account"}, {"apiPercentDisplay", 0}} {
		changed := map[string]any{}
		for k, v := range payload {
			changed[k] = v
		}
		changed[tc.name] = tc.value
		body, _ := json.Marshal(changed)
		if _, err := projectCursorCLIUsage(body, CursorDisplaySelection{ProducerRef: "source", ProducerIncarnation: "i-1", Sequence: 1, BindingState: BindingUnknown, MaxAge: 30 * time.Minute}); err == nil {
			t.Fatal("false source precision accepted", tc.name)
		}
	}
}

func TestLegacyCursorDisplayCannotServeVerifiedAccountOnlyBinding(t *testing.T) {
	selection := cursorDisplaySelection()
	selection.BindingState = BindingVerified
	selection.BindingGeneration = 1
	selection.AccountRef = stringRef("cursor-email-sha256:" + strings.Repeat("a", 64))
	selection.WorkspaceRef = nil
	raw := cursorDisplayDoc(t, map[string]any{"accountRef": *selection.AccountRef})
	if _, err := CursorDisplayProject(raw, selection, cursorDisplayNow()); !errors.Is(err, ErrCursorDisplayBinding) {
		t.Fatal("legacy display served account-only verified binding", err)
	}
}
