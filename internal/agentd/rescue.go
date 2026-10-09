package agentd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// rescueUntrackedCap is v1's limit on untracked files a rescue commits: more
// is renders, recordings or an unignored venv, a human's call.
var rescueUntrackedCap int64 = 50 << 20

// rescueFetchTimeout bounds the fetch that tells which refs origin lacks.
const rescueFetchTimeout = 60 * time.Second

// rescueLockFile serialises rescues in one pod.
const rescueLockFile = "rescue.lock"

// Rescue is `agentd ctl rescue`, step 1 of D-10 with v1's rules (D-43): in
// every clone under ~/repos, every worktree's tracked edits and untracked files
// (gitignored ones excluded) are committed to rescue/<worktree>-<stamp>, and a
// detached HEAD whose commit is on no ref is anchored on such a branch. v1
// switched the worktree onto the rescue branch; agentd commits through a
// temporary index instead and leaves the worktree, its index and its branch as
// they were, because a v2 session can be resumed after its rescue. It refuses a
// worktree with a merge or rebase in progress, an untracked nested repo, a
// submodule holding work origin lacks, or more than 50 MiB untracked. Rescue branches are
// never pushed (D-10).
//
// Then it writes D-10 step 2's bundle of the unpushed refs to the shared volume
// (D-48, writeBundles). With opt.StopAgent it first stops the agent CLI, so the
// rescue is the worktree's last state: the operator asks for that before it
// deletes a pod.
//
// A volume that holds nothing (volumeEmpty) gets a report that says so and
// nothing else: the rescue writes nothing to it, so it stays empty for the
// next look (D-55).
func Rescue(ctx context.Context, r Runner, s Settings, session string, now time.Time, opt RescueOptions) (protocol.RescueReport, error) {
	if s.WorkspaceID != "" {
		return rescueSharedTask(ctx, r, s, session, now, opt)
	}
	if opt.WorkspaceStopProof != nil {
		return protocol.RescueReport{}, errors.New("workspace stop proof cannot authorize a private rescue")
	}
	if s.Getenv != nil {
		if sess, err := LoadSession(s.Getenv); err == nil && sess.Workspace != nil {
			return protocol.RescueReport{}, errors.New("shared session lacks its operator workspace binding; refusing private rescue")
		}
	}
	empty, err := volumeEmpty(s)
	if err != nil {
		return protocol.RescueReport{}, err
	}
	if empty {
		rep := protocol.RescueReport{Session: session, Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(),
			Repos: []protocol.RepoRescue{}, OK: true, CleanAndPushed: true, VolumeEmpty: true}
		if opt.StopAgent {
			// Only reads agentd's pid file, which an empty volume lacks.
			rep.Agent = stopAgent(s, opt.StopGrace)
		}
		rep.FinishedAt = time.Now().UTC()
		return rep, nil
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return protocol.RescueReport{}, err
	}
	unlock, err := lockFile(s.statePath(rescueLockFile))
	if err != nil {
		return protocol.RescueReport{}, err
	}
	defer unlock()

	rep := protocol.RescueReport{Session: session, Stamp: now.UTC().Format("20060102-1504"), StartedAt: now.UTC(), Repos: []protocol.RepoRescue{}, OK: true, CleanAndPushed: true}
	if opt.StopAgent {
		rep.Agent = stopAgent(s, opt.StopGrace)
	}
	repos, err := clonesIn(s.ReposDir())
	if err != nil {
		return protocol.RescueReport{}, err
	}
	for _, repo := range repos {
		rr := rescueRepo(ctx, r, s, repo, rep.Stamp)
		if rr.Error != "" {
			rep.OK = false
		}
		if rr.Error != "" || !rr.Fetched || len(rr.UnpushedRefs) > 0 {
			rep.CleanAndPushed = false
		}
		for _, w := range rr.Worktrees {
			if w.Refused != "" {
				rep.OK = false
			}
			if w.Dirty || w.Refused != "" || w.RescueBranch != "" {
				rep.CleanAndPushed = false
			}
		}
		rep.Repos = append(rep.Repos, rr)
	}
	writeBundles(ctx, r, s, &rep, now)
	rep.FinishedAt = time.Now().UTC()
	return rep, nil
}

// lostAndFound is the directory mkfs.ext4 puts at the root of every ext4
// volume, gasha01-rbd's included.
const lostAndFound = "lost+found"

