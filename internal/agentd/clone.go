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
	full := append([]string{"-C", dir}, args...)
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
	var notes []string

	if _, err := os.Stat(filepath.Join(ws.Clone, ".git")); err == nil {
		if _, err := s.git(ctx, r, ws.Clone, "fetch", "--prune", "origin"); err != nil {
			notes = append(notes, "WARN fetch failed, using what the clone has: "+cmdDetail(err))
		} else {
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

	// A restore runs on the first boot only, before the worktree exists: its
	// base may be a rescued branch (D-67). A failure leaves no worktree, so the
	// next boot tries again.
	if sess.Restore != "" {
		if _, err := os.Lstat(ws.Worktree); errors.Is(err, fs.ErrNotExist) {
			note, err := restoreRescue(ctx, r, s, sess, ws.Clone)
			if err != nil {
				return ws, newStep(name, notes, err)
			}
			notes = append(notes, note)
		}
	}

	base, err := resolveBase(ctx, r, s, ws.Clone, sess.Base)
	if err != nil {
		return ws, newStep(name, notes, err)
	}
	note, err := ensureWorktree(ctx, r, s, ws, base)
	if err != nil {
		return ws, newStep(name, notes, err)
	}
	notes = append(notes, note)
	if head, err := s.git(ctx, r, ws.Worktree, "rev-parse", "HEAD"); err == nil {
		ws.Head = head
	}
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
		if err := os.RemoveAll(tmp); err != nil {
			return 0, err
		}
		start := time.Now()
		_, err := r.Run(ctx, Cmd{Name: "git", Args: []string{"clone", "--filter=blob:none", "--quiet", s.RemoteURL(repo), tmp}, Env: gitEnv})
		if err == nil {
			if err := os.Rename(tmp, dst); err != nil {
				return 0, err
			}
			return time.Since(start), nil
		}
		lastErr = err
	}
	_ = os.RemoveAll(tmp)
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

// ensureWorktree reuses ~/work/<name> when it is a worktree, recreates it on
// its branch when only the branch survived, and otherwise adds it on a new
// branch from base. A directory there that is not a worktree is never touched.
func ensureWorktree(ctx context.Context, r Runner, s Settings, ws protocol.Workspace, base string) (string, error) {
	if fi, err := os.Stat(ws.Worktree); err == nil {
		if !fi.IsDir() {
			return "", fmt.Errorf("%s exists and is not a directory", ws.Worktree)
		}
		top, err := s.git(ctx, r, ws.Worktree, "rev-parse", "--show-toplevel")
		if err == nil && filepath.Clean(top) == filepath.Clean(ws.Worktree) {
			return "reused the worktree " + ws.Worktree, nil
		}
		return "", fmt.Errorf("%s exists and is not a git worktree; left as it is", ws.Worktree)
	}
	if err := os.MkdirAll(filepath.Dir(ws.Worktree), 0o755); err != nil {
		return "", err
	}
	// A worktree whose directory went away is still registered; prune it so
	// its branch can be checked out again.
	_, _ = s.git(ctx, r, ws.Clone, "worktree", "prune")
	if _, err := s.git(ctx, r, ws.Clone, "rev-parse", "--verify", "--quiet", "refs/heads/"+ws.Branch); err == nil {
		if _, err := s.git(ctx, r, ws.Clone, "worktree", "add", ws.Worktree, ws.Branch); err != nil {
			return "", fmt.Errorf("worktree add on the existing branch %s: %s", ws.Branch, cmdDetail(err))
		}
		return "recreated the worktree on the existing branch " + ws.Branch, nil
	}
	if _, err := s.git(ctx, r, ws.Clone, "worktree", "add", "-b", ws.Branch, ws.Worktree, base); err != nil {
		return "", fmt.Errorf("worktree add from %s: %s", base, cmdDetail(err))
	}
	return "worktree " + ws.Worktree + " on " + ws.Branch + " from " + base, nil
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
