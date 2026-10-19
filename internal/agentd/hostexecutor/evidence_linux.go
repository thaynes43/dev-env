//go:build linux

package hostexecutor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const EvidenceSocketName = "owned-root-evidence.sock"

// EvidenceConfig is supplied by the original immutable supervisor route, never
// by a worker request. Production requires the original namespace PID1.
type EvidenceConfig struct {
	HomeUID           string
	Baseline          string
	RequirePID1       bool
	ReportStopped     func(context.Context, Receipt) error
	AfterStopped      func(Receipt) error
	ResumeBaseline    string
	CheckContinuation func() bool
	mu                sync.Mutex
	server            *evidenceServer
}

type Observation struct {
	Version           int      `json:"version"`
	Challenge         string   `json:"challenge"`
	Binding           Binding  `json:"binding"`
	Guard             Identity `json:"guard"`
	HomeUID           string   `json:"homeUID"`
	Baseline          string   `json:"baseline"`        // Original policy baseline; never rebased by continuation.
	StartupBaseline   string   `json:"startupBaseline"` // Actual measurement for this native-process epoch.
	ContinuationReady bool     `json:"continuationReady"`
	Stage             string   `json:"stage"`
	Root              Identity `json:"root"`
	NativeVersion     string   `json:"nativeVersion"`
	RootWaitObserved  bool     `json:"rootWaitObserved"`
	TreeReaped        bool     `json:"treeReaped"`
}

type evidenceServer struct {
	mu                sync.Mutex
	current           Observation
	nativeSocket      string
	listener          net.Listener
	done              chan struct{}
	checkContinuation func() bool
}

func ProtectEvidenceProcess() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	mode, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || mode != 0 {
		return errors.New("evidence process memory protection unavailable")
	}
	return nil
}

