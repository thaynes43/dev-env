package session

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/thaynes43/dev-env/web/portal"
)

func TestProductionRejectsPartialMetadataAndCanaries(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	root := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(root, "secrets/OPENPROJECT_TOKEN"), []byte("op-token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(root); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "SECRET-CANARY") {
		t.Fatal("partial metadata mount or canary accepted")
	}
	broken := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(broken, "config/origin"), []byte("https://evil.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(broken); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), "SUBJECT-CANARY") {
		t.Fatal("malformed origin leaked configuration")
	}
}

func TestProductionClosedDatabaseDoesNotServeSecrets(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	root := writeRuntimeDir(t, "SECRET-CANARY")
	rt, err := LoadProduction(root)
	if err != nil {
		t.Fatal("production configuration rejected")
	}
	if strings.Contains(fmt.Sprintf("%v%+v%#v", rt, rt, rt), "SECRET-CANARY") || strings.Contains(fmt.Sprintf("%#v", rt), "SUBJECT-CANARY") {
		t.Fatal("runtime formatting leaked configuration")
	}
	rt.Close()
	w := httptest.NewRecorder()
	rt.Handler.ServeHTTP(w, portalRequest("GET", "/auth/login", ""))
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "SECRET-CANARY") || strings.Contains(w.Body.String(), "SUBJECT-CANARY") {
		t.Fatal("closed database produced a login or a secret")
	}
	w = httptest.NewRecorder()
	health := portalRequest("GET", "/healthz", "")
	rt.Handler.ServeHTTP(w, health)
	if w.Code != http.StatusOK || w.Body.String() != "ok\n" {
		t.Fatal("probe did not stay independent of the database")
	}
}

