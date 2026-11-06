package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	pa "github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// TestOfficialClaudeSDKReadingReachesLastReportedWithoutCurrentCapacity is the
// Claude analog of the Codex test: a retained SDK usage capture with no
// provider observation time (freshness unknown, "provider time missing") must
// surface only as a last-reported reading, never as current capacity.
func TestOfficialClaudeSDKReadingReachesLastReportedWithoutCurrentCapacity(t *testing.T) {
	at := time.Date(2026, 11, 2, 18, 2, 10, 0, time.UTC)
	account := "anthropic-org-sha256:" + strings.Repeat("c", 64)
	received := at.Format(time.RFC3339Nano)
	reset := "2026-11-09T18:00:00Z"
	payload := pa.ClaudeUsageCapture{Schema: pa.ClaudeSDKCaptureSchema, ReceivedAt: received, ProviderObservedAt: nil, Limits: []pa.ClaudeUsageLimit{{Kind: "weekly_all", Group: "weekly", Percent: json.Number("31"), ResetsAt: &reset, Scope: nil, Severity: "normal", IsActive: true}}}
	raw, _ := json.Marshal(map[string]any{"schema": pa.CaptureBundleSchema, "captures": []any{map[string]any{"provider": "anthropic", "producerRef": "official-fixture", "producerIncarnation": "i-1", "sequence": 1, "capturedAt": received, "receivedAt": received, "payload": payload}}})
	ctx := pa.TrustedContext{Provider: "anthropic", ProducerRef: "official-fixture", ProducerIncarnation: "i-1", BindingState: pa.BindingVerified, AccountRef: &account, SourceKind: pa.SourceNativeRPC, SourceVersion: pa.ClaudeSDKSourceVersion}
	fields := map[string]pa.DirectFieldRule{}
	for _, name := range []string{"kind", "group", "severity"} {
		fields["claude-sdk-0-"+name] = pa.DirectFieldRule{OriginalType: pa.TypeString, Class: pa.ClassDisplay}
	}
	fields["claude-sdk-0-active"] = pa.DirectFieldRule{OriginalType: pa.TypeBoolean, Class: pa.ClassDisplay}
	min, max := "0", "100"
	parts := pa.RegistryParts{
		Sources:  []pa.SourceRule{{Provider: "anthropic", Kind: pa.SourceNativeRPC, Version: pa.ClaudeSDKSourceVersion, CapabilityRef: "claude-sdk:usage-report", AllowNormalizedMeasures: true, AllowDerivations: true, DirectFields: fields}},
		Accounts: []pa.AccountRule{{AccountRef: account, AccountScopeOnly: true, ProducerRef: "official-fixture", ProducerIncarnation: "i-1"}},
		Metrics:  []pa.MetricRule{{ValidationRef: "claude-sdk:claude-sdk-0-used-percent", Provider: "anthropic", SourceKind: pa.SourceNativeRPC, SourceVersion: pa.ClaudeSDKSourceVersion, Category: pa.CategoryIncluded, Unit: pa.UnitPercent, Direction: pa.DirectionUsed, Windows: []string{"claude-sdk-0-weekly_all"}, Min: &min, Max: &max, HistoricalOnly: true, ComplementOf: "100"}},
	}
	reg := pa.NewStaticRegistry(parts)
	doc, err := pa.AssembleCaptureBundle(raw, pa.AggregateTrust{Contexts: []pa.TrustedContext{ctx}, Registry: reg, MaxAge: 1800}, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(doc)
	policy := AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true}
	reader := NewCachedAllowanceReader(body, pa.FixtureParseLimits, []pa.TrustedContext{ctx}, reg, policy)
	for _, now := range []time.Time{at.Add(2 * time.Minute), at.Add(40 * time.Minute)} {
		var found bool
		for _, card := range reader.Allowances(now) {
			if card.Provider != "Claude" {
				continue
			}
			found = true
			if len(card.Windows) != 0 || len(card.LastReported) != 1 || card.Value != portal.AllowanceUnknown {
				t.Fatalf("reading lost or made current at %s: %+v", now.Format(time.RFC3339), card)
			}
			got := card.LastReported[0]
			if got.Percent != "31% used" || got.ReadAtExact != received || got.ProviderAtExact != "" {
				t.Fatalf("last-reported row wrong at %s: %+v", now.Format(time.RFC3339), got)
			}
		}
		if !found {
			t.Fatal("missing Claude card")
		}
	}
	foreign := ctx
	different := "anthropic-org-sha256:" + strings.Repeat("d", 64)
	foreign.AccountRef = &different
	refused := NewCachedAllowanceReader(body, pa.FixtureParseLimits, []pa.TrustedContext{foreign}, reg, policy).Allowances(at.Add(time.Minute))
	for _, card := range refused {
		if card.Provider == "Claude" && len(card.LastReported) != 0 {
			t.Fatal("wrong-account historical reading exposed")
		}
	}
}
