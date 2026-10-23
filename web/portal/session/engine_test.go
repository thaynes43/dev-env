package session

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"

	"github.com/thaynes43/dev-env/web/portal"
)

// Inert stores exist only in tests. This is not a production persistence or
// multi-replica revocation claim.
type memoryStore struct {
	mu          sync.Mutex
	logins      map[Handle]Login
	sessions    map[Handle]Record
	fail        bool
	failRevoke  bool
	revocations int
	puts        int
}

func (s *memoryStore) PutLogin(_ context.Context, h Handle, v Login) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("SECRET-STORE-CANARY")
	}
	s.logins[h] = v
	return nil
}
func (s *memoryStore) TakeLogin(_ context.Context, h Handle, state string, now time.Time) (Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return Login{}, portal.ErrUnavailable
	}
	v, ok := s.logins[h]
	if !ok || !now.Before(v.ExpiresAt) || subtle.ConstantTimeCompare([]byte(v.State), []byte(state)) != 1 {
		return Login{}, portal.ErrDenied
	}
	delete(s.logins, h)
	return v, nil
}
func (s *memoryStore) PutSession(_ context.Context, h Handle, v Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("SECRET-STORE-CANARY")
	}
	s.sessions[h] = v
	s.puts++
	return nil
}
func (s *memoryStore) GetSession(_ context.Context, h Handle) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return Record{}, errors.New("SECRET-STORE-CANARY")
	}
	v, ok := s.sessions[h]
	if !ok {
		return Record{}, portal.ErrDenied
	}
	return v, nil
}
func (s *memoryStore) RevokeSession(_ context.Context, h Handle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revocations++
	if s.fail || s.failRevoke {
		return errors.New("SECRET-STORE-CANARY")
	}
	delete(s.sessions, h)
	return nil
}

type tripFunc func(*http.Request) (*http.Response, error)

func (f tripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type authFixture struct {
	e                *Engine
	store            *memoryStore
	now              time.Time
	nonce, challenge string
	subject          string
	exchanges        int
	exchangeFailure  bool
	exchangeAdvance  time.Duration
	callback         *http.Request
	session          *http.Cookie
}

func setupAuth(t *testing.T) *authFixture {
	t.Helper()
	f := &authFixture{now: time.Date(2026, 10, 23, 16, 0, 0, 0, time.UTC), store: &memoryStore{logins: map[Handle]Login{}, sessions: map[Handle]Record{}}}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewTokenVerifier(VerifierConfig{Issuer: "https://issuer.example/portal/", ClientID: "synthetic-client", Algorithms: []string{oidc.ES256}, Keys: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewIssuerHTTPClient(v.issuer, map[string]string{"https://issuer.example/token": "POST"}, tripFunc(func(r *http.Request) (*http.Response, error) {
		f.exchanges++
		if f.exchangeFailure {
			return nil, errors.New("SECRET-PROVIDER-CANARY")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		verifier := r.PostForm.Get("code_verifier")
		sum := sha256.Sum256([]byte(verifier))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge || r.PostForm.Get("code") != "synthetic-code" || r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("redirect_uri") != "https://portal.example/auth/callback" {
			t.Fatal("code/PKCE/callback binding missing")
		}
		id, syntheticCredential, ok := r.BasicAuth()
		if !ok || id != "synthetic-client" || syntheticCredential != "fixture-only-not-a-credential" {
			t.Fatal("confidential client authentication missing")
		}
		subject := f.subject
		if subject == "" {
			subject = "synthetic-subject"
		}
		raw := signedFixture(t, key, jose.ES256, map[string]any{"iss": v.issuer, "aud": "synthetic-client", "sub": subject, "nonce": f.nonce, "iat": f.now.Unix(), "exp": f.now.Add(time.Hour).Unix()})
		body, err := json.Marshal(map[string]string{"id_token": raw, "access_token": "synthetic-unused", "token_type": "Bearer"})
		if err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(f.exchangeAdvance)
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	}), time.Second, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := NewCodeClient(CodeConfig{Issuer: v.issuer, ClientID: "synthetic-client", ClientSecret: "fixture-only-not-a-credential", Callback: "https://portal.example/auth/callback", AuthorizationURL: "https://issuer.example/authorize", TokenURL: "https://issuer.example/token", AuthStyle: oauth2.AuthStyleInHeader, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	f.e, err = New(Config{Origin: "https://portal.example", Callback: "https://portal.example/auth/callback", LoginTTL: time.Minute, IdleTTL: 2 * time.Minute, AbsoluteTTL: time.Hour, Now: func() time.Time { return f.now }, Store: f.store, Codes: codes, Verifier: v})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *authFixture) begin(t *testing.T) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, httptest.NewRequest("GET", "https://portal.example/auth/login", nil))
	if w.Code != 303 {
		t.Fatalf("login start %d", w.Code)
	}
	destination, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	q := destination.Query()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	if q.Get("scope") != "openid profile" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || strings.Contains(destination.String(), "fixture-only-not-a-credential") {
		t.Fatal("unsafe authorization request")
	}
	pending := findCookie(t, w, loginCookie)
	f.callback = httptest.NewRequest("GET", "https://portal.example/auth/callback?"+url.Values{"code": []string{"synthetic-code"}, "state": q["state"]}.Encode(), nil)
	f.callback.AddCookie(pending)
	return pending
}

func findCookie(t *testing.T, w *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == name {
			if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie flags")
			}
			return cookie
		}
	}
	t.Fatalf("missing %s cookie", name)
	return nil
}

func (f *authFixture) login(t *testing.T) {
	t.Helper()
	f.begin(t)
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, f.callback)
	if w.Code != 303 {
		t.Fatalf("callback status %d: %s", w.Code, w.Body.String())
	}
	f.session = findCookie(t, w, sessionCookie)
}

func (f *authFixture) request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "https://portal.example"+path, strings.NewReader(body))
	if f.session != nil {
		r.AddCookie(f.session)
	}
	return r
}

