package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const (
	codexRemoteLifecycleLock = "codex-remote-lifecycle.lock"
	codexRemoteStopFile      = "codex-remote-stop.json"
)

var (
	errCodexRemoteStopping = errors.New("Codex host is stopped for rescue; only a new pod may start its daemon")
	errCodexRemotePath     = errors.New("Codex host lifecycle path is not private")
)

func codexRemoteStateDir(s Settings, create bool) error {
	if !filepath.IsAbs(s.Home) || s.StateDir != filepath.Join(s.Home, ".agentd") || noSymlinkComponents(s.Home) != nil {
		return errCodexRemotePath
	}
	fi, err := os.Lstat(s.StateDir)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil
		}
		if os.Mkdir(s.StateDir, 0700) != nil {
			return errCodexRemotePath
		}
		fi, err = os.Lstat(s.StateDir)
	}
	if err != nil || noSymlinkComponents(s.StateDir) != nil || !fi.IsDir() || fi.Mode().Perm()&0077 != 0 || !ownedByProcess(fi) {
		return errCodexRemotePath
	}
	return nil
}

// The stop survives ctl exit and a supervisor restart in the same pod. A new
// pod on the same Session/HOME may supervise the daemon again; this record
// carries no thread selection and cannot resume a conversation.
type codexRemoteStop struct {
	Version    int    `json:"version"`
	SessionUID string `json:"sessionUID"`
	PodUID     string `json:"podUID"`
}

// The interprocess lock belongs to the private runtime directory:
// even a never-started remote host must keep D-55's empty-volume proof. The
// fallback preserves non-stop callers with legacy Settings and no runtime dir;
// an actual remote rescue requires the operator's separate runtime directory.
func codexRemoteGateDir(s Settings) string {
	if s.RuntimeDir != "" {
		return s.RuntimeDir
	}
	return s.StateDir
}
func codexRemoteGatePath(s Settings, name string) string {
	return filepath.Join(codexRemoteGateDir(s), name)
}

