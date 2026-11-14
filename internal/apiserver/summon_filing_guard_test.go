package apiserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Count write sites, not files: a second builder in an already allowed file
// must fail too. Keep the older guard's restriction on all label users.
// Generated deepcopy is mechanical copying, not an identity constructor.
func TestSummonFilingIdentityHasOneWriter(t *testing.T) {
	root := filepath.Join("..", "..")
	got := map[string][]string{}
	for _, dir := range []string{"api", "cmd", "internal"} {
		if err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			if ast.IsGenerated(file) {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			for kind, sites := range summonIdentityWrites(file) {
				for _, fn := range sites {
					got[kind] = append(got[kind], filepath.ToSlash(rel)+":"+fn)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	want := map[string][]string{
		"summoned":        {"internal/apiserver/summon.go:buildSummonSession", "internal/controller/summon_filing.go:summonFilingChild"},
		"filed-for-uid":   {"internal/controller/summon_filing.go:summonFilingChild"},
		"FiledFor":        {"internal/controller/summon_filing.go:summonFilingChild"},
		"filing-prefix":   {"internal/summonledger/request.go:FilingID"},
		"filing-dispatch": {"internal/controller/summon_filing.go:summonFilingChild"},
	}
	for _, sites := range got {
		slices.Sort(sites)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("identity write sites=%v, want %v", got, want)
	}
}

func summonIdentityKey(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		switch x.Name {
		case "LabelSummoned":
			return "summoned"
		case "LabelFiledForUID":
			return "filed-for-uid"
		case "FiledFor":
			return "FiledFor"
		}
	case *ast.SelectorExpr:
		return summonIdentityKey(x.Sel)
	case *ast.IndexExpr:
		return summonIdentityKey(x.Index)
	case *ast.BasicLit:
		value, _ := strconv.Unquote(x.Value)
		if value == "dev-env.haynesops.com/summoned" || value == "summoned" {
			return "summoned"
		}
		if value == "dev-env.haynesops.com/filed-for-uid" || value == "filed-for-uid" {
			return "filed-for-uid"
		}
	case *ast.BinaryExpr:
		if key := summonIdentityKey(x.Y); key != "" {
			return key
		}
		return summonIdentityKey(x.X)
	}
	return ""
}

func summonIdentityWrites(file *ast.File) map[string][]string {
	writes := map[string][]string{}
	for _, decl := range file.Decls {
		name := "<package>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			name = fn.Name.Name
		}
		add := func(key string) {
			if key != "" {
				writes[key] = append(writes[key], name)
			}
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				// A label selector is a read, not a stored label.
				if sel, ok := x.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "MatchingLabels" {
					return false
				}
			case *ast.KeyValueExpr:
				add(summonIdentityKey(x.Key))
				if key, ok := x.Key.(*ast.Ident); ok && key.Name == "DispatchID" && callsFilingID(x.Value) {
					add("filing-dispatch")
				}
			case *ast.AssignStmt:
				for i, lhs := range x.Lhs {
					add(summonIdentityKey(lhs))
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == "DispatchID" && i < len(x.Rhs) && callsFilingID(x.Rhs[i]) {
						add("filing-dispatch")
					}
				}
			case *ast.BasicLit:
				value, _ := strconv.Unquote(x.Value)
				if strings.HasPrefix(value, "filing:") {
					add("filing-prefix")
				}
			}
			return true
		})
	}
	return writes
}

func callsFilingID(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok {
			value, _ := strconv.Unquote(lit.Value)
			found = found || strings.HasPrefix(value, "filing:")
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				found = found || sel.Sel.Name == "FilingID"
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				found = found || id.Name == "FilingID"
			}
		}
		return true
	})
	return found
}

// Mutations within one file: duplicate literal sites and assignment sites
// are counted separately; selectors and identity comparisons add no writes.
func TestSummonFilingIdentityGuardCountsDuplicateWrites(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package fixture
func builder() {
 _ = map[string]string{out.LabelSummoned: "true", out.LabelFiledForUID: "one"}
 _ = map[string]string{out.LabelSummoned: "true", out.LabelFiledForUID: "two"}
 s.Labels["dev-env.haynesops.com/summoned"] = "true"
 s.Labels[out.LabelFiledForUID] = "three"
 _ = Sum{FiledFor: value, DispatchID: ledger.FilingID(uid)}
 s.Spec.Summon.FiledFor = value
 s.Spec.Summon.DispatchID = ledger.FilingID(uid)
 _ = "filing:" + string(uid)
 _ = "filing:" + string(other)
 _ = client.MatchingLabels{out.LabelSummoned: "true", out.LabelFiledForUID: "read"}
 _ = s.Spec.Summon.FiledFor == value
}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := summonIdentityWrites(file)
	for key, count := range map[string]int{"summoned": 3, "filed-for-uid": 3, "FiledFor": 2, "filing-prefix": 2, "filing-dispatch": 2} {
		if len(got[key]) != count {
			t.Fatalf("%s writes=%d, want %d: %v", key, len(got[key]), count, got)
		}
	}
}
