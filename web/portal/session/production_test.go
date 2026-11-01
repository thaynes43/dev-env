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

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/web/portal"
)

func TestProductionRejectsPartialMetadataAndCanaries(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	root := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(root, "secrets/OPENPROJECT_TOKEN"), []byte("op token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(root); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "SECRET-CANARY") {
		t.Fatal("malformed task token was accepted")
	}
	independent := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(independent, "secrets/OPENPROJECT_TOKEN"), []byte("op-token-canary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(independent); err != nil {
		t.Fatal("tasks token without a sessions token was rejected")
	}
	broken := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(broken, "config/origin"), []byte("https://evil.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(broken); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "SECRET-CANARY") || strings.Contains(err.Error(), "SUBJECT-CANARY") {
		t.Fatal("malformed origin leaked configuration")
	}
}

// The recovery witness is gone (Tom, 2026-10-26). Production needs no admission
// input, and inputs a not-yet-updated deployment still mounts are ignored.
func TestProductionLoadsWithoutAdmissionInputs(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	stale := []string{"config/admission-api-url", "config/admission-uid", "config/admission-token-file", "config/witness-gets-per-second", "config/witness-get-burst", "ca/admission-ca.crt"}
	root := writeRuntimeDir(t, "SECRET-CANARY")
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("fixture still mounts %s", name)
		}
	}
	rt, err := LoadProduction(root)
	if err != nil {
		t.Fatal("production required an admission input")
	}
	rt.Close()
	leftover := writeRuntimeDir(t, "SECRET-CANARY")
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(leftover, name), []byte("not-read\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rt, err = LoadProduction(leftover)
	if err != nil {
		t.Fatal("leftover admission inputs were read")
	}
	rt.Close()
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
	authorizer, err := newCurrentAuthorizer(CurrentAuthorizationConfig{Issuer: productionIssuer, UserInfoURL: productionUserInfo, Store: store, Now: time.Now, Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}, dial)
	if err != nil {
		t.Fatal("authorizer rejected pinned userinfo")
	}
	hits.prompt = false
	handler := productionGate(NewAssembly(AssemblyConfig{Session: engine, Authorizer: authorizer, Reader: reader, FreshFor: time.Minute, Now: time.Now}), productionHost)
	disabledSend := portalRequest("POST", portalMessagesPath, rawCookie)
	disabledSend.Header.Set("Origin", productionOrigin)
	disabledSend.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	sendResponse := httptest.NewRecorder()
	handler.ServeHTTP(sendResponse, disabledSend)
	if sendResponse.Code == http.StatusOK {
		t.Fatal("production unexpectedly enabled portal send")
	}
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
		"config/openproject-base-url": openProjectPortalOrigin, "config/operator-api-url": operatorPortalOrigin,
		"config/auth-style": "header",
		"config/login-ttl":  "1m", "config/idle-ttl": "30m", "config/absolute-ttl": "1h", "config/fresh-for": "1m",
		"config/clock-skew":      "0s",
		"secrets/OIDC_CLIENT_ID": productionClientID, "secrets/OIDC_CLIENT_SECRET": "fixture-only-not-a-credential",
		"secrets/DB_USERNAME": productionDBUser, "secrets/DB_PASSWORD": password,
		"secrets/SESSION_AEAD_KEY": base64.StdEncoding.EncodeToString(make([]byte, 32)), "secrets/SESSION_GENERATION": strings.Repeat("g", 32),
	}
	for name, value := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ca/ca.crt", "ca/issuer-ca.crt", "ca/openproject-ca.crt", "ca/operator-ca.crt"} {
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

