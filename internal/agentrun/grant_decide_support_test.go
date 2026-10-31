package agentrun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The preflight read supplies names for the binding display. It must describe
// the same UID and digest the caller reviewed, not a replacement or newer scope.
func TestGrantDecideRefusesStaleDisplay(t *testing.T) {
	for _, field := range []string{"uid", "scopeDigest"} {
		t.Run(field, func(t *testing.T) {
			h := newHarness(t)
			var current map[string]any
			if err := json.Unmarshal([]byte(decideGrantJSON(apiv1.GrantPending)), &current); err != nil {
				t.Fatal(err)
			}
			current[field] = "changed"
			body, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath(decideGrantName): func(*http.Request) (int, string) { return http.StatusOK, string(body) },
				apiv1.GrantDecisionPath(decideGrantName): func(*http.Request) (int, string) {
					t.Error("posted a stale decision")
					return http.StatusAccepted, decideGrantJSON(apiv1.GrantPending)
				},
			})
			h.mustRun(ExitFailed, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "msg-1")
			if len(h.api.requests()) != 1 || len(decisionPosts(t, h)) != 0 {
				t.Fatalf("requests %+v", h.api.requests())
			}
			if strings.Contains(h.stdout.String(), "binding scope digest") {
				t.Fatalf("claimed stale names were bound: %s", h.stdout.String())
			}
			contains(t, "stderr", h.stderr.String(), "UID or scope digest changed")
		})
	}
}

// Match the server's Unicode rune limit and both Cc and Cf refusals. These
// cases run through the command/tool, so a validation bypass reaches the API.
func TestGrantDecisionReasonParity(t *testing.T) {
	reasons := []string{strings.Repeat("界", 257), "bad\x00reason", "bad\u007freason", "bad\u0085reason", "bad\u202ereason", "bad\u200breason", string([]byte{0xff})}
	for i, reason := range reasons {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newHarness(t)
			h.mustRun(ExitUsage, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", "msg-1", "--reason", reason)
			if len(h.api.requests()) != 0 {
				t.Fatal("invalid reason reached API")
			}
		})
	}
	// 256 non-ASCII characters are allowed; count runes, not UTF-8 bytes.
	h := newHarness(t)
	serveGrantDecision(t, h, http.StatusAccepted, decideGrantJSON(apiv1.GrantPending), nil)
	reason := strings.Repeat("界", 256)
	h.mustRun(ExitOK, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", strings.Repeat("a", 128), "--reason", reason)
	posts := decisionPosts(t, h)
	var req apiv1.GrantDecisionRequest
	if len(posts) != 1 {
		t.Fatalf("posts %+v", posts)
	}
	if err := json.Unmarshal(posts[0].body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Reason != reason {
		t.Fatal("reason changed")
	}
}

func TestDecideGrantMCPLocalRefusals(t *testing.T) {
	cases := []struct {
		field string
		value any
	}{
		{"name", "not-a-grant"}, {"grantUID", " "}, {"digest", " "}, {"ref", ""}, {"ref", "bad@ref"}, {"ref", strings.Repeat("a", 129)},
		{"ttl", "bad"}, {"ttl", "9m59s"}, {"reason", strings.Repeat("界", 257)}, {"reason", "bad\nreason"}, {"reason", "bad\u202ereason"}, {"approve", "true"},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newHarness(t)
			args := map[string]any{"name": decideGrantName, "grantUID": decideUID, "digest": decideDigest, "ref": decideRef}
			args[tc.field] = tc.value
			frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "decide_grant", "arguments": args}})
			if err != nil {
				t.Fatal(err)
			}
			h.env.Stdin = strings.NewReader(string(frame) + "\n")
			h.mustRun(ExitOK, "mcp")
			if len(h.api.requests()) != 0 || !strings.Contains(h.stdout.String(), `"isError":true`) {
				t.Fatalf("invalid MCP input sent: %s", h.stdout.String())
			}
		})
	}
}

