package usagecollector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const CodexRPCSchema = providerallowance.CodexRPCSchema

type CodexAccountCapture = providerallowance.CodexAccountCapture
type CodexWindow = providerallowance.CodexWindow

// CaptureError carries only fixed phase/category labels and an optional JSON-RPC
// error code. Provider messages, stdout/stderr, identities and paths are excluded.
type CaptureError struct {
	Stage string
	Code  int
	Kind  string
}

func (e *CaptureError) Error() string { return "provider capture unavailable" }
func (e *CaptureError) Unwrap() error { return ErrCapture }
func atStage(stage string, err error) error {
	e := &CaptureError{Stage: stage, Kind: "source_refused"}
	var prior *CaptureError
	if errors.As(err, &prior) {
		e.Kind = prior.Kind
		e.Code = prior.Code
	}
	return e
}
func FailureCode(err error) string {
	var e *CaptureError
	if !errors.As(err, &e) {
		return "unavailable"
	}
	switch e.Stage {
	case "initialize", "account_before", "quota", "account_after", "account_binding", "quota_shape", "process_start":
	default:
		return "unavailable"
	}
	if e.Kind == "credentials_refresh_requested" {
		return e.Stage + ":credentials_refresh_requested"
	}
	if e.Kind == "account_metadata_changed" {
		return e.Stage + ":account_metadata_changed"
	}
	if e.Kind == "rpc_framing" || e.Kind == "rpc_shape" || e.Kind == "rpc_request" {
		return e.Stage + ":" + e.Kind
	}
	if e.Kind == "rpc_error" {
		return fmt.Sprintf("%s:rpc_error_%d", e.Stage, e.Code)
	}
	return e.Stage + ":source_refused"
}

type rpcWire struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
	Params json.RawMessage `json:"params"`
}
type rpcClient struct {
	in       io.Writer
	out      *bufio.Reader
	messages int
	account  codexAccount
	bracket  bool
}

// CollectCodex performs only initialization, three non-forced account reads and
// one quota read in an explicitly selected installed official CLI. Account reads
// bracket the quota read and must agree. No thread/model/auth mutation is sent.
// PRECONDITION: the selected home must be independently qualified access-only,
// such as the keeper codexauth projection. A rotating refresh-token login is
// forbidden: the CLI could refresh internally without a JSON-RPC request. The
// CLI requires an explicit caller acknowledgement, not an auth-file inspection.
// The child uses its normal existing authentication; this code opens no auth
// files and services no credential-refresh requests. It always stops/reaps it.
func CollectCodex(ctx context.Context, executable string, now func() time.Time) (CodexAccountCapture, error) {
	if ctx == nil || ctx.Err() != nil || executable == "" || now == nil {
		return CodexAccountCapture{}, ErrCapture
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "app-server", "--listen", "stdio://")
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		return CodexAccountCapture{}, ErrCapture
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return CodexAccountCapture{}, ErrCapture
	}
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return CodexAccountCapture{}, atStage("process_start", err)
	}
	stopped := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = out.Close()
		case <-stopped:
		}
	}()
	defer func() {
		_ = in.Close()
		cancel()
		_ = out.Close()
		_ = cmd.Wait()
		close(stopped)
		<-watcherDone
	}()
	return collectCodexRPC(in, out, now)
}

