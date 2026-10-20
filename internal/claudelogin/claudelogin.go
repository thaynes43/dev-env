// Package claudelogin is the login ceremony for the keeper-owned shared Claude
// login (Story110 T09 increment 5). It mirrors the Codex ceremony: a helper
// container shares a private memory-backed directory with the keeper; the helper
// asks the keeper (over a private unix socket) for a staging directory, runs the
// pinned `claude auth login` in it, prints only the sign-in link to the caller,
// and hands the caller's one-time code to the CLI on stdin. When the CLI exits
// the helper asks the keeper to adopt the staged credential; the keeper parses
// it, writes its journal and wipes the staging.
//
// This package holds the wire types, the socket client and the helper side. It
// reads no credential: the staged file is opened only by the keeper. Only fixed
// codes cross the socket and only the sign-in link crosses the helper's output.
package claudelogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

const (
	// DefaultDir is the memory-backed directory the keeper and the helper share.
	DefaultDir = "/run/dev-env-claude-login/private"
	// ControlSocket is the keeper's socket inside the directory (mode 0600).
	ControlSocket = "control.sock"
	// StagedCredentials is the staged credential file, relative to an attempt
	// directory. The helper runs the CLI with HOME set to the attempt directory
	// and CLAUDE_CONFIG_DIR to its .claude directory.
	StagedCredentials = ".claude/.credentials.json"
	// Lifetime bounds one ceremony, from begin to adopt.
	Lifetime = 15 * time.Minute
)

// ErrHelper is every refusal of the helper or the socket client. It carries no
// detail, so nothing from the CLI or the socket can reach a log through it.
var ErrHelper = errors.New("claude login helper requires a fresh authenticated ceremony")

var uidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

// ValidUID reports whether s is an attempt identifier.
func ValidUID(s string) bool { return uidPattern.MatchString(s) }

// Request is the only body the socket accepts.
type Request struct {
	AttemptUID string `json:"attemptUID"`
}

// Response is the only answer the socket gives. Code is one of the fixed words
// checked in Control.
type Response struct {
	OK         bool    `json:"ok"`
	Code       string  `json:"code"`
	AttemptUID string  `json:"attemptUID,omitempty"`
	Generation uint64  `json:"generation,omitempty"`
	Login      *Report `json:"login,omitempty"`
	// AccessExpiresAt and LoginExpiresAt are what `status` adds (NB-01): when
	// the journaled access token lapses (the keeper renews it) and when the
	// login itself lapses (the refresh token's expiry, absent when the provider
	// did not say). Times only; a keeper from before this field omits them.
	AccessExpiresAt *time.Time `json:"accessExpiresAt,omitempty"`
	LoginExpiresAt  *time.Time `json:"loginExpiresAt,omitempty"`
}

// Report is fixed local flow metadata only. Native output, headers, response
// bodies, attempt IDs and credential values are never included.
type Report struct {
	Phase              string `json:"phase"`
	FailureCode        string `json:"failureCode,omitempty"`
	NativeStarted      bool   `json:"nativeStarted"`
	NativeExitCode     *int   `json:"nativeExitCode,omitempty"`
	NativeGroupAbsent  bool   `json:"nativeGroupAbsent"`
	ChallengePresented bool   `json:"challengePresented"`
	AttemptCleared     bool   `json:"attemptCleared"`
	CleanupComplete    bool   `json:"cleanupComplete"`
}

// Control calls the keeper's private socket. action is begin, adopt, cancel or
// status; attempt is required for adopt and cancel.
func Control(ctx context.Context, dir, action, attempt string) (Response, error) {
	if !filepath.IsAbs(dir) || (action != "begin" && action != "adopt" && action != "status" && action != "cancel") || (attempt != "" && !ValidUID(attempt)) {
		return Response{}, ErrHelper
	}
	const timeout = 15 * time.Second
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	h := &http.Client{Timeout: timeout, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", filepath.Join(dir, ControlSocket))
	}, DisableKeepAlives: true}}
	raw, _ := json.Marshal(Request{AttemptUID: attempt})
	req, err := http.NewRequestWithContext(c, http.MethodPost, "http://keeper/"+action, bytes.NewReader(raw))
	if err != nil {
		return Response{}, ErrHelper
	}
	resp, err := h.Do(req)
	if err != nil {
		return Response{}, ErrHelper
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	var result Response
	if err != nil || len(data) > 1024 || codexauth.DecodeStrict(data, &result) != nil || (result.AttemptUID != "" && !ValidUID(result.AttemptUID)) {
		return Response{}, ErrHelper
	}
	switch result.Code {
	case "Started", "Adopted", "AdoptedPendingPublication", "Cancelled", "Ready", "PendingPublication",
		"NeedsLogin", "Unavailable", "InvalidRequest", "Busy", "AccountMismatch":
	default:
		return Response{}, ErrHelper
	}
	if (result.Code == "Ready" || result.Code == "PendingPublication" || result.Code == "Adopted" || result.Code == "AdoptedPendingPublication") && result.Generation == 0 {
		return Response{}, ErrHelper
	}
	return result, nil
}
