package agentd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// gitEnv keeps git from ever waiting on a terminal prompt.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0"}

// A shared command must not start implicit repository-wide maintenance. Git
// propagates these command options to its internal promisor fetches too.
var sharedGitPolicyArgs = []string{"-c", "gc.auto=0", "-c", "maintenance.auto=false"}

// cloneAttempts and cloneBackoff bound the clone's retries: S-7 measured every
// repo under 25 s, so three tries cover a passing network blip.
var (
	cloneAttempts = 3
	cloneBackoff  = []time.Duration{5 * time.Second, 15 * time.Second}
)

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
	if s.WorkspaceID != "" {
		full = append(full, sharedGitPolicyArgs...)
	}
	full = append(full, args...)
	res, err := r.Run(ctx, Cmd{Name: "git", Args: full, Env: gitEnv})
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
		fetchArgs := []string{"fetch", "--prune", "origin"}
		if s.WorkspaceID != "" {
			fetchArgs = []string{"fetch", "--no-auto-maintenance", "--no-prune", "origin"}
		}
		if _, fetchErr = s.git(ctx, r, ws.Clone, fetchArgs...); fetchErr == nil {
			notes = append(notes, "reused the clone and fetched origin")
		}
	} else {
		waited := waitForFile(ctx, s.GHTokenFile, s.TokenWait)
		if !waited {
			notes = append(notes, "no GitHub token at "+s.GHTokenFile+"; cloning without one (public repos only)")
		}
		took, err := cloneRepo(ctx, r, s, sess.Repo, ws.Clone)
		if err != nil {
			return ws, newStep(name, notes, err)
		}
		notes = append(notes, fmt.Sprintf("cloned %s (blob:none) in %s", s.RemoteURL(sess.Repo), took.Round(100*time.Millisecond)))
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
	var lastErr error
	for attempt := 0; attempt < cloneAttempts; attempt++ {
		if attempt > 0 {
			if err := sleepFunc(ctx, cloneBackoff[min(attempt-1, len(cloneBackoff)-1)]); err != nil {
				return 0, err
			}
		}
		if s.WorkspaceID != "" {
			if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
				return 0, fmt.Errorf("shared staging clone %s is partial or uncertain; preserved and refused", tmp)
			}
		} else if err := os.RemoveAll(tmp); err != nil {
			return 0, err
		}
		start := time.Now()
		_, err := r.Run(ctx, Cmd{Name: "git", Args: []string{"clone", "--filter=blob:none", "--quiet", s.RemoteURL(repo), tmp}, Env: gitEnv})
		if err == nil {
			if err := configureSharedGitPolicy(ctx, r, s, tmp); err != nil {
				return 0, err
			}
			if err := os.Rename(tmp, dst); err != nil {
				return 0, err
			}
			return time.Since(start), nil
		}
		lastErr = err
		if s.WorkspaceID != "" {
			if _, statErr := os.Lstat(tmp); !errors.Is(statErr, os.ErrNotExist) {
				return 0, fmt.Errorf("shared clone failed with partial or uncertain staging data preserved: %s", cmdDetail(err))
			}
		}
	}
	if s.WorkspaceID == "" {
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
			notes = append(notes, "WARN fetch failed; preserving the existing worktree without moving its branch: "+cmdDetail(fetchErr))
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
			notes = append(notes, "WARN fetch failed; recovering the existing session branch without moving it: "+cmdDetail(fetchErr))
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
			return notes, fmt.Errorf("fetch origin failed; refusing a new session branch from %s: %s", base, cmdDetail(fetchErr))
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
