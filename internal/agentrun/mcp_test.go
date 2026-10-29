package agentrun

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

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
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"kube","reason":"read pods","role":"dev-env-grant-workloads"}}}`,
		"",
	}, "\n"))
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
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"later","reason":"x"}}}` + "\n")
	h.mustRun(ExitOK, "mcp")
	if len(h.api.requests()) != before {
		t.Fatal("a refused tool call reached the API")
	}
	if !strings.Contains(h.stdout.String(), `"isError":true`) {
		t.Fatalf("refused call %s", h.stdout.String())
	}
}

func TestAccessMCPInitializeDoesNotConnect(t *testing.T) {
	h := newHarness(t)
	h.vars[envTokenFile] = filepath.Join(h.dir, "missing-token")
	h.env.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":7,\"method\":\"initialize\"}\n")
	h.mustRun(ExitOK, "mcp")
	if !strings.Contains(h.stdout.String(), `"id":7`) || !strings.Contains(h.stdout.String(), `"name":"dev-env"`) {
		t.Fatalf("initialize %s", h.stdout.String())
	}
	if len(h.api.requests()) != 0 {
		t.Fatal("initialize called the API")
	}
}

func TestAccessMCPEgressProtocolsMatchTheGrantSchema(t *testing.T) {
	for _, proto := range []string{"tcp", "udp", "TCP", "UDP", ""} {
		t.Run(proto, func(t *testing.T) {
			h := newHarness(t)
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantsPath: func(r *http.Request) (int, string) {
					var req apiv1.CreateGrantRequest
					requests := h.api.requests()
					if err := json.Unmarshal(requests[len(requests)-1].body, &req); err != nil {
						t.Error(err)
					}
					if len(req.Ports) != 1 || (req.Ports[0].Protocol != "" && req.Ports[0].Protocol != "TCP" && req.Ports[0].Protocol != "UDP") {
						return http.StatusUnprocessableEntity, `{"code":"invalid","message":"protocol must be TCP or UDP"}`
					}
					return http.StatusCreated, `{"name":"grant-abc","phase":"Pending","type":"egress"}`
				},
			})
			h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"egress","reason":"read the registry","fqdns":["ghcr.io"],"ports":[{"port":443,"protocol":"` + proto + `"}]}}}` + "\n")
			h.mustRun(ExitOK, "mcp")
			msgs := decodeMCPFrames(t, h.stdout.String())
			if len(msgs) != 1 || !strings.Contains(msgs[0], `"isError":false`) {
				t.Fatalf("egress request refused: %s", h.stdout.String())
			}
		})
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
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"grant_status","arguments":{"phase":"Active","mine":true}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"release_access","arguments":{"name":"grant-abc"}}}`,
		"",
	}, "\n"))
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
	sc := newMCPScanner(strings.NewReader(raw))
	var out []string
	for {
		body, err := nextMCPMessage(sc)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(body))
	}
}