// NativeHomeBaseline returns bounded canonical native startup/recovery content.
// Only the keeper-owned rotating access document is excluded. No bytes leave.
func NativeHomeBaseline(home string) (string, error) {
	root := filepath.Join(home, ".codex")
	openRoot := func() (int, error) {
		return unix.Openat2(unix.AT_FDCWD, root, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	}
	fd, err := openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(fd) }()
	type entry struct {
		Path string
		Mode uint32
		SHA  string
	}
	entries := []entry{}
	var total int64
	visited := 0
	var walk func(int, string) error
	walk = func(dir int, rel string) error {
		var before, after unix.Stat_t
		if unix.Fstat(dir, &before) != nil {
			return errors.New("native baseline directory unavailable")
		}
		dup, e := unix.Dup(dir)
		if e != nil {
			return e
		}
		f := os.NewFile(uintptr(dup), "baseline-dir")
		names, e := f.Readdirnames(4097)
		_ = f.Close()
		if e != nil && !errors.Is(e, io.EOF) {
			return e
		}
		if len(names) > 4096 {
			return errors.New("native baseline entry limit")
		}
		sort.Strings(names)
		for _, name := range names {
			visited++
			if visited > 4096 {
				return errors.New("native baseline entry limit")
			}
			var st unix.Stat_t
			if unix.Fstatat(dir, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				return errors.New("native baseline entry unavailable")
			}
			mode := st.Mode & unix.S_IFMT
			if mode != unix.S_IFDIR && mode != unix.S_IFREG || mode == unix.S_IFREG && st.Nlink != 1 {
				return errors.New("native baseline special file or alias")
			}
			path := filepath.Join(rel, name)
			if path == "auth.json" {
				if mode != unix.S_IFREG {
					return errors.New("access path is not regular")
				}
				continue
			}
			child, e := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			if e != nil {
				return e
			}
			item := entry{Path: filepath.ToSlash(path), Mode: st.Mode}
			if mode == unix.S_IFDIR {
				e = walk(child, path)
			} else {
				total += st.Size
				if st.Size < 0 || total > 64<<20 {
					_ = unix.Close(child)
					return errors.New("native baseline byte limit")
				}
				file := os.NewFile(uintptr(child), "baseline-file")
				h := sha256.New()
				var n int64
				n, e = io.Copy(h, io.LimitReader(file, st.Size+1))
				if n != st.Size {
					e = errors.New("native baseline changed during read")
				}
				item.SHA = hex.EncodeToString(h.Sum(nil))
				var current unix.Stat_t
				if unix.Fstat(child, &current) != nil || !stableBaselineStat(current, st) {
					e = errors.New("native baseline changed during read")
				}
				_ = file.Close()
				child = -1
			}
			if child >= 0 {
				_ = unix.Close(child)
			}
			var current unix.Stat_t
			if unix.Fstatat(dir, name, &current, unix.AT_SYMLINK_NOFOLLOW) != nil || !stableBaselineStat(current, st) {
				e = errors.New("native baseline entry changed")
			}
			if e != nil {
				return e
			}
			entries = append(entries, item)
		}
		if unix.Fstat(dir, &after) != nil || !stableBaselineStat(before, after) {
			return errors.New("native baseline directory changed")
		}
		return nil
	}
	var original unix.Stat_t
	if unix.Fstat(fd, &original) != nil {
		return "", errors.New("native baseline root unavailable")
	}
	if err = walk(fd, ""); err != nil {
		return "", err
	}
	again, err := openRoot()
	if err != nil {
		return "", err
	}
	defer func() { _ = unix.Close(again) }()
	var final unix.Stat_t
	if unix.Fstat(again, &final) != nil || !stableBaselineStat(original, final) {
		return "", errors.New("native baseline root changed")
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Access times may legitimately advance during measurement; content/identity may not.
func stableBaselineStat(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Uid == b.Uid && a.Gid == b.Gid && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

// Yama protects freshly exec'd sibling probes before their Go entry point can
// become nondumpable. Without this independently enforced kernel boundary the
// same-UID worker could race probe startup. No sysctl is changed here.
func RequireProbeIsolation() error {
	raw, err := os.ReadFile("/proc/sys/kernel/yama/ptrace_scope")
	if err != nil || !validPtraceScope(string(raw)) {
		return errors.New("owned root requires enforced Yama ptrace isolation")
	}
	return nil
}
func validPtraceScope(raw string) bool {
	s := strings.TrimSpace(raw)
	return s == "1" || s == "2" || s == "3"
}

func sameBinding(a, b Binding) bool {
	return a.TaskUID == b.TaskUID && a.Epoch == b.Epoch && a.HostID == b.HostID && a.PodUID == b.PodUID && a.Deadline.Equal(b.Deadline)
}
func validChallenge(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func (e *EvidenceConfig) start(c Config) error {
	if e.RequirePID1 && os.Getpid() != 1 {
		return errors.New("owned root evidence requires original PID1")
	}
	if e.RequirePID1 {
		if err := RequireProbeIsolation(); err != nil {
			return err
		}
		if err := ProtectEvidenceProcess(); err != nil {
			return err
		}
	}
	if e.HomeUID == "" || len(e.HomeUID) > 128 || len(e.Baseline) != 64 {
		return errors.New("owned HOME authority unavailable")
	}
	baseline, err := NativeHomeBaseline(c.Home)
	expectedBaseline := e.Baseline
	if e.ResumeBaseline != "" {
		expectedBaseline = e.ResumeBaseline
	}
	if err != nil || baseline != expectedBaseline {
		return errors.New("native baseline does not match assigned HOME authority")
	}
	path := filepath.Join(filepath.Dir(c.ReceiptPath), EvidenceSocketName)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("owned evidence socket already exists")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	guard, err := identity(os.Getpid())
	if err != nil {
		_ = listener.Close()
		return err
	}
	s := &evidenceServer{current: Observation{Version: 1, Binding: c.Binding, Guard: guard, HomeUID: e.HomeUID, Baseline: e.Baseline, StartupBaseline: baseline, Stage: "Prepared"}, checkContinuation: e.CheckContinuation, nativeSocket: c.SocketPath, listener: listener, done: make(chan struct{})}
	e.mu.Lock()
	e.server = s
	e.mu.Unlock()
	go func() {
		defer close(s.done)
		var active sync.WaitGroup
		defer active.Wait()
		slots := make(chan struct{}, 4)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			select {
			case slots <- struct{}{}:
				active.Add(1)
				go func() { defer active.Done(); defer func() { <-slots }(); s.serve(conn) }()
			default:
				_ = conn.Close()
			}
		}
	}()
	return nil
}

func (e *EvidenceConfig) Close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	s := e.server
	e.server = nil
	e.mu.Unlock()
	if s != nil {
		_ = s.listener.Close()
		<-s.done
	}
}
func (e *EvidenceConfig) update(stage string, r Receipt) {
	if e == nil {
		return
	}
	e.mu.Lock()
	s := e.server
	e.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current.Stage = stage
	s.current.Root = r.Root
	s.current.NativeVersion = r.NativeVersion
	s.current.RootWaitObserved = r.RootWaitObserved
	s.current.TreeReaped = r.TreeReaped
}

func (s *evidenceServer) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(500 * time.Millisecond)
	_ = conn.SetDeadline(deadline)
	var req struct {
		Challenge string  `json:"challenge"`
		Binding   Binding `json:"binding"`
	}
	d := json.NewDecoder(io.LimitReader(conn, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || !validChallenge(req.Challenge) {
		return
	}
	s.mu.Lock()
	result := s.current
	s.mu.Unlock()
	if !sameBinding(req.Binding, result.Binding) {
		return
	}
	if result.Stage == "Ready" {
		probeDeadline := time.Now().Add(300 * time.Millisecond)
		if deadline.Before(probeDeadline) {
			probeDeadline = deadline
		}
		ctx, cancel := context.WithDeadline(context.Background(), probeDeadline)
		err := probeNative(ctx, s.nativeSocket, result.Root, result.NativeVersion)
		cancel()
		if err != nil {
			return
		}
	}
	if !rootLive(result.Guard) {
		return
	}
	s.mu.Lock()
	stable := reflect.DeepEqual(result, s.current)
	s.mu.Unlock()
	if !stable {
		return
	}
	if result.Stage == "Stopped" && s.checkContinuation != nil {
		result.ContinuationReady = s.checkContinuation()
	}
	result.Challenge = req.Challenge
	_ = json.NewEncoder(conn).Encode(result)
}

// ProbeEvidence's anchor comes from the trusted launch topology. The production
// CLI always supplies namespace PID1; inert process tests use their original PID.
func ProbeEvidence(ctx context.Context, path string, anchor int, b Binding, challenge string) (Observation, error) {
	var empty Observation
	if !validChallenge(challenge) || anchor < 1 {
		return empty, probeRefusal{ProbeRefusedChallenge, errors.New("invalid evidence challenge/anchor")}
	}
	guard, err := identity(anchor)
	if err != nil {
		return empty, probeRefusal{ProbeRefusedGuardIdentity, errors.New("original guard identity unavailable")}
	}
	if !rootLive(guard) {
		return empty, probeRefusal{ProbeRefusedGuardLiveness, errors.New("original guard unavailable")}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return empty, probeRefusal{ProbeRefusedConnect, err}
	}
	defer func() { _ = conn.Close() }()
	if !ownedSocketPeer(conn, guard) {
		return empty, probeRefusal{ProbeRefusedPeer, errors.New("evidence peer is not original guard")}
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return empty, probeRefusal{ProbeRefusedDeadline, errors.New("evidence observation requires finite context")}
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return empty, probeRefusal{ProbeRefusedDeadline, err}
	}
	if json.NewEncoder(conn).Encode(struct {
		Challenge string  `json:"challenge"`
		Binding   Binding `json:"binding"`
	}{challenge, b}) != nil {
		return empty, probeRefusal{ProbeRefusedRequest, errors.New("evidence request unavailable")}
	}
	raw, err := io.ReadAll(io.LimitReader(conn, 8193))
	if err != nil || len(raw) > 8192 {
		return empty, probeRefusal{ProbeRefusedResponse, errors.New("evidence response unavailable")}
	}
	var result Observation
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.Version != 1 || result.Challenge != challenge || !sameBinding(result.Binding, b) || !reflect.DeepEqual(result.Guard, guard) || !rootLive(guard) {
		return empty, probeRefusal{ProbeRefusedIdentity, errors.New("evidence identity changed")}
	}
	if result.Stage == "Ready" && (!rootLive(result.Root) || result.NativeVersion != PinnedNativeVersion) {
		return empty, probeRefusal{ProbeRefusedNative, errors.New("original native root unavailable")}
	}
	return result, nil
}

func (e *EvidenceConfig) reportStopped(c Config, r Receipt) error {
	if e == nil {
		return nil
	}
	e.update("Stopped", r)
	if e.ReportStopped == nil {
		return fmt.Errorf("owned root stop authority: %w", ErrDisabled)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.ReportStopped(ctx, r); err != nil {
		return err
	}
	if e.AfterStopped != nil {
		return e.AfterStopped(r)
	}
	return nil
}
