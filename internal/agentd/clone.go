package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// gitEnv keeps git from ever waiting on a terminal prompt.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0"}

// Private administrative Git must use the proved repository's storage,
// never an index, object directory or repository selected by the caller's
// shell. Credential helpers and their normal environment remain available.
var privateGitUnsetEnv = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_QUARANTINE_PATH", "GIT_IMPLICIT_WORK_TREE",
	"GIT_GRAFT_FILE", "GIT_SHALLOW_FILE", "GIT_REPLACE_REF_BASE", "GIT_NO_REPLACE_OBJECTS",
	"GIT_PREFIX", "GIT_INTERNAL_SUPER_PREFIX", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
}

func privateGitCommand(s Settings, cmd Cmd) Cmd {
	if s.privateProjectGit {
		cmd.UnsetEnv = privateGitUnsetEnv
	}
	return cmd
}

// Prove the directories where administrative Git creates objects and the
// exact refs/index/reflogs it may replace. Inspect only the object directory's
// bounded fanout entries, not a repository-wide object walk.
func privateGitStorageClear(gitDir string, paths ...string) error {
	objects := filepath.Join(gitDir, "objects")
	if noSymlinkComponents(objects) != nil {
		return errors.New("private Git object storage is uncertain")
	}
	dir, err := os.Open(objects)
	if err != nil {
		return errors.New("private Git object storage is unavailable")
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(260)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 258 {
		return errors.New("private Git object fanout is uncertain")
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("private Git object directory is redirected or uncertain")
		}
	}
	for _, name := range []string{"info/alternates", "info/http-alternates"} {
		if _, err := os.Lstat(filepath.Join(objects, name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("private Git alternate storage is present or uncertain")
		}
	}
	for _, name := range append([]string{"HEAD", "index", "config", "packed-refs", "ORIG_HEAD"}, paths...) {
		path := filepath.Join(gitDir, name)
		if err := noSymlinkComponents(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("private Git write path is redirected or uncertain")
		}
		if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() || err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("private Git write path is not a regular file")
		}
	}
	return privateOrigHeadClear(gitDir)
}

func privateOrigHeadClear(gitDir string) error {
	path := filepath.Join(gitDir, "ORIG_HEAD")
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 65 || noSymlinkComponents(path) != nil {
		return errors.New("private original HEAD is redirected or uncertain")
	}
	data, err := os.ReadFile(path)
	if err != nil || !rootObjectID.MatchString(strings.TrimSpace(string(data))) {
		return errors.New("private original HEAD is symbolic or uncertain")
	}
	return nil
}

// A shared command must not start implicit repository-wide maintenance. Git
// propagates these command options to its internal promisor fetches too.
var sharedGitPolicyArgs = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// originBranchesRefspec maps origin's branches into refs/remotes/origin only.
const originBranchesRefspec = "+refs/heads/*:refs/remotes/origin/*"

// originFetchArgs is a whole-origin fetch that can only change
// refs/remotes/origin. --refmap= and the explicit refspec replace the clone's
// configured remote.origin.fetch: a mapping into refs/heads would otherwise
// let --prune delete, or a forced update move, local branches origin lacks,
// session and peer branches included. --no-prune-tags keeps local tags.
func originFetchArgs(opts ...string) []string {
	return append(append([]string{"fetch"}, opts...), "--no-prune-tags", "--refmap=", "origin", originBranchesRefspec)
}

// cloneAttempts and cloneBackoff bound the clone's retries: S-7 measured every
// repo under 25 s, so three tries cover a passing network blip.
var (
	cloneAttempts = 3
	cloneBackoff  = []time.Duration{5 * time.Second, 15 * time.Second}
)

