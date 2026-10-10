package agentd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

var errCodexHostUnknown = errors.New("retained Codex host state is unknown or incompatible")

type codexHostIdentity struct {
	BootID     string `json:"bootId"`
	StartTicks uint64 `json:"startTicks"`
}
type codexHostPID struct {
	PID                int                `json:"pid"`
	ProcessStartTime   string             `json:"processStartTime"`
	ProcessIdentity    *codexHostIdentity `json:"processIdentity,omitempty"`
	LinuxIdentity      *codexHostIdentity `json:"linuxProcessIdentity,omitempty"`
	ExecutableIdentity json.RawMessage    `json:"executableIdentity,omitempty"`
}
type codexHostProcess struct {
	State    string
	PID      int
	Identity codexHostIdentity
}

// All platform reads are injectable for finite fixtures. Production reads only
// native metadata and proc identity, never native transcripts/account records.
type codexHostFS struct {
	read  func(string) ([]byte, error)
	lstat func(string) (os.FileInfo, error)
}

func realCodexHostFS() codexHostFS {
	return codexHostFS{func(path string) ([]byte, error) { return boundedHostFile(path, 1<<20, false) }, os.Lstat}
}

func boundedHostFile(path string, bound int, private bool) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(bound) || (private && st.Mode().Perm()&0o077 != 0) {
		return nil, errCodexHostUnknown
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(bound)+1))
	if err != nil || len(data) > bound {
		return nil, errCodexHostUnknown
	}
	return data, nil
}
func decodeHostJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(target) != nil || d.Decode(new(any)) != io.EOF {
		return errCodexHostUnknown
	}
	return nil
}

// A failed socket/version check is never absence. Empty reservations and active
// native locks are unknown; this observer never acquires or removes native locks.
func inspectCodexHostProcess(s Settings, fs codexHostFS) (codexHostProcess, error) {
	state := filepath.Join(s.CodexHome, "app-server-daemon")
	boot, err := fs.read("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		return codexHostProcess{}, errCodexHostUnknown
	}
	locks, err := fs.read("/proc/locks")
	if err != nil || len(locks) > 1<<20 {
		return codexHostProcess{}, errCodexHostUnknown
	}
	for _, name := range []string{"daemon.lock", "daemon.pid.lock", "daemon-updater.pid.lock", "app-server.pid.lock", "app-server-updater.pid.lock"} {
		st, err := fs.lstat(filepath.Join(state, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.Mode().IsRegular() {
			return codexHostProcess{}, errCodexHostUnknown
		}
		if stat, ok := st.Sys().(*syscall.Stat_t); ok {
			key := strconv.FormatUint(uint64(unix.Major(stat.Dev)), 16) + ":" + strconv.FormatUint(uint64(unix.Minor(stat.Dev)), 16) + ":" + strconv.FormatUint(stat.Ino, 10)
			for _, line := range strings.Split(string(locks), "\n") {
				for _, field := range strings.Fields(line) {
					parts := strings.Split(field, ":")
					if len(parts) == 3 && canonicalHostLockID(field) == canonicalHostLockID(key) {
						return codexHostProcess{}, errCodexHostUnknown
					}
				}
			}
		} else {
			return codexHostProcess{}, errCodexHostUnknown
		}
	}
	current := codexHostProcess{State: "Absent"}
	for _, name := range []string{"daemon.pid", "app-server.pid", "daemon-updater.pid", "app-server-updater.pid"} {
		path := filepath.Join(state, name)
		st, err := fs.lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > 16<<10 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		data, err := fs.read(path)
		if err != nil || len(data) > 16<<10 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		var record codexHostPID
		if decodeHostJSON(data, &record) != nil || record.PID <= 0 || record.PID > 1<<30 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		identity := record.ProcessIdentity
		if identity == nil {
			identity = record.LinuxIdentity
		}
		if identity == nil || identity.BootID == "" || identity.StartTicks == 0 || (record.ProcessIdentity != nil && record.LinuxIdentity != nil) {
			return codexHostProcess{}, errCodexHostUnknown
		}
		if identity.BootID != strings.TrimSpace(string(boot)) {
			continue
		}
		proc, err := fs.read("/proc/" + strconv.Itoa(record.PID) + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || len(proc) > 16<<10 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		end := strings.LastIndexByte(string(proc), ')')
		if end < 0 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		fields := strings.Fields(string(proc)[end+1:])
		if len(fields) < 20 {
			return codexHostProcess{}, errCodexHostUnknown
		}
		ticks, err := strconv.ParseUint(fields[19], 10, 64)
		if err != nil {
			return codexHostProcess{}, errCodexHostUnknown
		}
		if ticks != identity.StartTicks || fields[0] == "Z" {
			continue
		}
		if strings.Contains(name, "updater") || current.State == "Live" {
			return codexHostProcess{}, errCodexHostUnknown
		}
		current = codexHostProcess{State: "Live", PID: record.PID, Identity: *identity}
	}
	// The advertised rendezvous may be a native symlink. It is never resolved,
	// unlinked or reconstructed by this supervisor. A socket without a live PID
	// remains unknown even after a failed connection, including stale rendezvous.
	socket := filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock")
	st, err := fs.lstat(socket)
	if current.State == "Absent" && !errors.Is(err, os.ErrNotExist) {
		return codexHostProcess{}, errCodexHostUnknown
	}
	if current.State == "Live" && (err != nil || (st.Mode()&os.ModeSocket == 0 && st.Mode()&os.ModeSymlink == 0)) {
		return codexHostProcess{}, errCodexHostUnknown
	}
	return current, nil
}
func canonicalHostLockID(value string) string {
	fields := strings.Split(value, ":")
	if len(fields) != 3 {
		return ""
	}
	major, e1 := strconv.ParseUint(fields[0], 16, 64)
	minor, e2 := strconv.ParseUint(fields[1], 16, 64)
	inode, e3 := strconv.ParseUint(fields[2], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil {
		return ""
	}
	return strconv.FormatUint(major, 10) + ":" + strconv.FormatUint(minor, 10) + ":" + strconv.FormatUint(inode, 10)
}

// Mirror pinned native modern/legacy package selection; never choose a version
// from a download or substitute the invoking CLI for a missing live package.
func codexHostManagedBin(s Settings) (string, error) {
	root := filepath.Join(s.CodexHome, "packages", "app-server-daemon")
	if _, err := os.Lstat(filepath.Join(root, "current")); errors.Is(err, os.ErrNotExist) {
		state := filepath.Join(s.CodexHome, "app-server-daemon")
		modern, legacy := false, false
		for _, name := range []string{"daemon.pid", "daemon.stderr.log", "daemon-updater.pid", "daemon-updater.stderr.log"} {
			if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
				modern = true
			}
		}
		if !modern {
			for _, name := range []string{"app-server.pid", "app-server.stderr.log", "app-server-updater.pid", "app-server-updater.stderr.log"} {
				if _, err := os.Lstat(filepath.Join(state, name)); !errors.Is(err, os.ErrNotExist) {
					legacy = true
				}
			}
		}
		if legacy {
			root = filepath.Join(s.CodexHome, "packages", "standalone")
		}
	} else if err != nil {
		return "", errCodexHostUnknown
	}
	path := filepath.Join(root, "current", "bin", "codex")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && filepath.Base(root) == "standalone" {
		path = filepath.Join(root, "current", "codex")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
		return "", errCodexHostUnknown
	}
	st, err := os.Stat(resolved)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return "", errCodexHostUnknown
	}
	return resolved, nil
}
