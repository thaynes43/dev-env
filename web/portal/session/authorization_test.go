package session

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

func currentFixture(f *authFixture, trip tripFunc) *CurrentAuthorizer {
	return &CurrentAuthorizer{issuer: f.e.config.Verifier.issuer, subject: "synthetic-subject", endpoint: "https://issuer.example/application/o/userinfo/", store: f.store, now: func() time.Time { return f.now }, client: &http.Client{Transport: trip, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

func userInfoResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

const currentAllow = `{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":true}`

func TestCurrentUserInfoStrictClaims(t *testing.T) {
	for _, body := range []string{
		`{}`, `null`, `[]`, `true`, currentAllow + `{}`, currentAllow + `oops`,
		`{"sub":"other","portal_is_active":true,"portal_is_admin":true}`,
		`{"sub":"synthetic-subject","portal_is_active":false,"portal_is_admin":true}`,
		`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":false}`,
		`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":"true"}`,
		`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":null}`,
		`{"sub":"synthetic-subject","portal_is_active":1,"portal_is_admin":true}`,
		`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":true,"portal_is_admin":false}`,
		`{"sub":"other","sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":true}`,
		`{"sub":["synthetic-subject"],"portal_is_active":true,"portal_is_admin":true}`,
	} {
		if validUserInfo([]byte(body), "synthetic-subject") {
			t.Fatal("malformed or denied current claims accepted")
		}
	}
	if !validUserInfo([]byte(currentAllow), "synthetic-subject") {
		t.Fatal("well-typed current claims denied")
	}
}

func TestCurrentAuthorizationRechecksBeforeFinalRelease(t *testing.T) {
	for _, mode := range []string{"allow", "group-withdrawn", "account-disabled", "token-revoked", "subject-changed", "malformed", "outage", "revoked-in-fetch", "replaced-token-in-fetch", "outside-ID", "mismatched-ID", "final-userinfo-revoke", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			calls, reads := 0, 0
			r := f.request("GET", LandingPath, "")
			key, _ := requestHandle(r, sessionCookie)
			original := f.store.sessions[key]
			a := currentFixture(f, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.String() != "https://issuer.example/application/o/userinfo/" || req.Header.Get("Authorization") != "Bearer "+original.accessToken {
					t.Fatal("exact session/token/endpoint binding lost")
				}
				if calls == 2 {
					switch mode {
					case "group-withdrawn":
						return userInfoResponse(strings.Replace(currentAllow, `"portal_is_admin":true`, `"portal_is_admin":false`, 1)), nil
					case "account-disabled":
						return userInfoResponse(strings.Replace(currentAllow, `"portal_is_active":true`, `"portal_is_active":false`, 1)), nil
					case "token-revoked":
						response := userInfoResponse(currentAllow)
						response.StatusCode = 401
						return response, nil
					case "subject-changed":
						return userInfoResponse(strings.Replace(currentAllow, "synthetic-subject", "other", 1)), nil
					case "malformed":
						return userInfoResponse(`{"portal_is_admin":"true"}`), nil
					case "outage":
						return nil, errors.New("SECRET-PROVIDER-CANARY")
					case "final-userinfo-revoke":
						if err := f.store.RevokeSession(context.Background(), key); err != nil {
							t.Fatal("fixture revoke failed")
						}
					}
				}
				return userInfoResponse(currentAllow), nil
			})
			h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: a, Reader: readFunc(func(ctx context.Context, resource portal.Resource) (portal.Snapshot, error) {
				reads++
				switch mode {
				case "revoked-in-fetch":
					_ = f.store.RevokeSession(ctx, key)
				case "replaced-token-in-fetch":
					changed := original
					changed.accessToken = "other-session-token-canary"
					f.store.sessions[key] = changed
				}
				task := resource
				task.Task = "178"
				id := 178
				if mode == "outside-ID" {
					task.Task = "0178"
					id = 178
				}
				if mode == "mismatched-ID" {
					id = 123
				}
				return portal.Snapshot{Resource: resource, State: portal.Verified, UpdatedAt: f.now, VerifiedAt: f.now, Items: []portal.Item{{Resource: task, WorkPackage: id, Group: portal.NeedsYou, Title: "SYNTHETIC-METADATA-CANARY", UpdatedAt: f.now, VerifiedAt: f.now}}}, nil
			}), FreshFor: time.Minute, Now: func() time.Time { return f.now }})
			if mode == "cancelled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if mode == "allow" {
				if w.Code != 200 || calls != 2 || reads != 1 || !strings.Contains(w.Body.String(), "SYNTHETIC-METADATA-CANARY") {
					t.Fatal("expected fresh batched release did not occur")
				}
			} else if w.Code == 200 || strings.Contains(w.Body.String(), "SYNTHETIC-METADATA-CANARY") {
				t.Fatal("changed current authorization leaked buffered metadata")
			}
			if original.accessToken != f.store.sessions[key].accessToken && mode != "revoked-in-fetch" && mode != "final-userinfo-revoke" && mode != "replaced-token-in-fetch" {
				t.Fatal("fixture old token claims unexpectedly changed")
			}
			for _, secret := range []string{original.accessToken, "SECRET-PROVIDER-CANARY", original.CSRF} {
				if strings.Contains(w.Body.String(), secret) || strings.Contains(w.Header().Get("Set-Cookie"), secret) {
					t.Fatal("credential leaked")
				}
			}
		})
	}
}

