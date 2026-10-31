package agentrun

import (
	"encoding/json"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"net/http"
	"strings"
	"testing"
)

const reservedTestUID = "11111111-1111-1111-1111-111111111111"

func TestReservedRequestClientFlagsAndValidation(t *testing.T) {
	for _, scope := range []string{"snapshot", "storage"} {
		t.Run(scope, func(t *testing.T) {
			h := newHarness(t)
			serveRaw(h, map[string]func(*http.Request) (int, string){apiv1.GrantsPath: func(r *http.Request) (int, string) { return http.StatusCreated, `{"name":"grant-a","phase":"Pending"}` }})
			args := []string{"grant", "request", "--no-wait", "--type", "kube", "--reason", "synthetic", "--ttl", "30m"}
			if scope == "snapshot" {
				args = append(args, "--role", "dev-env-grant-secrets-read", "--all-namespaces")
			} else {
				args = append(args, "--role", "dev-env-grant-storage", "--namespace", "database", "--storage-target", "PersistentVolumeClaim/data@"+reservedTestUID)
			}
			h.mustRun(ExitOK, args...)
			var req apiv1.CreateGrantRequest
			if err := json.Unmarshal(h.api.requests()[0].body, &req); err != nil {
				t.Fatal(err)
			}
			if !req.Reserved || (scope == "snapshot" && !req.AllNamespaces) || (scope == "storage" && (len(req.StorageTargets) != 1 || req.StorageTargets[0].UID != reservedTestUID)) {
				t.Fatal("client dropped reserved scope", req)
			}
		})
	}
	for _, args := range [][]string{
		{"grant", "request", "--type", "kube", "--reason", "test", "--role", "dev-env-grant-storage", "--reserved", "--namespace", "database"},
		{"grant", "request", "--type", "kube", "--reason", "test", "--role", "dev-env-grant-workloads", "--all-namespaces"},
		{"grant", "request", "--type", "kube", "--reason", "test", "--role", "dev-env-grant-secrets-read", "--all-namespaces", "--namespace", "database"},
		{"grant", "request", "--type", "kube", "--reason", "test", "--role", "dev-env-grant-secrets-read", "--all-namespaces", "--ttl", "2h"},
		{"grant", "request", "--type", "kube", "--reason", "test", "--role", "dev-env-grant-storage", "--namespace", "database", "--storage-target", "PersistentVolumeClaim/data@not-a-uid"},
		{"grant", "decide", "grant-a", "--grant-uid", reservedTestUID, "--digest", strings.Repeat("A", 43), "--ref", "request-1", "--reserved"},
	} {
		h := newHarness(t)
		h.mustRun(ExitUsage, args...)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid reserved flags reached API")
		}
	}
}

func reservedClientGrant() apiv1.Grant {
	return apiv1.Grant{Name: "grant-a", UID: reservedTestUID, ScopeDigest: strings.Repeat("A", 43), Type: "breakglass", Role: apiv1.BreakglassRole, Reserved: true, TTL: "30m", Phase: "Pending"}
}

func TestReservedDecisionClientRetainsAuthenticationAndExactReply(t *testing.T) {
	for _, mode := range []string{"valid", "deny", "wrong-ref", "wrong-tom-ref", "wrong-uid", "wrong-digest", "wrong-ttl", "wrong-case", "no-audit", "missing-actor", "unavailable", "accepted-wrong-status"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			current := reservedClientGrant()
			serveRaw(h, map[string]func(*http.Request) (int, string){
				apiv1.GrantPath("grant-a"): func(r *http.Request) (int, string) {
					raw, _ := json.Marshal(current)
					return http.StatusOK, string(raw)
				},
				apiv1.GrantDecisionPath("grant-a"): func(r *http.Request) (int, string) {
					if r.Header.Get("Authorization") != "Bearer "+testToken {
						t.Fatal("decision changed authenticated identity")
					}
					var req apiv1.GrantDecisionRequest
					if err := json.Unmarshal(h.api.requests()[len(h.api.requests())-1].body, &req); err != nil {
						t.Fatal(err)
					}
					if !req.Reserved || req.TomRef != "tom-message-1" || req.Ref != "request-1" || req.Approve != (mode != "deny") {
						t.Fatal("request correlation lost", req)
					}
					out := current
					out.ReservedDecision = &apiv1.ReservedDecisionBinding{Case: "rd2", BoundsRevision: "1", GrantUID: req.GrantUID, RequesterUID: "requester", RequesterPodUID: "requester-pod", ActorUID: "actor-pod", ScopeDigest: req.Digest, EffectiveDigest: strings.Repeat("A", 43), TomRef: req.TomRef, Ref: req.Ref, TTL: "30m", DecidedAt: testNow}
					status := http.StatusAccepted
					switch mode {
					case "wrong-ref":
						out.ReservedDecision.Ref = "other"
					case "wrong-tom-ref":
						out.ReservedDecision.TomRef = "other"
					case "wrong-uid":
						out.UID = "other"
					case "wrong-digest":
						out.ScopeDigest = strings.Repeat("B", 43)
					case "wrong-ttl":
						out.ReservedDecision.TTL = "1h"
					case "wrong-case":
						out.ReservedDecision.Case = "rd3a"
					case "no-audit":
						out.ReservedDecision = nil
					case "missing-actor":
						out.ReservedDecision.ActorUID = ""
					case "unavailable":
						return http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"source test"}}`
					case "accepted-wrong-status":
						status = http.StatusOK
					}
					raw, _ := json.Marshal(out)
					return status, string(raw)
				},
			})
			args := []string{"grant", "decide", "grant-a", "-o", "json", "--grant-uid", reservedTestUID, "--digest", current.ScopeDigest, "--ref", "request-1", "--reserved", "--tom-ref", "tom-message-1"}
			if mode != "deny" {
				args = append(args, "--approve")
			}
			want := ExitUnconfirmed
			if mode == "valid" || mode == "deny" {
				want = ExitOK
			}
			h.mustRun(want, args...)
			if len(h.api.requests()) != 2 || len(h.sleeps) != 0 {
				t.Fatal("reserved mutation retried or polled")
			}
			if want == ExitOK {
				contains(t, "binding", h.stderr.String(), "agent assertion, not verified human consent")
			} else if h.stdout.Len() != 0 {
				t.Fatal("uncertain decision printed a receipt")
			}
		})
	}
}

func TestReservedMCPFieldsUseSameRequestAndDecisionContracts(t *testing.T) {
	args := accessToolArgs{Type: "kube", Role: "dev-env-grant-secrets-read", Reason: "test", AllNamespaces: true, TTL: "30m"}
	req, err := accessRequest(args)
	if err != nil || !req.Reserved || !req.AllNamespaces {
		t.Fatal("MCP dropped reserved scope", req, err)
	}
	found := map[string]map[string]any{}
	for _, tool := range accessTools() {
		found[tool["name"].(string)] = tool
	}
	for _, name := range []string{"request_access", "decide_grant", "hardware_intent"} {
		if found[name] == nil {
			t.Fatal("MCP tool missing", name)
		}
	}
	props := found["decide_grant"]["inputSchema"].(map[string]any)["properties"].(map[string]any)
	if props["reserved"] == nil || props["tomRef"] == nil {
		t.Fatal("MCP omitted decision flags")
	}
}
