package agentrun

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// serveList answers every request with an empty session list.
func serveList(h *harness) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{}})
	}
}

func (h *harness) lastAuth() string {
	h.t.Helper()
	reqs := h.api.requests()
	if len(reqs) == 0 {
		h.t.Fatal("no request reached the API")
	}
	return reqs[len(reqs)-1].auth
}

// A session pod has the operator's AGENTD_API_* settings and its projected
// token (D-41); agent-run uses them with no flag.
func TestConnectFromASessionPod(t *testing.T) {
	h := newHarness(t)
	serveList(h)
	ca := h.vars[envCAFile]
	h.vars = map[string]string{envAgentdURL: h.api.srv.URL + "/", envAgentdCAFile: ca}
	h.write("no-session-token", "session-token\n")
	h.mustRun(ExitOK, "list")
	if got := h.lastAuth(); got != "Bearer session-token" {
		t.Errorf("Authorization = %q, want the projected token", got)
	}

	// AGENTD_API_TOKEN_FILE moves the token.
	h.vars[envAgentdTokenFile] = h.write("elsewhere/token", "moved-token")
	h.mustRun(ExitOK, "list")
	if got := h.lastAuth(); got != "Bearer moved-token" {
		t.Errorf("Authorization = %q, want AGENTD_API_TOKEN_FILE's token", got)
	}
}

// With the projected token but no AGENTD_API_URL, a session pod calls the
// API's Service by its full name.
func TestConnectDefaultsToTheService(t *testing.T) {
	h := newHarness(t)
	h.vars = map[string]string{}
	h.write("no-session-token", "session-token")
	c, err := (&app{env: h.env}).connect(connOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if c.url != DefaultAPIURL {
		t.Errorf("url = %s, want %s", c.url, DefaultAPIURL)
	}
	if got := c.token.Describe(); got != "the token at "+h.env.SessionTokenFile+" (this session pod's projected token)" {
		t.Errorf("token = %s", got)
	}
}

// Flags beat DEV_ENV_API_*, which beat the session pod's settings.
func TestConnectPrecedence(t *testing.T) {
	h := newHarness(t)
	serveList(h)
	h.write("no-session-token", "session-token")
	h.vars[envAgentdURL] = "https://127.0.0.1:1"
	h.mustRun(ExitOK, "list")
	if got := h.lastAuth(); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want DEV_ENV_API_TOKEN_FILE's token over the session's", got)
	}

	flagToken := h.write("flag-token", "flag-token")
	h.vars[envAPIURL] = "https://127.0.0.1:1"
	h.mustRun(ExitOK, "list", "--api-url", h.api.srv.URL, "--token-file", flagToken, "--ca-file", h.vars[envCAFile])
	if got := h.lastAuth(); got != "Bearer flag-token" {
		t.Errorf("Authorization = %q, want --token-file's token", got)
	}
}

func TestConnectErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars map[string]string
		args []string
		exit int
		want string
	}{
		{"nothing outside the cluster", map[string]string{}, nil, ExitAuth, "no API address: outside the cluster, pass --api-url or set DEV_ENV_API_URL"},
		{"an address but no token", map[string]string{envAPIURL: "https://api.example"}, nil, ExitAuth, "no token for the API: outside a cluster pod, pass --token-file"},
		{"plain http", map[string]string{envAPIURL: "http://api.example"}, nil, ExitUsage, `the API address "http://api.example" is not an https:// URL`},
		{"no host", map[string]string{}, []string{"--api-url", "https://"}, ExitUsage, "is not an https:// URL with a host"},
		{"a missing token file", map[string]string{envAPIURL: "https://127.0.0.1:1"}, []string{"--token-file", "/nonexistent/token"}, ExitAuth, "no token at /nonexistent/token (from --token-file)"},
		{"a missing CA file", map[string]string{envAPIURL: "https://127.0.0.1:1", envTokenFile: "/x"}, []string{"--ca-file", "/nonexistent/ca.crt"}, ExitAuth, "the CA file: open /nonexistent/ca.crt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.vars = tc.vars
			h.mustRun(tc.exit, append([]string{"list"}, tc.args...)...)
			contains(t, "stderr", h.stderr.String(), tc.want)
		})
	}

	h := newHarness(t)
	h.vars[envTokenFile] = h.write("empty-token", "\n")
	h.mustRun(ExitAuth, "list")
	contains(t, "stderr", h.stderr.String(), "is empty")

	h.vars[envCAFile] = h.write("not-a-ca", "hello")
	h.mustRun(ExitAuth, "list")
	contains(t, "stderr", h.stderr.String(), "holds no PEM certificate")
}

// A certificate agent-run does not trust is not retried: it will not change.
func TestConnectUntrustedCertificate(t *testing.T) {
	h := newHarness(t)
	serveList(h)
	delete(h.vars, envCAFile)
	h.mustRun(ExitAuth, "list")
	contains(t, "stderr", h.stderr.String(), "has a certificate agent-run does not trust", "--ca-file or DEV_ENV_API_CA_FILE")
	if len(h.sleeps) != 0 {
		t.Errorf("retried %d times, want none", len(h.sleeps))
	}
}

