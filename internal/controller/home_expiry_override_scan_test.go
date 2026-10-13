package controller

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestNoCodeWritesHomeExpiryOverride is DESIGN-025 section 9's guard: the
// owner's override, the dev-env.haynesops.com/home-expiry annotation, is Tom's
// alone, so no dev-env code writes it. The scan reads every non-test Go file of
// the module. AnnotationHomeExpiry may appear only as the key of a read,
// X.Annotations[AnnotationHomeExpiry] or X.GetAnnotations()[...], that is not
// assigned to and not deleted; and no string literal names the key, except the
// constant's own declaration. TestOverrideScanCatchesWrites runs the scan over
// testdata/overridescan, where each function writes the override in a
// different way. Known gaps: a key assembled from pieces that do not spell
// "home-expiry", and writes through reflection or another module; review
// covers those, and the haynes-ops admission policy refuses every
// ServiceAccount's write anyway.
func TestNoCodeWritesHomeExpiryOverride(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "bin" || name == "vendor" || name == "node_modules" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 50 {
		t.Fatalf("scanned only %d files; the module walk is broken", len(files))
	}
	scan := scanOverrideWrites(t, files)
	for _, v := range scan.violations {
		t.Error(v)
	}
	// The scan must see the declaration and the expiry's and the stop's reads,
	// or it proves nothing.
	if scan.declared != 1 || scan.reads < 3 {
		t.Fatalf("saw the declaration %d times and %d reads; the scan is broken", scan.declared, scan.reads)
	}
}

// TestOverrideScanCatchesWrites runs the scan over testdata/overridescan, a
// file that is never compiled. Each function there writes the override, and
// the scan must name every one.
func TestOverrideScanCatchesWrites(t *testing.T) {
	scan := scanOverrideWrites(t, []string{filepath.Join("testdata", "overridescan", "override.go")})
	for _, fn := range []string{
		"assignHold", "deleteOverride", "mapLiteral", "setterCall", "mergePatchString", "jsonPointerPatch",
		"aliasedKey", "literalKey", "getterAssign", "incrementOverride",
	} {
		if !slices.ContainsFunc(scan.violations, func(v string) bool { return strings.Contains(v, " in "+fn+";") }) {
			t.Errorf("the override scan does not catch %s", fn)
		}
	}
}

type overrideScan struct {
	violations []string
	declared   int
	reads      int
}

// overrideKeyLiteral matches a string that names the override's key, and not
// the expiry's own hold pod annotations (home-expiry-executor and
// home-expiry-hold-intent), which are other keys.
var overrideKeyLiteral = regexp.MustCompile(`home-expiry($|[^-a-z])`)

func scanOverrideWrites(t *testing.T, files []string) overrideScan {
	t.Helper()
	fset := token.NewFileSet()
	scan := overrideScan{}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		report := func(pos token.Pos, fn, format string, args ...any) {
			scan.violations = append(scan.violations, fmt.Sprintf("%s: in %s; ", fset.Position(pos), fn)+fmt.Sprintf(format, args...))
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Body != nil {
					scanOverrideBody(&scan, report, d.Name.Name, d.Body)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range vs.Names {
						if name.Name == "AnnotationHomeExpiry" && i < len(vs.Values) {
							// The constant's own declaration names the key.
							scan.declared++
							continue
						}
						if i < len(vs.Values) {
							scanOverrideExpr(&scan, report, name.Name, vs.Values[i])
						}
					}
				}
			}
		}
	}
	return scan
}

// scanOverrideBody checks one function body.
func scanOverrideBody(scan *overrideScan, report func(token.Pos, string, string, ...any), fn string, body *ast.BlockStmt) {
	scanOverrideExpr(scan, report, fn, body)
}

// scanOverrideExpr walks n and reports every use of the key that is not a
// plain read of an object's annotations.
func scanOverrideExpr(scan *overrideScan, report func(token.Pos, string, string, ...any), fn string, n ast.Node) {
	// reads are the key identifiers used as the index of a read; written are
	// index expressions that are assigned to or deleted.
	reads := map[ast.Node]bool{}
	written := map[ast.Node]bool{}
	ast.Inspect(n, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, lhs := range x.Lhs {
				written[unparen(lhs)] = true
			}
		case *ast.IncDecStmt:
			written[unparen(x.X)] = true
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "delete" && len(x.Args) == 2 {
				if key := overrideKey(x.Args[1]); key != nil {
					report(x.Pos(), fn, "deletes the owner's HOME expiry override; only Tom removes it (DESIGN-025 9)")
					reads[key] = true
				}
			}
		case *ast.IndexExpr:
			key := overrideKey(x.Index)
			if key == nil {
				return true
			}
			if annotationsOf(x.X) && !written[x] {
				reads[key] = true
				scan.reads++
			} else {
				reads[key] = true
				report(x.Pos(), fn, "writes the owner's HOME expiry override; only Tom sets it (DESIGN-025 9)")
			}
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if v, err := strconv.Unquote(x.Value); err == nil && overrideKeyLiteral.MatchString(v) {
					report(x.Pos(), fn, "a string names the owner's HOME expiry override key %q; read it through v1alpha1.AnnotationHomeExpiry only", v)
				}
			}
		}
		return true
	})
	ast.Inspect(n, func(n ast.Node) bool {
		var key ast.Node
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if x.Sel.Name == "AnnotationHomeExpiry" {
				key = x
			}
		case *ast.Ident:
			if x.Name == "AnnotationHomeExpiry" {
				key = x
			}
		}
		if key == nil {
			return true
		}
		if !reads[key] {
			report(key.Pos(), fn, "uses AnnotationHomeExpiry other than as the key of a read of an object's annotations (DESIGN-025 9)")
		}
		// A selector's own Sel is the same name: do not report it twice.
		return false
	})
}

// overrideKey is the AnnotationHomeExpiry identifier or selector e names, or
// nil.
func overrideKey(e ast.Expr) ast.Node {
	switch x := unparen(e).(type) {
	case *ast.SelectorExpr:
		if x.Sel.Name == "AnnotationHomeExpiry" {
			return x
		}
	case *ast.Ident:
		if x.Name == "AnnotationHomeExpiry" {
			return x
		}
	}
	return nil
}

// annotationsOf reports whether e is X.Annotations or X.GetAnnotations().
func annotationsOf(e ast.Expr) bool {
	switch x := unparen(e).(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "Annotations"
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "GetAnnotations" && len(x.Args) == 0
	}
	return false
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
}
