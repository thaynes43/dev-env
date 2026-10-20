package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/claudelogin"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// DefaultClaudeLoginDir is the keeper/helper memory-backed directory.
const DefaultClaudeLoginDir = claudelogin.DefaultDir

// claudeMinLifetime: a staged access token must outlive the adoption by at
// least the CLI's own five minute refresh margin.
const claudeMinLifetime = 5 * time.Minute

// claudeAttempt is the one staging directory a helper may have open. It lives
// in memory only: a keeper restart forgets it, and the helper's next adopt is
// refused, so a ceremony is simply started again. Nothing about it is journaled.
type claudeAttempt struct {
	uid   string
	until time.Time
}

// serveControl serves begin/adopt/cancel/status on a private unix socket in the
// memory-backed login directory (mode 0600, reachable only through the
// authenticated pods/exec boundary). No network port or Service exists. It
// never accepts or returns token-bearing JSON.
func (w *claudeWorker) serveControl(ctx context.Context) error {
	unavailable := errors.New("claude control socket is unavailable")
	if !filepath.IsAbs(w.LoginDir) || os.MkdirAll(w.LoginDir, 0o700) != nil {
		return unavailable
	}
	if fi, err := os.Lstat(w.LoginDir); err != nil || !fi.IsDir() || os.Chmod(w.LoginDir, 0o700) != nil {
		return unavailable
	}
	path := filepath.Join(w.LoginDir, claudelogin.ControlSocket)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 || os.Remove(path) != nil {
			return unavailable
		}
	} else if !os.IsNotExist(err) {
		return unavailable
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return unavailable
	}
	if os.Chmod(path, 0o600) != nil {
		_ = l.Close()
		return unavailable
	}
	s := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return ctx }}
	s.Handler = http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		response := claudelogin.Response{Code: "InvalidRequest"}
		var in claudelogin.Request
		raw, err := io.ReadAll(io.LimitReader(req.Body, 1025))
		if req.Method == http.MethodPost && err == nil && len(raw) <= 1024 && codexauth.DecodeStrict(raw, &in) == nil {
			if !w.mu.TryLock() {
				response.Code = "Busy"
			} else {
				switch req.URL.Path {
				case "/begin":
					if in.AttemptUID == "" {
						response = w.begin(req.Context())
					}
				case "/adopt":
					response = w.adopt(req.Context(), in.AttemptUID)
				case "/cancel":
					response = w.cancelLogin(in.AttemptUID)
				case "/status":
					if in.AttemptUID == "" {
						response = w.controlStatus(req.Context())
					}
				}
				w.mu.Unlock()
			}
		}
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).Encode(response)
	})
	go func() { _ = s.Serve(l) }()
	go func() { <-ctx.Done(); _ = s.Close(); _ = os.Remove(path) }()
	return nil
}

func (w *claudeWorker) controlStatus(ctx context.Context) claudelogin.Response {
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return claudelogin.Response{Code: "Unavailable"}
	}
	_, r, err := w.load(ctx)
	if err != nil {
		return claudelogin.Response{Code: "Unavailable"}
	}
	result := claudelogin.Response{OK: true, Code: "NeedsLogin"}
	if r.Stage == claudeReady && !r.Access.ExpiresAt.IsZero() {
		// Times only (NB-01 status): never a token. The login's own lapse is the
		// refresh token's expiry, when the provider said one.
		at := r.Access.ExpiresAt.UTC()
		result.AccessExpiresAt = &at
		if !r.RefreshExpiresAt.IsZero() {
			lt := r.RefreshExpiresAt.UTC()
			result.LoginExpiresAt = &lt
		}
	}
	if r.Stage == claudeReady && r.Access.Validate(w.Clock.Now()) == nil {
		result.Generation = r.Access.Generation
		result.Code = "PendingPublication"
		if w.published == r.Access.Generation {
			result.Code = "Ready"
		}
	}
	return result
}

