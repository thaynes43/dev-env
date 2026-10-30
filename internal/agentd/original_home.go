package agentd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/homeguard"
	"github.com/thaynes43/dev-env/internal/tokenfile"
)

const homeServiceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// VerifyOriginalHome uses only the existing in-cluster read identity. Never use
// HOME kubeconfig, active grants, proxies, redirects, native RPC or provider data.
// Empty binding is the compatibility path and performs no I/O.
func VerifyOriginalHome(ctx context.Context) error {
	raw := os.Getenv(homeguard.Env)
	if raw == "" {
		return nil
	}
	b, err := homeguard.Parse(raw)
	if err != nil {
		return homeguard.ErrUnsafe
	}
	ca, err := os.ReadFile(homeServiceAccountDir + "/ca.crt")
	if err != nil {
		return homeguard.ErrUnsafe
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return homeguard.ErrUnsafe
	}
	token, err := tokenfile.Read(homeServiceAccountDir+"/token", tokenfile.MaxBytes)
	if err != nil {
		return homeguard.ErrUnsafe
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer tr.CloseIdleConnections()
	h := &http.Client{Transport: tr, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return homeguard.ErrUnsafe }}
	return verifyOriginalHomeAPI(ctx, h, "https://kubernetes.default.svc", string(token), *b)
}

func verifyOriginalHomeAPI(ctx context.Context, h *http.Client, base, token string, b homeguard.Binding) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	paths := []string{"/apis/dev-env.haynesops.com/v1alpha1/namespaces/" + url.PathEscape(b.Namespace) + "/agentsessions/" + url.PathEscape(b.Session), "/api/v1/namespaces/" + url.PathEscape(b.Namespace) + "/persistentvolumeclaims/" + url.PathEscape(b.Claim()), "/api/v1/persistentvolumes/" + url.PathEscape(b.PV)}
	var bodies [3][]byte
	for i, path := range paths {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return homeguard.ErrUnsafe
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := h.Do(req)
		if err != nil {
			return homeguard.ErrUnsafe
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, (256<<10)+1))
		_ = resp.Body.Close()
		if readErr != nil || len(data) > 256<<10 || resp.StatusCode != http.StatusOK {
			return homeguard.ErrUnsafe
		}
		bodies[i] = data
	}
	// Admission annotation must still match the Pod's immutable configured tuple.
	var s struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
	}
	if json.Unmarshal(bodies[0], &s) != nil {
		return homeguard.ErrUnsafe
	}
	bound, err := homeguard.Select(nil, b.Namespace, b.Session, b.SessionUID, s.Metadata.Annotations)
	if err != nil || bound == nil || *bound != b {
		return homeguard.ErrUnsafe
	}
	return b.Verify(bodies[0], bodies[1], bodies[2], true)
}
