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
// (CLAUDE.md, hard rules). With the ops App unset, the only Job is the dev bot
// token, including when rotating logins are enabled. The ops installation
// token is a second Job only when its App is set. A rotating login is never one.
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
	// A named ops Secret with no App is still the dev token alone.
	o.OpsGHTokenSecret = types.NamespacedName{Namespace: "dev-agents", Name: DefaultOpsGHTokenSecret}
	if jobs = keeperJobs(o); len(jobs) != 1 || jobs[0].Name != "gh-token" {
		t.Fatalf("ops Secret without an App added a Job: %+v", jobs)
	}
	o.OpsGitHubApp = &GitHubApp{
		Dir:          DefaultOpsGitHubAppDir,
		Permissions:  DefaultOpsBotPermissions,
		Repositories: []string{"haynes-ops"},
	}
	jobs = keeperJobs(o)
	if len(jobs) != 2 || jobs[0].Name != "gh-token" || jobs[1].Name != "ops-gh-token" || jobs[1].Secret != o.OpsGHTokenSecret || jobs[1].Key != GHTokenKey || jobs[1].Refresh == nil {
		t.Fatalf("ops App Jobs: %+v", jobs)
	}
	if err := validateOps(o); err != nil {
		t.Fatal(err)
	}
	o.OpsGitHubApp.Repositories = nil
	if err := validateOps(o); err == nil {
		t.Fatal("an ops App with no repository list was accepted")
	}

	file, err := parser.ParseFile(token.NewFileSet(), "keeper.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(file.Doc.Text()), " ")
	for _, want := range []string{"Only non-rotating credentials (today, the GitHub App installation tokens) may be Jobs.", "A rotating login must never be one", "--enable-ops-gh-token"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the package doc no longer says %q", want)
		}
	}
	if strings.Contains(doc, "new Jobs on the same loop") {
		t.Error("the package doc again says rotating logins are new Jobs on the same loop")
	}
}
