package apiserver

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestGrantsDocKeepsTheExpiryInformer keeps grants.go's file comment true to
// cmd/dev-env-operator, which registers grantexpiry.Reconciler and so holds an
// AccessGrant informer (D-64). The RBAC it needs is proven by the broker suite
// (TestOperatorKeepsGrantExpiryInformerRBAC); a comment that said the operator
// keeps no informer invited a reviewer to trim that RBAC.
func TestGrantsDocKeepsTheExpiryInformer(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "grants.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var comments []string
	for _, g := range file.Comments {
		comments = append(comments, g.Text())
	}
	doc := strings.Join(strings.Fields(strings.Join(comments, " ")), " ")
	if strings.Contains(doc, "keeps no AccessGrant informer") || !strings.Contains(doc, "The operator does hold an AccessGrant informer, for D-64's egress expiry backstop") {
		t.Error("grants.go no longer says the operator holds an AccessGrant informer for D-64's expiry backstop")
	}
}