// volumeEmpty reports whether the session volume holds nothing a pod wrote:
// at most an empty lost+found and the shared volume's own mount point, whose
// content is not on the session volume. That is what a volume whose pod never
// started looks like, and agentd's boot writes ~/.agentd before anything else,
// so no agent ever ran on it. It is the one case where a volume without the
// session's clone proves it holds no work (D-51, D-55). Anything else, one
// file or an unreadable lost+found, is not empty, and the rescue goes on as
// usual.
func volumeEmpty(s Settings) (bool, error) {
	entries, err := os.ReadDir(s.Home)
	if err != nil {
		return false, err
	}
	shared := filepath.Clean(s.SharedDir)
	for _, e := range entries {
		p := filepath.Join(s.Home, e.Name())
		switch {
		case e.Name() == lostAndFound && e.IsDir():
			inner, err := os.ReadDir(p)
			if err != nil || len(inner) > 0 {
				return false, nil
			}
		case p == shared && e.IsDir() && sharedIsMounted(p, s.Home) == nil:
		default:
			return false, nil
		}
	}
	return true, nil
}

// RescueOptions are `agentd ctl rescue`'s flags.
type RescueOptions struct {
	// WorkspaceStopProof is fresh controller input for a distinct hold Pod,
	// supplied through bounded stdin on every shared rescue attempt.
	WorkspaceStopProof *protocol.WorkspaceStopProof
	// StopAgent stops the agent CLI before the rescue (--stop-agent, D-48).
	StopAgent bool
	// StopGrace is how long the stop waits after SIGTERM before SIGKILL.
	StopGrace time.Duration
}

// clonesIn lists the git clones directly under dir, skipping hidden entries
// (a clone in progress lives in a dot directory).
func clonesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if isNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if fi, err := os.Stat(filepath.Join(p, ".git")); err == nil && fi.IsDir() {
			out = append(out, p)
		}
	}
	return out, nil
}

func rescueRepo(ctx context.Context, r Runner, s Settings, repo, stamp string) protocol.RepoRescue {
	rr := protocol.RepoRescue{Path: repo, Worktrees: []protocol.WorktreeRescue{}}
	fctx, cancel := context.WithTimeout(ctx, rescueFetchTimeout)
	_, err := s.git(fctx, r, repo, "fetch", "--prune", "--quiet", "origin")
	cancel()
	if err != nil {
		// Stale remote refs can only make more refs look unpushed, the safe
		// direction; but they prove nothing, so CleanAndPushed stays false.
		rr.FetchError = cmdDetail(err)
	} else {
		rr.Fetched = true
	}
	wts, err := listWorktrees(ctx, r, s, repo)
	if err != nil {
		rr.Error = "worktree list: " + cmdDetail(err)
		return rr
	}
	for _, wt := range wts {
		rr.Worktrees = append(rr.Worktrees, rescueWorktree(ctx, r, s, wt, stamp))
	}
	refs, err := unpushedRefs(ctx, r, s, repo)
	if err != nil {
		rr.Error = "unpushed refs: " + cmdDetail(err)
		return rr
	}
	rr.UnpushedRefs = refs
	return rr
}

// listWorktrees parses `git worktree list --porcelain`, leaving out bare and
// prunable entries (a worktree whose directory is gone).
func listWorktrees(ctx context.Context, r Runner, s Settings, repo string) ([]string, error) {
	out, err := s.git(ctx, r, repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, block := range strings.Split(out, "\n\n") {
		var path string
		skip := false
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "worktree "):
				path = strings.TrimPrefix(line, "worktree ")
			case line == "bare", strings.HasPrefix(line, "prunable"):
				skip = true
			}
		}
		if path != "" && !skip {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func rescueWorktree(ctx context.Context, r Runner, s Settings, wt, stamp string) protocol.WorktreeRescue {
	w := protocol.WorktreeRescue{Path: wt}
	head, err := s.git(ctx, r, wt, "rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		w.Refused = "no HEAD commit"
		return w
	}
	w.Head = head
	if b, err := s.git(ctx, r, wt, "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		w.Branch = b
	}
	gitDir, err := s.git(ctx, r, wt, "rev-parse", "--absolute-git-dir")
	if err != nil {
		w.Refused = "cannot resolve its git dir: " + cmdDetail(err)
		return w
	}
	for _, f := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		if exists(filepath.Join(gitDir, f)) {
			w.Refused = "a merge or rebase is in progress (" + f + ")"
			return w
		}
	}
	// Refuse a submodule holding work the rescue cannot reach, so the volume
	// is kept and a human decides.
	if why, err := submoduleProblem(ctx, r, wt); err != nil {
		w.Refused = "git submodule status failed: " + cmdDetail(err)
		return w
	} else if why != "" {
		w.Refused = why + "; rescue does not reach into submodules"
		return w
	}
	// --no-optional-locks: never take the index lock from a running agent.
	st, err := s.git(ctx, r, wt, "--no-optional-locks", "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		w.Refused = "git status failed: " + cmdDetail(err)
		return w
	}
	w.Dirty = st != ""

	if w.Dirty {
		if why := untrackedProblem(ctx, r, s, wt); why != "" {
			w.Refused = why
			return w
		}
		branch, err := commitWIP(ctx, r, s, wt, gitDir, w, stamp)
		if err != nil {
			w.Refused = err.Error()
			return w
		}
		w.RescueBranch = branch
		return w
	}
	if w.Branch == "" {
		// A clean detached HEAD loses nothing unless its commit is on no ref.
		on, err := s.git(ctx, r, wt, "for-each-ref", "--contains", head, "--count=1", "refs/heads", "refs/remotes", "refs/tags")
		if err != nil {
			w.Refused = "for-each-ref failed: " + cmdDetail(err)
			return w
		}
		if on == "" {
			branch, err := createRescueRef(ctx, r, s, wt, stamp, head)
			if err != nil {
				w.Refused = err.Error()
				return w
			}
			w.RescueBranch = branch
		}
	}
	return w
}