func TestProductionMetadataAllowlistAndAssembly(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	hits := &pathList{}
	opCert, opCA := mintNamed(t, openProjectHost)
	operatorCert, operatorCA := mintNamed(t, "operator.dev-env.svc")
	issuerCert, issuerCA := mintNamed(t, "authentik.haynesnetwork.com")
	opSrv := serveTLS(t, opCert, openProjectHandler(t, hits))
	operatorSrv := serveTLS(t, operatorCert, operatorHandler(t, hits))
	issuerSrv := serveTLS(t, issuerCert, userInfoHandler(t))
	dial := routeDial(map[string]string{openProjectHost: opSrv, "operator.dev-env.svc": operatorSrv, "authentik.haynesnetwork.com": issuerSrv})
	reader, err := newMetadataReader(metadataConfig{enabled: true, openProjectCA: opCA, operatorCA: operatorCA, openProjectToken: "op-token-canary", operatorToken: "operator-token-canary", operatorBase: "https://operator.dev-env.svc", bindings: testBindings(), timeout: time.Second, now: func() time.Time { return now }}, dial)
	if err != nil {
		t.Fatal("metadata reader rejected")
	}
	if _, err := reader.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal", Task: "124"}); !errors.Is(err, portal.ErrDenied) || hits.len() != 0 {
		t.Fatal("unapproved work package was fetched")
	}
	snapshot, err := reader.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal"})
	if err != nil || snapshot.State != portal.Unknown || len(snapshot.Items) != len(approvedTasks) || snapshot.Items[0].Group != "" || snapshot.Items[0].TaskOutcome != "" || snapshot.Items[0].Title != "Synthetic subject" || snapshot.Items[0].WorkerState != "Running" || strings.Contains(fmt.Sprintf("%#v", snapshot), "SECRET-DESCRIPTION") {
		t.Fatal("allowlisted read invented attention or kept source text")
	}
	if hits.len() != 11 {
		t.Fatalf("allowlisted read made %d calls", hits.len())
	}
	badCA, _ := mintNamed(t, openProjectHost)
	_ = badCA
	_, wrongCA := mintNamed(t, "other.example")
	untrusted, err := newMetadataReader(metadataConfig{enabled: true, openProjectCA: wrongCA, operatorCA: operatorCA, openProjectToken: "op-token-canary", operatorToken: "operator-token-canary", operatorBase: "https://operator.dev-env.svc", bindings: testBindings(), timeout: time.Second, now: func() time.Time { return now }}, dial)
	if err != nil {
		t.Fatal("untrusted reader did not construct")
	}
	if _, err := untrusted.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal", Task: "178"}); !errors.Is(err, portal.ErrUnavailable) || strings.Contains(err.Error(), "SECRET-DESCRIPTION") {
		t.Fatal("untrusted TLS returned metadata")
	}
	hits.prompt = true
	if _, err := reader.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal", Task: "178"}); !errors.Is(err, portal.ErrUnavailable) || strings.Contains(err.Error(), "SECRET-PROMPT") {
		t.Fatal("prompt payload was accepted")
	}

	store := &memoryStore{logins: map[Handle]Login{}, sessions: map[Handle]Record{}}
	rawCookie := base64.RawURLEncoding.EncodeToString(bytes32(7))
	store.sessions[sha256.Sum256([]byte(rawCookie))] = Record{Principal: portal.Principal{Issuer: productionIssuer, Subject: "synthetic-subject"}, CSRF: strings.Repeat("c", 43), CreatedAt: now.Add(-time.Minute), IdleExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour), accessToken: "synthetic-access-token", tokenExpiresAt: now.Add(time.Hour)}
	engine := testEngine(t, store, now)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(issuerCA) {
		t.Fatal("issuer CA rejected")
	}
	authorizer, err := newCurrentAuthorizer(CurrentAuthorizationConfig{Issuer: productionIssuer, Subject: "synthetic-subject", UserInfoURL: productionUserInfo, Store: store, Now: time.Now, Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}, dial)
	if err != nil {
		t.Fatal("authorizer rejected pinned userinfo")
	}
	hits.prompt = false
	handler := productionGate(NewAssembly(AssemblyConfig{Session: engine, Authorizer: authorizer, Reader: reader, FreshFor: time.Minute, Now: time.Now}), productionHost)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, portalRequest("GET", "/auth/login", ""))
	if w.Code != http.StatusSeeOther || strings.Contains(w.Header().Get("Location"), "fixture-only-not-a-credential") || !strings.HasPrefix(w.Header().Get("Location"), productionAuthorization) {
		t.Fatal("login redirect was not the pinned authorization endpoint")
	}
	before := hits.len()
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, portalRequest("GET", LandingPath, rawCookie))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Attention unknown") || !strings.Contains(body, "Synthetic subject") || !strings.Contains(body, "synthetic-session") || !strings.Contains(body, "<dt>Worker liveness</dt><dd>Running</dd>") || !strings.Contains(body, "<dt>Task outcome</dt><dd>Unknown</dd>") || strings.Contains(body, "Verified empty") || strings.Contains(body, "Needs you · ") || strings.Contains(body, "SECRET-DESCRIPTION") || strings.Contains(body, "op-token-canary") || strings.Contains(body, "synthetic-access-token") || strings.Contains(body, "synthetic-subject") {
		t.Fatal("attention response was not an unknown allowlisted read")
	}
	w = httptest.NewRecorder()
	forwarded := portalRequest("GET", LandingPath, rawCookie)
	forwarded.Header.Set("X-Forwarded-User", "synthetic-subject")
	forwarded.Header.Set("X-Forwarded-Host", productionHost)
	handler.ServeHTTP(w, forwarded)
	if w.Code == http.StatusOK || strings.Contains(w.Body.String(), "synthetic-subject") || hits.len() != before+11 {
		t.Fatal("forwarded identity was accepted")
	}
	w = httptest.NewRecorder()
	wrong := portalRequest("GET", LandingPath, rawCookie)
	wrong.Host = "evil.example"
	wrong.Header.Set("X-Forwarded-Host", productionHost)
	wrong.Header.Set("X-Forwarded-Proto", "https")
	handler.ServeHTTP(w, wrong)
	if w.Code == http.StatusOK {
		t.Fatal("forwarded host overrode the request host")
	}
	plain := portalRequest("GET", "/healthz", "")
	plain.TLS = nil
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, plain)
	if w.Code == http.StatusOK {
		t.Fatal("plaintext probe succeeded")
	}
}

