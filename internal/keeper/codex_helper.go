package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

const CodexLoginVersion = "0.160.1"

var ErrCodexHelper = errors.New("codex login helper requires a fresh authenticated ceremony")

// CodexControl calls the private local socket. Only fixed status and attempt
// metadata cross it; bearer material remains in staging/the private journal.
func CodexControl(ctx context.Context, dir, action, attempt string) (CodexControlResponse, error) {
	if !filepath.IsAbs(dir) || (action != "begin" && action != "adopt" && action != "status" && action != "cancel") || (attempt != "" && !validUID(attempt)) {
		return CodexControlResponse{}, ErrCodexHelper
	}
	return codexControl(ctx, dir, action, codexControlRequest{AttemptUID: attempt}, 10*time.Second)
}

// CodexRefreshOnce never retries a control request whose response is lost. A
// later deliberate call with the same generation cannot spend a replacement.
func CodexRefreshOnce(ctx context.Context, dir string, expected uint64) (CodexControlResponse, error) {
	if !filepath.IsAbs(dir) || expected == 0 {
		return CodexControlResponse{}, ErrCodexHelper
	}
	return codexControl(ctx, dir, "refresh-once", codexControlRequest{ExpectedGeneration: expected}, codexRefreshResponseWait)
}

func codexControl(ctx context.Context, dir, action string, input codexControlRequest, timeout time.Duration) (CodexControlResponse, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	h := &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: func(ctx context.Context, _ string, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", filepath.Join(dir, CodexControlSocket))
	}, DisableKeepAlives: true}}
	raw, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(c, http.MethodPost, "http://keeper/"+action, bytes.NewReader(raw))
	if err != nil {
		return CodexControlResponse{}, ErrCodexHelper
	}
	resp, err := h.Do(req)
	if err != nil {
		return CodexControlResponse{}, ErrCodexHelper
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	var result CodexControlResponse
	if err != nil || len(data) > 1024 || codexauth.DecodeStrict(data, &result) != nil || (result.AttemptUID != "" && !validUID(result.AttemptUID)) {
		return CodexControlResponse{}, ErrCodexHelper
	}
	switch result.Code {
	case "Started", "Adopted", "AdoptedPendingPublication", "Cancelled", "Ready", "PendingPublication", "Refreshed", "NeedsLogin", "Unavailable", "InvalidRequest", "Busy", "AccountMismatch", "GenerationMismatch":
	default:
		return CodexControlResponse{}, ErrCodexHelper
	}
	if (result.Code == "Ready" || result.Code == "PendingPublication" || result.Code == "Refreshed") && result.Generation == 0 {
		return CodexControlResponse{}, ErrCodexHelper
	}
	if result.Code == "Refreshed" && (input.ExpectedGeneration == 0 || result.Generation <= input.ExpectedGeneration) {
		return CodexControlResponse{}, ErrCodexHelper
	}
	return result, nil
}

// CodexLoginHelper is invoked through authenticated interactive control/exec,
// never as the helper container's entrypoint. Native output is filtered to the
// provider challenge; raw diagnostics cannot leak token-bearing response bodies.
func CodexLoginHelper(ctx context.Context, dir, method string, in io.Reader, out io.Writer) (result CodexControlResponse, resultErr error) {
	report := &CodexLoginReport{Phase: "Validation", ReservationState: "NotStarted", CleanupComplete: true, ReservationCleared: true}
	defer func() { result.Login = report }()
	fail := CodexControlResponse{Code: "NeedsLogin"}
	if method != "device" && method != "browser" {
		return CodexControlResponse{Code: "InvalidRequest"}, ErrCodexHelper
	}
	c, cancel := context.WithTimeout(ctx, codexCeremonyLifetime)
	defer cancel()
	versionCtx, versionCancel := context.WithTimeout(c, codexRequestTimeout)
	defer versionCancel()
	report.Phase = "Version"
	v := exec.CommandContext(versionCtx, "/usr/local/bin/codex", "--version")
	v.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "CODEX_HOME=" + dir, "TMPDIR=" + dir, "RUST_LOG=off"}
	v.WaitDelay = time.Second
	version, err := v.Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+CodexLoginVersion {
		return CodexControlResponse{Code: "UnsupportedCLI"}, ErrCodexHelper
	}
	report.Phase = "Reservation"
	// A lost begin reply may conceal a persisted reservation and staging path.
	report.ReservationState, report.ReservationCleared, report.CleanupComplete = "Uncertain", false, false
	r, err := CodexControl(c, dir, "begin", "")
	if err != nil {
		return CodexControlResponse{Code: "Unavailable"}, ErrCodexHelper
	}
	if !r.OK {
		if r.Code == "Busy" || r.Code == "InvalidRequest" {
			report.ReservationState, report.ReservationCleared, report.CleanupComplete = "Rejected", true, true
		}
		return r, ErrCodexHelper
	}
	if !validUID(r.AttemptUID) {
		return fail, ErrCodexHelper
	}
	home := filepath.Join(dir, r.AttemptUID)
	attempt := r.AttemptUID
	adopted := false
	report.ReservationState = "Reserved"
	defer func() {
		finishCodexLoginAttempt(dir, attempt, adopted, report, func() bool {
			r, err := CodexControl(context.Background(), dir, "cancel", attempt)
			return err == nil && r.OK && r.Code == "Cancelled"
		})
	}()
	// Fresh per-attempt home: native login clears existing auth before login.
	// File storage keeps all native credentials/private ceremony logs in tmpfs.
	report.Phase = "Staging"
	tmp := filepath.Join(home, "tmp")
	if os.Mkdir(tmp, 0o700) != nil {
		report.FailureCode = "StagingUnavailable"
		return fail, ErrCodexHelper
	}
	if os.WriteFile(filepath.Join(home, "config.toml"), []byte("cli_auth_credentials_store = \"file\"\n"), 0o600) != nil {
		report.FailureCode = "StagingUnavailable"
		return fail, ErrCodexHelper
	}
	args := []string{"login"}
	if method == "device" {
		args = append(args, "--device-auth")
	}
	cmd := exec.CommandContext(c, "/usr/local/bin/codex", args...)
	cmd.Dir = home
	cmd.Env = codexLoginEnvironment(home, tmp)
	cmd.Stdin = in
	prompt := &codexPromptWriter{Out: out, Device: method == "device", Challenge: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = prompt, prompt
	cmd.WaitDelay = time.Second
	report.Phase = "Native"
	if runCodexLoginProcess(cmd, prompt, cancel, report, codexInitialChallengeBound) != nil {
		return fail, ErrCodexHelper
	}
	report.Phase = "Adoption"
	r, err = CodexControl(c, dir, "adopt", r.AttemptUID)
	if err != nil {
		return CodexControlResponse{Code: "Unavailable"}, ErrCodexHelper
	}
	if !r.OK {
		return r, ErrCodexHelper
	}
	adopted = true
	report.Phase = "Adopted"
	return r, nil
}

