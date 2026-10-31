package agentrun

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	decideGrantName = "grant-abc"
	decideUID       = "uid-1"
	decideDigest    = "digest-1"
	decideRef       = "msg/1:ok#ref_1.2-3"
)

func decideGrantJSON(phase string) string {
	g := apiv1.Grant{
		Name: decideGrantName, UID: decideUID, Phase: phase, Type: "kube",
		Role: namespaceOperatorRole, Secrets: []string{"db-password", "api.token"},
		TTL: "1h", Reason: "rotate", ScopeDigest: decideDigest, Session: "task-1",
	}
	b, _ := json.Marshal(g)
	return string(b)
}

func serveGrantDecision(t *testing.T, h *harness, postStatus int, postBody string, before *[]string) {
	t.Helper()
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath(decideGrantName): func(r *http.Request) (int, string) {
			if r.Method != http.MethodGet {
				t.Errorf("grant method %s", r.Method)
			}
			return http.StatusOK, decideGrantJSON(apiv1.GrantPending)
		},
		apiv1.GrantDecisionPath(decideGrantName): func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("decision method %s", r.Method)
			}
			if before != nil {
				*before = append(*before, h.stdout.String(), h.stderr.String())
			}
			return postStatus, postBody
		},
	})
}

func decisionPosts(t *testing.T, h *harness) []recorded {
	t.Helper()
	var posts []recorded
	for _, req := range h.api.requests() {
		if req.method == http.MethodPost && req.path == apiv1.GrantDecisionPath(decideGrantName) {
			posts = append(posts, req)
		}
	}
	return posts
}

