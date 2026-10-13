// H7 Mac GET-only evidence transport. Raw responses exist only in IPC memory.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const configPath = "/Users/thaynes/.kube/haynes-ops-machine.yaml"
const expectedContext = "haynes-haynes-ops-machine"
const maxRequests = 64
const maxResponse = 2 * 1024 * 1024
const maxTotal = 32 * 1024 * 1024
const maxInput = 16384
const maxHeader = 32768
const runTimeout = 120 * time.Second
const requestTimeout = 5 * time.Second

type query struct {
	Limit    int    `json:"limit"`
	Continue string `json:"continue,omitempty"`
}
type input struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Query *query `json:"query,omitempty"`
}
type receipt struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Started       string `json:"startedAt"`
	Finished      string `json:"finishedAt"`
	Status        int    `json:"httpStatus,omitempty"`
	ResponseBytes int    `json:"responseBytes"`
	Attempt       int    `json:"attempt"`
	TotalBytes    int    `json:"totalResponseBytes"`
	Error         string `json:"error,omitempty"`
}
type output struct {
	Receipt receipt `json:"receipt"`
	Body    []byte  `json:"bodyBase64,omitempty"`
}
type reader struct {
	ctx             context.Context
	base            *url.URL
	token           string
	attempts, total int
	tlsConfig       *tls.Config // Tests substitute a private fixture trust root only.
}

// Finite public allowlist, identical to reviewed source/lineage scope; no version or discovery reads.
var lists = map[string]bool{}
var paths = map[string]bool{}

func init() {
	for _, p := range []string{
		"/apis/policies.kyverno.io/v1/validatingpolicies/dev-env-archive-guard",
		"/apis/policies.kyverno.io/v1/validatingpolicies/dev-env-exec-guard",
		"/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicies/dev-env-identity-guard",
		"/apis/admissionregistration.k8s.io/v1/validatingadmissionpolicybindings/dev-env-identity-guard",
		"/apis/admissionregistration.k8s.io/v1/validatingwebhookconfigurations/kyverno-resource-validating-webhook-cfg",
		"/api/v1/namespaces/dev-agents",
		"/api/v1/namespaces/dev-agents/pods/dev-env-shelf-b5ff85487-7r8vr",
		"/apis/apps/v1/namespaces/dev-agents/deployments/dev-env-shelf",
		"/apis/apps/v1/namespaces/dev-agents/replicasets/dev-env-shelf-66cdfb4755",
		"/apis/apps/v1/namespaces/dev-agents/replicasets/dev-env-shelf-77b4f85d6c",
		"/apis/apps/v1/namespaces/dev-agents/replicasets/dev-env-shelf-b5ff85487",
	} {
		paths[p] = true
	}
	for _, p := range []string{
		"/api/v1/namespaces/dev-agents/pods", "/api/v1/namespaces/dev-agents/replicationcontrollers",
		"/apis/apps/v1/namespaces/dev-agents/deployments", "/apis/apps/v1/namespaces/dev-agents/replicasets",
		"/apis/apps/v1/namespaces/dev-agents/statefulsets", "/apis/apps/v1/namespaces/dev-agents/daemonsets",
		"/apis/batch/v1/namespaces/dev-agents/jobs", "/apis/batch/v1/namespaces/dev-agents/cronjobs",
		"/apis/apps/v1/namespaces/dev-agents/controllerrevisions",
	} {
		paths[p], lists[p] = true, true
	}
}

func configured(ctx context.Context) (*reader, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, errors.New("ConfigUnavailable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(b) > 1024*1024 {
		return nil, errors.New("ConfigRefused")
	}
	return parseConfig(ctx, b)
}

func parseConfig(ctx context.Context, b []byte) (*reader, error) {
	var cfg struct {
		CurrentContext string `yaml:"current-context"`
		Contexts       []struct {
			Name    string            `yaml:"name"`
			Context map[string]string `yaml:"context"`
		} `yaml:"contexts"`
		Clusters []struct {
			Name    string                 `yaml:"name"`
			Cluster map[string]interface{} `yaml:"cluster"`
		} `yaml:"clusters"`
		Users []struct {
			Name string                 `yaml:"name"`
			User map[string]interface{} `yaml:"user"`
		} `yaml:"users"`
	}
	if yaml.Unmarshal(b, &cfg) != nil || cfg.CurrentContext != expectedContext {
		return nil, errors.New("ContextRefused")
	}
	var c map[string]string
	n := 0
	for _, x := range cfg.Contexts {
		if x.Name == expectedContext {
			c = x.Context
			n++
		}
	}
	if n != 1 || c["cluster"] == "" || c["user"] == "" {
		return nil, errors.New("ContextRefused")
	}
	var cluster, user map[string]interface{}
	nc, nu := 0, 0
	for _, x := range cfg.Clusters {
		if x.Name == c["cluster"] {
			cluster = x.Cluster
			nc++
		}
	}
	for _, x := range cfg.Users {
		if x.Name == c["user"] {
			user = x.User
			nu++
		}
	}
	// Only observed existing token/server shape: no plugin, impersonation, proxy,
	// file export, certificate conversion, insecure TLS or discovery.
	if nc != 1 || nu != 1 || len(cluster) != 1 || len(user) != 1 {
		return nil, errors.New("AuthShapeRefused")
	}
	server, sok := cluster["server"].(string)
	token, tok := user["token"].(string)
	if !sok || !tok || token == "" || len(token) > 16384 || strings.ContainsAny(token, "\r\n\t ") {
		return nil, errors.New("AuthShapeRefused")
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("ServerShapeRefused")
	}
	return &reader{ctx: ctx, base: u, token: token, tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12}}, nil
}

