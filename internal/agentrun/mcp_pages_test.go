package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePagesBackend struct {
	calls int
	raw   []byte
	err   error
}

func (f *fakePagesBackend) Fetch(_ context.Context, _ string) ([]byte, error) {
	f.calls++
	return f.raw, f.err
}
func callPages(s *pagesMCPSession, name, args string) map[string]any {
	raw := []byte(`{"name":"` + name + `","arguments":` + args + `}`)
	return s.call(raw)
}
func responseText(t *testing.T, r map[string]any) string {
	t.Helper()
	return r["content"].([]map[string]string)[0]["text"]
}
func TestPagesMCPDiscoveryAndDisabledBoundary(t *testing.T) {
	f := &fakePagesBackend{}
	s := &pagesMCPSession{ctx: context.Background(), backend: f, now: time.Now}
	var out bytes.Buffer
	input := strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	if err := s.serve(input, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Result.Tools) != 2 || doc.Result.Tools[0].Name != "pages_list" || doc.Result.Tools[1].Name != "pages_get" {
		t.Fatal("wrong tools")
	}
	for _, call := range []struct{ name, args string }{{"pages_list", `{"source":"gatus"}`}, {"pages_list", `{"source":"other"}`}, {"pages_list", `{"source":"gatus","url":"https://evil"}`}, {"pages_list", `{"source":"gatus","limit":"2"}`}, {"pages_list", `{"source":"gatus","source":"gatus"}`}, {"pages_get", `{"source":"gatus","key":null}`}, {"release_access", `{}`}} {
		r := callPages(s, call.name, call.args)
		if r["isError"] != true {
			t.Fatalf("accepted %s", call.args)
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid or disabled request contacted backend")
	}
}
func TestPagesMCPCommandAndSnapshotEnvelope(t *testing.T) {
	h := newHarness(t)
	h.env.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n")
	h.mustRun(ExitOK, "pages-mcp")
	if !strings.Contains(h.stdout.String(), "pages_list") || !strings.Contains(h.stdout.String(), "pages_get") {
		t.Fatal("command did not expose the two tools")
	}
	if _, _, err := normalizePagesMCP("alertmanager", []byte(`{"alerts":[]}`), time.Now()); err == nil {
		t.Fatal("accepted unsupported alert envelope")
	}
	items, _, err := normalizePagesMCP("alertmanager", []byte(`[]`), time.Now())
	if err != nil || len(items) != 0 {
		t.Fatal("valid empty alert array rejected", err)
	}
}
func TestPagesMCPConfigIsPrivateAndStrict(t *testing.T) {
	cfg, err := pagesMCPConfigFile("")
	if err != nil || cfg.Gatus || cfg.Alertmanager {
		t.Fatal("default not disabled")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for _, tc := range []struct {
		body string
		mode os.FileMode
		ok   bool
	}{{`{"gatus":true}`, 0600, true}, {`{"gatus":true}`, 0644, false}, {`{"gatus":null}`, 0600, false}, {`{"gatus":true,"gatus":false}`, 0600, false}, {`{"Gatus":true}`, 0600, false}, {`{"gatus":true,"url":"x"}`, 0600, false}} {
		if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tc.mode); err != nil {
			t.Fatal(err)
		}
		_, err := pagesMCPConfigFile(path)
		if (err == nil) != tc.ok {
			t.Fatalf("bad config acceptance %s", tc.body)
		}
	}
}
func TestPagesMCPListGetRevisionAndUnavailable(t *testing.T) {
	raw := `[{"key":"x","name":"Example","results":[{"success":false,"timestamp":"2026-10-28T10:00:00Z","status":0,"duration":0}]}]`
	f := &fakePagesBackend{raw: []byte(raw)}
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Gatus: true}, backend: f, now: func() time.Time { return now }}
	r := callPages(s, "pages_list", `{"source":"gatus","filter":"example","limit":1}`)
	var list pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, r)), &list); err != nil || len(list.Items) != 1 || list.Items[0].Success == nil || *list.Items[0].Success || list.Items[0].HTTPStatus == nil || *list.Items[0].HTTPStatus != 0 {
		t.Fatal(list, err)
	}
	s.now = func() time.Time { return now.Add(time.Minute) }
	r = callPages(s, "pages_get", `{"source":"gatus","key":"x","expectedRevision":"`+list.Revision+`"}`)
	var got pagesMCPResult
	if err := json.Unmarshal([]byte(responseText(t, r)), &got); err != nil || got.Revision != list.Revision || got.ChangedSinceList {
		t.Fatal(got, err)
	}
	f.raw = []byte(`[]`)
	r = callPages(s, "pages_get", `{"source":"gatus","key":"x","expectedRevision":"`+list.Revision+`"}`)
	if err := json.Unmarshal([]byte(responseText(t, r)), &got); err != nil || got.State != "NotPresentInCurrentSnapshot" || !got.ChangedSinceList {
		t.Fatal(got, err)
	}
	f.err = errors.New("SECRET-UPSTREAM")
	r = callPages(s, "pages_list", `{"source":"gatus"}`)
	if r["isError"] != true || strings.Contains(responseText(t, r), "SECRET") {
		t.Fatal("backend error leaked")
	}
}

