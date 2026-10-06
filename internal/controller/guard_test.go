package controller

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// 5.1's guard refuses every state today: drain is plan 04, and nothing is rescued
// before plan 01 step 5. The table covers every phase in both operating modes,
// deleted or not.
func TestTheGuardRefusesEverythingBeforeRescue(t *testing.T) {
	phases := []v1alpha1.SessionPhase{"", v1alpha1.PhasePending, v1alpha1.PhaseRunning, v1alpha1.PhaseIdle,
		v1alpha1.PhaseDraining, v1alpha1.PhaseSuspended, v1alpha1.PhaseArchived, v1alpha1.PhaseFailed}
	now := metav1.Now()
	for _, phase := range phases {
		for _, mode := range []v1alpha1.OperatingMode{v1alpha1.OperatingModeRunning, v1alpha1.OperatingModeSuspended} {
			for _, deleted := range []bool{false, true} {
				s := &v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{OperatingMode: mode}, Status: v1alpha1.AgentSessionStatus{Phase: phase}}
				if deleted {
					s.DeletionTimestamp = &now
				}
				err := podRemovalAllowed(s)
				if err == nil {
					t.Errorf("phase %q, %s, deleted %v: the guard allowed a pod delete", phase, mode, deleted)
					continue
				}
				switch {
				case phase == v1alpha1.PhaseDraining:
					if !errors.Is(err, errDrainNotBuilt) {
						t.Errorf("phase Draining: %v, want the drain reason", err)
					}
				case mode == v1alpha1.OperatingModeSuspended || deleted:
					if !errors.Is(err, errRescueNotBuilt) {
						t.Errorf("phase %q, %s, deleted %v: %v, want the rescue reason", phase, mode, deleted, err)
					}
				default:
					if !strings.Contains(err.Error(), "neither draining nor suspending") {
						t.Errorf("phase %q, running: %v", phase, err)
					}
				}
			}
		}
	}
}

// TestOnlyTheGuardDeletes reads this package's source. A Delete or DeleteAllOf
// call may appear only in deletePod, after its guard; RemoveFinalizer only in
// releaseSession, which only releaseIfEmpty calls. A new delete path cannot slip
// in without changing this test, which is where review looks (5.1).
func TestOnlyTheGuardDeletes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	seen := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			guardAt := token.NoPos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					name = f.Sel.Name
				case *ast.Ident:
					name = f.Name
				}
				at := fset.Position(call.Pos())
				switch name {
				case "podRemovalAllowed":
					if fn.Name.Name == "deletePod" && guardAt == token.NoPos {
						guardAt = call.Pos()
					}
				case "Delete", "DeleteAllOf":
					seen[name]++
					if fn.Name.Name != "deletePod" {
						t.Errorf("%s: %s in %s; only deletePod may delete (5.1)", at, name, fn.Name.Name)
					} else if guardAt == token.NoPos || guardAt > call.Pos() {
						t.Errorf("%s: deletePod deletes before it asks podRemovalAllowed", at)
					}
				case "RemoveFinalizer":
					seen[name]++
					if fn.Name.Name != "releaseSession" {
						t.Errorf("%s: RemoveFinalizer in %s; only releaseSession may lift the finalizer (D-45)", at, fn.Name.Name)
					}
				case "releaseSession":
					if fn.Name.Name != "releaseIfEmpty" {
						t.Errorf("%s: releaseSession called from %s; only releaseIfEmpty, which checks the API server, may", at, fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	// The test must see the calls it guards, or it proves nothing.
	if seen["Delete"] != 1 || seen["RemoveFinalizer"] != 1 {
		t.Errorf("found %d Delete and %d RemoveFinalizer calls; want exactly one each", seen["Delete"], seen["RemoveFinalizer"])
	}
}
