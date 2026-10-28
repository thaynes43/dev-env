package session

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/pages"
	"github.com/thaynes43/dev-env/web/portal"
)

const syntheticAlerts = `[{"fingerprint":"f1","labels":{"alertname":"<script>bad</script>","Name":"One","name":"two"},"annotations":{"summary":"ALERT-CANARY","description":"detail","runbook_url":"javascript:bad"},"status":{"state":"active"}}]`

func pagesFixture(t *testing.T) (*authFixture, *pagesSource, *int) {
	t.Helper()
	f := setupAuth(t)
	f.login(t)
	count := new(int)
	source := newPagesSource(true, func() time.Time { return f.now }, nil)
	source.client = &http.Client{Transport: tripFunc(func(r *http.Request) (*http.Response, error) {
		*count++
		if r.URL.String() != pagesSourceURL || r.Method != "GET" || r.Header.Get("Authorization") != "" {
			t.Error("unexpected outbound request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(syntheticAlerts)), Header: make(http.Header)}, nil
	}), Timeout: 5 * time.Second}
	return f, source, count
}

func pagesCheckConfig(t *testing.T, doc string) string {
	t.Helper()
	root := t.TempDir()
	if doc != "" {
		if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "config/pages-alertmanager.json"), []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestPagesSourceCheckDisabledAndMalformedDoNotDial(t *testing.T) {
	for _, doc := range []string{"", "broken", `{"source_url":null}`, `{"source_url":"http://example.invalid"}`, `{"source_url":"` + pagesSourceURL + `","extra":true}`} {
		root := pagesCheckConfig(t, doc)
		calls := 0
		dial := func(context.Context, string, string) (net.Conn, error) {
			calls++
			return nil, errors.New("PRIVATE-DIAL")
		}
		got := checkPagesSource(context.Background(), root, time.Now, dial)
		want := "unavailable"
		if doc == "" {
			want = "disabled"
		}
		if got.Status != want || got.Count != nil || got.ObservedAt != nil || calls != 0 {
			t.Fatalf("doc=%q result=%+v dials=%d", doc, got, calls)
		}
	}
}

func TestPagesSourceCheckFixedReadAndSanitizedFailures(t *testing.T) {
	root := pagesCheckConfig(t, `{"source_url":"`+pagesSourceURL+`"}`)
	at := time.Date(2026, 10, 28, 12, 0, 0, 123, time.UTC)
	// Environment proxy settings must not redirect this fixed transport.
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	for _, tc := range []struct {
		name, body string
		status     int
		dialErr    error
		count      int
	}{
		{"empty", "[]", 200, nil, 0},
		{"alerts", syntheticAlerts, 200, nil, 1},
		{"two alerts", `[{"fingerprint":"f1","labels":{"alertname":"PRIVATE-ONE"},"status":{"state":"active"}},{"fingerprint":"f2","labels":{"alertname":"PRIVATE-TWO"},"status":{"state":"suppressed"}}]`, 200, nil, 2},
		{"transport", "", 0, errors.New("PRIVATE-TRANSPORT-URL"), -1},
		{"timeout", "", 0, context.DeadlineExceeded, -1},
		{"non200", "PRIVATE-BODY", 503, nil, -1},
		{"redirect", "PRIVATE-BODY", 302, nil, -1},
		{"oversize", strings.Repeat("x", pages.AlertmanagerMaxBytes+1), 200, nil, -1},
		{"schema", `[{"fingerprint":null}]`, 200, nil, -1},
		{"envelope", `{"alerts":[]}`, 200, nil, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan *http.Request, 1)
			calls := 0
			dial := func(_ context.Context, network, addr string) (net.Conn, error) {
				calls++
				if network != "tcp" || addr != "alertmanager-operated.observability.svc.cluster.local:9093" {
					t.Errorf("unexpected target %s %s", network, addr)
				}
				if tc.dialErr != nil {
					return nil, tc.dialErr
				}
				client, server := net.Pipe()
				go func() {
					defer func() { _ = server.Close() }()
					req, err := http.ReadRequest(bufio.NewReader(server))
					if err != nil {
						t.Error(err)
						return
					}
					requests <- req
					response := &http.Response{StatusCode: tc.status, ProtoMajor: 1, ProtoMinor: 1, Header: http.Header{"Location": {"http://example.invalid/PRIVATE"}}, ContentLength: int64(len(tc.body)), Body: io.NopCloser(strings.NewReader(tc.body))}
					// An over-bound response may be closed before its last byte.
					_ = response.Write(server)
				}()
				return client, nil
			}
			got := checkPagesSource(context.Background(), root, func() time.Time { return at }, dial)
			if calls != 1 {
				t.Fatalf("unexpected retry/dial count %d", calls)
			}
			if tc.dialErr == nil {
				req := <-requests
				if req.Method != http.MethodGet || req.URL.RequestURI() != "/api/v2/alerts" || req.Host != "alertmanager-operated.observability.svc.cluster.local:9093" || req.ContentLength != 0 {
					t.Fatal("unexpected fixed request")
				}
				for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Origin"} {
					if req.Header.Get(key) != "" {
						t.Fatalf("unexpected %s", key)
					}
				}
			}
			if tc.count >= 0 {
				if got.Status != "available" || got.Count == nil || *got.Count != tc.count || got.ObservedAt == nil || *got.ObservedAt != at.Format(time.RFC3339Nano) {
					t.Fatalf("successful result %+v", got)
				}
			} else if got.Status != "unavailable" || got.Count != nil || got.ObservedAt != nil {
				t.Fatalf("failed result %+v", got)
			}
			raw, err := json.Marshal(got)
			if err != nil || len(raw) > 160 || strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "ALERT-CANARY") || strings.Contains(string(raw), "script") || strings.Contains(string(raw), "http") {
				t.Fatalf("unsafe diagnostic %q", raw)
			}
		})
	}
}
func pagesAssembly(f *authFixture, s *pagesSource, author portal.Authorizer) http.Handler {
	return NewAssembly(AssemblyConfig{Session: f.e, Authorizer: author, Reader: portal.UnavailableReader{}, Pages: s, FreshFor: time.Minute, Now: func() time.Time { return f.now }})
}
func allowPages() portal.Authorizer {
	return authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error { return nil })
}
func TestPagesScopeAndRequestBoundary(t *testing.T) {
	f, s, count := pagesFixture(t)
	for _, tc := range []struct {
		name, method, path string
		source             *pagesSource
		author             portal.Authorizer
	}{
		{"disabled", "GET", pagesPath, nil, allowPages()},
		{"denied", "GET", pagesPath, s, authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error { return portal.ErrDenied })},
		{"method", "POST", pagesPath, s, allowPages()}, {"query", "GET", pagesPath + "?q=x", s, allowPages()}, {"path", "GET", pagesPath + "/extra", s, allowPages()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			pagesAssembly(f, tc.source, tc.author).ServeHTTP(w, f.request(tc.method, tc.path, ""))
			if *count != 0 || strings.Contains(w.Body.String(), "ALERT-CANARY") {
				t.Fatal("unauthorized fetch/release")
			}
			if tc.name == "disabled" {
				if !strings.Contains(w.Body.String(), "Unavailable") || !strings.Contains(w.Body.String(), "Pages not enabled") {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
	w := httptest.NewRecorder()
	pagesAssembly(f, s, allowPages()).ServeHTTP(w, f.request("GET", pagesPath, ""))
	if w.Code != 200 || *count != 1 || !strings.Contains(w.Body.String(), "ALERT-CANARY") || strings.Contains(w.Body.String(), "<script>bad</script>") || !strings.Contains(w.Body.String(), "Unknown") || !strings.Contains(w.Body.String(), f.now.Format(time.RFC3339)) {
		t.Fatalf("%d %d %s", w.Code, *count, w.Body.String())
	}
}
func TestPagesNoReleaseAfterRevocation(t *testing.T) {
	for _, mode := range []string{"session", "authorization"} {
		t.Run(mode, func(t *testing.T) {
			f, s, _ := pagesFixture(t)
			r := f.request("GET", pagesPath, "")
			key, err := requestHandle(r, sessionCookie)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			author := authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error {
				calls++
				if mode == "authorization" && calls > 1 {
					return portal.ErrDenied
				}
				return nil
			})
			s.client.Transport = tripFunc(func(*http.Request) (*http.Response, error) {
				if mode == "session" {
					if err := f.store.RevokeSession(context.Background(), key); err != nil {
						t.Fatal(err)
					}
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(syntheticAlerts)), Header: make(http.Header)}, nil
			})
			w := httptest.NewRecorder()
			pagesAssembly(f, s, author).ServeHTTP(w, r)
			if strings.Contains(w.Body.String(), "ALERT-CANARY") || w.Code == 200 {
				t.Fatal("revoked response released")
			}
		})
	}
}
func TestPagesFreshEmptyAndFailures(t *testing.T) {
	f, s, _ := pagesFixture(t)
	calls := 0
	s.client.Transport = tripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(syntheticAlerts)), Header: make(http.Header)}, nil
		}
		return nil, errors.New("PRIVATE-ERROR")
	})
	h := pagesAssembly(f, s, allowPages())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", pagesPath, ""))
	if !strings.Contains(w.Body.String(), "ALERT-CANARY") {
		t.Fatal("first read missing")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, f.request("GET", pagesPath, ""))
	if strings.Contains(w.Body.String(), "ALERT-CANARY") || strings.Contains(w.Body.String(), "PRIVATE-ERROR") || !strings.Contains(w.Body.String(), "Unavailable") || !strings.Contains(w.Body.String(), "Alertmanager is unavailable") || strings.Contains(w.Body.String(), "Pages not enabled") {
		t.Fatal("stale or failed output")
	}
	for _, tc := range []struct {
		name, body string
		status     int
		err        error
	}{{"empty", "[]", 200, nil}, {"malformed", "broken", 200, nil}, {"envelope", `{"alerts":[]}`, 200, nil}, {"oversize", strings.Repeat("x", 2<<20+1), 200, nil}, {"redirect", "", 302, nil}, {"error", "", 500, nil}, {"timeout", "", 0, context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			s.client.Transport = tripFunc(func(*http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})
			w := httptest.NewRecorder()
			h.ServeHTTP(w, f.request("GET", pagesPath, ""))
			want := "Unavailable"
			if tc.name == "empty" {
				want = "Observed zero current alerts"
			}
			if !strings.Contains(w.Body.String(), want) {
				t.Fatalf("%s: %d", tc.name, w.Code)
			}
		})
	}
}
func TestPagesSourceTransportIsFixed(t *testing.T) {
	s := newPagesSource(true, time.Now, nil)
	tr := s.client.Transport.(*http.Transport)
	if tr.Proxy != nil || s.client.Timeout != 5*time.Second || !errors.Is(s.client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("unsafe source transport")
	}
}

