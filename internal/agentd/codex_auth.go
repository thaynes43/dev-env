package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	raw, err := readCodexAccessFile(s.CodexAccessFile)
	if err != nil {
		return ErrCodexAccess
	}
	a, err := codexauth.Decode(raw, now)
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
