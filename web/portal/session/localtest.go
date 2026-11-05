//go:build portallocaltest

package session

// This file is compiled only with the portallocaltest build tag (DESIGN-055).
// make build-portal and images/portal/Dockerfile never set that tag, so the
// fixed test principal, the in-memory store and the fixture upstreams below
// are not part of the deployed portal. TestLocalTestExcludedFromDefaultBuild
// pins that.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	// LocalTestBannerID is the id of the banner every local test page carries.
	LocalTestBannerID = "portal-localtest-banner"

	localTestHost     = "portal.localtest.invalid"
	localTestOrigin   = "https://" + localTestHost
	localTestIssuer   = "https://issuer.localtest.invalid/fixture-only/"
	localTestClientID = "dev-env-portal-localtest"
	localTestSubject  = "local-test-reader"
	localTestBanner   = `<p class="banner" id="` + LocalTestBannerID + `" role="note">Local test instance · fixture data · fixed test principal · not production</p>`
	localTestText     = "Local test instance (fixture data, not production).\n"
	localTestOPToken  = "local-test-openproject-fixture"
	localTestAMHost   = "alertmanager-operated.observability.svc.cluster.local:9093"
)

var errLocalTestNetwork = errors.New("portal local test instance opens no network connection")

// LocalTestFixtures holds the version-1 product fixture envelopes that the
// preview command also reads (scripts/trials/portal_product).
type LocalTestFixtures struct{ Sessions, Tasks []byte }

// LocalTest is one in-process portal test instance: the production assembly
// and gate, one fixed test principal, and in-memory fixture upstreams.
type LocalTest struct {
	handler   http.Handler
	upstreams *localTestNet
	close     func()
}

// Handler serves the instance. Mount it only on the loopback address given to
// NewLocalTest.
func (l *LocalTest) Handler() http.Handler { return l.handler }

// Close stops the fixture upstreams and removes the fixture token directory.
func (l *LocalTest) Close() { l.close() }

// NewLocalTest builds the instance for one bound loopback address. Requests
// must arrive from loopback with that address (or localhost and its port) as
// their Host. The gate then presents each request to the production gate as a
// TLS request for the fixed test host carrying the fixed test session.
func NewLocalTest(listen netip.AddrPort, fixtures LocalTestFixtures) (*LocalTest, error) {
	if !listen.Addr().Unmap().IsLoopback() || listen.Port() == 0 {
		return nil, portal.ErrUnavailable
	}
	sessionsFixture, err := readProductFixture(bytes.NewReader(fixtures.Sessions))
	if err != nil {
		return nil, err
	}
	tasksFixture, err := readProductFixture(bytes.NewReader(fixtures.Tasks))
	if err != nil {
		return nil, err
	}
	at := sessionsFixture.Now.UTC()
	now := func() time.Time { return at }

	dir, err := os.MkdirTemp("", "portal-localtest-")
	if err != nil {
		return nil, portal.ErrUnavailable
	}
	upstreams := &localTestNet{routes: map[string]*pipeListener{}}
	closeAll := func() {
		upstreams.close()
		_ = os.RemoveAll(dir)
	}
	instance, err := assembleLocalTest(listen, dir, at, now, sessionsFixture.Body, tasksFixture.Body, upstreams)
	if err != nil {
		closeAll()
		return nil, err
	}
	return &LocalTest{handler: instance, upstreams: upstreams, close: closeAll}, nil
}

