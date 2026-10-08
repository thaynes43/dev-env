package agentrun

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	operatorNamespace = "dev-env-system"
	operatorService   = "dev-env-operator"
	humanAccount      = "dev-env-human"
	apiCAConfigMap    = "dev-env-api-ca"
)

// kubectlClient captures the context once. Every subsequent operation, including
// attach/detach, uses that same context even if current-context later changes.
type kubectlClient struct {
	env    Env
	prefix []string
}

func (a *app) kubectl(ctx context.Context, o connOpts) (*kubectlClient, error) {
	if a.kube != nil {
		return a.kube, nil
	}
	path, err := a.env.LookPath("kubectl")
	if err != nil {
		return nil, fail(ExitAuth, "laptop access needs kubectl on PATH and an existing kubeconfig; explicit API credentials use --api-url and --token-file")
	}
	if a.env.Output == nil {
		return nil, fail(ExitAuth, "kubectl output is unavailable")
	}
	prefix := []string{path}
	if o.kubeconfig != "" {
		prefix = append(prefix, "--kubeconfig", o.kubeconfig)
	}
	k := &kubectlClient{env: a.env, prefix: prefix}
	contextName := o.context
	if contextName == "" {
		raw, err := k.output(ctx, "read the kubeconfig's current context", "config", "current-context")
		if err != nil {
			return nil, err
		}
		contextName = strings.TrimSpace(string(raw))
		if contextName == "" || strings.ContainsAny(contextName, "\r\n") {
			return nil, fail(ExitAuth, "the kubeconfig has no single current context; choose one with --context")
		}
	}
	k.prefix = append(k.prefix, "--context", contextName)
	a.kube = k
	return k, nil
}

func (k *kubectlClient) argv(args ...string) []string {
	return append(append([]string(nil), k.prefix...), args...)
}

func (k *kubectlClient) output(ctx context.Context, operation string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	argv := k.argv(append([]string{"--request-timeout=30s"}, args...)...)
	raw, err := k.env.Output(bounded, argv)
	if err != nil {
		// Never include subprocess errors or output: a TokenRequest response may
		// contain a credential, including when a kubectl auth plugin fails.
		if ctx.Err() != nil {
			return nil, fail(ExitFailed, "kubectl could not %s: stopped", operation)
		}
		return nil, fail(ExitAuth, "kubectl could not %s; check the selected kubeconfig, cluster access and permissions", operation)
	}
	return raw, nil
}

func (k *kubectlClient) apiCA(ctx context.Context) (*x509.CertPool, error) {
	raw, err := k.output(ctx, "read the pinned API CA", "get", "configmap", apiCAConfigMap, "-n", SessionNamespace, "-o", "json")
	if err != nil {
		return nil, err
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &cm); err != nil {
		return nil, fail(ExitAuth, "kubectl answered the pinned API CA request with invalid JSON")
	}
	// The laptop's automatic path trusts only this dedicated, GitOps-pinned CA.
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cm.Data["ca.crt"])) {
		return nil, fail(ExitAuth, "ConfigMap %s/%s holds no PEM CA certificate in ca.crt", SessionNamespace, apiCAConfigMap)
	}
	return pool, nil
}

// humanToken keeps a ten-minute, audience-scoped token in memory and re-mints
// after five minutes, as the in-pod minter does. No token goes to a file or argv.
type humanToken struct {
	kube *kubectlClient
	env  Env
	mu   sync.Mutex
	tok  string
	at   time.Time
}

func (m *humanToken) Describe() string {
	return fmt.Sprintf("a token minted with the kubeconfig for %s/%s", operatorNamespace, humanAccount)
}

func (m *humanToken) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tok != "" && m.env.Now().Sub(m.at) < mintReuse {
		return m.tok, nil
	}
	raw, err := m.kube.output(ctx, "mint the human API token", "create", "token", humanAccount, "-n", operatorNamespace,
		"--audience", apiv1.TokenAudience, "--duration=600s")
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" || strings.ContainsAny(tok, "\r\n\t ") {
		return "", fail(ExitAuth, "kubectl returned no single token for %s/%s", operatorNamespace, humanAccount)
	}
	m.tok, m.at = tok, m.env.Now()
	return m.tok, nil
}

func (a *app) forwardAPI(ctx context.Context, k *kubectlClient) (string, error) {
	if a.env.PortForward == nil {
		return "", fail(ExitFailed, "kubectl port-forward is unavailable")
	}
	address, stop, err := a.env.PortForward(ctx, k.argv("port-forward", "-n", operatorNamespace,
		"service/"+operatorService, ":8443", "--address=127.0.0.1"))
	if err != nil {
		// A helper may have acquired a process before discovering a failure.
		if stop != nil {
			stop()
		}
		if ctx.Err() != nil {
			return "", fail(ExitFailed, "kubectl port-forward to the operator API: stopped")
		}
		return "", fail(ExitRetry, "kubectl port-forward to the operator API did not become ready within 30s; check cluster access and port-forward permissions")
	}
	a.cleanups = append(a.cleanups, stop)
	return address, nil
}
