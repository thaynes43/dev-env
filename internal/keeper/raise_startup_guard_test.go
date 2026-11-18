package keeper

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Run constructs its Kubernetes client, manager and lease lock directly, so
// it cannot run against the startup fakes without a production refactor. Keep
// package-wide raise wiring here; raise_startup_test.go checks the gated helper's I/O.
func TestRaiseStartupRunUsesGate(t *testing.T) {
	positions := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	var run, announce *ast.FuncDecl
	var gateDeclaration *ast.Ident
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(positions, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = file
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "github.com/thaynes43/dev-env/internal/sessionraise" && name != "raise_pages.go" {
				t.Errorf("%s: raise key and ledger access belongs in raise_pages.go", positions.Position(imp.Pos()))
			}
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if name == "run.go" && fn.Name.Name == "Run" {
				run = fn
			}
			if name == "raise_pages.go" {
				switch fn.Name.Name {
				case "registerRaisePager":
					gateDeclaration = fn.Name
				case "announce":
					announce = fn
				}
			}
		}
	}
	if run == nil || run.Body == nil {
		t.Fatal("Run not found")
	}
	isName := func(expr ast.Expr, name string) bool {
		id, ok := expr.(*ast.Ident)
		return ok && id.Name == name
	}
	if gateDeclaration == nil || announce == nil || announce.Body == nil {
		t.Fatal("raise startup gate or announce not found")
	}
	// Track aliases too, including chains declared in raise_pages.go and
	// then used elsewhere. No type checking or Kubernetes client is needed.
	raiseTypes := map[string]bool{"RaisePager": true}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			ast.Inspect(file, func(node ast.Node) bool {
				spec, ok := node.(*ast.TypeSpec)
				if !ok || !spec.Assign.IsValid() || raiseTypes[spec.Name.Name] {
					return true
				}
				ast.Inspect(spec.Type, func(node ast.Node) bool {
					if id, ok := node.(*ast.Ident); ok && raiseTypes[id.Name] {
						raiseTypes[spec.Name.Name] = true
						changed = true
					}
					return true
				})
				return true
			})
		}
	}
	gateCalls, gateReferences, ensureCalls, ensureReferences := 0, 0, 0, 0
	for name, file := range files {
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				if raiseTypes[n.Name] && name != "raise_pages.go" {
					t.Errorf("%s: raise pager construction and registration belong in the gated helper", positions.Position(n.Pos()))
				}
				if n.Name == "registerRaisePager" && n != gateDeclaration {
					gateReferences++
				}
			case *ast.SelectorExpr:
				if n.Sel.Name == "Ensure" {
					ensureReferences++
					if name != "raise_pages.go" || n.Pos() < announce.Body.Pos() || n.End() > announce.Body.End() || !isName(n.X, "sessionraise") {
						t.Errorf("%s: raise key Ensure belongs only in RaisePager.announce", positions.Position(n.Pos()))
					}
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && isName(sel.X, "sessionraise") && sel.Sel.Name == "Ensure" {
					ensureCalls++
				}
				if isName(n.Fun, "registerRaisePager") {
					gateCalls++
					if n.Pos() < run.Body.Pos() || n.End() > run.Body.End() || len(n.Args) != 2 || !isName(n.Args[0], "o") || !isName(n.Args[1], "mgr") {
						t.Errorf("%s: Run must call registerRaisePager(o, mgr) directly", positions.Position(n.Pos()))
					}
				}
			}
			return true
		})
	}
	if ensureCalls != 1 || ensureReferences != 1 {
		t.Errorf("raise_pages.go must call raise key Ensure exactly once in announce: calls=%d references=%d", ensureCalls, ensureReferences)
	}
	if gateCalls != 1 || gateReferences != 1 {
		t.Errorf("keeper package must use registerRaisePager exactly once, directly in Run: calls=%d references=%d", gateCalls, gateReferences)
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