// begin makes the one staging directory a helper may use. A second begin while
// an unexpired attempt exists is Busy; an expired one is wiped first.
func (w *claudeWorker) begin(ctx context.Context) claudelogin.Response {
	fail := claudelogin.Response{Code: "Unavailable"}
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return fail
	}
	if w.attempt.uid != "" {
		if w.Clock.Now().Before(w.attempt.until) {
			return claudelogin.Response{Code: "Busy"}
		}
		w.dropAttempt()
	}
	id := string(uuid.NewUUID())
	if os.Mkdir(filepath.Join(w.LoginDir, id), 0o700) != nil {
		return fail
	}
	w.attempt = claudeAttempt{uid: id, until: w.Clock.Now().Add(claudelogin.Lifetime)}
	return claudelogin.Response{OK: true, Code: "Started", AttemptUID: id}
}

func (w *claudeWorker) cancelLogin(id string) claudelogin.Response {
	if !claudelogin.ValidUID(id) || w.attempt.uid != id {
		return claudelogin.Response{Code: "Unavailable"}
	}
	w.dropAttempt()
	return claudelogin.Response{OK: true, Code: "Cancelled"}
}

// dropAttempt wipes the attempt's staging and forgets it. Staged credentials are
// single use, so every adoption outcome ends here.
func (w *claudeWorker) dropAttempt() {
	if w.attempt.uid != "" {
		_ = os.RemoveAll(filepath.Join(w.LoginDir, w.attempt.uid))
	}
	w.attempt = claudeAttempt{}
}

// readClaudePrivateFile reads a regular 0600 file without following a symlink.
func readClaudePrivateFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errClaudeRefresh
	}
	f := os.NewFile(uintptr(fd), "Claude private material")
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > claudeauth.MaxBytes {
		return nil, errClaudeRefresh
	}
	raw, err := io.ReadAll(io.LimitReader(f, claudeauth.MaxBytes+1))
	if err != nil || len(raw) > claudeauth.MaxBytes {
		return nil, errClaudeRefresh
	}
	return raw, nil
}

