package main

import (
	"os"
	"path/filepath"

	"github.com/thaynes43/dev-env/web/portal"
)

func writeProductStates(dir string) error {
	page := portal.ProductPage{Title: "Sessions", Connected: true, Complete: true, Preview: true,
		ObservedAt: "2026-10-29T14:00:00Z", Filter: portal.ProductFilter{View: "usage"},
		Usage: &portal.ProductUsage{Scope: "Synthetic usage preview", ScopeNote: "Fixture data only.",
			Stats:   []portal.ProductStat{{Label: "Measured", Value: "Unknown", Tone: "muted"}},
			Columns: []string{"Session", "Input"}, Rows: []portal.ProductUsageRow{{ID: "s1", Name: "synthetic-agent", Cells: []portal.ProductUsageCell{{Label: "Input", Value: "Unknown", Unknown: true}}}}}}
	body, err := portal.RenderProduct(page)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "usage.html"), body, 0600)
}
