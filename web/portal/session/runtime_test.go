package session

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/jackc/pgx/v5"

	"github.com/thaynes43/dev-env/web/portal"
)

type fixtureFence struct {
	mu            sync.Mutex
	generation    string
	calls, failAt int
	unavailable   bool
}

func (f *fixtureFence) check(ctx context.Context, generation string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ctx.Err() != nil || f.unavailable || generation != f.generation || (f.failAt > 0 && f.calls >= f.failAt) {
		return portal.ErrUnavailable
	}
	return nil
}

func (f *fixtureFence) Check(ctx context.Context, tx pgx.Tx, generation string) (AdmissionSnapshot, error) {
	if err := f.check(ctx, generation); err != nil {
		return AdmissionSnapshot{}, err
	}
	// Hosted DB fixtures measure the actual acquired connection too.
	if tx != nil {
		if _, err := measureIncarnation(ctx, tx); err != nil {
			return AdmissionSnapshot{}, err
		}
	}
	return AdmissionSnapshot{Generation: generation}, nil
}
func (f *fixtureFence) Recheck(ctx context.Context, snapshot AdmissionSnapshot) error {
	return f.check(ctx, snapshot.Generation)
}

func fixtureStore(t *testing.T) *PostgresStore {
	t.Helper()
	s, err := newPostgresStore(PostgresConfig{Key: base64.StdEncoding.EncodeToString(make([]byte, 32)), Generation: strings.Repeat("g", 32), Fence: &fixtureFence{generation: strings.Repeat("g", 32)}, Now: time.Now, Timeout: time.Second})
	if err != nil {
		t.Fatal("fixture store configuration failed")
	}
	return s
}

func TestAEADRowTypeVersionGenerationStateExpiryBinding(t *testing.T) {
	s := fixtureStore(t)
	h := sha256.Sum256([]byte("synthetic-handle"))
	state := sha256.Sum256([]byte("synthetic-state"))
	row := s.row("pending_login", h, time.Now().Add(time.Minute), state)
	login := Login{State: strings.Repeat("s", 43), Nonce: strings.Repeat("n", 43), Verifier: strings.Repeat("v", 43), ExpiresAt: row.ExpiresAt}
	sealed, err := s.encrypt(row, login)
	if err != nil {
		t.Fatal("fixture encryption failed")
	}
	for _, mode := range []string{"valid", "tamper", "swap-handle", "swap-type", "version", "generation", "expiry", "state", "truncated", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			changed := row
			data := append([]byte(nil), sealed...)
			store := s
			switch mode {
			case "tamper":
				data[len(data)-1] ^= 1
			case "swap-handle":
				changed.Handle[0] ^= 1
			case "swap-type":
				changed.Table = "session"
			case "version":
				changed.Version++
			case "generation":
				changed.Generation = strings.Repeat("h", 32)
			case "expiry":
				changed.ExpiresAt = changed.ExpiresAt.Add(time.Second)
			case "state":
				changed.State[0] ^= 1
			case "truncated":
				data = data[:10]
			case "wrong-key":
				block := make([]byte, 32)
				block[0] = 1
				var err error
				store, err = newPostgresStore(PostgresConfig{Key: base64.StdEncoding.EncodeToString(block), Generation: s.generation, Fence: s.fence, Now: time.Now, Timeout: time.Second})
				if err != nil {
					t.Fatal("fixture key failed")
				}
			}
			var out Login
			err := store.decrypt(changed, data, &out)
			if mode == "valid" {
				if err != nil || out.State != login.State {
					t.Fatal("valid payload rejected")
				}
			} else if !errors.Is(err, portal.ErrUnavailable) {
				t.Fatal("modified ciphertext context accepted")
			}
		})
	}
	if strings.Contains(string(sealed), login.State) || strings.Contains(string(sealed), login.Verifier) {
		t.Fatal("sensitive payload remained plaintext")
	}
}

func TestMissingRecoveryWitnessAndRuntimeDefaultDeny(t *testing.T) {
	if _, err := newPostgresStore(PostgresConfig{Key: base64.StdEncoding.EncodeToString(make([]byte, 32)), Generation: strings.Repeat("g", 32), Now: time.Now, Timeout: time.Second}); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("missing fence accepted")
	}
	for _, path := range []string{LandingPath, "/auth/login", "/auth/callback", "/auth/logout"} {
		w := httptest.NewRecorder()
		NewRuntimeAssembly().ServeHTTP(w, httptest.NewRequest("GET", "https://portal.example"+path, nil))
		if w.Code != 503 {
			t.Fatal("unproven recovery runtime became available")
		}
	}
	s := fixtureStore(t)
	if _, err := s.GetSession(context.Background(), Handle{}); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("missing database fell back")
	}
}