func TestAccessMCPHWSSHCarriesTheNotice(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(*http.Request) (int, string) {
			return http.StatusCreated, `{"name":"grant-1017-160000-ab12","phase":"Pending","type":"credential","credential":"hw-ssh","session":"task-1","ttl":"1h0m0s","reason":"look at a node"}`
		},
	})
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"credential","credential":"hw-ssh","reason":"look at a node"}}}`,
		"",
	}, "\n"))
	h.mustRun(ExitOK, "mcp")
	msgs := decodeMCPFrames(t, h.stdout.String())
	if len(msgs) != 2 || !strings.Contains(msgs[1], "hw-ssh notice") || !strings.Contains(msgs[1], "not containment and not revocation") {
		t.Fatalf("call %v", msgs)
	}
}

func TestAsksMCPToolSchemas(t *testing.T) {
	h := newHarness(t)
	h.vars[envTokenFile] = filepath.Join(h.dir, "missing-token")
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	h.mustRun(ExitOK, "mcp")
	var reply struct {
		Result struct {
			Tools []struct {
				Name   string         `json:"name"`
				Schema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(h.stdout.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]map[string]any{}
	for _, tool := range reply.Result.Tools {
		if _, exists := schemas[tool.Name]; exists {
			t.Fatalf("duplicate tool %s", tool.Name)
		}
		if tool.Schema["type"] != "object" {
			t.Errorf("%s input schema must have an object root: %v", tool.Name, tool.Schema)
		}
		for _, keyword := range []string{"oneOf", "anyOf", "allOf", "not", "if", "then", "else", "enum", "$ref"} {
			if _, exists := tool.Schema[keyword]; exists {
				t.Errorf("%s input schema has unsupported top-level %s", tool.Name, keyword)
			}
		}
		schemas[tool.Name] = tool.Schema
	}
	for _, name := range []string{"request_access", "grant_status", "release_access", "ask", "ask_status", "list_my_asks", "withdraw"} {
		if schemas[name] == nil {
			t.Fatalf("missing tool %s", name)
		}
	}
	if len(schemas) != 7 || len(h.api.requests()) != 0 {
		t.Fatalf("tools/list advertised unexpected tools or called the API: %s", h.stdout.String())
	}
	for _, name := range []string{"ask", "ask_status", "list_my_asks", "withdraw"} {
		schema := schemas[name]
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Errorf("%s must accept only its defined object fields: %v", name, schema)
		}
	}
	props := schemas["ask"]["properties"].(map[string]any)
	if !reflect.DeepEqual(schemas["ask"]["required"], []any{"requestId", "type", "question"}) {
		t.Errorf("create required fields: %v", schemas["ask"]["required"])
	}
	if !reflect.DeepEqual(props["type"].(map[string]any)["enum"], []any{"info", "approval"}) {
		t.Errorf("ask types: %v", props["type"])
	}
	assertBounds := func(name string, schema map[string]any, minKey string, min float64, maxKey string, max float64) {
		t.Helper()
		if schema[minKey] != min || schema[maxKey] != max {
			t.Errorf("%s bounds: %v, want %s=%v %s=%v", name, schema, minKey, min, maxKey, max)
		}
	}
	assertBounds("question", props["question"].(map[string]any), "minLength", 1, "maxLength", 1000)
	assertBounds("requestId", props["requestId"].(map[string]any), "minLength", 8, "maxLength", 64)
	if props["requestId"].(map[string]any)["pattern"] != "^[A-Za-z0-9-]{8,64}$" || props["expiresAt"] != nil || !strings.Contains(props["expiresIn"].(map[string]any)["description"].(string), "168h") {
		t.Errorf("create idempotency/expiry schema: %v", props)
	}
	for _, phrase := range []string{"whole seconds", "1m", "24h", "90m", "7d is not accepted", "scope.expiresAt"} {
		if !strings.Contains(props["expiresIn"].(map[string]any)["description"].(string), phrase) {
			t.Errorf("expiresIn help missing %q", phrase)
		}
	}
	for _, name := range []string{"requestId", "type", "question", "options", "allowFreeText", "context", "scope"} {
		if description, _ := props[name].(map[string]any)["description"].(string); description == "" {
			t.Errorf("%s needs a description for the caller", name)
		}
	}
	if !strings.Contains(props["requestId"].(map[string]any)["description"].(string), "new unique value") {
		t.Fatal("requestId must explain when to use a new value")
	}
	for _, name := range []string{"options", "allowFreeText"} {
		if !strings.Contains(props[name].(map[string]any)["description"].(string), "An info ask needs at least one option or allowFreeText: true") {
			t.Errorf("%s must explain the info answer requirement", name)
		}
	}
	options := props["options"].(map[string]any)
	if options["maxItems"] != float64(6) {
		t.Errorf("options: %v", options)
	}
	option := options["items"].(map[string]any)
	optionProps := option["properties"].(map[string]any)
	assertBounds("option label", optionProps["label"].(map[string]any), "minLength", 1, "maxLength", 80)
	assertBounds("option description", optionProps["description"].(map[string]any), "minLength", 0, "maxLength", 300)
	pattern := regexp.MustCompile(optionProps["id"].(map[string]any)["pattern"].(string))
	if !pattern.MatchString("a-1") || !pattern.MatchString(strings.Repeat("a", 24)) || pattern.MatchString("A") || pattern.MatchString(strings.Repeat("a", 25)) {
		t.Errorf("option id pattern: %s", pattern)
	}
	contextSchema := props["context"].(map[string]any)
	contextProps := contextSchema["properties"].(map[string]any)
	links := contextProps["links"].(map[string]any)
	link := links["items"].(map[string]any)
	if links["maxItems"] != float64(5) || link["maxLength"] != float64(300) || link["pattern"] != "^https://[!-~]+$" || contextSchema["additionalProperties"] != false {
		t.Errorf("context bounds: %v", contextSchema)
	}
	if contextProps["sessionName"] != nil || contextProps["sessionUID"] != nil || props["answer"] != nil || props["approved"] != nil {
		t.Fatal("server-owned fields were exposed as ask inputs")
	}
	scope := props["scope"].(map[string]any)
	scopeProps := scope["properties"].(map[string]any)
	assertBounds("scope action", scopeProps["action"].(map[string]any), "minLength", 1, "maxLength", 200)
	assertBounds("scope target", scopeProps["target"].(map[string]any), "minLength", 1, "maxLength", 200)
	if !reflect.DeepEqual(scope["required"], []any{"action", "target", "expiresAt"}) || !strings.Contains(scope["description"].(string), "Required when type is approval; omit for info") {
		t.Errorf("approval scope requirement: %v", schemas["ask"])
	}
	for _, expiry := range []map[string]any{scopeProps["expiresAt"].(map[string]any)} {
		if expiry["format"] != "date-time" || !strings.Contains(expiry["description"].(string), "24 hours") {
			t.Errorf("expiry bounds: %v", expiry)
		}
	}
	listProps := schemas["list_my_asks"]["properties"].(map[string]any)
	assertBounds("list limit", listProps["limit"].(map[string]any), "minimum", 1, "maximum", 100)
	if listProps["cursor"].(map[string]any)["maxLength"] != float64(4096) {
		t.Fatal("list schema must bound the cursor to 4096")
	}
	if listProps["limit"].(map[string]any)["default"] != float64(apiv1.AskListDefaultLimit) {
		t.Fatal("list schema must advertise the shared default limit")
	}
	for _, name := range []string{"ask_status", "withdraw"} {
		if !reflect.DeepEqual(schemas[name]["required"], []any{"id"}) {
			t.Errorf("%s required id: %v", name, schemas[name])
		}
		id := schemas[name]["properties"].(map[string]any)["id"].(map[string]any)
		if description, _ := id["description"].(string); description == "" {
			t.Errorf("%s must describe the ask id", name)
		}
		pattern := regexp.MustCompile(id["pattern"].(string))
		if !pattern.MatchString("ask-0123456789abcdef") || pattern.MatchString("ask-ABCDEF0123456789") || pattern.MatchString("../answer") {
			t.Errorf("%s id pattern: %s", name, pattern)
		}
	}
}

func TestAsksMCPCreateStatusListWithdraw(t *testing.T) {
	const id = "ask-0123456789abcdef"
	open := askJSON(t, askFixture(apiv1.AskStateOpen))
	answeredAsk := askFixture(apiv1.AskStateAnswered)
	answered := askJSON(t, answeredAsk)
	list := askJSON(t, apiv1.AskList{Asks: []apiv1.Ask{answeredAsk}, NextCursor: "next/page+1"})
	withdrawnAsk := askFixture(apiv1.AskStateWithdrawn)
	withdrawnAsk.ID = "ask-fedcba9876543210"
	withdrawn := askJSON(t, withdrawnAsk)
	h := newHarness(t)
	reads := 0
	serveRaw(h, map[string]func(*http.Request) (int, string){
		"/v1/asks": func(r *http.Request) (int, string) {
			if r.Method == http.MethodPost {
				return http.StatusCreated, open
			}
			if r.Method != http.MethodGet || r.URL.Query().Get("mine") != "true" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("cursor") != "page/+1" {
				t.Errorf("list request %s %s", r.Method, r.URL)
			}
			return http.StatusOK, list
		},
		"/v1/asks/" + id: func(r *http.Request) (int, string) {
			if r.Method != http.MethodGet {
				t.Errorf("status method %s", r.Method)
			}
			reads++
			if reads == 1 {
				return http.StatusOK, open
			}
			return http.StatusOK, answered
		},
		"/v1/asks/ask-fedcba9876543210/withdraw": func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("withdraw method %s", r.Method)
			}
			return http.StatusOK, withdrawn
		},
	})
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask","arguments":{"requestId":"region-257","type":"info","question":"Which region?","options":[{"id":"east","label":"East"}],"recommended":"east","allowFreeText":true,"context":{"workPackage":257,"links":["https://example.com/context"]}}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"ask_status","arguments":{"id":"` + id + `"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_status","arguments":{"id":"` + id + `"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"list_my_asks","arguments":{"limit":2,"cursor":"page/+1"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"withdraw","arguments":{"id":"ask-fedcba9876543210"}}}`,
		"",
	}, "\n"))
	h.mustRun(ExitOK, "mcp")
	frames := decodeMCPFrames(t, h.stdout.String())
	if len(frames) != 5 || len(h.api.requests()) != 5 || len(h.sleeps) != 0 {
		t.Fatalf("expected five immediate API calls and results: requests=%v sleeps=%v stdout=%s", h.api.requests(), h.sleeps, h.stdout.String())
	}
	for i, want := range []string{open, open, answered, list, withdrawn} {
		var reply struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(frames[i]), &reply); err != nil {
			t.Fatal(err)
		}
		if reply.Result.IsError || len(reply.Result.Content) != 1 || reply.Result.Content[0].Type != "text" {
			t.Fatalf("tool response %s", frames[i])
		}
		var gotJSON, wantJSON any
		if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &gotJSON); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotJSON, wantJSON) {
			t.Errorf("tool %d JSON: got %v, want %v", i, gotJSON, wantJSON)
		}
	}
	var create map[string]any
	if err := json.Unmarshal(h.api.requests()[0].body, &create); err != nil {
		t.Fatal(err)
	}
	if create["requestId"] != "region-257" || create["recommended"] != "east" || create["allowFreeText"] != true {
		t.Errorf("create intent %v", create)
	}
}

