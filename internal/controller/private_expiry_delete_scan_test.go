package controller

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestExpiryDeleteNeedsVerify (DESIGN-025 11) reads this package's source. The
// HOME delete (deleteExpiryHome, which also lifts the finalizer of a claim
// already deleting) and the write of phase Deleting are each reachable only
// after, in the same function and in this order: historyArchiveVerified, as
// an if's init whose body returns; expiryRechecked, its view checked against
// nil with a return; and noPodUsesHome on that view, as an if's init whose
// body returns. The delete must take the Session and the claim from that same
// view, the fresh reads made after the verify. So every attempt, the retries
// from Deleting included, verifies the archive and checks for Pods first,
// like DESIGN-024's rule 4. TestOnlyTheGuardDeletes pins the rest: the one
// caller of deleteExpiryHome, removeExpiredHome, and the one caller of
// releaseExpiryHome, deleteExpiryHome. The scan reads syntax, not values;
// TestExpiryDeleteScanCatchesUncheckedDeletes shows what it catches.
func TestExpiryDeleteNeedsVerify(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	files = slices.DeleteFunc(files, func(f string) bool { return strings.HasSuffix(f, "_test.go") })
	scan := scanExpiryDelete(t, files)
	for _, v := range scan.violations {
		t.Error(v)
	}
	// It must see the one delete and the one Deleting write, or it proves
	// nothing.
	if scan.deletes != 1 || scan.deletingWrites != 1 {
		t.Errorf("the scan saw %d calls of deleteExpiryHome and %d writes of Deleting; want one each", scan.deletes, scan.deletingWrites)
	}
}

// TestExpiryDeleteScanCatchesUncheckedDeletes runs the scan over
// testdata/expirydeletescan, never compiled, where each function but the two
// good ones breaks one rule.
func TestExpiryDeleteScanCatchesUncheckedDeletes(t *testing.T) {
	scan := scanExpiryDelete(t, []string{filepath.Join("testdata", "expirydeletescan", "deletes.go")})
	for _, fn := range []string{
		"deleteWithoutVerify", "verifyAfterDelete", "recheckBeforeVerify", "podCheckOnStaleView", "deleteOnStaleView",
		"verifyIgnored", "noNilCheck", "deletingWithoutChecks", "deletingBeforePodCheck",
	} {
		if !slices.ContainsFunc(scan.violations, func(v string) bool { return strings.Contains(v, " in "+fn+":") }) {
			t.Errorf("the scan does not catch %s", fn)
		}
	}
	for _, v := range scan.violations {
		if strings.Contains(v, " in goodDelete:") || strings.Contains(v, " in goodDeleting:") {
			t.Errorf("a false finding: %s", v)
		}
	}
}

type expiryDeleteScan struct {
	violations             []string
	deletes, deletingWrites int
}

func scanExpiryDelete(t *testing.T, files []string) expiryDeleteScan {
	t.Helper()
	fset := token.NewFileSet()
	var scan expiryDeleteScan
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				scanExpiryDeleteFunc(fset, fn, &scan)
			}
		}
	}
	return scan
}

func scanExpiryDeleteFunc(fset *token.FileSet, fn *ast.FuncDecl, scan *expiryDeleteScan) {
	name := fn.Name.Name
	report := func(pos token.Pos, format string, args ...any) {
		scan.violations = append(scan.violations, fmt.Sprintf("%s: in %s: ", fset.Position(pos), name)+fmt.Sprintf(format, args...))
	}
	// The checks found so far, each only after the one before it.
	var verified, rechecked, nilChecked, podChecked token.Pos
	view := ""
	checked := func() bool { return podChecked != token.NoPos }
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt:
			if call := initCall(x.Init); call != nil && endsInReturn(x.Body) {
				switch calleeName(call) {
				case "historyArchiveVerified":
					if verified == token.NoPos {
						verified = call.Pos()
					}
				case "noPodUsesHome":
					if nilChecked != token.NoPos && len(call.Args) == 1 && isIdent(call.Args[0], view) && podChecked == token.NoPos {
						podChecked = call.Pos()
					}
				}
			}
			if be, ok := x.Cond.(*ast.BinaryExpr); ok && rechecked != token.NoPos && be.Op == token.EQL && isIdent(be.X, view) && isIdent(be.Y, "nil") && endsInReturn(x.Body) && nilChecked == token.NoPos {
				nilChecked = x.Pos()
			}
		case *ast.AssignStmt:
			if len(x.Rhs) == 1 {
				if call, ok := x.Rhs[0].(*ast.CallExpr); ok && calleeName(call) == "expiryRechecked" && verified != token.NoPos && rechecked == token.NoPos {
					if id, ok := x.Lhs[0].(*ast.Ident); ok && id.Name != "_" {
						rechecked, view = call.Pos(), id.Name
					}
				}
			}
			for i, lhs := range x.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "Phase" && i < len(x.Rhs) && mentions(x.Rhs[i], "HomeExpiryDeleting") {
					scan.deletingWrites++
					if !checked() {
						report(x.Pos(), "Deleting is written before the archive's verify, the re-check and the Pod check, in that order")
					}
				}
			}
		case *ast.CallExpr:
			if calleeName(x) != "deleteExpiryHome" {
				break
			}
			scan.deletes++
			switch {
			case !checked():
				report(x.Pos(), "the HOME delete comes before the archive's verify, the re-check and the Pod check, in that order")
			case len(x.Args) != 5 || !selectorOf(x.Args[3], view, "s") || !selectorOf(x.Args[4], view, "claim"):
				report(x.Pos(), "the HOME delete does not take the Session and claim from the view %s read after the verify", view)
			}
		}
		return true
	})
}

// initCall is the call an if's init makes: `if a, b := f(); ...`.
func initCall(s ast.Stmt) *ast.CallExpr {
	as, ok := s.(*ast.AssignStmt)
	if !ok || len(as.Rhs) != 1 {
		return nil
	}
	call, _ := as.Rhs[0].(*ast.CallExpr)
	return call
}

// endsInReturn reports whether a block's last statement is a return.
func endsInReturn(b *ast.BlockStmt) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	_, ok := b.List[len(b.List)-1].(*ast.ReturnStmt)
	return ok
}

// mentions reports whether e names ident, alone or as a selector's field.
func mentions(e ast.Expr, ident string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == ident {
			found = true
		}
		return !found
	})
	return found
}

// selectorOf reports whether e is base.field.
func selectorOf(e ast.Expr, base, field string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == field && isIdent(sel.X, base)
}
