package agentd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const workspaceVersion = 1

// Shared preparation and rescue each bound their complete locked Git section,
// including token wait, clone retries/backoff, worktree and bundle commands.
// A queued administrator covers that complete bound and the
// runner's pipe-drain allowance, while a shorter caller deadline still wins.
const (
	sharedGitPrepareBudget = 2 * time.Minute
	sharedGitAdminBudget   = sharedGitPrepareBudget + 10*time.Second
)

// The marker is provisioned with the retained claim. agentd never creates or
// repairs it: an ordinary home directory must not masquerade as shared storage.
type workspaceMarker struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
}

type taskOwner struct {
	Version    int       `json:"version"`
	Workspace  string    `json:"workspace"`
	Task       string    `json:"task"`
	Repo       string    `json:"repo"`
	Clone      string    `json:"clone"`
	Worktree   string    `json:"worktree"`
	SessionUID string    `json:"sessionUID"`
	PodUID     string    `json:"podUID"`
	Generation uint64    `json:"generation"`
	State      string    `json:"state"`
	Launched   bool      `json:"launched"`
	UpdatedAt  time.Time `json:"updatedAt"`
	StoppedAt  time.Time `json:"stoppedAt,omitempty"`
	StopReason string    `json:"stopReason,omitempty"`
}

func (o *taskOwner) UnmarshalJSON(data []byte) error {
	// A truncated/edited receipt cannot silently default `launched` to
	// false and become stop proof. Every identity and admission field is
	// mandatory, including false booleans.
	type plain taskOwner
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"version", "workspace", "task", "repo", "clone", "worktree", "sessionUID", "podUID", "generation", "state", "launched", "updatedAt"} {
		value, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("owner receipt lacks %s", name)
		}
	}
	*o = taskOwner(decoded)
	return nil
}

// writerLease protects a task for the daemon's lifetime. Releasing the kernel
// lock never changes the durable owner record or proves its writers stopped.
type writerLease struct {
	owner  taskOwner
	unlock func()
}

func (s Settings) workspaceDir() string { return filepath.Join(s.Home, ".workspace") }
func (s Settings) ownerPath(task string) string {
	return filepath.Join(s.workspaceDir(), "tasks", task+".json")
}

var readWorkspaceMountInfo = func() ([]byte, error) { return os.ReadFile("/proc/self/mountinfo") }

// workspacePreflight verifies every literal mount, its subpath and the claim's
// provisioned identity before any shared Git administration or file creation.
func workspacePreflight(s Settings, sess protocol.Session) error {
	if s.WorkspaceID == "" && sess.Workspace == nil {
		return nil
	}
	if sess.Workspace == nil || s.WorkspaceID == "" || sess.Workspace.ID != s.WorkspaceID || s.PodUID == "" {
		return errors.New("shared workspace requires matching operator and session bindings and a Pod UID")
	}
	if err := sess.Validate(); err != nil {
		return err
	}
	for _, p := range []string{s.Home, s.workspaceDir(), s.ReposDir(), filepath.Join(s.Home, "codex"), s.WorkDir()} {
		if err := noSymlinkComponents(p); err != nil {
			return fmt.Errorf("workspace path %s: %w", p, err)
		}
	}
	data, err := readWorkspaceMountInfo()
	if err != nil {
		return fmt.Errorf("workspace mount identity: %w", err)
	}
	if err := verifyWorkspaceMounts(s.Home, data); err != nil {
		return err
	}
	var marker workspaceMarker
	if err := readWorkspaceJSON(filepath.Join(s.workspaceDir(), "marker.json"), &marker); err != nil {
		return fmt.Errorf("workspace marker: %w", err)
	}
	if marker.Version != workspaceVersion || marker.ID != s.WorkspaceID {
		return errors.New("workspace marker does not match the operator binding")
	}
	return nil
}

type workspaceMount struct{ device, root, fs, source string }

