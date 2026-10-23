package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/oauth2"

	"github.com/thaynes43/dev-env/web/portal"
)

const sessionCookie = "__Host-portal"
const loginCookie = "__Host-portal-login"
const LandingPath = "/portal/projects/dev-env/environments/internal/attention"

type Config struct {
	Origin, Callback string
	// All durations are explicit source configuration, not production defaults.
	// M3 has no activity/touch operation: IdleTTL is the fixed server lifetime
	// from creation. AbsoluteTTL currently determines cookie expiry; with
	// IdleTTL <= AbsoluteTTL its later server check cannot decide independently.
	// A reviewed activity path and new tests are prerequisites for sliding idle
	// behavior or an independently deciding absolute lifetime. Polling never
	// extends a deadline.
	LoginTTL, IdleTTL, AbsoluteTTL time.Duration
	Now                            func() time.Time
	Store                          Store
	Codes                          CodeClient
	Verifier                       *TokenVerifier
}

type Engine struct {
	config             Config
	host, issuerOrigin string
	codeBinding        CodeBinding
}

func New(c Config) (*Engine, error) {
	origin, err := configuredURL(c.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, portal.ErrUnavailable
	}
	if c.Callback != c.Origin+"/auth/callback" || c.LoginTTL <= 0 || c.IdleTTL <= 0 || c.AbsoluteTTL <= 0 || c.IdleTTL > c.AbsoluteTTL || c.Now == nil || c.Store == nil || c.Codes == nil || c.Verifier == nil {
		return nil, portal.ErrUnavailable
	}
	issuer, err := url.Parse(c.Verifier.issuer)
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	binding := c.Codes.Binding()
	endpoint, err := configuredURL(binding.AuthorizationEndpoint)
	if err != nil || endpoint.Host != issuer.Host || binding.Issuer != c.Verifier.issuer || binding.ClientID != c.Verifier.clientID || binding.Callback != c.Callback {
		return nil, portal.ErrUnavailable
	}
	return &Engine{config: c, host: origin.Host, issuerOrigin: issuer.Scheme + "://" + issuer.Host, codeBinding: binding}, nil
}

func randomHandle() (string, Handle, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", Handle{}, portal.ErrUnavailable
	}
	text := base64.RawURLEncoding.EncodeToString(raw[:])
	return text, sha256.Sum256([]byte(text)), nil
}

func requestHandle(r *http.Request, name string) (Handle, error) {
	var value string
	found := false
	for _, cookie := range r.Cookies() {
		if cookie.Name == name {
			if found {
				return Handle{}, portal.ErrDenied
			}
			value = cookie.Value
			found = true
		}
	}
	if len(value) != 43 {
		return Handle{}, portal.ErrDenied
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) != 32 {
		return Handle{}, portal.ErrDenied
	}
	return sha256.Sum256([]byte(value)), nil
}

func cookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge})
}

func cookieAge(ttl time.Duration) int {
	seconds := ttl / time.Second
	if ttl%time.Second != 0 {
		seconds++
	}
	return int(seconds)
}

func (e *Engine) Authenticate(r *http.Request) (portal.Principal, error) {
	key, record, err := e.current(r)
	if err != nil {
		return portal.Principal{}, err
	}
	if ref, ok := r.Context().Value(sessionContextKey{}).(sessionReference); ok && (ref.handle != key || ref.principal != record.Principal || ref.tokenDigest != sha256.Sum256([]byte(record.accessToken))) {
		return portal.Principal{}, portal.ErrDenied
	}
	return record.Principal, nil
}

type sessionContextKey struct{}
type sessionReference struct {
	handle      Handle
	principal   portal.Principal
	tokenDigest [32]byte
}

func (e *Engine) bind(r *http.Request) *http.Request {
	key, record, err := e.current(r)
	if err != nil {
		return r
	}
	ref := sessionReference{handle: key, principal: record.Principal, tokenDigest: sha256.Sum256([]byte(record.accessToken))}
	return r.WithContext(context.WithValue(r.Context(), sessionContextKey{}, ref))
}

