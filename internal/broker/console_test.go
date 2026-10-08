package broker

import (
	"context"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

type consoleFixture struct {
	console *Console
	client  client.Client
	clock   *clocktesting.FakePassiveClock
	grant   *v1alpha1.AccessGrant
}

func newConsoleFixture(t *testing.T, bg bool) *consoleFixture {
	t.Helper()
	now := time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakePassiveClock(now)
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	g := &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "grant-console", CreationTimestamp: metav1.NewTime(now)},
		Spec: v1alpha1.AccessGrantSpec{Type: v1alpha1.GrantKube, Requester: v1alpha1.GrantRequester{Session: "session", Repo: "repo", Profile: "full"},
			Kube: &v1alpha1.KubeGrant{Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}}, TTL: metav1.Duration{Duration: time.Hour}, Reason: "test a workload change"},
		Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantPending},
	}
	if bg {
		g.Spec.Type, g.Spec.Kube.Role, g.Spec.Kube.Namespaces = v1alpha1.GrantBreakglass, v1alpha1.RoleBreakglass, nil
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(g).WithStatusSubresource(&v1alpha1.AccessGrant{}).Build()
	b := &Broker{Client: c, APIReader: c, Clock: clk, SessionNamespace: "dev-agents", PolicyNamespace: "dev-env-system"}
	a, err := NewForwardAuthAuthenticator("owner", "", "")
	if err != nil {
		t.Fatal(err)
	}
	a.Now = clk.Now
	console, err := NewConsole(b, a, "https://approvals.example.com", "https://auth.example.com/if/flow/fresh/")
	if err != nil {
		t.Fatal(err)
	}
	return &consoleFixture{console: console, client: c, clock: clk, grant: g}
}

func (f *consoleFixture) request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("X-authentik-username", "owner")
	r.Header.Set("X-authentik-jwt", consoleToken(map[string]any{"exp": f.clock.Now().Add(time.Hour).Unix(), "auth_time": f.clock.Now().Add(-time.Minute).Unix()}))
	if method == http.MethodPost {
		r.Header.Set("Origin", f.console.Origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return r
}

func (f *consoleFixture) cookie(t *testing.T) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	f.console.Handler().ServeHTTP(w, f.request(http.MethodGet, "/approvals/"+f.grant.Name, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == csrfCookieName {
			if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
				t.Fatalf("unsafe cookie %+v", cookie)
			}
			return cookie
		}
	}
	t.Fatal("GET set no CSRF cookie")
	return nil
}

func (f *consoleFixture) stored(t *testing.T) *v1alpha1.AccessGrant {
	t.Helper()
	var g v1alpha1.AccessGrant
	if err := f.client.Get(context.Background(), client.ObjectKeyFromObject(f.grant), &g); err != nil {
		t.Fatal(err)
	}
	return &g
}

func TestConsoleCrossReplicaDecision(t *testing.T) {
	f := newConsoleFixture(t, false)
	cookie := f.cookie(t)
	second, err := NewConsole(f.console.Broker, f.console.Auth, f.console.Origin, f.console.ReauthURL)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"csrf": {cookie.Value}, "action": {"approve"}, "ttl": {"20m"}}
	r := f.request(http.MethodPost, "/approvals/"+f.grant.Name+"/decision", form.Encode())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	second.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/approvals/"+f.grant.Name {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	g := f.stored(t)
	if g.Status.Phase != v1alpha1.GrantActive || g.Status.ApprovedBy != "authentik/owner" || g.Status.ApprovedTTL.Duration != 20*time.Minute || !g.Status.ExpiresAt.Time.Equal(f.clock.Now().Add(20*time.Minute)) {
		t.Errorf("decision %+v", g.Status)
	}
	w = httptest.NewRecorder()
	r = f.request(http.MethodPost, "/approvals/"+f.grant.Name+"/decision", form.Encode())
	r.AddCookie(cookie)
	second.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Errorf("repeat decision returned %d", w.Code)
	}
}

