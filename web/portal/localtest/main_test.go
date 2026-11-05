//go:build portallocaltest

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/web/portal/session"
)

func TestLocalTestListenRefusesNonLoopback(t *testing.T) {
	for _, raw := range []string{"127.0.0.1:0", "127.0.0.2:8099", "[::1]:0", "[::ffff:127.0.0.1]:0"} {
		if _, err := loopbackAddr(raw); err != nil {
			t.Fatalf("%s refused: %v", raw, err)
		}
	}
	for _, raw := range []string{"0.0.0.0:0", ":0", ":8080", "[::]:0", "192.168.1.10:8099", "10.0.0.1:0", "localhost:0", "example.com:443", "unix:/tmp/portal.sock", "/tmp/portal.sock", "[fe80::1%eth0]:0", "127.0.0.1"} {
		if _, err := loopbackAddr(raw); err == nil {
			t.Fatalf("%s accepted", raw)
		}
	}
	var stderr strings.Builder
	if got := run(context.Background(), []string{"-listen", "0.0.0.0:0"}, io.Discard, &stderr); got != 2 || !strings.Contains(stderr.String(), "refuses non-loopback") {
		t.Fatalf("code=%d stderr=%q", got, stderr.String())
	}
	if got := run(context.Background(), []string{"extra"}, io.Discard, io.Discard); got != 2 {
		t.Fatalf("positional argument accepted: %d", got)
	}
}

// interceptDNS fails and counts every DNS query in this process for one
// test. The command test reaches its server by IP literal, so any recorded
// query means some code tried to resolve a name for a real dial.
func interceptDNS(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	var attempts []string
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(_ context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		attempts = append(attempts, network+" "+address)
		mu.Unlock()
		return nil, errors.New("outbound attempt intercepted by the test")
	}}
	t.Cleanup(func() { net.DefaultResolver = previous })
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		if len(attempts) != 0 {
			t.Errorf("outbound attempts intercepted: %d %v", len(attempts), attempts)
		}
	})
}

func TestLocalTestCommandServesSessionsWithBanner(t *testing.T) {
	interceptDNS(t)
	ctx, cancel := context.WithCancel(context.Background())
	out, in := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-listen", "127.0.0.1:0", "-fixtures", "../../../scripts/trials/portal_product"}, in, io.Discard)
		_ = in.Close()
	}()
	stopped := false
	t.Cleanup(func() {
		cancel()
		_ = out.Close()
		if !stopped {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("server did not stop")
			}
		}
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	at := strings.Index(line, "http://")
	if err != nil || at < 0 {
		t.Fatalf("no URL line: %q %v", line, err)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	url := strings.TrimSpace(line[at:])
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, session.SessionsPath) {
		t.Fatalf("url %q", url)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	page := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(page, `id="`+session.LocalTestBannerID+`"`) || !strings.Contains(page, "fixture-portal-polish") {
		t.Fatalf("status %d page %.400s", response.StatusCode, page)
	}
	cancel()
	select {
	case code := <-done:
		stopped = true
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
}
