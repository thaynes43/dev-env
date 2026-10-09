package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// ProjectSyncOptions is intentionally not read from environment or templates.
// There are no production call sites until management authorization, storage
// acceptance and provider acceptance are separately wired and proved.
type ProjectSyncOptions struct {
	Enabled         bool
	AcceptedCatalog *ProjectCatalogSource
}

const projectSyncBudget = 10 * time.Minute

type ProjectFinding struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Detail string `json:"detail"`
}
type ProjectSyncReport struct {
	CatalogRevision string           `json:"catalogRevision"`
	Findings        []ProjectFinding `json:"findings"`
}

// SyncProjects is the common primitive for future boot/daily/explicit sync.
// It never prunes worktree registrations, branches, projects or staging data.
// The caller must use one server-resolved accepted catalog for the invocation.
func SyncProjects(ctx context.Context, r Runner, s Settings, catalog *projectcatalog.Catalog, options ProjectSyncOptions) (ProjectSyncReport, error) {
	var report ProjectSyncReport
	if !options.Enabled {
		return report, errors.New("project catalog synchronization is disabled")
	}
	if catalog == nil || catalog.Revision() == "" {
		return report, errors.New("no accepted project catalog")
	}
	if err := workspaceStoragePreflight(s); err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(ctx, projectSyncBudget)
	defer cancel()
	authority, err := captureProjectCatalogAuthority(ctx, options.AcceptedCatalog, catalog)
	if err != nil {
		return report, err
	}
	report.CatalogRevision = catalog.Revision()
	add := func(path, state, detail string) {
		report.Findings = append(report.Findings, ProjectFinding{path, state, detail})
	}
	entries, err := os.ReadDir(filepath.Join(s.Home, "codex"))
	if err != nil {
		return report, err
	}
	names := catalog.ProjectNames()
	global := map[string]projectcatalog.Repository{}
	for _, repo := range catalog.Repositories() {
		global[repo.Name] = repo
	}
	for _, entry := range entries {
		if !slices.Contains(names, entry.Name()) {
			add(filepath.Join(s.Home, "codex", entry.Name()), "undeclared", "preserved")
		}
	}
	for _, name := range names {
		// Selected repository is irrelevant to materialization, but immutable
		// snapshots always require it; get the project's first declared repo.
		snapshot, err := catalog.ProjectSnapshot(name)
		if err != nil {
			return report, err
		}
		findings, err := syncProject(ctx, r, s, catalog, snapshot, global, authority)
		report.Findings = append(report.Findings, findings...)
		if err != nil {
			add(filepath.Join(s.Home, "codex", name), "preserved", err.Error())
		}
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
	}
	return report, nil
}

func withProjectAdmin(ctx context.Context, s Settings, repo string, run func(context.Context) error) error {
	unlock, err := workspaceAdminLock(ctx, s, repo)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, sharedGitPrepareBudget)
	defer cancel()
	if err := workspaceStoragePreflight(s); err != nil {
		return err
	}
	return run(ctx)
}

