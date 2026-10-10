package projectsync

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// NewReader accepts only the in-cluster Service address, public CA and explicit
// projected API token. There is no kubeconfig, host credential, proxy, retry,
// list/watch, mutation or redirect route. API failures never include raw bodies,
// server addresses or bearer material in public errors.
func NewReader(host, port, tokenFile, caFile string) (Reader, error) {
	p, err := strconv.ParseUint(port, 10, 16)
	if net.ParseIP(host) == nil || err != nil || p == 0 || tokenFile != APITokenFile || caFile != APICAFile {
		return nil, errors.New("sync requires its explicit projected Kubernetes identity and Service address")
	}
	ca, err := os.ReadFile(caFile)
	if err != nil {
		return nil, errors.New("sync public API CA is unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("sync public API CA is invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second}
	return &apiReader{client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, endpoint: "https://" + net.JoinHostPort(host, port), tokenFile: tokenFile}, nil
}

type apiReader struct {
	client              *http.Client
	endpoint, tokenFile string
}

func (r *apiReader) named(ctx context.Context, prefix, namespace, resource, name string, out any) error {
	token, err := os.ReadFile(r.tokenFile)
	if err != nil || len(token) == 0 || len(token) > 64<<10 {
		return errors.New("sync API identity is unavailable")
	}
	value := strings.TrimSpace(string(token))
	if value == "" || strings.ContainsAny(value, "\r\n\t ") {
		return errors.New("sync API identity is invalid")
	}
	path := prefix + "/namespaces/" + url.PathEscape(namespace) + "/" + resource + "/" + url.PathEscape(name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint+path, nil)
	if err != nil {
		return errors.New("sync named API read is invalid")
	}
	request.Header.Set("Authorization", "Bearer "+value)
	response, err := r.client.Do(request)
	if err != nil {
		return errors.New("sync named API read is unavailable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return errors.New("sync named API read is unconfirmed")
	}
	const maxResource = 2 << 20
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResource+1))
	if err != nil || len(data) > maxResource || json.Unmarshal(data, out) != nil {
		return errors.New("sync named API resource is invalid")
	}
	return nil
}

func (r *apiReader) Pod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	var resource corev1.Pod
	if err := r.named(ctx, "/api/v1", namespace, "pods", name, &resource); err != nil {
		return nil, err
	}
	return &resource, nil
}
func (r *apiReader) Job(ctx context.Context, namespace, name string) (*batchv1.Job, error) {
	var resource batchv1.Job
	if err := r.named(ctx, "/apis/batch/v1", namespace, "jobs", name, &resource); err != nil {
		return nil, err
	}
	return &resource, nil
}
func (r *apiReader) Catalog(ctx context.Context, namespace, name string) (*corev1.ConfigMap, error) {
	var resource corev1.ConfigMap
	if err := r.named(ctx, "/api/v1", namespace, "configmaps", name, &resource); err != nil {
		return nil, err
	}
	return &resource, nil
}
