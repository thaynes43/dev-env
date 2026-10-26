package session

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/thaynes43/dev-env/internal/tokenfile"
	"github.com/thaynes43/dev-env/web/portal"
)

const (
	ProductionRoot   = "/etc/dev-env-portal"
	productionListen = ":8443"
	productionHost   = "dev-env-v2.haynesops.com"
	productionOrigin = "https://dev-env-v2.haynesops.com"
	productionIssuer = "https://authentik.haynesnetwork.com/application/o/dev-env-v2-portal/"
	// The existing code client pins this issuer-host authorization path. The provider
	// well-known document was not read: that GET returned HTTP 403 and is not retried.
	productionAuthorization = "https://authentik.haynesnetwork.com/application/o/authorize/"
	productionToken         = "https://authentik.haynesnetwork.com/application/o/token/"
	productionUserInfo      = "https://authentik.haynesnetwork.com/application/o/userinfo/"
	productionJWKS          = productionIssuer + "jwks/"
	productionClientID      = "dev-env-v2-portal-internal"
	productionDBUser        = "dev_env_v2_portal_runtime"
	productionDBHost        = "postgres16-rw.database.svc.cluster.local"
)

var errProductionConfig = errors.New("portal production configuration unavailable")

var forwardedIdentity = []string{
	"X-Forwarded-User", "X-Remote-User", "X-Auth-Request-User", "X-Auth-Request-Email",
	"X-Forwarded-Email", "X-Forwarded-Preferred-Username", "X-Forwarded-Groups",
}

// Runtime is one validated production assembly. Formatting never prints configuration.
type Runtime struct {
	Handler   http.Handler
	TLSConfig *tls.Config
	store     *PostgresStore
}

func (Runtime) String() string     { return "[redacted portal runtime]" }
func (r Runtime) GoString() string { return r.String() }

func (r Runtime) Close() {
	if r.store != nil {
		r.store.Close()
	}
}