func syncProject(ctx context.Context, r Runner, s Settings, catalog *projectcatalog.Catalog, snapshot projectcatalog.Snapshot, global map[string]projectcatalog.Repository, authority *projectCatalogAuthority) ([]ProjectFinding, error) {
	repos := snapshot.Repositories()
	primary := repos[0].Name
	root := filepath.Join(s.Home, "codex", snapshot.Project())
	if err := withProjectAdmin(ctx, s, primary, func(context.Context) error { return ensurePlainProjectRoot(root) }); err != nil {
		return nil, err
	}
	var findings []ProjectFinding
	ready := 0
	for _, repo := range repos {
		clone := s.ClonePath(repo.Name)
		err := withProjectAdmin(ctx, s, repo.Name, func(ctx context.Context) error {
			if err := ensurePlainProjectRoot(root); err != nil {
				return err
			}
			canonical := global[repo.Name]
			target, fetched, err := syncProjectReference(ctx, r, s, canonical, &findings)
			if err != nil {
				return err
			}
			findings = append(findings, ProjectFinding{clone, "healthy", "fresh target " + target})
			if repo.DefaultBranch != canonical.DefaultBranch {
				target, fetched, err = fetchProjectTarget(ctx, r, s, repo)
				if err != nil {
					return err
				}
			}
			anchor := filepath.Join(root, repo.Name)
			if err := syncProjectAnchor(ctx, r, s, repo, anchor, target); err != nil {
				findings = append(findings, ProjectFinding{anchor, "preserved", err.Error()})
				return nil
			}
			ready++
			findings = append(findings, ProjectFinding{anchor, "ready", "detached at " + target + " fetched " + fetched.Format(time.RFC3339Nano)})
			return nil
		})
		if err != nil {
			state := "preserved"
			var staging *projectStagingPreserved
			if errors.As(err, &staging) {
				state = "staging-preserved"
			}
			findings = append(findings, ProjectFinding{clone, state, err.Error()})
		}
		if ctx.Err() != nil {
			return findings, ctx.Err()
		}
	}
	// Publication is serialized by the deterministic primary's existing Git
	// lock. A future server must serialize accepted catalog replacement across
	// this operation; no client revision or new filesystem lock has authority.
	err := withProjectAdmin(ctx, s, primary, func(ctx context.Context) error {
		if err := ensurePlainProjectRoot(root); err != nil {
			return err
		}
		accepted, err := catalog.ProjectSnapshot(snapshot.Project())
		if err != nil {
			return err
		}
		before, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		now, err := json.Marshal(accepted)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, now) {
			return errors.New("accepted project snapshot changed before publication")
		}
		declared := []string{"AGENTS.md", "CLAUDE.md"}
		for _, repo := range repos {
			declared = append(declared, repo.Name)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !slices.Contains(declared, entry.Name()) {
				findings = append(findings, ProjectFinding{filepath.Join(root, entry.Name()), "undeclared", "preserved"})
			}
		}
		if err := authority.confirm(ctx); err != nil {
			return err
		}
		if err := writeProjectWrappers(s, accepted, root); err != nil {
			return err
		}
		state := "partial"
		if ready == len(repos) {
			state = "ready"
		}
		findings = append(findings, ProjectFinding{root, state, fmt.Sprintf("%d of %d repository anchors ready at catalog %s", ready, len(repos), accepted.CatalogRevision())})
		return nil
	})
	return findings, err
}

func ensurePlainProjectRoot(root string) error {
	if err := noSymlinkComponents(filepath.Dir(root)); err != nil {
		return err
	}
	if err := os.Mkdir(root, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := noSymlinkComponents(root); err != nil {
		return err
	}
	fi, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return errors.New("project root is not a plain directory")
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("project root has Git administration; preserved")
	}
	return nil
}

func projectGit(ctx context.Context, r Runner, s Settings, dir string, args ...string) (string, error) {
	// Administrative checkouts do not execute a clone's local hooks or fsmonitor.
	full := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.fsync=reference", "-c", "core.fsyncMethod=fsync", "-c", "core.useReplaceRefs=false"}, args...)
	return s.git(ctx, r, dir, full...)
}

func validateProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository) error {
	clone := s.ClonePath(repo.Name)
	if err := noSymlinkComponents(filepath.Join(clone, ".git")); err != nil {
		return err
	}
	common, err := projectGit(ctx, r, s, clone, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(common) != filepath.Join(clone, ".git") {
		return errors.New("reference has unexpected common Git administration")
	}
	top, err := projectGit(ctx, r, s, clone, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != clone {
		return errors.New("reference has unexpected root")
	}
	urls, err := projectGit(ctx, r, s, clone, "remote", "get-url", "--all", "origin")
	if err != nil || urls != repo.URL() {
		return errors.New("reference identity does not match the catalog")
	}
	pushURLs, err := projectGit(ctx, r, s, clone, "remote", "get-url", "--push", "--all", "origin")
	if err != nil || pushURLs != repo.URL() {
		return errors.New("reference push identity does not match the catalog")
	}
	return nil
}

type projectStagingPreserved struct{ path, reason string }

func (e *projectStagingPreserved) Error() string {
	return e.reason + "; staging preserved at " + e.path + "; inspect it before another clone attempt"
}

func cloneProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository) error {
	clone := s.ClonePath(repo.Name)
	tmp := filepath.Join(s.ReposDir(), "."+repo.Name+".agentd-clone")
	for attempt := 0; attempt < cloneAttempts; attempt++ {
		// Never delete an earlier or failed partial clone, even on retry.
		if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
			return &projectStagingPreserved{tmp, "reference clone staging exists or is uncertain"}
		}
		if _, err := os.Lstat(clone); !errors.Is(err, os.ErrNotExist) {
			return errors.New("reference destination exists or is uncertain; preserved")
		}
		_, err := r.Run(ctx, Cmd{Name: "git", Args: []string{"-c", "core.hooksPath=/dev/null", "clone", "--filter=blob:none", "--quiet", "--branch", repo.DefaultBranch, repo.URL(), tmp}, Env: gitEnv})
		if err == nil {
			if err := noSymlinkComponents(filepath.Join(tmp, ".git")); err != nil {
				return &projectStagingPreserved{tmp, "successful clone has uncertain Git administration"}
			}
			if err := configureSharedGitPolicy(ctx, r, s, tmp); err != nil {
				return &projectStagingPreserved{tmp, "clone configuration cannot be safely provisioned"}
			}
			if _, err := os.Lstat(clone); !errors.Is(err, os.ErrNotExist) {
				return &projectStagingPreserved{tmp, "reference destination appeared"}
			}
			if err := os.Rename(tmp, clone); err != nil {
				return &projectStagingPreserved{tmp, "reference publication failed"}
			}
			return nil
		}
		if _, stagingErr := os.Lstat(tmp); !errors.Is(stagingErr, os.ErrNotExist) {
			return &projectStagingPreserved{tmp, "clone failed"}
		}
		if attempt == cloneAttempts-1 {
			return errors.New("bounded reference clone attempts failed")
		}
		if err := sleepFunc(ctx, cloneBackoff[min(attempt, len(cloneBackoff)-1)]); err != nil {
			return err
		}
	}
	return errors.New("bounded reference clone attempts exhausted")
}

func syncProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, findings *[]ProjectFinding) (string, time.Time, error) {
	clone := s.ClonePath(repo.Name)
	if _, err := os.Lstat(clone); errors.Is(err, os.ErrNotExist) {
		if err := cloneProjectReference(ctx, r, s, repo); err != nil {
			return "", time.Time{}, err
		}
	} else if err != nil {
		return "", time.Time{}, err
	}
	if err := validateProjectReference(ctx, r, s, repo); err != nil {
		return "", time.Time{}, err
	}
	if err := configureSharedGitPolicy(ctx, r, s, clone); err != nil {
		return "", time.Time{}, err
	}
	target, fetched, err := fetchProjectTarget(ctx, r, s, repo)
	if err != nil {
		return "", fetched, err
	}
	if err := repairProjectReference(ctx, r, s, repo, target, fetched, findings); err != nil {
		return "", fetched, err
	}
	return target, fetched, nil
}

func fetchProjectTarget(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository) (string, time.Time, error) {
	ref := "refs/remotes/origin/" + repo.DefaultBranch
	if _, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "fetch", "--no-auto-maintenance", "--no-prune", "--no-recurse-submodules", "--no-tags", "origin", "+refs/heads/"+repo.DefaultBranch+":"+ref); err != nil {
		return "", time.Time{}, errors.New("fresh reference fetch failed; preserved")
	}
	fetched := time.Now().UTC()
	target, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", fetched, errors.New("fresh reference target is unavailable")
	}
	return target, fetched, nil
}

type projectGitState struct{ head, branch, indexTree, indexCommit, defaultHead string }

func projectGitOperationClear(ctx context.Context, r Runner, s Settings, dir string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "REBASE_HEAD", "BISECT_LOG", "rebase-merge", "rebase-apply", "sequencer", "index.lock", "HEAD.lock", "config.lock", "packed-refs.lock", "shallow.lock"} {
		path, err := projectGit(ctx, r, s, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return errors.New("git operation or lock is present or uncertain; preserved")
		}
	}
	return nil
}

