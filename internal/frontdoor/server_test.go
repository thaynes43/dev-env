package frontdoor

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	testBearer   = "door-bearer-token"
	testOperator = "operator-sa-token"
	testTracker  = "tracker-secret-do-not-print"
)

const stubScript = `#!/bin/sh
echo "ORIGIN=$OPWORK_WRITE_ORIGIN"
echo "SESSION=$OPWORK_SESSION"
if [ -n "$OPWORK_TOKEN_FILE" ]; then echo "TOKEN_SET=yes"; else echo "TOKEN_SET=no"; fi
i=0
for arg in "$@"; do
  i=$((i + 1))
  printf '%s\n' "ARG $i=$arg"
  case "$arg" in
    --description=*|--acceptance=*|--constraints=*)
      value=${arg#*=}
      if [ -f "$value" ]; then
        echo "FILE"
        cat "$value"
      fi
      ;;
  esac
done
`

type recorded struct {
	method, path, auth string
	query              map[string][]string
	body               []byte
	limits, outcome    string
}

type fakeAPI struct {
	mu   sync.Mutex
	reqs []recorded
	fn   func(w http.ResponseWriter, r *http.Request, body []byte)
}

func (f *fakeAPI) reset() {
	f.mu.Lock()
	f.reqs = nil
	f.fn = nil
	f.mu.Unlock()
}

func (f *fakeAPI) set(fn func(w http.ResponseWriter, r *http.Request, body []byte)) {
	f.mu.Lock()
	f.fn = fn
	f.mu.Unlock()
}

