package apiserver

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Proof 8 of story 782 (DESIGN-064 6.8, ruling Q2 on item 742): the summoning
// create labels its session for the keeper's pager, and no other create path
// does.
func TestSummonCreateLabelsTheSession(t *testing.T) {
	f := summonFixture(t)
	w := summonPost(t, f, remediationBody("rem-responder-1", map[string]string{"alert": "disk full"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	stored := f.session(decode[apiv1.Session](t, w).Name)
	if stored.Labels[summonoutcome.LabelSummoned] != summonoutcome.LabelSummonedValue || stored.Labels[v1alpha1.LabelDepth] != "0" || stored.Spec.Caller == "" {
		t.Fatalf("labels %v caller %q", stored.Labels, stored.Spec.Caller)
	}

	g := newFixture(t)
	w = g.do(http.MethodPost, apiv1.SessionsPath, tokHuman, task())
	if w.Code != http.StatusCreated {
		t.Fatalf("ordinary create: %d %s", w.Code, w.Body.String())
	}
	if _, ok := g.session(decode[apiv1.Session](t, w).Name).Labels[summonoutcome.LabelSummoned]; ok {
		t.Fatal("an ordinary create carries the summoned label")
	}
}

// Only the summoning create's builder names the label among the packages that
// create AgentSessions; the keeper only reads it.
func TestSummonedLabelHasOneWriter(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := map[string]bool{
		filepath.Join("internal", "summonoutcome", "pages.go"): true, // the constant
		filepath.Join("internal", "apiserver", "summon.go"):    true, // the one create that sets it
		filepath.Join("internal", "keeper", "summon_pages.go"): true, // the pager's selector
	}
	var users []string
	for _, dir := range []string{"api", "cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			found := false
			ast.Inspect(file, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					found = found || x.Sel.Name == "LabelSummoned"
				case *ast.BasicLit:
					found = found || strings.Contains(x.Value, "dev-env.haynesops.com/summoned")
				}
				return true
			})
			if found {
				rel, _ := filepath.Rel(root, path)
				users = append(users, rel)
				if !allowed[rel] {
					t.Errorf("%s names the summoned label", rel)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(users) != 2 { // summon.go and summon_pages.go select it; pages.go declares it by name, not by selector
		t.Fatalf("label users %v", users)
	}
}
