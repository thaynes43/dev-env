package agentd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
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

// hookRunner answers the commands hook claims and runs the rest.
type hookRunner struct {
	Runner
	hook func(ctx context.Context, c Cmd) (handled bool, err error)
}

func (r *hookRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if handled, err := r.hook(ctx, c); handled {
		return Result{ExitCode: ExitCodeOf(err)}, err
	}
	return r.Runner.Run(ctx, c)
}

func isAncestryCheck(c Cmd, dir string) bool {
	return len(c.Args) > 1 && c.Args[1] == dir && slices.Contains(c.Args, "merge-base") && slices.Contains(c.Args, "--is-ancestor")
}

// endingContext is a parent context the test ends on demand, the way the
// operation budget's deadline or agentd's SIGTERM ends the real one.
type endingContext struct {
	context.Context
	once sync.Once
	done chan struct{}
	err  error
}

func newEndingContext() *endingContext {
	return &endingContext{Context: context.Background(), done: make(chan struct{})}
}

func (c *endingContext) Done() <-chan struct{} { return c.done }

func (c *endingContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

func (c *endingContext) end(err error) {
	c.once.Do(func() {
		c.err = err
		close(c.done)
	})
}

// A refresh that a passed deadline ends reports TimedOut at the item and the
// catalog; one that agentd's stop (a cancelled context) ends reports Stopped.
// Neither is mistaken for the other, and the step stays where it stopped.
func TestPrivateCatalogBootstrapReportsWhyItsContextEnded(t *testing.T) {
	isFetch := func(c Cmd) bool { return slices.Contains(c.Args, "fetch") }
	isAnchorAdd := func(c Cmd) bool { return slices.Contains(c.Args, "worktree") && slices.Contains(c.Args, "add") }
	cases := []struct {
		name  string
		end   error
		roots bool
		at    func(Cmd) bool
		want  string
	}{
		{"repository fetch past the deadline", context.DeadlineExceeded, false, isFetch, protocol.ReasonTimedOut},
		{"repository fetch when agentd stops", context.Canceled, false, isFetch, protocol.ReasonStopped},
		{"anchor add past the deadline", context.DeadlineExceeded, true, isAnchorAdd, protocol.ReasonTimedOut},
		{"anchor add when agentd stops", context.Canceled, true, isAnchorAdd, protocol.ReasonStopped},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
			s.PrivateProjectRoots = c.roots
			parent := newEndingContext()
			runner := &hookRunner{Runner: r, hook: func(ctx context.Context, cmd Cmd) (bool, error) {
				if !c.at(cmd) {
					return false, nil
				}
				// The command is in flight when its context ends, and is
				// killed once the end reaches it.
				parent.end(c.end)
				<-ctx.Done()
				return true, &CmdError{Name: "git", Sub: "killed", ExitCode: -1}
			}}
			report, err := RefreshPrivateProjects(parent, runner, s, source)
			if err == nil || report.State != protocol.BootstrapFailed || report.Reason != c.want || len(report.Repositories) != 1 {
				t.Fatalf("ended bootstrap reported as %v %+v", err, report)
			}
			wantSummary := "catalog bootstrap outcome Failed: " + c.want + "; demo=" + c.want
			repo := report.Repositories[0]
			if c.roots {
				wantSummary = "catalog bootstrap outcome Failed: " + c.want + "; project sample=" + c.want + "@demo"
				if repo.State != protocol.BootstrapReady || len(report.Projects) != 1 {
					t.Fatalf("repository before the root %+v, projects %+v", repo, report.Projects)
				}
				if got := report.Projects[0]; got.State != protocol.BootstrapFailed || got.Reason != c.want || got.Repository != "demo" || got.Cause != "" {
					t.Fatalf("ended root reported as %s@%s(%s)", got.Reason, got.Repository, got.Cause)
				}
			} else if repo.State != protocol.BootstrapPreserved || repo.Step != protocol.StepFetch || repo.Reason != c.want {
				t.Fatalf("ended repository reported as %+v", repo)
			}
			if got := report.summary(); got != wantSummary {
				t.Fatalf("summary\n got %q\nwant %q", got, wantSummary)
			}
			stopped := strings.HasPrefix(report.Failure, "agentd stopped during ")
			if stopped != (c.want == protocol.ReasonStopped) || stopped == strings.Contains(report.Failure, "time budget ended") {
				t.Fatalf("private failure text %q for %s", report.Failure, c.want)
			}
		})
	}
}