func TestURLConstructorsRejectEmptyHostAndAmbiguousConfiguration(t *testing.T) {
	for _, raw := range []string{"https://:443", "https://:", "https://", "https://issuer.example:", "https://issuer.example:0", "https://issuer.example:65536", "https://issuer..example", "https://-issuer.example", "https://issuer_example", "https://issuer.example?", "https://issuer.example/#fragment"} {
		if _, err := configuredURL(raw); err == nil {
			t.Fatal("malformed trusted URL accepted")
		}
		if _, err := NewTokenVerifier(VerifierConfig{Issuer: raw, ClientID: "synthetic-client", Algorithms: []string{oidc.ES256}, Keys: &oidc.StaticKeySet{}, Now: time.Now}); err == nil {
			t.Fatal("verifier bypassed URL validation")
		}
	}
	f := setupAuth(t)
	c := f.e.config
	c.Origin = "https://:443"
	c.Callback = c.Origin + "/auth/callback"
	if _, err := New(c); err == nil {
		t.Fatal("engine accepted empty hostname")
	}
}

func TestFixedLandingAndForgedReturnURLOrHost(t *testing.T) {
	f := setupAuth(t)
	f.begin(t)
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, f.callback)
	if w.Code != 303 || w.Header().Get("Location") != SessionsPath {
		t.Fatal("callback did not use fixed authorized landing")
	}
	for _, path := range []string{"/auth/login?return=https://evil.example/", "/auth/login?next=//evil.example/", LandingPath + "?return=https://evil.example/"} {
		w := httptest.NewRecorder()
		NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error { return portal.ErrDenied }), Reader: portal.UnavailableReader{}, FreshFor: time.Minute, Now: func() time.Time { return f.now }}).ServeHTTP(w, f.request("GET", path, ""))
		if w.Code == 200 || w.Code == 303 || w.Header().Get("Location") != "" {
			t.Fatal("forged return URL accepted")
		}
	}
	f.login(t)
	r := f.request("GET", LandingPath, "")
	r.Host = "evil.example"
	r.Header.Set("X-Forwarded-Host", "portal.example")
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := f.e.Authenticate(r); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("forwarded headers established trust")
	}
	r = f.request("GET", LandingPath, "")
	r.TLS = nil
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := f.e.Authenticate(r); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("plaintext backend accepted forwarded TLS claim")
	}
}

func TestSessionAndCookieCappedBySignedAccessExpiry(t *testing.T) {
	f := setupAuth(t)
	f.now = f.now.Add(125 * time.Millisecond)
	f.e.config.IdleTTL = 2 * time.Hour
	f.e.config.AbsoluteTTL = 3 * time.Hour
	f.login(t)
	key, _ := requestHandle(f.request("GET", LandingPath, ""), sessionCookie)
	r := f.store.sessions[key]
	if r.ExpiresAt.After(r.tokenExpiresAt) || r.IdleExpiresAt.After(r.tokenExpiresAt) || f.session.MaxAge > int(r.ExpiresAt.Sub(r.CreatedAt)/time.Second) || f.session.Expires.After(r.tokenExpiresAt) {
		t.Fatal("session or cookie outlived access token")
	}
	before := r.ExpiresAt
	f.now = f.now.Add(time.Minute)
	if _, err := f.e.Authenticate(f.request("GET", LandingPath, "")); err != nil {
		t.Fatal("unexpired session denied")
	}
	if !f.store.sessions[key].ExpiresAt.Equal(before) {
		t.Fatal("polling extended token/session lifetime")
	}
	f.now = r.tokenExpiresAt
	if _, err := f.e.Authenticate(f.request("GET", LandingPath, "")); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("expired token authenticated")
	}
}