// projectIndexProof hashes every tracked file through Git's index filters. It
// does not trust cached stat data, assume-unchanged or sparse-checkout bits.
func projectIndexProof(ctx context.Context, r Runner, s Settings, dir string) (string, error) {
	if err := projectGitOperationClear(ctx, r, s, dir); err != nil {
		return "", err
	}
	untracked, err := projectGit(ctx, r, s, dir, "ls-files", "--others")
	if err != nil || untracked != "" {
		return "", errors.New("untracked files (including ignored files) or uncertain scan; preserved")
	}
	flags, err := projectGit(ctx, r, s, dir, "ls-files", "-v")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(flags, "\n") {
		if line != "" && !strings.HasPrefix(line, "H ") {
			return "", errors.New("nonordinary tracked-file flags; preserved")
		}
	}
	staged, err := projectGit(ctx, r, s, dir, "-c", "core.quotePath=true", "ls-files", "--stage")
	if err != nil {
		return "", err
	}
	var paths strings.Builder
	var expected []string
	for _, line := range strings.Split(staged, "\n") {
		if line == "" {
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || fields[2] != "0" || (fields[0] != "100644" && fields[0] != "100755") {
			return "", errors.New("conflicts, submodules or special tracked files; preserved")
		}
		actualPath := path
		if strings.HasPrefix(path, "\"") {
			actualPath, err = strconv.Unquote(path)
			if err != nil {
				return "", errors.New("tracked filename cannot be proved; preserved")
			}
		}
		fullPath := filepath.Join(dir, actualPath)
		if err := noSymlinkComponents(fullPath); err != nil {
			return "", errors.New("tracked path is missing or has symlink components; preserved")
		}
		fi, err := os.Lstat(fullPath)
		if err != nil || !fi.Mode().IsRegular() || (fi.Mode().Perm()&0o100 != 0) != (fields[0] == "100755") {
			return "", errors.New("tracked file mode differs from index; preserved")
		}
		paths.WriteString(path)
		paths.WriteByte('\n')
		expected = append(expected, fields[1])
		if len(expected) > 10000 {
			return "", errors.New("reference index exceeds the repair proof bound")
		}
	}
	result, err := r.Run(ctx, Cmd{Name: "git", Args: []string{"-C", dir, "-c", "core.fsmonitor=false", "hash-object", "--stdin-paths"}, Env: gitEnv, Stdin: strings.NewReader(paths.String())})
	if err != nil || !slices.Equal(strings.Fields(string(result.Stdout)), expected) {
		return "", errors.New("tracked files differ from the index; preserved")
	}
	return projectGit(ctx, r, s, dir, "write-tree")
}

func inspectProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, target string) (projectGitState, error) {
	var state projectGitState
	clone := s.ClonePath(repo.Name)
	if err := validateProjectReference(ctx, r, s, repo); err != nil {
		return state, err
	}
	tree, err := projectIndexProof(ctx, r, s, clone)
	if err != nil {
		return state, err
	}
	state.indexTree = tree
	state.head, err = projectGit(ctx, r, s, clone, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return state, err
	}
	state.branch, err = projectGit(ctx, r, s, clone, "symbolic-ref", "--quiet", "HEAD")
	if err != nil && ExitCodeOf(err) != 1 {
		return state, err
	}
	state.defaultHead, err = projectGit(ctx, r, s, clone, "rev-parse", "--verify", "refs/heads/"+repo.DefaultBranch+"^{commit}")
	if err != nil && ExitCodeOf(err) != 128 {
		return state, err
	}
	history, err := projectGit(ctx, r, s, clone, "log", "--max-count=4096", "--format=%H:%T", target)
	if err != nil {
		return state, err
	}
	for _, line := range strings.Split(history, "\n") {
		commit, tree, ok := strings.Cut(line, ":")
		if ok && tree == state.indexTree {
			state.indexCommit = commit
			break
		}
	}
	if state.indexCommit == "" {
		return state, errors.New("index does not match a commit in bounded freshly fetched history; preserved")
	}
	return state, nil
}

