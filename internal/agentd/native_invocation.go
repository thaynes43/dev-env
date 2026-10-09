package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const nativeInvocationFile = "native-invocation.json"

type nativeStarted struct {
	Version int           `json:"version"`
	Binding nativeBinding `json:"binding"`
	Pid     int           `json:"pid"`
	Start   string        `json:"start"`
}

// A native binding names one invocation of the same admitted task writer. It
// grants no stop, takeover, transfer or new-task authority.
type nativeBinding struct {
	InvocationID string `json:"invocationID"`
	LaunchDigest string `json:"launchDigest"`
	Session      string `json:"session"`
	SessionUID   string `json:"sessionUID"`
	PodUID       string `json:"podUID"`
	BootID       string `json:"bootID"`
	ThreadID     string `json:"threadID"`
	Generation   uint64 `json:"generation"`
}

type nativeInvocation struct {
	Version      int           `json:"version"`
	Binding      nativeBinding `json:"binding"`
	Phase        string        `json:"phase"`
	StartedAt    time.Time     `json:"startedAt"`
	FinishedAt   time.Time     `json:"finishedAt"`
	LeaderReaped bool          `json:"leaderReaped"`
	GroupAbsent  bool          `json:"groupAbsent"`
}

// The owning daemon supplies a digest of the genuine previous result. The
// next run-agent consumes that result durably before its own native spawn.
type nativeContinuation struct {
	Source        nativeBinding `json:"source"`
	ReceiptDigest string        `json:"receiptDigest"`
}

type nativeInvocationLease struct {
	s      Settings
	sess   protocol.Session
	unlock func()
}

func (lease *nativeInvocationLease) recordStarted(l Launch, pid int) error {
	if lease == nil {
		return nil
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return err
	}
	start := procStartTime(pid)
	if start == "" {
		return errors.New("native process start identity is unavailable")
	}
	return writeWorkspaceJSON(lease.s.statePath("native-started.json"), nativeStarted{1, binding, pid, start})
}

func ownedNativeStarted(s Settings, l Launch) error {
	var started nativeStarted
	if err := readPrivateManagedJSON(s, "native-started.json", 16<<10, &started); err != nil {
		return err
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return err
	}
	prior := binding
	prior.ThreadID = started.Binding.ThreadID
	if started.Version != 1 || started.Binding != prior || !pidAlive(agentPid{started.Pid, started.Start}) {
		return errors.New("native startup lacks its exact owned live process")
	}
	pgid, err := syscall.Getpgid(started.Pid)
	if err != nil || pgid != started.Pid {
		return errors.New("native process-group identity changed")
	}
	return nil
}