func TestProductionActionScopes(t *testing.T) {
	root := t.TempDir()
	if got, err := loadActionScopes(root); err != nil || got.Send != nil || got.History != nil || got.Launch != nil {
		t.Fatal("missing action file must leave all actions off")
	}
	if os.MkdirAll(filepath.Join(root, "config"), 0700) != nil {
		t.Fatal("fixture")
	}
	path := filepath.Join(root, "config", "session-actions.json")
	send := `{"targets":[{"name":"agent-a","uid":"uid-a"}]}`
	for _, tc := range []struct {
		value         string
		valid         bool
		send, history bool
	}{
		{`{"send":` + send + `}`, true, true, false}, {`{"history":` + send + `}`, true, false, true}, {`{"send":` + send + `,"history":` + send + `}`, true, true, true},
		{`{}`, false, false, false}, {`{"other":` + send + `}`, false, false, false}, {`{"send":` + send + `,"send":` + send + `}`, false, false, false},
		{`{"Send":` + send + `}`, false, false, false}, {strings.Replace(`{"send":`+send+`}`, `"uid":"uid-a"`, `"uid":"uid-a","UID":"uid-b"`, 1), false, false, false},
		{strings.Replace(`{"send":`+send+`}`, `"uid":"uid-a"`, `"uid":""`, 1), false, false, false},
		{strings.Replace(`{"send":`+send+`}`, `"name":"agent-a"`, `"name":"INVALID"`, 1), false, false, false},
		{`{"send":{"subject":"synthetic-subject","targets":[{"name":"agent-a","uid":"uid-a"}]}}`, false, false, false},
		{`{"send":{"issuer":"` + productionIssuer + `","targets":[{"name":"agent-a","uid":"uid-a"}]}}`, false, false, false},
		{strings.Replace(`{"send":`+send+`}`, `}]`, `},{"name":"agent-a","uid":"uid-b"}]`, 1), false, false, false},
		{strings.Replace(`{"send":`+send+`}`, `}]`, `},{"name":"agent-b","uid":"uid-a"}]`, 1), false, false, false},
	} {
		if os.WriteFile(path, []byte(tc.value), 0600) != nil {
			t.Fatal("fixture write")
		}
		got, err := loadActionScopes(root)
		if (err == nil) != tc.valid || (tc.valid && ((got.Send != nil) != tc.send || (got.History != nil) != tc.history)) {
			t.Fatalf("invalid action config acceptance: %s", tc.value)
		}
	}
}

func TestProductionActionScopesRejectLegacyFilesWithAllActionsOff(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"send", "history", "launch"} {
		for _, legacy := range []string{
			`"issuer":"` + productionIssuer + `","subject":"synthetic-subject",`,
			`"issuer":"` + productionIssuer + `",`,
			`"subject":"synthetic-subject",`,
		} {
			fields := map[string]string{
				"send":    `"targets":[{"name":"agent-a","uid":"uid-a"}]`,
				"history": `"targets":[{"name":"agent-a","uid":"uid-a"}]`,
				"launch":  `"repos":["dev-env"]`,
			}
			fields[action] = legacy + fields[action]
			config := fmt.Sprintf(`{"send":{%s},"history":{%s},"launch":{%s}}`, fields["send"], fields["history"], fields["launch"])
			if err := os.WriteFile(filepath.Join(root, "config/session-actions.json"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := loadActionScopes(root)
			if err == nil || got.Send != nil || got.History != nil || got.Launch != nil {
				t.Fatalf("legacy %s entry did not disable every action", action)
			}
		}
	}
}

func TestProductionActionScopeBounds(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"send", "history", "launch"} {
		limit, key := 64, "targets"
		if action == "launch" {
			limit, key = apiv1.MaxPortalLaunchRepos, "repos"
		}
		for _, count := range []int{0, limit, limit + 1} {
			var items []string
			for i := 0; i < count; i++ {
				item := fmt.Sprintf(`{"name":"a-%d","uid":"u-%d"}`, i, i)
				if action == "launch" {
					item = fmt.Sprintf(`"r-%d"`, i)
				}
				items = append(items, item)
			}
			config := fmt.Sprintf(`{%q:{%q:[%s]}}`, action, key, strings.Join(items, ","))
			if err := os.WriteFile(filepath.Join(root, "config/session-actions.json"), []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := loadActionScopes(root)
			if count == limit {
				if err != nil {
					t.Fatalf("%s maximum scope refused", action)
				}
			} else if err == nil || got.Send != nil || got.History != nil || got.Launch != nil {
				t.Fatalf("%s invalid count %d did not fail closed", action, count)
			}
		}
	}
}

func TestProductionActionConstructor(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	for _, action := range []string{"send", "history"} {
		t.Run(action, func(t *testing.T) {
			root := writeRuntimeDir(t, "SECRET-CANARY")
			config := fmt.Sprintf(`{%q:{"targets":[{"name":"agent-a","uid":"uid-a"}]}}`, action)
			if os.WriteFile(filepath.Join(root, "config/session-actions.json"), []byte(config), 0600) != nil {
				t.Fatal("scope fixture")
			}
			if _, err := LoadProduction(root); err == nil {
				t.Fatal("scope without operator credential enabled")
			}
			token := fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))
			if os.WriteFile(filepath.Join(root, "secrets/OPERATOR_TOKEN"), []byte(token), 0600) != nil {
				t.Fatal("token fixture")
			}
			rt, err := LoadProduction(root)
			if err != nil {
				t.Fatal("scoped constructor rejected")
			}
			defer rt.Close()
			w := httptest.NewRecorder()
			rt.Handler.ServeHTTP(w, portalRequest("POST", portalMessagesPath, ""))
			if w.Code == http.StatusOK {
				t.Fatal("unauthenticated request accepted")
			}
		})
	}
}