type projectRepairReceipt struct {
	Version         int       `json:"version"`
	Repo            string    `json:"repo"`
	GitHub          string    `json:"github"`
	Target          string    `json:"target"`
	FetchedAt       time.Time `json:"fetchedAt"`
	PreviousHead    string    `json:"previousHead"`
	PreviousBranch  string    `json:"previousBranch"`
	PreviousDefault string    `json:"previousDefault"`
	IndexCommit     string    `json:"indexCommit"`
	PreservedRefs   []string  `json:"preservedRefs"`
	State           string    `json:"state"`
}

func repairProjectReference(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, target string, fetched time.Time, findings *[]ProjectFinding) error {
	state, err := inspectProjectReference(ctx, r, s, repo, target)
	if err != nil {
		return err
	}
	targetTree, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "rev-parse", target+"^{tree}")
	if err != nil {
		return err
	}
	if state.branch == "refs/heads/"+repo.DefaultBranch && state.head == target && state.indexTree == targetTree {
		return nil
	}
	add := func(kind, detail string) {
		*findings = append(*findings, ProjectFinding{s.ClonePath(repo.Name), kind, detail})
	}
	if state.branch == "" {
		add("detached", "reference HEAD is detached")
	} else if state.branch != "refs/heads/"+repo.DefaultBranch {
		add("wrong-branch", "reference is on "+state.branch)
	}
	if state.head != target {
		_, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "merge-base", "--is-ancestor", state.head, target)
		if err == nil {
			add("behind", "reference HEAD is behind the fresh target")
		} else if ExitCodeOf(err) == 1 {
			add("diverged", "reference HEAD is outside fresh target history; saved before repair")
		} else {
			return err
		}
	}
	headTree, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "rev-parse", state.head+"^{tree}")
	if err != nil {
		return err
	}
	if state.indexTree != headTree {
		add("stale-index", "index matches fetched commit "+state.indexCommit+" rather than HEAD")
	}
	// A peer checkout of the declared default forbids moving that branch.
	worktrees, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	var path string
	for _, line := range strings.Split(worktrees, "\n") {
		if strings.HasPrefix(line, "worktree ") {
			path = strings.TrimPrefix(line, "worktree ")
		}
		if line == "branch refs/heads/"+repo.DefaultBranch && path != s.ClonePath(repo.Name) {
			return errors.New("default branch belongs to another worktree; preserved")
		}
	}
	// Repeat every proof under the same common-Git lock immediately before
	// repair. A mismatch never gains reset authority from the earlier check.
	again, err := inspectProjectReference(ctx, r, s, repo, target)
	if err != nil {
		return err
	}
	if again != state {
		return errors.New("reference changed during the repair proof; preserved")
	}
	id, err := newUUID()
	if err != nil {
		return err
	}
	receipt := projectRepairReceipt{Version: 1, Repo: repo.Name, GitHub: repo.GitHub, Target: target, FetchedAt: fetched, PreviousHead: state.head, PreviousBranch: state.branch, PreviousDefault: state.defaultHead, IndexCommit: state.indexCommit, State: "prepared"}
	base := "refs/dev-env/repairs/" + id
	receipt.PreservedRefs = []string{base + "/head"}
	if state.defaultHead != "" {
		receipt.PreservedRefs = append(receipt.PreservedRefs, base+"/default")
	}
	parent := filepath.Join(s.workspaceDir(), "repairs")
	if err := ensureWorkspaceDirectory(parent); err != nil {
		return err
	}
	path = filepath.Join(parent, id+".json")
	if err := writeWorkspaceJSON(path, receipt); err != nil {
		return err
	}
	clone := s.ClonePath(repo.Name)
	if _, err := projectGit(ctx, r, s, clone, "update-ref", base+"/head", state.head, strings.Repeat("0", len(state.head))); err != nil {
		return err
	}
	if state.defaultHead != "" {
		if _, err := projectGit(ctx, r, s, clone, "update-ref", base+"/default", state.defaultHead, strings.Repeat("0", len(state.head))); err != nil {
			return err
		}
	}
	if _, err := projectGit(ctx, r, s, clone, "checkout", "--detach", "--force", target); err != nil {
		return err
	}
	old := state.defaultHead
	if old == "" {
		old = strings.Repeat("0", len(target))
	}
	if _, err := projectGit(ctx, r, s, clone, "update-ref", "refs/heads/"+repo.DefaultBranch, target, old); err != nil {
		return err
	}
	if _, err := projectGit(ctx, r, s, clone, "symbolic-ref", "HEAD", "refs/heads/"+repo.DefaultBranch); err != nil {
		return err
	}
	receipt.State = "repaired"
	return writeWorkspaceJSON(path, receipt)
}