func (r *reader) get(in input) output {
	rec := receipt{Method: "GET", Path: in.Path, Started: time.Now().UTC().Format(time.RFC3339Nano), Attempt: r.attempts, TotalBytes: r.total}
	// Unknown path text must not be reflected, even in a failed receipt.
	if !paths[in.Path] {
		rec.Path = ""
	}
	fail := func(code string) output {
		rec.Error = code
		rec.Finished = time.Now().UTC().Format(time.RFC3339Nano)
		return output{Receipt: rec}
	}
	if in.Op != "get" || !paths[in.Path] || (in.Query != nil && (!lists[in.Path] || in.Query.Limit != 32 || len(in.Query.Continue) > 8192)) {
		return fail("RequestShapeRefused")
	}
	if lists[in.Path] && in.Query == nil {
		return fail("ListQueryRequired")
	}
	if r.ctx.Err() != nil || r.attempts >= maxRequests || r.total >= maxTotal {
		return fail("RequestBudgetExhausted")
	}
	ctx, cancel := context.WithTimeout(r.ctx, requestTimeout)
	defer cancel()
	u := *r.base
	u.Path = in.Path
	u.RawPath = ""
	u.RawQuery = ""
	if in.Query != nil {
		q := url.Values{"limit": []string{"32"}}
		if in.Query.Continue != "" {
			q.Set("continue", in.Query.Continue)
		}
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fail("RequestRefused")
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Accept", "application/json")
	// Every call gets a fresh HTTP/1 transport. No reused connection can qualify
	// for net/http's replay path. RoundTrip itself does not follow redirects.
	tr := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: requestTimeout}).DialContext,
		TLSClientConfig: r.tlsConfig, TLSHandshakeTimeout: requestTimeout, ResponseHeaderTimeout: requestTimeout,
		DisableKeepAlives: true, DisableCompression: true, ForceAttemptHTTP2: false,
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}, MaxResponseHeaderBytes: maxHeader}
	defer tr.CloseIdleConnections()
	r.attempts++
	rec.Attempt = r.attempts
	resp, err := tr.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return fail("RequestTimeout")
		}
		return fail("TransportRefused")
	}
	defer resp.Body.Close()
	rec.Status = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		return fail("HTTPStatusRefused")
	}
	if resp.Header.Get("Content-Encoding") != "" {
		return fail("EncodingRefused")
	}
	capBytes := maxResponse
	if maxTotal-r.total < capBytes {
		capBytes = maxTotal - r.total
	}
	if resp.ContentLength > int64(capBytes) {
		return fail("DeclaredResponseBudgetExhausted")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(capBytes)+1))
	rec.ResponseBytes = len(body)
	r.total += len(body)
	rec.TotalBytes = r.total
	if len(body) > capBytes {
		return fail("ResponseBudgetExhausted")
	}
	if err != nil {
		if ctx.Err() != nil {
			return fail("RequestTimeout")
		}
		return fail("ResponseReadRefused")
	}
	if !json.Valid(body) {
		return fail("MalformedJSONResponse")
	}
	rec.Finished = time.Now().UTC().Format(time.RFC3339Nano)
	return output{Receipt: rec, Body: body}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	// Bounds blocked stdin/stdout as well; exit emits no credential/error text.
	go func() { <-ctx.Done(); os.Exit(2) }()
	enc := json.NewEncoder(os.Stdout)
	r, err := configured(ctx)
	if err != nil {
		_ = enc.Encode(output{Receipt: receipt{Error: err.Error()}})
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4096), maxInput)
	for sc.Scan() {
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		var in input
		if err := dec.Decode(&in); err != nil {
			_ = enc.Encode(output{Receipt: receipt{Error: "MalformedRequest"}})
			return
		}
		var extra interface{}
		if dec.Decode(&extra) != io.EOF {
			_ = enc.Encode(output{Receipt: receipt{Error: "MalformedRequest"}})
			return
		}
		out := r.get(in)
		if enc.Encode(out) != nil || out.Receipt.Error != "" {
			return
		}
	}
	if sc.Err() != nil {
		_ = enc.Encode(output{Receipt: receipt{Error: "InputBudgetExhausted"}})
	}
}
