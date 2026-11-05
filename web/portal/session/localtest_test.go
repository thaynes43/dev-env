//go:build portallocaltest

package session

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal"
)

var localTestListen = netip.MustParseAddrPort("127.0.0.1:8099")

var errInterceptedOutbound = errors.New("outbound attempt intercepted by the test")

// interceptOutbound replaces the process resolver for one test. Every DNS
// query is recorded and failed inside the process, so code that dials a
// name with any real dialer records an attempt instead of reaching the
// network. Refused destinations in these tests are names (.invalid or the
// pinned cluster names), never IP literals, so no dial can skip the resolver.
// At cleanup the test fails if anything was recorded.
func interceptOutbound(t *testing.T) *outboundRecorder {
	t.Helper()
	recorder := &outboundRecorder{}
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		recorder.mu.Lock()
		recorder.attempts = append(recorder.attempts, network+" "+address)
		recorder.mu.Unlock()
		return nil, errInterceptedOutbound
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	t.Cleanup(func() {
		if attempts := recorder.list(); len(attempts) != 0 {
			t.Errorf("outbound attempts intercepted: %d %v", len(attempts), attempts)
		}
	})
	return recorder
}

type outboundRecorder struct {
	mu       sync.Mutex
	attempts []string
}

func (r *outboundRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.attempts...)
}

func (r *outboundRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := r.attempts
	r.attempts = nil
	return taken
}

// TestLocalTestInterceptorCatchesRealDialers checks the safety net itself: a
// real dialer and a default HTTP client aimed at a .invalid name are recorded
// and failed inside the process.
func TestLocalTestInterceptorCatchesRealDialers(t *testing.T) {
	recorder := interceptOutbound(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", "probe.localtest.invalid:443"); err == nil {
		_ = conn.Close()
		t.Fatal("real dialer connected")
	}
	if len(recorder.take()) == 0 {
		t.Fatal("real dialer lookup was not intercepted")
	}
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	if response, err := client.Get("http://probe.localtest.invalid/"); err == nil {
		_ = response.Body.Close()
		t.Fatal("default HTTP transport connected")
	}
	if len(recorder.take()) == 0 {
		t.Fatal("HTTP client lookup was not intercepted")
	}
}

