package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, handler http.HandlerFunc) (*reader, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		count.Add(1)
		if q.Method != "GET" || q.Header.Get("Authorization") != "Bearer fixture" {
			t.Error("unexpected request")
		}
		handler(w, q)
	}))
	t.Cleanup(s.Close)
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	u, _ := url.Parse(s.URL)
	return &reader{ctx: context.Background(), base: u, token: "fixture", tlsConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}, count
}
func TestFiniteHTTP(t *testing.T) {
	for _, status := range []int{200, 301, 302, 307, 308, 401, 403, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			r, count := fixture(t, func(w http.ResponseWriter, q *http.Request) {
				w.Header().Set("Location", "/api/v1/namespaces/dev-agents")
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"ok":true}`)
			})
			o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
			if count.Load() != 1 || r.attempts != 1 {
				t.Fatal("replayed request")
			}
			if status == 200 {
				if o.Receipt.Error != "" || string(o.Body) != `{"ok":true}` {
					t.Fatal("valid response refused")
				}
			} else if o.Receipt.Error != "HTTPStatusRefused" || len(o.Body) != 0 {
				t.Fatal("status not refused")
			}
		})
	}
}
func TestStreamCaps(t *testing.T) {
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprint(declared), func(t *testing.T) {
			r, _ := fixture(t, func(w http.ResponseWriter, q *http.Request) {
				if declared {
					w.Header().Set("Content-Length", fmt.Sprint(maxResponse+1000))
				} else {
					w.(http.Flusher).Flush()
				}
				fmt.Fprint(w, strings.Repeat("x", maxResponse+1000))
			})
			o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
			if o.Receipt.Error == "" || len(o.Body) != 0 || o.Receipt.ResponseBytes > maxResponse+1 {
				t.Fatal("response cap failure")
			}
			if !declared && o.Receipt.ResponseBytes != maxResponse+1 {
				t.Fatal("missing bounded overrun sentinel")
			}
		})
	}
	r, _ := fixture(t, func(w http.ResponseWriter, q *http.Request) {
		w.(http.Flusher).Flush()
		fmt.Fprint(w, strings.Repeat("x", 200))
	})
	r.total = maxTotal - 100
	o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
	if o.Receipt.Error != "ResponseBudgetExhausted" || o.Receipt.ResponseBytes != 101 || r.total != maxTotal+1 {
		t.Fatal("aggregate cap failure")
	}
}
func TestRefuseBeforeNetwork(t *testing.T) {
	r, count := fixture(t, func(w http.ResponseWriter, q *http.Request) { t.Error("unexpected network") })
	for _, in := range []input{{Op: "get", Path: "/api/v1/secrets/private"}, {Op: "get", Path: "/api/v1/namespaces/dev-agents", Query: &query{Limit: 32}}, {Op: "get", Path: "/api/v1/namespaces/dev-agents/pods"}, {Op: "get", Path: "/api/v1/namespaces/dev-agents/pods", Query: &query{Limit: 100}}} {
		if r.get(in).Receipt.Error == "" {
			t.Fatal("shape accepted")
		}
	}
	r.attempts = maxRequests
	if r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"}).Receipt.Error != "RequestBudgetExhausted" || count.Load() != 0 {
		t.Fatal("budget failure")
	}
}
func TestDeadline(t *testing.T) {
	r, _ := fixture(t, func(w http.ResponseWriter, q *http.Request) { <-q.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	r.ctx = ctx
	start := time.Now()
	o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
	if o.Receipt.Error != "RequestTimeout" || time.Since(start) > time.Second {
		t.Fatal("wall deadline failure")
	}
}
func TestMalformedAndEncoding(t *testing.T) {
	for _, mode := range []string{"json", "gzip"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := fixture(t, func(w http.ResponseWriter, q *http.Request) {
				if mode == "gzip" {
					w.Header().Set("Content-Encoding", "gzip")
				}
				fmt.Fprint(w, "raw-invalid-body")
			})
			o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
			if o.Receipt.Error == "" || len(o.Body) != 0 {
				t.Fatal("invalid response accepted")
			}
		})
	}
}

func TestDisconnectedRequestNeverReplayed(t *testing.T) {
	r, count := fixture(t, func(w http.ResponseWriter, q *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	})
	o := r.get(input{Op: "get", Path: "/api/v1/namespaces/dev-agents"})
	if o.Receipt.Error != "TransportRefused" || count.Load() != 1 || r.attempts != 1 {
		t.Fatal("broken connection replayed")
	}
}
func TestAuthShape(t *testing.T) {
	base := "current-context: haynes-haynes-ops-machine\ncontexts:\n- name: haynes-haynes-ops-machine\n  context: {cluster: local, user: existing}\nclusters:\n- name: local\n  cluster: {server: 'https://example.invalid'}\nusers:\n- name: existing\n  user: {token: fixture}\n"
	good, err := parseConfig(context.Background(), []byte(base))
	if err != nil || good.token != "fixture" {
		t.Fatal("fixture configuration rejected")
	}
	cases := []string{strings.Replace(base, "current-context: haynes-haynes-ops-machine", "current-context: different", 1), strings.Replace(base, "user: {token: fixture}", "user: {token: fixture, exec: {command: dangerous}}", 1), strings.Replace(base, "user: {token: fixture}", "user: {tokenFile: ignored}", 1), strings.Replace(base, "https://example.invalid", "http://example.invalid", 1), strings.Replace(base, "server: 'https://example.invalid'", "server: 'https://example.invalid', insecure-skip-tls-verify: true", 1), strings.Replace(base, "https://example.invalid", "https://example.invalid/?private=hidden", 1), strings.Replace(base, "user: {token: fixture}", "user: {token: fixture, as: other}", 1)}
	for i, s := range cases {
		if _, err := parseConfig(context.Background(), []byte(s)); err == nil {
			t.Fatalf("unsafe config case%d accepted", i)
		} else if strings.Contains(err.Error(), "fixture") || strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "example") {
			t.Fatal("private input in error")
		}
	}
}
func TestUnknownPathRedacted(t *testing.T) {
	r, count := fixture(t, func(w http.ResponseWriter, q *http.Request) { t.Error("network not allowed") })
	o := r.get(input{Op: "get", Path: "/private/value"})
	if o.Receipt.Path != "" || count.Load() != 0 {
		t.Fatal("unknown path disclosed")
	}
}