func TestGrantDecidePostsTheDecision(t *testing.T) {
	h := newHarness(t)
	var before []string
	serveGrantDecision(t, h, http.StatusAccepted, decideGrantJSON(apiv1.GrantPending), &before)
	h.mustRun(ExitOK, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", decideRef, "--approve", "--ttl", "10m", "--reason", "reviewed")
	posts := decisionPosts(t, h)
	if len(posts) != 1 {
		t.Fatalf("posts %d", len(posts))
	}
	want, err := json.Marshal(apiv1.GrantDecisionRequest{
		GrantUID: decideUID, Digest: decideDigest, Approve: true, TTL: "10m", Ref: decideRef, Reason: "reviewed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(posts[0].body) != string(want) {
		t.Fatalf("body %s\nwant %s", posts[0].body, want)
	}
	if len(before) < 2 || strings.Contains(before[0], "decision accepted") {
		t.Fatalf("binding was not printed before the post: %q", before)
	}
	contains(t, "before send", before[0], "binding scope digest "+decideDigest, "secrets: db-password, api.token", "approval", "not a human decision")
	contains(t, "stdout", h.stdout.String(), "decision accepted", "phase: Pending", "secrets: db-password, api.token")
	if strings.Contains(h.stdout.String(), "phase: Active") {
		t.Fatalf("202 was treated as Active:\n%s", h.stdout.String())
	}
}

func TestGrantDecideDefaultDeny(t *testing.T) {
	h := newHarness(t)
	var before []string
	serveGrantDecision(t, h, http.StatusAccepted, decideGrantJSON(apiv1.GrantPending), &before)
	h.mustRun(ExitOK, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "msg-1")
	posts := decisionPosts(t, h)
	if len(posts) != 1 {
		t.Fatalf("posts %d", len(posts))
	}
	want, err := json.Marshal(apiv1.GrantDecisionRequest{GrantUID: decideUID, Digest: decideDigest, Approve: false, Ref: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if string(posts[0].body) != string(want) {
		t.Fatalf("body %s\nwant %s", posts[0].body, want)
	}
	if strings.Contains(string(posts[0].body), "ttl") || strings.Contains(string(posts[0].body), "reason") || strings.Contains(string(posts[0].body), "secrets") {
		t.Fatalf("deny body has extra fields: %s", posts[0].body)
	}
	contains(t, "before send", before[0], "denial", "binding scope digest "+decideDigest, "secrets: db-password, api.token")
	contains(t, "stdout", h.stdout.String(), "denial", "decision accepted", "phase: Pending")
	if strings.Contains(h.stdout.String(), "phase: Active") {
		t.Fatalf("denial was treated as Active:\n%s", h.stdout.String())
	}
}

func TestGrantDecideLocalValidationSendsNothing(t *testing.T) {
	h := newHarness(t)
	serveGrantDecision(t, h, http.StatusAccepted, decideGrantJSON(apiv1.GrantPending), nil)
	longRef := strings.Repeat("a", 129)
	longReason := strings.Repeat("a", 257)
	cases := [][]string{
		{"grant", "decide"},
		{"grant", "decide", "not-a-grant", "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m"},
		{"grant", "decide", decideGrantName, "--digest", decideDigest, "--ref", "m"},
		{"grant", "decide", decideGrantName, "--grant-uid", " ", "--digest", decideDigest, "--ref", "m"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--ref", "m"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", " ", "--ref", "m"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "bad ref"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "bad@ref"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", longRef},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--ttl", "9m"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--ttl", "9m59s"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--ttl", "nope"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--reason", longReason},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--reason", "bad\nreason"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "extra"},
		{"grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m", "--nope"},
	}
	for _, args := range cases {
		h.mustRun(ExitUsage, args...)
	}
	if n := len(h.api.requests()); n != 0 {
		t.Fatalf("a refused decision reached the API %d times", n)
	}
	if !validGrantDecisionRef(strings.Repeat("a", 128)) || validGrantDecisionRef(longRef) || validGrantDecisionRef("") {
		t.Fatal("ref bounds")
	}
}

func TestGrantDecideMapsErrorStatuses(t *testing.T) {
	errBody := func(code, msg string) string {
		b, _ := json.Marshal(apiv1.ErrorResponse{Error: apiv1.Error{Code: code, Message: msg}})
		return string(b)
	}
	cases := []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
		absent string
	}{
		{"forbidden", http.StatusForbidden, errBody(apiv1.CodeForbidden, "the caller is not an allowlisted delegate"), ExitAuth, "forbidden", "not enabled"},
		{"conflict", http.StatusConflict, errBody(apiv1.CodeConflict, "stale, replayed, mismatched or already decided"), ExitFailed, "conflict", "not enabled"},
		{"invalid", http.StatusUnprocessableEntity, errBody(apiv1.CodeInvalid, "ttl is too short"), ExitFailed, "invalid", "not enabled"},
		{"typed not_found is a missing grant", http.StatusNotFound, errBody(apiv1.CodeNotFound, "grant grant-abc: not found"), ExitNotFound, "grant grant-abc: not found", "not enabled"},
		{"typed missing with route words", http.StatusNotFound, errBody(apiv1.CodeNotFound, "grant not found: no route information"), ExitNotFound, "grant not found: no route information", "not enabled"},
		{"unregistered route", http.StatusNotFound, errBody(apiv1.CodeNotFound, "no route POST /v1/grants/grant-abc/decision"), ExitNotFound, "delegate decisions are not enabled on this operator", "grant grant-abc: not found"},
		{"plain 404 is an unregistered route", http.StatusNotFound, "404 page not found", ExitNotFound, "delegate decisions are not enabled on this operator", "not_found"},
		{"unavailable", http.StatusServiceUnavailable, errBody(apiv1.CodeUnavailable, "the Kubernetes API is busy"), ExitRetry, "the Kubernetes API is busy", "not enabled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveGrantDecision(t, h, tc.status, tc.body, nil)
			h.mustRun(tc.exit, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "m")
			contains(t, "stderr", h.stderr.String(), tc.want)
			if tc.absent != "" && strings.Contains(h.stderr.String(), tc.absent) {
				t.Fatalf("stderr contains %q:\n%s", tc.absent, h.stderr.String())
			}
		})
	}
}

func TestGrantDecideJSONOutput(t *testing.T) {
	h := newHarness(t)
	var before []string
	serveGrantDecision(t, h, http.StatusAccepted, decideGrantJSON(apiv1.GrantPending), &before)
	h.mustRun(ExitOK, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "msg-1", "-o", "json")
	var got apiv1.Grant
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not a grant: %v\n%s", err, h.stdout.String())
	}
	if got.Phase != apiv1.GrantPending || got.ScopeDigest != decideDigest || !reflect.DeepEqual(got.Secrets, []string{"db-password", "api.token"}) {
		t.Fatalf("grant %#v", got)
	}
	if strings.Contains(h.stdout.String(), "decision accepted") || strings.Contains(h.stdout.String(), "deciding ") {
		t.Fatalf("json stdout mixed with text:\n%s", h.stdout.String())
	}
	if len(before) < 2 {
		t.Fatal("no binding captured")
	}
	contains(t, "stderr before send", before[1], "binding scope digest "+decideDigest, "secrets: db-password, api.token", "denial")
	posts := decisionPosts(t, h)
	want, _ := json.Marshal(apiv1.GrantDecisionRequest{GrantUID: decideUID, Digest: decideDigest, Approve: false, Ref: "msg-1"})
	if len(posts) != 1 || string(posts[0].body) != string(want) {
		t.Fatalf("posts %+v", posts)
	}
}

func TestGrantDecideHelp(t *testing.T) {
	text := strings.Join(strings.Fields(commandHelp["grant"]), " ")
	for _, phrase := range []string{
		"allowlisted delegate",
		"cannot decide its own or an ancestor's request",
		"Grant.ScopeDigest",
		"grant show -o json",
		"not a human decision",
		"grant decide",
	} {
		if !strings.Contains(text, phrase) {
			t.Errorf("help missing %q", phrase)
		}
	}
	h := newHarness(t)
	h.mustRun(ExitUsage, "grant", "nope")
	contains(t, "stderr", h.stderr.String(), "attest", "decide")
}

func TestGrantRequestSecrets(t *testing.T) {
	h := newHarness(t)
	longName := "a" + strings.Repeat("b", 62)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			var out apiv1.Grant
			if err := json.Unmarshal([]byte(decideGrantJSON(apiv1.GrantPending)), &out); err != nil {
				t.Fatal(err)
			}
			out.Secrets = append(out.Secrets, longName)
			body, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			return http.StatusCreated, string(body)
		},
	})
	h.mustRun(ExitOK, "grant", "request", "--no-wait", "--type", "kube", "--role", namespaceOperatorRole,
		"--namespace", "dev", "--secret", "db-password", "--secret", "api.token", "--secret", longName, "--reason", "rotate")
	want, err := json.Marshal(apiv1.CreateGrantRequest{
		Type: "kube", Role: namespaceOperatorRole, Namespaces: []string{"dev"},
		Secrets: []string{"db-password", "api.token", longName}, Reason: "rotate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(h.api.requests()[0].body) != string(want) {
		t.Fatalf("body %s\nwant %s", h.api.requests()[0].body, want)
	}
	contains(t, "stdout", h.stdout.String(), "secrets: db-password, api.token, "+longName)
}