type pagesRoundTripFunc func(*http.Request) (*http.Response, error)

func (f pagesRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPagesMCPFixedBackendProtocol(t *testing.T) {
	calls := 0
	b := newFixedPagesBackend()
	tr := b.client.Transport.(*http.Transport)
	if tr.Proxy != nil || b.client.Jar != nil || !errors.Is(b.client.CheckRedirect(nil, nil), http.ErrUseLastResponse) {
		t.Fatal("unsafe transport")
	}
	b.client.Transport = pagesRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.URL.String() != pagesGrafanaURL || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("unexpected HTTP request")
		}
		raw, _ := io.ReadAll(r.Body)
		var msg map[string]any
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Error(err)
		}
		var payload string
		header := make(http.Header)
		switch calls {
		case 1:
			if msg["method"] != "initialize" {
				t.Error("missing initialize")
			}
			header.Set("Mcp-Session-Id", "synthetic-session")
			payload = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"synthetic","version":"1"}}}`
		case 2:
			if msg["method"] != "notifications/initialized" || r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
				t.Error("missing initialized notification")
			}
			payload = ""
		case 3:
			if msg["method"] != "tools/call" || r.Header.Get("Mcp-Session-Id") != "synthetic-session" {
				t.Error("wrong tool call")
			}
			p := msg["params"].(map[string]any)
			if p["name"] != "grafana_api_request" {
				t.Error("wrong backend tool")
			}
			a := p["arguments"].(map[string]any)
			if len(a) != 2 || a["method"] != "GET" || a["endpoint"] != pagesGrafanaPath {
				t.Error("wrong backend operation")
			}
			payload = `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"[]"}]}}`
		default:
			t.Error("unexpected additional request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload)), Header: header}, nil
	})
	got, err := b.Fetch(context.Background(), "alertmanager")
	if err != nil || string(got) != "[]" || calls != 3 {
		t.Fatal("fixed protocol failed", err, calls)
	}
	calls = 0
	b.client.Transport = pagesRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.String() != pagesGatusURL || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("wrong Gatus request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`[]`)), Header: make(http.Header)}, nil
	})
	if got, err = b.Fetch(context.Background(), "gatus"); err != nil || string(got) != "[]" || calls != 1 {
		t.Fatal("Gatus request failed", err)
	}
}
func TestPagesMCPBackendDenialRedirectAndEnvelopes(t *testing.T) {
	for _, status := range []int{301, 403, 500} {
		calls := 0
		b := newFixedPagesBackend()
		b.client.Transport = pagesRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("SECRET-UPSTREAM")), Header: http.Header{"Location": []string{"https://elsewhere.invalid"}}}, nil
		})
		if _, err := b.Fetch(context.Background(), "gatus"); err == nil || calls != 1 || strings.Contains(err.Error(), "SECRET") {
			t.Fatal("backend retried or leaked")
		}
	}
	for _, result := range []string{`{"structuredContent":{"alerts":[]}}`, `{"content":[{"type":"text","text":"[]"},{"type":"text","text":"[]"}]}`, `{"content":[{"type":"text","text":"{}"}]}`, `{"content":[{"type":"text","text":"[]"}],"isError":true}`, `{"content":[{"type":"text","text":"[]"}],"unknown":1}`} {
		calls := 0
		b := newFixedPagesBackend()
		b.client.Transport = pagesRoundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			var body string
			switch calls {
			case 1:
				body = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{}}}`
			case 2:
				body = ""
			default:
				body = `{"jsonrpc":"2.0","id":2,"result":` + result + `}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		if _, err := b.Fetch(context.Background(), "alertmanager"); err == nil || calls != 3 {
			t.Fatal("ambiguous envelope accepted")
		}
	}
}
func TestPagesMCPStrictBoundsAndRedaction(t *testing.T) {
	f := &fakePagesBackend{}
	s := &pagesMCPSession{ctx: context.Background(), config: pagesMCPConfig{Gatus: true}, backend: f, now: time.Now}
	for _, input := range []string{`{"source":"gatus","limit":null}`, `{"source":"gatus","limit":1.2}`, `{"source":"gatus","ſource":"gatus"}`, `{"source":"gatus","filter":{"x":1}}`, strings.Repeat("[", 20) + strings.Repeat("]", 20)} {
		r := callPages(s, "pages_list", input)
		if r["isError"] != true {
			t.Fatal("invalid input accepted")
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid request fetched")
	}
	f.raw = []byte(`[{"key":"x","name":"token=secret https://example.invalid/a","results":[{"timestamp":"2026-10-28T10:00:00Z","success":true,"hostname":"PRIVATE-HOST","errors":["PRIVATE-ERROR"]}]}]`)
	text := responseText(t, callPages(s, "pages_list", `{"source":"gatus"}`))
	for _, bad := range []string{"secret", "example.invalid", "PRIVATE-HOST", "PRIVATE-ERROR"} {
		if strings.Contains(text, bad) {
			t.Fatalf("leak: %s", bad)
		}
	}
}