func (f *fakeAPI) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func TestFrontDoorTools(t *testing.T) {
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bearer"), testBearer+"\n")
	writeFile(t, filepath.Join(dir, "operator"), testOperator+"\n")
	writeFile(t, filepath.Join(dir, "tracker"), testTracker+"\n")
	stub := filepath.Join(dir, "opwork")
	writeFile(t, stub, stubScript)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	api := &fakeAPI{}
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.reqs = append(api.reqs, recorded{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			query: r.URL.Query(), body: body,
			limits: r.Header.Get(apiv1.CallerPolicyLimitsHeader), outcome: r.Header.Get(apiv1.TaskOutcomeHeader),
		})
		fn := api.fn
		api.mu.Unlock()
		if fn == nil {
			writeJSON(w, http.StatusNotFound, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeNotFound, Message: "no handler"}})
			return
		}
		fn(w, r, body)
	}))
	t.Cleanup(tlsSrv.Close)
	ca := filepath.Join(dir, "ca.pem")
	writeFile(t, ca, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw})))

	var logs bytes.Buffer
	h, op, err := newHandler(Config{
		BearerFile: filepath.Join(dir, "bearer"), TrackerFile: filepath.Join(dir, "tracker"),
		OperatorTokenFile: filepath.Join(dir, "operator"), APIURL: tlsSrv.URL, CAFile: ca, Opwork: stub,
	}, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(op.Close)
	mcp := httptest.NewServer(h)
	t.Cleanup(mcp.Close)

	call := func(t *testing.T, token, method string, params any) (int, map[string]any) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, mcp.URL+"/mcp", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		if strings.Contains(string(raw), testBearer) || strings.Contains(string(raw), testOperator) || strings.Contains(string(raw), testTracker) {
			t.Fatal("response contains a credential")
		}
		var doc map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("status %d body %q", res.StatusCode, raw)
			}
		}
		return res.StatusCode, doc
	}
	tool := func(t *testing.T, name string, args map[string]any) (string, bool) {
		t.Helper()
		status, doc := call(t, testBearer, "tools/call", map[string]any{"name": name, "arguments": args})
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		result, _ := doc["result"].(map[string]any)
		if result == nil {
			t.Fatalf("no result: %#v", doc)
		}
		content, _ := result["content"].([]any)
		if len(content) != 1 {
			t.Fatalf("content %#v", result)
		}
		text, _ := content[0].(map[string]any)["text"].(string)
		isError, _ := result["isError"].(bool)
		return text, isError
	}

	t.Run("missing token", func(t *testing.T) {
		api.reset()
		status, doc := call(t, "", "tools/list", nil)
		if status != http.StatusUnauthorized || len(doc) != 0 {
			t.Fatalf("status %d doc %#v", status, doc)
		}
		req, _ := http.NewRequest(http.MethodGet, mcp.URL+"/mcp", nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized || len(raw) != 0 || res.Header.Get("WWW-Authenticate") != "" {
			t.Fatalf("unauthenticated GET status %d body %d auth %q", res.StatusCode, len(raw), res.Header.Get("WWW-Authenticate"))
		}
		if len(api.requests()) != 0 {
			t.Fatal("unauthenticated call reached the operator")
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		status, doc := call(t, "other-token", "tools/list", nil)
		if status != http.StatusUnauthorized || len(doc) != 0 {
			t.Fatalf("status %d doc %#v", status, doc)
		}
	})

	t.Run("tools list", func(t *testing.T) {
		status, doc := call(t, testBearer, "tools/list", map[string]any{})
		if status != http.StatusOK {
			t.Fatal(status)
		}
		result := doc["result"].(map[string]any)
		tools := result["tools"].([]any)
		if len(tools) != 10 {
			t.Fatalf("got %d tools", len(tools))
		}
		want := []string{"backlog_find", "backlog_show", "backlog_create", "backlog_groom", "backlog_order", "backlog_close", "coordinator_start", "session_status", "session_stop", "waiting_on_tom"}
		total := 0
		for i, raw := range tools {
			tool := raw.(map[string]any)
			if tool["name"] != want[i] {
				t.Fatalf("tool %d is %v", i, tool["name"])
			}
			desc := tool["description"].(string)
			n := len(strings.Fields(desc))
			if n == 0 || n > 30 {
				t.Fatalf("%s description has %d words", tool["name"], n)
			}
			total += descriptionWords(tool)
		}
		if total > 300 {
			t.Fatalf("tools/list descriptions have %d words", total)
		}
	})

	t.Run("initialize", func(t *testing.T) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26"}})
		req, _ := http.NewRequest(http.MethodPost, mcp.URL+"/mcp", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testBearer)
		req.Header.Set("Accept", "application/json, text/event-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK || len(res.Header.Get("Mcp-Session-Id")) < 8 {
			t.Fatalf("status %d session %q", res.StatusCode, res.Header.Get("Mcp-Session-Id"))
		}
	})

	backlog := []struct {
		name string
		args map[string]any
		want []string
	}{
		{"backlog_find", map[string]any{"text": "front door", "status": "Ungroomed", "epic": 516}, []string{"ARG 2=search", "ARG 3=--status=Ungroomed", "ARG 4=--parent=516", "ARG 5=--", "ARG 6=front door", "ORIGIN=meatball", "SESSION=meatball", "TOKEN_SET=yes"}},
		{"backlog_show", map[string]any{"id": 528}, []string{"ARG 2=show", "ARG 3=528"}},
		{"backlog_create", map[string]any{"epic": 516, "title": "A story", "scope": "the scope", "acceptance": "done when tested"}, []string{"ARG 2=create", "--epic=516", "--subject=A story", "--description=the scope", "--acceptance=done when tested", "meatball-516-"}},
		{"backlog_groom", map[string]any{"id": 528, "fields": map[string]any{"description": "new scope", "acceptance": "new acceptance"}}, []string{"ARG 2=groom", "ARG 3=528", "FILE", "new scope", "new acceptance", "ORIGIN=meatball"}},
		{"backlog_order", map[string]any{"id": 528, "priority": "High"}, []string{"ARG 2=order", "ARG 3=528", "--priority=High"}},
		{"backlog_close", map[string]any{"id": 528, "evidence": "tests passed"}, []string{"ARG 2=complete", "ARG 3=528", "--evidence=tests passed"}},
	}
	for _, tc := range backlog {
		t.Run(tc.name, func(t *testing.T) {
			api.reset()
			text, isError := tool(t, tc.name, tc.args)
			if isError {
				t.Fatalf("tool error: %s", text)
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
			if strings.Contains(text, "--force") {
				t.Fatal("close or groom passed --force")
			}
			if len(api.requests()) != 0 {
				t.Fatal("backlog tool called the operator")
			}
		})
	}

	t.Run("coordinator start without model", func(t *testing.T) {
		api.reset()
		api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
			t.Errorf("operator saw %s %s", r.Method, r.URL.Path)
		})
		text, isError := tool(t, "coordinator_start", map[string]any{"item": 528, "repo": "dev-env", "hours": 2, "workers": 2})
		if !isError || !strings.Contains(text, "model") {
			t.Fatalf("text %q error %v", text, isError)
		}
		if len(api.requests()) != 0 {
			t.Fatal("a request was sent")
		}
	})

	t.Run("coordinator start rejects effort before a request", func(t *testing.T) {
		api.reset()
		text, isError := tool(t, "coordinator_start", map[string]any{"item": 528, "repo": "dev-env", "model": "claude-opus-5-5", "hours": 2, "workers": 2, "effort": "max"})
		if !isError || !strings.Contains(text, "effort") {
			t.Fatalf("text %q error %v", text, isError)
		}
		if len(api.requests()) != 0 {
			t.Fatal("a request was sent")
		}
	})

	t.Run("coordinator start", func(t *testing.T) {
		api.reset()
		api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionsPath:
				writeJSON(w, 200, apiv1.SessionList{})
			case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
				var req apiv1.CreateSessionRequest
				if err := json.Unmarshal(body, &req); err != nil {
					t.Error(err)
				}
				var keys map[string]json.RawMessage
				_ = json.Unmarshal(body, &keys)
				if _, ok := keys["effort"]; ok || req.Model != "claude-opus-5-5" || req.Agent != "claude" || req.Mode != "task" || req.Profile != "dev" || req.Size != "M" || req.Repo != "dev-env" || req.IdempotencyKey != "coordinator-528" || req.Limits == nil || req.Limits.Timeout != "2h" || req.Limits.MaxWorkers == nil || *req.Limits.MaxWorkers != 3 {
					t.Errorf("request %+v", req)
				}
				if !strings.Contains(req.Prompt, "opwork show 528 --history") || !strings.Contains(req.Prompt, "2 hours") || !strings.Contains(req.Prompt, "3 workers") {
					t.Error("brief was not the shared coordinator brief")
				}
				writeJSON(w, 201, apiv1.Session{Name: "dev-env-1110-060000", Phase: "Pending", IdempotencyKey: req.IdempotencyKey})
			default:
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
				writeJSON(w, 500, apiv1.ErrorResponse{Error: apiv1.Error{Code: "internal", Message: "unexpected"}})
			}
		})
		text, isError := tool(t, "coordinator_start", map[string]any{"item": 528, "repo": "dev-env", "model": "claude-opus-5-5", "hours": 2, "workers": 3})
		if isError {
			t.Fatal(text)
		}
		if !strings.Contains(text, "dev-env-1110-060000") || !strings.Contains(text, "Pending") || !strings.Contains(text, "work_packages/528") {
			t.Fatalf("text %s", text)
		}
		reqs := api.requests()
		if len(reqs) != 2 || reqs[0].method != http.MethodGet || reqs[1].method != http.MethodPost {
			t.Fatalf("requests %+v", reqs)
		}
		for _, req := range reqs {
			if req.auth != "Bearer "+testOperator || req.limits != apiv1.CallerPolicyLimitsV1 || req.outcome != apiv1.TaskOutcomeV1 {
				t.Fatalf("operator call was not the shared client: auth set %v limits %q outcome %q", req.auth == "Bearer "+testOperator, req.limits, req.outcome)
			}
			if strings.Contains(req.auth, testBearer) || strings.Contains(string(req.body), testTracker) {
				t.Fatal("operator call carried the wrong credential")
			}
		}
	})

	refused := func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, http.StatusForbidden, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeForbidden, Message: "only sessions this caller started"}})
	}
	t.Run("session status refused", func(t *testing.T) {
		api.reset()
		api.set(refused)
		text, isError := tool(t, "session_status", map[string]any{"name": "foreign-session"})
		if !isError || !strings.Contains(text, "refused:") || !strings.Contains(text, "only sessions this caller started") {
			t.Fatalf("text %q error %v", text, isError)
		}
	})
	t.Run("session stop refused", func(t *testing.T) {
		api.reset()
		api.set(refused)
		text, isError := tool(t, "session_stop", map[string]any{"name": "foreign-session"})
		if !isError || !strings.Contains(text, "refused:") || !strings.Contains(text, "only sessions this caller started") {
			t.Fatalf("text %q error %v", text, isError)
		}
	})
	t.Run("session status by item", func(t *testing.T) {
		api.reset()
		api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch {
			case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionsPath:
				writeJSON(w, 200, apiv1.SessionList{Sessions: []apiv1.Session{{Name: "coord-1", IdempotencyKey: "coordinator-528", Phase: "Running"}}})
			case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath("coord-1"):
				writeJSON(w, 200, apiv1.Session{Name: "coord-1", Phase: "Running", IdempotencyKey: "coordinator-528"})
			default:
				writeJSON(w, 404, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeNotFound, Message: "missing"}})
			}
		})
		text, isError := tool(t, "session_status", map[string]any{"item": 528})
		if isError || !strings.Contains(text, "coord-1") || !strings.Contains(text, "Running") {
			t.Fatalf("text %q error %v", text, isError)
		}
	})

	t.Run("waiting on tom without an asks route", func(t *testing.T) {
		api.reset()
		api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
			if r.URL.Path != apiv1.AsksPath {
				t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			}
			writeJSON(w, http.StatusForbidden, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeForbidden, Message: "CallerPolicy permits creating tasks and reading or reaping only sessions this caller started"}})
		})
		text, isError := tool(t, "waiting_on_tom", map[string]any{})
		if isError {
			t.Fatal(text)
		}
		if !strings.Contains(text, "no route that lists open Asks") || !strings.Contains(text, "Needs approval,Needs answer") {
			t.Fatalf("text %s", text)
		}
	})

	t.Run("waiting on tom includes open asks", func(t *testing.T) {
		api.reset()
		api.set(func(w http.ResponseWriter, r *http.Request, body []byte) {
			writeJSON(w, 200, map[string]any{
				"asks": []map[string]any{
					{"id": "ask-open", "type": "info", "question": "Ship the front door?", "recommended": "", "allowFreeText": false, "state": "open", "createdAt": "2026-11-10T00:00:00Z", "expiresAt": "2026-11-11T00:00:00Z", "context": map[string]any{"sessionName": "coord-1", "sessionUID": "uid-1", "workPackage": 528}},
					{"id": "ask-done", "type": "info", "question": "hidden question", "recommended": "", "allowFreeText": false, "state": "answered", "createdAt": "2026-11-10T00:00:00Z", "expiresAt": "2026-11-11T00:00:00Z", "context": map[string]any{"sessionName": "coord-1", "sessionUID": "uid-1"}},
				},
				"complete": true,
			})
		})
		text, isError := tool(t, "waiting_on_tom", map[string]any{})
		if isError {
			t.Fatal(text)
		}
		if !strings.Contains(text, "ask-open") || !strings.Contains(text, "Ship the front door?") || strings.Contains(text, "hidden question") || strings.Contains(text, "uid-1") {
			t.Fatalf("text %s", text)
		}
	})

	if logs.Len() == 0 {
		t.Fatal("expected request logs")
	}
	logged := logs.String()
	for _, secret := range []string{testBearer, testOperator, testTracker} {
		if strings.Contains(logged, secret) {
			t.Fatal("log contains a credential")
		}
	}
}

func descriptionWords(v any) int {
	switch x := v.(type) {
	case map[string]any:
		n := 0
		for key, child := range x {
			if key == "description" {
				if text, ok := child.(string); ok {
					n += len(strings.Fields(text))
				}
			} else {
				n += descriptionWords(child)
			}
		}
		return n
	case []any:
		n := 0
		for _, child := range x {
			n += descriptionWords(child)
		}
		return n
	default:
		return 0
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
