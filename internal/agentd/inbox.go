package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	InboxMaxRequestBytes  = 6*(16<<10) + 4096
	InboxMaxResponseBytes = 1 << 20
)

// InboxClient performs one explicit API action and reads no provider files.
// It never follows a redirect or retries a publication/acknowledgement.
type InboxClient struct {
	URL       string
	TokenFile string
	HTTP      *http.Client
}

// The standard projected token resolves through kubelet symlinks. Open it anew
// per call, but reject FIFOs/devices and oversized files before a bounded read.
func readInboxToken(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errors.New("inbox projected token unavailable")
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 16<<10 {
		return nil, errors.New("inbox projected token unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, (16<<10)+1))
	if err != nil || len(data) > 16<<10 || len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("inbox projected token unavailable")
	}
	return data, nil
}
func NewInboxClient(s Settings) (*InboxClient, error) {
	u, err := url.Parse(s.APIURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("inbox API requires a verified HTTPS origin")
	}
	h, err := NewHeartbeatClient(s, "")
	if err != nil {
		return nil, errors.New("inbox TLS configuration unavailable")
	}
	h.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &InboxClient{URL: strings.TrimSuffix(s.APIURL, "/"), TokenFile: s.APITokenFile, HTTP: h.HTTP}, nil
}

// Do preserves bounded JSON response bodies, including confirmed conflicts and
// Unknown outcomes. Network errors never include tokens or response content.
func (c *InboxClient) Do(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	if c.HTTP == nil || len(path) > 8192 || len(body) > InboxMaxRequestBytes || !utf8.Valid(body) || (path != apiv1.InboxPath && !strings.HasPrefix(path, apiv1.InboxPath+"?") && !strings.HasPrefix(path, apiv1.InboxPath+"/")) {
		return nil, 0, errors.New("invalid bounded inbox operation")
	}
	return c.doBounded(ctx, method, path, body, "inbox", InboxMaxRequestBytes)
}

// doBounded shares the existing projected-identity/TLS transport. Callers build
// fixed validated routes; it is deliberately not an exported arbitrary API client.
func (c *InboxClient) doBounded(ctx context.Context, method, path string, body []byte, operation string, maxRequest int) ([]byte, int, error) {
	if c == nil || c.HTTP == nil || len(path) > 8192 || len(body) > maxRequest || !utf8.Valid(body) {
		return nil, 0, fmt.Errorf("invalid bounded %s operation", operation)
	}
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	token, err := readInboxToken(c.TokenFile)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("invalid %s request", operation)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	h := *c.HTTP
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := h.Do(req)
	if err != nil {
		hint := "inspect the same intent before any retry"
		if operation == "inbox" {
			hint = "retry the same intent and key"
		}
		return nil, 0, fmt.Errorf("%s outcome Unknown; %s", operation, hint)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 3 {
		return nil, resp.StatusCode, fmt.Errorf("%s redirect refused", operation)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, InboxMaxResponseBytes+1))
	if err != nil || len(data) > InboxMaxResponseBytes || !utf8.Valid(data) || !json.Valid(data) {
		return nil, resp.StatusCode, fmt.Errorf("%s response invalid or exceeds bound", operation)
	}
	return data, resp.StatusCode, nil
}
