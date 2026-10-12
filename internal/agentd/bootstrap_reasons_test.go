package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Each refusal of the clean-checkout proof carries one closed-set reason.
func TestProjectIndexProofReasonCodes(t *testing.T) {
	cases := []struct {
		mode, reason string
		readOnly     bool
	}{
		{"untracked", protocol.ReasonUntrackedFiles, true},
		{"assume unchanged", protocol.ReasonUnsupportedTrackedEntry, true},
		{"submodule", protocol.ReasonUnsupportedTrackedEntry, true},
		{"conflict", protocol.ReasonUnsupportedTrackedEntry, true},
		{"dirty", protocol.ReasonWorkingTreeDiffers, true},
		{"mode", protocol.ReasonWorkingTreeDiffers, true},
		{"missing", protocol.ReasonWorkingTreeDiffers, true},
		{"retargeted", protocol.ReasonWorkingTreeDiffers, true},
		{"staged", protocol.ReasonStagedChanges, true},
		{"operation", protocol.ReasonGitOperationPresent, false},
		{"bound", protocol.ReasonIndexBoundExceeded, false},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			g, s, r, dir := symlinkProofRepo(t, "sha1")
			switch c.mode {
			case "untracked":
				writeFile(t, filepath.Join(dir, "new.txt"), "owner\n")
			case "assume unchanged":
				gitRun(t, g.env, dir, "update-index", "--assume-unchanged", "README.md")
			case "submodule":
				head := gitRun(t, g.env, dir, "rev-parse", "HEAD")
				gitRun(t, g.env, dir, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
				gitRun(t, g.env, dir, "commit", "-qm", "submodule")
				if err := os.Mkdir(filepath.Join(dir, "module"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "conflict":
				gitRun(t, g.env, dir, "checkout", "-qb", "other")
				writeFile(t, filepath.Join(dir, "README.md"), "other\n")
				gitRun(t, g.env, dir, "commit", "-qam", "other")
				gitRun(t, g.env, dir, "checkout", "-q", "main")
				writeFile(t, filepath.Join(dir, "README.md"), "main\n")
				gitRun(t, g.env, dir, "commit", "-qam", "main")
				if err := gitCommand(g.env, dir, "merge", "-q", "other").Run(); err == nil {
					t.Fatal("fixture merge did not conflict")
				}
				if err := os.Remove(filepath.Join(dir, ".git", "MERGE_HEAD")); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				writeFile(t, filepath.Join(dir, "README.md"), "owner\n")
			case "mode":
				if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
					t.Fatal(err)
				}
			case "retargeted":
				replaceWithSymlink(t, filepath.Join(dir, "link"), "run.sh")
			case "staged":
				writeFile(t, filepath.Join(dir, "README.md"), "owner\n")
				gitRun(t, g.env, dir, "add", "README.md")
			case "operation":
				writeFile(t, filepath.Join(dir, ".git", "MERGE_HEAD"), "owner operation\n")
			case "bound":
				many := filepath.Join(dir, "many")
				if err := os.Mkdir(many, 0o755); err != nil {
					t.Fatal(err)
				}
				for i := 0; i <= 10000; i++ {
					if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("%05d", i)), nil, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				gitRun(t, g.env, dir, "add", "many")
				gitRun(t, g.env, dir, "commit", "-qm", "many")
			}
			_, err := proveWithin(t, s, r, dir, c.readOnly)
			if got := reasonOf(err, ""); err == nil || got != c.reason {
				t.Fatalf("reason %q, want %q (%v)", got, c.reason, err)
			}
		})
	}
}

