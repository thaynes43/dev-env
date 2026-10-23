package session

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

type authorizeFunc func(context.Context, portal.Principal, portal.Resource) error

func (f authorizeFunc) Authorize(c context.Context, p portal.Principal, r portal.Resource) error {
	return f(c, p, r)
}

type readFunc func(context.Context, portal.Resource) (portal.Snapshot, error)

func (f readFunc) Read(c context.Context, r portal.Resource) (portal.Snapshot, error) { return f(c, r) }

func TestAuthenticatedSessionStillNeedsCurrentAuthorization(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	reads := 0
	h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizeFunc(func(_ context.Context, p portal.Principal, r portal.Resource) error {
		if p.Subject != "synthetic-subject" {
			t.Fatal("identity changed")
		}
		return portal.ErrDenied
	}), Reader: readFunc(func(context.Context, portal.Resource) (portal.Snapshot, error) {
		reads++
		return portal.Snapshot{}, nil
	}), FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	r := f.request("GET", "/portal/projects/dev-env/environments/internal/attention", "")
	r.Header.Set("X-Forwarded-User", "admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || reads != 0 || strings.Contains(w.Body.String(), "synthetic-subject") {
		t.Fatal("login/header granted metadata permission")
	}
}

func TestTwoPortalUsersAndProjectsRemainIsolated(t *testing.T) {
	f := setupAuth(t)
	f.subject = "subject-one"
	f.login(t)
	first := f.session
	f.subject = "subject-two"
	f.login(t)
	second := f.session
	for _, actor := range []struct {
		subject, project string
		cookie           *http.Cookie
	}{{"subject-one", "project-one", first}, {"subject-two", "project-two", second}} {
		for _, project := range []string{"project-one", "project-two"} {
			t.Run(actor.subject+project, func(t *testing.T) {
				reads := 0
				h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizeFunc(func(_ context.Context, p portal.Principal, r portal.Resource) error {
					if p.Subject != actor.subject {
						t.Fatal("session principal mixed")
					}
					if r.Project != actor.project || r.Environment != "internal" {
						return portal.ErrDenied
					}
					return nil
				}), Reader: readFunc(func(_ context.Context, r portal.Resource) (portal.Snapshot, error) {
					reads++
					return portal.Snapshot{Resource: r, State: portal.Verified, UpdatedAt: f.now, VerifiedAt: f.now}, nil
				}), FreshFor: time.Minute, Now: func() time.Time { return f.now }})
				r := httptest.NewRequest("GET", "https://portal.example/portal/projects/"+project+"/environments/internal/attention", nil)
				r.AddCookie(actor.cookie)
				r.Header.Set("X-Forwarded-User", "admin")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if project == actor.project {
					if w.Code != 200 || reads != 1 || !strings.Contains(w.Body.String(), "Verified empty") {
						t.Fatal("authorized read failed")
					}
				} else {
					if w.Code != 404 || reads != 0 || w.Body.String() != "Attention unavailable. Current task state is Unknown.\n" {
						t.Fatal("cross-project read leaked")
					}
				}
			})
		}
	}
}

func TestSessionRevokedDuringMetadataReadDiscardsResponse(t *testing.T) {
	f := setupAuth(t)
	f.login(t)
	r := f.request("GET", "/portal/projects/dev-env/environments/internal/attention", "")
	key, err := requestHandle(r, sessionCookie)
	if err != nil {
		t.Fatal(err)
	}
	h := NewAssembly(AssemblyConfig{Session: f.e, Authorizer: authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error { return nil }), Reader: readFunc(func(_ context.Context, r portal.Resource) (portal.Snapshot, error) {
		if err := f.store.RevokeSession(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		task := r
		task.Task = "task-one"
		return portal.Snapshot{Resource: r, State: portal.Verified, UpdatedAt: f.now, VerifiedAt: f.now, Items: []portal.Item{{Resource: task, WorkPackage: 178, Group: portal.NeedsYou, Title: "SECRET-METADATA-CANARY", UpdatedAt: f.now, VerifiedAt: f.now}}}, nil
	}), FreshFor: time.Minute, Now: func() time.Time { return f.now }})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || w.Body.String() != "Attention unavailable. Current task state is Unknown.\n" {
		t.Fatal("revoked session leaked response")
	}
}

func TestPanicAndResponseBoundContainment(t *testing.T) {
	for _, mode := range []string{"panic", "oversize", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(previous)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h := contained(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Set-Cookie", "SECRET-COOKIE-CANARY")
				if mode == "oversize" {
					_, _ = w.Write(make([]byte, maximumResponse+1))
					return
				}
				_, _ = w.Write([]byte("SECRET-BODY-CANARY"))
				if mode == "cancelled" {
					cancel()
					return
				}
				panic("SECRET-PANIC-CANARY")
			}))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "https://portal.example/", nil).WithContext(ctx))
			if w.Code != 503 || w.Body.String() != "Portal authentication unavailable or denied.\n" || w.Header().Get("Set-Cookie") != "" || logs.Len() != 0 {
				t.Fatal("buffered failure leaked body/header/panic log")
			}
		})
	}
}

func TestUnconfiguredAssemblyAndAuthenticationMethods(t *testing.T) {
	for _, path := range []string{"/auth/login", "/auth/logout", "/portal/projects/dev-env/environments/internal/attention"} {
		w := httptest.NewRecorder()
		NewAssembly(AssemblyConfig{}).ServeHTTP(w, httptest.NewRequest("GET", "https://portal.example"+path, nil))
		if w.Code != 503 {
			t.Fatal("unconfigured assembly appeared available")
		}
	}
	f := setupAuth(t)
	for _, path := range []string{"/auth/login", "/auth/callback", "/auth/logout"} {
		for _, method := range []string{"HEAD", "OPTIONS", "DELETE", "PATCH"} {
			w := httptest.NewRecorder()
			f.e.ServeHTTP(w, httptest.NewRequest(method, "https://portal.example"+path, nil))
			if w.Code != 405 || w.Header().Get("Allow") == "" {
				t.Fatal("rejected auth method accepted")
			}
		}
	}
	if _, err := New(Config{}); !errors.Is(err, portal.ErrUnavailable) {
		t.Fatal("unconfigured session engine accepted")
	}
}
