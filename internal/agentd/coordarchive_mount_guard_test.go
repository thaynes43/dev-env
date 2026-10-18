package agentd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTestsCallSetCoordArchiveMountCheck keeps the coordination-archive
// mount bypass out of production. The declaration in coordarchive.go is the
// only non-test mention. A synthetic caller proves the scan sees both a bare
// call and a selector call.
func TestOnlyTestsCallSetCoordArchiveMountCheck(t *testing.T) {
	root := moduleRoot(t)
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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
	decls, uses := scanMountBypass(t, files)
	if len(decls) != 1 || !strings.HasSuffix(decls[0], filepath.Join("internal", "agentd", "coordarchive.go")) {
		t.Fatalf("declarations = %v, want only coordarchive.go", decls)
	}
	if len(uses) != 0 {
		t.Fatalf("non-test uses of SetCoordArchiveMountCheck: %s", strings.Join(uses, "\n"))
	}

	dir := t.TempDir()
	caller := filepath.Join(dir, "caller.go")
	body := "package p\nfunc f() { SetCoordArchiveMountCheck(nil) }\nfunc g() { agentd.SetCoordArchiveMountCheck(func(string, string) error { return nil }) }\n"
	if err := os.WriteFile(caller, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	decls, uses = scanMountBypass(t, []string{caller})
	if len(decls) != 0 || len(uses) != 2 {
		t.Fatalf("synthetic scan decls=%v uses=%v", decls, uses)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("module root not found at %s: %v", root, err)
	}
	return root
}

func scanMountBypass(t *testing.T, files []string) (decls, uses []string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var decl token.Pos
		ast.Inspect(file, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if ok && fd.Name != nil && fd.Name.Name == "SetCoordArchiveMountCheck" {
				decl = fd.Name.Pos()
				decls = append(decls, path)
			}
			return true
		})
		seen := map[token.Pos]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			name, pos, ok := mountBypassIdent(n)
			if ok && name == "SetCoordArchiveMountCheck" && pos != decl && !seen[pos] {
				seen[pos] = true
				uses = append(uses, fset.Position(pos).String())
			}
			return true
		})
	}
	return decls, uses
}

func mountBypassIdent(n ast.Node) (string, token.Pos, bool) {
	switch x := n.(type) {
	case *ast.Ident:
		return x.Name, x.Pos(), true
	case *ast.SelectorExpr:
		if x.Sel != nil {
			return x.Sel.Name, x.Sel.Pos(), true
		}
	}
	return "", token.NoPos, false
}
