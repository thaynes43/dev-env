package agentd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const privateProjectBootstrapFile = "project-bootstrap.json"

// PrivateProjectSource records an observed fetch, separately from the source
// pin a new task must obtain immediately before creating its own worktree.
type PrivateProjectSource struct {
	Repository projectcatalog.Repository `json:"repository"`
	Target     string                    `json:"target,omitempty"`
	FetchedAt  time.Time                 `json:"fetchedAt,omitempty"`
	State      string                    `json:"state"`
	Detail     string                    `json:"detail"`
}

type PrivateProjectReport struct {
	Version                int                        `json:"version"`
	State                  string                     `json:"state"`
	CatalogNamespace       string                     `json:"catalogNamespace,omitempty"`
	CatalogName            string                     `json:"catalogName,omitempty"`
	CatalogUID             string                     `json:"catalogUID,omitempty"`
	CatalogResourceVersion string                     `json:"catalogResourceVersion,omitempty"`
	CatalogRevision        string                     `json:"catalogRevision,omitempty"`
	StartedAt              time.Time                  `json:"startedAt"`
	FinishedAt             time.Time                  `json:"finishedAt"`
	Repositories           []PrivateProjectSource     `json:"repositories,omitempty"`
	Projects               []PrivateProjectRootReport `json:"projects,omitempty"`
	Failure                string                     `json:"failure,omitempty"`
}

var errPrivateProjectBootstrap = errors.New("private project bootstrap is incomplete; inspect its private report")

// ValidatePrivateProjectBootstrap checks pod-owned configuration without
// reading a catalog, token or filesystem. It does not enable task admission.
func ValidatePrivateProjectBootstrap(s Settings) error {
	if s.PrivateProjectRoots && !s.PrivateProjectBootstrap {
		return errors.New("private project roots require trusted private catalog bootstrap")
	}
	if !s.PrivateProjectBootstrap {
		return nil
	}
	if s.WorkspaceID != "" || s.ProjectCatalog != "dev-env-system/dev-env-project-catalog" || !projectcatalog.ValidName(s.ProjectCloneOwner) {
		return errors.New("private project bootstrap requires a private home, the fixed accepted catalog and an explicit clone owner")
	}
	return nil
}

// RefreshPrivateProjects is used at startup and by explicit refresh. It has
// no shared mount, marker, writer or administrative-lock dependency. The only
// serialization is a nonblocking lock inside this pod's private state.
func RefreshPrivateProjects(ctx context.Context, r Runner, s Settings, source *ProjectCatalogSource) (report PrivateProjectReport, resultErr error) {
	report = PrivateProjectReport{Version: 1, State: "Disabled"}
	if err := ValidatePrivateProjectBootstrap(s); err != nil {
		return report, err
	}
	if !s.PrivateProjectBootstrap {
		return report, nil
	}
	ctx, cancel := context.WithTimeout(ctx, projectSyncBudget)
	defer cancel()
	report.State, report.StartedAt = "Failed", time.Now().UTC()
	if s.StateDir != filepath.Join(s.Home, ".agentd") || noSymlinkComponents(s.Home) != nil {
		return report, errors.New("private bootstrap state or home is uncertain")
	}
	if err := ensureWorkspaceDirectory(s.StateDir); err != nil {
		return report, errors.New("private bootstrap state is unavailable")
	}
	fi, err := os.Stat(s.StateDir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		return report, errors.New("private bootstrap state must have mode 0700")
	}
	lock, err := os.OpenFile(s.statePath("project-bootstrap.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return report, errors.New("private bootstrap lock is unavailable")
	}
	defer func() { _ = lock.Close() }()
	if fi, err := lock.Stat(); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return report, errors.New("private bootstrap is already running or its lock is uncertain")
	}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if writeWorkspaceJSON(s.statePath(privateProjectBootstrapFile), report) != nil {
			report.State, report.Failure = "Failed", "private bootstrap report retention is unconfirmed"
			resultErr = errors.New("private project bootstrap report could not be retained")
		}
	}()
	// Supersede an earlier Ready report before doing any Git work. An abrupt
	// process exit leaves Started, never evidence that this invocation passed.
	started := report
	started.State = "Started"
	if writeWorkspaceJSON(s.statePath(privateProjectBootstrapFile), started) != nil {
		report.Failure = "private bootstrap report cannot be retained; repositories preserved"
		return report, errPrivateProjectBootstrap
	}
	if source == nil || source.Namespace+"/"+source.Name != s.ProjectCatalog || source.Read == nil {
		report.Failure = "trusted accepted catalog source is missing or mismatched"
		return report, errPrivateProjectBootstrap
	}
	// Capture once, then confirm the exact UID/RV/bytes before each repository
	// mutation and final Ready publication. No caller snapshot has authority.
	reader := &projectCatalogAuthority{source: *source}
	accepted, err := reader.read(ctx)
	if err != nil {
		report.Failure = "accepted catalog read is unconfirmed"
		return report, errPrivateProjectBootstrap
	}
	catalog, err := projectcatalog.Parse(accepted.Data)
	if err != nil {
		report.Failure = "accepted catalog is invalid"
		return report, errPrivateProjectBootstrap
	}
	authority, err := captureProjectCatalogAuthority(ctx, source, catalog)
	if err != nil || authority.accepted.UID != accepted.UID || authority.accepted.ResourceVersion != accepted.ResourceVersion {
		report.Failure = "accepted catalog changed during capture"
		return report, errPrivateProjectBootstrap
	}
	report.CatalogNamespace, report.CatalogName = accepted.Namespace, accepted.Name
	report.CatalogUID, report.CatalogResourceVersion = accepted.UID, accepted.ResourceVersion
	report.CatalogRevision = catalog.Revision()
	for _, repo := range catalog.Repositories() {
		owner, _, _ := strings.Cut(repo.GitHub, "/")
		if !strings.EqualFold(owner, s.ProjectCloneOwner) {
			report.Failure = "catalog repository is outside the configured clone owner"
			return report, errPrivateProjectBootstrap
		}
	}
	if err := ensureWorkspaceDirectory(s.ReposDir()); err != nil {
		report.Failure = "private repository directory is uncertain"
		return report, errPrivateProjectBootstrap
	}
	s.privateProjectGit = true
	for _, repo := range catalog.Repositories() {
		if authority.confirm(ctx) != nil {
			report.Failure = "accepted catalog changed or became unconfirmed; remaining repositories preserved"
			return report, errPrivateProjectBootstrap
		}
		bounded, stop := context.WithTimeout(ctx, sharedGitPrepareBudget)
		observed := refreshPrivateProjectReference(bounded, r, s, repo, authority)
		stop()
		report.Repositories = append(report.Repositories, observed)
		if ctx.Err() != nil {
			report.Failure = "catalog bootstrap time budget ended; remaining repositories preserved"
			return report, errPrivateProjectBootstrap
		}
	}
	if s.PrivateProjectRoots {
		for _, project := range catalog.ProjectNames() {
			bounded, stop := context.WithTimeout(ctx, sharedGitPrepareBudget)
			snapshot, _ := catalog.ProjectSnapshot(project)
			observed := refreshPrivateProjectRoot(bounded, r, s, snapshot, authority)
			stop()
			report.Projects = append(report.Projects, observed)
			if ctx.Err() != nil {
				report.Failure = "project root time budget ended; remaining projects preserved"
				return report, errPrivateProjectBootstrap
			}
		}
	}
	if authority.confirm(ctx) != nil {
		report.Failure = "accepted catalog changed or became unconfirmed before report publication"
		return report, errPrivateProjectBootstrap
	}
	report.State = "Ready"
	for _, repo := range report.Repositories {
		if repo.State != "Ready" {
			report.State = "Partial"
			return report, errPrivateProjectBootstrap
		}
	}
	for _, project := range report.Projects {
		if project.State != "Ready" {
			report.State = "Partial"
			return report, errPrivateProjectBootstrap
		}
	}
	return report, nil
}

func refreshPrivateProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, authority *projectCatalogAuthority) PrivateProjectSource {
	observed := PrivateProjectSource{Repository: repo, State: "Preserved", Detail: "reference identity or Git operation is uncertain; preserved"}
	clone := s.ClonePath(repo.Name)
	if _, err := os.Lstat(clone); errors.Is(err, os.ErrNotExist) {
		if err := cloneProjectReference(ctx, r, s, repo, authority.confirm); err != nil {
			observed.Detail = "reference clone failed or is uncertain; existing staging and destination preserved"
			return observed
		}
	} else if err != nil {
		return observed
	}
	if validateProjectReference(ctx, r, s, repo) != nil || projectGitOperationClear(ctx, r, s, clone) != nil {
		return observed
	}
	if authority.confirm(ctx) != nil {
		observed.Detail = "accepted catalog changed or became unconfirmed before fetch"
		return observed
	}
	target, fetched, err := fetchProjectTarget(ctx, r, s, repo)
	observed.Target, observed.FetchedAt = target, fetched
	if err != nil {
		observed.Detail = "fresh reference fetch failed; reference view preserved"
		return observed
	}
	if advancePrivateProjectReference(ctx, r, s, repo, target, authority) != nil {
		observed.Detail = "fresh source fetched; dirty, local-only, nondefault or uncertain reference view preserved"
		return observed
	}
	observed.State, observed.Detail = "Ready", "fresh default reference verified"
	return observed
}

func advancePrivateProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, target string, authority *projectCatalogAuthority) error {
	clone := s.ClonePath(repo.Name)
	prove := func() (string, error) {
		if err := validateProjectReference(ctx, r, s, repo); err != nil {
			return "", err
		}
		if _, err := projectIndexProof(ctx, r, s, clone, true); err != nil {
			return "", err
		}
		branch, err := projectGit(ctx, r, s, clone, "symbolic-ref", "--quiet", "HEAD")
		if err != nil || branch != "refs/heads/"+repo.DefaultBranch {
			return "", errors.New("reference is detached or on a nondefault branch")
		}
		worktrees, err := projectGit(ctx, r, s, clone, "worktree", "list", "--porcelain")
		if err != nil {
			return "", err
		}
		var path string
		for _, line := range strings.Split(worktrees, "\n") {
			if strings.HasPrefix(line, "worktree ") {
				path = strings.TrimPrefix(line, "worktree ")
			}
			if line == "branch refs/heads/"+repo.DefaultBranch && path != clone {
				return "", errors.New("default branch belongs to another worktree; preserved")
			}
		}
		head, err := projectGit(ctx, r, s, clone, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return "", err
		}
		if _, err := projectGit(ctx, r, s, clone, "merge-base", "--is-ancestor", head, target); err != nil {
			return "", errors.New("reference has local-only commits or uncertain ancestry")
		}
		return head, nil
	}
	head, err := prove()
	if err != nil {
		return err
	}
	if head == target {
		return nil
	}
	if err := authority.confirm(ctx); err != nil {
		return err
	}
	again, err := prove()
	if err != nil || again != head {
		return errors.New("reference changed during clean-view proof")
	}
	// Git's own locks and checkout safety still apply. Never force a checkout,
	// reset a branch, stash WIP or run hooks to make a reference look fresh.
	_, err = projectGit(ctx, r, s, clone, "-c", "merge.autostash=false", "merge", "--ff-only", "--no-edit", "--no-autostash", target)
	return err
}
