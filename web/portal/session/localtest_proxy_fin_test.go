//go:build portallocaltest

package session

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLocalTestEarlyFINCancelsDelayedGET records the Go 1.27 behavior the
// proxy must not provoke: a bodyless GET whose client FINs while the handler
// is still waiting has its request context cancelled.
func TestLocalTestEarlyFINCancelsDelayedGET(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", target.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err = io.WriteString(conn, "GET /ready HTTP/1.1\r\nHost: "+target.Host+"\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed handler did not start")
	}
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		t.Fatal("expected a TCP connection")
	}
	if err = tcp.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("early FIN did not cancel the delayed Go GET")
	}
}

// TestLocalTestProxyDelayedGETKeepsContext sends that same bodyless GET
// through the fixture proxy. The proxy keeps Connection: close and must not
// FIN early, so the Go handler finishes 200 with its context still active.
func TestLocalTestProxyDelayedGETKeepsContext(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(250 * time.Millisecond):
			w.Header().Set("Content-Length", "5")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ready")
		}
	}))
	t.Cleanup(srv.Close)
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller path")
	}
	script := filepath.Join(filepath.Dir(file), "..", "localtest", "smoke-flows.cjs")
	cmd := exec.Command(node, "-e", proxyHoldProgram, script, srv.URL)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var proxyErr strings.Builder
	cmd.Stderr = &proxyErr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Kill if still running, then Wait once. os/exec finishes the stderr copy
	// before Wait returns, so the builder is read only after that.
	var reapOnce sync.Once
	reap := func() {
		reapOnce.Do(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
		})
	}
	t.Cleanup(reap)
	proxyLine := make(chan string, 1)
	go func() {
		line, readErr := bufio.NewReader(stdout).ReadString('\n')
		if readErr != nil {
			proxyLine <- ""
			return
		}
		proxyLine <- strings.TrimSpace(line)
	}()
	var proxyURL string
	select {
	case proxyURL = <-proxyLine:
	case <-time.After(5 * time.Second):
		reap()
		t.Fatalf("proxy did not start: %s", proxyErr.String())
	}
	if proxyURL == "" {
		reap()
		t.Fatalf("proxy exited before listening: %s", proxyErr.String())
	}
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", proxy.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	request := "GET " + srv.URL + "/ready HTTP/1.1\r\nHost: " + target.Host + "\r\nConnection: close\r\n\r\n"
	if _, err = io.WriteString(conn, request); err != nil {
		t.Fatal(err)
	}
	status, body, err := readOneResponse(conn)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	default:
		t.Fatal("delayed handler did not run")
	}
	select {
	case <-cancelled:
		t.Fatal("proxy FIN cancelled the delayed Go GET")
	default:
	}
	if status != http.StatusOK || body != "ready" {
		t.Fatalf("proxied delayed GET %d %q", status, body)
	}
}

const proxyHoldProgram = `
const flows = require(process.argv[1]);
flows.startFixtureProxy(process.argv[2]).then((proxy) => {
  process.stdout.write(proxy.url + "\n");
}).catch((err) => {
  console.error(err && err.stack ? err.stack : String(err));
  process.exit(1);
});
`

func readOneResponse(conn net.Conn) (int, string, error) {
	reader := bufio.NewReader(conn)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return 0, "", err
	}
	fields := strings.Fields(strings.TrimSpace(statusLine))
	status := 0
	if len(fields) >= 2 {
		status, err = strconv.Atoi(fields[1])
		if err != nil {
			return 0, "", err
		}
	}
	length := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return 0, "", err
		}
		if line == "\r\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			length, err = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err != nil {
				return 0, "", err
			}
		}
	}
	body := make([]byte, length)
	if _, err = io.ReadFull(reader, body); err != nil {
		return 0, "", err
	}
	return status, string(body), nil
}
