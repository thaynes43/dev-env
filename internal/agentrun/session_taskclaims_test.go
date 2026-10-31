package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/pages"
)

func TestShowSessionTaskClaims(t *testing.T) {
	for _, mode := range []string{"available", "empty", "Unknown", "old operator"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("claimed-session", "Running")
			if mode != "old operator" {
				s.TaskClaims = &apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{}}
			}
			if mode == "Unknown" {
				s.TaskClaims.State, s.TaskClaims.Reason = "Unknown", "Unavailable"
			}
			if mode == "available" {
				s.TaskClaims.Items = []apiv1.TaskClaim{{Name: "canonical-claim", UID: "canonical-uid", Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:322"}, CreatedAt: testNow, Actor: apiv1.TaskClaimActor{Kind: "coordinator", ServiceAccount: "dev/host", PodName: "source-pod", PodUID: "pod-uid"}, Provenance: apiv1.TaskClaimProvenance{ReportedHead: strings.Repeat("a", 40)}}}
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Header.Get(apiv1.SessionTaskClaimsHeader) != apiv1.SessionTaskClaimsV1 {
					t.Error("show did not negotiate claims")
				}
				writeTestJSON(w, http.StatusOK, s)
			}
			h.mustRun(ExitOK, "show", s.Name)
			switch mode {
			case "available":
				contains(t, "claim metadata", h.stdout.String(), "sample/alias/issue:322", "canonical-claim", "canonical-uid", "coordinator", "dev/host", "source-pod", "pod-uid", testNow.Format("2006-01-02T15:04:05Z07:00"), strings.Repeat("a", 40))
			case "empty":
				contains(t, "empty observation", h.stdout.String(), "Task claims:", "Unknown")
			default:
				contains(t, "missing observation", h.stdout.String(), "Task claims:", "Unknown")
			}
			if len(h.api.requests()) != 1 {
				t.Fatal("show fanned out claim reads")
			}
		})
	}
}

func TestPagesMCPTaskClaimOwner(t *testing.T) {
	id := pages.AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	sources := responseSourcesFor(at, id, "claimed", "claimed", "https://example.test/work_packages/313")
	claimSource := sources.Claims.(parityClaims)
	claimSource.observation.Claim.Reference = pages.CanonicalClaimReference{Kind: "TaskClaim", ID: "dev-env-system/synthetic-claim/canonical-uid"}
	owner := apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "claimed-session", UID: "original-session-uid"}
	claimSource.observation.Claim.ClaimOwner = owner
	sources.Claims = claimSource
	mcp := &pagesMCPSession{config: pagesMCPConfig{Alertmanager: true}, backend: &fakePagesBackend{raw: []byte(responseAlertFixture)}, responses: sources, now: func() time.Time { return at }}
	mcp.ctx = context.Background()
	for _, tool := range []string{"pages_list", "pages_get"} {
		args := `{"source":"alertmanager"}`
		if tool == "pages_get" {
			args = `{"source":"alertmanager","key":"synthetic-fingerprint"}`
		}
		var out pagesMCPResult
		if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, tool, args))), &out); err != nil || len(out.Items) != 1 {
			t.Fatal(tool, err, out)
		}
		if got := out.Items[0].Response; got == nil || got.ClaimOwner != owner || got.Claim.ID != "dev-env-system/synthetic-claim/canonical-uid" {
			t.Fatal("MCP lost canonical custody", got)
		}
	}
}

func TestShowSessionTaskClaimsJSONUnknownOnOlderOperator(t *testing.T) {
	h := newHarness(t)
	s := testSession("older-operator", "Running")
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, s) }
	h.mustRun(ExitOK, "show", s.Name, "-o", "json")
	var got apiv1.Session
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || got.TaskClaims == nil || got.TaskClaims.State != "Unknown" || len(got.TaskClaims.Items) != 0 {
		t.Fatal("old operator omission is not Unknown in JSON", err, h.stdout.String())
	}
}
