package keeper

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/types"
)

// TestRunMakesOnlyTheGHTokenAJob ties the package doc to Run. Keeper.attempt
// retries a failed refresh and keeps no intent journal or fence, so a rotating
// login run as a Job would replay a spent refresh token and revoke its family
// (CLAUDE.md, hard rules). Run's Jobs are the gh installation token alone, with
// every other credential enabled, and the package doc says so.
func TestRunMakesOnlyTheGHTokenAJob(t *testing.T) {
	secret := types.NamespacedName{Namespace: "dev-agents", Name: "dev-env-gh-token"}
	o := Options{
		Namespace:     "dev-env-system",
		GHTokenSecret: secret,
		GitHubApp:     &GitHubApp{},
		CodexAuth:     CodexOptions{Enabled: true},
		ProxmoxGrants: ProxmoxGrantOptions{Enabled: true},
	}
	jobs := keeperJobs(o)
	if len(jobs) != 1 || jobs[0].Name != "gh-token" || jobs[0].Secret != secret || jobs[0].Key != GHTokenKey || jobs[0].Refresh == nil {
		t.Fatalf("the Keeper's Jobs are %+v, want the gh-token Job alone", jobs)
	}

	file, err := parser.ParseFile(token.NewFileSet(), "keeper.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(file.Doc.Text()), " ")
	for _, want := range []string{"Only non-rotating credentials (today, the gh installation token) may be Jobs.", "A rotating login must never be one"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the package doc no longer says %q", want)
		}
	}
	if strings.Contains(doc, "new Jobs on the same loop") {
		t.Error("the package doc again says rotating logins are new Jobs on the same loop")
	}
}