func readPrivateManagedJSON(s Settings, file string, bound int64, target any) error {
	if err := privateManagedState(s); err != nil {
		return err
	}
	f, err := os.OpenFile(s.statePath(file), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > bound {
		return errors.New("platform state is not a bounded private regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, bound))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("private platform state is invalid")
	}
	return nil
}

func privateCurrentLaunch(s Settings) (Launch, error) {
	var boot bootRecord
	if err := readPrivateManagedJSON(s, bootFile, 64<<10, &boot); err != nil || boot.BootID == "" {
		return Launch{}, errors.New("current private boot identity is unavailable")
	}
	for _, file := range []string{resumeFile, launchFile} {
		var l Launch
		err := readPrivateManagedJSON(s, file, 512<<10, &l)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Launch{}, err
		}
		if l.BootID == boot.BootID {
			return l, nil
		}
	}
	return Launch{}, errors.New("this boot has no private launch")
}

func privateManagedState(s Settings) error {
	if !strings.HasPrefix(s.StateDir, s.Home+string(filepath.Separator)) || noSymlinkComponents(s.StateDir) != nil {
		return errors.New("managed native state must be a private home directory")
	}
	fi, err := os.Stat(s.StateDir)
	if err != nil || !fi.IsDir() || fi.Mode().Perm()&0o077 != 0 {
		return errors.New("managed native state directory is not private")
	}
	for _, shared := range []string{s.ReposDir(), s.WorkDir(), filepath.Join(s.Home, "codex"), s.workspaceDir()} {
		if s.StateDir == shared || strings.HasPrefix(s.StateDir, shared+string(filepath.Separator)) {
			return errors.New("managed native state cannot be a shared path")
		}
	}
	return nil
}

// This is a PRIVATE invocation lock. The daemon's lifetime shared writer lease
// stays unchanged. O_CLOEXEC and no ExtraFiles keep the native CLI from inheriting
// it; a released lock is exclusion only, never evidence of an earlier Wait.
func nativeInvocationLock(s Settings) (func(), error) {
	return privateNativeLock(s, "native-invocation.lock")
}

func privateNativeLock(s Settings, name string) (func(), error) {
	if err := privateManagedState(s); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.statePath(name), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		_ = f.Close()
		return nil, errors.New("private native lock is not a 0600 regular file")
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("private native state is locked or uncertain")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func readNativeInvocation(s Settings) (nativeInvocation, error) {
	var result nativeInvocation
	if err := privateManagedState(s); err != nil {
		return result, err
	}
	f, err := os.OpenFile(s.statePath(nativeInvocationFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return result, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > 16<<10 {
		return result, errors.New("native invocation result is not a bounded private regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		return result, err
	}
	if d.Decode(new(any)) != io.EOF {
		return result, errors.New("native invocation result has trailing data")
	}
	return result, nil
}

func nativeReceiptDigest(result nativeInvocation) string {
	b, _ := json.Marshal(result)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func invocationBinding(l Launch) (nativeBinding, error) {
	if !l.ChildDecisions || l.Provider != protocol.AgentCodex || !nativeThreadID.MatchString(l.NativeInvocationID) ||
		l.Session == "" || l.SessionUID == "" || l.PodUID == "" || l.BootID == "" || l.CreatedAt.IsZero() || l.WorkspaceOwner == nil ||
		l.WorkspaceOwner.Generation == 0 || l.WorkspaceOwner.PodUID != l.PodUID || l.WorkspaceOwner.SessionUID != l.SessionUID {
		return nativeBinding{}, errors.New("native invocation lacks exact platform task bindings")
	}
	// Thread confirmation is persisted during the first exec. Exclude that
	// mutable confirmation from the original launch digest, but bind it explicitly
	// in the exit result and every continuation proof.
	copy := l
	copy.ConversationID, copy.NativeThreadConfirmed, copy.Continuation = "", false, nil
	b, err := json.Marshal(copy)
	if err != nil {
		return nativeBinding{}, err
	}
	h := sha256.Sum256(b)
	return nativeBinding{l.NativeInvocationID, hex.EncodeToString(h[:]), l.Session, l.SessionUID, l.PodUID, l.BootID, l.ConversationID, l.WorkspaceOwner.Generation}, nil
}

func beginNativeInvocation(l Launch, stateDir string) (*nativeInvocationLease, error) {
	if !l.ChildDecisions {
		if l.Continuation != nil {
			return nil, errors.New("native continuation requires explicit child-decision admission")
		}
		return nil, nil
	}
	s, err := LoadSettings(os.Getenv)
	if err != nil {
		return nil, err
	}
	sess, err := LoadSession(os.Getenv)
	if err != nil {
		return nil, err
	}
	if !s.ManagedChildDecisions || !s.ManagedCodexTasks || sess.Agent != protocol.AgentCodex || sess.Mode != protocol.ModeTask ||
		sess.SessionUID != l.SessionUID || sess.Name != l.Session || s.PodUID != l.PodUID || s.StateDir != stateDir {
		return nil, errors.New("native invocation differs from this enabled private executor")
	}
	if _, err := invocationBinding(l); err != nil {
		return nil, err
	}
	unlock, err := nativeInvocationLock(s)
	if err != nil {
		return nil, err
	}
	return &nativeInvocationLease{s, sess, unlock}, nil
}

func (lease *nativeInvocationLease) beforeSpawn(l Launch, now time.Time) error {
	if lease == nil {
		return validateWorkspaceLaunch(l)
	}
	if l.Continuation != nil {
		return finalizeNativeContinuation(lease.s, lease.sess, l, now)
	}
	if _, err := readNativeInvocation(lease.s); !errors.Is(err, os.ErrNotExist) {
		return errors.New("an existing or uncertain native invocation cannot restart its initial prompt")
	}
	if err := validateWorkspaceLaunch(l); err != nil {
		return err
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return err
	}
	return writeWorkspaceJSON(lease.s.statePath(nativeInvocationFile), nativeInvocation{Version: 1, Binding: binding, Phase: "Running", StartedAt: now.UTC()})
}

// Called ONLY after this run-agent's own cmd.Wait returned and reaped its
// direct native leader. A process-group kill is a request; bounded observation
// of exact group absence is recorded independently and grants no transfer proof.
func (lease *nativeInvocationLease) afterWait(l Launch, state *os.ProcessState, pgid int, now time.Time) error {
	if lease == nil {
		return nil
	}
	result, err := readNativeInvocation(lease.s)
	if err != nil {
		return err
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return err
	}
	prior := binding
	prior.ThreadID = result.Binding.ThreadID
	if result.Phase != "Running" || result.Binding != prior || state == nil || pgid <= 0 || state.Pid() != pgid {
		return errors.New("owned native Wait result does not bind the running invocation")
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
	result.Binding, result.LeaderReaped, result.FinishedAt = binding, true, now.UTC()
	result.GroupAbsent = waitNativeGroupAbsent(pgid, time.Second)
	result.Phase = "Uncertain"
	if result.GroupAbsent && l.NativeThreadConfirmed && nativeThreadID.MatchString(binding.ThreadID) {
		result.Phase = "Exited"
	}
	if err := writeWorkspaceJSON(lease.s.statePath(nativeInvocationFile), result); err != nil {
		return err
	}
	if result.Phase != "Exited" {
		return errors.New("native exit or exact process-group absence is unconfirmed")
	}
	return nil
}

func waitNativeGroupAbsent(pgid int, bound time.Duration) bool {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-tick.C:
		}
	}
}

func validNativeExit(result nativeInvocation, source nativeBinding) bool {
	return result.Version == 1 && result.Phase == "Exited" && result.Binding == source && result.LeaderReaped && result.GroupAbsent &&
		!result.StartedAt.IsZero() && !result.FinishedAt.Before(result.StartedAt) && nativeThreadID.MatchString(source.ThreadID)
}

// The daemon still owns its original shared writer lease. This producer admits
// sequential continuation only; it neither resets Launched nor assigns a new
// writer generation. The run-agent consumer must independently recheck it.
func admitNativeContinuation(ctx context.Context, s Settings, sess protocol.Session, current Launch, next *Launch) error {
	if !s.ManagedChildDecisions || s.writer == nil || !current.ChildDecisions || !next.ChildDecisions || !next.Resume || !next.TUI || next.Prompt != "" {
		return errors.New("same-owner native continuation is not enabled")
	}
	unlock, err := workspaceAdminLock(ctx, s, sess.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	gate, err := workspaceSupervisorLock(ctx, s)
	if err != nil {
		return err
	}
	defer gate()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		return err
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return err
	}
	if owner.State != "owned" || !owner.Launched || owner.Generation != s.writer.owner.Generation || current.WorkspaceOwner == nil ||
		current.WorkspaceOwner.Generation != owner.Generation || current.SessionUID != sess.SessionUID || current.PodUID != s.PodUID ||
		current.BootID != next.BootID || current.ConversationID != next.ConversationID || !current.NativeThreadConfirmed || current.NativeInvocationID == next.NativeInvocationID {
		return errors.New("continuation does not retain the same current launched writer")
	}
	if stop, err := workspaceStopForOwner(s, sess, owner); err != nil || stop {
		return errors.New("native continuation refuses a requested or uncertain supervisor stop")
	}
	source, err := invocationBinding(current)
	if err != nil {
		return err
	}
	result, err := readNativeInvocation(s)
	if err != nil || !validNativeExit(result, source) {
		return errors.New("continuation lacks a genuine owned native Wait and group-absence receipt")
	}
	next.WorkspaceOwner = &owner
	next.Continuation = &nativeContinuation{Source: source, ReceiptDigest: nativeReceiptDigest(result)}
	return nil
}

// Called under the private native lifetime lock, then common Git -> stop gate.
// Replacing Exited with the next Running result is the one-use durable fence.
// Failed/ambiguous writes or starts never restore the old continuation authority.
func finalizeNativeContinuation(s Settings, sess protocol.Session, l Launch, now time.Time) error {
	if l.Continuation == nil || !l.Resume || !l.TUI || l.Prompt != "" || !l.NativeThreadConfirmed {
		return errors.New("native continuation launch is invalid")
	}
	unlock, err := workspaceAdminLock(context.Background(), s, sess.Repo)
	if err != nil {
		return err
	}
	defer unlock()
	gate, err := workspaceSupervisorLock(context.Background(), s)
	if err != nil {
		return err
	}
	defer gate()
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
		return err
	}
	if err := ownerMatches(s, sess, owner, true); err != nil {
		return err
	}
	if l.WorkspaceOwner == nil || owner.State != "owned" || !owner.Launched || !sameOwner(owner, *l.WorkspaceOwner) {
		return errors.New("continuation writer receipt changed")
	}
	if stop, err := workspaceStopForOwner(s, sess, owner); err != nil || stop {
		return errors.New("native continuation refuses a requested or uncertain supervisor stop")
	}
	result, err := readNativeInvocation(s)
	if err != nil || !validNativeExit(result, l.Continuation.Source) || nativeReceiptDigest(result) != l.Continuation.ReceiptDigest {
		return errors.New("native continuation result is missing, changed or already consumed")
	}
	binding, err := invocationBinding(l)
	if err != nil {
		return err
	}
	source := l.Continuation.Source
	if binding.Session != source.Session || binding.SessionUID != source.SessionUID || binding.PodUID != source.PodUID || binding.BootID != source.BootID ||
		binding.ThreadID != source.ThreadID || binding.Generation != source.Generation || binding.InvocationID == source.InvocationID {
		return errors.New("native continuation changed its task, thread or writer identity")
	}
	var first Launch
	if readPrivateManagedJSON(s, launchFile, 512<<10, &first) != nil || !first.NativeThreadConfirmed || first.Provider != protocol.AgentCodex || first.Session != l.Session || first.SessionUID != l.SessionUID || first.ConversationID != l.ConversationID || first.Dir != l.Dir {
		return errors.New("native continuation cannot verify the original confirmed conversation")
	}
	return writeWorkspaceJSON(s.statePath(nativeInvocationFile), nativeInvocation{Version: 1, Binding: binding, Phase: "Running", StartedAt: now.UTC()})
}
