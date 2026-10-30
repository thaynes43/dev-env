package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
	"github.com/thaynes43/dev-env/web/portal/session"
)

func writeProductStates(dir string) error {
	disconnected, err := allowancePreview("not-connected")
	if err != nil {
		return err
	}
	page := portal.ProductPage{Title: "Sessions", Connected: true, Complete: true, Preview: true,
		ObservedAt: "2026-10-29T14:00:00Z", ReadCompletedAt: "2026-10-29T13:59:00Z", Filter: portal.ProductFilter{View: "usage"},
		Allowance: disconnected,
		Usage: &portal.ProductUsage{Allowance: disconnected, Scope: "Synthetic usage preview", ScopeNote: "Fixture data only.",
			Stats:   []portal.ProductStat{{Label: "Measured", Value: "Unknown", Tone: "muted"}},
			Columns: []string{"Session", "Input"}, Rows: []portal.ProductUsageRow{{ID: "s1", SessionUID: "uid-synthetic-agent", Name: "synthetic-agent", Cells: []portal.ProductUsageCell{{Label: "Input", Value: "Unknown", Unknown: true}}}}}}
	body, err := portal.RenderProduct(page)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.html"), body, 0600); err != nil {
		return err
	}
	empty := page
	empty.Usage = &portal.ProductUsage{Allowance: disconnected, Stats: []portal.ProductStat{{Label: "Measured", Value: "0 of 0"}, {Label: "Interactive", Value: "0"}, {Label: "Headless", Value: "0"}}}
	body, err = portal.RenderProduct(empty)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-empty.html"), body, 0600); err != nil {
		return err
	}
	for _, state := range []string{"measured", "stale", "seen-varies", "unknown", "not-connected"} {
		allowances, err := allowancePreview(state)
		if err != nil {
			return err
		}
		fixturePage := page
		fixturePage.Filter = portal.ProductFilter{}
		fixturePage.Allowance = allowances
		fixturePage.Groups = []portal.ProductGroup{{Title: "In progress", Rows: []portal.ProductRow{{ID: "s1", Title: "synthetic-agent", Status: portal.ProductStatus{Text: "Idle", Tone: "muted"}}}}}
		body, err := portal.RenderProduct(fixturePage)
		if err != nil {
			return err
		}
		if state != "not-connected" {
			if err := os.WriteFile(filepath.Join(dir, "sessions-allowance-"+state+".html"), body, 0600); err != nil {
				return err
			}
		}
		if state == "seen-varies" {
			continue
		}
		fixtureUsage := *page.Usage
		fixtureUsage.Allowance = allowances
		fixtureUsage.Coverage = []portal.ProductField{{Label: "Account allowance", Value: "Synthetic provider account windows; independent of token counts"}}
		fixturePage.Usage = &fixtureUsage
		fixturePage.Filter = portal.ProductFilter{View: "usage"}
		body, err = portal.RenderProduct(fixturePage)
		if err != nil {
			return err
		}
		name := "usage-allowance-" + state + ".html"
		if state == "measured" {
			name = "usage-allowance.html"
		}
		if err := os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			return err
		}
	}
	for _, fixture := range []struct {
		name, reason      string
		timedOut          bool
		page, row, status int
	}{
		{"sessions-not-connected", "not_connected", false, 0, -1, 0},
		{"sessions-credential-unavailable", "credential_unavailable", false, 0, -1, 0},
		{"sessions-unreachable", "operator_unreachable", false, 1, -1, 0},
		{"sessions-timed-out", "operator_unreachable", true, 3, -1, 0},
		{"sessions-refused", "operator_refused", false, 1, -1, 403},
		{"sessions-contract-refused", "response_contract", false, 2, 1, 0},
		{"sessions-clock-ahead", "clock_ahead", false, 2, 0, 0},
		{"sessions-cancelled", "cancelled", false, 2, -1, 0},
		{"sessions-read-not-started", "read_not_started", false, 0, -1, 0},
	} {
		failure := &portal.ProductReadFailure{ReasonCode: fixture.reason, Page: fixture.page, RowIndex: fixture.row, HTTPStatus: fixture.status, TimedOut: fixture.timedOut, ObservedAt: "2026-10-29T14:00:00Z"}
		if fixture.reason != "not_connected" {
			failure.LastSuccessAt = "2026-10-29T13:59:00Z"
		}
		page := portal.ProductPage{Title: "Sessions", Notice: "Sessions unavailable", Preview: true, ObservedAt: failure.ObservedAt, ReadFailure: failure, Filter: portal.ProductFilter{Attention: true}}
		if fixture.reason == "not_connected" {
			page.Notice = "Sessions not connected"
		}
		if fixture.timedOut {
			page.Launch = &portal.ProductLaunch{Action: portal.ProductSessionLaunchesHref, CSRF: strings.Repeat("c", 43), RequestID: strings.Repeat("r", 43), Repos: []string{"fixture-repo-one"}, Models: []string{"claude-opus-5-5"}}
		}
		body, err := portal.RenderProduct(page)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, fixture.name+".html"), body, 0600); err != nil {
			return err
		}
		if fixture.timedOut {
			page.Filter = portal.ProductFilter{View: "usage"}
			body, err = portal.RenderProduct(page)
			if err != nil {
				return err
			}
			if err = os.WriteFile(filepath.Join(dir, "usage-timed-out.html"), body, 0600); err != nil {
				return err
			}
		}
	}
	for _, state := range []string{"empty", "stale", "current"} {
		page := portal.ProductPage{Title: "Sessions", Notice: "The completed session read returned no sessions.", Preview: true, Connected: true, Complete: true, ObservedAt: "2026-10-29T14:00:00Z", ReadCompletedAt: "2026-10-29T13:59:00Z"}
		if state != "empty" {
			page.Notice = "Session freshness is " + state + "."
			page.Groups = []portal.ProductGroup{{Title: "In progress", Rows: []portal.ProductRow{{ID: "s1", Title: "synthetic-agent", Status: portal.ProductStatus{Text: state, Tone: "muted"}, Sections: []portal.ProductSection{{Title: "Observation", Fields: []portal.ProductField{{Label: "Freshness", Value: state}, {Label: "Usage", Value: "Unknown"}}}}}}}}
		}
		body, err := portal.RenderProduct(page)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, "sessions-"+state+".html"), body, 0600); err != nil {
			return err
		}
	}
	for _, fixture := range []struct {
		name, reason      string
		timedOut          bool
		page, row, status int
	}{
		{"not-connected", "not_connected", false, 0, -1, 0},
		{"credential-unavailable", "credential_unavailable", false, 0, -1, 0},
		{"unreachable", "operator_unreachable", false, 1, -1, 0},
		{"timed-out", "operator_unreachable", true, 3, -1, 0},
		{"refused", "operator_refused", false, 1, -1, 403},
		{"non-200", "operator_refused", false, 1, -1, 503},
		{"media-or-size", "response_contract", false, 1, -1, 0},
		{"contract-refused", "response_contract", false, 2, 1, 0},
		{"clock-ahead", "clock_ahead", false, 2, 0, 0},
		{"cancelled", "cancelled", false, 1, -1, 0},
		{"unavailable", "unknown", false, 0, -1, 0},
	} {
		failure := &portal.ProductReadFailure{ReasonCode: fixture.reason, Page: fixture.page, RowIndex: fixture.row, HTTPStatus: fixture.status, TimedOut: fixture.timedOut, ObservedAt: "2026-10-29T14:00:00Z"}
		if fixture.reason != "not_connected" && fixture.reason != "unknown" {
			failure.LastSuccessAt = "2026-10-29T13:59:00Z"
		}
		for _, attention := range []bool{false, true} {
			page := portal.ProductPage{Title: "Tasks", Notice: "Tasks unavailable", Preview: true, ObservedAt: failure.ObservedAt, ReadFailure: failure, Filter: portal.ProductFilter{Attention: attention}}
			if fixture.reason == "not_connected" {
				page.Notice = "Tasks not connected"
			}
			body, err := portal.RenderProduct(page)
			if err != nil {
				return err
			}
			name := "tasks-" + fixture.name
			if attention {
				name += "-attention"
			}
			if err = os.WriteFile(filepath.Join(dir, name+".html"), body, 0600); err != nil {
				return err
			}
		}
	}
	for _, state := range []string{"empty", "unknown"} {
		page := portal.ProductPage{Title: "Tasks", Notice: "The completed task read returned no work packages.", Preview: true, Connected: true, Complete: true,
			ObservedAt: "2026-10-29T14:00:00Z", ReadCompletedAt: "2026-10-29T13:59:00Z"}
		if state == "unknown" {
			page.ReadCompletedAt = ""
		}
		body, err := portal.RenderProduct(page)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, "tasks-"+state+".html"), body, 0600); err != nil {
			return err
		}
	}
	return writeAttentionStates(dir)
}

