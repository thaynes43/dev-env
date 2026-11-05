//go:build portallocaltest

package session

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

var localTestListen = netip.MustParseAddrPort("127.0.0.1:8099")

func newLocalTestForTest(t *testing.T) *LocalTest {
	t.Helper()
	sessions, err := os.ReadFile("../../../scripts/trials/portal_product/sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := os.ReadFile("../../../scripts/trials/portal_product/tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	instance, err := NewLocalTest(localTestListen, LocalTestFixtures{Sessions: sessions, Tasks: tasks})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(instance.Close)
	return instance
}

func localTestRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = localTestListen.String()
	r.RemoteAddr = "127.0.0.1:50000"
	return r
}

func serveLocalTest(instance *LocalTest, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	instance.Handler().ServeHTTP(w, r)
	return w
}

func TestLocalTestServesRealPagesWithBanner(t *testing.T) {
	instance := newLocalTestForTest(t)
	for _, tc := range []struct {
		path, policy string
		want         []string
	}{
		{SessionsPath, portal.ProductPolicy, []string{"fixture-portal-polish", "fixture-coordinator"}},
		{TasksPath, portal.ProductPolicy, []string{"Fixture: portal visual refresh"}},
		{pagesPath, "", []string{"Fixture sample alert"}},
		{AsksPath, "", []string{"Which fixture should the agent use for the next check?"}},
		{ClaimsPath, "", []string{"fixture-coordinator"}},
	} {
		w := serveLocalTest(instance, localTestRequest(http.MethodGet, tc.path))
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("X-Portal-Local-Test") != "fixture" {
			t.Fatalf("%s: status %d: %.300s", tc.path, w.Code, body)
		}
		policy := w.Header().Get("Content-Security-Policy")
		if (tc.policy != "" && policy != tc.policy) || !strings.HasPrefix(policy, "default-src 'none'") {
			t.Fatalf("%s: policy %q differs from production", tc.path, policy)
		}
		if !regexp.MustCompile(`<body[^>]*><p class="banner" id="`+LocalTestBannerID+`"`).MatchString(body) || !strings.Contains(body, "<title>Local test · ") {
			t.Fatalf("%s: no local test banner or title", tc.path)
		}
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: fixture row %q missing", tc.path, want)
			}
		}
		if strings.Contains(body, "local-test-fixture-access-token") || strings.Contains(body, localTestOPToken) || strings.Contains(body, "fixture-not-signed") {
			t.Fatalf("%s: a fixture token reached the page", tc.path)
		}
	}
	// Every source reached its fixture upstream through the in-memory net; a
	// source wired with another dialer would leave its count at zero.
	for _, addr := range []string{operatorPortalHost + ":8443", openProjectPortalHost + ":8443", localTestAMHost} {
		instance.upstreams.mu.Lock()
		routed := instance.upstreams.routed[addr]
		instance.upstreams.mu.Unlock()
		if routed == 0 {
			t.Fatalf("no page dialled %s through the fixture net", addr)
		}
	}
	w := serveLocalTest(instance, localTestRequest(http.MethodGet, "/"))
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != SessionsPath {
		t.Fatalf("root redirect %d %q", w.Code, w.Header().Get("Location"))
	}
	w = serveLocalTest(instance, localTestRequest(http.MethodGet, "/healthz"))
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), localTestText) {
		t.Fatalf("healthz %d %q", w.Code, w.Body.String())
	}
}