func TestExactSessionCannotBorrowHealthySameSubjectToken(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	first := f.session
	firstRequest := f.e.bind(f.request("GET", LandingPath, ""))
	firstKey, _ := requestHandle(firstRequest, sessionCookie)
	firstRecord := f.store.sessions[firstKey]
	f.now = f.now.Add(time.Second)
	f.login(t)
	secondRequest := f.e.bind(f.request("GET", LandingPath, ""))
	secondKey, _ := requestHandle(secondRequest, sessionCookie)
	secondRecord := f.store.sessions[secondKey]
	if firstRecord.accessToken == secondRecord.accessToken {
		t.Fatal("fixture did not mint distinct tokens")
	}
	a := currentFixture(f, func(req *http.Request) (*http.Response, error) {
		response := userInfoResponse(currentAllow)
		if req.Header.Get("Authorization") == "Bearer "+firstRecord.accessToken {
			response.StatusCode = 401
		} else if req.Header.Get("Authorization") != "Bearer "+secondRecord.accessToken {
			t.Fatal("unknown session credential")
		}
		return response, nil
	})
	p := firstRecord.Principal
	r := portal.Resource{Project: "dev-env", Environment: "internal"}
	if a.Authorize(secondRequest.Context(), p, r) != nil {
		t.Fatal("healthy second session denied")
	}
	if !errors.Is(a.Authorize(firstRequest.Context(), p, r), portal.ErrDenied) {
		t.Fatal("revoked first token borrowed second session")
	}
	if !errors.Is(a.Authorize(context.Background(), p, r), portal.ErrDenied) {
		t.Fatal("unbound principal authorized")
	}
	// Even the same principal cannot replace the bound request's browser handle.
	secondRequest.Header.Del("Cookie")
	secondRequest.AddCookie(first)
	if _, err := f.e.Authenticate(secondRequest); !errors.Is(err, portal.ErrDenied) {
		t.Fatal("exact handle binding replaced")
	}
	wrong := p
	wrong.Subject = "other"
	if !errors.Is(a.Authorize(firstRequest.Context(), wrong, r), portal.ErrDenied) {
		t.Fatal("wrong principal accepted")
	}
}

