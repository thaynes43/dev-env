package keeper

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
)

// Catch a new trigger from the control socket, consumer status, or another
// keeper runnable. The positive provider fixture separately verifies that the
// cadence path sends the exact current journal bearer to the profile endpoint.
func TestClaudeEarlyDecisionHasOnlyTheKeeperCadenceEntryPoint(t *testing.T) {
	want := map[string]map[string]int{
		"runClaudeProbes":       {"Start": 1},
		"probeClaude":           {"runClaudeProbes": 1},
		"considerEarlyRefresh":  {"probeClaude": 1},
		"dispatchClaudeRefresh": {"probeClaude": 1, "refresh": 1},
	}
	got := make(map[string]map[string]int)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(files, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				s, ok := n.(*ast.SelectorExpr)
				if !ok || want[s.Sel.Name] == nil {
					return true
				}
				if got[s.Sel.Name] == nil {
					got[s.Sel.Name] = make(map[string]int)
				}
				got[s.Sel.Name][fn.Name.Name]++
				return true
			})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("early decision paths changed: got %v, want %v", got, want)
	}
}