func TestAsksMCPInvalidInputsDoNotReachAPI(t *testing.T) {
	createArgs := func(changes map[string]any) string {
		t.Helper()
		args := map[string]any{"requestId": "validation-257", "type": "info", "question": "Which region?", "allowFreeText": true}
		for key, value := range changes {
			args[key] = value
		}
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for _, tc := range []struct{ tool, args string }{
		{"ask", `{}`},
		{"ask", `null`},
		{"ask", `[]`},
		{"ask", createArgs(map[string]any{"requestId": ""})},
		{"ask", createArgs(map[string]any{"type": "grant"})},
		{"ask", createArgs(map[string]any{"allowFreeText": false})},
		{"ask", createArgs(map[string]any{"scope": map[string]any{"action": "Apply", "target": "fixture", "expiresAt": "2026-10-07T21:00:00Z"}})},
		{"ask", createArgs(map[string]any{"question": strings.Repeat("界", 1001)})},
		{"ask", createArgs(map[string]any{"options": []map[string]string{{"id": "A", "label": "Invalid"}}})},
		{"ask", createArgs(map[string]any{"options": []map[string]string{{"id": "a", "label": strings.Repeat("界", 81)}}})},
		{"ask", createArgs(map[string]any{"options": []map[string]string{{"id": "a", "label": "A", "description": strings.Repeat("界", 301)}}})},
		{"ask", createArgs(map[string]any{"options": []map[string]string{{"id": "a", "label": "A"}, {"id": "b", "label": "B"}, {"id": "c", "label": "C"}, {"id": "d", "label": "D"}, {"id": "e", "label": "E"}, {"id": "f", "label": "F"}, {"id": "g", "label": "G"}}})},
		{"ask", createArgs(map[string]any{"recommended": "missing"})},
		{"ask", createArgs(map[string]any{"context": map[string]any{"links": []string{"http://example.com"}}})},
		{"ask", createArgs(map[string]any{"context": map[string]any{"sessionName": "another-session"}})},
		{"ask", createArgs(map[string]any{"context": map[string]any{"sessionUID": "another-uid"}})},
		{"ask", createArgs(map[string]any{"context": map[string]any{"links": []string{"https://example.com/" + strings.Repeat("a", 301)}}})},
		{"ask", createArgs(map[string]any{"context": map[string]any{"links": []string{"https://a.example", "https://b.example", "https://c.example", "https://d.example", "https://e.example", "https://f.example"}}})},
		{"ask", createArgs(map[string]any{"type": "approval"})},
		{"ask", createArgs(map[string]any{"type": "approval", "scope": map[string]any{"action": strings.Repeat("界", 201), "target": "fixture", "expiresAt": "2026-10-07T21:00:00Z"}})},
		{"ask", createArgs(map[string]any{"type": "approval", "scope": map[string]any{"action": "Apply", "target": strings.Repeat("界", 201), "expiresAt": "2026-10-07T21:00:00Z"}})},
		{"ask", createArgs(map[string]any{"expiresIn": "169h"})},
		{"ask", createArgs(map[string]any{"expiresIn": "7d"})},
		{"ask", createArgs(map[string]any{"expiresIn": "1m0.5s"})},
		{"ask", createArgs(map[string]any{"expiresIn": "59s"})},
		{"ask", createArgs(map[string]any{"question": "\u115f"})},
		{"ask", createArgs(map[string]any{"question": "\u2800"})},
		{"ask", createArgs(map[string]any{"question": "text\u034f"})},
		{"ask", createArgs(map[string]any{"question": "text\U000e0100"})},
		{"ask", createArgs(map[string]any{"options": []map[string]string{{"id": "a", "label": "Has\ttab"}}})},
		{"ask", createArgs(map[string]any{"answer": map[string]any{"approved": true}})},
		{"ask_status", `{}`},
		{"ask_status", `{"id":"../portal/asks/answer"}`},
		{"ask_status", `{"id":"ask-0123456789abcdef","answer":{}}`},
		{"list_my_asks", `{"limit":0}`},
		{"list_my_asks", `{"limit":101}`},
		{"list_my_asks", `{"limit":1.5}`},
		{"list_my_asks", `{"mine":false}`},
		{"withdraw", `{}`},
		{"withdraw", `{"id":"ask-ABCDEF0123456789"}`},
		{"withdraw", `{"id":"ask-0123456789abcdef","approved":true}`},
		{"answer", `{"id":"ask-0123456789abcdef","approved":true}`},
	} {
		t.Run(tc.tool+"/"+tc.args, func(t *testing.T) {
			h := newHarness(t)
			h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.tool + `","arguments":` + tc.args + `}}` + "\n")
			h.mustRun(ExitOK, "mcp")
			if !strings.Contains(h.stdout.String(), `"isError":true`) || len(h.api.requests()) != 0 {
				t.Fatalf("invalid input reached API or succeeded: requests=%v stdout=%s", h.api.requests(), h.stdout.String())
			}
		})
	}
}