func assembleLocalTest(listen netip.AddrPort, dir string, at time.Time, now func() time.Time, sessionsBody, tasksBody []byte, upstreams *localTestNet) (http.Handler, error) {
	operatorToken := localTestOperatorToken(at.Add(30 * 24 * time.Hour))
	operatorPath, openProjectPath := filepath.Join(dir, "operator-token"), filepath.Join(dir, "openproject-token")
	if os.WriteFile(operatorPath, []byte(operatorToken+"\n"), 0o600) != nil || os.WriteFile(openProjectPath, []byte(localTestOPToken+"\n"), 0o600) != nil {
		return nil, portal.ErrUnavailable
	}
	caPEM, certs, err := localTestCertificates(operatorPortalHost, openProjectPortalHost)
	if err != nil {
		return nil, err
	}
	asks, claims := localTestAsks(at), localTestClaims(at)
	upstreams.serve(operatorPortalHost+":8443", &certs[0], localTestOperator(operatorToken, sessionsBody, asks, claims))
	upstreams.serve(openProjectPortalHost+":8443", &certs[1], localTestOpenProject(tasksBody))
	upstreams.serve(localTestAMHost, nil, localTestAlertmanager())

	sessions, err := newSessionSource(operatorPath, caPEM, now, upstreams.dial)
	if err != nil {
		return nil, err
	}
	tasks, err := newTaskSource(openProjectPath, caPEM, now, upstreams.dial)
	if err != nil {
		return nil, err
	}
	asksBase, err := newSessionSource(operatorPath, caPEM, now, upstreams.dial)
	if err != nil {
		return nil, err
	}
	claimsBase, err := newSessionSource(operatorPath, caPEM, now, upstreams.dial)
	if err != nil {
		return nil, err
	}

	principal := portal.Principal{Issuer: localTestIssuer, Subject: localTestSubject}
	store := &localTestStore{logins: map[Handle]Login{}, sessions: map[Handle]Record{}}
	cookieValue, key, err := randomHandle()
	if err != nil {
		return nil, err
	}
	csrf, _, err := randomHandle()
	if err != nil {
		return nil, err
	}
	expires := at.Add(12 * time.Hour)
	if err = store.PutSession(context.Background(), key, Record{Principal: principal, CSRF: csrf, CreatedAt: at.Add(-time.Minute), IdleExpiresAt: expires, ExpiresAt: expires, accessToken: "local-test-fixture-access-token", tokenExpiresAt: expires}); err != nil {
		return nil, err
	}
	verifier, err := NewTokenVerifier(VerifierConfig{Issuer: localTestIssuer, ClientID: localTestClientID, Algorithms: []string{oidc.ES256}, Keys: localTestKeys{}, Now: now})
	if err != nil {
		return nil, err
	}
	engine, err := New(Config{Origin: localTestOrigin, Callback: localTestOrigin + "/auth/callback", LoginTTL: 10 * time.Minute, IdleTTL: 12 * time.Hour, AbsoluteTTL: 12 * time.Hour, Now: now, Store: store, Codes: localTestCodes{}, Verifier: verifier})
	if err != nil {
		return nil, err
	}
	config := AssemblyConfig{
		Session: engine, Authorizer: localTestAuthorizer{principal: principal}, Reader: portal.UnavailableReader{},
		Sessions: sessions, Tasks: tasks, Pages: newPagesSource(true, now, upstreams.dial),
		Asks:     &AsksSource{path: asksBase.path, client: asksBase.client, now: now},
		Claims:   &ClaimsSource{path: claimsBase.path, client: claimsBase.client, now: now, project: claimsProject},
		FreshFor: 5 * time.Minute, Now: now,
	}
	if err := localTestDialersFixtureOnly(config, upstreams); err != nil {
		return nil, err
	}
	assembly := productionGate(NewAssembly(config), localTestHost)

	hosts := map[string]bool{listen.String(): true, net.JoinHostPort("localhost", portText(listen)): true}
	return &localTestGate{next: assembly, hosts: hosts, cookie: cookieValue}, nil
}

// localTestDialersFixtureOnly refuses to build the instance unless every
// source's client dials through the fixture net, has no proxy, and no source
// carries another reader. It runs before any request can be served.
func localTestDialersFixtureOnly(c AssemblyConfig, upstreams *localTestNet) error {
	if c.Sessions == nil || c.Tasks == nil || c.Pages == nil || c.Asks == nil || c.Claims == nil || c.Send != nil || c.Launch != nil {
		return errLocalTestNetwork
	}
	if c.Sessions.conversations != nil || c.Sessions.allowance != nil || c.Sessions.currentSource != nil || c.Sessions.aggregateAllowance != nil || c.Sessions.questionAuditProject != "" {
		return errLocalTestNetwork
	}
	want := reflect.ValueOf(upstreams.dial).Pointer()
	for _, client := range []*http.Client{c.Sessions.client, c.Tasks.client, c.Pages.client, c.Asks.client, c.Claims.client} {
		if client == nil {
			return errLocalTestNetwork
		}
		transport, ok := client.Transport.(*http.Transport)
		if !ok || transport.Proxy != nil || transport.DialTLSContext != nil || transport.DialContext == nil || reflect.ValueOf(transport.DialContext).Pointer() != want {
			return errLocalTestNetwork
		}
	}
	return nil
}

func portText(a netip.AddrPort) string {
	_, port, _ := net.SplitHostPort(a.String())
	return port
}