func (e *Engine) current(r *http.Request) (Handle, Record, error) {
	if e == nil || e.config.Store == nil {
		return Handle{}, Record{}, portal.ErrUnavailable
	}
	if r.Context().Err() != nil {
		return Handle{}, Record{}, portal.ErrUnavailable
	}
	if r.TLS == nil || r.Host != e.host {
		return Handle{}, Record{}, portal.ErrDenied
	}
	key, err := requestHandle(r, sessionCookie)
	if err != nil {
		return Handle{}, Record{}, portal.ErrDenied
	}
	record, err := e.config.Store.GetSession(r.Context(), key)
	if err != nil {
		return Handle{}, Record{}, normalize(err)
	}
	if r.Context().Err() != nil {
		return Handle{}, Record{}, portal.ErrUnavailable
	}
	now := e.config.Now()
	if !validRecord(record, now) || record.Principal.Issuer != e.config.Verifier.issuer {
		return Handle{}, Record{}, portal.ErrDenied
	}
	return key, record, nil
}

// ServeHTTP serves authentication routes only. The assembly routes portal reads
// separately and supplies buffering/panic containment before runtime wiring.
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	authHeaders(w)
	if e == nil || r.TLS == nil || r.Host != e.host || r.URL.RawPath != "" {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	switch r.URL.Path {
	case "/auth/login":
		if r.Method != "GET" {
			methodFailure(w, "GET")
			return
		}
		e.begin(w, r)
	case "/auth/callback":
		if r.Method != "GET" {
			methodFailure(w, "GET")
			return
		}
		e.callback(w, r)
	case "/auth/logout":
		if r.Method != "GET" && r.Method != "POST" {
			methodFailure(w, "GET, POST")
			return
		}
		e.logout(w, r)
	default:
		authFailure(w, portal.ErrDenied)
	}
}

func (e *Engine) begin(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		authFailure(w, portal.ErrDenied)
		return
	}
	handle, key, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	state, _, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	nonce, _, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	verifier, _, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	destination, err := e.config.Codes.AuthorizationURL(state, nonce, verifier)
	u, parseErr := url.Parse(destination)
	if err != nil || parseErr != nil || u.Scheme+"://"+u.Host != e.issuerOrigin || u.User != nil || u.Fragment != "" {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	q, err := url.ParseQuery(u.RawQuery)
	endpoint := *u
	endpoint.RawQuery = ""
	if err != nil || endpoint.String() != e.codeBinding.AuthorizationEndpoint || len(q) != 8 || len(q["state"]) != 1 || q.Get("state") != state || q.Get("nonce") != nonce || q.Get("client_id") != e.codeBinding.ClientID || q.Get("redirect_uri") != e.config.Callback || q.Get("response_type") != "code" || q.Get("scope") != "openid profile" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") != oauth2.S256ChallengeFromVerifier(verifier) {
		authFailure(w, portal.ErrUnavailable)
		return
	}
	for _, values := range q {
		if len(values) != 1 {
			authFailure(w, portal.ErrUnavailable)
			return
		}
	}
	expires := e.config.Now().Add(e.config.LoginTTL)
	if err = e.config.Store.PutLogin(r.Context(), key, Login{State: state, Nonce: nonce, Verifier: verifier, ExpiresAt: expires}); err != nil {
		authFailure(w, normalize(err))
		return
	}
	cookie(w, loginCookie, handle, expires, cookieAge(e.config.LoginTTL))
	w.Header().Set("Location", destination)
	w.WriteHeader(http.StatusSeeOther)
}

