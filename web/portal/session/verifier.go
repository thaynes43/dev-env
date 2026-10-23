// Package session prepares the source-only portal authentication boundary.
// It creates no listener, live provider, operator identity or authorization.
package session

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/thaynes43/dev-env/web/portal"
)

// VerifierConfig is trusted server configuration, never request metadata. A
// production key set needs a separately bounded issuer-only HTTP transport;
// this constructor neither discovers an issuer nor initiates a request.
type VerifierConfig struct {
	Issuer, ClientID string
	Algorithms       []string
	Keys             oidc.KeySet
	Now              func() time.Time
	ClockSkew        time.Duration
}

type TokenVerifier struct {
	verifier         *oidc.IDTokenVerifier
	issuer, clientID string
	now              func() time.Time
	skew             time.Duration
}

// NewRemoteTokenVerifier constructs a maintained remote key set without making
// a request. Future verification may fetch only the configured issuer's exact
// JWKS URL through the bounded client. Discovery/userinfo are not performed.
func NewRemoteTokenVerifier(c VerifierConfig, jwksURL string, client *http.Client) (*TokenVerifier, error) {
	issuer, err := configuredURL(c.Issuer)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	keys, err := configuredURL(jwksURL)
	if err != nil || keys.Host != issuer.Host || client == nil || client.Timeout <= 0 {
		return nil, portal.ErrUnavailable
	}
	transport, ok := client.Transport.(*issuerTransport)
	if !ok || transport.allowed[jwksURL] != "GET" {
		return nil, portal.ErrUnavailable
	}
	copyClient := *client
	copyClient.Jar = nil
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.Keys = oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), &copyClient), jwksURL)
	return NewTokenVerifier(c)
}

func NewTokenVerifier(c VerifierConfig) (*TokenVerifier, error) {
	u, err := configuredURL(c.Issuer)
	if err != nil || !identityText(c.Issuer) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !identityText(c.ClientID) || c.Keys == nil || c.Now == nil || c.ClockSkew < 0 || len(c.Algorithms) == 0 {
		return nil, portal.ErrUnavailable
	}
	for _, alg := range c.Algorithms {
		switch alg {
		case oidc.RS256, oidc.RS384, oidc.RS512, oidc.ES256, oidc.ES384, oidc.ES512, oidc.PS256, oidc.PS384, oidc.PS512, oidc.EdDSA:
		default:
			return nil, portal.ErrUnavailable
		}
	}
	// No skip options are exposed. The maintained verifier owns JWT parsing,
	// algorithm/signature/issuer/audience/expiry validation.
	v := oidc.NewVerifier(c.Issuer, c.Keys, &oidc.Config{ClientID: c.ClientID, SupportedSigningAlgs: append([]string(nil), c.Algorithms...), Now: c.Now})
	return &TokenVerifier{verifier: v, issuer: c.Issuer, clientID: c.ClientID, now: c.Now, skew: c.ClockSkew}, nil
}

// Authentik access tokens use the same issuer/client signing configuration.
// Verify both signatures and immutable identity; signed access expiry is the
// actual cap even if expires_in overstates it. at_hash, when present, must match.
func (v *TokenVerifier) verifyExchange(ctx context.Context, pair codeTokens, nonce string) (portal.Principal, time.Time, error) {
	p, err := v.Verify(ctx, pair.idToken, nonce)
	if err != nil {
		return portal.Principal{}, time.Time{}, err
	}
	access, err := v.Verify(ctx, pair.accessToken, nonce)
	if err != nil || access != p {
		return portal.Principal{}, time.Time{}, portal.ErrDenied
	}
	id, err := v.verifier.Verify(ctx, pair.idToken)
	if err != nil {
		return portal.Principal{}, time.Time{}, portal.ErrDenied
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(pair.accessToken) != nil {
		return portal.Principal{}, time.Time{}, portal.ErrDenied
	}
	token, err := v.verifier.Verify(ctx, pair.accessToken)
	if err != nil || pair.expiresAt.IsZero() {
		return portal.Principal{}, time.Time{}, portal.ErrDenied
	}
	expiry := pair.expiresAt
	if token.Expiry.Before(expiry) {
		expiry = token.Expiry
	}
	if id.Expiry.Before(expiry) {
		expiry = id.Expiry
	}
	if !expiry.After(v.now()) {
		return portal.Principal{}, time.Time{}, portal.ErrDenied
	}
	return p, expiry, nil
}

// Verify returns only the immutable principal, never raw claims/provider tokens.
// expectedNonce must come from a consumed server-side login transaction. Nonce
// verification alone does not implement state/PKCE or a portal session.
func (v *TokenVerifier) Verify(ctx context.Context, raw, expectedNonce string) (portal.Principal, error) {
	if v == nil || v.verifier == nil {
		return portal.Principal{}, portal.ErrUnavailable
	}
	if ctx.Err() != nil || len(raw) == 0 || len(raw) > 64*1024 || len(expectedNonce) < 32 || len(expectedNonce) > 512 {
		return portal.Principal{}, portal.ErrDenied
	}
	token, err := v.verifier.Verify(ctx, raw)
	if err != nil {
		return portal.Principal{}, portal.ErrDenied
	}
	now := v.now()
	if token.Issuer != v.issuer || !identityText(token.Subject) || subtle.ConstantTimeCompare([]byte(token.Nonce), []byte(expectedNonce)) != 1 || !token.Expiry.After(now) || token.IssuedAt.IsZero() || token.IssuedAt.After(now.Add(v.skew)) {
		return portal.Principal{}, portal.ErrDenied
	}
	// The upstream audience check does not validate the authorized-party claim,
	// and its nbf check allows a fixed five-minute leeway. Bind both explicitly
	// to the client's reviewed configuration, without weakening upstream checks.
	var binding struct {
		AuthorizedParty string `json:"azp"`
		NotBefore       *int64 `json:"nbf"`
	}
	if err = token.Claims(&binding); err != nil {
		return portal.Principal{}, portal.ErrDenied
	}
	if (len(token.Audience) > 1 && binding.AuthorizedParty != v.clientID) || (binding.AuthorizedParty != "" && binding.AuthorizedParty != v.clientID) || (binding.NotBefore != nil && time.Unix(*binding.NotBefore, 0).After(now.Add(v.skew))) {
		return portal.Principal{}, portal.ErrDenied
	}
	if ctx.Err() != nil {
		return portal.Principal{}, portal.ErrDenied
	}
	return portal.Principal{Issuer: token.Issuer, Subject: token.Subject}, nil
}

func identityText(s string) bool {
	if s == "" || len(s) > 512 || !utf8.ValidString(s) || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return false
		}
	}
	return true
}