// localTestGate is the test boundary. It stands in for the TLS listener and the
// browser's login: it refuses anything but a loopback client naming this
// instance, then hands the production gate a TLS request for the fixed test
// host with the fixed session cookie. Only an Origin equal to the request's
// own scheme and Host is translated, so cross-origin form posts still fail
// the production checks.
type localTestGate struct {
	next   http.Handler
	hosts  map[string]bool
	cookie string
}

func (g *localTestGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	remote, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil || !remote.Addr().Unmap().IsLoopback() || !g.hosts[r.Host] || r.URL.Host != "" || r.TLS != nil {
		authHeaders(w)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, localTestText+"Refused: loopback clients that name this instance only.\n")
		return
	}
	inner := r.Clone(r.Context())
	inner.Host = localTestHost
	inner.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true, ServerName: localTestHost}
	inner.Header.Del("Cookie")
	inner.AddCookie(&http.Cookie{Name: sessionCookie, Value: g.cookie})
	if origins := inner.Header.Values("Origin"); len(origins) == 1 && origins[0] == "http://"+r.Host {
		inner.Header.Set("Origin", localTestOrigin)
	}
	buffer := &responseBuffer{header: make(http.Header)}
	g.next.ServeHTTP(buffer, inner)
	if buffer.failed {
		buffer = &responseBuffer{header: make(http.Header)}
		authHeaders(buffer)
		authFailure(buffer, portal.ErrUnavailable)
	}
	for name, values := range buffer.header {
		w.Header()[name] = append([]string(nil), values...)
	}
	w.Header().Del("Content-Length")
	w.Header().Set("X-Portal-Local-Test", "fixture")
	code := buffer.code
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
	_, _ = w.Write(markLocalTest(buffer.header.Get("Content-Type"), buffer.body.Bytes()))
}

// markLocalTest puts the banner at the top of every HTML page and a first line
// on every plain-text answer, so no screenshot or capture passes for production.
func markLocalTest(contentType string, body []byte) []byte {
	switch {
	case strings.HasPrefix(contentType, "text/html"):
		out := bytes.Replace(body, []byte("<title>"), []byte("<title>Local test · "), 1)
		at := bytes.Index(out, []byte("<body"))
		if at < 0 {
			return append([]byte(localTestBanner), out...)
		}
		end := bytes.IndexByte(out[at:], '>')
		if end < 0 {
			return append([]byte(localTestBanner), out...)
		}
		cut := at + end + 1
		return append(append(append([]byte(nil), out[:cut]...), localTestBanner...), out[cut:]...)
	case strings.HasPrefix(contentType, "text/plain"):
		return append([]byte(localTestText), body...)
	default:
		return body
	}
}

// localTestAuthorizer admits only the fixed principal, only on the resources
// the production authorizer admits, and only for the bound session reference.
type localTestAuthorizer struct{ principal portal.Principal }

func (a localTestAuthorizer) Authorize(ctx context.Context, p portal.Principal, r portal.Resource) error {
	ref, ok := ctx.Value(sessionContextKey{}).(sessionReference)
	if p != a.principal || !allowedResource(r) || !ok || ref.principal != p {
		return portal.ErrDenied
	}
	return nil
}

// localTestKeys verifies no signature: no ID token is ever accepted, so the
// only session is the seeded one.
type localTestKeys struct{}

func (localTestKeys) VerifySignature(context.Context, string) ([]byte, error) {
	return nil, portal.ErrDenied
}

// localTestCodes has no identity provider: /auth/login answers unavailable.
type localTestCodes struct{}

func (localTestCodes) Binding() CodeBinding {
	return CodeBinding{Issuer: localTestIssuer, ClientID: localTestClientID, Callback: localTestOrigin + "/auth/callback", AuthorizationEndpoint: "https://issuer.localtest.invalid/authorize/"}
}
func (localTestCodes) AuthorizationURL(string, string, string) (string, error) {
	return "", portal.ErrUnavailable
}
func (localTestCodes) Exchange(context.Context, string, string) (codeTokens, error) {
	return codeTokens{}, portal.ErrUnavailable
}

// localTestStore is the in-memory session store of one instance.
type localTestStore struct {
	mu       sync.Mutex
	logins   map[Handle]Login
	sessions map[Handle]Record
}

