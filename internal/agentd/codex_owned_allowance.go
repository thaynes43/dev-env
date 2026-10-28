package agentd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/agentd/hostexecutor"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// ownedAllowanceProjection validates, but never updates, the existing fence.
// Runtime callers must independently authorize and bind any diagnostic.
func ownedAllowanceProjection(s Settings, now time.Time) ([]byte, codexReceipt, error) {
	if s.CodexAccessFile == "" || s.StateDir == "" || now.IsZero() {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	raw, err := readCodexAccessFile(s.CodexAccessFile)
	if err != nil {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	access, err := codexauth.Decode(raw, now)
	if err != nil || !access.ExpiresAt.After(now.Add(5*time.Minute)) {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	canonical, err := codexauth.Encode(access, now)
	if err != nil {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	sum := sha256.Sum256(canonical)
	expected := codexReceipt{Generation: access.Generation, Fingerprint: hex.EncodeToString(sum[:])}
	receipt, err := boundedHostFile(s.statePath("codex-access-generation.json"), 64<<10, true)
	if err != nil {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	var actual codexReceipt
	if codexauth.DecodeStrict(receipt, &actual) != nil || actual.Generation != expected.Generation || subtle.ConstantTimeCompare([]byte(actual.Fingerprint), []byte(expected.Fingerprint)) != 1 {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	native, err := codexauth.NativeJSON(access, now)
	if err != nil {
		return nil, codexReceipt{}, ErrCodexAccess
	}
	return native, expected, nil
}

// ownedAllowanceStillCurrent repeats read-only projection and fence validation.
func ownedAllowanceStillCurrent(s Settings, now time.Time, expected codexReceipt) bool {
	_, current, err := ownedAllowanceProjection(s, now)
	return err == nil && current == expected
}

// ownedAllowanceWriteAuth creates only a new private observer credential file.
func ownedAllowanceWriteAuth(dir string, native []byte) error {
	f, err := os.OpenFile(dir+"/auth.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrCodexAccess
	}
	if _, err = f.Write(native); err != nil {
		_ = f.Close()
		return ErrCodexAccess
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return ErrCodexAccess
	}
	if err = f.Close(); err != nil {
		return ErrCodexAccess
	}
	return nil
}

func ownedAllowanceRuntimeDirectory(info os.FileInfo, uid uint32) bool {
	if info == nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uid
}

// OwnedAllowanceContext carries caller-supplied provenance, not authentication.
type OwnedAllowanceContext struct {
	Session protocol.Session
	PodUID  string
}

type ownedAllowanceOwner interface {
	PID() int
	Alive() bool
	Stop() error
}
type ownedAllowanceDeps struct {
	start   func(context.Context, string) (ownedAllowanceOwner, error)
	read    func(context.Context, Settings, int, time.Time, CodexAllowanceCapture) (providerallowance.ReadResponse, error)
	version func(context.Context, Settings) error
	binary  func() (os.FileInfo, error)
	now     func() time.Time
	nonce   func() (string, error)
}

func realOwnedAllowanceDeps() ownedAllowanceDeps {
	return ownedAllowanceDeps{start: func(ctx context.Context, home string) (ownedAllowanceOwner, error) {
		return hostexecutor.StartAllowanceOwner(ctx, home)
	}, read: ReadCodexNativeAllowance, binary: func() (os.FileInfo, error) { return os.Stat("/usr/local/bin/codex") }, version: func(ctx context.Context, s Settings) error {
		return hostVersion(ctx, s, realCodexHostDeps(), "/usr/local/bin/codex")
	}, now: time.Now, nonce: func() (string, error) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		return hex.EncodeToString(b[:]), nil
	}}
}

// CaptureOwnedAllowance is a local diagnostic; external execution approval is required.
func CaptureOwnedAllowance(ctx context.Context, s Settings, session protocol.Session, recheck func() (OwnedAllowanceContext, error)) (providerallowance.ReadResponse, error) {
	if s.Home != "/home/dev" || s.StateDir != "/home/dev/.agentd" || s.CodexAccessFile != "/opt/dev-env/codex-access/access.json" || s.RuntimeDir != filepath.Join("/dev/shm", "run-"+strconv.Itoa(os.Geteuid())) {
		return providerallowance.ReadResponse{}, ErrCodexAccess
	}
	var fs unix.Statfs_t
	if unix.Statfs(s.RuntimeDir, &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
		return providerallowance.ReadResponse{}, ErrCodexAccess
	}
	return captureOwnedAllowance(ctx, s, session, recheck, realOwnedAllowanceDeps())
}
func captureOwnedAllowance(ctx context.Context, s Settings, session protocol.Session, recheck func() (OwnedAllowanceContext, error), d ownedAllowanceDeps) (providerallowance.ReadResponse, error) {
	unavailable := func() (providerallowance.ReadResponse, error) {
		return providerallowance.ReadResponse{}, ErrCodexAccess
	}
	if ctx.Err() != nil || recheck == nil || !validAllowanceRef(s.PodUID) || !validAllowanceRef(session.SessionUID) || session.Agent != protocol.AgentCodex || session.Mode != protocol.ModeRemote || s.CodexAccessFile == "" {
		return unavailable()
	}
	expected := OwnedAllowanceContext{Session: session, PodUID: s.PodUID}
	current := func() bool { actual, err := recheck(); return err == nil && reflect.DeepEqual(actual, expected) }
	if !current() {
		return unavailable()
	}
	native, fence, err := ownedAllowanceProjection(s, d.now())
	if err != nil {
		return unavailable()
	}
	binary, err := d.binary()
	if err != nil || !binary.Mode().IsRegular() {
		return unavailable()
	}
	binaryUnchanged := func() bool {
		current, err := d.binary()
		return err == nil && os.SameFile(binary, current)
	}
	base, err := os.Lstat(s.RuntimeDir)
	if err != nil || !ownedAllowanceRuntimeDirectory(base, uint32(os.Geteuid())) || noSymlinkComponents(s.RuntimeDir) != nil {
		return unavailable()
	}
	home := filepath.Join(s.RuntimeDir, "codex-owned-allowance-observer")
	if os.Mkdir(home, 0700) != nil {
		return unavailable()
	}
	cleanup := func() bool {
		if os.RemoveAll(home) != nil {
			return false
		}
		_, err := os.Lstat(home)
		return os.IsNotExist(err)
	}
	providerHome := filepath.Join(home, ".codex")
	if os.Mkdir(providerHome, 0700) != nil || os.Mkdir(filepath.Join(providerHome, "app-server-control"), 0700) != nil || ownedAllowanceWriteAuth(providerHome, native) != nil {
		_ = cleanup()
		return unavailable()
	}
	versionSettings := s
	versionSettings.Home, versionSettings.CodexHome = home, providerHome
	if d.version(ctx, versionSettings) != nil || !binaryUnchanged() {
		_ = cleanup()
		return unavailable()
	}
	if !current() || !ownedAllowanceStillCurrent(s, d.now(), fence) {
		_ = cleanup()
		return unavailable()
	}
	owner, err := d.start(ctx, home)
	// A failed helper start may have created a child; retain credentials until a
	// separate cleanup proof rather than guessing that the process stopped.
	if err != nil {
		return unavailable()
	}
	stopped := false
	defer func() {
		if !stopped && owner.Stop() == nil {
			_ = cleanup()
		}
	}()
	socket := filepath.Join(providerHome, "app-server-control", "app-server-control.sock")
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, e := os.Lstat(socket)
		if e == nil {
			if info.Mode()&os.ModeSocket == 0 {
				return unavailable()
			}
			break
		}
		if !os.IsNotExist(e) || !owner.Alive() {
			return unavailable()
		}
		select {
		case <-ctx.Done():
			return unavailable()
		case <-deadline.C:
			return unavailable()
		case <-ticker.C:
		}
	}
	if !owner.Alive() || !binaryUnchanged() || !current() || !ownedAllowanceStillCurrent(s, d.now(), fence) {
		return unavailable()
	}
	id, err := d.nonce()
	if err != nil || !validAllowanceRef(id) {
		return unavailable()
	}
	capture := CodexAllowanceCapture{ObservationID: "capture-" + id, ProducerRef: "agentd-owned-observer", ProducerIncarnation: "capture-" + id, Sequence: 1, EvidenceRef: "capture-" + id, SnapshotRevision: "capture-" + id}
	target := s
	target.CodexHome = providerHome
	result, err := d.read(ctx, target, owner.PID(), d.now().UTC(), capture)
	if err != nil || !owner.Alive() || !binaryUnchanged() || !current() || !ownedAllowanceStillCurrent(s, d.now(), fence) {
		return unavailable()
	}
	if owner.Stop() != nil {
		stopped = true
		return unavailable()
	}
	stopped = true
	if !current() || !ownedAllowanceStillCurrent(s, d.now(), fence) {
		_ = cleanup()
		return unavailable()
	}
	if !cleanup() {
		return unavailable()
	}
	return result, nil
}
