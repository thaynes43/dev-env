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

	"github.com/thaynes43/dev-env/internal/claudeauth"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// Primary source for the endpoint, client id and body shape: the Claude Code
// 2.1.299 binary's OAuth refresh path, inspected offline on 2026-10-19 (plan
// CLAUDE-110-T09-PLAN.md section 2, endpoint and client id unchanged from
// R-02). A refresh is one JSON POST. The response's refresh_token is OPTIONAL:
// the CLI keeps the old one when it is absent, so unlike the Codex transport an
// unchanged or absent refresh token is not an error. Nothing here is proven
// against the live provider; that is increment 6's bounded checkpoint.
const (
	claudeTokenURL      = "https://platform.claude.com/v1/oauth/token"
	claudeProfileURL    = "https://api.anthropic.com/api/oauth/profile"
	claudeClientID      = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeRequestTime   = 3 * time.Second
	claudeSaveTimeout   = 2 * time.Second
	claudeSafetyMargin  = 2 * time.Second
	claudeRequestBudget = claudeRequestTime + claudeSaveTimeout + claudeSafetyMargin
	// claudeMaxLifetime bounds expires_in and refresh_token_expires_in so a
	// hostile or broken response cannot park a credential for years.
	claudeMaxLifetime = 400 * 24 * time.Hour
)

var (
	errClaudeRefresh = errors.New("claude refresh requires a fresh login")
	errClaudeProfile = errors.New("claude profile is unavailable")
)

// claudeTokens is one validated refresh response. Refresh is empty when the
// provider did not rotate it; the caller then keeps the old one.
type claudeTokens struct {
	Access           secretValue
	Refresh          secretValue
	ExpiresIn        time.Duration
	RefreshExpiresIn time.Duration // zero when absent
	Scopes           []string      // nil when absent
	AccountUUID      string        // empty when absent
	OrganizationUUID string        // empty when absent
}

// claudeProfile is the non-secret subscription metadata for one access token.
type claudeProfile struct {
	AccountUUID      string
	OrganizationUUID string
	SubscriptionType string
	RateLimitTier    string
}

type claudeRefreshTransport interface {
	refresh(ctx context.Context, refresh secretValue, scopes []string) (claudeTokens, error)
	profile(ctx context.Context, access secretValue) (claudeProfile, error)
}

// The worker (increment 4) depends on the interface, so tests can fake the provider.
var _ claudeRefreshTransport = (*claudeHTTPRefresh)(nil)

type claudeHTTPRefresh struct {
	Client      *http.Client
	endpoint    string
	profilePath string
}

func newClaudeHTTPRefresh() *claudeHTTPRefresh {
	return &claudeHTTPRefresh{endpoint: claudeTokenURL, profilePath: claudeProfileURL, Client: &http.Client{
		Timeout:       claudeRequestTime,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, DisableKeepAlives: true},
	}}
}

type claudeAccountWire struct {
	UUID string `json:"uuid"`
}

type claudeOrgWire struct {
	UUID string `json:"uuid"`
}

type claudeTokenWire struct {
	Access           string             `json:"access_token"`
	Refresh          string             `json:"refresh_token,omitempty"`
	ExpiresIn        int64              `json:"expires_in"`
	RefreshExpiresIn int64              `json:"refresh_token_expires_in,omitempty"`
	Scope            string             `json:"scope,omitempty"`
	Account          *claudeAccountWire `json:"account,omitempty"`
	Organization     *claudeOrgWire     `json:"organization,omitempty"`
}

func (t *claudeHTTPRefresh) refresh(ctx context.Context, token secretValue, scopes []string) (claudeTokens, error) {
	body := map[string]string{"grant_type": "refresh_token", "refresh_token": token.Reveal(), "client_id": claudeClientID}
	if len(scopes) > 0 {
		body["scope"] = strings.Join(scopes, " ")
	}
	raw, _ := json.Marshal(body)
	c, cancel := context.WithTimeout(ctx, claudeRequestTime)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodPost, t.endpoint, strings.NewReader(string(raw)))
	if err != nil {
		return claudeTokens{}, errClaudeRefresh
	}
	req.Header.Set("Content-Type", "application/json")
	// One POST only. No redirect, retry or response-body logging, even on a
	// network error: the old refresh token may already be spent.
	resp, err := t.Client.Do(req)
	if err != nil {
		return claudeTokens{}, errClaudeRefresh
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return claudeTokens{}, errClaudeRefresh
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, claudeauth.MaxBytes+1))
	if err != nil || len(data) > claudeauth.MaxBytes {
		return claudeTokens{}, errClaudeRefresh
	}
	// Unknown members are ignored: by now the provider has accepted the POST and
	// spent the old refresh token, so refusing a 200 over an extra member would
	// lose the login. Duplicate members, the size bound and the required
	// access_token and expires_in are still enforced; present fields are validated.
	var w claudeTokenWire
	if codexauth.DecodeTolerant(data, &w) != nil {
		return claudeTokens{}, errClaudeRefresh
	}
	out, ok := validClaudeTokens(w)
	if !ok {
		return claudeTokens{}, errClaudeRefresh
	}
	return out, nil
}