func TestLocalTestGateRefusesOutsideRequests(t *testing.T) {
	instance := newLocalTestForTest(t)
	for name, mutate := range map[string]func(*http.Request){
		"non-loopback client": func(r *http.Request) { r.RemoteAddr = "192.168.1.20:50000" },
		"unparsed client":     func(r *http.Request) { r.RemoteAddr = "pipe" },
		"rebound host":        func(r *http.Request) { r.Host = "evil.example:8099" },
		"other port":          func(r *http.Request) { r.Host = "127.0.0.1:8100" },
		"production host":     func(r *http.Request) { r.Host = productionHost },
		"absolute form":       func(r *http.Request) { r.URL, _ = url.Parse("http://evil.example" + SessionsPath) },
	} {
		r := localTestRequest(http.MethodGet, SessionsPath)
		mutate(r)
		w := serveLocalTest(instance, r)
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "fixture-portal-polish") || !strings.HasPrefix(w.Body.String(), localTestText) {
			t.Fatalf("%s: status %d", name, w.Code)
		}
	}
	for _, header := range forwardedIdentity {
		r := localTestRequest(http.MethodGet, SessionsPath)
		r.Header.Set(header, "someone-else")
		if w := serveLocalTest(instance, r); w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "fixture-portal-polish") {
			t.Fatalf("%s: forwarded identity status %d", header, w.Code)
		}
	}
	r := localTestRequest(http.MethodGet, SessionsPath)
	r.Host = "localhost:8099"
	if w := serveLocalTest(instance, r); w.Code != http.StatusOK {
		t.Fatalf("localhost host refused: %d", w.Code)
	}
}

func TestLocalTestLoginAndCrossOriginPostsStayClosed(t *testing.T) {
	instance := newLocalTestForTest(t)
	if w := serveLocalTest(instance, localTestRequest(http.MethodGet, "/auth/login")); w.Code == http.StatusSeeOther || w.Code == http.StatusOK {
		t.Fatalf("login started: %d %q", w.Code, w.Header().Get("Location"))
	}
	w := serveLocalTest(instance, localTestRequest(http.MethodGet, "/auth/logout"))
	match := regexp.MustCompile(`name="csrf" value="([A-Za-z0-9_-]{43})"`).FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || match == nil {
		t.Fatalf("logout form %d", w.Code)
	}
	post := func(origin string) int {
		r := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader("csrf="+match[1]))
		r.Host, r.RemoteAddr = localTestListen.String(), "127.0.0.1:50000"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		return serveLocalTest(instance, r).Code
	}
	for _, origin := range []string{"https://evil.example", "http://localhost:8099", "http://127.0.0.1:8100"} {
		if code := post(origin); code != http.StatusForbidden {
			t.Fatalf("logout from origin %s: %d", origin, code)
		}
	}
	if code := post("http://" + localTestListen.String()); code != http.StatusOK {
		t.Fatalf("same-origin logout %d", code)
	}
	if w := serveLocalTest(instance, localTestRequest(http.MethodGet, SessionsPath)); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "fixture-portal-polish") {
		t.Fatal("revoked fixed session still reads")
	}
}

func TestLocalTestNetworkRefusesEveryOtherDestination(t *testing.T) {
	upstreams := &localTestNet{routes: map[string]*pipeListener{}}
	defer upstreams.close()
	upstreams.serve(operatorPortalHost+":8443", nil, http.NotFoundHandler())
	// A regression that dials for real must fail within the deadline, not hang.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, addr := range []string{"dev-env-operator.dev-env-system.svc.cluster.local:443", "example.invalid:443", "127.0.0.1:8443", "issuer.localtest.invalid:443"} {
		conn, err := upstreams.dial(ctx, "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, errLocalTestNetwork) {
			t.Fatalf("%s dialled: %v", addr, err)
		}
	}
	if _, err := upstreams.dial(ctx, "udp", operatorPortalHost+":8443"); !errors.Is(err, errLocalTestNetwork) {
		t.Fatal("udp dialled")
	}
	conn, err := upstreams.dial(ctx, "tcp", operatorPortalHost+":8443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestLocalTestRefusesNonLoopbackListenAndBadFixtures(t *testing.T) {
	sessions, _ := os.ReadFile("../../../scripts/trials/portal_product/sessions.json")
	tasks, _ := os.ReadFile("../../../scripts/trials/portal_product/tasks.json")
	for _, raw := range []string{"0.0.0.0:8099", "192.168.1.10:8099", "[::]:8099", "127.0.0.1:0"} {
		if _, err := NewLocalTest(netip.MustParseAddrPort(raw), LocalTestFixtures{Sessions: sessions, Tasks: tasks}); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
	if _, err := NewLocalTest(localTestListen, LocalTestFixtures{Sessions: []byte(`{}`), Tasks: tasks}); err == nil {
		t.Fatal("invalid fixture accepted")
	}
}
