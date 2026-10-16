package agentd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

func taskClaimViewFixture() apiv1.TaskClaim {
	id := apiv1.TaskClaimIdentity{Project: "sample", Repository: "repo", TaskKey: "task:pilot"}
	return apiv1.TaskClaim{Name: taskclaim.Name(coord.TaskIdentity{Project: id.Project, Repository: id.Repository, TaskKey: id.TaskKey}), UID: "11111111-1111-4111-8111-111111111111", Identity: id, Owner: apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "owner", UID: "22222222-2222-4222-8222-222222222222"}}
}
func TestTaskClaimClientFixedOperationsRotationAndConflict(t *testing.T) {
	claim := taskClaimViewFixture()
	calls := 0
	var auths []string
	_, s := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		auths = append(auths, r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		switch calls {
		case 1, 2:
			if r.Method != "POST" || r.URL.RequestURI() != "/v1/task-claims" {
				t.Error("create route", r.Method, r.URL)
			}
			var req apiv1.CreateTaskClaimRequest
			if json.Unmarshal(body, &req) != nil || req.Owner != nil || req.Identity != claim.Identity {
				t.Error("intent changed or owner supplied")
			}
			status, outcome := 201, "Created"
			if calls == 2 {
				status, outcome = 409, "Conflict"
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(apiv1.TaskClaimResult{Outcome: outcome, Claim: claim})
		case 3:
			if r.Method != "GET" || r.URL.Query().Get("continue") != "opaque&/cursor" || r.URL.Query().Get("project") != "sample" || r.URL.Query().Get("limit") != "1" {
				t.Error("list scope changed", r.URL)
			}
			_ = json.NewEncoder(w).Encode(apiv1.TaskClaimList{Items: []apiv1.TaskClaim{claim}, Continue: "next-page"})
		case 4:
			if r.URL.Path != "/v1/task-claims/"+claim.Name || r.URL.Query().Get("uid") != claim.UID {
				t.Error("get fence changed", r.URL)
			}
			_ = json.NewEncoder(w).Encode(claim)
		default:
			t.Error("unexpected retry")
		}
	}))
	c, err := NewTaskClaimClient(s)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err = os.WriteFile(s.APITokenFile, []byte([]string{"first", "rotated"}[i]), 0600); err != nil {
			t.Fatal(err)
		}
		data, status, e := c.Create(context.Background(), claim.Identity)
		if e != nil || status != []int{201, 409}[i] || !strings.Contains(string(data), "22222222-2222-4222-8222-222222222222") {
			t.Fatal(status, e, string(data))
		}
	}
	if _, _, err = c.List(context.Background(), "sample", 1, "opaque&/cursor"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.Get(context.Background(), "sample", claim.Name, claim.UID); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || auths[0] != "Bearer first" || auths[1] != "Bearer rotated" {
		t.Fatal("identity rotation/retry changed")
	}
	before := calls
	for _, id := range []apiv1.TaskClaimIdentity{{}, {Project: "sample", Repository: "repo", TaskKey: "issue:0"}} {
		if _, _, err = c.Create(context.Background(), id); err == nil {
			t.Fatal("invalid intent accepted")
		}
	}
	if _, _, err = c.List(context.Background(), "sample", 1, "\n"); err == nil {
		t.Fatal("cursor accepted")
	}
	if _, _, err = c.Get(context.Background(), "sample", "../../inbox", ""); err == nil {
		t.Fatal("path escaped")
	}
	if calls != before {
		t.Fatal("refused input reached API")
	}
}
func TestTaskClaimClientTruthfulFailureAndReplyFences(t *testing.T) {
	for _, kind := range []string{"forbidden", "unavailable", "network", "redirect", "overflow", "bad-json", "wrong-project", "wrong-uid", "null-list", "wrong-outcome", "unexpected-status"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			claim := taskClaimViewFixture()
			_, s := inboxTLSFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch kind {
				case "forbidden":
					w.WriteHeader(403)
					_, _ = io.WriteString(w, `{"error":{"code":"forbidden"}}`)
				case "unavailable":
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `{"error":{"code":"unavailable"}}`)
				case "network":
					h := w.(http.Hijacker)
					conn, _, _ := h.Hijack()
					_ = conn.Close()
				case "redirect":
					http.Redirect(w, r, "/v1/inbox", 307)
				case "overflow":
					_, _ = io.WriteString(w, strings.Repeat(" ", InboxMaxResponseBytes+1))
				case "bad-json":
					_, _ = io.WriteString(w, `{} {}`)
				case "wrong-project":
					claim.Identity.Project = "other"
					_ = json.NewEncoder(w).Encode(claim)
				case "wrong-uid":
					claim.UID = "33333333-3333-4333-8333-333333333333"
					_ = json.NewEncoder(w).Encode(claim)
				case "null-list":
					_, _ = io.WriteString(w, `{"items":null}`)
				case "wrong-outcome":
					_ = json.NewEncoder(w).Encode(apiv1.TaskClaimResult{Outcome: "Created", Claim: claim})
				case "unexpected-status":
					w.WriteHeader(202)
					_ = json.NewEncoder(w).Encode(claim)
				}
			}))
			c, e := NewTaskClaimClient(s)
			if e != nil {
				t.Fatal(e)
			}
			var data []byte
			var status int
			switch kind {
			case "null-list":
				data, status, e = c.List(context.Background(), "sample", 1, "")
			case "wrong-outcome":
				data, status, e = c.Create(context.Background(), claim.Identity)
			default:
				data, status, e = c.Get(context.Background(), "sample", claim.Name, claim.UID)
			}
			if kind == "forbidden" || kind == "unavailable" {
				if e != nil || status/100 == 2 || !json.Valid(data) {
					t.Fatal("HTTP refusal lost", status, e)
				}
			} else if e == nil {
				t.Fatal("unconfirmed reply accepted", status, string(data))
			}
			if calls != 1 {
				t.Fatal("retried", calls)
			}
			if e != nil && (strings.Contains(e.Error(), "token-one") || strings.Contains(e.Error(), s.APITokenFile)) {
				t.Fatal("token leaked")
			}
		})
	}
}