func TestProductionLaunchActionScope(t *testing.T) {
	root := t.TempDir()
	if os.MkdirAll(filepath.Join(root, "config"), 0700) != nil {
		t.Fatal("fixture")
	}
	path := filepath.Join(root, "config", "session-actions.json")
	launch := `{"repos":["dev-env","haynes-ops"]}`
	send := `{"targets":[{"name":"agent-a","uid":"uid-a"}]}`
	for _, tc := range []struct {
		name, value               string
		valid, launch, send, hist bool
	}{
		{"launch alone", `{"launch":` + launch + `}`, true, true, false, false},
		{"launch with send and history", `{"launch":` + launch + `,"send":` + send + `,"history":` + send + `}`, true, true, true, true},
		{"send alone is unchanged", `{"send":` + send + `}`, true, false, true, false},
		{"history alone is unchanged", `{"history":` + send + `}`, true, false, false, true},
		{"duplicate launch", `{"launch":` + launch + `,"launch":` + launch + `}`, false, false, false, false},
		{"case alias", `{"Launch":` + launch + `}`, false, false, false, false},
		{"issuer key", `{"launch":{"issuer":"` + productionIssuer + `","repos":["dev-env"]}}`, false, false, false, false},
		{"subject key", `{"launch":{"subject":"synthetic-subject","repos":["dev-env"]}}`, false, false, false, false},
		{"missing repos", `{"launch":{}}`, false, false, false, false},
		{"targets instead of repos", `{"launch":{"targets":[{"name":"a","uid":"b"}]}}`, false, false, false, false},
		{"extra key", strings.Replace(`{"launch":`+launch+`}`, `"repos"`, `"profile":"full","repos"`, 1), false, false, false, false},
		{"case alias key", strings.Replace(`{"launch":`+launch+`}`, `"repos"`, `"Repos"`, 1), false, false, false, false},
		{"empty repos", strings.Replace(`{"launch":`+launch+`}`, `["dev-env","haynes-ops"]`, `[]`, 1), false, false, false, false},
		{"null repos", strings.Replace(`{"launch":`+launch+`}`, `["dev-env","haynes-ops"]`, `null`, 1), false, false, false, false},
		{"duplicate repo", strings.Replace(`{"launch":`+launch+`}`, `"haynes-ops"`, `"dev-env"`, 1), false, false, false, false},
		{"repo with a slash", strings.Replace(`{"launch":`+launch+`}`, `"haynes-ops"`, `"a/b"`, 1), false, false, false, false},
		{"repo dot dot", strings.Replace(`{"launch":`+launch+`}`, `"haynes-ops"`, `".."`, 1), false, false, false, false},
		{"repo empty", strings.Replace(`{"launch":`+launch+`}`, `"haynes-ops"`, `""`, 1), false, false, false, false},
		{"repo not a string", strings.Replace(`{"launch":`+launch+`}`, `"haynes-ops"`, `7`, 1), false, false, false, false},
		{"seventeen repos", `{"launch":{"repos":["r1","r2","r3","r4","r5","r6","r7","r8","r9","r10","r11","r12","r13","r14","r15","r16","r17"]}}`, false, false, false, false},
		{"launch is a list", `{"launch":[]}`, false, false, false, false},
	} {
		if os.WriteFile(path, []byte(tc.value), 0600) != nil {
			t.Fatal("fixture write")
		}
		got, err := loadActionScopes(root)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: acceptance %v", tc.name, err == nil)
		}
		if tc.valid && ((got.Launch != nil) != tc.launch || (got.Send != nil) != tc.send || (got.History != nil) != tc.hist) {
			t.Fatalf("%s: scopes %+v", tc.name, got)
		}
		if tc.valid && tc.launch && (strings.Join(got.Launch.Repos, ",") != "dev-env,haynes-ops") {
			t.Fatalf("%s: launch scope %+v", tc.name, got.Launch)
		}
	}
}

func TestProductionLaunchConstructor(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	root := writeRuntimeDir(t, "SECRET-CANARY")
	config := `{"launch":{"repos":["dev-env"]}}`
	if os.WriteFile(filepath.Join(root, "config/session-actions.json"), []byte(config), 0600) != nil {
		t.Fatal("scope fixture")
	}
	if _, err := LoadProduction(root); err == nil {
		t.Fatal("a launch scope without an operator credential enabled")
	}
	token := fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))
	if os.WriteFile(filepath.Join(root, "secrets/OPERATOR_TOKEN"), []byte(token), 0600) != nil {
		t.Fatal("token fixture")
	}
	rt, err := LoadProduction(root)
	if err != nil {
		t.Fatal("scoped constructor rejected")
	}
	defer rt.Close()
	for _, method := range []string{"POST", "GET"} {
		w := httptest.NewRecorder()
		rt.Handler.ServeHTTP(w, portalRequest(method, portalLaunchesPath, ""))
		if w.Code == http.StatusOK || w.Code == http.StatusSeeOther {
			t.Fatalf("unauthenticated %s accepted", method)
		}
	}
}