// submoduleProblem names a reason to refuse a worktree because of a
// submodule, or returns "". A submodule's own edits and commits live in its
// own repo, which the rescue commit (it records only the gitlink) and the ref
// list never reach. So a submodule passes only when it is clean and origin has
// everything it holds: not drifted from the recorded commit (+) or conflicted
// (U), no uncommitted change, no stash, and no branch, tag or HEAD commit that
// its origin lacks. One never initialized (-) holds nothing of its own.
func submoduleProblem(ctx context.Context, r Runner, wt string) (string, error) {
	if !exists(filepath.Join(wt, ".gitmodules")) {
		return "", nil
	}
	env := append(append([]string(nil), gitEnv...), "GIT_OPTIONAL_LOCKS=0")
	gitOut := func(dir string, args ...string) (string, error) {
		res, err := r.Run(ctx, Cmd{Name: "git", Args: append([]string{"-C", dir}, args...), Env: env})
		return strings.TrimSpace(string(res.Stdout)), err
	}
	out, err := gitOut(wt, "submodule", "status", "--recursive")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		state := line[0]
		f := strings.Fields(line[1:])
		if len(f) < 2 {
			continue
		}
		path := f[1]
		switch state {
		case '-':
			continue
		case '+', 'U':
			return fmt.Sprintf("submodule %s is not at its recorded commit", path), nil
		}
		sub := filepath.Join(wt, path)
		if st, err := gitOut(sub, "status", "--porcelain=v1", "--untracked-files=all"); err != nil || st != "" {
			return fmt.Sprintf("submodule %s has uncommitted changes", path), nil
		}
		if st, err := gitOut(sub, "stash", "list"); err != nil || st != "" {
			return fmt.Sprintf("submodule %s has a stash", path), nil
		}
		if extra, err := gitOut(sub, "rev-list", "-n", "1", "HEAD", "--branches", "--tags", "--not", "--remotes=origin"); err != nil || extra != "" {
			return fmt.Sprintf("submodule %s has commits its origin lacks", path), nil
		}
	}
	return "", nil
}

// untrackedProblem is v1's rescue_untracked_ok: it names a reason to refuse,
// or returns "".
func untrackedProblem(ctx context.Context, r Runner, s Settings, wt string) string {
	res, err := r.Run(ctx, Cmd{Name: "git", Args: []string{"-C", wt, "ls-files", "-o", "--exclude-standard", "-z"}, Env: gitEnv})
	if err != nil {
		return "git ls-files failed: " + cmdDetail(err)
	}
	var total int64
	for _, name := range bytes.Split(res.Stdout, []byte{0}) {
		if len(name) == 0 {
			continue
		}
		// ls-files lists an untracked nested repo as "dir/": a commit would
		// record an empty gitlink while the repo itself stayed behind.
		if bytes.HasSuffix(name, []byte("/")) {
			return "an untracked nested git repo: " + string(name)
		}
		if fi, err := os.Lstat(filepath.Join(wt, string(name))); err == nil {
			total += fi.Size()
		}
	}
	if total > rescueUntrackedCap {
		return fmt.Sprintf("%d MiB of untracked files (cap %d MiB)", total>>20, rescueUntrackedCap>>20)
	}
	return ""
}