func TestMetadataGetUsesGeneratedClaimName(t *testing.T) {
	binding := testBindings()["178"]
	now := time.Now().UTC().Truncate(time.Second)
	opCert, opCA := mintNamed(t, openProjectHost)
	operatorCert, operatorCA := mintNamed(t, "operator.dev-env.svc")
	opSrv := serveTLS(t, opCert, openProjectHandler(t, &pathList{}))
	mode := "match"
	var claimURI string
	operatorSrv := serveTLS(t, operatorCert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer operator-token-canary" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/sessions" && r.URL.RawQuery == "repo=dev-env" {
			body, _ := json.Marshal(map[string]any{"sessions": []any{map[string]any{"uid": binding.sessionUID, "name": binding.session, "repo": "dev-env", "phase": "Running"}}})
			_, _ = w.Write(body)
			return
		}
		claimURI = r.URL.RequestURI()
		if r.URL.Path != "/v1/task-claims/"+binding.claim || r.URL.RawQuery != "project=dev-env" {
			http.NotFound(w, r)
			return
		}
		name, uid, project, repository, key, owner, ownerUID := binding.claim, binding.claimUID, binding.project, binding.repository, binding.taskKey, binding.session, binding.sessionUID
		switch mode {
		case "uid":
			uid = "other-claim-uid"
		case "owner":
			owner = "other-session"
		case "owner-uid":
			ownerUID = "other-session-uid"
		case "key":
			key = "issue:178"
		case "project":
			project = "other"
		case "repository":
			repository = "other"
		case "name":
			name = claimObjectName("dev-env", "dev-env", "issue:178")
		}
		body, _ := json.Marshal(map[string]any{"name": name, "uid": uid, "identity": map[string]string{"project": project, "repository": repository, "taskKey": key}, "owner": map[string]string{"namespace": "dev-agents", "name": owner, "uid": ownerUID}, "createdAt": now.Format(time.RFC3339)})
		_, _ = w.Write(body)
	}))
	dial := routeDial(map[string]string{openProjectHost: opSrv, "operator.dev-env.svc": operatorSrv})
	reader, err := newMetadataReader(metadataConfig{enabled: true, openProjectCA: opCA, operatorCA: operatorCA, openProjectToken: "op-token-canary", operatorToken: "operator-token-canary", operatorBase: "https://operator.dev-env.svc", bindings: testBindings(), timeout: time.Second, now: func() time.Time { return now }}, dial)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal", Task: "178"})
	if err != nil || claimURI != "/v1/task-claims/"+binding.claim+"?project=dev-env" || snapshot.State != portal.Unknown || len(snapshot.Items) != 1 || snapshot.Items[0].WorkPackage != 178 || snapshot.Items[0].Group != "" || snapshot.Items[0].Owner != binding.session || snapshot.Items[0].WorkerState != "Running" {
		t.Fatalf("generated claim was not read from its named endpoint: %s %v", claimURI, err)
	}
	for _, mismatch := range []string{"uid", "owner", "owner-uid", "key", "project", "repository", "name"} {
		mode = mismatch
		if _, err := reader.Read(context.Background(), portal.Resource{Project: "dev-env", Environment: "internal", Task: "178"}); !errors.Is(err, portal.ErrUnavailable) {
			t.Fatalf("mismatched %s claim was accepted", mismatch)
		}
	}
}