func (e *Engine) callback(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q) != 2 || len(q["code"]) != 1 || len(q["state"]) != 1 || q.Get("code") == "" || len(q.Get("code")) > 4096 || len(q.Get("state")) != 43 {
		authFailure(w, portal.ErrDenied)
		return
	}
	key, err := requestHandle(r, loginCookie)
	if err != nil {
		authFailure(w, portal.ErrDenied)
		return
	}
	now := e.config.Now()
	login, err := e.config.Store.TakeLogin(r.Context(), key, q.Get("state"), now)
	if err != nil {
		authFailure(w, normalize(err))
		return
	}
	if subtle.ConstantTimeCompare([]byte(login.State), []byte(q.Get("state"))) != 1 || !now.Before(login.ExpiresAt) || len(login.Nonce) != 43 || len(login.Verifier) != 43 {
		authFailure(w, portal.ErrDenied)
		return
	}
	// Consume before exchange. An interrupted/failed exchange needs a new login,
	// never a blind replay of this authorization code or transaction.
	raw, err := e.config.Codes.Exchange(r.Context(), q.Get("code"), login.Verifier)
	if err != nil {
		authFailure(w, normalize(err))
		return
	}
	principal, tokenExpiry, err := e.config.Verifier.verifyExchange(r.Context(), raw, login.Nonce)
	if err != nil {
		authFailure(w, normalize(err))
		return
	}
	if !e.config.Now().Before(login.ExpiresAt) {
		authFailure(w, portal.ErrDenied)
		return
	}
	handle, sessionKey, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	csrf, _, err := randomHandle()
	if err != nil {
		authFailure(w, err)
		return
	}
	now = e.config.Now()
	expires := now.Add(e.config.IdleTTL)
	if absolute := now.Add(e.config.AbsoluteTTL); absolute.Before(expires) {
		expires = absolute
	}
	if tokenExpiry.Before(expires) {
		expires = tokenExpiry
	}
	// Max-Age takes precedence over Expires in browsers. Floor it so a
	// fractional remaining second never extends the token's cookie deadline.
	maxAge := int(expires.Sub(now) / time.Second)
	if maxAge <= 0 {
		authFailure(w, portal.ErrDenied)
		return
	}
	record := Record{Principal: principal, CSRF: csrf, CreatedAt: now, IdleExpiresAt: expires, ExpiresAt: expires, accessToken: raw.accessToken, tokenExpiresAt: tokenExpiry}
	if err = e.config.Store.PutSession(r.Context(), sessionKey, record); err != nil {
		authFailure(w, normalize(err))
		return
	}
	cookie(w, loginCookie, "", time.Unix(1, 0), -1)
	cookie(w, sessionCookie, handle, record.ExpiresAt, maxAge)
	w.Header().Set("Location", LandingPath)
	w.WriteHeader(http.StatusSeeOther)
}

func validRecord(r Record, now time.Time) bool {
	return identityText(r.Principal.Issuer) && identityText(r.Principal.Subject) && bearerText(r.accessToken) && !r.CreatedAt.IsZero() && !r.CreatedAt.After(now) && now.Before(r.IdleExpiresAt) && now.Before(r.ExpiresAt) && now.Before(r.tokenExpiresAt) && !r.IdleExpiresAt.After(r.ExpiresAt) && !r.ExpiresAt.After(r.tokenExpiresAt) && len(r.CSRF) == 43
}

var logoutForm = template.Must(template.New("logout").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Portal logout</title></head><body><main><h1>Portal logout</h1><form method="post" action="/auth/logout"><input type="hidden" name="csrf" value="{{.}}"><button type="submit">Log out of this portal</button></form></main></body></html>`))

func (e *Engine) logout(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		authFailure(w, portal.ErrDenied)
		return
	}
	key, record, err := e.current(r)
	if err != nil {
		authFailure(w, err)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = logoutForm.Execute(w, record.CSRF)
		return
	}
	if r.Header.Get("Origin") != e.config.Origin || len(r.Header.Values("Origin")) != 1 {
		authFailure(w, portal.ErrDenied)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	if err = r.ParseForm(); err != nil || len(r.PostForm) != 1 || len(r.PostForm["csrf"]) != 1 || subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(record.CSRF)) != 1 {
		authFailure(w, portal.ErrDenied)
		return
	}
	if err = e.config.Store.RevokeSession(r.Context(), key); err != nil {
		authFailure(w, normalize(err))
		return
	}
	cookie(w, sessionCookie, "", time.Unix(1, 0), -1)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "Portal session revoked. Identity-provider SSO status is separate.\n")
}

func normalize(err error) error {
	if errors.Is(err, portal.ErrDenied) {
		return portal.ErrDenied
	}
	return portal.ErrUnavailable
}
func authFailure(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	status := http.StatusServiceUnavailable
	if errors.Is(err, portal.ErrDenied) {
		status = http.StatusForbidden
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "Portal authentication unavailable or denied.\n")
}
func methodFailure(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusMethodNotAllowed)
	_, _ = io.WriteString(w, "Portal authentication unavailable or denied.\n")
}
func authHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
}

// Compile-time interface proof; request headers never select the principal.
var _ portal.Authenticator = (*Engine)(nil)
