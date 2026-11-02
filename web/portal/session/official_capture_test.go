package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestOfficialPersonalCodexReadingReachesLastReportedWithoutCurrentCapacity(t *testing.T) {
	at := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
	account := "openai-email-sha256:" + strings.Repeat("a", 64)
	duration, reset := int64(10080), int64(1794224930)
	payload := pa.CodexAccountCapture{Schema: pa.CodexRPCSchema, AccountRef: account, IdentityBasis: "provider_email_sha256", PlanType: "pro", ReceivedAt: at.Format(time.RFC3339Nano), Primary: &pa.CodexWindow{UsedPercent: 12, WindowDurationMins: &duration, ResetsAt: &reset}}
	raw, _ := json.Marshal(map[string]any{"schema": pa.CaptureBundleSchema, "captures": []any{map[string]any{"provider": "openai", "producerRef": "official-fixture", "producerIncarnation": "i-1", "sequence": 1, "capturedAt": payload.ReceivedAt, "receivedAt": payload.ReceivedAt, "payload": payload}}})
	ctx := pa.TrustedContext{Provider: "openai", ProducerRef: "official-fixture", ProducerIncarnation: "i-1", BindingState: pa.BindingVerified, AccountRef: &account, SourceKind: pa.SourceNativeRPC, SourceVersion: pa.CodexRPCSourceVersion}
	min, max := "0", "100"
	parts := pa.RegistryParts{Sources: []pa.SourceRule{{Provider: "openai", Kind: pa.SourceNativeRPC, Version: pa.CodexRPCSourceVersion, CapabilityRef: pa.CodexRPCCapability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: map[string]pa.DirectFieldRule{}}}, Accounts: []pa.AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: "official-fixture", ProducerIncarnation: "i-1"}}}
	for _, name := range []string{"primary", "secondary"} {
		parts.Metrics = append(parts.Metrics, pa.MetricRule{ValidationRef: "codex-app-server:" + name + "-used-percent", Provider: "openai", SourceKind: pa.SourceNativeRPC, SourceVersion: pa.CodexRPCSourceVersion, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionUsed, Windows: []string{"codex-" + name}, Min: &min, Max: &max, HistoricalOnly: true, ComplementOf: "100"})
	}
	reg := pa.NewStaticRegistry(parts)
	doc, err := pa.AssembleCaptureBundle(raw, pa.AggregateTrust{Contexts: []pa.TrustedContext{ctx}, Registry: reg, MaxAge: 1800}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(doc)
	reader := NewCachedAllowanceReader(body, pa.FixtureParseLimits, []pa.TrustedContext{ctx}, reg, AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true})
	for _, now := range []time.Time{at.Add(2 * time.Minute), at.Add(40 * time.Minute)} {
		cards := reader.Allowances(now)
		var found bool
		for _, card := range cards {
			if card.Provider != "Codex" {
				continue
			}
			found = true
			if len(card.Windows) != 0 || len(card.LastReported) != 1 || card.LastReported[0].Percent != "12% used" || card.LastReported[0].ReadAtExact != payload.ReceivedAt || card.LastReported[0].ProviderAtExact != "" || card.LastReported[0].Account != "Account verified" {
				t.Fatalf("reading lost or made current %+v", card)
			}
		}
		if !found {
			t.Fatal("missing Codex card")
		}
	}
	foreign := ctx
	different := "different-account"
	foreign.AccountRef = &different
	refused := NewCachedAllowanceReader(body, pa.FixtureParseLimits, []pa.TrustedContext{foreign}, reg, AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true}).Allowances(at.Add(time.Minute))
	for _, card := range refused {
		if card.Provider == "Codex" && len(card.LastReported) != 0 {
			t.Fatal("wrong-account historical reading exposed")
		}
	}
}

func TestCursorBillingLabelDoesNotBecomeAllowanceRenewal(t *testing.T) {
	at := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	account := "cursor-email-sha256:" + strings.Repeat("b", 64)
	used, label := 61, "Nov 6"
	payload := pa.CursorCLIUsageCapture{Schema: pa.CursorCLICaptureSchema, RendererVersion: pa.CursorCLIQualifiedVersion, CollectionStartedAt: at.Format(time.RFC3339Nano), ReceivedAt: at.Add(time.Minute).Format(time.RFC3339Nano), AccountRef: &account, Plan: "fixture-tier", IncludedPercentDisplay: &used, PercentPrecision: "rounded_integer_display", ResetMonthDay: &label, Period: "not_exposed"}
	raw, _ := json.Marshal(map[string]any{"schema": pa.CaptureBundleSchema, "captures": []any{map[string]any{"provider": "cursor", "producerRef": "cli-fixture", "producerIncarnation": "i-1", "sequence": 1, "capturedAt": payload.CollectionStartedAt, "receivedAt": payload.ReceivedAt, "payload": payload}}})
	ctx := pa.TrustedContext{Provider: "cursor", ProducerRef: "cli-fixture", ProducerIncarnation: "i-1", BindingState: pa.BindingVerified, AccountRef: &account, SourceKind: pa.SourceNativeDisplay, SourceVersion: pa.CursorCLISourceVersion}
	fields := map[string]pa.DirectFieldRule{}
	for _, name := range []string{"plan", "accountLabel", "billingResetDisplayText", "rendererVersion", "percentPrecision", "captureTimeBasis", "period", "includedPercentDisplay"} {
		fields[name] = pa.DirectFieldRule{OriginalType: pa.TypeString, Class: pa.ClassDisplay}
	}
	min, max := "0", "100"
	reg := pa.NewStaticRegistry(pa.RegistryParts{Sources: []pa.SourceRule{{Provider: "cursor", Kind: pa.SourceNativeDisplay, Version: pa.CursorCLISourceVersion, CapabilityRef: pa.CursorCLICapability, AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: fields}}, Accounts: []pa.AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: "cli-fixture", ProducerIncarnation: "i-1"}}, Metrics: []pa.MetricRule{{ValidationRef: pa.CursorDisplayIncludedValidation, Provider: "cursor", SourceKind: pa.SourceNativeDisplay, SourceVersion: pa.CursorCLISourceVersion, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionUsed, Windows: []string{pa.CursorDisplayIncludedWindow}, Min: &min, Max: &max, ComplementOf: "100", HistoricalOnly: true}}})
	doc, err := pa.AssembleCaptureBundle(raw, pa.AggregateTrust{Contexts: []pa.TrustedContext{ctx}, Registry: reg, MaxAge: 1800}, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(doc)
	cards := NewCachedAllowanceReader(body, pa.FixtureParseLimits, []pa.TrustedContext{ctx}, reg, AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true}).Allowances(at.Add(3 * time.Minute))
	found := false
	for _, card := range cards {
		if card.Provider != "Cursor" {
			continue
		}
		found = true
		if len(card.Windows) != 0 || len(card.LastReported) != 1 || card.LastReported[0].Percent != "61% used" || card.LastReported[0].Reset != "Reset time not reported." || card.LastReported[0].ResetAtExact != "" {
			t.Fatal("billing end became quota renewal", card)
		}
	}
	if !found {
		t.Fatal("missing Cursor reported reading")
	}
}