func TestConsoleRefusesUnsafeDecision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*http.Request, url.Values)
		status int
	}{
		{"missing identity", func(r *http.Request, _ url.Values) { r.Header.Del("X-authentik-username") }, 401},
		{"wrong owner", func(r *http.Request, _ url.Values) { r.Header.Set("X-authentik-username", "another") }, 403},
		{"foreign origin", func(r *http.Request, _ url.Values) { r.Header.Set("Origin", "https://another.example.com") }, 403},
		{"missing origin", func(r *http.Request, _ url.Values) { r.Header.Del("Origin") }, 403},
		{"duplicate origin", func(r *http.Request, _ url.Values) { r.Header.Add("Origin", r.Header.Get("Origin")) }, 403},
		{"missing cookie", func(r *http.Request, _ url.Values) { r.Header.Del("Cookie") }, 403},
		{"duplicate cookie", func(r *http.Request, _ url.Values) { r.Header.Add("Cookie", r.Header.Get("Cookie")) }, 403},
		{"wrong token", func(_ *http.Request, v url.Values) { v.Set("csrf", "different") }, 403},
		{"duplicate token", func(_ *http.Request, v url.Values) { v.Add("csrf", v.Get("csrf")) }, 400},
		{"client approver", func(_ *http.Request, v url.Values) { v.Set("by", "authentik/owner") }, 400},
		{"bad action", func(_ *http.Request, v url.Values) { v.Set("action", "release") }, 400},
		{"malformed ttl", func(_ *http.Request, v url.Values) { v.Set("ttl", "forever") }, 400},
		{"zero ttl", func(_ *http.Request, v url.Values) { v.Set("ttl", "0s") }, 400},
		{"short ttl", func(_ *http.Request, v url.Values) { v.Set("ttl", "5m") }, 422},
		{"long ttl", func(_ *http.Request, v url.Values) { v.Set("ttl", "2h") }, 422},
		{"multipart", func(r *http.Request, _ url.Values) { r.Header.Set("Content-Type", "multipart/form-data") }, 415},
		{"query approver", func(r *http.Request, _ url.Values) { r.URL.RawQuery = "by=owner" }, 400},
		{"large form", func(_ *http.Request, v url.Values) { v.Set("reason", strings.Repeat("x", 9<<10)) }, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConsoleFixture(t, false)
			cookie := f.cookie(t)
			form := url.Values{"csrf": {cookie.Value}, "action": {"approve"}}
			r := f.request(http.MethodPost, "/approvals/"+f.grant.Name+"/decision", "")
			r.AddCookie(cookie)
			tc.edit(r, form)
			body := form.Encode()
			r.Body = io.NopCloser(strings.NewReader(body))
			r.ContentLength = int64(len(body))
			w := httptest.NewRecorder()
			f.console.Handler().ServeHTTP(w, r)
			if w.Code != tc.status || f.stored(t).Status.Phase != v1alpha1.GrantPending {
				t.Fatalf("POST: %d, want %d; status %+v", w.Code, tc.status, f.stored(t).Status)
			}
		})
	}
}

func TestConsoleBreakglassActualLoginTime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		login  time.Duration
		omit   bool
		deny   bool
		status int
	}{
		{"fresh", -time.Minute, false, false, 303},
		{"at five minutes", -5 * time.Minute, false, false, 303},
		{"old login fresh issuance", -6 * time.Minute, false, false, 403},
		{"future login", time.Second, false, false, 403},
		{"missing login", 0, true, false, 403},
		{"stale login denial", -time.Hour, false, true, 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newConsoleFixture(t, true)
			cookie := f.cookie(t)
			form := url.Values{"csrf": {cookie.Value}, "action": {"approve"}}
			if tc.deny {
				form.Set("action", "deny")
				form.Set("reason", "not today")
			}
			r := f.request(http.MethodPost, "/approvals/"+f.grant.Name+"/decision", form.Encode())
			r.AddCookie(cookie)
			claims := map[string]any{"exp": f.clock.Now().Add(time.Hour).Unix(), "iat": f.clock.Now().Unix()}
			if !tc.omit {
				claims["auth_time"] = f.clock.Now().Add(tc.login).Unix()
			}
			r.Header.Set("X-authentik-jwt", consoleToken(claims))
			w := httptest.NewRecorder()
			f.console.Handler().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("POST: %d, want %d", w.Code, tc.status)
			}
			if tc.status == 403 && f.stored(t).Status.Phase != v1alpha1.GrantPending {
				t.Fatal("stale approval changed the grant")
			}
			if tc.deny && f.stored(t).Status.DeniedBy != "authentik/owner" {
				t.Fatal("denial did not record the owner")
			}
		})
	}
	f := newConsoleFixture(t, true)
	cookie := f.cookie(t)
	r := f.request(http.MethodPost, "/approvals/"+f.grant.Name+"/decision", url.Values{"csrf": {cookie.Value}, "action": {"approve"}}.Encode())
	r.AddCookie(cookie)
	f.clock.SetTime(f.clock.Now().Add(5 * time.Minute))
	w := httptest.NewRecorder()
	f.console.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("page freshness reused at POST: %d", w.Code)
	}
}