// cloneStallArgs make Git give up on a stalled or trickling transfer by itself,
// so its own cleanup removes the staging directory and the retry above can run.
// A transfer is stalled when it averages under cloneLowSpeedLimit bytes a
// second for cloneLowSpeedTime seconds. Healthy GitHub clones of the catalog
// run at megabytes a second (S-7, and the 2026-10-20 NB-08b measurements);
// the stalls measured in session pods ran at about 100 KB/s for a minute or
// two, and the 2-minute repository deadline then killed Git mid-clone and left
// staging that every later boot refuses to touch (StagingPreserved). The
// deadline stays the backstop.
//
// Git leaves a clone whose checkout fails in place ("Clone succeeded, but
// checkout failed"), and the stalls were seen in the checkout's lazy blob
// fetch. So clones use --no-checkout and checkoutStaging populates the staging
// directory as its own retried step; a failed clone command removes its own
// directory, so the clone retry still starts clean.
//
// The assumption: a healthy transfer or pack build produces at least
// cloneLowSpeedLimit bytes a second on average over cloneLowSpeedTime. A build
// that sends only keepalives for longer than that (--quiet turns progress off)
// is abandoned on every attempt; GitHub's packs for the catalog did not.
const (
	cloneLowSpeedLimit = "250000"
	cloneLowSpeedTime  = "20"
)

func cloneStallArgs() []string {
	return []string{"-c", "http.lowSpeedLimit=" + cloneLowSpeedLimit, "-c", "http.lowSpeedTime=" + cloneLowSpeedTime}
}

// checkoutStaging populates a --no-checkout clone's worktree, fetching the
// blobs it needs, and retries a stalled or failed checkout in place. Git
// restores the index and worktree from HEAD, which is idempotent, and staging
// holds nothing else yet. A failure keeps the staging for the caller to preserve.
func checkoutStaging(ctx context.Context, r Runner, s Settings, tmp string) error {
	args := append([]string{"-C", tmp, "-c", "core.hooksPath=/dev/null"}, cloneStallArgs()...)
	if s.WorkspaceID != "" || s.privateProjectGit {
		// The clone command carried these; no auto maintenance in staging.
		args = append(args, sharedGitPolicyArgs...)
	}
	args = append(args, "checkout", "--force", "--quiet")
	// An empty repository clones with an unborn HEAD and nothing to check out;
	// a plain clone succeeds there, so must this. Exit 1 is Git's "no such
	// commit"; any other failure is not an answer and goes to the checkout.
	if _, perr := r.Run(ctx, privateGitCommand(s, Cmd{Name: "git", Args: []string{"-C", tmp, "rev-parse", "--verify", "--quiet", "HEAD"}, Env: gitEnv})); perr != nil && ExitCodeOf(perr) == 1 {
		return nil
	}
	var err error
	for attempt := 0; attempt < cloneAttempts; attempt++ {
		if attempt > 0 {
			if serr := sleepFunc(ctx, cloneBackoff[min(attempt-1, len(cloneBackoff)-1)]); serr != nil {
				return serr
			}
		}
		if _, err = r.Run(ctx, privateGitCommand(s, Cmd{Name: "git", Args: args, Env: gitEnv})); err == nil {
			return nil
		}
	}
	return err
}

