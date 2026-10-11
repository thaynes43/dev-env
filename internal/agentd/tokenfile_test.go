package agentd

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

type tokenRefusalTransport struct{}

func (tokenRefusalTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("invalid token must be refused before any HTTP request")
}

func TestAPIClientsRefuseUnboundedTokenBeforeHTTP(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "synthetic-fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(dir, "synthetic-large")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, large} {
		h := &HeartbeatClient{URL: "https://synthetic.invalid", Session: "synthetic", TokenFile: path, HTTP: &http.Client{Transport: tokenRefusalTransport{}}}
		if err := h.Send(context.Background(), protocol.Status{}); err == nil {
			t.Fatal("heartbeat accepted unsafe token file")
		}
		if _, err := h.ReadDecisionAuthority(context.Background()); err == nil {
			t.Fatal("decision authority accepted unsafe token file")
		}
		if err := h.contextRequest(context.Background(), http.MethodGet, "/v1/published-contexts", nil, new(any)); err == nil {
			t.Fatal("published context accepted unsafe token file")
		}
	}
}
