package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestMaintenanceRequestClaimBindingProducer(t *testing.T) {
	h := newHarness(t)
	var headers []string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		headers = append(headers, r.Header.Get(apiv1.GrantTaskClaimsHeader))
		if r.URL.Path == apiv1.GrantsPath && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"name":"grant-fixture","type":"maintenance","phase":"Pending"}`))
			return
		}
		if r.URL.Path == apiv1.GrantsPath && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"items":[]}`))
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
	}
	name := canonicalClientClaimName('a', 'b')
	uid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	task := map[string]any{
		"runtimeProfile": "nvidia-utility", "node": "synthetic-node", "nodeUID": "node-uid",
		"resource": "device", "operation": "issue:322", "image": "digest-reviewed",
		"cpuMillis": 100, "memoryMiB": 128,
		"coordinator": map[string]string{"name": "ignored", "uid": "ignored", "podUID": "ignored"},
	}
	raw, _ := json.Marshal(task)
	before := len(h.api.requests())
	h.mustRun(ExitUsage, "grant", "maintenance-request", "--task-json", string(withTaskClaim(t, raw, map[string]string{"name": "issue:322", "uid": uid, "project": "sample"})), "--reason", "synthetic maintenance")
	h.mustRun(ExitUsage, "grant", "maintenance-request", "--task-json", string(withTaskClaim(t, raw, map[string]string{"name": name, "uid": "NOT-A-UID", "project": "sample"})), "--reason", "synthetic maintenance")
	h.mustRun(ExitUsage, "grant", "maintenance-request", "--task-json", string(withTaskClaim(t, raw, map[string]string{"name": name, "uid": uid, "project": "Bad"})), "--reason", "synthetic maintenance")
	if len(h.api.requests()) != before {
		t.Fatal("invalid association was posted", h.api.requests())
	}
	h.mustRun(ExitOK, "grant", "maintenance-request", "--task-json", string(raw), "--reason", "synthetic maintenance")
	if bytes.Contains(h.api.requests()[0].body, []byte("taskClaim")) || headers[0] != "" {
		t.Fatal("legacy request gained an association or the claims header", headers, h.api.requests()[0].body)
	}
	posted := withTaskClaim(t, raw, map[string]string{"name": name, "uid": uid, "project": "sample"})
	h.mustRun(ExitOK, "grant", "maintenance-request", "--task-json", string(posted), "--reason", "synthetic maintenance")
	var req apiv1.CreateGrantRequest
	if err := json.Unmarshal(h.api.requests()[1].body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Type != "maintenance" || req.Maintenance == nil || req.Maintenance.TaskClaim == nil || req.Maintenance.TaskClaim.Name != name || req.Maintenance.TaskClaim.UID != uid || req.Maintenance.TaskClaim.Project != "sample" || req.Maintenance.Operation != "issue:322" || headers[1] != "" {
		t.Fatal(req.Maintenance, headers)
	}
	h.mustRun(ExitOK, "grant", "list")
	if headers[2] != "" || strings.Contains(h.stdout.String(), "taskClaims") || strings.Contains(h.stdout.String(), "claimBinding") || strings.Contains(h.stdout.String(), "NotReported") {
		t.Fatal("grant list negotiated claim metadata", headers, h.stdout.String())
	}
}

