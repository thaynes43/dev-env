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

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// The keeper's Claude live-credential projection (plan 03, Q-11/D-11,
// Story110 T09 increment 1). A profile that sets AGENTD_CLAUDE_LIVE_FILE makes
// the pod follow the keeper's single Max login: agentd merges the projection's
// access fields into the pod's own .credentials.json and never holds, writes or
// runs a refresh. Without the variable nothing here runs (the task's static
// token and the pod-owned login of remote.go are unchanged). Errors are fixed
// text: no token value reaches an error, a log line or a status.

var (
	// ErrClaudeAccess: the projection is missing, invalid, expired or older
	// than the generation already applied.
	ErrClaudeAccess = errors.New("claude authentication requires a current keeper projection")
	// ErrClaudeLoginConflict: the pod's credentials hold a refresh token of
	// their own, so a pod-owned login and the keeper's would refresh one family
	// twice. Nothing is merged and the agent is not started.
	ErrClaudeLoginConflict = errors.New("login-conflict: this pod's Claude credentials hold their own login while the keeper projection is configured; nothing was merged and the agent was not started")
	// ErrClaudeProjectionLogin: the login ceremony does not run in projection mode.
	ErrClaudeProjectionLogin = errors.New("this pod follows the keeper's Claude login; no login is run here")
)

type claudeReceipt struct {
	Generation  uint64 `json:"generation"`
	Fingerprint string `json:"fingerprint"`
}

// readBounded reads a regular file of at most max bytes. A symlink is followed
// (a mounted Secret is a link into its data directory); the opened file must
// be regular.
func readBounded(path string, max int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > max {
		return nil, ErrClaudeAccess
	}
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(raw)) > max {
		return nil, ErrClaudeAccess
	}
	return raw, nil
}

// readClaudeCredentials returns the pod's credentials file bytes, nil for a
// missing file, and an error for a link or anything that is not a bounded
// regular file.
func readClaudeCredentials(s Settings) ([]byte, error) {
	path := filepath.Join(s.ClaudeConfigDir, credentialsFileName)
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxLoginFileBytes {
		return nil, ErrClaudeAccess
	}
	return readBounded(path, maxLoginFileBytes)
}

// SyncClaudeAccess applies the keeper's projection once: it validates the
// document, fences generation rollback, merges the access fields into the
// pod's credentials file (0600, atomic rename, every CLI-owned member kept)
// and seeds oauthAccount. It is idempotent and safe to call every poll. It
// never runs the CLI and never writes a refresh token.
func SyncClaudeAccess(s Settings, now time.Time) error {
	if s.ClaudeLiveFile == "" {
		return nil
	}
	if !filepath.IsAbs(s.ClaudeLiveFile) || !filepath.IsAbs(s.ClaudeConfigDir) || !filepath.IsAbs(s.StateDir) {
		return ErrClaudeAccess
	}
	// A pod-owned login is a conflict whatever the projection holds, so a
	// missing or older projection cannot hide it.
	existing, err := readClaudeCredentials(s)
	if err != nil {
		return ErrClaudeAccess
	}
	if claudeauth.HoldsRefreshToken(existing) {
		return ErrClaudeLoginConflict
	}
	raw, err := readBounded(s.ClaudeLiveFile, claudeauth.MaxBytes)
	if err != nil {
		return ErrClaudeAccess
	}
	a, err := claudeauth.Decode(raw, now)
	if err != nil {
		return ErrClaudeAccess
	}
	canonical, err := claudeauth.Encode(a, now)
	if err != nil {
		return ErrClaudeAccess
	}
	sum := sha256.Sum256(canonical)
	next := claudeReceipt{a.Generation, hex.EncodeToString(sum[:])}
	receiptPath := s.statePath("claude-access-generation.json")
	receiptCurrent := false
	if data, err := readBounded(receiptPath, 4<<10); err == nil {
		var old claudeReceipt
		if codexauth.DecodeStrict(data, &old) != nil || old.Generation == 0 || len(old.Fingerprint) != 64 ||
			a.Generation < old.Generation || (a.Generation == old.Generation && next.Fingerprint != old.Fingerprint) {
			return ErrClaudeAccess
		}
		receiptCurrent = old == next
	} else if _, err := os.Lstat(receiptPath); !os.IsNotExist(err) {
		return ErrClaudeAccess
	}

	merged, changed, err := claudeauth.MergeCredentials(existing, a)
	switch {
	case errors.Is(err, claudeauth.ErrPodOwnedLogin):
		return ErrClaudeLoginConflict
	case err != nil:
		return ErrClaudeAccess
	}

	// Persist the generation fence first: if the credentials write fails, an
	// older projection cannot overwrite the newer generation on restart. The
	// same generation is always safe to apply again.
	if !receiptCurrent {
		receipt, _ := json.Marshal(next)
		if writeClaudePrivateAtomic(receiptPath, receipt) != nil {
			return ErrClaudeAccess
		}
	}
	credPath := filepath.Join(s.ClaudeConfigDir, credentialsFileName)
	if changed {
		if writeClaudePrivateAtomic(credPath, merged) != nil {
			return ErrClaudeAccess
		}
	} else if fi, err := os.Lstat(credPath); err == nil && fi.Mode().Perm() != 0o600 {
		if os.Chmod(credPath, 0o600) != nil {
			return ErrClaudeAccess
		}
	}
	if ensureOAuthAccount(s.ClaudeState, oauthAccount{AccountUUID: a.AccountUUID, OrganizationUUID: a.OrganizationUUID}) != nil {
		return ErrClaudeAccess
	}
	return nil
}