func TestCurrentAuthorizationDefaultDenyAndTransport(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	a := currentFixture(f, func(*http.Request) (*http.Response, error) {
		t.Fatal("outside allowlist called UserInfo")
		return nil, portal.ErrUnavailable
	})
	r := f.e.bind(f.request("GET", LandingPath, ""))
	p, _ := f.e.Authenticate(r)
	if !canonicalWorkPackageID("179") || canonicalWorkPackageID("0178") || canonicalWorkPackageID("178/logs") {
		t.Fatal("work-package id grammar drifted")
	}
	for _, resource := range []portal.Resource{{Project: "haynes-engineering", Environment: "internal", Task: "178"}, {Project: "other", Environment: "internal"}, {Project: "dev-env", Environment: "prod"}, {Project: "dev-env", Environment: "internal", Task: "0178"}, {Project: "dev-env", Environment: "internal", Task: "178/logs"}} {
		if !errors.Is(a.Authorize(r.Context(), p, resource), portal.ErrDenied) {
			t.Fatal("outside allowlist accepted")
		}
	}
	for _, mode := range []string{"redirect", "html", "oversize", "nil", "error"} {
		a = currentFixture(f, func(*http.Request) (*http.Response, error) {
			response := userInfoResponse(currentAllow)
			switch mode {
			case "redirect":
				response.StatusCode = 302
				response.Header.Set("Location", "https://evil.example/")
			case "html":
				response.Header.Set("Content-Type", "text/html")
			case "oversize":
				response.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 65537)))
			case "nil":
				return nil, nil
			case "error":
				return nil, errors.New("SECRET-TRANSPORT-CANARY")
			}
			return response, nil
		})
		if a.Authorize(r.Context(), p, portal.Resource{Project: "dev-env", Environment: "internal"}) == nil {
			t.Fatal("unsafe UserInfo response accepted")
		}
	}
}

func TestCurrentAuthorizationConstructorTLSAndEndpoint(t *testing.T) {
	for _, mode := range []string{"valid", "http", "cross-host", "wrong-path", "redirect-query", "noTLS", "skipTLS", "wrong-peer", "custom-dial", "proxy"} {
		c := CurrentAuthorizationConfig{Issuer: "https://issuer.example/portal/", Subject: "synthetic-subject", UserInfoURL: "https://issuer.example/application/o/userinfo/", Store: &memoryStore{}, Now: time.Now, Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
		switch mode {
		case "http":
			c.UserInfoURL = "http://issuer.example/application/o/userinfo/"
		case "cross-host":
			c.UserInfoURL = "https://evil.example/application/o/userinfo/"
		case "wrong-path":
			c.UserInfoURL = "https://issuer.example/other"
		case "redirect-query":
			c.UserInfoURL += "?next=elsewhere"
		case "noTLS":
			c.Transport.TLSClientConfig = nil
		case "skipTLS":
			c.Transport.TLSClientConfig.InsecureSkipVerify = true
		case "wrong-peer":
			c.Transport.TLSClientConfig.ServerName = "other.example"
		case "custom-dial":
			c.Transport.DialContext = http.DefaultTransport.(*http.Transport).DialContext
		case "proxy":
			c.Transport.Proxy = http.ProxyFromEnvironment
		}
		a, err := NewCurrentAuthorizer(c)
		if (mode == "valid") != (err == nil && a != nil) {
			t.Fatalf("%s: TLS constructor accepted unsafe configuration or rejected valid configuration", mode)
		}
	}
}

func TestReferenceTokenDigestNotRawCredential(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	r := f.e.bind(f.request("GET", LandingPath, ""))
	ref, ok := r.Context().Value(sessionContextKey{}).(sessionReference)
	if !ok || ref.tokenDigest != sha256.Sum256([]byte(f.store.sessions[ref.handle].accessToken)) {
		t.Fatal("missing private exact-session reference")
	}
}

func TestUserInfoServiceErrorsRemainUnavailable(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 502, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := setupAuth(t)
			f.login(t)
			req := f.e.bind(f.request("GET", LandingPath, ""))
			principal, err := f.e.Authenticate(req)
			if err != nil {
				t.Fatal("fixture authenticate failed")
			}
			a := currentFixture(f, func(*http.Request) (*http.Response, error) {
				r := userInfoResponse(currentAllow)
				r.StatusCode = status
				return r, nil
			})
			err = a.Authorize(req.Context(), principal, portal.Resource{Project: "dev-env", Environment: "internal"})
			want := portal.ErrDenied
			if status == 429 || status >= 500 {
				want = portal.ErrUnavailable
			}
			if !errors.Is(err, want) {
				t.Fatal("provider diagnostic classification lost")
			}
		})
	}
}