// Only Git's exit 1 answers an ancestry check "no". Any other failure, such
// as exit 128 for a missing object, is no answer: Uncertain for a reference,
// and AnchorUnproven with cause Uncertain for a root's anchor.
func TestPrivateBootstrapAncestryCodesNeedGitsAnswer(t *testing.T) {
	t.Run("reference", func(t *testing.T) {
		_, s, r, source := privateBootstrapFixture(t, projectTestCatalog)
		runner := &hookRunner{Runner: r, hook: func(_ context.Context, c Cmd) (bool, error) {
			if !isAncestryCheck(c, s.ClonePath("demo")) {
				return false, nil
			}
			return true, &CmdError{Name: "git", Sub: "merge-base", ExitCode: 128}
		}}
		report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
		if err == nil || report.State != protocol.BootstrapPartial {
			t.Fatalf("an unanswered ancestry check was admitted: %v %+v", err, report)
		}
		if got := report.Repositories[0]; got.State != protocol.BootstrapPreserved || got.Step != protocol.StepAdvance || got.Reason != protocol.ReasonUncertain {
			t.Fatalf("unanswered ancestry reported as %+v", got)
		}
	})
	// The root asks twice per moved anchor: before the first change, from
	// the recorded commit, and again before the fast-forward, from the
	// checked-out commit.
	for _, c := range []struct {
		name   string
		call   int
		exit   int
		reason string
		cause  string
	}{
		{"recorded commit, no answer", 1, 128, protocol.ReasonAnchorUnproven, protocol.ReasonUncertain},
		{"checked-out commit, no answer", 2, 128, protocol.ReasonAnchorUnproven, protocol.ReasonUncertain},
		{"checked-out commit, answered no", 2, 1, protocol.ReasonAnchorNotFastForward, ""},
	} {
		t.Run("anchor "+c.name, func(t *testing.T) {
			g, s, r, source := privateRootsFixture(t, projectTestCatalog)
			requirePrivateRootReady(t, s, r, source)
			anchor := filepath.Join(privateRootPath(s), "demo")
			pushBootstrapChange(t, g)
			calls := 0
			runner := &hookRunner{Runner: r, hook: func(_ context.Context, cmd Cmd) (bool, error) {
				if !isAncestryCheck(cmd, anchor) {
					return false, nil
				}
				if calls++; calls != c.call {
					return false, nil
				}
				return true, &CmdError{Name: "git", Sub: "merge-base", ExitCode: c.exit}
			}}
			report, err := RefreshPrivateProjects(context.Background(), runner, s, source)
			if err == nil || report.State != protocol.BootstrapPartial || report.Repositories[0].State != protocol.BootstrapReady || calls < c.call {
				t.Fatalf("anchor ancestry check %d not refused: %v %+v", c.call, err, report)
			}
			if got := report.Projects[0]; got.State != protocol.BootstrapFailed || got.Reason != c.reason || got.Repository != "demo" || got.Cause != c.cause {
				t.Fatalf("anchor ancestry reported as %s@%s(%s), want %s@demo(%s)", got.Reason, got.Repository, got.Cause, c.reason, c.cause)
			}
		})
	}
}

// With more than one removed anchor, the report names the first by name, so
// the same state on disk always gives the same status and summary.
func TestPrivateRootsNameTheFirstRemovedAnchor(t *testing.T) {
	_, s, r, source := privateRootsFixture(t, twoRepositoryRootDeclaration())
	requirePrivateRootReady(t, s, r, source)
	changed := strings.Replace(twoRepositoryRootDeclaration(), `"github":"fixture/extra"}},`, `"github":"fixture/extra"},"other":{"github":"fixture/other"}},`, 1)
	changed = strings.Replace(changed, `[{"name":"demo"},{"name":"extra"}]`, `[{"name":"other"}]`, 1)
	if changed == twoRepositoryRootDeclaration() || !strings.Contains(changed, `[{"name":"other"}]`) {
		t.Fatal("fixture catalog did not change")
	}
	source = projectSyncFixtureOptions(changed).AcceptedCatalog
	// Map order would pick either anchor; six runs make a lucky pass 1 in 64.
	for run := 0; run < 6; run++ {
		report, err := RefreshPrivateProjects(context.Background(), r, s, source)
		if err == nil || len(report.Projects) != 1 {
			t.Fatalf("run %d: removed anchors admitted: %v %+v", run, err, report)
		}
		if got := report.Projects[0]; got.Reason != protocol.ReasonAnchorRemoved || got.Repository != "demo" {
			t.Fatalf("run %d: removed anchors reported as %s@%s, want AnchorRemoved@demo", run, got.Reason, got.Repository)
		}
	}
}