// fakeKube is the Kubernetes API's TokenRequest route.
type fakeKube struct {
	srv    *httptest.Server
	mints  atomic.Int32
	mu     sync.Mutex
	auth   string
	path   string
	body   map[string]any
	status int
}

func jwtFor(sub string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"sub":"`+sub+`","kubernetes.io":{"namespace":"dev"}}`)) + ".c2ln"
}

// inPod puts the harness in a pod that is not a session: a ServiceAccount
// token, the cluster's CA and KUBERNETES_SERVICE_HOST, and a fake Kubernetes
// API that mints tokens.
func inPod(h *harness, status int) *fakeKube {
	k := &fakeKube{status: status}
	k.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		k.auth, k.path = r.Header.Get("Authorization"), r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&k.body)
		k.mu.Unlock()
		if k.status != http.StatusCreated {
			writeTestJSON(w, k.status, map[string]any{"kind": "Status", "message": `serviceaccounts "dev-env" is forbidden: User "system:serviceaccount:dev:dev-env" cannot create resource "serviceaccounts/token"`})
			return
		}
		n := k.mints.Add(1)
		writeTestJSON(w, http.StatusCreated, map[string]any{"status": map[string]any{"token": fmt.Sprintf("minted-%d", n)}})
	}))
	h.t.Cleanup(k.srv.Close)
	u, _ := url.Parse(k.srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	h.env.ServiceAccountDir = filepath.Join(h.dir, "sa")
	h.write("sa/token", jwtFor("system:serviceaccount:dev:dev-env"))
	h.write("sa/ca.crt", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: k.srv.Certificate().Raw})))
	h.vars[envKubeHost], h.vars[envKubePort] = host, port
	delete(h.vars, envTokenFile)
	return k
}

// The v1 pod mints a token for its own ServiceAccount, as kubectl create token
// would (D-46), and reuses it for five minutes.
func TestConnectMintsATokenInAPod(t *testing.T) {
	h := newHarness(t)
	k := inPod(h, http.StatusCreated)
	serveCreate(h, pendingOn(testSession(name, ""), "Creating", "creating the pod"), runningOn(testSession(name, ""), "talosw02"))

	h.mustRun(ExitOK, "--repo", "haynes-ops", "-p", "task")
	if k.path != "/api/v1/namespaces/dev/serviceaccounts/dev-env/token" {
		t.Errorf("TokenRequest path = %s", k.path)
	}
	if k.auth != "Bearer "+jwtFor("system:serviceaccount:dev:dev-env") {
		t.Errorf("TokenRequest Authorization = %q, want the pod's own token", k.auth)
	}
	spec, _ := k.body["spec"].(map[string]any)
	if aud, _ := spec["audiences"].([]any); len(aud) != 1 || aud[0] != apiv1.TokenAudience || spec["expirationSeconds"] != float64(mintSeconds) {
		t.Errorf("TokenRequest spec = %v, want audience dev-env-operator for %d s", spec, mintSeconds)
	}
	for _, r := range h.api.requests() {
		if r.auth != "Bearer minted-1" {
			t.Errorf("%s %s sent %q, want the minted token", r.method, r.path, r.auth)
		}
	}
	if n := k.mints.Load(); n != 1 {
		t.Errorf("minted %d tokens for one run, want 1", n)
	}

	// A minted token is used for five minutes, then minted again.
	app := &app{env: h.env}
	c, err := app.connect(connOpts{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	first, _ := c.token.Token(ctx)
	h.now = h.now.Add(4 * time.Minute)
	again, _ := c.token.Token(ctx)
	h.now = h.now.Add(2 * time.Minute)
	later, err := c.token.Token(ctx)
	if err != nil || first != again || later == first {
		t.Errorf("tokens %q, %q, %q (%v): want one reused, then a new one after 5 minutes", first, again, later, err)
	}
	if got := c.token.Describe(); got != "a token minted for this pod's ServiceAccount dev/dev-env" {
		t.Errorf("Describe = %q", got)
	}
}

func TestConnectMintRefused(t *testing.T) {
	h := newHarness(t)
	inPod(h, http.StatusForbidden)
	serveList(h)
	h.mustRun(ExitAuth, "list")
	contains(t, "stderr", h.stderr.String(),
		"the Kubernetes API refused to mint a token for dev/dev-env (403)",
		"needs create on serviceaccounts/token for itself (resourceNames: [dev-env])")
	if n := len(h.api.requests()); n != 0 {
		t.Errorf("sent %d requests to the API without a token", n)
	}
}

func TestServiceAccountOf(t *testing.T) {
	ns, n, err := serviceAccountOf(jwtFor("system:serviceaccount:dev:dev-env"))
	if err != nil || ns != "dev" || n != "dev-env" {
		t.Errorf("got %q %q %v, want dev dev-env", ns, n, err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	for _, tok := range []string{
		"",
		"a.b",
		"a.!!!.c",
		"a." + enc([]byte("not json")) + ".c",
		jwtFor("system:node:talosw02"),
		jwtFor("system:serviceaccount:dev"),
		jwtFor("system:serviceaccount::dev-env"),
		jwtFor("system:serviceaccount:dev:a:b"),
	} {
		if _, _, err := serviceAccountOf(tok); err == nil {
			t.Errorf("%q: no error", tok)
		}
	}
}