func TestPagesConfigStrictAndDefaultOff(t *testing.T) {
	root := t.TempDir()
	enabled, err := loadPagesEnabled(root)
	if err != nil || enabled {
		t.Fatal("absent config not disabled")
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config", "pages-alertmanager.json")
	valid := `{"source_url":"` + pagesSourceURL + `"}`
	cases := []struct {
		doc string
		ok  bool
	}{{valid, true}, {`{"source_url":null}`, false}, {`{"source_url":123}`, false}, {`{"source_url":"` + pagesSourceURL + `","source_url":"` + pagesSourceURL + `"}`, false}, {strings.Replace(valid, `"source_url"`, `"Source_url"`, 1), false}, {strings.Replace(valid, `"source_url"`, `"ſource_url"`, 1), false}, {strings.TrimSuffix(valid, "}") + `,"unknown":true}`, false}, {strings.Replace(valid, pagesSourceURL, "http://evil.example", 1), false}}
	for _, tc := range cases {
		if err := os.WriteFile(path, []byte(tc.doc), 0600); err != nil {
			t.Fatal(err)
		}
		got, err := loadPagesEnabled(root)
		if (err == nil) != tc.ok || got != tc.ok {
			t.Fatalf("unexpected config %q", tc.doc)
		}
	}
}

func TestPagesTwoAuthorizedUsersFetch(t *testing.T) {
	f, s, count := pagesFixture(t)
	first := f.session
	f.subject = "other-subject"
	f.login(t)
	second := f.session
	h := pagesAssembly(f, s, allowPages())
	for _, tc := range []struct {
		cookie *http.Cookie
		want   string
	}{{first, "ALERT-CANARY"}, {second, "ALERT-CANARY"}} {
		r := f.request("GET", pagesPath, "")
		r.Header.Del("Cookie")
		r.AddCookie(tc.cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("missing %s", tc.want)
		}
	}
	if *count != 2 {
		t.Fatalf("fetches: %d", *count)
	}
}
func TestPagesFixedTransportNoRedirectOrProxy(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/v2/alerts" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect request")
		}
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer server.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != "alertmanager-operated.observability.svc.cluster.local:9093" {
			t.Errorf("unexpected target %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	s := newPagesSource(true, time.Now, dial)
	if _, err := s.read(context.Background()); err == nil || hits != 1 {
		t.Fatalf("redirect followed or accepted: %d", hits)
	}
}

func TestPagesOversizedRenderReleasesNothing(t *testing.T) {
	f, s, _ := pagesFixture(t)
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < 130; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"fingerprint":"f%d","labels":{"alertname":"BIG-ALERT-CANARY"},"annotations":{"description":"%s"},"status":{"state":"active"}}`, i, strings.Repeat("x", 8000))
	}
	b.WriteByte(']')
	s.client.Transport = tripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b.String())), Header: make(http.Header)}, nil
	})
	w := httptest.NewRecorder()
	pagesAssembly(f, s, allowPages()).ServeHTTP(w, f.request("GET", pagesPath, ""))
	if w.Code != 503 || strings.Contains(w.Body.String(), "BIG-ALERT-CANARY") {
		t.Fatal("oversized render escaped containment")
	}
}

func TestPagesAnonymousLoginAndSignedInDenial(t *testing.T) {
	f, s, count := pagesFixture(t)
	r := f.request("GET", pagesPath, "")
	r.Header.Del("Cookie")
	w := httptest.NewRecorder()
	pagesAssembly(f, s, allowPages()).ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/auth/login" || *count != 0 {
		t.Fatal("anonymous request did not start login")
	}
	deny := authorizeFunc(func(context.Context, portal.Principal, portal.Resource) error { return portal.ErrDenied })
	w = httptest.NewRecorder()
	pagesAssembly(f, s, deny).ServeHTTP(w, f.request("GET", pagesPath, ""))
	if w.Code == http.StatusSeeOther || w.Header().Get("Location") != "" || *count != 0 {
		t.Fatal("signed-in denial looped to login or fetched")
	}
}
