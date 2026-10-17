package agentrun

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestAccessMCPRequestsAndDoesNotInstall(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusCreated, `{"name":"grant-1017-160000-ab12","phase":"Pending","type":"kube","session":"task-1","ttl":"1h0m0s","reason":"read pods","approvalURL":"https://broker.example/g"}`
		},
	})
	initBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	note := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	call := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"kube","reason":"read pods","role":"dev-env-grant-workloads"}}}`)
	h.env.Stdin = strings.NewReader(frameMCP(initBody) + frameMCP(note) + frameMCP(call))
	h.mustRun(ExitOK, "mcp")
	msgs := decodeMCPFrames(t, h.stdout.String())
	if len(msgs) != 2 {
		t.Fatalf("responses %d: %s", len(msgs), h.stdout.String())
	}
	if !strings.Contains(msgs[0], `"name":"dev-env"`) || strings.Contains(msgs[0], `"id":2`) {
		t.Fatalf("initialize %s", msgs[0])
	}
	if !strings.Contains(msgs[1], "https://broker.example/g") || !strings.Contains(msgs[1], `"isError":false`) {
		t.Fatalf("call %s", msgs[1])
	}
	var got apiv1.CreateGrantRequest
	if err := json.Unmarshal(h.api.requests()[0].body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Type != "kube" || got.Reason != "read pods" || got.Role != "dev-env-grant-workloads" {
		t.Fatalf("body %#v", got)
	}
	before := len(h.api.requests())
	bad := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"later","reason":"x"}}}`)
	h.env.Stdin = strings.NewReader(frameMCP(bad))
	h.mustRun(ExitOK, "mcp")
	if len(h.api.requests()) != before {
		t.Fatal("a refused tool call reached the API")
	}
	if !strings.Contains(h.stdout.String(), `"isError":true`) {
		t.Fatalf("refused call %s", h.stdout.String())
	}
}

func TestAccessMCPStatusAndRelease(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.URL.Query().Get("mine") != "true" || r.URL.Query().Get("phase") != "Active" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			return http.StatusOK, `{"items":[{"name":"grant-abc","phase":"Active","type":"kube","ttl":"30m","reason":"read pods"}]}`
		},
		apiv1.GrantPath("grant-abc"): func(r *http.Request) (int, string) {
			if r.Method != http.MethodDelete {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusAccepted, `{"name":"grant-abc","phase":"Active","type":"kube","ttl":"30m","reason":"read pods","release":true}`
		},
	})
	list := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grant_status","arguments":{"phase":"Active","mine":true}}}`)
	rel := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"release_access","arguments":{"name":"grant-abc"}}}`)
	h.env.Stdin = strings.NewReader(frameMCP(list) + frameMCP(rel))
	h.mustRun(ExitOK, "mcp")
	if len(h.api.requests()) != 2 || h.api.requests()[1].method != http.MethodDelete {
		t.Fatalf("requests %+v", h.api.requests())
	}
	if !strings.Contains(h.stdout.String(), "grant-abc") || !strings.Contains(h.stdout.String(), `release\": true`) {
		t.Fatalf("stdout %s", h.stdout.String())
	}
	h.mustRun(ExitUsage, "mcp", "serve")
}

func decodeMCPFrames(t *testing.T, raw string) []string {
	t.Helper()
	r := bufio.NewReader(strings.NewReader(raw))
	var out []string
	for {
		body, err := readMCPMessage(r)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(body))
	}
}
