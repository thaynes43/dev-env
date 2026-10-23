package session

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/thaynes43/dev-env/web/portal"
)

type CodeConfig struct {
	Issuer, ClientID, ClientSecret, Callback string
	AuthorizationURL, TokenURL               string
	AuthStyle                                oauth2.AuthStyle
	HTTPClient                               *http.Client
}

type OAuthCodeClient struct {
	config oauth2.Config
	client *http.Client
	issuer string
}

// NewCodeClient selects a confidential code-only flow, with no offline scope,
// refresh operation or dynamic endpoint discovery. Secret material must be
// supplied later by reviewed server-side configuration, never a browser/header.
func NewCodeClient(c CodeConfig) (*OAuthCodeClient, error) {
	issuer, err := configuredURL(c.Issuer)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	auth, err := configuredURL(c.AuthorizationURL)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	token, err := configuredURL(c.TokenURL)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	callback, err := configuredURL(c.Callback)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	if auth.Host != issuer.Host || token.Host != issuer.Host || callback.Path != "/auth/callback" || !identityText(c.ClientID) || c.ClientSecret == "" || c.HTTPClient == nil || (c.AuthStyle != oauth2.AuthStyleInHeader && c.AuthStyle != oauth2.AuthStyleInParams) {
		return nil, portal.ErrUnavailable
	}
	transport, ok := c.HTTPClient.Transport.(*issuerTransport)
	if !ok || transport.allowed[c.TokenURL] != "POST" || c.HTTPClient.Timeout <= 0 {
		return nil, portal.ErrUnavailable
	}
	client := *c.HTTPClient
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// Automatic auth-style detection may retry a code exchange; require the
	// actual reviewed provider method explicitly instead.
	return &OAuthCodeClient{config: oauth2.Config{ClientID: c.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.Callback, Scopes: []string{oidc.ScopeOpenID, oidc.ScopeProfile}, Endpoint: oauth2.Endpoint{AuthURL: c.AuthorizationURL, TokenURL: c.TokenURL, AuthStyle: c.AuthStyle}}, client: &client, issuer: c.Issuer}, nil
}

func (c *OAuthCodeClient) Binding() CodeBinding {
	if c == nil {
		return CodeBinding{}
	}
	return CodeBinding{Issuer: c.issuer, ClientID: c.config.ClientID, Callback: c.config.RedirectURL, AuthorizationEndpoint: c.config.Endpoint.AuthURL}
}

func (c *OAuthCodeClient) AuthorizationURL(state, nonce, verifier string) (string, error) {
	if c == nil || len(state) != 43 || len(nonce) != 43 || len(verifier) != 43 {
		return "", portal.ErrUnavailable
	}
	return c.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), nil
}

func (c *OAuthCodeClient) Exchange(ctx context.Context, code, verifier string) (string, error) {
	if c == nil || c.client == nil {
		return "", portal.ErrUnavailable
	}
	if code == "" || len(code) > 4096 || len(verifier) != 43 {
		return "", portal.ErrDenied
	}
	token, err := c.config.Exchange(oidc.ClientContext(ctx, c.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", portal.ErrUnavailable
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" || len(raw) > 64*1024 || token.RefreshToken != "" {
		return "", portal.ErrDenied
	}
	return raw, nil
}

func configuredURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || !identityText(raw) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" || net.ParseIP(u.Hostname()) != nil || u.Hostname() == "localhost" || strings.HasSuffix(u.Hostname(), ".localhost") {
		return nil, portal.ErrUnavailable
	}
	return u, nil
}

// NewIssuerHTTPClient bounds future token/JWKS/discovery requests to exact
// configured HTTPS URLs at the issuer origin, with no redirects. Base transport
// and explicit budgets are required; nil never means the default network client.
// Tests inject an inert RoundTripper. Production TLS/egress remains Gate A.
func NewIssuerHTTPClient(issuer string, allowed map[string]string, base http.RoundTripper, timeout time.Duration, maxBytes int64) (*http.Client, error) {
	u, err := configuredURL(issuer)
	if err != nil || base == nil || timeout <= 0 || maxBytes <= 0 || maxBytes > 1024*1024 || len(allowed) == 0 || len(allowed) > 3 {
		return nil, portal.ErrUnavailable
	}
	copyAllowed := make(map[string]string, len(allowed))
	for raw, method := range allowed {
		target, err := configuredURL(raw)
		if err != nil || target.Host != u.Host || (method != "GET" && method != "POST") {
			return nil, portal.ErrUnavailable
		}
		copyAllowed[raw] = method
	}
	return &http.Client{Transport: &issuerTransport{allowed: copyAllowed, base: base, maxBytes: maxBytes}, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type issuerTransport struct {
	allowed  map[string]string
	base     http.RoundTripper
	maxBytes int64
}

func (t *issuerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL == nil {
		return nil, portal.ErrUnavailable
	}
	method, allowed := t.allowed[r.URL.String()]
	if !allowed || method != r.Method || (r.Method == "GET" && r.Header.Get("Authorization") != "") {
		return nil, portal.ErrUnavailable
	}
	response, err := t.base.RoundTrip(r)
	if response != nil && response.Body != nil {
		body := response.Body
		defer func() { _ = body.Close() }()
	}
	if err != nil || response == nil || response.Body == nil {
		return nil, portal.ErrUnavailable
	}
	body := response.Body
	data, err := io.ReadAll(io.LimitReader(body, t.maxBytes+1))
	if err != nil || int64(len(data)) > t.maxBytes || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, portal.ErrUnavailable
	}
	response.Body = io.NopCloser(bytes.NewReader(data))
	return response, nil
}