func verifyWorkspaceMounts(home string, data []byte) error {
	mounts := map[string]workspaceMount{}
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	for scan.Scan() {
		parts := strings.Split(scan.Text(), " - ")
		if len(parts) != 2 {
			return errors.New("unreadable workspace mount table")
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			return errors.New("unreadable workspace mount entry")
		}
		decode := func(v string) string {
			return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(v)
		}
		path := decode(left[4])
		if _, exists := mounts[path]; exists {
			return fmt.Errorf("ambiguous mount at %s", path)
		}
		mounts[path] = workspaceMount{left[2], decode(left[3]), right[0], decode(right[1])}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	base, ok := mounts[filepath.Join(home, ".workspace")]
	if !ok || filepath.Base(base.root) != "metadata" {
		return errors.New("workspace metadata is not its own expected subpath mount")
	}
	private, ok := mounts[home]
	if !ok || private.device == base.device {
		return errors.New("workspace must be a separate mount from the private provider home")
	}
	parent := filepath.Dir(base.root)
	for _, sub := range []string{"repos", "codex", "work"} {
		m, ok := mounts[filepath.Join(home, sub)]
		if !ok || m.device != base.device || m.fs != base.fs || m.source != base.source || m.root != filepath.Join(parent, sub) {
			return fmt.Errorf("workspace %s is not the same claim's expected literal subpath mount", sub)
		}
	}
	return nil
}

func noSymlinkComponents(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path is not absolute and clean")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		fi, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink components are refused")
		}
	}
	return nil
}

