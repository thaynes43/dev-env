package coordretention

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestOnlyRetentionDeletesCoordinationRecords is the source guard for DESIGN-024
// section 5 and section 8. deleteRecord is the only Delete, release is its only
// caller, and that caller reaches it only after releaseRechecked and
// archiveVerified. The operator starts the runnable only inside the retention
// flag. A disabled runnable does not register metrics or delete.
//
// Known gap: the scan reads function declarations, not function literals, and
// it does not type-check other packages. Review still covers those.
func TestOnlyRetentionDeletesCoordinationRecords(t *testing.T) {
	scan := scanRetention(t, ".")
	for _, violation := range scan.violations {
		t.Error(violation)
	}
	if scan.deletes != 1 || !scan.preconditions {
		t.Fatalf("deleteRecord deletes=%d preconditions=%v, want one Delete with UID and resourceVersion", scan.deletes, scan.preconditions)
	}
	if scan.callers["release"] < 1 {
		t.Fatal("release does not call deleteRecord")
	}
	for caller := range scan.callers {
		if caller != "release" {
			t.Errorf("deleteRecord caller %s", caller)
		}
	}
	recheck := indexOf(scan.release, "releaseRechecked")
	verified := indexOf(scan.release, "archiveVerified")
	deleted := indexOf(scan.release, "deleteRecord")
	if recheck < 0 || verified < 0 || deleted < 0 || !(recheck < verified && verified < deleted) {
		t.Fatalf("release call order = %v", scan.release)
	}
	if !retentionFlagGatesRunnable(t, filepath.Join("..", "..", "cmd", "dev-env-operator", "main.go")) {
		t.Fatal("coordination retention runnable is not created inside the retention flag")
	}

	reg := prometheus.NewRegistry()
	runnable := &Runnable{
		Enabled: false, Grace: DefaultGrace, Registerer: reg,
		Client: deletingClient{t: t}, Reader: deletingClient{t: t},
		Archive: &memArchive{}, Slots: nil,
	}
	if err := runnable.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := runnable.Pass(t.Context()); got != "" {
		t.Fatalf("disabled pass = %q", got)
	}
	families, err := reg.Gather()
	if err != nil || len(families) != 0 {
		t.Fatalf("disabled runnable exported metrics: %v %v", families, err)
	}
}

// TestRetentionGuardScanCatches runs the same scan over testdata/guardscan, a
// file that is never compiled. Each function breaks one retention-delete rule.
func TestRetentionGuardScanCatches(t *testing.T) {
	scan := scanRetention(t, filepath.Join("testdata", "guardscan"))
	joined := strings.Join(scan.violations, "\n")
	for _, want := range []string{"extraDelete", "DeleteAllOf", "deleteRecord holds", "sneaky", "archiveVerified"} {
		if !strings.Contains(joined, want) {
			t.Errorf("guard scan does not mention %s\n%s", want, joined)
		}
	}
}

type retentionScan struct {
	violations    []string
	deletes       int
	preconditions bool
	callers       map[string]int
	release       []string
}

func scanRetention(t *testing.T, dir string) retentionScan {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scan := retentionScan{callers: map[string]int{}}
	var parsed []*ast.File
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
	}
	if len(parsed) == 0 {
		t.Fatalf("no Go files in %s", dir)
	}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			calls := callNames(fn.Body)
			if name == "release" {
				scan.release = calls
			}
			deletes := 0
			for _, call := range calls {
				switch call {
				case "Delete":
					deletes++
					if name != "deleteRecord" {
						scan.violations = append(scan.violations, name+": Delete outside deleteRecord")
					}
				case "DeleteAllOf":
					scan.violations = append(scan.violations, name+": DeleteAllOf")
				case "Update", "Patch":
					scan.violations = append(scan.violations, name+": "+call+" of a coordination record")
				case "deleteRecord":
					if name != "release" {
						scan.violations = append(scan.violations, name+": calls deleteRecord")
					}
					scan.callers[name]++
				}
			}
			if name == "deleteRecord" {
				scan.deletes = deletes
				if deletes != 1 {
					scan.violations = append(scan.violations, fmt.Sprintf("deleteRecord holds %d Delete calls", deletes))
				}
				scan.preconditions = deleteHasPreconditions(fn.Body)
				if !scan.preconditions {
					scan.violations = append(scan.violations, "deleteRecord Delete lacks UID and resourceVersion preconditions")
				}
			}
			if name == "release" && indexOf(calls, "deleteRecord") >= 0 {
				if indexOf(calls, "releaseRechecked") < 0 || indexOf(calls, "archiveVerified") < 0 || !(indexOf(calls, "releaseRechecked") < indexOf(calls, "archiveVerified") && indexOf(calls, "archiveVerified") < indexOf(calls, "deleteRecord")) {
					scan.violations = append(scan.violations, "release reaches deleteRecord without releaseRechecked and archiveVerified")
				}
			}
		}
	}
	return scan
}

func callNames(body *ast.BlockStmt) []string {
	var names []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		names = append(names, callName(call.Fun))
		return true
	})
	return names
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	default:
		return ""
	}
}

func deleteHasPreconditions(body *ast.BlockStmt) bool {
	found := false
	ok := true
	ast.Inspect(body, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall || callName(call.Fun) != "Delete" {
			return true
		}
		found = true
		var preconditions, uid, rv bool
		ast.Inspect(call, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if x.Sel.Name == "Preconditions" {
					preconditions = true
				}
			case *ast.KeyValueExpr:
				if id, isIdent := x.Key.(*ast.Ident); isIdent {
					if id.Name == "UID" {
						uid = true
					}
					if id.Name == "ResourceVersion" {
						rv = true
					}
				}
			}
			return true
		})
		if !preconditions || !uid || !rv {
			ok = false
		}
		return true
	})
	return found && ok
}

func indexOf(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

func retentionFlagGatesRunnable(t *testing.T, path string) bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var gates [][2]token.Pos
	var calls []token.Pos
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt:
			if nodeNames(x.Cond, "coordinationRetention") && x.Body != nil {
				gates = append(gates, [2]token.Pos{x.Body.Pos(), x.Body.End()})
			}
		case *ast.CallExpr:
			if callName(x.Fun) == "newCoordinationRetention" {
				calls = append(calls, x.Pos())
			}
		}
		return true
	})
	if len(calls) != 1 {
		return false
	}
	for _, gate := range gates {
		if calls[0] >= gate[0] && calls[0] < gate[1] {
			return true
		}
	}
	return false
}

func nodeNames(n ast.Node, want string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if x.Name == want {
				found = true
			}
		case *ast.SelectorExpr:
			if x.Sel.Name == want {
				found = true
			}
		}
		return true
	})
	return found
}

type deletingClient struct {
	client.Client
	t *testing.T
}

func (c deletingClient) Delete(context.Context, client.Object, ...client.DeleteOption) error {
	c.t.Fatal("disabled retention deleted a record")
	return nil
}
