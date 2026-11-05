//go:build portallocaltest

package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
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

func TestLocalTestCommandServesSessionsWithBanner(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out, in := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"-listen", "127.0.0.1:0", "-fixtures", "../../../scripts/trials/portal_product"}, in, io.Discard)
		_ = in.Close()
	}()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatalf("no URL line: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, out) }()
	url := strings.TrimSpace(line[strings.Index(line, "http://"):])
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
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not stop")
	}
}