func TestAsksMCPReturnsOperatorErrors(t *testing.T) {
	for _, tc := range []struct {
		tool, args string
		status     int
	}{
		{"ask", `{"requestId":"disabled-257","type":"info","question":"Which region?","allowFreeText":true}`, 404},
		{"ask", `{"requestId":"conflict-257","type":"info","question":"Which region?","allowFreeText":true}`, 409},
		{"ask_status", `{"id":"ask-0123456789abcdef"}`, 403},
		{"list_my_asks", `{}`, 404},
		{"withdraw", `{"id":"ask-0123456789abcdef"}`, 409},
		{"withdraw", `{"id":"ask-0123456789abcdef"}`, 410},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
				apiError(w, tc.status, "refused", "operator detail")
			}
			h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.tool + `","arguments":` + tc.args + `}}` + "\n")
			h.mustRun(ExitOK, "mcp")
			contains(t, "tool error", h.stdout.String(), `"isError":true`, "operator detail")
			if tc.status == 403 || tc.status == 404 {
				contains(t, "tool error", h.stdout.String(), "asks are not enabled on this operator")
			}
			if tc.status == 409 {
				if tc.tool == "ask" {
					contains(t, "tool error", h.stdout.String(), "requestId conflicts with a different intent")
				} else {
					contains(t, "tool error", h.stdout.String(), "ask is not open")
				}
			}
			if tc.status == 410 {
				contains(t, "tool error", h.stdout.String(), "ask has expired")
			}
			if len(h.api.requests()) != 1 {
				t.Fatalf("tool made unexpected requests: %v", h.api.requests())
			}
		})
	}
}

