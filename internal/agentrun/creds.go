package agentrun

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Where agent-run finds the API and a token for it (D-06, D-50). A session
// pod has both from the operator; another pod in the cluster (the v1 pod until
// cutover) mints a token for its own ServiceAccount; a laptop uses kubectl and
// its existing kubeconfig for a human token and a verified TLS port-forward.
const (
	// DefaultAPIURL is the API's Service by its full name: session pods use
	// ndots:1 and the DNS allowlist refuses search-expanded names (D-46).
	DefaultAPIURL = "https://dev-env-operator.dev-env-system.svc.cluster.local:8443"
	// DefaultSessionTokenFile is a session pod's projected token for the
	// audience dev-env-operator (D-41).
	DefaultSessionTokenFile = "/var/run/secrets/dev-env/token"
	// DefaultServiceAccountDir is the kubelet's mount of a pod's own
	// ServiceAccount token.
	DefaultServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// The environment variables agent-run reads. The DEV_ENV_API_* ones override
// what it finds; the AGENTD_API_* ones are the session pod's (D-41).
const (
	envAPIURL          = "DEV_ENV_API_URL"
	envTokenFile       = "DEV_ENV_API_TOKEN_FILE"
	envCAFile          = "DEV_ENV_API_CA_FILE"
	envAgentdURL       = "AGENTD_API_URL"
	envAgentdTokenFile = "AGENTD_API_TOKEN_FILE"
	envAgentdCAFile    = "AGENTD_API_CA_FILE"
	envKubeHost        = "KUBERNETES_SERVICE_HOST"
	envKubePort        = "KUBERNETES_SERVICE_PORT"
)

// A minted token lives ten minutes, the shortest a TokenRequest allows, and is
// minted again after five, so a long --wait never sends an expired one.
const (
	mintSeconds = 600
	mintReuse   = 5 * time.Minute
)

// requestTimeout bounds one request to the API or to the Kubernetes API.
const requestTimeout = 30 * time.Second

// tokenSource yields the bearer token for each request.
type tokenSource interface {
	Token(ctx context.Context) (string, error)
	// Describe names the source in messages: never the token.
	Describe() string
}

// conn is how this run reaches the API.
type conn struct {
	url   string
	token tokenSource
	http  *http.Client
}

// connOpts are the connection flags.
type connOpts struct {
	apiURL, tokenFile, caFile string
	kubeconfig, context       string
}

// connect finds the API's address, a token and the CA to trust.
func (a *app) connect(ctx context.Context, o connOpts) (*conn, error) {
	env := a.env
	inCluster := env.Getenv(envKubeHost) != "" && exists(filepath.Join(env.ServiceAccountDir, "token"))
	sessionTokenFile := env.Getenv(envAgentdTokenFile)
	if sessionTokenFile == "" && exists(env.SessionTokenFile) {
		sessionTokenFile = env.SessionTokenFile
	}
	selectedKube := o.kubeconfig != "" || o.context != ""
	// A kube selector must never move only exec to another cluster while API
	// discovery or token minting still uses this pod's cluster. A fully explicit
	// URL and token remain the caller's manual connection.
	explicitURL := firstOf(o.apiURL, env.Getenv(envAPIURL))
	explicitToken := firstOf(o.tokenFile, env.Getenv(envTokenFile))
	if selectedKube && (inCluster || sessionTokenFile != "") && (explicitURL == "" || explicitToken == "") {
		return nil, usageError("--context and --kubeconfig cannot select another cluster while using this pod's API or identity; omit them, or give an explicit --api-url and --token-file for that cluster")
	}

	base := firstOf(o.apiURL, env.Getenv(envAPIURL), env.Getenv(envAgentdURL))
	laptop := !inCluster && sessionTokenFile == ""
	forward := laptop && base == ""
	if base == "" && (inCluster || sessionTokenFile != "") {
		base = DefaultAPIURL
	}
	if forward {
		base = DefaultAPIURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, usageError("the API address %q is not an https:// URL with a host (the API serves HTTPS only)", base)
	}
	base = strings.TrimSuffix(base, "/")

	var tok tokenSource
	switch {
	case o.tokenFile != "":
		tok = &fileToken{path: o.tokenFile, from: "from --token-file"}
	case env.Getenv(envTokenFile) != "":
		tok = &fileToken{path: env.Getenv(envTokenFile), from: "from " + envTokenFile}
	case sessionTokenFile != "":
		tok = &fileToken{path: sessionTokenFile, from: "this session pod's projected token"}
	case inCluster:
		m, err := newMinter(env)
		if err != nil {
			return nil, err
		}
		tok = m
	default:
		k, err := a.kubectl(ctx, o)
		if err != nil {
			return nil, err
		}
		tok = &humanToken{kube: k, env: env}
	}

	caFile := firstOf(o.caFile, env.Getenv(envCAFile), env.Getenv(envAgentdCAFile))
	if selectedKube && (inCluster || sessionTokenFile != "") {
		caFile = firstOf(o.caFile, env.Getenv(envCAFile))
	}
	tlsConf := &tls.Config{MinVersion: tls.VersionTLS13}
	if caFile != "" {
		pool, err := certPool(caFile)
		if err != nil {
			return nil, fail(ExitAuth, "%v", err)
		}
		tlsConf.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if forward {
		k, err := a.kubectl(ctx, o)
		if err != nil {
			return nil, err
		}
		if caFile == "" {
			pool, err := k.apiCA(ctx)
			if err != nil {
				return nil, err
			}
			tlsConf.RootCAs = pool
		}
		// Fail to mint/read a token before opening a long-lived subprocess.
		if _, err := tok.Token(ctx); err != nil {
			return nil, err
		}
		base, err = a.forwardAPI(ctx, k)
		if err != nil {
			return nil, err
		}
		tlsConf.ServerName = u.Hostname()
		tr.Proxy = nil
	}
	tr.TLSClientConfig = tlsConf
	a.cleanups = append(a.cleanups, tr.CloseIdleConnections)
	return &conn{url: base, token: tok, http: &http.Client{Transport: tr, Timeout: requestTimeout}}, nil
}

// certPool is the system roots plus the PEM certificates in file.
func certPool(file string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("the CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("the CA file %s holds no PEM certificate", file)
	}
	return pool, nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// fileToken reads a token file before each request: the kubelet rotates a
// projected token in place.
type fileToken struct {
	path string
	from string
}

func (f *fileToken) Token(context.Context) (string, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fail(ExitAuth, "no token at %s (%s)", f.path, f.from)
		}
		return "", fail(ExitAuth, "the token at %s (%s): %v", f.path, f.from, err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fail(ExitAuth, "the token file %s (%s) is empty", f.path, f.from)
	}
	return tok, nil
}

func (f *fileToken) Describe() string {
	return fmt.Sprintf("the token at %s (%s)", f.path, f.from)
}

// minter mints a token for the pod's own ServiceAccount with a TokenRequest,
// as `kubectl create token <sa> --audience dev-env-operator` does. The v1 pod
// calls the API this way until cutover; its RBAC allows create on
// serviceaccounts/token for its own ServiceAccount only (D-46).
type minter struct {
	env       Env
	server    string
	namespace string
	name      string
	http      *http.Client

	mu  sync.Mutex
	tok string
	at  time.Time
}

func newMinter(env Env) (*minter, error) {
	dir := env.ServiceAccountDir
	own, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return nil, fail(ExitAuth, "the pod's ServiceAccount token: %v", err)
	}
	ns, name, err := serviceAccountOf(strings.TrimSpace(string(own)))
	if err != nil {
		return nil, fail(ExitAuth, "the pod's ServiceAccount token at %s: %v", filepath.Join(dir, "token"), err)
	}
	pool, err := certPool(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, fail(ExitAuth, "the cluster's CA: %v", err)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	port := firstOf(env.Getenv(envKubePort), "443")
	return &minter{
		env:       env,
		server:    "https://" + net.JoinHostPort(env.Getenv(envKubeHost), port),
		namespace: ns,
		name:      name,
		http:      &http.Client{Transport: tr, Timeout: requestTimeout},
	}, nil
}

func (m *minter) Describe() string {
	return fmt.Sprintf("a token minted for this pod's ServiceAccount %s/%s", m.namespace, m.name)
}

func (m *minter) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok != "" && m.env.Now().Sub(m.at) < mintReuse {
		return m.tok, nil
	}
	own, err := os.ReadFile(filepath.Join(m.env.ServiceAccountDir, "token"))
	if err != nil {
		return "", fail(ExitAuth, "the pod's ServiceAccount token: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"apiVersion": "authentication.k8s.io/v1",
		"kind":       "TokenRequest",
		"spec":       map[string]any{"audiences": []string{apiv1.TokenAudience}, "expirationSeconds": mintSeconds},
	})
	u := fmt.Sprintf("%s/api/v1/namespaces/%s/serviceaccounts/%s/token", m.server, url.PathEscape(m.namespace), url.PathEscape(m.name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(own)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := m.http.Do(req)
	if err != nil {
		return "", fail(ExitRetry, "could not reach the Kubernetes API at %s to mint a token: %v", m.server, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		var st struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &st)
		msg := firstOf(st.Message, http.StatusText(resp.StatusCode))
		code := ExitFailed
		switch {
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			code = ExitAuth
			msg += fmt.Sprintf("; the ServiceAccount needs create on serviceaccounts/token for itself (resourceNames: [%s]) to call the dev-env API (D-46)", m.name)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			code = ExitRetry
		}
		return "", fail(code, "the Kubernetes API refused to mint a token for %s/%s (%d): %s", m.namespace, m.name, resp.StatusCode, msg)
	}
	var tr struct {
		Status struct {
			Token string `json:"token"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil || tr.Status.Token == "" {
		return "", fail(ExitFailed, "the Kubernetes API answered a TokenRequest for %s/%s with no token", m.namespace, m.name)
	}
	m.tok, m.at = tr.Status.Token, m.env.Now()
	return m.tok, nil
}

// serviceAccountOf reads the namespace and name from a ServiceAccount token's
// sub claim, system:serviceaccount:<namespace>:<name>. It does not verify the
// token: the API server does that when the token asks for one of its own.
func serviceAccountOf(token string) (string, string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", "", errors.New("its payload is not base64url")
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", errors.New("its payload is not JSON")
	}
	rest, ok := strings.CutPrefix(claims.Sub, "system:serviceaccount:")
	ns, name, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || ns == "" || name == "" || strings.Contains(name, ":") {
		return "", "", fmt.Errorf("its subject %q is not a ServiceAccount's", claims.Sub)
	}
	return ns, name, nil
}