func collectCodexRPC(in io.Writer, out io.Reader, now func() time.Time) (CodexAccountCapture, error) {
	rpc := rpcClient{in: in, out: bufio.NewReaderSize(out, 64<<10)}
	if _, err := rpc.call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "dev_env_usage_collector", "version": "1"}, "capabilities": map[string]any{"experimentalApi": false}}); err != nil {
		return CodexAccountCapture{}, atStage("initialize", err)
	}
	if err := rpc.send(map[string]any{"method": "initialized"}); err != nil {
		return CodexAccountCapture{}, ErrCapture
	}
	before, err := rpc.call(2, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return CodexAccountCapture{}, atStage("account_before", err)
	}
	account, err := decodeAccount(before)
	if err != nil {
		return CodexAccountCapture{}, atStage("account_binding", err)
	}
	rpc.account = account
	// Establish a second account barrier before the quota bracket. Initial auth
	// notifications are processed only during setup; every mid-bracket auth
	// update invalidates the observation, even with the same plan/mode.
	barrier, err := rpc.call(3, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return CodexAccountCapture{}, atStage("account_before", err)
	}
	barrierAccount, err := decodeAccount(barrier)
	if err != nil || barrierAccount != account {
		return CodexAccountCapture{}, atStage("account_binding", err)
	}
	rpc.bracket = true
	quota, err := rpc.call(4, "account/rateLimits/read", nil)
	if err != nil {
		return CodexAccountCapture{}, atStage("quota", err)
	}
	received := now()
	after, err := rpc.call(5, "account/read", map[string]bool{"refreshToken": false})
	if err != nil {
		return CodexAccountCapture{}, atStage("account_after", err)
	}
	last, err := decodeAccount(after)
	if err != nil || last != account {
		return CodexAccountCapture{}, atStage("account_binding", err)
	}
	report, err := sanitizeCodexQuota(account, quota, received)
	if err != nil {
		return CodexAccountCapture{}, atStage("quota_shape", err)
	}
	return report, nil
}
func (r *rpcClient) send(value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return ErrCapture
	}
	body = append(body, '\n')
	n, err := r.in.Write(body)
	if err != nil || n != len(body) {
		return ErrCapture
	}
	return nil
}
func (r *rpcClient) call(id int, method string, params any) (json.RawMessage, error) {
	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	if r.send(req) != nil {
		return nil, ErrCapture
	}
	for r.messages < 64 {
		line, err := r.out.ReadSlice('\n')
		r.messages++
		if err != nil || len(line) > 64<<10 {
			return nil, &CaptureError{Kind: "rpc_framing"}
		}
		// Reuse the bounded duplicate-key detector before normal decoding. RPC
		// envelopes may contain optional fields; no raw bytes enter error messages.
		if !providerallowance.ValidJSONDocument(line, 64<<10) || !providerallowance.CanonicalFieldNames(line, "id", "method", "result", "error", "params") {
			return nil, &CaptureError{Kind: "rpc_shape"}
		}
		var msg rpcWire
		if json.Unmarshal(line, &msg) != nil {
			return nil, ErrCapture
		}
		if len(msg.ID) == 0 || bytes.Equal(msg.ID, []byte("null")) {
			// The server also emits initial auth metadata, which is not an
			// account switch. Matching mode/plan metadata does not replace the
			// independent before/after account identity bracket. Incompatible
			// metadata or a credential request invalidates this capture.
			if msg.Method == "account/updated" {
				if r.bracket {
					return nil, &CaptureError{Kind: "account_metadata_changed"}
				}
				var update struct {
					AuthMode string  `json:"authMode"`
					Plan     *string `json:"planType"`
				}
				if !providerallowance.CanonicalFieldNames(msg.Params, "authMode", "planType") || json.Unmarshal(msg.Params, &update) != nil || update.AuthMode != "chatgpt" || rpcAccountPlanMismatch(r.account, update.Plan) {
					return nil, &CaptureError{Kind: "account_metadata_changed"}
				}
			}
			continue
		}
		if len(msg.Error) > 0 && !bytes.Equal(msg.Error, []byte("null")) {
			var failure struct {
				Code int `json:"code"`
			}
			if !providerallowance.CanonicalFieldNames(msg.Error, "code", "message", "data") || json.Unmarshal(msg.Error, &failure) != nil {
				return nil, ErrCapture
			}
			return nil, &CaptureError{Kind: "rpc_error", Code: failure.Code}
		}
		if msg.Method == "account/chatgptAuthTokens/refresh" {
			return nil, &CaptureError{Kind: "credentials_refresh_requested"}
		}
		if msg.Method != "" {
			return nil, &CaptureError{Kind: "rpc_request"}
		}
		var got int
		if msg.Method != "" || json.Unmarshal(msg.ID, &got) != nil || got != id || len(msg.Error) > 0 && !bytes.Equal(msg.Error, []byte("null")) || len(msg.Result) == 0 {
			return nil, ErrCapture
		}
		return append(json.RawMessage(nil), msg.Result...), nil
	}
	return nil, ErrCapture
}

type codexAccount struct{ ref, plan string }

