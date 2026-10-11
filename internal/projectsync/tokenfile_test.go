package projectsync

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type tokenRefusalTransport struct{}

func (tokenRefusalTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("invalid token must be refused before any named GET")
}

func TestNamedAPIRefusesUnboundedTokenBeforeHTTP(t *testing.T) {
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
		r := &apiReader{client: &http.Client{Transport: tokenRefusalTransport{}}, endpoint: "https://synthetic.invalid", tokenFile: path}
		if _, err := r.Pod(context.Background(), "synthetic", "pod"); err == nil {
			t.Fatal("named GET accepted unsafe token file")
		}
	}
}