func TestAccessTokenPairMustMatchVerifiedIdentityAndHash(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	nonce := strings.Repeat("n", 43)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal("fixture signing key failed")
	}
	v, err := NewTokenVerifier(VerifierConfig{Issuer: "https://issuer.example/portal/", ClientID: "synthetic-client", Algorithms: []string{oidc.ES256}, Keys: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal("fixture verifier failed")
	}
	for _, mode := range []string{"valid", "wrong-subject", "wrong-audience", "wrong-issuer", "expired", "wrong-hash", "wrong-nonce", "opaque-access", "no-expiry", "overstated-expires-in"} {
		t.Run(mode, func(t *testing.T) {
			id := map[string]any{"iss": v.issuer, "aud": v.clientID, "sub": "synthetic-subject", "nonce": nonce, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
			access := map[string]any{}
			for k, val := range id {
				access[k] = val
			}
			expiry := now.Add(time.Hour)
			switch mode {
			case "wrong-subject":
				access["sub"] = "other"
			case "wrong-audience":
				access["aud"] = "other"
			case "wrong-issuer":
				access["iss"] = "https://other.example/"
			case "expired":
				access["exp"] = now.Unix()
			case "wrong-hash":
				id["at_hash"] = "invalid"
			case "wrong-nonce":
				access["nonce"] = "other"
			case "no-expiry":
				expiry = time.Time{}
			case "overstated-expires-in":
				access["exp"] = now.Add(time.Minute).Unix()
			}
			raw := signedFixture(t, key, jose.ES256, access)
			if mode == "opaque-access" {
				raw = "opaque-fixture"
			}
			_, actual, err := v.verifyExchange(context.Background(), codeTokens{idToken: signedFixture(t, key, jose.ES256, id), accessToken: raw, expiresAt: expiry}, nonce)
			if mode == "valid" || mode == "overstated-expires-in" {
				if err != nil {
					t.Fatal("verified token pair refused")
				}
				if mode == "overstated-expires-in" && !actual.Equal(now.Add(time.Minute)) {
					t.Fatal("wire expiry exceeded signed deadline")
				}
			} else if err == nil {
				t.Fatal("unbound token pair accepted")
			}
		})
	}
}

func TestServerValuesDoNotFormatAsCredentials(t *testing.T) {
	for _, value := range []any{Record{CSRF: "SECRET-CANARY", accessToken: "SECRET-CANARY"}, Login{State: "SECRET-CANARY"}, codeTokens{accessToken: "SECRET-CANARY"}, PostgresConfig{Password: "SECRET-CANARY", Key: "SECRET-CANARY"}, CodeConfig{ClientSecret: "SECRET-CANARY"}, sessionPayload{AccessToken: "SECRET-CANARY"}, sessionReference{principal: portal.Principal{Subject: "SECRET-CANARY"}}, CurrentAuthorizationConfig{UserInfoURL: "SECRET-CANARY"}, CurrentAuthorizer{endpoint: "SECRET-CANARY"}, &Record{CSRF: "SECRET-CANARY"}, &CodeConfig{ClientSecret: "SECRET-CANARY"}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "SECRET-CANARY") {
				t.Fatal("formatting leaked credential")
			}
		}
	}
}

func TestPostgresClientPinsVerifiedPrimaryAndRejectsAmbientAuthority(t *testing.T) {
	t.Setenv("PGHOST", "unapproved.example")
	t.Setenv("PGPORT", "6432")
	t.Setenv("PGUSER", "postgres")
	t.Setenv("PGSSLMODE", "require")
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	roots := x509.NewCertPool()
	c, err := postgresPoolConfig("postgres16-rw.database.svc.cluster.local", "dev_env_v2_portal_runtime", "synthetic-fixture", roots)
	if err != nil || c.ConnConfig.Host != "postgres16-rw.database.svc.cluster.local" || c.ConnConfig.Port != 5432 || c.ConnConfig.User != "dev_env_v2_portal_runtime" || c.ConnConfig.Database != "dev_env_v2_portal" || c.ConnConfig.TLSConfig.InsecureSkipVerify || c.ConnConfig.TLSConfig.ServerName != c.ConnConfig.Host || c.ConnConfig.TLSConfig.RootCAs != roots || len(c.ConnConfig.Fallbacks) != 0 || c.ConnConfig.ValidateConnect == nil || c.MaxConns != 2 {
		t.Fatal("ambient config changed verified primary boundary")
	}
	t.Setenv("PGSERVICEFILE", "unapproved-credential-path")
	if _, err := postgresPoolConfig("postgres16-rw.database.svc.cluster.local", "dev_env_v2_portal_runtime", "synthetic-fixture", roots); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("ambient credential authority accepted")
	}
}

func TestExplicitHS256DowngradeRejected(t *testing.T) {
	f := setupAuth(t)
	secret := []byte("synthetic-HMAC-key-at-least-32-bytes")
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: secret}, nil)
	if err != nil {
		t.Fatal("fixture HMAC signer failed")
	}
	claims := fmt.Sprintf(`{"iss":%q,"aud":"synthetic-client","sub":"synthetic-subject","nonce":"synthetic-nonce","exp":%d}`, f.e.config.Verifier.issuer, f.now.Add(time.Hour).Unix())
	object, err := signer.Sign([]byte(claims))
	if err != nil {
		t.Fatal("fixture signing failed")
	}
	raw, err := object.CompactSerialize()
	if err != nil {
		t.Fatal("fixture serialization failed")
	}
	if _, err := f.e.config.Verifier.Verify(context.Background(), raw, "synthetic-nonce"); err == nil {
		t.Fatal("HS256 downgrade accepted by asymmetric verifier")
	}
	if _, err := NewTokenVerifier(VerifierConfig{Issuer: f.e.config.Verifier.issuer, ClientID: "synthetic-client", Algorithms: []string{"HS256"}, Keys: &oidc.StaticKeySet{}, Now: time.Now}); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("symmetric signing configured")
	}
}

func TestLoginAdmissionBeforeAllocationOrStoreWrite(t *testing.T) {
	f := setupAuth(t)
	for range 4 {
		w := httptest.NewRecorder()
		f.e.ServeHTTP(w, f.request("GET", "/auth/login", ""))
		if w.Code != 303 {
			t.Fatal("legitimate bounded login start refused")
		}
	}
	before := len(f.store.logins)
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, f.request("GET", "/auth/login", ""))
	if w.Code != 503 || len(f.store.logins) != before || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" {
		t.Fatal("login admission exceeded bound before storage")
	}
}