func (s *localTestStore) PutLogin(_ context.Context, h Handle, v Login) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logins[h] = v
	return nil
}
func (s *localTestStore) TakeLogin(_ context.Context, h Handle, state string, now time.Time) (Login, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.logins[h]
	if !ok || v.State != state || !now.Before(v.ExpiresAt) {
		return Login{}, portal.ErrDenied
	}
	delete(s.logins, h)
	return v, nil
}
func (s *localTestStore) PutSession(_ context.Context, h Handle, v Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[h] = v
	return nil
}
func (s *localTestStore) GetSession(_ context.Context, h Handle) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.sessions[h]
	if !ok {
		return Record{}, portal.ErrDenied
	}
	return v, nil
}
func (s *localTestStore) RevokeSession(_ context.Context, h Handle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, h)
	return nil
}

// localTestOperatorToken has the shape the portal checks (audience, expiry)
// and no signature. Only the in-memory fixture operator accepts it.
func localTestOperatorToken(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"fixture"}`))
	payload, _ := json.Marshal(map[string]any{"aud": apiv1.TokenAudience, "exp": exp.Unix(), "sub": "local-test-fixture"})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".fixture-not-signed"
}

func localTestCertificates(hosts ...string) ([]byte, []tls.Certificate, error) {
	notBefore, notAfter := time.Now().Add(-time.Hour), time.Now().Add(30*24*time.Hour)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, portal.ErrUnavailable
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "portal local test fixture CA"}, NotBefore: notBefore, NotAfter: notAfter, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, nil, portal.ErrUnavailable
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, nil, portal.ErrUnavailable
	}
	var certs []tls.Certificate
	for i, host := range hosts {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, portal.ErrUnavailable
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: notBefore, NotAfter: notAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if err != nil {
			return nil, nil, portal.ErrUnavailable
		}
		certs = append(certs, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key})
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certs, nil
}

// localTestNet routes the pinned production upstream names to in-memory
// fixture servers over net.Pipe. Every other destination is refused, so the
// instance never reaches the operator, OpenProject or Alertmanager.
type localTestNet struct {
	mu      sync.Mutex
	routes  map[string]*pipeListener
	servers []*http.Server
	routed  map[string]int
}

func (n *localTestNet) serve(addr string, cert *tls.Certificate, handler http.Handler) {
	listener := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	n.mu.Lock()
	n.routes[addr] = listener
	n.servers = append(n.servers, server)
	n.mu.Unlock()
	var serving net.Listener = listener
	if cert != nil {
		serving = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12})
	}
	go func() { _ = server.Serve(serving) }()
}

func (n *localTestNet) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	n.mu.Lock()
	listener, ok := n.routes[addr]
	if ok && network == "tcp" {
		if n.routed == nil {
			n.routed = map[string]int{}
		}
		n.routed[addr]++
	}
	n.mu.Unlock()
	if !ok || network != "tcp" {
		return nil, errLocalTestNetwork
	}
	return listener.dial(ctx)
}

func (n *localTestNet) close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, server := range n.servers {
		_ = server.Close()
	}
	for _, listener := range n.routes {
		_ = listener.Close()
	}
}

type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return pipeAddr{} }
func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
	case <-ctx.Done():
	}
	_ = client.Close()
	_ = server.Close()
	return nil, errLocalTestNetwork
}

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "portal-localtest-fixture" }

func localTestJSON(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(body)
}

func localTestOperator(token string, sessions []byte, asks []apiv1.Ask, claims apiv1.PortalTaskClaims) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "fixture operator refused", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v1/sessions":
			localTestJSON(w, mediaJSON, sessions)
		case r.URL.Path == apiv1.PortalAsksPath:
			list := apiv1.AskList{Asks: []apiv1.Ask{}, Complete: true}
			for _, ask := range asks {
				if r.URL.Query().Get(apiv1.AskQueryState) != apiv1.AskStateFilterOpen || ask.State == apiv1.AskStateOpen {
					list.Asks = append(list.Asks, ask)
				}
			}
			raw, _ := json.Marshal(list)
			localTestJSON(w, mediaJSON, raw)
		case strings.HasPrefix(r.URL.Path, apiv1.PortalAsksPath+"/"):
			for _, ask := range asks {
				if r.URL.Path == apiv1.PortalAskPath(ask.ID) {
					raw, _ := json.Marshal(ask)
					localTestJSON(w, mediaJSON, raw)
					return
				}
			}
			http.NotFound(w, r)
		case r.URL.Path == apiv1.PortalTaskClaimsPath:
			raw, _ := json.Marshal(claims)
			localTestJSON(w, mediaJSON, raw)
		default:
			http.NotFound(w, r)
		}
	})
}

func localTestOpenProject(tasks []byte) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+localTestOPToken || r.URL.Path != openProjectProjectPath || r.URL.Query().Get("offset") != "1" {
			http.NotFound(w, r)
			return
		}
		localTestJSON(w, mediaHAL, tasks)
	})
}

const localTestAlerts = `[{"fingerprint":"fixture-alert-01","labels":{"alertname":"Fixture sample alert","severity":"warning","service":"fixture-only"},"annotations":{"summary":"Example alert in the local test instance","description":"Synthetic fixture content. No live alert data is shown."},"status":{"state":"active"},"startsAt":"2026-10-26T12:00:00Z","updatedAt":"2026-10-26T12:01:00Z"},{"fingerprint":"fixture-alert-02","labels":{"alertname":"Fixture sample alert","severity":"critical"},"annotations":{"summary":"Second fixture group"},"status":{"state":"active"},"startsAt":"2026-10-26T11:00:00Z","updatedAt":"2026-10-26T11:30:00Z"}]`

func localTestAlertmanager() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		localTestJSON(w, mediaJSON, []byte(localTestAlerts))
	})
}

func localTestAsks(now time.Time) []apiv1.Ask {
	info := apiv1.Ask{ID: "ask-0000000000000001", Type: apiv1.AskTypeInfo, Question: "Which fixture should the agent use for the next check?", Options: []apiv1.AskOption{{ID: "small", Label: "Small fixture", Description: "A bounded check with synthetic data."}, {ID: "full", Label: "Full fixture", Description: "A larger synthetic sample."}}, Recommended: "small", AllowFreeText: true, Context: apiv1.AskContext{SessionName: "fixture-portal-polish", SessionUID: "00000000-1111-2222-3333-444444444444", WorkPackage: 9101, Links: []string{"https://github.com/thaynes43/dev-env"}}, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(23 * time.Hour), State: apiv1.AskStateOpen}
	approval := info
	approval.ID, approval.Type, approval.Question = "ask-0000000000000002", apiv1.AskTypeApproval, "Approve running the synthetic fixture check?"
	approval.ExpiresAt = now.Add(2 * time.Hour)
	approval.Scope = &apiv1.AskScope{Action: "Run one synthetic fixture check", Target: "Local test fixture only", ExpiresAt: now.Add(2 * time.Hour)}
	answered := approval
	answered.ID, answered.State = "ask-0000000000000003", apiv1.AskStateAnswered
	approved := true
	answered.Answer = &apiv1.AskAnswer{Text: "Use the bounded fixture.", Approved: &approved, AnsweredBy: apiv1.AskActor{Issuer: localTestIssuer, Subject: localTestSubject, DisplayName: "Local test reader"}, AnsweredAt: now.Add(-30 * time.Minute)}
	return []apiv1.Ask{info, approval, answered}
}

func localTestClaims(now time.Time) apiv1.PortalTaskClaims {
	beat := now.Add(-2 * time.Minute)
	item := func(i int, owner, state, freshness string, heartbeat *time.Time) apiv1.PortalTaskClaimItem {
		digit := string(rune('a' + i))
		return apiv1.PortalTaskClaimItem{Name: "task-claim." + strings.Repeat(digit, 32) + "." + strings.Repeat("0", 32), UID: "00000000-0000-4000-8000-00000000000" + string(rune('1'+i)),
			Repository: "dev-env", TaskKey: "issue:" + string(rune('7'+i)), GitHub: "thaynes43/dev-env", CreatedAt: now.Add(-time.Duration(i+1) * time.Hour),
			Owner: apiv1.PortalTaskClaimOwner{Name: owner, UID: "11111111-0000-4000-8000-00000000000" + string(rune('1'+i)), State: state, Freshness: freshness, HeartbeatAt: heartbeat}}
	}
	return apiv1.PortalTaskClaims{Version: apiv1.PortalTaskClaimsVersion, Project: claimsProject, State: apiv1.PortalTaskClaimsStateAvailable, Reason: apiv1.PortalTaskClaimsReasonComplete,
		Items: []apiv1.PortalTaskClaimItem{
			item(0, "fixture-portal-polish", apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimFreshnessObserved, &beat),
			item(1, "fixture-coordinator", apiv1.PortalClaimOwnerIdle, apiv1.PortalClaimFreshnessObserved, &beat),
			item(2, "fixture-retired-agent", apiv1.PortalClaimOwnerNotFound, apiv1.PortalClaimFreshnessUnknown, nil),
		},
		Limit: claimsReadLimit, Complete: true, ObservedAt: now}
}
