package keeper

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

// Pinned primary source: openai/codex d27764b82f7118f674371e6d6e76271d9d606edb,
// codex-rs/login/src/{auth/manager.rs,oauth/client.rs}. Native refresh uses JSON,
// unlike the form-encoded authorization-code exchange handled by the login CLI.
const (
	codexTokenURL       = "https://auth.openai.com/oauth/token"
	codexClientID       = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexRequestTimeout = 3 * time.Second
	codexSafetyMargin   = 2 * time.Second
	codexRequestBudget  = codexRequestTimeout + codexSaveTimeout + codexSafetyMargin
)

var errCodexRefresh = errors.New("Codex refresh requires a fresh login")

type codexRefreshTransport interface {
	refresh(context.Context, secretValue) (secretValue, secretValue, secretValue, error)
}

type codexHTTPRefresh struct {
	Client   *http.Client
	endpoint string
}

func newCodexHTTPRefresh() *codexHTTPRefresh {
	return &codexHTTPRefresh{endpoint: codexTokenURL, Client: &http.Client{
		Timeout:       codexRequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, DisableKeepAlives: true},
	}}
}

func (t *codexHTTPRefresh) refresh(ctx context.Context, token secretValue) (secretValue, secretValue, secretValue, error) {
	var body struct {
		Grant   string `json:"grant_type"`
		Client  string `json:"client_id"`
		Refresh string `json:"refresh_token"`
	}
	body.Grant, body.Client, body.Refresh = "refresh_token", codexClientID, token.Reveal()
	raw, _ := json.Marshal(body)
	c, cancel := context.WithTimeout(ctx, codexRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodPost, t.endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return secretValue{}, secretValue{}, secretValue{}, errCodexRefresh
	}
	req.Header.Set("Content-Type", "application/json")
	// One POST only. No redirect, idempotency header, response-body logging or
	// retry, even on network errors: the old refresh token may already be spent.
	resp, err := t.Client.Do(req)
	if err != nil {
		return secretValue{}, secretValue{}, secretValue{}, errCodexRefresh
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return secretValue{}, secretValue{}, secretValue{}, errCodexRefresh
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, codexauth.MaxBytes+1))
	if err != nil || len(data) > codexauth.MaxBytes {
		return secretValue{}, secretValue{}, secretValue{}, errCodexRefresh
	}
	var tokens struct {
		ID      string          `json:"id_token"`
		Access  string          `json:"access_token"`
		Refresh string          `json:"refresh_token"`
		Type    string          `json:"token_type,omitempty"`
		Expires json.RawMessage `json:"expires_in,omitempty"`
		Scope   string          `json:"scope,omitempty"`
	}
	if codexauth.DecodeStrict(data, &tokens) != nil || tokens.ID == "" || tokens.Access == "" || tokens.Refresh == "" || len(tokens.Refresh) > codexauth.MaxTokenBytes || tokens.Refresh == token.Reveal() {
		return secretValue{}, secretValue{}, secretValue{}, errCodexRefresh
	}
	return newSecretValue(tokens.ID), newSecretValue(tokens.Access), newSecretValue(tokens.Refresh), nil
}