func TestConfidentialLoginCookieAndReplay(t *testing.T) {
	f := setupAuth(t)
	pending := f.begin(t)
	forgedSession := strings.Repeat("x", 43)
	f.callback.AddCookie(&http.Cookie{Name: sessionCookie, Value: forgedSession})
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, f.callback)
	if w.Code != 303 || f.exchanges != 1 {
		t.Fatal("login failed")
	}
	f.session = findCookie(t, w, sessionCookie)
	if f.session.Value == pending.Value || f.session.Value == forgedSession {
		t.Fatal("login handle became session")
	}
	principal, err := f.e.Authenticate(f.request("GET", "/portal/projects/dev-env/environments/internal/attention", ""))
	if err != nil || principal.Subject != "synthetic-subject" {
		t.Fatal("stored session refused")
	}
	w = httptest.NewRecorder()
	f.e.ServeHTTP(w, f.callback)
	if w.Code != 403 || f.exchanges != 1 || strings.Contains(w.Body.String(), "synthetic-subject") {
		t.Fatal("callback replay accepted or leaked")
	}
}

func TestStateExpiryAndFailedExchangeDoNotReplay(t *testing.T) {
	for _, mode := range []string{"wrong-state", "expired-state", "failed-exchange", "expired-during-exchange"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.begin(t)
			if mode == "wrong-state" {
				q := f.callback.URL.Query()
				q.Set("state", strings.Repeat("x", 43))
				f.callback.URL.RawQuery = q.Encode()
			}
			if mode == "expired-state" {
				f.now = f.now.Add(time.Minute)
			}
			if mode == "failed-exchange" {
				f.exchangeFailure = true
			}
			if mode == "expired-during-exchange" {
				f.exchangeAdvance = time.Minute
			}
			w := httptest.NewRecorder()
			f.e.ServeHTTP(w, f.callback)
			if w.Code == 303 || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "CANARY") {
				t.Fatal("failed transaction succeeded or leaked")
			}
			if mode == "failed-exchange" {
				f.exchangeFailure = false
				w = httptest.NewRecorder()
				f.e.ServeHTTP(w, f.callback)
				if w.Code != 403 || f.exchanges != 1 {
					t.Fatal("exchange failure blindly replayed")
				}
			} else if mode == "expired-during-exchange" {
				if f.exchanges != 1 || f.store.puts != 0 {
					t.Fatal("expired transaction created session")
				}
			} else if f.exchanges != 0 {
				t.Fatal("invalid state reached provider")
			}
		})
	}
}