func codexRemoteStopped(s Settings) error {
	if err := codexRemoteStateDir(s, false); err != nil {
		return err
	}
	// HOME is authoritative across container restarts in the same Pod. The
	// runtime-only marker is used solely by a verified never-written hold.
	paths := []string{s.statePath(codexRemoteStopFile), codexRemoteGatePath(s, codexRemoteStopFile)}
	for _, path := range paths {
		raw, err := boundedHostFile(path, 4096, true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var rec codexRemoteStop
		if err != nil || decodeHostJSON(raw, &rec) != nil || rec.Version != 1 || rec.SessionUID == "" || rec.PodUID == "" || s.PodUID == "" || s.Getenv == nil {
			return errors.New("Codex host stop binding is unreadable; refusing a writer")
		}
		sess, err := LoadSession(s.Getenv)
		if err != nil || !codexRemoteSession(sess) || sess.SessionUID != rec.SessionUID {
			return errors.New("Codex host stop binding does not match the retained Session")
		}
		if rec.PodUID == s.PodUID {
			return errCodexRemoteStopping
		}
	}
	return nil
}

// Each supported remote writer takes a shared lock before its first mutation.
// Rescue takes the exclusive lock, publishes the durable stop, and holds the
// lock through its snapshot. Busy/unknown is a refusal, never permission to race.
func codexRemoteLifecycle(s Settings, exclusive bool) (func(), error) {
	dir := codexRemoteGateDir(s)
	if !filepath.IsAbs(dir) {
		return nil, errors.New("Codex lifecycle requires an absolute private directory")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, errors.New("cannot prepare Codex host lifecycle gate")
	}
	stDir, dirErr := os.Stat(dir)
	if noSymlinkComponents(dir) != nil || dirErr != nil || !stDir.IsDir() || stDir.Mode().Perm()&0077 != 0 || !ownedByProcess(stDir) {
		return nil, errCodexRemotePath
	}
	f, err := os.OpenFile(codexRemoteGatePath(s, codexRemoteLifecycleLock), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, errors.New("cannot open Codex host lifecycle gate")
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || !ownedByProcess(st) {
		_ = f.Close()
		return nil, errors.New("Codex host lifecycle gate is not a private regular file")
	}
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("Codex host lifecycle is busy; stop and launch cannot overlap")
	}
	unlock := func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
	if !exclusive {
		if err := codexRemoteStopped(s); err != nil {
			unlock()
			return nil, err
		}
	}
	return unlock, nil
}

// stopCodexRemoteForRescue never runs a provider command. The process layer
// verifies a dedicated tini/agentd PID namespace, freezes the supervisor, and
// drains all other processes (including tmux and detached descendants). It
// leaves the supervisor frozen until pod deletion, including on failure.
func stopCodexRemoteForRescue(ctx context.Context, s Settings, sess protocol.Session, grace time.Duration) (*protocol.AgentStop, func(), error) {
	if !codexRemoteSession(sess) || sess.SessionUID == "" || s.PodUID == "" {
		return nil, nil, errors.New("Codex host stop requires the Session and pod UID")
	}
	runtime := filepath.Clean(s.RuntimeDir)
	home := filepath.Clean(s.Home)
	if !filepath.IsAbs(s.RuntimeDir) || runtime == home || strings.HasPrefix(runtime, home+string(filepath.Separator)) {
		return nil, nil, errors.New("Codex host stop requires its private runtime directory outside HOME")
	}
	unlock, err := codexRemoteLifecycle(s, true)
	if err != nil {
		return nil, nil, err
	}
	fail := func(err error) (*protocol.AgentStop, func(), error) { unlock(); return nil, nil, err }
	// Reject a mismatched retained HOME even on a later pod. A same-pod stop
	// is intentionally repeatable after an interrupted rescue.
	if err := codexRemoteStopped(s); err != nil && !errors.Is(err, errCodexRemoteStopping) {
		return fail(err)
	}
	processes := codexRemoteProcessesFor()
	empty, err := volumeEmpty(s)
	if err != nil {
		return fail(errors.New("Codex stop cannot inspect HOME"))
	}
	marker := s.statePath(codexRemoteStopFile)
	if empty {
		supervisor, err := processes.supervisor(s, sess)
		if err != nil || !supervisor.hold {
			return fail(errors.New("only a verified empty rescue hold may omit the HOME stop fence"))
		}
		marker = codexRemoteGatePath(s, codexRemoteStopFile)
	}
	if !empty {
		if err := codexRemoteStateDir(s, true); err != nil {
			return fail(err)
		}
	}
	if !empty {
		if err := codexRemoteStateDir(s, true); err != nil {
			return fail(err)
		}
	}
	data, _ := json.Marshal(codexRemoteStop{1, sess.SessionUID, s.PodUID})
	if err := writeFileDurable(marker, data, 0600); err != nil {
		return fail(errors.New("cannot retain Codex host stop fence; rescue blocked"))
	}
	st, err := quiesceCodexRemote(ctx, s, sess, grace, processes)
	if err != nil {
		return fail(err)
	}
	// Recheck the exact marker after writers have stopped. A path or fence
	// changed during the drain is not persistence evidence for the snapshot.
	if !empty {
		if err := codexRemoteStateDir(s, false); err != nil {
			return fail(err)
		}
	}
	raw, readErr := boundedHostFile(marker, 4096, true)
	var stopped codexRemoteStop
	if readErr != nil || decodeHostJSON(raw, &stopped) != nil || stopped != (codexRemoteStop{1, sess.SessionUID, s.PodUID}) {
		return fail(errors.New("Codex stop fence changed during quiesce; rescue blocked"))
	}
	if empty {
		stillEmpty, err := volumeEmpty(s)
		if err != nil || !stillEmpty {
			return fail(errors.New("HOME changed while stopping the empty Codex rescue hold"))
		}
	}
	return st, unlock, nil
}