func decodeAccount(raw []byte) (codexAccount, error) {
	var result struct {
		Account *struct {
			Type  string  `json:"type"`
			Email *string `json:"email"`
			Plan  string  `json:"planType"`
		} `json:"account"`
	}
	if !providerallowance.CanonicalFieldNames(raw, "account", "requiresOpenaiAuth") || json.Unmarshal(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" || result.Account.Email == nil {
		return codexAccount{}, ErrCapture
	}
	var accountFields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &accountFields)
	if !providerallowance.CanonicalFieldNames(accountFields["account"], "type", "email", "planType") {
		return codexAccount{}, ErrCapture
	}
	email := *result.Account.Email
	if len(email) == 0 || len(email) > 512 || strings.TrimSpace(email) != email || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n\x00") {
		return codexAccount{}, ErrCapture
	}
	// Only documented personal plan types qualify account-only scope. An org
	// account needs its independently qualified workspace, never an invented one.
	if !providerallowance.PersonalCodexPlan(result.Account.Plan) {
		return codexAccount{}, ErrCapture
	}
	hash := sha256.Sum256([]byte(email))
	return codexAccount{ref: "openai-email-sha256:" + hex.EncodeToString(hash[:]), plan: result.Account.Plan}, nil
}
func sanitizeCodexQuota(account codexAccount, raw []byte, received time.Time) (CodexAccountCapture, error) {
	if received.IsZero() {
		return CodexAccountCapture{}, ErrCapture
	}
	type bucket struct {
		ID        *string      `json:"limitId"`
		Plan      *string      `json:"planType"`
		Primary   *CodexWindow `json:"primary"`
		Secondary *CodexWindow `json:"secondary"`
	}
	var result struct {
		RateLimits bucket            `json:"rateLimits"`
		ByID       map[string]bucket `json:"rateLimitsByLimitId"`
	}
	if !providerallowance.CanonicalFieldNames(raw, "rateLimits", "rateLimitsByLimitId") || json.Unmarshal(raw, &result) != nil {
		return CodexAccountCapture{}, ErrCapture
	}
	var rawResult map[string]json.RawMessage
	_ = json.Unmarshal(raw, &rawResult)
	rawBuckets := map[string]json.RawMessage{}
	if v := rawResult["rateLimitsByLimitId"]; len(v) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		_ = json.Unmarshal(v, &rawBuckets)
	}
	if v := rawResult["rateLimits"]; len(v) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		rawBuckets["legacy"] = v
	}
	for _, bucketRaw := range rawBuckets {
		if !providerallowance.CanonicalFieldNames(bucketRaw, "limitId", "planType", "primary", "secondary") {
			return CodexAccountCapture{}, ErrCapture
		}
	}
	selected := result.RateLimits
	if preferred, ok := result.ByID["codex"]; ok {
		a, _ := json.Marshal(selected)
		b, _ := json.Marshal(preferred)
		if selected.ID != nil && *selected.ID == "codex" && !bytes.Equal(a, b) {
			return CodexAccountCapture{}, ErrCapture
		}
		selected = preferred
	}
	if selected.ID == nil || *selected.ID != "codex" || selected.Plan != nil && *selected.Plan != account.plan {
		return CodexAccountCapture{}, ErrCapture
	}
	for _, w := range []*CodexWindow{selected.Primary, selected.Secondary} {
		if w == nil {
			continue
		}
		if w.UsedPercent < 0 || w.UsedPercent > 100 || w.WindowDurationMins != nil && (*w.WindowDurationMins < 1 || *w.WindowDurationMins > 525600) || w.ResetsAt != nil && (*w.ResetsAt < 0 || *w.ResetsAt > 253402300799) {
			return CodexAccountCapture{}, ErrCapture
		}
	}
	return CodexAccountCapture{Schema: CodexRPCSchema, AccountRef: account.ref, IdentityBasis: "provider_email_sha256", PlanType: account.plan, ReceivedAt: received.UTC().Format(time.RFC3339Nano), Primary: selected.Primary, Secondary: selected.Secondary}, nil
}

func rpcAccountPlanMismatch(account codexAccount, plan *string) bool {
	if plan == nil {
		return true
	}
	return account.plan != "" && *plan != account.plan
}
