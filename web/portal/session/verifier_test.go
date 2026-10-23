package session

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"

	"github.com/thaynes43/dev-env/web/portal"
)

func signedFixture(t *testing.T, key *ecdsa.PrivateKey, alg jose.SignatureAlgorithm, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := signer.Sign(raw)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestMaintainedVerifierAndCallerBindings(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	nonce := strings.Repeat("n", 32)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongAlgKey, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	config := VerifierConfig{Issuer: "https://issuer.example/portal/", ClientID: "synthetic-client", Algorithms: []string{oidc.ES256}, Keys: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, Now: func() time.Time { return now }}
	v, err := NewTokenVerifier(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []string{"valid", "wrong-issuer", "wrong-audience", "wrong-signature", "wrong-algorithm", "missing-subject", "blank-subject", "missing-nonce", "wrong-nonce", "expired", "exact-expiry", "future-issued", "missing-issued", "future-not-before", "multiple-aud-no-azp", "wrong-azp", "multiple-aud-valid-azp", "cancelled"} {
		t.Run(test, func(t *testing.T) {
			claims := map[string]any{"iss": config.Issuer, "aud": config.ClientID, "sub": "synthetic-subject", "nonce": nonce, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Hour).Unix()}
			signingKey := key
			alg := jose.ES256
			switch test {
			case "wrong-issuer":
				claims["iss"] = "https://other.example/"
			case "wrong-audience":
				claims["aud"] = "other-client"
			case "wrong-signature":
				signingKey = wrongKey
			case "wrong-algorithm":
				signingKey = wrongAlgKey
				alg = jose.ES384
			case "missing-subject":
				delete(claims, "sub")
			case "blank-subject":
				claims["sub"] = " "
			case "missing-nonce":
				delete(claims, "nonce")
			case "wrong-nonce":
				claims["nonce"] = strings.Repeat("x", 32)
			case "expired":
				claims["exp"] = now.Add(-time.Second).Unix()
			case "exact-expiry":
				claims["exp"] = now.Unix()
			case "future-issued":
				claims["iat"] = now.Add(time.Second).Unix()
			case "missing-issued":
				delete(claims, "iat")
			case "future-not-before":
				claims["nbf"] = now.Add(time.Second).Unix()
			case "multiple-aud-no-azp":
				claims["aud"] = []string{config.ClientID, "other-client"}
			case "wrong-azp":
				claims["azp"] = "other-client"
			case "multiple-aud-valid-azp":
				claims["aud"] = []string{config.ClientID, "other-client"}
				claims["azp"] = config.ClientID
			}
			raw := signedFixture(t, signingKey, alg, claims)
			ctx := context.Background()
			if test == "cancelled" {
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelCtx
			}
			principal, err := v.Verify(ctx, raw, nonce)
			if test == "valid" || test == "multiple-aud-valid-azp" {
				if err != nil || principal != (portal.Principal{Issuer: config.Issuer, Subject: "synthetic-subject"}) {
					t.Fatalf("valid identity refused: %v", err)
				}
			} else {
				if !errors.Is(err, portal.ErrDenied) || principal != (portal.Principal{}) {
					t.Fatalf("invalid token produced identity: %s", test)
				}
			}
		})
	}
}

func TestVerifierFailsUnavailableWithoutTrustedDependencies(t *testing.T) {
	for _, config := range []VerifierConfig{{}, {Issuer: "http://issuer.example/"}, {Issuer: "https://user:password@issuer.example/"}, {Issuer: "https://issuer.example/?url=elsewhere"}} {
		v, err := NewTokenVerifier(config)
		if !errors.Is(err, portal.ErrUnavailable) || v != nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	var absent *TokenVerifier
	principal, err := absent.Verify(context.Background(), "SECRET-TOKEN-CANARY", strings.Repeat("n", 32))
	if !errors.Is(err, portal.ErrUnavailable) || principal != (portal.Principal{}) || strings.Contains(err.Error(), "CANARY") {
		t.Fatal("missing verifier leaked or authenticated")
	}
}

func TestMaintainedRemoteKeySetUsesOnlyBoundedIssuerClient(t *testing.T) {
	now := time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC)
	nonce := strings.Repeat("n", 32)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Algorithm: oidc.ES256, Use: "sig"}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client, err := NewIssuerHTTPClient("https://issuer.example/portal/", map[string]string{"https://issuer.example/keys": "GET"}, tripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(jwks)))}, nil
	}), time.Second, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	c := VerifierConfig{Issuer: "https://issuer.example/portal/", ClientID: "synthetic-client", Algorithms: []string{oidc.ES256}, Now: func() time.Time { return now }}
	v, err := NewRemoteTokenVerifier(c, "https://issuer.example/keys", client)
	if err != nil || calls != 0 {
		t.Fatal("remote verifier constructor fetched keys")
	}
	raw := signedFixture(t, key, jose.ES256, map[string]any{"iss": c.Issuer, "aud": c.ClientID, "sub": "synthetic-subject", "nonce": nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()})
	principal, err := v.Verify(context.Background(), raw, nonce)
	if err != nil || principal.Subject != "synthetic-subject" || calls != 1 {
		t.Fatalf("bounded key verification failed: %v", err)
	}
	for _, endpoint := range []string{"https://evil.example/keys", "http://169.254.169.254/keys", "https://issuer.example/other"} {
		if v, err := NewRemoteTokenVerifier(c, endpoint, client); !errors.Is(err, portal.ErrUnavailable) || v != nil {
			t.Fatal("unconfigured JWKS destination accepted")
		}
	}
}