func TestCallbackRejectsMalformedParametersAndLoginCookies(t *testing.T) {
	for _, mode := range []string{"missing-code", "missing-state", "duplicate-code", "duplicate-state", "extra-iss", "extra-session-state", "oversized-code", "malformed-query", "missing-cookie", "duplicate-cookie", "empty-duplicate-cookie"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			pending := f.begin(t)
			r := f.callback
			q := r.URL.Query()
			switch mode {
			case "missing-code":
				q.Del("code")
			case "missing-state":
				q.Del("state")
			case "duplicate-code":
				q.Add("code", "synthetic-code")
			case "duplicate-state":
				q.Add("state", q.Get("state"))
			case "extra-iss":
				q.Set("iss", f.e.config.Verifier.issuer)
			case "extra-session-state":
				q.Set("session_state", "synthetic")
			case "oversized-code":
				q.Set("code", strings.Repeat("x", 4097))
			case "missing-cookie":
				r.Header.Del("Cookie")
			case "duplicate-cookie":
				r.AddCookie(pending)
			case "empty-duplicate-cookie":
				r.Header.Set("Cookie", loginCookie+"=; "+loginCookie+"="+pending.Value)
			}
			r.URL.RawQuery = q.Encode()
			if mode == "malformed-query" {
				r.URL.RawQuery = "code=%ZZ&state=" + q.Get("state")
			}
			w := httptest.NewRecorder()
			f.e.ServeHTTP(w, r)
			if w.Code != 403 || w.Body.String() != "Portal authentication unavailable or denied.\n" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Location") != "" || f.exchanges != 0 || f.store.puts != 0 {
				t.Fatal("invalid callback reached exchange or leaked success/data")
			}
		})
	}
}

func TestBackToBackTabsKeepStateBoundToLatestBrowserCookie(t *testing.T) {
	f := setupAuth(t)
	f.begin(t)
	firstQuery := f.callback.URL.RawQuery
	latestCookie := f.begin(t)
	latestCallback := f.callback
	// The browser now carries the second login cookie. A callback from the
	// earlier tab must not consume the second tab's transaction.
	wrongTab := httptest.NewRequest("GET", "https://portal.example/auth/callback?"+firstQuery, nil)
	wrongTab.AddCookie(latestCookie)
	w := httptest.NewRecorder()
	f.e.ServeHTTP(w, wrongTab)
	if w.Code != 403 || f.exchanges != 0 {
		t.Fatal("cross-tab state mismatch accepted")
	}
	w = httptest.NewRecorder()
	f.e.ServeHTTP(w, latestCallback)
	if w.Code != 303 || f.exchanges != 1 || f.store.puts != 1 {
		t.Fatal("wrong-state attempt consumed the current transaction")
	}
	_ = findCookie(t, w, sessionCookie)
}

type alteredCodes struct {
	CodeClient
	mode string
}

func (c alteredCodes) AuthorizationURL(state, nonce, verifier string) (string, error) {
	raw, err := c.CodeClient.AuthorizationURL(state, nonce, verifier)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	q := u.Query()
	switch c.mode {
	case "nonce":
		q.Set("nonce", "other")
	case "callback":
		q.Set("redirect_uri", "https://evil.example/auth/callback")
	case "scope":
		q.Set("scope", "openid profile offline_access")
	case "pkce":
		q.Set("code_challenge_method", "plain")
	case "extra":
		q.Set("client_secret", "SYNTHETIC-CANARY")
	case "duplicate":
		q.Add("state", state)
	case "path":
		u.Path = "/other"
	case "origin":
		u.Host = "evil.example"
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func TestEngineRejectsMismatchedClientAndAuthorizationParameters(t *testing.T) {
	for _, mode := range []string{"issuer", "client", "callback"} {
		t.Run("binding-"+mode, func(t *testing.T) {
			f := setupAuth(t)
			config := f.e.config
			original := config.Codes.(*OAuthCodeClient)
			copyClient := *original
			switch mode {
			case "issuer":
				copyClient.issuer = "https://other.example/"
			case "client":
				copyClient.config.ClientID = "other-client"
			case "callback":
				copyClient.config.RedirectURL = "https://evil.example/auth/callback"
			}
			config.Codes = &copyClient
			if e, err := New(config); !errors.Is(err, portal.ErrUnavailable) || e != nil {
				t.Fatal("mismatched bindings accepted")
			}
		})
	}
	for _, mode := range []string{"nonce", "callback", "scope", "pkce", "extra", "duplicate", "path", "origin"} {
		t.Run("authorization-"+mode, func(t *testing.T) {
			f := setupAuth(t)
			config := f.e.config
			config.Codes = alteredCodes{config.Codes, mode}
			e, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			e.ServeHTTP(w, httptest.NewRequest("GET", "https://portal.example/auth/login", nil))
			if w.Code != 503 || w.Header().Get("Location") != "" || w.Header().Get("Set-Cookie") != "" || len(f.store.logins) != 0 || strings.Contains(w.Body.String(), "CANARY") {
				t.Fatal("invalid authorization parameters escaped")
			}
		})
	}
}

func TestLocalRevocationExpiryAndPolling(t *testing.T) {
	// With no activity path, IdleTTL is the effective fixed lifetime. The
	// after-both-deadlines case cannot isolate the later absolute-time check.
	for _, mode := range []string{"revoke", "idle-expiry", "after-both-deadlines", "unavailable", "duplicate-cookie", "forged-cookie", "wrong-host", "plain-http"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			r := f.request("GET", "/portal/projects/dev-env/environments/internal/attention", "")
			f.now = f.now.Add(time.Minute)
			if _, err := f.e.Authenticate(r); err != nil {
				t.Fatal(err)
			}
			if f.store.puts != 1 {
				t.Fatal("polling rewrote session")
			}
			switch mode {
			case "revoke":
				key, _ := requestHandle(r, sessionCookie)
				if err := f.store.RevokeSession(context.Background(), key); err != nil {
					t.Fatal(err)
				}
			case "idle-expiry":
				f.now = f.now.Add(time.Minute)
			case "after-both-deadlines":
				f.now = f.now.Add(time.Hour)
			case "unavailable":
				f.store.fail = true
			case "duplicate-cookie":
				r.AddCookie(f.session)
			case "forged-cookie":
				r.Header.Set("Cookie", sessionCookie+"="+strings.Repeat("x", 43))
				r.Header.Set("X-Forwarded-User", "synthetic-subject")
			case "wrong-host":
				r.Host = "evil.example"
			case "plain-http":
				r.TLS = nil
			}
			p, err := f.e.Authenticate(r)
			if err == nil || p != (portal.Principal{}) {
				t.Fatal("invalid session authenticated")
			}
			if f.exchanges != 1 {
				t.Fatal("session verification contacted provider")
			}
		})
	}
}

