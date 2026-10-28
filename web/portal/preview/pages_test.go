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
		{"pages-available.html", []string{"Fixture preview · synthetic data", "Synthetic sample alert", "Example alert for a local preview", "Unknown"}},
		{"pages-empty.html", []string{"Fixture preview · synthetic data", "Observed zero current alerts"}},
		{"pages-unavailable.html", []string{"Fixture preview · synthetic data", "Alertmanager is unavailable"}},
		{"pages-disabled.html", []string{"Fixture preview · synthetic data", "Pages not enabled"}},
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
		if strings.Contains(string(body), "<script") || strings.Contains(string(body), "<form") {
			t.Fatal("unexpected active content")
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
