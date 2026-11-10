package frontdoor

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestListenAddrDefaultsTo8080(t *testing.T) {
	if listenAddr("") != ":8080" || listenAddr(":9090") != ":9090" {
		t.Fatalf("listen %q %q", listenAddr(""), listenAddr(":9090"))
	}
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
		if len(tools) != 11 {
			t.Fatalf("got %d tools", len(tools))
		}
		want := []string{"backlog_find", "backlog_show", "backlog_create", "backlog_groom", "backlog_order", "backlog_close", "coordinator_start", "session_status", "session_stop", "waiting_on_tom", "standing_authority"}
		// readOnlyHint, destructiveHint, idempotentHint, openWorldHint.
		wantAnn := map[string][4]bool{
			"backlog_find":       {true, false, true, false},
			"backlog_show":       {true, false, true, false},
			"backlog_create":     {false, false, true, false},
			"backlog_groom":      {false, false, false, false},
			"backlog_order":      {false, false, true, false},
			"backlog_close":      {false, false, false, false},
			"coordinator_start":  {false, false, true, true},
			"session_status":     {true, false, true, false},
			"session_stop":       {false, true, true, false},
			"waiting_on_tom":     {true, false, true, false},
			"standing_authority": {true, false, true, false},
		}
		keys := []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"}
		total := 0
		for i, raw := range tools {
			tool := raw.(map[string]any)
			name, _ := tool["name"].(string)
			if name != want[i] {
				t.Fatalf("tool %d is %v", i, tool["name"])
			}
			desc := tool["description"].(string)
			n := len(strings.Fields(desc))
			if n == 0 || n > 30 {
				t.Fatalf("%s description has %d words", name, n)
			}
			total += descriptionWords(tool)
			ann, _ := tool["annotations"].(map[string]any)
			if len(ann) != len(keys) {
				t.Fatalf("%s annotations %#v", name, tool["annotations"])
			}
			for k, key := range keys {
				got, ok := ann[key].(bool)
				if !ok || got != wantAnn[name][k] {
					t.Fatalf("%s %s = %#v, want %v", name, key, ann[key], wantAnn[name][k])
				}
			}
		}
		if total != descriptionWordBound || descriptionWordBound != 150 {
			t.Fatalf("tools/list descriptions have %d words; bound %d; measured contract 150", total, descriptionWordBound)
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
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || len(res.Header.Get("Mcp-Session-Id")) < 8 {
			t.Fatalf("status %d session %q", res.StatusCode, res.Header.Get("Mcp-Session-Id"))
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		result, _ := doc["result"].(map[string]any)
		instr, _ := result["instructions"].(string)
		if instr != serverInstructions {
			t.Fatalf("instructions %q", instr)
		}
		n := len(strings.Fields(instr))
		if n > 120 || n != 58 {
			t.Fatalf("instructions have %d words", n)
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

func startDoor(t *testing.T, script string) (string, *fakeAPI, *bytes.Buffer, http.Handler) {
	t.Helper()
	t.Setenv("NO_PROXY", "127.0.0.1,localhost")
	t.Setenv("no_proxy", "127.0.0.1,localhost")
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bearer"), testBearer+"\n")
	writeFile(t, filepath.Join(dir, "operator"), testOperator+"\n")
	writeFile(t, filepath.Join(dir, "tracker"), testTracker+"\n")
	stub := filepath.Join(dir, "opwork")
	writeFile(t, stub, script)
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
	return mcp.URL, api, &logs, h
}

func postMCP(t *testing.T, client *http.Client, base, token, method string, params any) (int, []byte) {
	t.Helper()
	status, raw, err := exchange(client, base+"/mcp", token, mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}))
	if err != nil {
		t.Fatal(err)
	}
	return status, raw
}

func postRaw(t *testing.T, client *http.Client, url, token string, body []byte) (int, []byte) {
	t.Helper()
	status, raw, err := exchange(client, url, token, body)
	if err != nil {
		t.Fatal(err)
	}
	return status, raw
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func exchange(client *http.Client, url, token string, body []byte) (int, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	return res.StatusCode, raw, err
}

func toolResult(t *testing.T, status int, raw []byte) (string, bool) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("body %q", raw)
	}
	result, _ := doc["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if result == nil || len(content) != 1 {
		t.Fatalf("result %#v", doc)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError
}

func TestMutatingCallWithoutTokenDoesNoWork(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "opwork-invocations")
	authPath := filepath.Join(dir, "authority")
	writeFile(t, authPath, "authority-sentinel-value\n")
	base, api, _, h := startDoor(t, fmt.Sprintf("#!/bin/sh\nprintf 'x\\n' >> %q\n", count))
	h.(*handler).authorityFile = authPath
	calls := []struct {
		name string
		args map[string]any
	}{
		{"coordinator_start", map[string]any{"item": 528, "repo": "dev-env", "model": "claude-opus-5-5", "hours": 2, "workers": 2}},
		{"backlog_close", map[string]any{"id": 528, "evidence": "tests passed"}},
		{"standing_authority", map[string]any{}},
	}
	for _, token := range []string{"", "wrong-token"} {
		for _, call := range calls {
			api.reset()
			status, raw := postMCP(t, nil, base, token, "tools/call", map[string]any{"name": call.name, "arguments": call.args})
			if status != http.StatusUnauthorized || len(raw) != 0 || strings.Contains(string(raw), "authority-sentinel-value") {
				t.Fatalf("%s token rejected as status %d body %d", call.name, status, len(raw))
			}
			if len(api.requests()) != 0 {
				t.Fatalf("%s reached the operator", call.name)
			}
			if _, err := os.Stat(count); !os.IsNotExist(err) {
				t.Fatalf("%s invoked opwork", call.name)
			}
		}
	}
	api.reset()
	status, raw := postMCP(t, nil, base, testBearer, "tools/call", map[string]any{"name": "backlog_close", "arguments": calls[1].args})
	text, isError := toolResult(t, status, raw)
	if isError {
		t.Fatal(text)
	}
	got, err := os.ReadFile(count)
	if err != nil || len(got) == 0 {
		t.Fatal("a valid close did not invoke opwork")
	}
	if len(api.requests()) != 0 {
		t.Fatal("close reached the operator")
	}
	api.reset()
	status, raw = postMCP(t, nil, base, testBearer, "tools/call", map[string]any{"name": "coordinator_start", "arguments": calls[0].args})
	_, _ = toolResult(t, status, raw)
	if len(api.requests()) == 0 {
		t.Fatal("a valid coordinator start made no operator request")
	}
}

func TestStandingAuthority(t *testing.T) {
	base, api, _, h := startDoor(t, stubScript)
	door := h.(*handler)
	call := func(args map[string]any) (string, bool) {
		t.Helper()
		status, raw := postMCP(t, nil, base, testBearer, "tools/call", map[string]any{"name": "standing_authority", "arguments": args})
		return toolResult(t, status, raw)
	}
	text, isError := call(map[string]any{})
	if isError || text != noStandingAuthority {
		t.Fatalf("no flag: error %v text %q", isError, text)
	}
	dir := t.TempDir()
	door.authorityFile = filepath.Join(dir, "missing")
	text, isError = call(map[string]any{})
	if isError || text != noStandingAuthority {
		t.Fatalf("missing: error %v text %q", isError, text)
	}
	empty := filepath.Join(dir, "empty")
	writeFile(t, empty, "")
	door.authorityFile = empty
	text, isError = call(map[string]any{})
	if isError || text != noStandingAuthority {
		t.Fatalf("empty: error %v text %q", isError, text)
	}
	path := filepath.Join(dir, "authority")
	writeFile(t, path, "first\n\tline")
	door.authorityFile = path
	text, isError = call(map[string]any{})
	if isError || text != "first\n\tline" {
		t.Fatalf("file: error %v text %q", isError, text)
	}
	writeFile(t, path, "second line\n")
	text, isError = call(map[string]any{})
	if isError || text != "second line\n" {
		t.Fatalf("reread: error %v text %q", isError, text)
	}
	writeFile(t, path, "\n")
	text, isError = call(map[string]any{})
	if isError || text != "\n" {
		t.Fatalf("newline: error %v text %q", isError, text)
	}
	exact := strings.Repeat("a", authorityMaxBytes)
	writeFile(t, path, exact)
	text, isError = call(map[string]any{})
	if isError || text != exact {
		t.Fatalf("cap: error %v len %d", isError, len(text))
	}
	writeFile(t, path, strings.Repeat("b", authorityMaxBytes+1))
	text, isError = call(map[string]any{})
	if !isError || text != "the standing authority file is larger than 4096 bytes" || strings.Contains(text, "bbb") {
		t.Fatalf("oversize: error %v text %q", isError, text)
	}
	writeFile(t, path, "restored\n")
	text, isError = call(map[string]any{})
	if isError || text != "restored\n" {
		t.Fatalf("reread after error: error %v text %q", isError, text)
	}
	writeFile(t, path, "bad\r\ntext")
	text, isError = call(map[string]any{})
	if !isError || text != "the standing authority file contains a control character" || strings.Contains(text, "bad") {
		t.Fatalf("control: error %v text %q", isError, text)
	}
	if err := os.WriteFile(path, []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	text, isError = call(map[string]any{})
	if !isError || text != "the standing authority file is not UTF-8 text" {
		t.Fatalf("utf8: error %v text %q", isError, text)
	}
	other := filepath.Join(dir, "other")
	writeFile(t, other, "other-secret-marker")
	text, isError = call(map[string]any{"path": other})
	if !isError || strings.Contains(text, "other-secret-marker") || strings.Contains(text, other) {
		t.Fatalf("caller path: error %v text %q", isError, text)
	}
	door.authorityFile = dir
	text, isError = call(map[string]any{})
	if !isError || text != "the standing authority file cannot be read" || strings.Contains(text, "other-secret-marker") {
		t.Fatalf("directory: error %v text %q", isError, text)
	}
	door.authorityFile = ""
	text, isError = call(map[string]any{})
	if isError || text != noStandingAuthority {
		t.Fatalf("cleared flag: error %v text %q", isError, text)
	}
	if len(api.requests()) != 0 {
		t.Fatal("standing authority called the operator")
	}
}

func TestToolCallsAreLimited(t *testing.T) {
	if toolDeadline != 90*time.Second || toolSlots != 4 {
		t.Fatalf("deadline %s slots %d", toolDeadline, toolSlots)
	}
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	release := filepath.Join(dir, "release")
	base, api, _, _ := startDoor(t, fmt.Sprintf("#!/bin/sh\nmkdir -p %q\necho $$ > %q/$$\nwhile [ ! -f %q ]; do sleep 1; done\n", pids, pids, release))
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("x"), 0o600) })
	var wg sync.WaitGroup
	errs := make(chan error, toolSlots)
	body := mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "backlog_show", "arguments": map[string]any{"id": 528}}})
	for i := 0; i < toolSlots; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw, err := exchange(nil, base+"/mcp", testBearer, body)
			if err != nil {
				errs <- err
				return
			}
			if status != http.StatusOK || strings.Contains(string(raw), "busy, try again") {
				errs <- fmt.Errorf("admitted call status %d", status)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(pids)
		if len(entries) >= toolSlots {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("started %d opwork processes", len(entries))
		}
		time.Sleep(20 * time.Millisecond)
	}
	started := time.Now()
	client := &http.Client{Timeout: 3 * time.Second}
	status, raw := postMCP(t, client, base, testBearer, "tools/call", map[string]any{
		"name": "coordinator_start", "arguments": map[string]any{"item": 528, "repo": "dev-env", "model": "claude-opus-5-5", "hours": 2, "workers": 2},
	})
	if time.Since(started) > 2*time.Second {
		t.Fatal("the extra call waited for a slot")
	}
	text, isError := toolResult(t, status, raw)
	if !isError || text != "busy, try again" {
		t.Fatalf("text %q error %v", text, isError)
	}
	entries, _ := os.ReadDir(pids)
	if len(entries) != toolSlots {
		t.Fatalf("opwork processes %d", len(entries))
	}
	if len(api.requests()) != 0 {
		t.Fatal("the extra call reached the operator")
	}
	if err := os.WriteFile(release, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("admitted calls did not finish")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestToolCallDeadlineIsShared(t *testing.T) {
	if toolDeadline != 90*time.Second {
		t.Fatalf("deadline %s", toolDeadline)
	}
	t.Cleanup(func() { toolDeadline = 90 * time.Second })
	toolDeadline = time.Second
	base, api, _, _ := startDoor(t, "#!/bin/sh\nprintf 'started\\n'\nexec sleep 8\n")
	api.set(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	client := &http.Client{Timeout: 8 * time.Second}
	for _, call := range []struct {
		name, want string
		args       map[string]any
	}{
		{name: "session_status", args: map[string]any{"name": "coord-1"}, want: "deadline"},
		{name: "backlog_show", args: map[string]any{"id": 528}, want: "timed out"},
	} {
		t.Run(call.name, func(t *testing.T) {
			started := time.Now()
			status, raw := postMCP(t, client, base, testBearer, "tools/call", map[string]any{"name": call.name, "arguments": call.args})
			if time.Since(started) >= 4*time.Second {
				t.Fatal("the call used more than the tool deadline")
			}
			text, isError := toolResult(t, status, raw)
			if !isError || !strings.Contains(text, call.want) {
				t.Fatalf("text %q error %v", text, isError)
			}
		})
	}
}

func TestToolCallStopsWhenTheClientDisconnects(t *testing.T) {
	if toolDeadline != 90*time.Second {
		t.Fatalf("deadline %s", toolDeadline)
	}
	base, api, _, _ := startDoor(t, "#!/bin/sh\nprintf 'started\\n'\nexec sleep 8\n")
	entered := make(chan struct{})
	done := make(chan struct{})
	var enteredOnce, doneOnce sync.Once
	api.set(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		enteredOnce.Do(func() { close(entered) })
		<-r.Context().Done()
		doneOnce.Do(func() { close(done) })
		w.WriteHeader(http.StatusGatewayTimeout)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "session_status", "arguments": map[string]any{"name": "coord-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		res, err := http.DefaultClient.Do(req)
		if res != nil {
			_, _ = io.Copy(io.Discard, res.Body)
			_ = res.Body.Close()
		}
		_ = err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the operator was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the operator request kept running after the client disconnected")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("the client call did not return")
	}
}

func TestLogsUseFixedLabels(t *testing.T) {
	base, _, logs, _ := startDoor(t, stubScript)
	status, raw := postRaw(t, nil, base+"/path-secret-value", "", nil)
	if status != http.StatusUnauthorized || len(raw) != 0 {
		t.Fatalf("status %d body %d", status, len(raw))
	}
	secretMethod, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "method-secret-value\nforged-log-line", "params": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	status, _ = postRaw(t, nil, base+"/mcp", testBearer, secretMethod)
	if status != http.StatusOK {
		t.Fatalf("status %d", status)
	}
	logged := logs.String()
	for _, secret := range []string{"path-secret-value", "method-secret-value", "forged-log-line", testBearer, testOperator, testTracker} {
		if strings.Contains(logged, secret) {
			t.Fatal("log retained a request value")
		}
	}
	if !strings.Contains(logged, "unknown") {
		t.Fatalf("log %q", logged)
	}
	if strings.Count(logged, "\n") < 2 {
		t.Fatalf("log %q", logged)
	}
}

func TestOptionsStarIsUnauthorized(t *testing.T) {
	_, _, logs, h := startDoor(t, stubScript)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newHTTPServer(ln.Addr().String(), h, log.New(logs, "", 0))
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	raw := optionsStar(t, ln.Addr().String(), "")
	if !strings.Contains(raw, "HTTP/1.1 401") || strings.Contains(raw, "WWW-Authenticate") {
		t.Fatalf("response %q", raw)
	}
	parts := strings.SplitN(raw, "\r\n\r\n", 2)
	if len(parts) != 2 || parts[1] != "" {
		t.Fatalf("body %q", raw)
	}
	authed := optionsStar(t, ln.Addr().String(), testBearer)
	if strings.Contains(authed, "HTTP/1.1 200") || !strings.Contains(authed, "HTTP/1.1 404") {
		t.Fatalf("authenticated response %q", authed)
	}
	if strings.Contains(logs.String(), "*") {
		t.Fatal("log retained the target")
	}
}

func optionsStar(t *testing.T, addr, token string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	header := ""
	if token != "" {
		header = "Authorization: Bearer " + token + "\r\n"
	}
	if _, err := fmt.Fprintf(conn, "OPTIONS * HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n%s\r\n", addr, header); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tmp := make([]byte, 1024)
	for {
		n, err := conn.Read(tmp)
		if n > 0 {
			_, _ = buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
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