// writeAttentionStates exercises the same strict decoder and attention builder
// as the live read. Each file has exactly one synthetic session and cause.
func writeAttentionStates(dir string) error {
	now := time.Date(2026, 10, 29, 14, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	for _, f := range []struct{ name, mode, phase, state, result, rcState, url string }{
		{"waiting-linked", "remote", "Running", protocol.AgentWaiting, "", "registered", "https://claude.ai/code/session_fixture-247"},
		{"waiting-no-link", "remote", "Running", protocol.AgentWaiting, "", "registered", ""},
		{"waiting-not-reported", "remote", "Running", protocol.AgentWaiting, "", "", ""},
		{"waiting-state-not-reported", "remote", "Running", protocol.AgentWaiting, "", "not-reported", ""},
		{"waiting-unknown-state", "remote", "Running", protocol.AgentWaiting, "", "future-state", ""},
		{"waiting-login-required", "remote", "Running", protocol.AgentWaiting, "", "login-required", ""},
		{"waiting-unpaired", "remote", "Running", protocol.AgentWaiting, "", "unpaired", ""},
		{"waiting-offline", "remote", "Running", protocol.AgentWaiting, "", "offline", ""},
		{"waiting-pairing", "remote", "Running", protocol.AgentWaiting, "", "pair-code-issued", ""},
		{"waiting-errored", "remote", "Running", protocol.AgentWaiting, "", "errored", ""},
		{"waiting-starting", "remote", "Running", protocol.AgentWaiting, "", "starting", ""},
		{"waiting-local", "local", "Running", protocol.AgentWaiting, "", "", ""},
		{"task-failed", "task", "Failed", protocol.AgentExited, "Failed", "", ""},
		{"task-timed-out", "task", "Failed", protocol.AgentExited, "TimedOut", "", ""},
		{"pod-failed", "remote", "Failed", protocol.AgentIdle, "", "", ""},
		{"agent-failed", "remote", "Running", protocol.AgentFailed, "", "", ""},
		{"agent-interrupted", "remote", "Running", protocol.AgentInterrupted, "", "", ""},
	} {
		unknown := protocol.TokenObservation{State: protocol.UsageUnknown}
		a := &apiv1.PortalSessionActivity{Mode: f.mode, Provider: "claude", AgentState: f.state, TaskResult: f.result, RequestedModel: "claude-opus-5-5", LastActivity: &at,
			Usage: apiv1.PortalSessionUsage{Scope: protocol.UsageUnknown, ResultState: protocol.UsageUnknown, ServedModels: []string{}, InputTokens: unknown, OutputTokens: unknown, CacheReadInputTokens: unknown, CacheCreationInputTokens: unknown, Cost: protocol.CostObservation{State: protocol.UsageUnknown, Kind: protocol.UsageUnknown}}}
		if f.rcState != "" {
			a.RemoteControl = &apiv1.PortalRemoteControl{State: f.rcState, URL: f.url}
		}
		page := apiv1.PortalSessionPage{Limit: 50, Complete: true, Freshness: "observed", ObservedAt: now, Sessions: []apiv1.PortalSessionSummary{{Name: "synthetic-attention", UID: "uid-synthetic-attention", Repo: "dev-env", Phase: f.phase, Freshness: "observed", ObservedAt: at, Activity: a}}}
		source, err := json.Marshal(map[string]any{"version": 1, "now": now, "body": page})
		if err != nil {
			return err
		}
		body, err := session.RenderSessionsFixture(bytes.NewReader(source))
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, "attention-"+f.name+".html"), body, 0600); err != nil {
			return err
		}
	}
	return nil
}