func TestAsksMCPReadTimeStateAndApproval(t *testing.T) {
	for _, tc := range []struct {
		name          string
		approval      bool
		answered      bool
		approved      bool
		afterCreation time.Duration
		wantState     string
		wantValid     bool
	}{
		{"info at expiry", false, false, false, 24 * time.Hour, "expired", false},
		{"info before expiry", false, false, false, 24*time.Hour - time.Second, "open", false},
		{"approval at expiry", true, false, false, 24 * time.Hour, "expired", false},
		{"pending approval", true, false, false, time.Minute, "open", false},
		{"granted approval", true, true, true, 2 * time.Minute, "answered", true},
		{"denied approval", true, true, false, 2 * time.Minute, "answered", false},
		{"lapsed approval", true, true, true, 24 * time.Hour, "answered", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.now = testNow.Add(tc.afterCreation)
			record := askFixture(apiv1.AskStateOpen)
			if tc.approval {
				record.Type = apiv1.AskTypeApproval
				record.Scope = &apiv1.AskScope{Action: "Apply", Target: "Fixture", ExpiresAt: record.ExpiresAt}
				record.Options = nil
				record.Recommended = ""
				record.AllowFreeText = false
			}
			if tc.answered {
				record.State = apiv1.AskStateAnswered
				record.Answer = &apiv1.AskAnswer{
					Approved: &tc.approved, Text: "Approval note\nSecond line", AnsweredAt: testNow.Add(time.Minute),
					AnsweredBy: apiv1.AskActor{Issuer: "https://identity.example/private", Subject: "private-subject", DisplayName: "Tom"},
				}
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path == apiv1.AsksPath {
					if r.URL.Query().Get(apiv1.AskQueryMine) != "true" || r.URL.Query().Get(apiv1.AskQueryLimit) != "20" {
						t.Errorf("wrong default list query: %s", r.URL.RawQuery)
					}
					writeTestJSON(w, http.StatusOK, apiv1.AskList{Asks: []apiv1.Ask{record}, NextCursor: "next/+page"})
					return
				}
				if r.URL.Path != apiv1.AskPath(testAskID) {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				writeTestJSON(w, http.StatusOK, record)
			}
			h.env.Stdin = strings.NewReader(
				`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_status","arguments":{"id":"` + testAskID + `"}}}` + "\n" +
					`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_my_asks","arguments":{}}}` + "\n")
			h.mustRun(ExitOK, "mcp")
			frames := decodeMCPFrames(t, h.stdout.String())
			if len(frames) != 2 || len(h.api.requests()) != 2 || len(h.sleeps) != 0 {
				t.Fatal("reads must make exactly one immediate API call each")
			}
			for i, frame := range frames {
				var reply struct {
					Result struct {
						IsError bool                    `json:"isError"`
						Content []struct{ Text string } `json:"content"`
					}
				}
				if err := json.Unmarshal([]byte(frame), &reply); err != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
					t.Fatal("invalid MCP read result")
				}
				text := reply.Result.Content[0].Text
				for _, forbidden := range []string{"issuer", "subject", "https://identity.example/private", "private-subject", "delivery"} {
					if strings.Contains(text, forbidden) {
						t.Fatal("session output leaked audit identity or included inbox delivery")
					}
				}
				var ask map[string]any
				if i == 0 {
					if err := json.Unmarshal([]byte(text), &ask); err != nil {
						t.Fatal(err)
					}
				} else {
					var page struct {
						Asks       []map[string]any `json:"asks"`
						NextCursor string           `json:"nextCursor"`
						Complete   bool             `json:"complete"`
					}
					if err := json.Unmarshal([]byte(text), &page); err != nil || len(page.Asks) != 1 || page.NextCursor != "next/+page" || page.Complete {
						t.Fatal("list pagination changed")
					}
					ask = page.Asks[0]
				}
				if ask["state"] != tc.wantState {
					t.Errorf("state=%v, want %s", ask["state"], tc.wantState)
				}
				if tc.approval {
					computed, ok := ask["computed"].(map[string]any)
					if !ok || computed["approvalValid"] != tc.wantValid || ask["approvalValid"] != nil {
						t.Errorf("approval validity must be labelled computed: %v", ask)
					}
				} else if ask["computed"] != nil {
					t.Fatal("info ask must not claim approval validity")
				}
			}
		})
	}
}