func testEngine(t *testing.T, store *memoryStore, now time.Time) *Engine {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewTokenVerifier(VerifierConfig{Issuer: productionIssuer, ClientID: productionClientID, Algorithms: []string{oidc.ES256}, Keys: &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewIssuerHTTPClient(productionIssuer, map[string]string{productionToken: "POST"}, tripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("token endpoint unused")
	}), time.Second, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := NewCodeClient(CodeConfig{Issuer: productionIssuer, ClientID: productionClientID, ClientSecret: "fixture-only-not-a-credential", Callback: productionOrigin + "/auth/callback", AuthorizationURL: productionAuthorization, TokenURL: productionToken, AuthStyle: oauth2.AuthStyleInHeader, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := New(Config{Origin: productionOrigin, Callback: productionOrigin + "/auth/callback", LoginTTL: time.Minute, IdleTTL: 30 * time.Minute, AbsoluteTTL: time.Hour, Now: time.Now, Store: store, Codes: codes, Verifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func fixtureTaskKey(task string) (string, bool) {
	key, ok := map[string]string{
		"120": "task:attention-120",
		"121": "task:attention-121",
		"122": "issue:900122",
		"123": "task:wp.123",
		"178": "issue:501",
	}[task]
	return key, ok
}

func testBindings() map[string]sessionBinding {
	out := make(map[string]sessionBinding, len(approvedTasks))
	for _, task := range approvedTasks {
		key, ok := fixtureTaskKey(task)
		if !ok {
			panic("fixture missing canonical key")
		}
		out[task] = sessionBinding{project: "dev-env", repository: "dev-env", taskKey: key, session: "synthetic-session", sessionUID: "synthetic-session-uid", claim: claimObjectName("dev-env", "dev-env", key), claimUID: "claim-uid-" + task}
	}
	return out
}

type pathList struct {
	mu     sync.Mutex
	paths  []string
	prompt bool
}

func (p *pathList) add(path string) { p.mu.Lock(); p.paths = append(p.paths, path); p.mu.Unlock() }
func (p *pathList) len() int        { p.mu.Lock(); defer p.mu.Unlock(); return len(p.paths) }

func openProjectHandler(t *testing.T, hits *pathList) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.RequestURI())
		if r.Header.Get("Accept") != "application/hal+json" || r.Header.Get("Authorization") != "Bearer op-token-canary" || !strings.HasPrefix(r.URL.Path, "/api/v3/work_packages/") || strings.Contains(r.URL.Path, "/projects/") {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v3/work_packages/")
		body, _ := json.Marshal(map[string]any{"id": json.RawMessage(id), "subject": "Synthetic subject", "updatedAt": time.Now().UTC().Format(time.RFC3339), "description": map[string]string{"raw": "SECRET-DESCRIPTION"}, "_links": map[string]any{"project": map[string]string{"href": "/api/v3/projects/6"}}, "_embedded": map[string]any{"project": map[string]any{"id": 6, "identifier": "haynes-engineering", "_links": map[string]any{"self": map[string]string{"href": "/api/v3/projects/6"}}}}})
		w.Header().Set("Content-Type", "application/hal+json; charset=utf-8")
		_, _ = w.Write(body)
	})
}

func operatorHandler(t *testing.T, hits *pathList) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.add(r.URL.RequestURI())
		if r.Header.Get("Accept") != "application/json" || r.Header.Get("Authorization") != "Bearer operator-token-canary" || r.URL.Path == "/v1/task-claims" || strings.Contains(r.URL.Path, "/log") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/sessions" && r.URL.RawQuery == "repo=dev-env" {
			session := map[string]any{"uid": "synthetic-session-uid", "name": "synthetic-session", "repo": "dev-env", "phase": "Running"}
			if hits.prompt {
				session["prompt"] = "SECRET-PROMPT"
			}
			body, _ := json.Marshal(map[string]any{"sessions": []any{session}})
			_, _ = w.Write(body)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/v1/task-claims/")
		var task string
		for _, candidate := range approvedTasks {
			if testBindings()[candidate].claim == name {
				task = candidate
			}
		}
		key, keyOK := fixtureTaskKey(task)
		if !strings.HasPrefix(r.URL.Path, "/v1/task-claims/") || strings.Contains(name, "/") || r.URL.RawQuery != "project=dev-env" || !keyOK {
			http.NotFound(w, r)
			return
		}
		body, _ := json.Marshal(map[string]any{"name": testBindings()[task].claim, "uid": "claim-uid-" + task, "identity": map[string]string{"project": "dev-env", "repository": "dev-env", "taskKey": key}, "owner": map[string]string{"namespace": "dev-agents", "name": "synthetic-session", "uid": "synthetic-session-uid"}, "createdAt": time.Now().UTC().Format(time.RFC3339)})
		_, _ = w.Write(body)
	})
}

func userInfoHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/application/o/userinfo/" || r.Header.Get("Authorization") != "Bearer synthetic-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"synthetic-subject","portal_is_active":true,"portal_is_admin":true}`))
	})
}

func portalRequest(method, path, cookie string) *http.Request {
	req := httptest.NewRequest(method, productionOrigin+path, nil)
	req.Host = productionHost
	req.TLS = &tls.ConnectionState{HandshakeComplete: true, ServerName: productionHost}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
	}
	return req
}

func bytes32(fill byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = fill
	}
	return raw
}

func writeRuntimeDir(t *testing.T, password string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"config", "secrets", "ca", "tls"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	certPEM, keyPEM := selfSigned(t, productionHost)
	caPEM, _ := selfSigned(t, "fixture-ca.example")
	files := map[string]string{
		"config/origin": productionOrigin, "config/issuer": productionIssuer, "config/authorization-url": productionAuthorization,
		"config/token-url": productionToken, "config/userinfo-url": productionUserInfo, "config/jwks-url": productionJWKS,
		"config/openproject-base-url": openProjectBase, "config/operator-api-url": "https://operator.dev-env.svc",
		"config/admission-api-url": "https://kubernetes.default.svc", "config/admission-uid": "synthetic-admission-uid",
		"config/admission-token-file": filepath.Join(root, "admission-token"), "config/auth-style": "header",
		"config/login-ttl": "1m", "config/idle-ttl": "30m", "config/absolute-ttl": "1h", "config/fresh-for": "1m",
		"config/clock-skew": "0s", "config/witness-gets-per-second": "30", "config/witness-get-burst": "57",
		"secrets/OIDC_CLIENT_ID": productionClientID, "secrets/OIDC_CLIENT_SECRET": "fixture-only-not-a-credential",
		"secrets/DB_USERNAME": productionDBUser, "secrets/DB_PASSWORD": password,
		"secrets/SESSION_AEAD_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "secrets/SESSION_GENERATION": strings.Repeat("g", 32),
		"config/subject": "SUBJECT-CANARY",
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "admission-token"), []byte("synthetic-admission-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ca/ca.crt", "ca/admission-ca.crt", "ca/issuer-ca.crt", "ca/openproject-ca.crt", "ca/operator-ca.crt"} {
		if err := os.WriteFile(filepath.Join(root, name), caPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "tls/tls.crt"), certPEM, 0o600); err != nil || os.WriteFile(filepath.Join(root, "tls/tls.key"), keyPEM, 0o600) != nil {
		t.Fatal("serving certificate write failed")
	}
	return root
}

func selfSigned(t *testing.T, dns string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{dns}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func mintNamed(t *testing.T, dns string) (tls.Certificate, []byte) {
	t.Helper()
	certPEM, keyPEM := selfSigned(t, dns)
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM
}

func serveTLS(t *testing.T, cert tls.Certificate, handler http.Handler) string {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func routeDial(routes map[string]string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		dest, ok := routes[host]
		if err != nil || !ok {
			return nil, errors.New("rejected dial")
		}
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, dest)
	}
}