func newLocalTestForTest(t *testing.T) *LocalTest {
	t.Helper()
	interceptOutbound(t)
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
		{SessionsPath, portal.ProductSendPolicy, []string{"fixture-portal-polish", "fixture-coordinator"}},
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
	// Only .invalid names: a regression that dials for real cannot reach a
	// real address, and the deadline stops it from hanging.
	const routed = "fixture.localtest.invalid:8443"
	recorder := interceptOutbound(t)
	upstreams := &localTestNet{routes: map[string]*pipeListener{}}
	defer upstreams.close()
	upstreams.serve(routed, nil, http.NotFoundHandler())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, addr := range []string{"fixture.localtest.invalid:443", "example.invalid:443", "other.localtest.invalid:8443", "issuer.localtest.invalid:443"} {
		conn, err := upstreams.dial(ctx, "tcp", addr)
		if conn != nil {
			_ = conn.Close()
		}
		if !errors.Is(err, errLocalTestNetwork) {
			t.Fatalf("%s dialled: %v (intercepted attempts: %d)", addr, err, len(recorder.list()))
		}
	}
	if _, err := upstreams.dial(ctx, "udp", routed); !errors.Is(err, errLocalTestNetwork) {
		t.Fatal("udp dialled")
	}
	conn, err := upstreams.dial(ctx, "tcp", routed)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestLocalTestRefusesNonLoopbackListenAndBadFixtures(t *testing.T) {
	interceptOutbound(t)
	sessions, _ := os.ReadFile("../../../scripts/trials/portal_product/sessions.json")
	tasks, _ := os.ReadFile("../../../scripts/trials/portal_product/tasks.json")
	for _, raw := range []string{"0.0.0.0:8099", "192.168.1.10:8099", "[::]:8099", "127.0.0.1:0"} {
		if instance, err := NewLocalTest(netip.MustParseAddrPort(raw), LocalTestFixtures{Sessions: sessions, Tasks: tasks}); err == nil {
			t.Cleanup(instance.Close)
			t.Fatalf("%s accepted", raw)
		}
	}
	if instance, err := NewLocalTest(localTestListen, LocalTestFixtures{Sessions: []byte(`{}`), Tasks: tasks}); err == nil {
		t.Cleanup(instance.Close)
		t.Fatal("invalid fixture accepted")
	}
}

// TestLocalTestRefusesSourcesOffTheFixtureNet checks the construction-time
// guard: a source whose client could dial anything else stops the instance
// before it serves a request. No request is sent here.
func TestLocalTestRefusesSourcesOffTheFixtureNet(t *testing.T) {
	interceptOutbound(t)
	upstreams := &localTestNet{routes: map[string]*pipeListener{}}
	defer upstreams.close()
	caPEM, _, err := localTestCertificates("fixture.localtest.invalid")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC) }
	source := func(dial func(context.Context, string, string) (net.Conn, error)) *SessionSource {
		s, err := newSessionSource("unused-token-path", caPEM, now, dial)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	good := func() AssemblyConfig {
		s := source(upstreams.dial)
		tasks := &TaskSource{path: s.path, client: source(upstreams.dial).client, now: now}
		send := &commandHandler{transport: &commandClient{client: source(upstreams.dial).client}}
		launch := &launchHandler{transport: &launchClient{client: source(upstreams.dial).client}}
		authorizer, err := localTestCurrentAuthorizer(&localTestStore{}, caPEM, now, upstreams)
		if err != nil {
			t.Fatal(err)
		}
		s.UseConversations(&conversationSource{authorizer: authorizer, client: source(upstreams.dial).client})
		return AssemblyConfig{Sessions: s, Tasks: tasks, Pages: newPagesSource(true, now, upstreams.dial), Authorizer: authorizer,
			Asks: &AsksSource{client: source(upstreams.dial).client}, Claims: &ClaimsSource{client: source(upstreams.dial).client}, Send: send, Launch: launch}
	}
	offNetAuthorizer := func() *CurrentAuthorizer {
		a, err := newCurrentAuthorizer(CurrentAuthorizationConfig{Issuer: localTestIssuer, UserInfoURL: "https://" + localTestIssuerHost + "/application/o/userinfo/", Store: &localTestStore{}, Now: now, Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	if err := localTestDialersFixtureOnly(good(), upstreams); err != nil {
		t.Fatalf("fixture-only config refused: %v", err)
	}
	realDial := (&net.Dialer{}).DialContext // never called: no request is sent
	for name, mutate := range map[string]func(*AssemblyConfig){
		"sessions default dialer": func(c *AssemblyConfig) { c.Sessions = source(nil) },
		"tasks real dialer":       func(c *AssemblyConfig) { c.Tasks.client = source(realDial).client },
		"pages default dialer":    func(c *AssemblyConfig) { c.Pages = newPagesSource(true, now, nil) },
		"asks default dialer":     func(c *AssemblyConfig) { c.Asks.client = source(nil).client },
		"claims proxy":            func(c *AssemblyConfig) { c.Claims.client.Transport.(*http.Transport).Proxy = http.ProxyFromEnvironment },
		"claims missing":          func(c *AssemblyConfig) { c.Claims = nil },
		"conversations":           func(c *AssemblyConfig) { c.Sessions.conversations = previewConversation{} },
		"send default dialer": func(c *AssemblyConfig) {
			c.Send = &commandHandler{transport: &commandClient{client: source(nil).client}}
		},
		"send other transport": func(c *AssemblyConfig) { c.Send = &commandHandler{transport: &commandTransportFake{}} },
		"send missing":         func(c *AssemblyConfig) { c.Send = nil },
		"launch default dialer": func(c *AssemblyConfig) {
			c.Launch = &launchHandler{transport: &launchClient{client: source(nil).client}}
		},
		"launch missing": func(c *AssemblyConfig) { c.Launch = nil },
		"authorizer default dialer": func(c *AssemblyConfig) {
			a := offNetAuthorizer()
			c.Authorizer = a
			c.Sessions.conversations.(*conversationSource).authorizer = a
		},
		"authorizer other type": func(c *AssemblyConfig) { c.Authorizer = authorizeFunc(nil) },
		"history default dialer": func(c *AssemblyConfig) {
			c.Sessions.conversations.(*conversationSource).client = source(nil).client
		},
		"history other authorizer": func(c *AssemblyConfig) {
			c.Sessions.conversations.(*conversationSource).authorizer = offNetAuthorizer()
		},
		"history missing": func(c *AssemblyConfig) { c.Sessions.conversations = nil },
	} {
		c := good()
		mutate(&c)
		if err := localTestDialersFixtureOnly(c, upstreams); !errors.Is(err, errLocalTestNetwork) {
			t.Fatalf("%s accepted", name)
		}
	}
}
