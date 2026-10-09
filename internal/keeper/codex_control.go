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

	"github.com/thaynes43/dev-env/internal/codexauth"
)

const codexCeremonyLifetime = 15 * time.Minute
const CodexControlSocket = "control.sock"
const codexRefreshControlBound = 30 * time.Second
const codexRefreshResponseWait = codexRefreshControlBound + 5*time.Second

// The socket exists only in the keeper/helper's mode-0700 tmpfs, with mode 0600.
// Reachability is the authenticated pods/exec/control boundary; no network port
// or service is exposed. It never accepts or returns token-bearing JSON.
type codexControlRequest struct {
	AttemptUID         string `json:"attemptUID"`
	ExpectedGeneration uint64 `json:"expectedGeneration,omitempty"`
}
type CodexControlResponse struct {
	OK         bool   `json:"ok"`
	Code       string `json:"code"`
	AttemptUID string `json:"attemptUID,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
}

func (w *codexWorker) serveControl(ctx context.Context) (*http.Server, error) {
	if err := privateCodexDir(w.LoginDir); err != nil {
		return nil, err
	}
	path := filepath.Join(w.LoginDir, CodexControlSocket)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSocket == 0 || os.Remove(path) != nil {
			return nil, errors.New("codex control socket is unavailable")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("codex control socket is unavailable")
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, errors.New("codex control socket is unavailable")
	}
	if os.Chmod(path, 0o600) != nil {
		_ = l.Close()
		return nil, errors.New("codex control socket is unavailable")
	}
	s := &http.Server{ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 4096, BaseContext: func(net.Listener) context.Context { return ctx }}
	s.Handler = http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		response := CodexControlResponse{Code: "InvalidRequest"}
		var in codexControlRequest
		raw, err := io.ReadAll(io.LimitReader(req.Body, 1025))
		if req.Method == http.MethodPost && err == nil && len(raw) <= 1024 && codexauth.DecodeStrict(raw, &in) == nil {
			if !w.mu.TryLock() {
				response.Code = "Busy"
			} else {
				defer w.mu.Unlock()
				switch req.URL.Path {
				case "/begin":
					if in.AttemptUID == "" && in.ExpectedGeneration == 0 {
						response = w.begin(req.Context())
					}
				case "/adopt":
					if in.ExpectedGeneration == 0 {
						response = w.adopt(req.Context(), in.AttemptUID)
					}
				case "/cancel":
					if in.ExpectedGeneration == 0 {
						response = w.cancelLogin(req.Context(), in.AttemptUID)
					}
				case "/status":
					if in.AttemptUID == "" && in.ExpectedGeneration == 0 {
						response = w.controlStatus(req.Context())
					}
				case "/refresh-once":
					if in.AttemptUID == "" && in.ExpectedGeneration > 0 && req.Context().Err() == nil {
						if http.NewResponseController(out).SetWriteDeadline(time.Now().Add(codexRefreshResponseWait)) != nil {
							response.Code = "Unavailable"
							break
						}
						// Once admitted, a lost HTTP reply must not cancel durability
						// after consuming the token. Leadership loss still cancels it.
						transaction, cancel := context.WithTimeout(ctx, codexRefreshControlBound)
						response = w.refreshOnce(transaction, in.ExpectedGeneration)
						cancel()
					}
				}
			}
		}
		out.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(out).Encode(response)
	})
	go func() { _ = s.Serve(l) }()
	go func() { <-ctx.Done(); _ = s.Close(); _ = os.Remove(path) }()
	return s, nil
}

func (w *codexWorker) controlStatus(ctx context.Context) CodexControlResponse {
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return CodexControlResponse{Code: "Unavailable"}
	}
	d, err := w.load(ctx)
	if err != nil {
		return CodexControlResponse{Code: "Unavailable"}
	}
	result := CodexControlResponse{OK: true, Code: "NeedsLogin"}
	if !w.halted && d.Record.Stage == codexReady && d.Record.Access.Validate(w.Clock.Now()) == nil {
		result.Generation = d.Record.Access.Generation
		result.Code = "PendingPublication"
		if w.ready.Load() {
			result.Code = "Ready"
		}
	}
	return result
}

// The handler holds the same mutex as scheduled refresh/adoption. The caller's
// generation fences a duplicate request after a lost successful response.
func (w *codexWorker) refreshOnce(ctx context.Context, expected uint64) CodexControlResponse {
	if expected == 0 {
		return CodexControlResponse{Code: "InvalidRequest"}
	}
	err := w.tickExpected(ctx, expected)
	if errors.Is(err, errCodexGenerationMismatch) {
		return CodexControlResponse{Code: "GenerationMismatch"}
	}
	if err != nil {
		code := "Unavailable"
		if w.halted || errors.Is(err, errCodexRefresh) {
			code = "NeedsLogin"
		}
		return CodexControlResponse{Code: code}
	}
	return CodexControlResponse{OK: true, Code: "Refreshed", Generation: w.published}
}

func privateCodexDir(path string) error {
	if !filepath.IsAbs(path) || os.MkdirAll(path, 0o700) != nil {
		return errors.New("codex private staging is unavailable")
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || os.Chmod(path, 0o700) != nil {
		return errors.New("codex private staging is unavailable")
	}
	return nil
}

func (w *codexWorker) begin(ctx context.Context) CodexControlResponse {
	fail := CodexControlResponse{Code: "Unavailable"}
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return fail
	}
	d, err := w.load(ctx)
	if err != nil {
		return fail
	}
	if w.expiredLoginReservation(d.Record) {
		if w.finishExpiredLogin(ctx, d) != nil {
			_ = w.needsLogin(ctx)
			return fail
		}
		d, err = w.load(ctx)
		if err != nil {
			_ = w.needsLogin(ctx)
			return fail
		}
	}
	r := d.Record
	if !r.LoginUntil.IsZero() && w.Clock.Now().Before(r.LoginUntil) {
		return CodexControlResponse{Code: "Busy"}
	}
	if r.AttemptUID != "" {
		_ = os.RemoveAll(filepath.Join(w.LoginDir, r.AttemptUID))
	}
	id := string(uuid.NewUUID())
	path := filepath.Join(w.LoginDir, id)
	if os.Mkdir(path, 0o700) != nil {
		return fail
	}
	// Preserve only a known Ready, never ambiguously refreshed credential. The
	// durable reservation pauses all refreshes until adoption or explicit cancel.
	r.ResumeOnCancel = r.Stage == codexReady && !w.halted && !r.Refresh.Empty() && r.Access.Validate(w.Clock.Now()) == nil
	if !r.ResumeOnCancel {
		r.Refresh = secretValue{}
	}
	r.Stage, r.AttemptUID, r.LoginUntil = codexNeedsLogin, id, w.Clock.Now().Add(codexCeremonyLifetime)
	r.LoginOwner = w.Identity
	if w.Journal.save(ctx, d.Secret, r) != nil {
		_ = os.RemoveAll(path)
		_ = w.needsLogin(ctx)
		return fail
	}
	w.halted, w.published = true, 0
	w.ready.Store(false)
	return CodexControlResponse{OK: true, Code: "Started", AttemptUID: id}
}

func readCodexPrivateFile(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errCodexRefresh
	}
	f := os.NewFile(uintptr(fd), "Codex private material")
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > codexauth.MaxBytes {
		return nil, errCodexRefresh
	}
	raw, err := io.ReadAll(io.LimitReader(f, codexauth.MaxBytes+1))
	if err != nil || len(raw) > codexauth.MaxBytes {
		return nil, errCodexRefresh
	}
	return raw, nil
}

// nativeCodexLogin only accepts the pinned CLI's full ChatGPT file, from this
// fresh bound attempt. No v1/native host auth is imported by any command.
func nativeCodexLogin(raw []byte, generation uint64, now time.Time) (codexRecord, error) {
	var n struct {
		Mode   string  `json:"auth_mode"`
		APIKey *string `json:"OPENAI_API_KEY"`
		Tokens struct {
			ID      string `json:"id_token"`
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			Account string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh time.Time `json:"last_refresh"`
	}
	if codexauth.DecodeStrict(raw, &n) != nil || n.Mode != "chatgpt" || n.APIKey != nil || n.Tokens.Refresh == "" || len(n.Tokens.Refresh) > codexauth.MaxTokenBytes {
		return codexRecord{}, errCodexRefresh
	}
	account, exp, err := codexauth.Claims(n.Tokens.Access)
	r := codexRecord{Access: codexauth.Access{Generation: generation, AccountID: account, IDToken: n.Tokens.ID, AccessToken: n.Tokens.Access, ExpiresAt: exp, LastRefresh: n.LastRefresh}, Refresh: newSecretValue(n.Tokens.Refresh), Stage: codexReady}
	if err != nil || account != n.Tokens.Account || r.Access.Validate(now) != nil || !exp.After(now.Add(codexauth.MinLifetime)) || n.LastRefresh.Before(now.Add(-codexCeremonyLifetime)) {
		return codexRecord{}, errCodexRefresh
	}
	return r, nil
}

func (w *codexWorker) adopt(ctx context.Context, id string) CodexControlResponse {
	fail := CodexControlResponse{Code: "NeedsLogin"}
	if !validUID(id) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return fail
	}
	d, err := w.load(ctx)
	if err != nil || d.Record.Stage != codexNeedsLogin || d.Record.AttemptUID != id || d.Record.LoginOwner != w.Identity || !w.Clock.Now().Before(d.Record.LoginUntil) {
		return fail
	}
	path := filepath.Join(w.LoginDir, id)
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fail
	}
	raw, err := readCodexPrivateFile(filepath.Join(path, "auth.json"))
	if err != nil {
		return fail
	}
	next, err := nativeCodexLogin(raw, d.Record.Access.Generation+1, w.Clock.Now())
	if err != nil {
		return fail
	}
	if d.Record.Access.AccountID != "" && next.Access.AccountID != d.Record.Access.AccountID {
		return CodexControlResponse{Code: "AccountMismatch"}
	}
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return fail
	}
	next.Stage, next.AttemptUID, next.LoginUntil, next.LoginOwner = codexNeedsLogin, id, d.Record.LoginUntil, w.Identity
	if w.Journal.save(ctx, d.Secret, next) != nil {
		_ = w.needsLogin(ctx)
		_ = os.RemoveAll(path)
		return fail
	}
	d, err = w.load(ctx)
	if err != nil || d.Record.Stage != codexNeedsLogin || d.Record.AttemptUID != id || !sameCodexMaterial(d.Record, next) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		_ = w.needsLogin(ctx)
		return fail
	}
	next.Stage, next.AttemptUID, next.LoginUntil, next.LoginOwner = codexReady, "", time.Time{}, ""
	if w.Journal.save(ctx, d.Secret, next) != nil {
		_ = w.needsLogin(ctx)
		_ = os.RemoveAll(path)
		return fail
	}
	_ = os.RemoveAll(path)
	w.halted = false
	w.published = 0
	// Adoption is durable even if publication is temporarily unavailable. The
	// worker retries publication only, without rerunning login or refresh.
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil || w.Publisher.publish(ctx, next.Access, w.Clock.Now()) != nil {
		return CodexControlResponse{OK: true, Code: "AdoptedPendingPublication"}
	}
	w.published = next.Access.Generation
	w.ready.Store(true)
	return CodexControlResponse{OK: true, Code: "Adopted"}
}

func (w *codexWorker) cancelLogin(ctx context.Context, id string) CodexControlResponse {
	fail := CodexControlResponse{Code: "Unavailable"}
	if !validUID(id) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return fail
	}
	d, err := w.load(ctx)
	if err != nil || d.Record.Stage != codexNeedsLogin || d.Record.AttemptUID != id || d.Record.LoginOwner != w.Identity {
		return fail
	}
	r := d.Record
	restore := r.ResumeOnCancel && !r.Refresh.Empty() && r.Access.Validate(w.Clock.Now()) == nil
	r.LoginUntil, r.LoginOwner, r.AttemptUID, r.ResumeOnCancel = time.Time{}, "", "", false
	if restore {
		r.Stage = codexReady
	} else {
		r.Refresh = secretValue{}
	}
	if w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return fail
	}
	if w.Journal.save(ctx, d.Secret, r) != nil {
		_ = w.needsLogin(ctx)
		return fail
	}
	_ = os.RemoveAll(filepath.Join(w.LoginDir, id))
	if restore {
		w.halted, w.published = false, 0
	}
	return CodexControlResponse{OK: true, Code: "Cancelled"}
}

func (w *codexWorker) expiredLoginReservation(r codexRecord) bool {
	return r.Stage == codexNeedsLogin && r.ResumeOnCancel && !r.LoginUntil.IsZero() && !w.Clock.Now().Before(r.LoginUntil)
}

// An expired reservation, unlike a recovered refresh/material intent, durably
// proves this is the previously known unused credential. Any current leader may
// finish it, but only a confirmed fenced save resumes a still-valid old login.
func (w *codexWorker) finishExpiredLogin(ctx context.Context, d *codexLoaded) error {
	if !w.expiredLoginReservation(d.Record) || w.Fence(ctx, codexSaveTimeout+codexSafetyMargin) != nil {
		return errCodexRefresh
	}
	r := d.Record
	id := r.AttemptUID
	restore := !r.Refresh.Empty() && r.Access.Validate(w.Clock.Now()) == nil
	r.AttemptUID, r.LoginUntil, r.LoginOwner, r.ResumeOnCancel = "", time.Time{}, "", false
	if restore {
		r.Stage = codexReady
	} else {
		r.Refresh = secretValue{}
	}
	if w.Journal.save(ctx, d.Secret, r) != nil {
		return errCodexJournal
	}
	_ = os.RemoveAll(filepath.Join(w.LoginDir, id))
	w.halted, w.published = !restore, 0
	w.ready.Store(false)
	return nil
}
