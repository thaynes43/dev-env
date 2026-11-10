package session

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// A session-v1 reading (DESIGN-059) is shown by the portal's own projection
// as a last-reported passive value whose account is not verified. The values
// are synthetic.
func TestSessionReadingShownAsLastReported(t *testing.T) {
	at := time.Date(2026, 11, 9, 23, 42, 10, 0, time.UTC)
	item, status, err := providerallowance.SessionReadingItem("anthropic", at, []providerallowance.SessionWindow{
		{Minutes: 10080, UsedPercent: "34", ResetAt: at.Add(60 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	unconfigured := func(provider string) providerallowance.SourceStatus {
		code, at := "source_unconfigured", "1970-01-01T00:00:00Z"
		return providerallowance.SourceStatus{Provider: provider, ProducerRef: "aggregate-unconfigured", Availability: providerallowance.AvailabilityUnconfigured,
			LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: providerallowance.AttemptUnknown, Code: &code},
			Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "unconfigured"},
			Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
			Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown}}
	}
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "r", ServedAt: at.Format(time.RFC3339Nano),
		Items: []providerallowance.ReadItem{item}, SourceStatuses: []providerallowance.SourceStatus{unconfigured("cursor"), status, unconfigured("openai")}}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	// The portal's private context for Claude names another source; it must
	// neither hide nor trust the session reading.
	account := "acct-private"
	contexts := []providerallowance.TrustedContext{{ProducerRef: "collector", ProducerIncarnation: "i", BindingGeneration: 1, AccountRef: &account, BindingState: providerallowance.BindingVerified, Provider: "anthropic", SourceKind: providerallowance.SourceNativeRPC, SourceVersion: providerallowance.ClaudeSDKSourceVersion}}
	reader := NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, contexts, nil, AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true})
	rows := reader.Allowances(at.Add(5 * time.Minute))
	var claude *portal.ProductAllowance
	for i := range rows {
		if rows[i].Provider == "Claude" {
			claude = &rows[i]
		}
	}
	if claude == nil {
		t.Fatal("no Claude row")
	}
	if claude.Value != portal.AllowanceUnknown || len(claude.Windows) != 0 {
		t.Fatalf("a session reading was shown as current: %+v", claude)
	}
	if len(claude.LastReported) != 1 {
		t.Fatalf("last reported %+v (reason %q)", claude.LastReported, claude.Reason)
	}
	line := claude.LastReported[0]
	if line.Percent != "34% used" || line.Window != "7 days" || line.Source != "Passive event" || line.Account != "Account not verified" || line.ReadAtExact != "2026-11-09T23:42:10Z" || line.ProviderAt != "" || !line.Session {
		t.Fatalf("line %+v", line)
	}
	body, err := portal.RenderProduct(portal.ProductPage{
		Title: "Sessions", Connected: true, Complete: true, Filter: portal.ProductFilter{View: "usage"},
		AsOf: at.Add(5 * time.Minute), Usage: &portal.ProductUsage{Allowance: rows},
	})
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	start := strings.Index(page, `<h3 class="glance-name">Claude</h3>`)
	end := strings.Index(page, `<h3 class="glance-name">Codex</h3>`)
	if start < 0 || end < start {
		t.Fatal("Claude tile was not rendered")
	}
	tile := page[start:end]
	for _, want := range []string{"34% of the week used", "resets Thursday 12 Nov 11:42 UTC", "reported 5 minutes ago", "Reported by a running session; not checked with the provider."} {
		if !strings.Contains(tile, want) {
			t.Errorf("mapped session tile lacks %q:\n%s", want, tile)
		}
	}
	if strings.Contains(tile, ">Unknown<") || strings.Contains(tile, "No current percent") {
		t.Errorf("mapped session tile kept the old words:\n%s", tile)
	}
	for _, row := range rows {
		if row.Provider == "Codex" && row.Value != portal.AllowanceNotConnected {
			t.Errorf("Codex row %+v", row)
		}
	}
}

// A session-v1 item the operator did not merge is served labelled rejected
// (providerallowance.RejectSessionReadings). The portal cannot tell it from a
// merged one by its content, so its own projection must keep that label: the
// tile shows no value and no last-reported line. The values are synthetic.
func TestSessionReadingRejectedByTheOperatorStaysHidden(t *testing.T) {
	at := time.Date(2026, 11, 9, 23, 42, 10, 0, time.UTC)
	item, status, err := providerallowance.SessionReadingItem("anthropic", at, []providerallowance.SessionWindow{
		{Minutes: 10080, UsedPercent: "99", ResetAt: at.Add(60 * time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "r", ServedAt: at.Format(time.RFC3339Nano),
		Items: []providerallowance.ReadItem{item}, SourceStatuses: []providerallowance.SourceStatus{status}}
	// The operator's serving steps for a document it did not build.
	providerallowance.RejectSessionReadings(&doc)
	providerallowance.ProjectCachedRead(&doc, nil, nil, 30*time.Minute, true, at.Add(time.Minute))
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	reader := NewCachedAllowanceReader(raw, providerallowance.FixtureParseLimits, nil, nil, AllowanceReadPolicy{MaxAge: 30 * time.Minute, Set: true})
	for _, row := range reader.Allowances(at.Add(5 * time.Minute)) {
		if row.Provider != "Claude" {
			continue
		}
		if row.Value != portal.AllowanceUnknown || len(row.Windows) != 0 || len(row.LastReported) != 0 {
			t.Fatalf("a rejected session item was shown: %+v", row)
		}
		return
	}
	t.Fatal("no Claude row")
}