func claudeToken(s string) bool {
	return s != "" && len(s) <= claudeauth.MaxTokenBytes && strings.IndexFunc(s, func(r rune) bool { return r < 33 || r > 126 }) == -1
}

func claudeField(s string) bool {
	return s != "" && len(s) <= 256 && strings.IndexFunc(s, func(r rune) bool { return r < 33 || r > 126 }) == -1
}

func claudeLifetime(seconds int64) (time.Duration, bool) {
	if seconds <= 0 || seconds > int64(claudeMaxLifetime/time.Second) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func validClaudeTokens(w claudeTokenWire) (claudeTokens, bool) {
	if !claudeToken(w.Access) || (w.Refresh != "" && !claudeToken(w.Refresh)) {
		return claudeTokens{}, false
	}
	life, ok := claudeLifetime(w.ExpiresIn)
	if !ok {
		return claudeTokens{}, false
	}
	out := claudeTokens{Access: newSecretValue(w.Access), Refresh: newSecretValue(w.Refresh), ExpiresIn: life}
	if w.RefreshExpiresIn != 0 {
		if out.RefreshExpiresIn, ok = claudeLifetime(w.RefreshExpiresIn); !ok {
			return claudeTokens{}, false
		}
	}
	if w.Scope != "" {
		out.Scopes = strings.Fields(w.Scope)
		if len(out.Scopes) == 0 || len(out.Scopes) > 32 {
			return claudeTokens{}, false
		}
		for _, s := range out.Scopes {
			if !claudeField(s) {
				return claudeTokens{}, false
			}
		}
	}
	if w.Account != nil {
		if !claudeField(w.Account.UUID) {
			return claudeTokens{}, false
		}
		out.AccountUUID = w.Account.UUID
	}
	if w.Organization != nil {
		if !claudeField(w.Organization.UUID) {
			return claudeTokens{}, false
		}
		out.OrganizationUUID = w.Organization.UUID
	}
	return out, true
}

// profile reads the account and subscription metadata for an access token. It
// is a plain GET, so a failure is retryable and carries no spent-token risk.
// The response has many fields this keeper does not use, so it is decoded
// tolerantly (unknown members ignored, duplicate members refused); only the five named members are read, and each is bounded.
func (t *claudeHTTPRefresh) profile(ctx context.Context, access secretValue) (claudeProfile, error) {
	c, cancel := context.WithTimeout(ctx, claudeRequestTime)
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, t.profilePath, nil)
	if err != nil {
		return claudeProfile{}, errClaudeProfile
	}
	req.Header.Set("Authorization", "Bearer "+access.Reveal())
	resp, err := t.Client.Do(req)
	if err != nil {
		return claudeProfile{}, errClaudeProfile
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return claudeProfile{}, errClaudeProfile
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, claudeauth.MaxBytes+1))
	if err != nil || len(data) > claudeauth.MaxBytes {
		return claudeProfile{}, errClaudeProfile
	}
	var w struct {
		Account struct {
			UUID string `json:"uuid"`
		} `json:"account"`
		Organization struct {
			UUID string `json:"uuid"`
			Type string `json:"organization_type"`
			Tier string `json:"rate_limit_tier"`
		} `json:"organization"`
	}
	if codexauth.DecodeTolerant(data, &w) != nil || !claudeField(w.Account.UUID) || !claudeField(w.Organization.UUID) {
		return claudeProfile{}, errClaudeProfile
	}
	if w.Organization.Tier != "" && !claudeField(w.Organization.Tier) {
		return claudeProfile{}, errClaudeProfile
	}
	return claudeProfile{AccountUUID: w.Account.UUID, OrganizationUUID: w.Organization.UUID, SubscriptionType: claudeSubscription(w.Organization.Type), RateLimitTier: w.Organization.Tier}, nil
}

// claudeSubscription maps the profile's organization_type to the CLI's
// subscriptionType words. An unknown type becomes empty (null in the
// credentials), never a guess.
func claudeSubscription(orgType string) string {
	switch orgType {
	case "claude_max":
		return "max"
	case "claude_pro":
		return "pro"
	case "claude_team":
		return "team"
	case "claude_enterprise":
		return "enterprise"
	}
	return ""
}