func TestGrantShowClaimBindingConsumer(t *testing.T) {
	nameA := canonicalClientClaimName('a', 'b')
	nameB := canonicalClientClaimName('c', 'd')
	uidA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uidB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	items := []apiv1.TaskClaim{
		{Name: nameA, UID: uidA, Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:322"}},
		{Name: nameB, UID: uidB, Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:323"}},
	}
	h := newHarness(t)
	var header string
	serve := func(claims *apiv1.SessionTaskClaims) {
		t.Helper()
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			header = r.Header.Get(apiv1.GrantTaskClaimsHeader)
			writeTestJSON(w, http.StatusOK, apiv1.Grant{Name: "grant-maint", Type: "maintenance", Session: "maintenance-owner", TTL: "30m0s", Reason: "synthetic", Phase: "Pending", TaskClaims: claims, Maintenance: &apiv1.MaintenanceTask{Operation: "issue:322", TaskClaim: &apiv1.MaintenanceTaskClaim{Name: nameB, UID: uidB, Project: "sample"}}})
		}
	}
	serve(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: items, ClaimBinding: &apiv1.MaintenanceClaimBinding{State: "Observed", Reason: "Exact", Name: nameB, UID: uidB, Project: "sample"}})
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	got := decodeShownGrant(t, h.stdout.Bytes())
	if header != apiv1.GrantTaskClaimsV1 || got.TaskClaims == nil || got.TaskClaims.ClaimBinding == nil || got.TaskClaims.ClaimBinding.State != "Observed" || got.TaskClaims.ClaimBinding.UID != uidB || len(got.TaskClaims.Items) != 2 || got.Maintenance == nil || got.Maintenance.TaskClaim == nil || got.Maintenance.TaskClaim.UID != uidB {
		t.Fatal(header, got.TaskClaims, got.Maintenance)
	}
	h.mustRun(ExitOK, "grant", "show", "grant-maint")
	if !strings.Contains(h.stdout.String(), "Task claim association: Observed (Exact)") || !strings.Contains(h.stdout.String(), uidB) || !strings.Contains(h.stdout.String(), "not approval") || strings.Contains(h.stdout.String(), "Unclaimed") {
		t.Fatal(h.stdout.String())
	}
	serve(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: items, ClaimBinding: &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: "Ambiguous"}})
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	got = decodeShownGrant(t, h.stdout.Bytes())
	if got.TaskClaims == nil || got.TaskClaims.ClaimBinding == nil || got.TaskClaims.ClaimBinding.State != "Unknown" || got.TaskClaims.ClaimBinding.Reason != "Ambiguous" || got.TaskClaims.ClaimBinding.UID != "" || len(got.TaskClaims.Items) != 2 || got.TaskClaims.Items[0].UID != uidA || got.TaskClaims.Items[1].UID != uidB {
		t.Fatal("competing claims selected a winner", got.TaskClaims)
	}
	h.mustRun(ExitOK, "grant", "show", "grant-maint")
	if !strings.Contains(h.stdout.String(), "Unknown (Ambiguous)") || strings.Contains(h.stdout.String(), "associated claim") || strings.Contains(h.stdout.String(), "Unclaimed") {
		t.Fatal(h.stdout.String())
	}
	serve(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: items})
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	got = decodeShownGrant(t, h.stdout.Bytes())
	if got.TaskClaims == nil || got.TaskClaims.ClaimBinding != nil || len(got.TaskClaims.Items) != 2 {
		t.Fatal("legacy observation invented a binding", got.TaskClaims)
	}
	serve(&apiv1.SessionTaskClaims{State: "Unknown", Reason: "Unavailable", Items: []apiv1.TaskClaim{}, ClaimBinding: &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: "Unavailable"}})
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	got = decodeShownGrant(t, h.stdout.Bytes())
	if got.TaskClaims == nil || got.TaskClaims.State != "Unknown" || got.TaskClaims.Reason != "Unavailable" || got.TaskClaims.ClaimBinding == nil || got.TaskClaims.ClaimBinding.Reason != "Unavailable" || len(got.TaskClaims.Items) != 0 {
		t.Fatal(got.TaskClaims)
	}
	serve(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: items, ClaimBinding: &apiv1.MaintenanceClaimBinding{State: "Unknown", Reason: "Stale"}})
	h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
	got = decodeShownGrant(t, h.stdout.Bytes())
	if got.TaskClaims == nil || got.TaskClaims.ClaimBinding == nil || got.TaskClaims.ClaimBinding.Reason != "Stale" || len(got.TaskClaims.Items) != 2 {
		t.Fatal(got.TaskClaims)
	}
}

func withTaskClaim(t *testing.T, raw []byte, claim map[string]string) []byte {
	t.Helper()
	var task map[string]any
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatal(err)
	}
	task["taskClaim"] = claim
	out, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decodeShownGrant(t *testing.T, raw []byte) apiv1.Grant {
	t.Helper()
	var got apiv1.Grant
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err, string(raw))
	}
	return got
}

func canonicalClientClaimName(left, right byte) string {
	return "task-claim." + strings.Repeat(string(left), 32) + "." + strings.Repeat(string(right), 32)
}