func TestGrantRequestSecretRefusalsSendNothing(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(*http.Request) (int, string) {
			t.Error("a refused secret request reached the API")
			return http.StatusCreated, `{}`
		},
	})
	args := []string{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--reason", "rotate"}
	for i := 0; i < maxGrantSecrets+1; i++ {
		args = append(args, "--secret", "s"+string(rune('a'+i)))
	}
	long := "a" + strings.Repeat("b", 63)
	cases := [][]string{
		{"grant", "request", "--type", "egress", "--secret", "db-password", "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", "dev-env-grant-workloads", "--secret", "db-password", "--reason", "x"},
		{"grant", "request", "--type", "breakglass", "--secret", "db-password", "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--secret", "Bad", "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--secret", "a-", "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--secret", long, "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--secret", "db", "--secret", "db", "--reason", "x"},
		{"grant", "request", "--type", "kube", "--role", namespaceOperatorRole, "--secret", "", "--reason", "x"},
		args,
	}
	for _, c := range cases {
		h.mustRun(ExitUsage, c...)
	}
	if len(h.api.requests()) != 0 {
		t.Fatalf("requests %+v", h.api.requests())
	}
	if !validDNS1123Name("a"+strings.Repeat("b", 62)) || validDNS1123Name(long) || validDNS1123Name("A") || validDNS1123Name("a..b") {
		t.Fatal("dns bounds")
	}
}