func readWorkspaceJSON(path string, dst any) error {
	if err := noSymlinkComponents(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
		return errors.New("workspace record is not a bounded regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("workspace record has trailing data")
	}
	return nil
}

func workspaceLock(s Settings, name string) (func(), error) {
	dir := filepath.Join(s.workspaceDir(), "locks")
	if err := ensureWorkspaceDirectory(dir); err != nil {
		return nil, err
	}
	if err := noSymlinkComponents(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		return nil, errors.New("workspace lock is not a regular file")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("workspace %s is busy or lock ownership is uncertain: %w", name, err)
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// The key is the expected common Git directory, not the worktree's private
// .git pointer. It also serializes the initial clone before that directory exists.
func workspaceAdminLock(ctx context.Context, s Settings, repo string) (func(), error) {
	key := sha256.Sum256([]byte(filepath.Join(s.ClonePath(repo), ".git")))
	caller := ctx
	ctx, cancel := context.WithTimeout(ctx, sharedGitAdminBudget)
	defer cancel()
	name := "git-" + hex.EncodeToString(key[:])
	for {
		if err := ctx.Err(); err != nil {
			return nil, workspaceAdminWaitFailure(caller, ctx)
		}
		unlock, err := workspaceLock(s, name)
		if err == nil {
			return unlock, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, workspaceAdminWaitFailure(caller, ctx)
		case <-timer.C:
		}
	}
}

// Only this local administrative deadline is retryable before owner admission.
// Caller cancellation/deadline, task-writer contention and uncertain receipts
// remain ordinary failures and never acquire retry authority from this type.
type workspaceAdminWaitTimeout struct{}

func (*workspaceAdminWaitTimeout) Error() string { return "shared Git administrative wait timed out" }
func (*workspaceAdminWaitTimeout) Unwrap() error { return context.DeadlineExceeded }

func workspaceAdminWaitFailure(caller, wait context.Context) error {
	if err := caller.Err(); err != nil {
		return fmt.Errorf("shared Git administration lock wait: %w", err)
	}
	if errors.Is(wait.Err(), context.DeadlineExceeded) {
		return &workspaceAdminWaitTimeout{}
	}
	return fmt.Errorf("shared Git administration lock wait: %w", wait.Err())
}

const workspaceAdmissionAttempts = 3

func retryWorkspaceWriterAdmission(ctx context.Context, acquire func() (*writerLease, error), pause func(context.Context, time.Duration) error) (*writerLease, error) {
	for attempt := 1; attempt <= workspaceAdmissionAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		writer, err := acquire()
		if err == nil {
			return writer, nil
		}
		var timeout *workspaceAdminWaitTimeout
		if !errors.As(err, &timeout) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == workspaceAdmissionAttempts {
			return nil, fmt.Errorf("shared administrative wait exhausted after %d admission attempts: %w", attempt, err)
		}
		// acquireWorkspaceWriter releases its task lock on every failure.
		// The next attempt reruns all mount and receipt identity checks.
		if err := pause(ctx, time.Duration(attempt)*time.Second); err != nil {
			return nil, err
		}
	}
	panic("unreachable admission attempt bound")
}

func pauseWorkspaceAdmission(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func acquireWorkspaceWriter(ctx context.Context, r Runner, s Settings, sess protocol.Session, now time.Time) (*writerLease, error) {
	if err := workspacePreflight(s, sess); err != nil {
		return nil, err
	}
	if sess.Workspace == nil {
		return nil, nil
	}
	unlock, err := workspaceLock(s, "writer-"+sess.Name)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*writerLease, error) { unlock(); return nil, err }
	admin, err := workspaceAdminLock(ctx, s, sess.Repo)
	if err != nil {
		return fail(err)
	}
	defer admin()
	// Initial admission also reads common Git identity and branch existence.
	// Keep those commands within the same maximum as other shared holders.
	ctx, cancelGit := context.WithTimeout(ctx, sharedGitPrepareBudget)
	defer cancelGit()
	owner := taskOwner{Version: workspaceVersion, Workspace: s.WorkspaceID, Task: sess.Name, Repo: sess.Repo,
		Clone: s.ClonePath(sess.Repo), Worktree: s.WorktreePath(sess.Name), SessionUID: sess.Workspace.SessionUID,
		PodUID: s.PodUID, Generation: 1, State: "owned", UpdatedAt: now.UTC()}
	var old taskOwner
	err = readWorkspaceJSON(s.ownerPath(sess.Name), &old)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := validateSharedClone(ctx, r, s, sess.Repo); err != nil {
			return fail(err)
		}
		if _, err := os.Lstat(owner.Clone); err == nil {
			_, err = s.git(ctx, r, owner.Clone, "show-ref", "--verify", "--quiet", "refs/heads/agent/"+sess.Name)
			if ExitCodeOf(err) != 1 {
				return fail(errors.New("an existing or uncertain task branch without a durable owner cannot be claimed"))
			}
		}
		if _, err := os.Lstat(owner.Worktree); !errors.Is(err, os.ErrNotExist) {
			return fail(errors.New("an existing task worktree without a durable owner cannot be claimed"))
		}
		// Do not adopt a private session's launch or migrate its WIP.
		for _, file := range []string{launchFile, resumeFile, pidFile} {
			if _, err := os.Lstat(s.statePath(file)); !errors.Is(err, os.ErrNotExist) {
				return fail(errors.New("private or uncertain launch state cannot be adopted into a shared workspace"))
			}
		}
	case err != nil:
		return fail(fmt.Errorf("uncertain task ownership: %w", err))
	default:
		if err := ownerMatches(s, sess, old, false); err != nil {
			return fail(err)
		}
		switch old.State {
		case "owned":
			if old.PodUID != s.PodUID || old.Launched {
				return fail(errors.New("previous task writers are not proven stopped; a free lock is not stop proof"))
			}
			owner = old
			owner.UpdatedAt = now.UTC()
		case "stopped":
			if old.PodUID == s.PodUID {
				return fail(errors.New("a new Pod UID and durable stopped receipt are required to resume"))
			}
			if old.Generation == ^uint64(0) {
				return fail(errors.New("writer generation exhausted"))
			}
			owner.Generation = old.Generation + 1
		default:
			return fail(errors.New("unknown task ownership state; takeover refused"))
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := writeWorkspaceJSON(s.ownerPath(sess.Name), owner); err != nil {
		return fail(err)
	}
	return &writerLease{owner: owner, unlock: unlock}, nil
}

func ownerMatches(s Settings, sess protocol.Session, owner taskOwner, pod bool) error {
	if sess.Workspace == nil || owner.Version != workspaceVersion || owner.Workspace != s.WorkspaceID || owner.Workspace != sess.Workspace.ID ||
		owner.Task != sess.Name || owner.Repo != sess.Repo || owner.Clone != s.ClonePath(sess.Repo) || owner.Worktree != s.WorktreePath(sess.Name) ||
		owner.SessionUID != sess.Workspace.SessionUID || owner.Generation == 0 || owner.PodUID == "" || owner.UpdatedAt.IsZero() || (pod && owner.PodUID != s.PodUID) {
		return errors.New("durable task owner does not match this workspace, task, session and Pod; transfer refused")
	}
	switch owner.State {
	case "owned":
		if !owner.StoppedAt.IsZero() || owner.StopReason != "" {
			return errors.New("owned receipt contains contradictory stop proof")
		}
	case "stopped":
		if owner.Launched || owner.StoppedAt.IsZero() || owner.StopReason != "no-agent-admitted" {
			return errors.New("stopped receipt lacks a supported proof; takeover refused")
		}
	default:
		return errors.New("unknown owner state; takeover refused")
	}
	return nil
}

// admitWorkspaceLaunch reserves the exact generation before tmux starts.
// run-agent rechecks it and durably marks launched before any writer starts.
func admitWorkspaceLaunch(ctx context.Context, s Settings, sess protocol.Session, launch *Launch) error {
	if sess.Workspace == nil && s.WorkspaceID == "" {
		return nil
	}
	if s.writer == nil {
		return errors.New("shared launch requires the daemon's writer lock")
	}
	if err := workspacePreflight(s, sess); err != nil {
		return err
	}
	unlock, err := workspaceAdminLock(ctx, s, sess.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		return err
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return err
	}
	if owner.State != "owned" || owner.Launched || owner.Generation != s.writer.owner.Generation {
		return errors.New("writer receipt changed before launch")
	}
	launch.WorkspaceOwner = &owner
	return nil
}

func validateWorkspaceLaunch(l Launch) error {
	if l.WorkspaceOwner == nil && os.Getenv("AGENTD_WORKSPACE_ID") == "" {
		return nil
	}
	s, err := LoadSettings(os.Getenv)
	if err != nil {
		return err
	}
	sess, err := LoadSession(os.Getenv)
	if err != nil {
		return err
	}
	if err := workspacePreflight(s, sess); err != nil {
		return err
	}
	if l.WorkspaceOwner == nil || l.Session != sess.Name || l.Dir != s.WorktreePath(sess.Name) {
		return errors.New("launch does not carry the admitted shared task")
	}
	return finalizeWorkspaceLaunch(s, sess, l, time.Now())
}

func finalizeWorkspaceLaunch(s Settings, sess protocol.Session, l Launch, now time.Time) error {
	unlock, err := workspaceAdminLock(context.Background(), s, sess.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		return err
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return err
	}
	if l.WorkspaceOwner == nil || owner.State != "owned" || owner.Launched || owner != *l.WorkspaceOwner {
		return errors.New("writer receipt no longer matches the admitted launch")
	}
	owner.Launched, owner.UpdatedAt = true, now.UTC()
	return writeWorkspaceJSON(s.ownerPath(sess.Name), owner)
}

// validateSharedClone rejects symlinked or foreign Git administration before
// fetch/worktree operations. A nonexistent clone is the only initialization case.
func validateSharedClone(ctx context.Context, r Runner, s Settings, repo string) error {
	clone := s.ClonePath(repo)
	if _, err := os.Lstat(clone); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := noSymlinkComponents(filepath.Join(clone, ".git")); err != nil {
		return err
	}
	common, err := s.git(ctx, r, clone, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Clean(common) != filepath.Join(clone, ".git") {
		return errors.New("shared clone has an unexpected common Git directory")
	}
	top, err := s.git(ctx, r, clone, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != clone {
		return errors.New("shared clone has an unexpected root")
	}
	origin, err := s.git(ctx, r, clone, "remote", "get-url", "origin")
	if err != nil || origin != s.RemoteURL(repo) {
		return errors.New("shared clone has an unexpected origin")
	}
	return nil
}

// Stop/ownership receipts use file fsync, atomic rename, and directory fsync.
// Ordinary private state files deliberately retain their existing behavior.
func writeWorkspaceJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := ensureWorkspaceDirectory(dir); err != nil {
		return err
	}
	if err := noSymlinkComponents(dir); err != nil {
		return err
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return errors.New("workspace receipt is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".receipt-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}

func ensureWorkspaceDirectory(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := noSymlinkComponents(dir); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}