// sleepFunc waits, or returns early when ctx ends. Tests replace it.
var sleepFunc = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (s Settings) git(ctx context.Context, r Runner, dir string, args ...string) (string, error) {
	full := []string{"-C", dir}
	if s.WorkspaceID != "" || s.privateProjectGit {
		full = append(full, sharedGitPolicyArgs...)
	}
	if s.privateProjectGit {
		full = append(full, projectGitPolicyArgs...)
	}
	full = append(full, args...)
	env := gitEnv
	if s.privateProjectGit {
		env = append(append([]string(nil), gitEnv...), "GIT_OPTIONAL_LOCKS=0")
	}
	res, err := r.Run(ctx, privateGitCommand(s, Cmd{Name: "git", Args: full, Env: env}))
	if err != nil {
		var ce *CmdError
		if errors.As(err, &ce) {
			// The first argument is -C; name the git subcommand instead.
			ce.Sub = args[0]
		}
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}

// Managed references retain this local policy for native task Git commands,
// whose commits otherwise may implicitly prune shared worktree registration.
// Call only while holding the common-Git administrative lock (or before a new
// staging clone is published). Private clone behavior remains unchanged.
func configureSharedGitPolicy(ctx context.Context, r Runner, s Settings, clone string) error {
	if s.WorkspaceID == "" {
		return nil
	}
	config := filepath.Join(clone, ".git", "config")
	if err := noSymlinkComponents(config); err != nil {
		return err
	}
	fi, err := os.Lstat(config)
	if err != nil || !fi.Mode().IsRegular() {
		return errors.New("shared Git configuration is not a regular file")
	}
	for _, policy := range [][2]string{{"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		current, err := s.git(ctx, r, clone, "config", "--local", "--get", policy[0])
		if err != nil && ExitCodeOf(err) != 1 {
			return err
		}
		if current != policy[1] {
			if _, err := s.git(ctx, r, clone, "config", "--local", "--replace-all", policy[0], policy[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// PrepareRepo is boot step 2 (DESIGN-001 3.6, D-15): a partial clone into
// ~/repos/<repo> and a worktree at ~/work/<name> on branch agent/<name>. On a
// resume, or after a container restart, it reuses what the volume holds and
// only fetches. It returns the workspace and the boot step that reports it.
func PrepareRepo(ctx context.Context, r Runner, s Settings, sess protocol.Session) (protocol.Workspace, Step) {
	const name = "repo"
	ws := protocol.Workspace{
		Clone:    s.ClonePath(sess.Repo),
		Worktree: s.WorktreePath(sess.Name),
		Branch:   "agent/" + sess.Name,
	}
	var privateProject *projectcatalog.Repository
	var privateSnapshot projectcatalog.Snapshot
	// identityDrift is an origin URL mismatch tolerated for an existing task:
	// it resumes without a fetch and can never create a new task branch.
	var identityDrift error
	if len(sess.ProjectSnapshot) != 0 && s.WorkspaceID == "" && sess.Workspace == nil {
		snapshot, err := acceptedProjectTask(s, sess)
		if err != nil {
			return ws, newStep(name, nil, err)
		}
		repo := snapshot.Selected()
		privateProject, privateSnapshot = &repo, snapshot
		s.TaskRepositoryURL = repo.URL()
		s.privateProjectGit = true
		// A reused private reference must prove its own Git storage before any
		// command and carry the accepted identity before any fetch. A foreign
		// or ambiguous directory is never a clone target.
		if _, err := os.Lstat(ws.Clone); err == nil {
			if err := validateProjectReferenceStorageAt(ctx, r, s, repo, ws.Clone); err != nil {
				return ws, newStep(name, nil, err)
			}
			if err := validateProjectReferenceIdentityAt(ctx, r, s, repo, ws.Clone); err != nil {
				// A resume needs no fetch, and task worktrees share this
				// origin configuration. Only a new task needs the full proof.
				existing, existErr := privateTaskExists(ctx, r, s, ws)
				if existErr != nil {
					return ws, newStep(name, nil, existErr)
				}
				if !existing {
					return ws, newStep(name, nil, err)
				}
				identityDrift = err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return ws, newStep(name, nil, err)
		}
	}
	if s.WorkspaceID != "" || sess.Workspace != nil {
		if err := workspacePreflight(s, sess); err != nil {
			return ws, newStep(name, nil, err)
		}
		if s.writer == nil {
			return ws, newStep(name, nil, errors.New("shared Git preparation requires the daemon's writer lock"))
		}
		unlock, err := workspaceAdminLock(ctx, s, sess.Repo)
		if err != nil {
			return ws, newStep(name, nil, err)
		}
		defer unlock()
		bounded, cancel := context.WithTimeout(ctx, sharedGitPrepareBudget)
		defer cancel()
		ctx = bounded
		var owner taskOwner
		if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
			return ws, newStep(name, nil, err)
		}
		if err := ownerMatches(s, sess, owner, true); err != nil {
			return ws, newStep(name, nil, err)
		}
		if owner.State != "owned" || owner.Generation != s.writer.owner.Generation {
			return ws, newStep(name, nil, errors.New("writer receipt changed before Git preparation"))
		}
		if err := validateSharedClone(ctx, r, s, sess.Repo); err != nil {
			return ws, newStep(name, nil, err)
		}
	}
	var notes []string
	var fetchErr error

	if _, err := os.Stat(filepath.Join(ws.Clone, ".git")); err == nil {
		if err := configureSharedGitPolicy(ctx, r, s, ws.Clone); err != nil {
			return ws, newStep(name, notes, err)
		}
		if privateProject == nil {
			fetchArgs := originFetchArgs("--prune")
			if s.WorkspaceID != "" {
				fetchArgs = originFetchArgs("--no-auto-maintenance", "--no-prune")
			}
			if _, fetchErr = s.git(ctx, r, ws.Clone, fetchArgs...); fetchErr == nil {
				notes = append(notes, "reused the clone and fetched origin")
			}
		}
	} else {
		waited := waitForFile(ctx, s.GHTokenFile, s.TokenWait)
		if !waited {
			notes = append(notes, "no GitHub token at "+s.GHTokenFile+"; cloning without one (public repos only)")
		}
		if privateProject != nil {
			// The staged reference clone, as bootstrap uses: the declared branch,
			// identity proved before publication, and staging never deleted.
			start := time.Now()
			if err := cloneProjectReference(ctx, r, s, *privateProject); err != nil {
				return ws, newStep(name, notes, err)
			}
			notes = append(notes, fmt.Sprintf("cloned %s (blob:none) on %s in %s", privateProject.URL(), privateProject.DefaultBranch, time.Since(start).Round(100*time.Millisecond)))
		} else {
			took, err := cloneRepo(ctx, r, s, sess.Repo, ws.Clone)
			if err != nil {
				return ws, newStep(name, notes, err)
			}
			notes = append(notes, fmt.Sprintf("cloned %s (blob:none) in %s", s.RemoteURL(sess.Repo), took.Round(100*time.Millisecond)))
		}
	}
	if privateProject != nil {
		sess.Base = "refs/remotes/origin/" + privateProject.DefaultBranch
		existing, err := privateTaskExists(ctx, r, s, ws)
		if err != nil {
			return ws, newStep(name, notes, err)
		}
		var recorded *protocol.TaskSource
		var sourceProblem bool
		if existing {
			// Only this session's record names the task's source; a resume
			// reports it and never rewrites it. A task from before the record
			// reports none, and so does a task whose record is unreadable or
			// binds another session or source.
			if recorded, err = sessionTaskSource(s, sess); err != nil {
				sourceProblem = true
				notes = append(notes, "WARN the task source record is not reported; preserved: "+err.Error())
			}
		}
		if identityDrift != nil {
			fetchErr = fetchSkipped{identityDrift}
			notes = append(notes, "WARN "+identityDrift.Error()+"; skipped the fetch: the existing task is reused as it is, and no new task branch is created until the reference is repaired")
		} else {
			if err := validateProjectReference(ctx, r, s, *privateProject); err != nil {
				return ws, newStep(name, notes, err)
			}
			// Fetch the declared branch explicitly; a configured fetch refspec or
			// stale local branch must not select a different base for a new task.
			target, fetched, err := fetchProjectTarget(ctx, r, s, *privateProject)
			fetchErr = err
			switch {
			case err != nil:
			case existing:
				note := fmt.Sprintf("refreshed origin/%s of %s at %s on %s; this is not the task's source", privateProject.DefaultBranch, privateProject.GitHub, target, fetched.Format(time.RFC3339Nano))
				switch {
				case recorded != nil:
					note += fmt.Sprintf(", which stays %s fetched %s", recorded.Commit, recorded.FetchedAt.Format(time.RFC3339Nano))
				case sourceProblem:
					note += ", and this task reports no source"
				default:
					note += ", and this task has no recorded source pin"
				}
				notes = append(notes, note)
			default:
				notes = append(notes, fmt.Sprintf("fetched accepted repository %s branch %s at %s on %s", privateProject.GitHub, privateProject.DefaultBranch, target, fetched.Format(time.RFC3339Nano)))
				// A new task records its pin once, before its worktree exists.
				source, note, err := pinTaskSource(s, sess, privateSnapshot, target, fetched)
				if err != nil {
					return ws, newStep(name, notes, err)
				}
				notes = append(notes, note)
				sess.Base, recorded = source.Commit, &source
			}
		}
		ws.Source = recorded
	}

	workspaceNotes, err := ensureWorktree(ctx, r, s, ws, sess, fetchErr)
	notes = append(notes, workspaceNotes...)
	if err != nil {
		return ws, newStep(name, notes, err)
	}
	head, err := s.git(ctx, r, ws.Worktree, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return ws, newStep(name, notes, fmt.Errorf("verify the workspace HEAD: %s", cmdDetail(err)))
	}
	ws.Head = head
	return ws, newStep(name, notes, nil)
}

// fetchSkipped is a fetch agentd chose not to run. Like a failed fetch, it
// preserves an existing task as it is and refuses a new task branch.
type fetchSkipped struct{ reason error }

func (f fetchSkipped) Error() string { return f.reason.Error() }

// fetchProblem names a fetch error for a note: skipped or failed.
func fetchProblem(err error) string {
	var skipped fetchSkipped
	if errors.As(err, &skipped) {
		return "fetch skipped"
	}
	return "fetch failed"
}

// privateTaskExists reports whether this session already has a task: its
// worktree path, its agent/<name> branch or its first launch record. Such a
// task resumes or is refused on its own terms; it never starts a new branch.
// The caller has proved the reference's storage, or the clone is absent.
func privateTaskExists(ctx context.Context, r Runner, s Settings, ws protocol.Workspace) (bool, error) {
	for _, path := range []string{ws.Worktree, s.statePath(launchFile)} {
		if _, err := os.Lstat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	if _, err := os.Lstat(filepath.Join(ws.Clone, ".git")); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	_, err := s.git(ctx, r, ws.Clone, "show-ref", "--verify", "--quiet", "refs/heads/"+ws.Branch)
	switch {
	case err == nil:
		return true, nil
	case ExitCodeOf(err) == 1:
		return false, nil
	default:
		return false, fmt.Errorf("check the session branch %s: %s", ws.Branch, cmdDetail(err))
	}
}

// readTaskSource reads the task's source record. A missing record is nil
// without an error: other sessions and older tasks have none.
func readTaskSource(s Settings) (*protocol.TaskSource, error) {
	var source protocol.TaskSource
	if err := readWorkspaceJSON(s.statePath(taskSourceFile), &source); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if source.Version != 1 || source.Session == "" || source.Repo == "" || source.GitHub == "" || source.Branch == "" ||
		!rootObjectID.MatchString(source.Commit) || source.FetchedAt.IsZero() {
		return nil, errors.New("task source record is invalid")
	}
	return &source, nil
}

// errForeignTaskSource is a well-formed record that binds another session,
// session UID, repository, branch or accepted snapshot.
var errForeignTaskSource = errors.New("task source record belongs to another session or source")

// taskSourceFor is the record this session's task writes, without the commit
// and fetch time that only the record itself holds.
func taskSourceFor(sess protocol.Session, snapshot projectcatalog.Snapshot) protocol.TaskSource {
	repo := snapshot.Selected()
	return protocol.TaskSource{Version: 1, Session: sess.Name, SessionUID: sess.SessionUID, Repo: repo.Name, GitHub: repo.GitHub, Branch: repo.DefaultBranch,
		CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision()}
}

// taskSourceBinds reports whether recorded is want's record: every field but
// the commit and the fetch time must match.
func taskSourceBinds(recorded, want protocol.TaskSource) bool {
	recorded.Commit, recorded.FetchedAt = want.Commit, want.FetchedAt
	return recorded == want
}

// sessionTaskSource is the task source record when it belongs to sess and its
// accepted project snapshot. A missing record is nil without an error. An
// unreadable or foreign record is an error: never reported as this task's
// source, and kept as it is.
func sessionTaskSource(s Settings, sess protocol.Session) (*protocol.TaskSource, error) {
	recorded, err := readTaskSource(s)
	if err != nil || recorded == nil {
		return recorded, err
	}
	if len(sess.ProjectSnapshot) == 0 {
		return nil, errForeignTaskSource
	}
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil || !taskSourceBinds(*recorded, taskSourceFor(sess, snapshot)) {
		return nil, errForeignTaskSource
	}
	return recorded, nil
}

// pinTaskSource returns the source a new task's worktree is created from. The
// first new-task boot records the fresh pin with an exclusive create. A retry,
// after a worktree creation that failed, uses that recorded pin: the record is
// never replaced, so it always names the commit the worktree came from.
func pinTaskSource(s Settings, sess protocol.Session, snapshot projectcatalog.Snapshot, target string, fetched time.Time) (protocol.TaskSource, string, error) {
	source := taskSourceFor(sess, snapshot)
	source.Commit, source.FetchedAt = target, fetched.UTC().Round(0)
	recorded, err := readTaskSource(s)
	if err != nil {
		return source, "", fmt.Errorf("%w; preserved, and no new task branch is created", err)
	}
	if recorded == nil {
		data, err := json.Marshal(source)
		if err != nil {
			return source, "", err
		}
		if err := writeFileOnce(s.statePath(taskSourceFile), append(data, '\n'), 0o600); err != nil {
			return source, "", fmt.Errorf("record the task source: %w", err)
		}
		return source, fmt.Sprintf("recorded the task source: %s branch %s at %s", source.GitHub, source.Branch, target), nil
	}
	if !taskSourceBinds(*recorded, source) {
		return source, "", fmt.Errorf("%w; preserved, and no new task branch is created", errForeignTaskSource)
	}
	return *recorded, fmt.Sprintf("creating the task from its recorded source %s fetched %s, not from this fetch", recorded.Commit, recorded.FetchedAt.Format(time.RFC3339Nano)), nil
}

// waitForFile waits until path is a non-empty file, up to max. The keeper's
// token Secret may land a little after the pod starts.
func waitForFile(ctx context.Context, path string, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		if sleepFunc(ctx, 2*time.Second) != nil {
			return false
		}
	}
}

// cloneRepo clones into a temporary directory beside the target and renames
// it into place, so a clone cut short never leaves a half repo at the path the
// next boot would reuse.
func cloneRepo(ctx context.Context, r Runner, s Settings, repo, dst string) (time.Duration, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, err
	}
	tmp := filepath.Join(filepath.Dir(dst), "."+filepath.Base(dst)+".agentd-clone")
	// Shared staging may be a peer's. With private bootstrap enabled, this path
	// holds bootstrap's preserved staging or this session's own interrupted
	// clone, which agentd cannot tell apart: neither is ever deleted here, and
	// a human recovers it (DESIGN-006).
	preserve := s.WorkspaceID != "" || s.PrivateProjectBootstrap
	kind, recovery := "shared staging clone", ""
	if s.WorkspaceID == "" {
		kind, recovery = "staging clone", "; inspect it and remove it before another clone attempt"
	}
	var lastErr error
	for attempt := 0; attempt < cloneAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepFunc(ctx, cloneBackoff[min(attempt-1, len(cloneBackoff)-1)]); err != nil {
				return 0, err
			}
		}
		if preserve {
			if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
				return 0, fmt.Errorf("%s %s is partial or uncertain; preserved and refused%s", kind, tmp, recovery)
			}
		} else if err := os.RemoveAll(tmp); err != nil {
			return 0, err
		}
		start := time.Now()
		_, err := r.Run(ctx, Cmd{Name: "git", Args: append(cloneStallArgs(), "clone", "--filter=blob:none", "--no-checkout", "--quiet", s.RemoteURL(repo), tmp), Env: gitEnv})
		if err == nil {
			// A checkout that keeps failing ends the clone: its own retries
			// have run, so the outer loop must not multiply them.
			if err := checkoutStaging(ctx, r, s, tmp); err != nil {
				if !preserve {
					_ = os.RemoveAll(tmp)
					return 0, fmt.Errorf("checkout of %s failed: %s", s.RemoteURL(repo), cmdDetail(err))
				}
				return 0, fmt.Errorf("clone checkout failed; %s %s keeps partial or uncertain data, preserved%s: %s", kind, tmp, recovery, cmdDetail(err))
			}
			if err := configureSharedGitPolicy(ctx, r, s, tmp); err != nil {
				return 0, err
			}
			if err := os.Rename(tmp, dst); err != nil {
				return 0, err
			}
			return time.Since(start), nil
		}
		lastErr = err
		if preserve {
			if _, statErr := os.Lstat(tmp); !errors.Is(statErr, os.ErrNotExist) {
				return 0, fmt.Errorf("clone failed; %s %s keeps partial or uncertain data, preserved%s: %s", kind, tmp, recovery, cmdDetail(err))
			}
		}
	}
	if !preserve {
		_ = os.RemoveAll(tmp)
	}
	return 0, fmt.Errorf("clone of %s failed %d times: %s", s.RemoteURL(repo), cloneAttempts, cmdDetail(lastErr))
}

// resolveBase is the session's base, or origin's default branch.
func resolveBase(ctx context.Context, r Runner, s Settings, clone, base string) (string, error) {
	if base != "" {
		return base, nil
	}
	head, err := s.git(ctx, r, clone, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil || head == "" {
		return "origin/main", nil
	}
	return head, nil
}

// ensureWorktree keeps surviving session work even when origin is unavailable.
// A new branch needs a successful refresh or an explicitly restored rescue
// base. Existing worktrees must belong to the session's clone; branch changes
// and detached HEAD are preserved with a warning.
func ensureWorktree(ctx context.Context, r Runner, s Settings, ws protocol.Workspace, sess protocol.Session, fetchErr error) ([]string, error) {
	var notes []string
	if fi, err := os.Lstat(ws.Worktree); err == nil {
		if !fi.IsDir() {
			return notes, fmt.Errorf("%s exists and is not a directory; left as it is", ws.Worktree)
		}
		if s.WorkspaceID != "" {
			if err := noSymlinkComponents(ws.Worktree); err != nil {
				return notes, err
			}
		}
		top, err := s.git(ctx, r, ws.Worktree, "rev-parse", "--show-toplevel")
		if err != nil || !sameDirectory(top, ws.Worktree) {
			return notes, fmt.Errorf("%s exists and is not a git worktree; left as it is", ws.Worktree)
		}
		common, err := s.git(ctx, r, ws.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || !sameDirectory(common, filepath.Join(ws.Clone, ".git")) {
			return notes, fmt.Errorf("%s does not belong to the session's clone; left as it is", ws.Worktree)
		}
		branch, branchErr := s.git(ctx, r, ws.Worktree, "symbolic-ref", "--quiet", "HEAD")
		if branchErr != nil && ExitCodeOf(branchErr) == 1 {
			notes = append(notes, "WARN the existing worktree has detached HEAD; expected session branch "+ws.Branch+"; preserving it as it is")
		} else if branchErr != nil {
			return notes, fmt.Errorf("identify the worktree branch: %s; left as it is", cmdDetail(branchErr))
		} else if branch != "refs/heads/"+ws.Branch {
			notes = append(notes, "WARN the existing worktree is on "+strings.TrimPrefix(branch, "refs/heads/")+"; expected session branch "+ws.Branch+"; preserving it as it is")
		}
		if fetchErr != nil {
			notes = append(notes, "WARN "+fetchProblem(fetchErr)+"; preserving the existing worktree without moving its branch: "+cmdDetail(fetchErr))
		}
		return append(notes, "reused the worktree "+ws.Worktree), nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return notes, err
	}

	_, branchErr := s.git(ctx, r, ws.Clone, "show-ref", "--verify", "--quiet", "refs/heads/"+ws.Branch)
	if branchErr != nil && ExitCodeOf(branchErr) != 1 {
		return notes, fmt.Errorf("check the session branch %s: %s", ws.Branch, cmdDetail(branchErr))
	}
	if branchErr == nil {
		if fetchErr != nil {
			notes = append(notes, "WARN "+fetchProblem(fetchErr)+"; recovering the existing session branch without moving it: "+cmdDetail(fetchErr))
		}
		if err := prepareWorktreePath(ctx, r, s, ws); err != nil {
			return notes, err
		}
		if _, err := s.git(ctx, r, ws.Clone, "worktree", "add", ws.Worktree, ws.Branch); err != nil {
			return notes, fmt.Errorf("worktree add on the existing branch %s: %s", ws.Branch, cmdDetail(err))
		}
		return append(notes, "recreated the worktree on the existing branch "+ws.Branch), nil
	}
	if _, err := os.Lstat(s.statePath(launchFile)); err == nil {
		return notes, fmt.Errorf("the session was already launched, but its worktree and branch are missing; restore its saved work in a new session before continuing")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return notes, fmt.Errorf("check the first launch record: %w", err)
	}

	base, err := resolveBase(ctx, r, s, ws.Clone, sess.Base)
	if err != nil {
		return notes, err
	}
	var offlineBase string
	if fetchErr != nil {
		if sess.Restore == "" || !strings.HasPrefix(base, "refs/rescued/") {
			return notes, fmt.Errorf("%s; refusing a new session branch from %s: %s", fetchProblem(fetchErr), base, cmdDetail(fetchErr))
		}
		offlineBase = base
	}
	// A restore is only for a new branch. A surviving session branch already
	// holds its own work, even if its old rescue has since been pruned.
	if sess.Restore != "" {
		note, err := restoreRescue(ctx, r, s, sess, ws.Clone, offlineBase)
		if err != nil {
			return notes, err
		}
		notes = append(notes, note)
	}
	if fetchErr != nil {
		notes = append(notes, "WARN fetch failed; creating the session from the verified rescue base: "+cmdDetail(fetchErr))
	}
	// Resolve once: worktree add must receive the same commit even if the ref
	// changes between these commands. Tags must peel to a commit too.
	head, err := s.git(ctx, r, ws.Clone, "rev-parse", "--verify", "--end-of-options", base+"^{commit}")
	if err != nil {
		return notes, fmt.Errorf("resolve base %s to a commit: %s", base, cmdDetail(err))
	}
	if err := prepareWorktreePath(ctx, r, s, ws); err != nil {
		return notes, err
	}
	if _, err := s.git(ctx, r, ws.Clone, "worktree", "add", "-b", ws.Branch, ws.Worktree, head); err != nil {
		return notes, fmt.Errorf("worktree add from %s at %s: %s", base, head, cmdDetail(err))
	}
	return append(notes, "worktree "+ws.Worktree+" on "+ws.Branch+" from "+base+" at "+head), nil
}

func sameDirectory(a, b string) bool {
	fi, err := os.Stat(a)
	if err != nil {
		return false
	}
	other, err := os.Stat(b)
	return err == nil && fi.IsDir() && other.IsDir() && os.SameFile(fi, other)
}

func prepareWorktreePath(ctx context.Context, r Runner, s Settings, ws protocol.Workspace) error {
	if err := os.MkdirAll(filepath.Dir(ws.Worktree), 0o755); err != nil {
		return err
	}
	if s.WorkspaceID != "" {
		// A missing peer path may be an unavailable mount, not an abandoned
		// worktree. Shared mode never removes another task's Git registration.
		return nil
	}
	// A worktree whose directory went away is still registered; prune it so
	// its branch can be checked out again.
	if _, err := s.git(ctx, r, ws.Clone, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune missing worktrees: %s", cmdDetail(err))
	}
	return nil
}

// cmdDetail is CmdError.Detail for any error. git's stderr holds no secret:
// the token reaches git through the credential helper, never the URL.
func cmdDetail(err error) string {
	var ce *CmdError
	if errors.As(err, &ce) {
		return ce.Detail()
	}
	return err.Error()
}
