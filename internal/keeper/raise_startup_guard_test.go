package keeper

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// Run constructs its Kubernetes client, manager and lease lock directly, so
// it cannot run against the startup fakes without a production refactor. Keep
// its raise wiring here; raise_startup_test.go checks the gated helper's I/O.
func TestRaiseStartupRunUsesGate(t *testing.T) {
	positions := token.NewFileSet()
	file, err := parser.ParseFile(positions, "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path == "github.com/thaynes43/dev-env/internal/sessionraise" {
			t.Error("run.go must leave raise key and ledger access to the gated helper")
		}
	}
	var run *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Run" {
			run = fn
		}
	}
	if run == nil || run.Body == nil {
		t.Fatal("Run not found")
	}
	isName := func(expr ast.Expr, name string) bool {
		id, ok := expr.(*ast.Ident)
		return ok && id.Name == name
	}
	gateCalls, gateReferences := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.Ident:
			if n.Name == "RaisePager" {
				t.Errorf("%s: raise pager construction and registration belong in the gated helper", positions.Position(n.Pos()))
			}
			if n.Name == "registerRaisePager" {
				gateReferences++
			}
		case *ast.CallExpr:
			if isName(n.Fun, "registerRaisePager") {
				gateCalls++
				if n.Pos() < run.Body.Pos() || n.End() > run.Body.End() || len(n.Args) != 2 || !isName(n.Args[0], "o") || !isName(n.Args[1], "mgr") {
					t.Errorf("%s: Run must call registerRaisePager(o, mgr) directly", positions.Position(n.Pos()))
				}
			}
		}
		return true
	})
	if gateCalls != 1 || gateReferences != 1 {
		t.Errorf("run.go must use registerRaisePager exactly once, directly in Run: calls=%d references=%d", gateCalls, gateReferences)
	}
	ast.Inspect(run.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if n.Sel.Name == "RaisePages" {
				t.Errorf("%s: Run must pass the original raise options to the gate", positions.Position(n.Pos()))
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if isName(lhs, "o") {
					t.Errorf("%s: Run must not replace its startup options", positions.Position(n.Pos()))
				}
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND && isName(n.X, "o") {
				t.Errorf("%s: Run must not expose mutable startup options", positions.Position(n.Pos()))
			}
		}
		return true
	})
}