func TestLogoutRequiresOriginAndSessionBoundCSRF(t *testing.T) {
	for _, mode := range []string{"valid", "missing-origin", "wrong-origin", "duplicate-origin", "missing-csrf", "wrong-csrf", "lookup-failure", "revoke-failure"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			r := f.request("GET", "/auth/logout", "")
			w := httptest.NewRecorder()
			f.e.ServeHTTP(w, r)
			key, _ := requestHandle(r, sessionCookie)
			record, err := f.store.GetSession(context.Background(), key)
			if err != nil || !strings.Contains(w.Body.String(), record.CSRF) {
				t.Fatal("logout form missing bound CSRF")
			}
			csrf := record.CSRF
			if mode == "missing-csrf" {
				csrf = ""
			}
			if mode == "wrong-csrf" {
				csrf = strings.Repeat("x", 43)
			}
			r = f.request("POST", "/auth/logout", url.Values{"csrf": []string{csrf}}.Encode())
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("Origin", "https://portal.example")
			if mode == "missing-origin" {
				r.Header.Del("Origin")
			}
			if mode == "wrong-origin" {
				r.Header.Set("Origin", "https://evil.example")
			}
			if mode == "duplicate-origin" {
				r.Header.Add("Origin", "https://evil.example")
			}
			if mode == "lookup-failure" {
				f.store.fail = true
			}
			if mode == "revoke-failure" {
				f.store.failRevoke = true
			}
			w = httptest.NewRecorder()
			f.e.ServeHTTP(w, r)
			if mode == "valid" {
				if w.Code != 200 || findCookie(t, w, sessionCookie).MaxAge != -1 {
					t.Fatal("logout failed")
				}
				if _, err = f.e.Authenticate(f.request("GET", "/auth/logout", "")); !errors.Is(err, portal.ErrDenied) {
					t.Fatal("revoked session survived")
				}
			} else {
				if w.Code == 200 || w.Header().Get("Set-Cookie") != "" || strings.Contains(w.Body.String(), "CANARY") {
					t.Fatal("invalid logout succeeded or leaked")
				}
				if mode == "revoke-failure" {
					if f.store.revocations != 1 {
						t.Fatal("revoke failure branch was not reached")
					}
					if _, err := f.e.Authenticate(f.request("GET", "/auth/logout", "")); err != nil {
						t.Fatal("failed revocation incorrectly claimed success")
					}
				}
			}
			if f.exchanges != 1 {
				t.Fatal("logout contacted provider")
			}
		})
	}
}