func finishCodexLoginAttempt(dir, attempt string, adopted bool, report *CodexLoginReport, cancelAttempt func() bool) {
	// The direct native child has been reaped, but killing its group alone does
	// not prove descendants exited. Retain the fenced attempt unless bounded
	// group absence was observed; expiry remains the recovery path.
	if report.NativeStarted && !report.NativeGroupAbsent {
		return
	}
	report.ReservationCleared = adopted || cancelAttempt()
	if report.ReservationCleared {
		report.ReservationState = "Cleared"
	} else {
		report.ReservationState = "Uncertain"
	}
	// Remove only this attempt after observing the native group absent.
	// A lost cancel acknowledgement still leaves durable expiry fencing.
	home := filepath.Join(dir, attempt)
	_ = os.RemoveAll(home)
	_, err := os.Lstat(home)
	report.CleanupComplete = os.IsNotExist(err)
}

var codexANSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)
var codexCode = regexp.MustCompile(`^[A-Za-z0-9-]{6,32}$`)
var codexURL = regexp.MustCompile(`https://auth\.openai\.com/[^\s]+`)

type codexPromptWriter struct {
	Out         io.Writer
	mu          sync.Mutex
	pending     string
	wantCode    bool
	Device      bool
	Challenge   chan struct{}
	urlSeen     bool
	codeSeen    bool
	failureCode string
}

func (w *codexPromptWriter) Write(raw []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(raw)
	if len(w.pending) > 32768 {
		w.pending = ""
		w.wantCode = false
		return len(raw), nil
	}
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(codexANSI.ReplaceAllString(w.pending[:i], ""))
		if code := fixedCodexNativeFailure(line); code != "" {
			w.failureCode = code
		}
		w.pending = w.pending[i+1:]
		if strings.HasPrefix(line, "2. Enter this one-time code") {
			w.wantCode = true
			continue
		}
		if w.wantCode && line != "" {
			w.wantCode = false
			if codexCode.MatchString(line) {
				w.codeSeen = true
				if json.NewEncoder(w.Out).Encode(map[string]string{"userCode": line}) != nil {
					return 0, ErrCodexHelper
				}
				w.confirmChallenge()
			}
			continue
		}
		if challenge := codexURL.FindString(line); validCodexChallenge(challenge) {
			w.urlSeen = true
			if json.NewEncoder(w.Out).Encode(map[string]string{"verificationUrl": challenge}) != nil {
				return 0, ErrCodexHelper
			}
			w.confirmChallenge()
		}
	}
	return len(raw), nil
}

func validCodexChallenge(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.User != nil || u.Fragment != "" || len(s) > 8192 {
		return false
	}
	if u.Path == "/codex/device" {
		return u.RawQuery == ""
	}
	if u.Path != "/oauth/authorize" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for key := range q {
		switch key {
		case "client_id", "response_type", "scope", "redirect_uri", "code_challenge", "code_challenge_method", "state", "id_token_add_organizations", "codex_cli_simplified_flow", "prompt", "originator":
		default:
			return false
		}
	}
	return q.Get("client_id") == codexClientID && q.Get("state") != "" && q.Get("code_challenge") != ""
}
