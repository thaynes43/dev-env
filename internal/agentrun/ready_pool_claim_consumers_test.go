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

func TestReadyPoolShowAndPagesKeepSessionOwner(t *testing.T) {
	claim, owner, ref := readyPoolClaim()
	h := newHarness(t)
	s := testSession(owner.Name, "Running")
	s.UID = owner.UID
	s.TaskClaims = &apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{claim}}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get(apiv1.SessionTaskClaimsHeader) != apiv1.SessionTaskClaimsV1 {
			t.Error("show did not negotiate claims")
		}
		writeTestJSON(w, http.StatusOK, s)
	}
	h.mustRun(ExitOK, "show", s.Name, "-o", "json")
	var got apiv1.Session
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || got.UID != owner.UID || got.TaskClaims == nil || got.TaskClaims.State != "Available" || len(got.TaskClaims.Items) != 1 {
		t.Fatal("show JSON", err, h.stdout.String())
	}
	item := got.TaskClaims.Items[0]
	if item.Name != claim.Name || item.UID != claim.UID || item.Identity != claim.Identity || item.Owner != claim.Owner || item.Owner.UID != got.UID || item.Actor.PodUID != claim.Actor.PodUID || item.Owner.UID == item.Actor.PodUID {
		t.Fatal("show kept actor pod as owner", item)
	}
	if len(h.api.requests()) != 1 {
		t.Fatal("show fanned out")
	}
	readyPoolPages(t, owner, ref, claim.Actor.PodUID)
}

func TestReadyPoolUnknownShowDropsOwnerPagesKeepsEvidence(t *testing.T) {
	claim, owner, ref := readyPoolClaim()
	h := newHarness(t)
	s := testSession("older-session", "Running")
	s.TaskClaims = &apiv1.SessionTaskClaims{
		State: "Unknown", Reason: "Unavailable", Items: []apiv1.TaskClaim{claim},
		ClaimBinding: &apiv1.MaintenanceClaimBinding{
			State: "Observed", Reason: "Exact", Name: claim.Name, UID: claim.UID, Project: claim.Identity.Project,
		},
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Header.Get(apiv1.SessionTaskClaimsHeader) != apiv1.SessionTaskClaimsV1 {
			t.Error("show did not negotiate claims")
		}
		writeTestJSON(w, http.StatusOK, s)
	}
	h.mustRun(ExitOK, "show", s.Name, "-o", "json")
	var got apiv1.Session
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || got.TaskClaims == nil || got.TaskClaims.State != "Unknown" || got.TaskClaims.Reason != "Unavailable" || len(got.TaskClaims.Items) != 0 {
		t.Fatal("unknown show", err, h.stdout.String())
	}
	b := got.TaskClaims.ClaimBinding
	out := h.stdout.String()
	if b == nil || b.State != "Unknown" || b.Reason != "Unavailable" || b.Name != "" || b.UID != "" || b.Project != "" || strings.Contains(out, owner.UID) || strings.Contains(out, claim.UID) || strings.Contains(out, claim.Actor.PodUID) || strings.Contains(out, claim.Name) || strings.Contains(out, owner.Name) || strings.Contains(out, "Exact") {
		t.Fatal("unknown show leaked joined owner", out)
	}
	if len(h.api.requests()) != 1 {
		t.Fatal("show fanned out")
	}
	readyPoolPages(t, owner, ref, claim.Actor.PodUID)
}

func readyPoolClaim() (apiv1.TaskClaim, pages.ClaimOwner, string) {
	owner := pages.ClaimOwner{Namespace: "dev-agents", Name: "claimed-session", UID: "original-session-uid"}
	claim := apiv1.TaskClaim{
		Name:      "synthetic-claim",
		UID:       "canonical-uid",
		Identity:  apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:322"},
		Owner:     apiv1.TaskClaimOwner{Namespace: owner.Namespace, Name: owner.Name, UID: owner.UID},
		Actor:     apiv1.TaskClaimActor{Kind: "coordinator", ServiceAccount: "dev/host", PodName: "source-pod", PodUID: "actor-pod-uid"},
		CreatedAt: testNow,
	}
	return claim, owner, "dev-env-system/" + claim.Name + "/" + claim.UID
}

func readyPoolPages(t *testing.T, owner pages.ClaimOwner, ref, podUID string) {
	t.Helper()
	id := pages.AlertIdentity{Fingerprint: "synthetic-fingerprint", StartsAt: "2026-10-30T11:00:00Z"}
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	sources := responseSourcesFor(at, id, "claimed", "claimed", "https://example.test/work_packages/313")
	claimSource := sources.Claims.(parityClaims)
	claimSource.observation.Claim.Reference = pages.CanonicalClaimReference{Kind: "TaskClaim", ID: ref}
	claimSource.observation.Claim.ClaimOwner = owner
	sources.Claims = claimSource
	mcp := &pagesMCPSession{
		ctx:       context.Background(),
		config:    pagesMCPConfig{Alertmanager: true},
		backend:   &fakePagesBackend{raw: []byte(responseAlertFixture)},
		responses: sources,
		now:       func() time.Time { return at },
	}
	for _, tool := range []string{"pages_list", "pages_get"} {
		args := `{"source":"alertmanager"}`
		if tool == "pages_get" {
			args = `{"source":"alertmanager","key":"synthetic-fingerprint"}`
		}
		var out pagesMCPResult
		if err := json.Unmarshal([]byte(responseText(t, callPages(mcp, tool, args))), &out); err != nil || len(out.Items) != 1 {
			t.Fatal(tool, err, out)
		}
		got := out.Items[0].Response
		if got == nil || got.Claim.Kind != "TaskClaim" || got.Claim.ID != ref || got.ClaimOwner != owner || got.ClaimOwner.UID == podUID {
			t.Fatal(tool, "pages lost session owner", got)
		}
	}
}