func TestPrivateCatalogBootstrapCloneReasonCodes(t *testing.T) {
	noSleep(t)
	for failure, want := range map[string]string{"failed": protocol.ReasonCloneFailed, "partial": protocol.ReasonStagingPreserved} {
		t.Run(failure, func(t *testing.T) {
			_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			r.cloneFailure = failure
			report, err := RefreshPrivateProjects(context.Background(), r, s, source)
			if err == nil || report.State != protocol.BootstrapPartial || len(report.Repositories) != 1 {
				t.Fatalf("failed clone admitted: %v %+v", err, report)
			}
			if got := report.Repositories[0]; got.State != protocol.BootstrapPreserved || got.Step != protocol.StepClone || got.Reason != want {
				t.Fatalf("clone refusal %+v, want %s", got, want)
			}
			if summary := report.summary(); summary != "catalog bootstrap outcome Partial: demo="+want {
				t.Fatalf("summary %q", summary)
			}
		})
	}
}

// The Q-30 canary's shape on the current code: a repository the proof still
// refuses (a submodule) is preserved, and the root naming it fails at that
// repository's anchor. The summary alone says so.
func TestPrivateCatalogBootstrapCanaryShapeReasonSummary(t *testing.T) {
	g, s, r, source := privateRootsFixture(t, projectTestCatalog)
	head := gitRun(t, g.env, g.seed, "rev-parse", "HEAD")
	gitRun(t, g.env, g.seed, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	gitRun(t, g.env, g.seed, "commit", "-qm", "submodule")
	gitRun(t, g.env, g.seed, "push", "-q", "origin", "main")
	report, err := RefreshPrivateProjects(context.Background(), r, s, source)
	if err == nil || report.State != protocol.BootstrapPartial {
		t.Fatalf("submodule admitted: %v %+v", err, report)
	}
	want := "catalog bootstrap outcome Partial: demo=UnsupportedTrackedEntry; project sample=AnchorUnproven@demo(UnsupportedTrackedEntry)"
	if got := report.summary(); got != want {
		t.Fatalf("summary\n got %q\nwant %q", got, want)
	}
	published := report.published()
	if published == nil || published.CatalogRevision == "" || published.Repositories[0] != (protocol.ProjectBootstrapRepository{Name: "demo", State: "Preserved", Step: "Advance", Reason: "UnsupportedTrackedEntry"}) ||
		published.Projects[0] != (protocol.ProjectBootstrapProject{Name: "sample", State: "Failed", Reason: "AnchorUnproven", Repository: "demo", Cause: "UnsupportedTrackedEntry"}) {
		t.Fatalf("published %+v", published)
	}
	// The private report keeps its codes and detail text.
	var saved PrivateProjectReport
	if err := readWorkspaceJSON(s.statePath(privateProjectBootstrapFile), &saved); err != nil || saved.Repositories[0].Reason != "UnsupportedTrackedEntry" || saved.Projects[0].Cause != "UnsupportedTrackedEntry" || saved.Repositories[0].Detail == "" {
		t.Fatalf("private report %v %+v", err, saved)
	}
}

func TestPrivateCatalogBootstrapCatalogReasonCodes(t *testing.T) {
	_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
	unavailable := *source
	unavailable.Read = func(context.Context) (ProjectCatalogResource, error) {
		return ProjectCatalogResource{}, os.ErrNotExist
	}
	report, err := RefreshPrivateProjects(context.Background(), r, s, &unavailable)
	if err == nil || report.State != protocol.BootstrapFailed || report.Reason != protocol.ReasonCatalogUnconfirmed || report.summary() != "catalog bootstrap outcome Failed: CatalogUnconfirmed" {
		t.Fatalf("unavailable catalog: %v %+v %q", err, report, report.summary())
	}
	other := s
	other.ProjectCloneOwner = "someone-else"
	if report, err := RefreshPrivateProjects(context.Background(), r, other, source); err == nil || report.Reason != protocol.ReasonOwnerMismatch {
		t.Fatalf("owner mismatch: %v %+v", err, report)
	}
	invalid := s
	invalid.ProjectCatalog = "elsewhere/catalog"
	if report, err := RefreshPrivateProjects(context.Background(), r, invalid, source); err == nil || report.State != protocol.BootstrapFailed || report.Reason != protocol.ReasonConfigInvalid {
		t.Fatalf("invalid configuration: %v %+v", err, report)
	}
}

var objectID = regexp.MustCompile(`[0-9a-f]{40}`)

// The summary is names and codes only: never a path, an object id or the
// private report's detail text, and never more than its bound.
func TestPrivateBootstrapSummaryPublishesNoPrivateDetail(t *testing.T) {
	secret := "/home/dev/repos/private-thing at 0123456789abcdef0123456789abcdef01234567"
	repo := func(name, reason string) PrivateProjectSource {
		return PrivateProjectSource{Repository: projectcatalog.Repository{Name: name, GitHub: "owner/" + name}, Target: strings.Repeat("a", 64), State: "Preserved", Step: protocol.StepAdvance, Reason: reason, Detail: secret}
	}
	report := PrivateProjectReport{State: "Partial", CatalogRevision: strings.Repeat("b", 64), Failure: secret}
	for i, reason := range protocol.RepositoryReasons {
		report.Repositories = append(report.Repositories, repo(fmt.Sprintf("repo-%02d", i), reason))
	}
	for i, reason := range protocol.RootReasons {
		report.Projects = append(report.Projects, PrivateProjectRootReport{Project: fmt.Sprintf("project-%02d", i), State: "Failed", Head: strings.Repeat("c", 40), Reason: reason, Repository: "repo-00", Cause: protocol.ReasonWorkingTreeDiffers, Detail: secret})
	}
	check := func(summary string) {
		t.Helper()
		if len(summary) > summaryLimit || strings.ContainsAny(summary, "/\n") || objectID.MatchString(summary) || strings.Contains(summary, "private-thing") || strings.Contains(summary, "owner") {
			t.Fatalf("summary leaks or overflows (%d bytes): %q", len(summary), summary)
		}
	}
	full := report.summary()
	check(full)
	if !strings.HasPrefix(full, "catalog bootstrap outcome Partial: repo-00=CloneFailed; ") || !regexp.MustCompile(` \+\d+ more$`).MatchString(full) {
		t.Fatalf("overflow is not counted: %q", full)
	}
	// Every count of items stays bounded and says how many it left out.
	for n := 0; n <= len(report.Repositories); n++ {
		partial := report
		partial.Repositories, partial.Projects = report.Repositories[:n], nil
		summary := partial.summary()
		check(summary)
		shown := strings.Count(summary, "=")
		if m := regexp.MustCompile(` \+(\d+) more$`).FindStringSubmatch(summary); m != nil {
			var more int
			_, _ = fmt.Sscan(m[1], &more)
			shown += more
		}
		if shown != n {
			t.Fatalf("%d items, summary accounts for %d: %q", n, shown, summary)
		}
	}
	failed := PrivateProjectReport{State: "Failed", Reason: protocol.ReasonTimedOut, Failure: secret, Repositories: []PrivateProjectSource{repo("a", protocol.ReasonFetchFailed)}}
	if got := failed.summary(); got != "catalog bootstrap outcome Failed: TimedOut; a=FetchFailed" {
		t.Fatalf("failed summary %q", got)
	}
	ready := PrivateProjectReport{State: "Ready", Repositories: []PrivateProjectSource{{Repository: projectcatalog.Repository{Name: "a"}, State: "Ready", Detail: secret}}}
	if got := ready.summary(); got != "catalog bootstrap outcome Ready" {
		t.Fatalf("ready summary %q", got)
	}
	// The published form carries the same names and codes, and no detail.
	data, err := json.Marshal(report.published())
	if err != nil || strings.Contains(string(data), "private-thing") || strings.Contains(string(data), "owner/") {
		t.Fatalf("published form leaks: %v %s", err, data)
	}
	for _, leak := range []string{strings.Repeat("a", 64), strings.Repeat("c", 40), "0123456789abcdef"} {
		if strings.Contains(string(data), leak) {
			t.Fatalf("published form carries an object id: %s", data)
		}
	}
	if (PrivateProjectReport{State: "Disabled"}).published() != nil {
		t.Fatal("a disabled bootstrap was published")
	}
}