func TestConsoleEscapesContentAndFiltersPending(t *testing.T) {
	f := newConsoleFixture(t, false)
	f.grant = f.stored(t)
	f.grant.Spec.Reason = `<script>alert("agent")</script>`
	f.grant.Spec.Requester.Parent = "parent"
	if err := f.client.Update(context.Background(), f.grant); err != nil {
		t.Fatal(err)
	}
	parent := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "parent"}, Spec: v1alpha1.AgentSessionSpec{Repo: "parent-repo"}}
	if err := f.client.Create(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	f.console.Handler().ServeHTTP(w, f.request(http.MethodGet, "/approvals/"+f.grant.Name, ""))
	if w.Code != 200 || strings.Contains(w.Body.String(), "<script>") || !strings.Contains(w.Body.String(), html.EscapeString(f.grant.Spec.Reason)) || !strings.Contains(w.Body.String(), "parent-repo") {
		t.Fatalf("unescaped or incomplete page: %d %s", w.Code, w.Body.String())
	}
	for _, h := range []string{"Cache-Control", "Content-Security-Policy", "X-Frame-Options", "Referrer-Policy", "X-Content-Type-Options"} {
		if w.Header().Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	f.clock.SetTime(f.clock.Now().Add(PendingTimeout))
	w = httptest.NewRecorder()
	f.console.Handler().ServeHTTP(w, f.request(http.MethodGet, "/approvals", ""))
	if strings.Contains(w.Body.String(), f.grant.Name) {
		t.Error("timed-out request listed as pending")
	}
	v, err := f.console.grantView(context.Background(), f.stored(t), ConsoleIdentity{})
	if err != nil || v.Pending {
		t.Fatalf("timed-out view: %+v %v", v, err)
	}
}

func TestConsolePolicySnippet(t *testing.T) {
	f := newConsoleFixture(t, false)
	text, err := f.console.policySnippet(f.grant)
	if err != nil {
		t.Fatal(err)
	}
	var p v1alpha1.GrantPolicy
	if err := yaml.Unmarshal([]byte(text), &p); err != nil || !Matches(&p, f.grant) || p.Namespace != "dev-env-system" {
		t.Fatalf("snippet failed to preserve the request: %+v %v", p, err)
	}
	for _, change := range []func(*v1alpha1.AccessGrant){
		func(g *v1alpha1.AccessGrant) { g.Spec.Requester.Profile = "ops" },
		func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Role = v1alpha1.RoleSecretsRead },
		func(g *v1alpha1.AccessGrant) {
			g.Spec.Type = v1alpha1.GrantBreakglass
			g.Spec.Kube.Role = v1alpha1.RoleBreakglass
		},
		func(g *v1alpha1.AccessGrant) { g.Spec.Kube.Namespaces = []string{"dev-agents"} },
	} {
		g := f.grant.DeepCopy()
		change(g)
		if text, err := f.console.policySnippet(g); err != nil || text != "" {
			t.Fatalf("forbidden snippet: %s %v", text, err)
		}
	}
	for _, mutate := range []func(*v1alpha1.AccessGrant){
		func(g *v1alpha1.AccessGrant) { g.Spec.Release = true },
		func(g *v1alpha1.AccessGrant) { g.Status.Phase = v1alpha1.GrantActive },
		func(g *v1alpha1.AccessGrant) { at := metav1.NewTime(f.clock.Now()); g.DeletionTimestamp = &at },
	} {
		g := f.grant.DeepCopy()
		mutate(g)
		if consolePending(g, f.clock.Now()) {
			t.Error("ended or released request shown as pending")
		}
	}
}