func TestProductionQuestionAuditWiring(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	absent := writeRuntimeDir(t, "SECRET-CANARY")
	rt, err := LoadProduction(absent)
	if err != nil || rt.questionAudit != "" {
		t.Fatalf("absent config err=%v project=%q", err, rt.questionAudit)
	}
	rt.Close()
	enabled := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(enabled, "config/question-audit"), []byte("sample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(enabled); err == nil {
		t.Fatal("question audit without an operator credential connected")
	}
	token := fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(enabled, "secrets/OPERATOR_TOKEN"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err = LoadProduction(enabled)
	if err != nil || rt.questionAudit != "sample" {
		t.Fatalf("enabled fixture err=%v project=%q", err, rt.questionAudit)
	}
	if strings.Contains(fmt.Sprintf("%v%+v%#v", rt, rt, rt), "sample") {
		t.Fatal("runtime formatting exposed the project")
	}
	rt.Close()
	for _, body := range []string{"Sample\n", "*\n", "sample,other\n", "\n", "sample\n\n", " sample\n"} {
		root := writeRuntimeDir(t, "SECRET-CANARY")
		if err := os.WriteFile(filepath.Join(root, "config/question-audit"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadProduction(root); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestProductionCurrentSourceConfigFailsClosed(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGSSLCERT", "PGSSLKEY", "PGSSLROOTCERT", "PGSSLCRL", "PGSSLCRLDIR"} {
		t.Setenv(name, "")
	}
	secret := []byte(`{"slot":"codex-current-source","SECRET-CONFIG":true}` + "\n")
	root := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(root, currentSourceConfigRel), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(root); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("malformed current source: %v", err)
	}
	valid := currentSourceConfigDocument("synthetic-january-2026", 1, 1800) + "\n"
	noToken := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(noToken, currentSourceConfigRel), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadProduction(noToken); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "synthetic-january-2026") || strings.Contains(err.Error(), "SECRET-CANARY") {
		t.Fatalf("config without an operator client: %v", err)
	}
	enabled := writeRuntimeDir(t, "SECRET-CANARY")
	if err := os.WriteFile(filepath.Join(enabled, currentSourceConfigRel), []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	token := fixtureOperatorToken(apiv1.TokenAudience, time.Now().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(enabled, "secrets/OPERATOR_TOKEN"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	rt, err := LoadProduction(enabled)
	if err != nil {
		t.Fatal("optional current-source file was rejected")
	}
	if strings.Contains(fmt.Sprintf("%v%+v%#v", rt, rt, rt), "synthetic-january-2026") || strings.Contains(fmt.Sprintf("%#v", rt), "SECRET-CANARY") {
		t.Fatal("runtime formatting exposed the current-source selection")
	}
	rt.Close()
	for _, raw := range []string{
		`{"slot":"codex-current-source","slot":"other","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":1800}`,
		`{"slot":"other-slot","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":1800}`,
		`{"slot":"codex-current-source","publisherRef":"other-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":1800}`,
		`{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"has space","sourceGeneration":1,"maxAgeSeconds":1800}`,
		`{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":0,"maxAgeSeconds":1800}`,
		`{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":0}`,
		`{"slot":"codex-current-source","publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":86401}`,
		`{"publisherRef":"codex-get-usage-limits-observation-publisher","producerIncarnation":"synthetic-january-2026","sourceGeneration":1,"maxAgeSeconds":1800}`,
		currentSourceConfigDocument("synthetic-january-2026", 1, 1800) + `{"extra":true}`,
	} {
		if _, err := decodeCurrentSourceConfig([]byte(raw)); !errors.Is(err, errProductionConfig) || strings.Contains(err.Error(), "synthetic") || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("accepted or leaked %s: %v", raw, err)
		}
	}
	ceiling, err := decodeCurrentSourceConfig([]byte(currentSourceConfigDocument("synthetic-january-2026", 1, 86400)))
	if err != nil || ceiling.MaxAge != 24*time.Hour || ceiling.SourceGeneration != 1 {
		t.Fatalf("24h selection %+v %v", ceiling, err)
	}
	if err := attachCurrentSourceConfig(t.TempDir(), nil); err != nil {
		t.Fatal("absent file with no session failed")
	}
}