func TestDecideGrantMCPApproveAndErrors(t *testing.T) {
	for _, status := range []int{http.StatusAccepted, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			h := newHarness(t)
			body := decideGrantJSON(apiv1.GrantPending)
			code := apiv1.CodeForbidden
			message := "delegate refused"
			switch status {
			case http.StatusConflict:
				code = apiv1.CodeConflict
			case http.StatusUnprocessableEntity:
				code = apiv1.CodeInvalid
			case http.StatusNotFound:
				code = apiv1.CodeNotFound
				message = "grant is missing"
			case http.StatusServiceUnavailable:
				code = apiv1.CodeUnavailable
			}
			if status != http.StatusAccepted {
				raw, err := json.Marshal(apiv1.ErrorResponse{Error: apiv1.Error{Code: code, Message: message}})
				if err != nil {
					t.Fatal(err)
				}
				body = string(raw)
			}
			serveGrantDecision(t, h, status, body, nil)
			h.env.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"decide_grant","arguments":{"name":"grant-abc","grantUID":"uid-1","digest":"digest-1","ref":"msg-1","approve":true,"ttl":"10m","reason":"reviewed"}}}` + "\n")
			h.mustRun(ExitOK, "mcp")
			posts := decisionPosts(t, h)
			want, _ := json.Marshal(apiv1.GrantDecisionRequest{GrantUID: decideUID, Digest: decideDigest, Ref: "msg-1", Approve: true, TTL: "10m", Reason: "reviewed"})
			if len(posts) == 0 || string(posts[0].body) != string(want) {
				t.Fatalf("posts %+v", posts)
			}
			if status == http.StatusAccepted {
				contains(t, "MCP", h.stdout.String(), `"isError":false`, "db-password", "Pending")
			} else {
				contains(t, "MCP", h.stdout.String(), `"isError":true`)
				if status == http.StatusServiceUnavailable {
					contains(t, "MCP", h.stdout.String(), "unavailable", message)
					if strings.Contains(h.stdout.String(), "not enabled") {
						t.Fatal("outage reported as disabled")
					}
				} else {
					contains(t, "MCP", h.stdout.String(), message)
				}
				if status == http.StatusNotFound && strings.Contains(h.stdout.String(), "not enabled") {
					t.Fatal("missing grant reported as disabled")
				}
			}
		})
	}
}

func TestRequestAccessMCPSecretBounds(t *testing.T) {
	cases := [][]string{{"A"}, {"a-"}, {"-a"}, {"a..b"}, {"a.-b"}, {"a-.b"}, {""}, {strings.Repeat("a", 64)}, {"db", "db"}}
	var names []string
	for i := 0; i < 17; i++ {
		names = append(names, fmt.Sprintf("secret-%d", i))
	}
	cases = append(cases, names)
	for i, secrets := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			h := newHarness(t)
			frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "request_access", "arguments": map[string]any{"type": "kube", "role": namespaceOperatorRole, "reason": "rotate", "namespaces": []string{"dev"}, "secrets": secrets}}})
			if err != nil {
				t.Fatal(err)
			}
			h.env.Stdin = strings.NewReader(string(frame) + "\n")
			h.mustRun(ExitOK, "mcp")
			if len(h.api.requests()) != 0 || !strings.Contains(h.stdout.String(), `"isError":true`) {
				t.Fatalf("invalid names sent: %s", h.stdout.String())
			}
		})
	}
	// The upper bound itself is valid, with names carried unchanged.
	req, err := accessRequest(accessToolArgs{Type: "kube", Role: namespaceOperatorRole, Reason: "rotate", Secrets: names[:16]})
	if err != nil || !reflect.DeepEqual(req.Secrets, names[:16]) {
		t.Fatalf("16 names rejected or changed: %+v %v", req, err)
	}
}

func TestGrantDecidePreflightRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		exit   int
		want   string
	}{
		{"disabled UID", http.StatusOK, `{"name":"grant-abc","phase":"Pending","scopeDigest":"digest-1","secrets":["db-password"]}`, ExitNotFound, "delegate decisions are not enabled on this operator: it shows no grant UID"},
		{"missing grant", http.StatusNotFound, `{"error":{"code":"not_found","message":"grant grant-abc: not found"}}`, ExitNotFound, "grant grant-abc: not found"},
		{"forbidden read", http.StatusForbidden, `{"error":{"code":"forbidden","message":"coordinator access is limited to its direct task children"}}`, ExitAuth, "coordinator access is limited to its direct task children"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath(decideGrantName): func(*http.Request) (int, string) { return tc.status, tc.body },
				apiv1.GrantDecisionPath(decideGrantName): func(*http.Request) (int, string) {
					t.Error("POST after refused preflight")
					return http.StatusAccepted, decideGrantJSON(apiv1.GrantPending)
				},
			})
			h.mustRun(tc.exit, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", decideRef)
			contains(t, "stderr", h.stderr.String(), tc.want)
			if len(h.api.requests()) != 1 || len(decisionPosts(t, h)) != 0 {
				t.Fatalf("requests %+v", h.api.requests())
			}
			if strings.Contains(h.stdout.String(), "binding scope digest") {
				t.Fatal("printed binding after preflight refusal")
			}
			if tc.name == "missing grant" && strings.Contains(h.stderr.String(), "not enabled") {
				t.Fatal("missing grant reported as disabled")
			}
		})
	}
}

func TestGrantDecisionRetryConflictIsUnconfirmed(t *testing.T) {
	for _, first := range []string{"503", "lost reply", "plain conflict"} {
		t.Run(first, func(t *testing.T) {
			h := newHarness(t)
			calls := 0
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath(decideGrantName): func(*http.Request) (int, string) { return http.StatusOK, decideGrantJSON(apiv1.GrantPending) },
				apiv1.GrantDecisionPath(decideGrantName): func(*http.Request) (int, string) {
					calls++
					if calls == 1 {
						switch first {
						case "503":
							return http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"Kubernetes timeout"}}`
						case "lost reply":
							panic(http.ErrAbortHandler)
						}
					}
					return http.StatusConflict, `{"error":{"code":"conflict","message":"already decided"}}`
				},
			})
			exit := ExitUnconfirmed
			if first == "plain conflict" {
				exit = ExitFailed
			}
			h.mustRun(exit, "grant", "decide", decideGrantName, "--grant-uid", decideUID, "--digest", decideDigest, "--ref", decideRef)
			if first == "plain conflict" {
				if calls != 1 || strings.Contains(h.stderr.String(), "may already have been recorded") {
					t.Fatalf("ordinary conflict: calls=%d %s", calls, h.stderr.String())
				}
				contains(t, "stderr", h.stderr.String(), "conflict")
			} else {
				if calls != 2 {
					t.Fatalf("retry count %d", calls)
				}
				contains(t, "stderr", h.stderr.String(), "may already have been recorded", "agent-run grant show "+decideGrantName)
				if strings.Contains(h.stderr.String(), "not enabled") || strings.Contains(h.stderr.String(), "(409 conflict)") {
					t.Fatalf("misleading diagnostic: %s", h.stderr.String())
				}
			}
		})
	}
}