// allowancePreview uses an explicitly synthetic document shaped like a provider
// API read. Every consumer page carries Preview and the existing fixture banner.
// No provider is contacted, and no captured account data is used.
func allowancePreview(state string) ([]portal.ProductAllowance, error) {
	text := func(v string) *string { return &v }
	doc := providerallowance.ReadResponse{Schema: providerallowance.SchemaRead, SnapshotRevision: "synthetic-allowance-305", ServedAt: "2026-10-29T14:00:00Z"}
	for _, provider := range []string{"anthropic", "openai"} {
		if state == "not-connected" {
			doc.SourceStatuses = append(doc.SourceStatuses, providerallowance.SourceStatus{Provider: provider, Availability: providerallowance.AvailabilityUnconfigured,
				Freshness:  providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: doc.ServedAt, Basis: providerallowance.BasisNone},
				Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified}, Binding: providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
				LastAttempt: providerallowance.LastAttempt{AttemptedAt: doc.ServedAt, Result: providerallowance.AttemptUnavailable}})
			continue
		}
		item := providerallowance.ReadItem{
			Observation: providerallowance.Observation{Schema: providerallowance.SchemaObservation, ObservationID: "synthetic-" + provider, ProducerRef: "synthetic-preview", ProducerIncarnation: "fixture-1", Sequence: 1,
				Provider: provider, Source: providerallowance.Source{Kind: providerallowance.SourceNativeRPC, Version: "synthetic-preview", CapabilityRef: "synthetic-only"},
				Scope: providerallowance.Scope{BindingState: providerallowance.BindingVerified}, CapturedAt: doc.ServedAt, ProviderObservedAt: text("2026-10-29T13:59:00Z"), Availability: providerallowance.AvailabilityAvailable, Completeness: providerallowance.CompletenessComplete},
			ReceivedAt: doc.ServedAt, Validation: providerallowance.ValidationRecord{State: providerallowance.ValidationSourceFields}, Binding: providerallowance.BindingRecord{State: providerallowance.BindingVerified},
			Freshness: providerallowance.Freshness{State: providerallowance.FreshFresh, AsOf: doc.ServedAt, Basis: providerallowance.BasisProvider},
		}
		for i, window := range []string{"primary", "secondary", "additional"} {
			value, direction := "62", providerallowance.DirectionRemaining
			if provider == "openai" {
				value, direction = "38.00", providerallowance.DirectionUsed
			}
			minutes := []providerallowance.Count{10080, 300, 60}[i]
			measure := providerallowance.Measure{ID: window, Category: providerallowance.CategoryIncluded, Window: providerallowance.Window{Ref: provider + "-" + window + "-window", DurationMinutes: &minutes, ResetAt: text("2026-11-02T14:00:00Z"), ResetPrecision: providerallowance.ResetInstant}, Unit: providerallowance.UnitPercent, Direction: direction, Value: providerallowance.MeasureValue{State: providerallowance.ValueKnown, Decimal: text(value)}}
			if i == 1 {
				measure.Window.ResetPrecision = providerallowance.ResetDisplayOnly
			}
			if i == 2 {
				measure.Window.DurationMinutes, measure.Window.ResetAt, measure.Window.ResetPrecision = nil, nil, providerallowance.ResetUnknown
				measure.Value = providerallowance.MeasureValue{State: providerallowance.ValueNotReported}
			}
			item.Observation.Measures = append(item.Observation.Measures, measure)
			current := providerallowance.CurrentMeasure{MeasureID: measure.ID, State: providerallowance.CurrentKnown, Value: measure.Value}
			if i == 2 {
				current.State = providerallowance.CurrentNotReported
			}
			item.CurrentMeasures = append(item.CurrentMeasures, current)
		}
		if state == "stale" {
			item.Freshness.State = providerallowance.FreshStale
		}
		if state == "unknown" {
			item.Binding.State = providerallowance.BindingMismatch
		}
		if state == "seen-varies" {
			other := item
			other.Observation.ObservationID += "-other"
			other.Observation.Sequence = 2
			other.Observation.ProviderObservedAt = text("2026-10-29T13:58:00Z")
			other.Observation.Measures = item.Observation.Measures[1:]
			other.CurrentMeasures = item.CurrentMeasures[1:]
			item.Observation.Measures = item.Observation.Measures[:1]
			item.CurrentMeasures = item.CurrentMeasures[:1]
			doc.Items = append(doc.Items, item, other)
			continue
		}
		doc.Items = append(doc.Items, item)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	decoded, err := providerallowance.DecodeReadResponse(raw, providerallowance.FixtureParseLimits)
	if err != nil {
		return nil, err
	}
	return session.MapProductAllowance(decoded), nil
}
