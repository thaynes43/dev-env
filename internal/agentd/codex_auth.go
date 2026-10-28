package agentd

import (
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

	"github.com/thaynes43/dev-env/internal/codexauth"
)

var ErrCodexAccess = errors.New("codex authentication requires renewal or a current keeper projection")

type codexReceipt struct {
	Generation  uint64 `json:"generation"`
	Fingerprint string `json:"fingerprint"`
}

func renderCodexAccess(s Settings, now time.Time) Step {
	if s.CodexAccessFile == "" {
		return skipStep("codex-access", "keeper Codex access projection is not configured")
	}
	return newStep("codex-access", nil, SyncCodexAccess(s, now))
}

func readCodexAccessFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrCodexAccess
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > codexauth.MaxBytes {
		return nil, ErrCodexAccess
	}
	raw, err := io.ReadAll(io.LimitReader(f, codexauth.MaxBytes+1))
	if err != nil || len(raw) > codexauth.MaxBytes {
		return nil, ErrCodexAccess
	}
	return raw, nil
}

// SyncCodexAccess validates one atomic projection document, fences generation
// rollback, and installs a private native auth file. It never runs a provider CLI
// or changes enrollment, daemon state, history, config or provider caches.
func SyncCodexAccess(s Settings, now time.Time) error {
	if s.CodexAccessFile == "" {
		return nil
	}
	if !filepath.IsAbs(s.CodexAccessFile) || !filepath.IsAbs(s.CodexHome) || !filepath.IsAbs(s.StateDir) {
		return ErrCodexAccess
	}
	if err := privateCodexHome(s); err != nil {
		return ErrCodexAccess
	}
	raw, err := readCodexAccessFile(s.CodexAccessFile)
	if err != nil {
		return ErrCodexAccess
	}
	a, err := codexauth.Decode(raw, now)
	if err != nil || !a.ExpiresAt.After(now.Add(5*time.Minute)) {
		return ErrCodexAccess
	}
	lock, err := lockCodexAccess(s)
	if err != nil {
		return ErrCodexAccess
	}
	defer func() { _ = lock.Close() }()
	// The projection can rotate while we wait for the lock. Read it again
	// within the transaction; the first validation merely avoids creating a
	// lock file for an already invalid input.
	raw, err = readCodexAccessFile(s.CodexAccessFile)
	if err != nil {
		return ErrCodexAccess
	}
	a, err = codexauth.Decode(raw, now)
	if err != nil || !a.ExpiresAt.After(now.Add(5*time.Minute)) {
		return ErrCodexAccess
	}
	canonical, err := codexauth.Encode(a, now)
	if err != nil {
		return ErrCodexAccess
	}
	sum := sha256.Sum256(canonical)
	next := codexReceipt{a.Generation, hex.EncodeToString(sum[:])}
	receiptPath := s.statePath("codex-access-generation.json")
	receiptCurrent := false
	if data, err := readCodexAccessFile(receiptPath); err == nil {
		var old codexReceipt
		if codexauth.DecodeStrict(data, &old) != nil || old.Generation == 0 || len(old.Fingerprint) != 64 || a.Generation < old.Generation || (a.Generation == old.Generation && next.Fingerprint != old.Fingerprint) {
			return ErrCodexAccess
		}
		receiptCurrent = old == next
	} else if _, err := os.Lstat(receiptPath); !os.IsNotExist(err) {
		return ErrCodexAccess
	}
	if os.MkdirAll(s.CodexHome, 0o700) != nil {
		return ErrCodexAccess
	}
	fi, err := os.Lstat(s.CodexHome)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || os.Chmod(s.CodexHome, 0o700) != nil {
		return ErrCodexAccess
	}
	auth, err := codexauth.NativeJSON(a, now)
	if err != nil {
		return ErrCodexAccess
	}
	// Persist the generation fence first. If auth replacement fails, an old
	// projection cannot overwrite the newer generation on restart. The same
	// generation is always safe to reapply after that interrupted installation.
	receipt, _ := json.Marshal(next)
	if !receiptCurrent && writeCodexPrivateAtomic(receiptPath, receipt) != nil {
		return ErrCodexAccess
	}
	dest := filepath.Join(s.CodexHome, "auth.json")
	if existing, err := readCodexAccessFile(dest); err == nil && string(existing) == string(auth) {
		if fi, err := os.Lstat(dest); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 {
			return nil
		}
	}
	if writeCodexPrivateAtomic(dest, auth) != nil {
		return ErrCodexAccess
	}
	return nil
}

// lockCodexAccess serializes every writer of the shared generation fence.
// The file is never unlinked: all processes must lock the same inode.
func lockCodexAccess(s Settings) (*os.File, error) {
	if err := noSymlinkComponents(s.StateDir); err != nil && !os.IsNotExist(err) {
		return nil, ErrCodexAccess
	}
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		return nil, ErrCodexAccess
	}
	dir, err := os.Lstat(s.StateDir)
	if err != nil || !dir.IsDir() || dir.Mode().Perm()&0o077 != 0 {
		return nil, ErrCodexAccess
	}
	dirStat, ok := dir.Sys().(*syscall.Stat_t)
	if !ok || dirStat.Uid != uint32(os.Geteuid()) {
		return nil, ErrCodexAccess
	}
	path := s.statePath("codex-access-generation.lock")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC|syscall.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, ErrCodexAccess
	}
	f := os.NewFile(uintptr(fd), "codex-access-lock")
	refuse := func() (*os.File, error) { _ = f.Close(); return nil, ErrCodexAccess }
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm() != 0o600 {
		return refuse()
	}
	stat, ok := opened.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return refuse()
	}
	deadline := time.Now().Add(time.Second)
	first := true
	for {
		if !first && !time.Now().Before(deadline) {
			return refuse()
		}
		first = false
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return refuse()
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return refuse()
		}
		if remaining > 10*time.Millisecond {
			remaining = 10 * time.Millisecond
		}
		time.Sleep(remaining)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return refuse()
	}
	return f, nil
}

// Check the private destination before any render, launch or periodic sync can
// install access material. Missing directories are allowed only after existing
// ancestors have passed the absolute, clean, no-symlink check.
func privateCodexHome(s Settings) error {
	if !strings.HasPrefix(s.CodexHome, s.Home+string(filepath.Separator)) {
		return errors.New("managed Codex requires a private provider home")
	}
	for _, shared := range []string{s.ReposDir(), s.WorkDir(), filepath.Join(s.Home, "codex"), s.workspaceDir()} {
		if s.CodexHome == shared || strings.HasPrefix(s.CodexHome, shared+string(filepath.Separator)) {
			return errors.New("codex provider state must remain private")
		}
	}
	if err := noSymlinkComponents(s.CodexHome); err != nil && !os.IsNotExist(err) {
		return errors.New("managed Codex requires a private provider home")
	}
	return nil
}

func writeCodexPrivateAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return ErrCodexAccess
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return ErrCodexAccess
	}
	f, err := os.CreateTemp(dir, ".codex-access-*")
	if err != nil {
		return ErrCodexAccess
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if f.Chmod(0o600) != nil {
		return ErrCodexAccess
	}
	if _, err = f.Write(raw); err != nil {
		return ErrCodexAccess
	}
	if f.Sync() != nil || f.Close() != nil || os.Rename(f.Name(), path) != nil {
		return ErrCodexAccess
	}
	d, err := os.Open(dir)
	if err != nil {
		return ErrCodexAccess
	}
	defer func() { _ = d.Close() }()
	if d.Sync() != nil {
		return ErrCodexAccess
	}
	return nil
}