func syncProjectAnchor(ctx context.Context, r Runner, s Settings, repo projectcatalog.Repository, anchor, target string) error {
	if _, err := os.Lstat(anchor); errors.Is(err, os.ErrNotExist) {
		_, err := projectGit(ctx, r, s, s.ClonePath(repo.Name), "worktree", "add", "--detach", anchor, target)
		return err
	} else if err != nil {
		return err
	}
	if err := noSymlinkComponents(anchor); err != nil {
		return err
	}
	common, err := projectGit(ctx, r, s, anchor, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(common) != filepath.Join(s.ClonePath(repo.Name), ".git") {
		return errors.New("anchor has foreign Git administration")
	}
	top, err := projectGit(ctx, r, s, anchor, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != anchor {
		return errors.New("anchor has unexpected root")
	}
	if _, err := projectGit(ctx, r, s, anchor, "symbolic-ref", "--quiet", "HEAD"); ExitCodeOf(err) != 1 {
		return errors.New("anchor is attached or uncertain; preserved")
	}
	tree, err := projectIndexProof(ctx, r, s, anchor)
	if err != nil {
		return err
	}
	headTree, err := projectGit(ctx, r, s, anchor, "rev-parse", "HEAD^{tree}")
	if err != nil || tree != headTree {
		return errors.New("anchor index differs from HEAD; preserved")
	}
	if _, err := projectGit(ctx, r, s, anchor, "merge-base", "--is-ancestor", "HEAD", target); err != nil {
		return errors.New("anchor has commits outside the fresh target history; preserved")
	}
	_, err = projectGit(ctx, r, s, anchor, "reset", "--hard", target)
	return err
}

type projectWrapperReceipt struct {
	Version       int    `json:"version"`
	Project       string `json:"project"`
	WrapperDigest string `json:"wrapperDigest"`
}

func writeProjectWrappers(s Settings, snapshot projectcatalog.Snapshot, root string) error {
	parent := filepath.Join(s.workspaceDir(), "projects")
	if err := ensureWorkspaceDirectory(parent); err != nil {
		return err
	}
	receiptPath := filepath.Join(parent, snapshot.Project()+".json")
	var old projectWrapperReceipt
	err := readWorkspaceJSON(receiptPath, &old)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && (old.Version != 1 || old.Project != snapshot.Project()) {
		return errors.New("project wrapper ownership is uncertain")
	}
	data := []byte(snapshot.ProjectRules())
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); err == nil {
			if err := noSymlinkComponents(path); err != nil {
				return err
			}
			fi, err := os.Lstat(path)
			if err != nil || !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
				return errors.New("project wrapper is not a regular file")
			}
			cur, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !bytes.Equal(cur, data) && (old.WrapperDigest == "" || projectcatalog.Digest(cur) != old.WrapperDigest) {
				return errors.New("unmanaged or modified project wrapper; preserved")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if err := writeFileAtomic(filepath.Join(root, name), data, 0o644); err != nil {
			return err
		}
	}
	return writeWorkspaceJSON(receiptPath, projectWrapperReceipt{1, snapshot.Project(), projectcatalog.Digest(data)})
}

// StoreProjectSnapshot refuses replacement: resume uses the original private
// state even after a new accepted catalog revision. Nothing is written to Git.
func StoreProjectSnapshot(s Settings, snapshot projectcatalog.Snapshot) (string, error) {
	if snapshot.Selected().Name == "" {
		return "", errors.New("invalid project snapshot")
	}
	if !strings.HasPrefix(s.StateDir, s.Home+string(filepath.Separator)) {
		return "", errors.New("task snapshot must be inside the private provider home")
	}
	if err := noSymlinkComponents(s.StateDir); err != nil {
		return "", err
	}
	fi, err := os.Stat(s.StateDir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return "", errors.New("task snapshot requires private state directory permissions")
	}
	for _, shared := range []string{s.ReposDir(), s.WorkDir(), filepath.Join(s.Home, "codex"), s.workspaceDir()} {
		if s.StateDir == shared || strings.HasPrefix(s.StateDir, shared+string(filepath.Separator)) {
			return "", errors.New("task snapshot must be private platform state")
		}
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	path := filepath.Join(s.StateDir, "project-snapshot.json")
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
			return "", errors.New("saved task snapshot is not a bounded regular file")
		}
		if err := noSymlinkComponents(path); err != nil {
			return "", err
		}
		cur, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(bytes.TrimSpace(cur), data) {
			return "", errors.New("saved project snapshot differs; resume must preserve it")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := writeWorkspaceJSON(path, snapshot); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	rulesPath := filepath.Join(s.StateDir, "project-rules.md")
	rules := []byte(snapshot.ProjectRules())
	if fi, err := os.Lstat(rulesPath); err == nil {
		if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
			return "", errors.New("saved task rules are not a regular file")
		}
		cur, err := os.ReadFile(rulesPath)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(cur, rules) {
			return "", errors.New("saved task rules differ; preserved")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := writeFileAtomic(rulesPath, rules, 0o600); err != nil {
			return "", err
		}
	} else {
		return "", err
	}
	return rulesPath, nil
}

// ProjectRuleInputs is a disabled integration seam, not a provider launch.
// Claude keeps its existing inline guard and native repository memory. Codex
// gets exactly one scalar developer_instructions override while configured
// developer text and repository instruction discovery stay intact.
func ProjectRuleInputs(provider, rulesPath, configuredDeveloper, guard string, snapshot projectcatalog.Snapshot, configuredFallbacks ...string) ([]string, error) {
	if guard == "" || snapshot.Selected().Name == "" {
		return nil, errors.New("project rules require the platform guard and a task snapshot")
	}
	switch provider {
	case "claude":
		if !filepath.IsAbs(rulesPath) {
			return nil, errors.New("claude project rules require an absolute private path")
		}
		return []string{"--append-system-prompt-file", rulesPath, "--append-system-prompt", guard}, nil
	case "codex":
		parts := []string{configuredDeveloper, snapshot.ProjectRules(), guard}
		developer := strings.Join(parts, "\n\n")
		if !utf8.ValidString(developer) {
			return nil, errors.New("developer instructions must be UTF-8")
		}
		// JSON's quoted UTF-8 string encoding is also a TOML basic string.
		// Go's strconv.Quote uses \x/\a/\v escapes that TOML cannot parse.
		literal, err := json.Marshal(developer)
		if err != nil {
			return nil, err
		}
		fallbacks := slices.Clone(configuredFallbacks)
		if !slices.Contains(fallbacks, "CLAUDE.md") {
			fallbacks = append(fallbacks, "CLAUDE.md")
		}
		quoted := make([]string, 0, len(fallbacks))
		for _, fallback := range fallbacks {
			if !utf8.ValidString(fallback) {
				return nil, errors.New("repository fallback names must be UTF-8")
			}
			literal, err := json.Marshal(fallback)
			if err != nil {
				return nil, err
			}
			quoted = append(quoted, string(literal))
		}
		return []string{"-c", "developer_instructions=" + string(literal), "-c", "project_doc_fallback_filenames=[" + strings.Join(quoted, ",") + "]"}, nil
	default:
		return nil, errors.New("unsupported project rule provider")
	}
}