func TestGrantShowAndListPrintSecretNames(t *testing.T) {
	h := newHarness(t)
	withSecrets := decideGrantJSON(apiv1.GrantPending)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantPath(decideGrantName): func(*http.Request) (int, string) {
			return http.StatusOK, withSecrets
		},
		apiv1.GrantPath("grant-plain"): func(*http.Request) (int, string) {
			return http.StatusOK, `{"name":"grant-plain","phase":"Pending","type":"kube","session":"task-1","ttl":"15m","reason":"lookup"}`
		},
		apiv1.GrantsPath: func(*http.Request) (int, string) {
			return http.StatusOK, `{"items":[` + withSecrets + `]}`
		},
	})
	h.mustRun(ExitOK, "grant", "show", decideGrantName)
	contains(t, "show", h.stdout.String(), "secrets: db-password, api.token")
	h.mustRun(ExitOK, "grant", "show", "grant-plain")
	if strings.Contains(h.stdout.String(), "secrets:") {
		t.Fatalf("empty secrets printed a line:\n%s", h.stdout.String())
	}
	h.mustRun(ExitOK, "grant", "show", decideGrantName, "-o", "json")
	if !strings.Contains(h.stdout.String(), `"db-password"`) || !strings.Contains(h.stdout.String(), `"api.token"`) {
		t.Fatalf("show json %s", h.stdout.String())
	}
	h.mustRun(ExitOK, "grant", "list", "-o", "json")
	if !strings.Contains(h.stdout.String(), `"db-password"`) || !strings.Contains(h.stdout.String(), `"api.token"`) {
		t.Fatalf("list json %s", h.stdout.String())
	}
}

func TestDecideGrantMCPSchemaAndCall(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantDecisionPath(decideGrantName): func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusAccepted, decideGrantJSON(apiv1.GrantPending)
		},
	})
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"decide_grant","arguments":{"name":"grant-abc","grantUID":"uid-1","digest":"digest-1","ref":"msg-1"}}}`,
		"",
	}, "\n"))
	h.mustRun(ExitOK, "mcp")
	msgs := decodeMCPFrames(t, h.stdout.String())
	if len(msgs) != 2 {
		t.Fatalf("frames %d: %s", len(msgs), h.stdout.String())
	}
	var listed struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				Description string         `json:"description"`
				Schema      map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(msgs[0]), &listed); err != nil {
		t.Fatal(err)
	}
	var tool struct {
		Description string
		Schema      map[string]any
	}
	for _, item := range listed.Result.Tools {
		if item.Name == "decide_grant" {
			tool.Description = item.Description
			tool.Schema = item.Schema
		}
	}
	if tool.Schema == nil {
		t.Fatal("tools/list omitted decide_grant")
	}
	if !strings.Contains(tool.Description, "delegates only") || !strings.Contains(tool.Description, "not a human decision") {
		t.Fatalf("description %s", tool.Description)
	}
	props, _ := tool.Schema["properties"].(map[string]any)
	for _, key := range []string{"name", "grantUID", "digest", "approve", "ttl", "ref", "reason"} {
		property, ok := props[key].(map[string]any)
		wantType := "string"
		if key == "approve" {
			wantType = "boolean"
		}
		if !ok || property["type"] != wantType {
			t.Errorf("property %s: %v; want %s", key, props[key], wantType)
		}
	}
	if !reflect.DeepEqual(tool.Schema["required"], []any{"name", "grantUID", "digest", "ref"}) {
		t.Fatalf("required %v", tool.Schema["required"])
	}
	posts := decisionPosts(t, h)
	want, _ := json.Marshal(apiv1.GrantDecisionRequest{GrantUID: decideUID, Digest: decideDigest, Approve: false, Ref: "msg-1"})
	if len(posts) != 1 || posts[0].path != apiv1.GrantDecisionPath(decideGrantName) || string(posts[0].body) != string(want) {
		t.Fatalf("posts %+v", posts)
	}
	if !strings.Contains(msgs[1], "db-password") || !strings.Contains(msgs[1], `"isError":false`) {
		t.Fatalf("call %s", msgs[1])
	}
	before := len(h.api.requests())
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"decide_grant","arguments":{"name":"grant-abc","digest":"digest-1","ref":"msg-1"}}}` + "\n")
	h.mustRun(ExitOK, "mcp")
	if len(h.api.requests()) != before || !strings.Contains(h.stdout.String(), `"isError":true`) {
		t.Fatalf("refused call reached the API or was not an error: %s", h.stdout.String())
	}
}