func TestAsksMCPMutationResultsUseCurrentState(t *testing.T) {
	for _, tc := range []struct {
		name, tool    string
		approval      bool
		state         apiv1.AskState
		afterCreation time.Duration
		wantState     apiv1.AskState
		wantValid     bool
	}{
		{"expired info replay", "ask", false, apiv1.AskStateOpen, 24 * time.Hour, apiv1.AskStateExpired, false},
		{"pending approval", "ask", true, apiv1.AskStateOpen, time.Minute, apiv1.AskStateOpen, false},
		{"granted approval replay", "ask", true, apiv1.AskStateAnswered, 2 * time.Minute, apiv1.AskStateAnswered, true},
		{"withdrawn info", "withdraw", false, apiv1.AskStateWithdrawn, 24 * time.Hour, apiv1.AskStateWithdrawn, false},
		{"withdrawn approval", "withdraw", true, apiv1.AskStateWithdrawn, 24 * time.Hour, apiv1.AskStateWithdrawn, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.now = testNow.Add(tc.afterCreation)
			record := askFixture(tc.state)
			if tc.approval {
				record.Type = apiv1.AskTypeApproval
				record.Scope = &apiv1.AskScope{Action: "Apply", Target: "Fixture", ExpiresAt: record.ExpiresAt}
				if record.Answer != nil {
					approved := true
					record.Answer.Approved = &approved
				}
			}
			var args any = map[string]string{"id": testAskID}
			path := apiv1.AskWithdrawPath(testAskID)
			if tc.tool == "ask" {
				args = apiv1.AskCreateRequest{
					RequestID: "mutation-257", Type: record.Type, Question: record.Question,
					Options: record.Options, AllowFreeText: record.AllowFreeText, Scope: record.Scope,
				}
				path = apiv1.AsksPath
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Method != http.MethodPost || r.URL.Path != path {
					t.Errorf("unexpected mutation: %s %s", r.Method, r.URL)
				}
				// A create replay can return a stale stored state.
				writeTestJSON(w, http.StatusOK, record)
			}
			h.env.Stdin = strings.NewReader(askJSON(t, map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": tc.tool, "arguments": args},
			}) + "\n")
			h.mustRun(ExitOK, "mcp")
			frames := decodeMCPFrames(t, h.stdout.String())
			if len(frames) != 1 || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatal("mutation must return after one immediate API call")
			}
			var reply struct {
				Result struct {
					IsError bool                    `json:"isError"`
					Content []struct{ Text string } `json:"content"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(frames[0]), &reply); err != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
				t.Fatalf("invalid MCP mutation result: %s", frames[0])
			}
			var got struct {
				State    apiv1.AskState `json:"state"`
				Computed *struct {
					ApprovalValid bool `json:"approvalValid"`
				} `json:"computed"`
			}
			if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &got); err != nil {
				t.Fatal(err)
			}
			if got.State != tc.wantState {
				t.Errorf("state=%s, want %s", got.State, tc.wantState)
			}
			if tc.approval {
				if got.Computed == nil || got.Computed.ApprovalValid != tc.wantValid {
					t.Errorf("incorrect computed approval validity: %s", reply.Result.Content[0].Text)
				}
			} else if got.Computed != nil {
				t.Fatal("info ask must not claim approval validity")
			}
		})
	}
}

func TestAsksMCPOptionalListArguments(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Path != apiv1.AsksPath || r.URL.Query().Get(apiv1.AskQueryLimit) != "20" {
			t.Errorf("unexpected list request: %s", r.URL)
		}
		writeTestJSON(w, http.StatusOK, apiv1.AskList{Complete: true})
	}
	h.env.Stdin = strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_my_asks"}}` + "\n" +
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_my_asks","arguments":null}}` + "\n")
	h.mustRun(ExitOK, "mcp")
	frames := decodeMCPFrames(t, h.stdout.String())
	if len(frames) != 2 || len(h.api.requests()) != 2 {
		t.Fatal("omitted and null list arguments must each read a page")
	}
	for _, frame := range frames {
		var reply struct {
			Result struct {
				IsError bool                    `json:"isError"`
				Content []struct{ Text string } `json:"content"`
			}
		}
		if err := json.Unmarshal([]byte(frame), &reply); err != nil || reply.Result.IsError || len(reply.Result.Content) != 1 {
			t.Fatalf("expected successful empty list: %s", frame)
		}
		var page struct {
			Asks     json.RawMessage `json:"asks"`
			Complete bool            `json:"complete"`
		}
		if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &page); err != nil || string(page.Asks) != "[]" || !page.Complete {
			t.Fatal("expected complete empty array page")
		}
	}
}