// stagedClaudeLogin accepts only a credential a fresh `claude auth login`
// would write: both tokens, an access token that outlives the adoption, and the
// profile and inference scopes. The account and organization ids and the
// subscription are filled in by adopt from the provider's profile; a refresh
// token's own expiry is kept when the CLI wrote one.
func stagedClaudeLogin(raw []byte, now time.Time) (claudeRecord, error) {
	var n struct {
		O *struct {
			AccessToken           string   `json:"accessToken"`
			RefreshToken          string   `json:"refreshToken"`
			ExpiresAt             int64    `json:"expiresAt"`
			RefreshTokenExpiresAt int64    `json:"refreshTokenExpiresAt"`
			Scopes                []string `json:"scopes"`
			SubscriptionType      string   `json:"subscriptionType"`
			RateLimitTier         string   `json:"rateLimitTier"`
		} `json:"claudeAiOauth"`
	}
	if codexauth.DecodeTolerant(raw, &n) != nil || n.O == nil {
		return claudeRecord{}, errClaudeRefresh
	}
	o := n.O
	exp := time.UnixMilli(o.ExpiresAt).UTC()
	if !claudeToken(o.AccessToken) || !claudeToken(o.RefreshToken) || o.ExpiresAt <= 0 || !exp.After(now.Add(claudeMinLifetime)) || exp.After(now.Add(claudeMaxLifetime)) ||
		len(o.Scopes) == 0 || len(o.Scopes) > 32 || !hasString(o.Scopes, "user:profile") || !hasString(o.Scopes, "user:inference") {
		return claudeRecord{}, errClaudeRefresh
	}
	for _, s := range o.Scopes {
		if !claudeField(s) {
			return claudeRecord{}, errClaudeRefresh
		}
	}
	r := claudeRecord{
		Access:  claudeauth.Access{AccessToken: o.AccessToken, ExpiresAt: exp, Scopes: o.Scopes, SubscriptionType: o.SubscriptionType, RateLimitTier: o.RateLimitTier},
		Refresh: newSecretValue(o.RefreshToken), LastRefresh: now, Stage: claudeReady,
	}
	if o.RefreshTokenExpiresAt > 0 {
		if r.RefreshExpiresAt = time.UnixMilli(o.RefreshTokenExpiresAt).UTC(); !r.RefreshExpiresAt.After(now) {
			return claudeRecord{}, errClaudeRefresh
		}
	}
	return r, nil
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// adopt moves one staged login into the journal. It runs only while holding the
// worker's mutex, so it can never overlap a refresh in flight; a held mutex is
// answered Busy before this runs. Whatever the outcome, the staging is wiped
// and the attempt is spent: the staged refresh token is single use.
func (w *claudeWorker) adopt(ctx context.Context, id string) claudelogin.Response {
	fail := claudelogin.Response{Code: "NeedsLogin"}
	if !claudelogin.ValidUID(id) || w.attempt.uid != id || !w.Clock.Now().Before(w.attempt.until) {
		if w.attempt.uid != "" && w.attempt.uid == id {
			w.dropAttempt()
		}
		return fail
	}
	defer w.dropAttempt()
	unavailable := claudelogin.Response{Code: "Unavailable"}
	home := filepath.Join(w.LoginDir, id)
	for _, d := range []string{home, filepath.Join(home, ".claude")} {
		if fi, err := os.Lstat(d); err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return fail
		}
	}
	raw, err := readClaudePrivateFile(filepath.Join(home, claudelogin.StagedCredentials))
	if err != nil {
		return fail
	}
	next, err := stagedClaudeLogin(raw, w.Clock.Now())
	if err != nil {
		return fail
	}
	// Leadership with room for the profile request and the save, before the
	// journal is read.
	if w.Fence(ctx, claudeRequestBudget) != nil {
		return unavailable
	}
	secret, prev, err := w.load(ctx)
	if err != nil {
		return unavailable
	}
	floor, err := w.Publisher.generation(ctx)
	if err != nil {
		return unavailable
	}
	gen, err := nextClaudeGeneration(prev.Access.Generation, floor)
	if err != nil {
		return unavailable
	}
	// The profile names the account and organization and the subscription for
	// this very access token. A GET: failing here spends nothing.
	profile, err := w.Transport.profile(ctx, newSecretValue(next.Access.AccessToken))
	if err != nil {
		return unavailable
	}
	if prev.Access.AccountUUID != "" && prev.Access.AccountUUID != profile.AccountUUID {
		return claudelogin.Response{Code: "AccountMismatch"}
	}
	next.Access.Generation = gen
	next.Access.AccountUUID, next.Access.OrganizationUUID = profile.AccountUUID, profile.OrganizationUUID
	if profile.SubscriptionType != "" {
		next.Access.SubscriptionType = profile.SubscriptionType
	}
	if profile.RateLimitTier != "" {
		next.Access.RateLimitTier = profile.RateLimitTier
	}
	if next.Access.Validate(w.Clock.Now()) != nil || !validClaudeRecord(next) {
		return fail
	}
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil {
		return unavailable
	}
	// One compare-and-swap on the resourceVersion read above: a newer leader's
	// write makes it conflict and nothing is overwritten.
	if w.Journal.save(ctx, secret, next) != nil {
		return unavailable
	}
	w.note("adopted")
	// Adoption is durable even if publication is unavailable now; the next tick
	// retries publication only.
	if w.Fence(ctx, claudeSaveTimeout+claudeSafetyMargin) != nil || w.Publisher.publish(ctx, next.Access, next.RefreshExpiresAt, w.Clock.Now()) != nil {
		return claudelogin.Response{OK: true, Code: "AdoptedPendingPublication", Generation: gen}
	}
	w.published = gen
	return claudelogin.Response{OK: true, Code: "Adopted", Generation: gen}
}
