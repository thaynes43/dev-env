package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/thaynes43/dev-env/web/portal"
)

func writeProductStates(dir string) error {
	page := portal.ProductPage{Title: "Sessions", Connected: true, Complete: true, Preview: true,
		ObservedAt: "2026-10-29T14:00:00Z", Filter: portal.ProductFilter{View: "usage"},
		Usage: &portal.ProductUsage{Scope: "Synthetic usage preview", ScopeNote: "Fixture data only.",
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
	empty.Usage = &portal.ProductUsage{Stats: []portal.ProductStat{{Label: "Measured", Value: "0 of 0"}, {Label: "Interactive", Value: "0"}, {Label: "Headless", Value: "0"}}}
	body, err = portal.RenderProduct(empty)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "usage-empty.html"), body, 0600); err != nil {
		return err
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
		page := portal.ProductPage{Title: "Sessions", Notice: "The completed session read returned no sessions.", Preview: true, Connected: true, Complete: true, ObservedAt: "2026-10-29T14:00:00Z"}
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
	return nil
}