func TestMCPRequestAndStatusRoundTripSecrets(t *testing.T) {
	h := newHarness(t)
	serveRaw(h, map[string]func(*http.Request) (int, string){
		apiv1.GrantsPath: func(r *http.Request) (int, string) {
			if r.Method != http.MethodPost {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusCreated, decideGrantJSON(apiv1.GrantPending)
		},
		apiv1.GrantPath(decideGrantName): func(r *http.Request) (int, string) {
			if r.Method != http.MethodGet {
				t.Errorf("method %s", r.Method)
			}
			return http.StatusOK, decideGrantJSON(apiv1.GrantActive)
		},
	})
	h.env.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"kube","role":"dev-env-grant-namespace-operator","reason":"rotate","namespaces":["dev"],"secrets":["db-password","api.token"]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"grant_status","arguments":{"name":"grant-abc"}}}`,
		"",
	}, "\n"))
	h.mustRun(ExitOK, "mcp")
	want, err := json.Marshal(apiv1.CreateGrantRequest{
		Type: "kube", Role: namespaceOperatorRole, Namespaces: []string{"dev"},
		Secrets: []string{"db-password", "api.token"}, Reason: "rotate",
	})
	if err != nil {
		t.Fatal(err)
	}
	var post recorded
	for _, req := range h.api.requests() {
		if req.method == http.MethodPost {
			post = req
		}
	}
	if string(post.body) != string(want) {
		t.Fatalf("body %s\nwant %s", post.body, want)
	}
	frames := decodeMCPFrames(t, h.stdout.String())
	if len(frames) != 2 {
		t.Fatalf("request/status frames %d", len(frames))
	}
	for i, frame := range frames {
		var response struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(frame), &response); err != nil {
			t.Fatal(err)
		}
		if response.Result.IsError || len(response.Result.Content) != 1 {
			t.Fatalf("frame %d: %s", i, frame)
		}
		var grant apiv1.Grant
		if err := json.Unmarshal([]byte(response.Result.Content[0].Text), &grant); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(grant.Secrets, []string{"db-password", "api.token"}) {
			t.Fatalf("frame %d lost names: %+v", i, grant)
		}
		wantPhase := apiv1.GrantPending
		if i == 1 {
			wantPhase = apiv1.GrantActive
		}
		if grant.Phase != wantPhase {
			t.Fatalf("frame %d phase %s", i, grant.Phase)
		}
	}
	before := len(h.api.requests())
	h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"request_access","arguments":{"type":"egress","reason":"x","secrets":["db-password"]}}}` + "\n")
	h.mustRun(ExitOK, "mcp")
	if len(h.api.requests()) != before || !strings.Contains(h.stdout.String(), `"isError":true`) {
		t.Fatalf("misplaced secrets reached the API: %s", h.stdout.String())
	}
}