// LoadProduction validates the mounted contract and wires the existing session,
// authorizer, fence, and metadata reader. Missing metadata tokens disable reads.
// They do not select a fixture reader or the system CA pool.
func LoadProduction(root string) (Runtime, error) {
	if root == "" {
		return Runtime{}, errProductionConfig
	}
	text := func(rel string) (string, error) { return readMountedText(root, rel, 4096) }
	pinned := func(rel, want string) error {
		got, err := text(rel)
		if err != nil || got != want {
			return errProductionConfig
		}
		return nil
	}
	if pinned("config/origin", productionOrigin) != nil || pinned("config/issuer", productionIssuer) != nil || pinned("config/authorization-url", productionAuthorization) != nil || pinned("config/token-url", productionToken) != nil || pinned("config/userinfo-url", productionUserInfo) != nil || pinned("config/jwks-url", productionJWKS) != nil || pinned("config/openproject-base-url", openProjectPortalOrigin) != nil || pinned("config/operator-api-url", operatorPortalOrigin) != nil {
		return Runtime{}, errProductionConfig
	}
	admissionURL, err := text("config/admission-api-url")
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	admissionUID, err := text("config/admission-uid")
	if err != nil || !boundedIdentifier(admissionUID) {
		return Runtime{}, errProductionConfig
	}
	admissionToken, err := text("config/admission-token-file")
	if err != nil || !filepath.IsAbs(admissionToken) {
		return Runtime{}, errProductionConfig
	}
	if _, err := readBearerFile(admissionToken); err != nil {
		return Runtime{}, errProductionConfig
	}
	authStyle, err := text("config/auth-style")
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	style := oauth2.AuthStyleInHeader
	switch authStyle {
	case "header":
	case "params":
		style = oauth2.AuthStyleInParams
	default:
		return Runtime{}, errProductionConfig
	}
	loginTTL, err := durationFile(root, "config/login-ttl", time.Nanosecond, 10*time.Minute)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	absoluteTTL, err := durationFile(root, "config/absolute-ttl", time.Nanosecond, 24*time.Hour)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	idleTTL, err := durationFile(root, "config/idle-ttl", time.Nanosecond, absoluteTTL)
	if err != nil || idleTTL > 24*time.Hour {
		return Runtime{}, errProductionConfig
	}
	freshFor, err := durationFile(root, "config/fresh-for", time.Nanosecond, 15*time.Minute)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	skew, err := durationFile(root, "config/clock-skew", 0, 2*time.Minute)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	perSecondText, err := text("config/witness-gets-per-second")
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	perSecond, err := strconv.ParseFloat(perSecondText, 64)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	burstText, err := text("config/witness-get-burst")
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	burst, err := strconv.Atoi(burstText)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	if _, ok := witnessBudget(perSecond, burst); !ok {
		return Runtime{}, errProductionConfig
	}
	clientID, err := text("secrets/OIDC_CLIENT_ID")
	secret, secretErr := text("secrets/OIDC_CLIENT_SECRET")
	dbUser, userErr := text("secrets/DB_USERNAME")
	password, passwordErr := text("secrets/DB_PASSWORD")
	key, keyErr := text("secrets/SESSION_AEAD_KEY")
	generation, generationErr := text("secrets/SESSION_GENERATION")
	if err != nil || secretErr != nil || userErr != nil || passwordErr != nil || keyErr != nil || generationErr != nil || clientID != productionClientID || dbUser != productionDBUser || !bearerText(secret) || !bearerText(password) {
		return Runtime{}, errProductionConfig
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(key)
	if err != nil || len(decoded) != 32 || !canonicalKey(key) || !validGeneration(generation) {
		return Runtime{}, errProductionConfig
	}
	postgresCA, err := readMountedBytes(root, "ca/ca.crt", 64<<10)
	admissionCA, admissionErr := readMountedBytes(root, "ca/admission-ca.crt", 64<<10)
	issuerCA, issuerErr := readMountedBytes(root, "ca/issuer-ca.crt", 64<<10)
	openProjectCA, openErr := readMountedBytes(root, "ca/openproject-ca.crt", 64<<10)
	operatorCA, operatorErr := readMountedBytes(root, "ca/operator-ca.crt", 64<<10)
	if err != nil || admissionErr != nil || issuerErr != nil || openErr != nil || operatorErr != nil || !pemPool(postgresCA) || !pemPool(admissionCA) || !pemPool(issuerCA) || !pemPool(openProjectCA) || !pemPool(operatorCA) {
		return Runtime{}, errProductionConfig
	}
	certPEM, err := readMountedBytes(root, "tls/tls.crt", 64<<10)
	keyPEM, keyFileErr := readMountedBytes(root, "tls/tls.key", 64<<10)
	if err != nil || keyFileErr != nil {
		return Runtime{}, errProductionConfig
	}
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil || len(certificate.Certificate) == 0 {
		return Runtime{}, errProductionConfig
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || leaf.NotBefore.After(time.Now()) || !leaf.NotAfter.After(time.Now()) || !sanMatch(leaf, productionHost) {
		return Runtime{}, errProductionConfig
	}
	sessions, tasks, err := productSources(root, openProjectCA, operatorCA)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	fence, err := NewKubernetesFence(AdmissionConfig{APIURL: admissionURL, CA: admissionCA, TokenFile: admissionToken, UID: admissionUID, Timeout: 5 * time.Second, WitnessGetsPerSecond: perSecond, WitnessGetBurst: burst})
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	store, err := NewPostgresStore(PostgresConfig{Host: productionDBHost, Username: dbUser, Password: password, CA: postgresCA, Key: key, Generation: generation, Fence: fence, Now: time.Now, Timeout: 5 * time.Second})
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	closeStore := true
	defer func() {
		if closeStore {
			store.Close()
		}
	}()
	issuerClient, err := issuerClient(issuerCA, style, skew)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	verifier, err := NewRemoteTokenVerifier(VerifierConfig{Issuer: productionIssuer, ClientID: clientID, Algorithms: []string{oidc.RS256}, Now: time.Now, ClockSkew: skew}, productionJWKS, issuerClient)
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	codes, err := NewCodeClient(CodeConfig{Issuer: productionIssuer, ClientID: clientID, ClientSecret: secret, Callback: productionOrigin + "/auth/callback", AuthorizationURL: productionAuthorization, TokenURL: productionToken, AuthStyle: style, HTTPClient: issuerClient})
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	engine, err := New(Config{Origin: productionOrigin, Callback: productionOrigin + "/auth/callback", LoginTTL: loginTTL, IdleTTL: idleTTL, AbsoluteTTL: absoluteTTL, Now: time.Now, Store: store, Codes: codes, Verifier: verifier})
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(issuerCA) {
		return Runtime{}, errProductionConfig
	}
	authorizer, err := NewCurrentAuthorizer(CurrentAuthorizationConfig{Issuer: productionIssuer, UserInfoURL: productionUserInfo, Store: store, Now: time.Now, Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}})
	if err != nil {
		return Runtime{}, errProductionConfig
	}
	closeStore = false
	return Runtime{Handler: productionGate(NewAssembly(AssemblyConfig{Session: engine, Authorizer: authorizer, Reader: portal.UnavailableReader{}, Sessions: sessions, Tasks: tasks, FreshFor: freshFor, Now: time.Now}), productionHost), TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"}}, store: store}, nil
}

func productSources(root string, openProjectCA, operatorCA []byte) (*SessionSource, *TaskSource, error) {
	sessions, err := optionalSessionSource(root, operatorCA)
	if err != nil {
		return nil, nil, err
	}
	tasks, err := optionalTaskSource(root, openProjectCA)
	if err != nil {
		return nil, nil, err
	}
	return sessions, tasks, nil
}

func optionalSessionSource(root string, ca []byte) (*SessionSource, error) {
	path := filepath.Join(root, "secrets/OPERATOR_TOKEN")
	present, err := mountedPresent(path)
	if err != nil || !present {
		return nil, err
	}
	if _, err := operatorCredential(path, time.Now()); err != nil {
		return nil, errProductionConfig
	}
	return newSessionSource(path, ca, time.Now, nil)
}

func optionalTaskSource(root string, ca []byte) (*TaskSource, error) {
	path := filepath.Join(root, "secrets/OPENPROJECT_TOKEN")
	present, err := mountedPresent(path)
	if err != nil || !present {
		return nil, err
	}
	if _, err := readBearerFile(path); err != nil {
		return nil, errProductionConfig
	}
	return newTaskSource(path, ca, time.Now, nil)
}

func parseBindings(raw []byte) (map[string]sessionBinding, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var doc struct {
		Schema   int               `json:"schema"`
		Sessions []bindingDocument `json:"sessions"`
	}
	if err := dec.Decode(&doc); err != nil {
		return nil, errProductionConfig
	}
	var extra any
	if dec.Decode(&extra) != io.EOF || doc.Schema != 2 || len(doc.Sessions) != len(approvedTasks) {
		return nil, errProductionConfig
	}
	out := make(map[string]sessionBinding, len(doc.Sessions))
	seenKey := map[string]struct{}{}
	seenClaim := map[string]struct{}{}
	seenClaimUID := map[string]struct{}{}
	sessionUID := map[string]string{}
	sessionName := map[string]string{}
	for _, item := range doc.Sessions {
		if !approvedWorkPackage(item.Task) {
			return nil, errProductionConfig
		}
		binding := sessionBinding{project: item.Project, repository: item.Repository, taskKey: item.TaskKey, session: item.Session, sessionUID: item.SessionUID, claim: item.Claim, claimUID: item.ClaimUID}
		if _, dup := out[item.Task]; dup || !binding.valid() || item.TaskKey == item.Task {
			return nil, errProductionConfig
		}
		if _, dup := seenKey[item.TaskKey]; dup {
			return nil, errProductionConfig
		}
		if _, dup := seenClaim[item.Claim]; dup {
			return nil, errProductionConfig
		}
		if _, dup := seenClaimUID[item.ClaimUID]; dup {
			return nil, errProductionConfig
		}
		if previous, ok := sessionName[item.Session]; ok && previous != item.SessionUID {
			return nil, errProductionConfig
		}
		if previous, ok := sessionUID[item.SessionUID]; ok && previous != item.Session {
			return nil, errProductionConfig
		}
		out[item.Task] = binding
		seenKey[item.TaskKey] = struct{}{}
		seenClaim[item.Claim] = struct{}{}
		seenClaimUID[item.ClaimUID] = struct{}{}
		sessionName[item.Session] = item.SessionUID
		sessionUID[item.SessionUID] = item.Session
	}
	for _, task := range approvedTasks {
		if _, ok := out[task]; !ok {
			return nil, errProductionConfig
		}
	}
	return out, nil
}

func approvedWorkPackage(task string) bool {
	for _, approved := range approvedTasks {
		if task == approved {
			return true
		}
	}
	return false
}

type bindingDocument struct {
	Task       string `json:"task"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	TaskKey    string `json:"taskKey"`
	Session    string `json:"session"`
	SessionUID string `json:"sessionUid"`
	Claim      string `json:"claim"`
	ClaimUID   string `json:"claimUid"`
}

func issuerClient(ca []byte, _ oauth2.AuthStyle, _ time.Duration) (*http.Client, error) {
	base, err := pinnedClient(ca, "authentik.haynesnetwork.com", 5*time.Second, nil)
	if err != nil {
		return nil, errProductionConfig
	}
	return NewIssuerHTTPClient(productionIssuer, map[string]string{productionToken: "POST", productionJWKS: "GET"}, base.Transport, 5*time.Second, 256<<10)
}

func productionGate(next http.Handler, host string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders(w)
		if r.TLS == nil || r.Host != host || (r.URL.Host != "" && r.URL.Host != host) {
			authFailure(w, portal.ErrUnavailable)
			return
		}
		for _, name := range forwardedIdentity {
			if r.Header.Get(name) != "" {
				authFailure(w, portal.ErrDenied)
				return
			}
		}
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.RawPath != "" {
				authFailure(w, portal.ErrUnavailable)
				return
			}
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ServeProduction listens on the fixed TLS port. It returns only a static error.
func ServeProduction(ctx context.Context, root string) error {
	runtime, err := LoadProduction(root)
	if err != nil {
		return errProductionConfig
	}
	defer runtime.Close()
	listener, err := net.Listen("tcp", productionListen)
	if err != nil {
		return errProductionConfig
	}
	server := &http.Server{
		Handler: runtime.Handler, TLSConfig: runtime.TLSConfig, ErrorLog: log.New(logWriter{}, "", 0),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(tls.NewListener(listener, runtime.TLSConfig)) }()
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errProductionConfig
	}
}

type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	_, _ = io.WriteString(os.Stderr, "portal http server error\n")
	return len(p), nil
}

func durationFile(root, rel string, min, max time.Duration) (time.Duration, error) {
	text, err := readMountedText(root, rel, 64)
	if err != nil {
		return 0, errProductionConfig
	}
	value, err := time.ParseDuration(text)
	if err != nil || value < min || value > max {
		return 0, errProductionConfig
	}
	return value, nil
}

func readMountedText(root, rel string, max int64) (string, error) {
	data, err := readMountedBytes(root, rel, max)
	if err != nil {
		return "", err
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) == 0 || bytesHas(data, 0) || bytesHas(data, '\r') {
		return "", errProductionConfig
	}
	text := string(data)
	if strings.TrimSpace(text) != text {
		return "", errProductionConfig
	}
	return text, nil
}

func readMountedBytes(root, rel string, max int64) ([]byte, error) {
	if strings.Contains(rel, "..") {
		return nil, errProductionConfig
	}
	data, err := tokenfile.Read(filepath.Join(root, rel), max)
	if err != nil || len(data) == 0 {
		return nil, errProductionConfig
	}
	return data, nil
}

func mountedPresent(path string) (bool, error) {
	_, err := tokenfile.Read(path, 8192)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errProductionConfig
	}
	return true, nil
}

func pemPool(pem []byte) bool {
	pool := x509.NewCertPool()
	return pool.AppendCertsFromPEM(pem)
}

func sanMatch(leaf *x509.Certificate, host string) bool {
	for _, name := range leaf.DNSNames {
		if name == host {
			return true
		}
	}
	return false
}

func bytesHas(data []byte, needle byte) bool {
	for _, b := range data {
		if b == needle {
			return true
		}
	}
	return false
}