// writeClaudePrivateAtomic writes raw beside path with mode 0600 and renames
// it into place, so a reader sees the whole old file or the whole new one.
func writeClaudePrivateAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if os.MkdirAll(dir, 0o700) != nil {
		return ErrClaudeAccess
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return ErrClaudeAccess
	}
	f, err := os.CreateTemp(dir, ".claude-access-*")
	if err != nil {
		return ErrClaudeAccess
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if f.Chmod(0o600) != nil {
		return ErrClaudeAccess
	}
	if _, err = f.Write(raw); err != nil {
		return ErrClaudeAccess
	}
	if f.Sync() != nil || f.Close() != nil || os.Rename(f.Name(), path) != nil {
		return ErrClaudeAccess
	}
	return nil
}

// checkProjectedLogin is checkLogin in projection mode: the login is the
// merged access token, usable until its own expiry. Refresh material in the
// file is a pod-owned login, which projection mode does not accept.
func checkProjectedLogin(s Settings) loginCheck {
	data, err := readClaudeCredentials(s)
	if err != nil || len(data) == 0 {
		return loginCheck{}
	}
	var shape credentialsShape
	if json.Unmarshal(data, &shape) != nil || shape.ClaudeAiOauth == nil {
		return loginCheck{}
	}
	o := shape.ClaudeAiOauth
	if o.AccessToken == "" || o.RefreshToken != "" || o.ExpiresAt <= 0 {
		return loginCheck{}
	}
	c := loginCheck{Present: true}
	if o.ExpiresAt < 1e11 {
		c.Expires = time.Unix(o.ExpiresAt, 0).UTC()
	} else {
		c.Expires = time.UnixMilli(o.ExpiresAt).UTC()
	}
	return c
}

// claudePendingWindow is how long after its expiry an applied access token is
// reported as a pending refresh rather than a login to renew. The keeper
// refreshes ahead of expiry; a Secret volume is ASSUMED (not proven) to trail
// the keeper by about a minute (kubelet sync), so a successor may be that late.
// Past the window the wait is reported as before. Beyond the status text this
// changes `agent-run rc start --wait`: it waits through the window instead of
// asking for owner action after 30 seconds of the old login-required text. The
// window (3 minutes) is longer than rc's 90 second agent-start budget, so a refresh
// that never lands ends there as timed_out / auth_refresh_pending, not as an
// owner action.
const claudePendingWindow = 3 * time.Minute

// projectionLoginStatus is the one plain status line in projection mode. An
// applied token that expired within claudePendingWindow is "refreshing": the
// keeper's next generation is pending and no login is needed. Anything else
// that is not usable is "login-required", as before.
func projectionLoginStatus(s Settings, now time.Time) string {
	return projectionStatusWith(s, now, lookupRejection(s, now))
}

// projectionStatusWith is projectionLoginStatus built on one rejection view (a
// single read of the marker, plus any rejection agentd saw but could not write).
func projectionStatusWith(s Settings, now time.Time, v rejectionView) string {
	if data, err := readClaudeCredentials(s); err == nil && claudeauth.HoldsRefreshToken(data) {
		return ErrClaudeLoginConflict.Error()
	}
	c := checkProjectedLogin(s)
	if c.Usable(now) {
		// Unexpired: a rejection recorded for this generation (or a marker that
		// cannot be believed) outranks "logged in".
		if text := rejectionUsableText(v, now); text != "" {
			return text
		}
		return "logged in on the keeper's projection, access token lapses " + c.Expires.Format(time.RFC3339)
	}
	// Expired: the expiry text stands (refreshing inside the pending window,
	// login-required after, or when the login itself lapsed), with the recorded
	// rejection appended so it is never hidden by "refreshing".
	if c.Present && !c.Expires.IsZero() && !projectedLoginLapsed(s, now) {
		if ago := now.Sub(c.Expires); ago >= 0 && ago < claudePendingWindow {
			return "refreshing: the access token lapsed " + ago.Round(time.Second).String() + " ago; waiting for the keeper's next generation; no login is needed unless it stays unpublished" + rejectionExpiredNote(s, v)
		}
	}
	return "login-required: waiting for the keeper's Claude projection; no login is run in this pod" + rejectionExpiredNote(s, v)
}

// projectedLoginLapsed reports whether the non-secret login.json the keeper
// publishes beside the projection says the whole login has lapsed. A missing,
// oversized or invalid document is "not known to have lapsed", as in auth status.
func projectedLoginLapsed(s Settings, now time.Time) bool {
	raw, err := readBounded(filepath.Join(filepath.Dir(s.ClaudeLiveFile), claudeauth.LoginKey), 1<<10)
	if err != nil {
		return false
	}
	lapse, err := claudeauth.DecodeLogin(raw)
	return err == nil && !lapse.After(now)
}
