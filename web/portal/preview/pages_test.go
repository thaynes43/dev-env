package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagesFixturesAreSyntheticAndDistinguishStates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pages")
	if err := writePages(dir); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		want []string
	}{
		{"pages-available.html", []string{"Fixture preview · synthetic data", "Synthetic sample alert", "Example alert for a local preview", "Unknown", "Current snapshot only", "2026-10-28T11:00:00Z", "2026-10-28T12:00:00Z"}},
		{"pages-empty.html", []string{"Fixture preview · synthetic data", "Observed zero current alerts", "No alert types in this current snapshot"}},
		{"pages-unavailable.html", []string{"Fixture preview · synthetic data", "Alertmanager is unavailable"}},
		{"pages-disabled.html", []string{"Fixture preview · synthetic data", "Pages not enabled", "Unavailable"}},
		{"pages-detail.html", []string{"Fixture preview · synthetic data", `id="pages-group-2" class="pages-detail is-default"`, "synthetic-detail-other", "Warning detail summary", "2026-10-28T15:00:00Z", "https://example.test/graph?g0.expr=up", "https://example.test/runbook", "Generator", "Runbook"}},
		{"pages-configuration.html", []string{"Fixture preview · synthetic data", "Not connected: saving and live effect unavailable", "No preference document is loaded. This is not a loaded empty store.", "Unknown / Not connected", "DiskFull", "InodeFull", "Unset", "Desired", "<fieldset disabled"}},
	}
	for _, tc := range tests {
		body, err := os.ReadFile(filepath.Join(dir, tc.name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range tc.want {
			if !strings.Contains(string(body), want) {
				t.Fatalf("%s missing %s", tc.name, want)
			}
		}
		text := string(body)
		if strings.Contains(text, "<script") || strings.Contains(text, "<form") || strings.Contains(text, "policy applied") || strings.Contains(text, "Saved preferences are loaded.") {
			t.Fatal("unexpected active content")
		}
		if tc.name == "pages-detail.html" && (strings.Contains(text, "-04:00") || strings.Contains(text, "evil=1")) {
			t.Fatal("detail mixed an offset or kept an unsafe query")
		}
		if tc.name == "pages-unavailable.html" && (strings.Contains(text, "Pages not enabled") || strings.Contains(text, "Observed zero current alerts") || strings.Contains(text, "Current snapshot is invalid")) {
			t.Fatal("unavailable collapsed into another state")
		}
		if tc.name == "pages-empty.html" && (strings.Contains(text, "Alertmanager is unavailable") || strings.Contains(text, "Pages not enabled")) {
			t.Fatal("empty collapsed into another state")
		}
		if tc.name == "pages-disabled.html" && (strings.Contains(text, "Alertmanager is unavailable") || strings.Contains(text, "Observed zero current alerts")) {
			t.Fatal("disabled collapsed into another state")
		}
	}
	if err := writePages(dir); err == nil {
		t.Fatal("existing output overwritten")
	}
}

func TestPagesPreviewIsOfflineAndFixtureIdentityIsNotProduction(t *testing.T) {
	if principal.Issuer == "https://authentik.haynesnetwork.com/application/o/dev-env-v2-portal/" || principal.Subject != "synthetic-reader" {
		t.Fatal("fixture identity crossed production boundary")
	}
	for _, filename := range []string{"main.go", "pages.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if sel, ok := c.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "Listen", "ListenAndServe", "Serve", "Get", "Post":
						t.Errorf("%s contains %s", filename, sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
}