// commitWIP commits the worktree's state through a copy of its index, so the
// worktree, its index and its branch stay untouched.
func commitWIP(ctx context.Context, r Runner, s Settings, wt, gitDir string, w protocol.WorktreeRescue, stamp string) (string, error) {
	tmp, err := os.CreateTemp(s.StateDir, "rescue-index-*")
	if err != nil {
		return "", err
	}
	index := tmp.Name()
	defer func() { _ = os.Remove(index) }()
	src, err := os.Open(filepath.Join(gitDir, "index"))
	switch {
	case err == nil:
		_, err = io.Copy(tmp, src)
		_ = src.Close()
		if err != nil {
			_ = tmp.Close()
			return "", err
		}
		if err := tmp.Close(); err != nil {
			return "", err
		}
	case isNotExist(err):
		// git refuses a zero-byte index file but reads a missing one as empty.
		_ = tmp.Close()
		if err := os.Remove(index); err != nil {
			return "", err
		}
	default:
		_ = tmp.Close()
		return "", err
	}
	env := append(append([]string(nil), gitEnv...), "GIT_INDEX_FILE="+index)
	run := func(args ...string) (string, error) {
		res, err := r.Run(ctx, Cmd{Name: "git", Args: append([]string{"-C", wt}, args...), Env: env})
		if err != nil {
			return "", fmt.Errorf("git %s: %s", args[0], cmdDetail(err))
		}
		return strings.TrimSpace(string(res.Stdout)), nil
	}
	if _, err := run("add", "-A"); err != nil {
		return "", err
	}
	tree, err := run("write-tree")
	if err != nil {
		return "", err
	}
	was := w.Branch
	if was == "" {
		was = "a detached HEAD"
	}
	msg := fmt.Sprintf("wip: rescued by agentd from %s (was on %s @ %s)", wt, was, short(w.Head))
	commit, err := run("commit-tree", tree, "-p", w.Head, "-m", msg)
	if err != nil {
		return "", err
	}
	return createRescueRef(ctx, r, s, wt, stamp, commit)
}

// createRescueRef creates refs/heads/rescue/<worktree>-<stamp> at commit,
// failing rather than moving a ref that exists; a second rescue in the same
// minute gets a numeric suffix.
func createRescueRef(ctx context.Context, r Runner, s Settings, wt, stamp, commit string) (string, error) {
	base := "rescue/" + filepath.Base(wt) + "-" + stamp
	if s.WorkspaceID != "" {
		// The slash bounds the namespace: task a cannot snapshot a peer
		// named a-b through a common string prefix.
		base = "rescue/" + filepath.Base(wt) + "/" + stamp
	}
	var lastErr error
	for i := 1; i <= 20; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		if _, err := s.git(ctx, r, wt, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			continue
		}
		// The empty old value makes update-ref refuse a ref that exists.
		if _, err := s.git(ctx, r, wt, "update-ref", "-m", "agentd rescue", "refs/heads/"+name, commit, ""); err != nil {
			lastErr = err
			continue
		}
		return name, nil
	}
	if lastErr == nil {
		lastErr = errors.New("every name is taken")
	}
	return "", fmt.Errorf("could not create a rescue branch %s: %s", base, cmdDetail(lastErr))
}

// unpushedRefs lists the local branches and tags, and every stash entry,
// whose commits origin's refs do not contain. refs/stash names only the newest
// entry; the older ones live only in its reflog, so each entry is listed as
// stash@{n}, which step 5 anchors before it bundles.
func unpushedRefs(ctx context.Context, r Runner, s Settings, repo string) ([]protocol.Ref, error) {
	out, err := s.git(ctx, r, repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, err
	}
	var cands []protocol.Ref
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		if sha, name, ok := strings.Cut(sc.Text(), " "); ok {
			cands = append(cands, protocol.Ref{Name: name, Commit: sha})
		}
	}
	stashes, err := s.git(ctx, r, repo, "stash", "list", "--format=%H")
	if err != nil {
		return nil, err
	}
	n := 0
	for _, sha := range strings.Fields(stashes) {
		cands = append(cands, protocol.Ref{Name: fmt.Sprintf("stash@{%d}", n), Commit: sha})
		n++
	}
	var refs []protocol.Ref
	for _, ref := range cands {
		extra, err := s.git(ctx, r, repo, "rev-list", "-n", "1", ref.Commit, "--not", "--remotes=origin")
		if err != nil {
			return nil, err
		}
		if extra != "" {
			refs = append(refs, ref)
		}
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// lockFile takes an exclusive lock, or fails at once if another process holds
// it.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errors.New("another rescue is running in this pod")
		}
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
