package agentrun

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const (
	claimBindSession    = "maintenance-owner"
	claimBindSessionUID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	claimBindPodUID     = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
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
	owner := apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: "maintenance-owner", UID: claimBindSessionUID}
	items := []apiv1.TaskClaim{
		{Name: nameA, UID: uidA, Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:322"}, Owner: owner},
		{Name: nameB, UID: uidB, Identity: apiv1.TaskClaimIdentity{Project: "sample", Repository: "alias", TaskKey: "issue:323"}, Owner: owner},
	}
	h := newHarness(t)
	var header string
	serve := func(claims *apiv1.SessionTaskClaims) {
		t.Helper()
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			header = r.Header.Get(apiv1.GrantTaskClaimsHeader)
			writeTestJSON(w, http.StatusOK, apiv1.Grant{Name: "grant-maint", Type: "maintenance", Session: "maintenance-owner", TTL: "30m0s", Reason: "synthetic", Phase: "Pending", TaskClaims: claims, Maintenance: &apiv1.MaintenanceTask{Operation: "issue:322", Coordinator: apiv1.MaintenanceIdentity{Name: "maintenance-owner", UID: claimBindSessionUID, PodUID: claimBindPodUID}, TaskClaim: &apiv1.MaintenanceTaskClaim{Name: nameB, UID: uidB, Project: "sample"}}})
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

type grantClaimBindingCase struct {
	name           string
	grant          apiv1.Grant
	wantState      string
	wantReason     string
	wantBindState  string
	wantBindReason string
	wantBindName   string
	wantBindUID    string
	wantBindProj   string
	clearItems     bool
	textHas        []string
	textAbsent     []string
}

func TestGrantShowClaimBindingConsistency(t *testing.T) {
	for _, tc := range grantClaimBindingCases() {
		t.Run(tc.name, func(t *testing.T) {
			inputItems := append([]apiv1.TaskClaim(nil), tc.grant.TaskClaims.Items...)
			inputBinding := *tc.grant.TaskClaims.ClaimBinding
			normalized := normalizeMaintenanceGrantClaims(tc.grant)
			if tc.grant.TaskClaims.ClaimBinding.State != inputBinding.State || tc.grant.TaskClaims.ClaimBinding.UID != inputBinding.UID || tc.grant.TaskClaims.ClaimBinding.Name != inputBinding.Name {
				t.Fatal("normalization mutated the wire binding", tc.grant.TaskClaims.ClaimBinding)
			}
			for i := range inputItems {
				if tc.grant.TaskClaims.Items[i].Name != inputItems[i].Name || tc.grant.TaskClaims.Items[i].UID != inputItems[i].UID || tc.grant.TaskClaims.Items[i].Identity != inputItems[i].Identity || tc.grant.TaskClaims.Items[i].Owner != inputItems[i].Owner {
					t.Fatal("normalization mutated a wire item", i)
				}
			}
			assertClaimBindingCase(t, tc, normalized)
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Header.Get(apiv1.GrantTaskClaimsHeader) != apiv1.GrantTaskClaimsV1 || r.Method != http.MethodGet || r.URL.RawQuery != "" {
					t.Errorf("grant show %s %s header %q", r.Method, r.URL.RequestURI(), r.Header.Get(apiv1.GrantTaskClaimsHeader))
				}
				writeTestJSON(w, http.StatusOK, tc.grant)
			}
			h.mustRun(ExitOK, "grant", "show", "grant-maint", "-o", "json")
			if len(h.api.requests()) != 1 {
				t.Fatal("grant show fanned out", len(h.api.requests()))
			}
			got := decodeShownGrant(t, h.stdout.Bytes())
			assertClaimBindingCase(t, tc, got.TaskClaims)
			if got.Maintenance == nil || got.Maintenance.Coordinator != tc.grant.Maintenance.Coordinator || (tc.grant.Maintenance.TaskClaim == nil) != (got.Maintenance.TaskClaim == nil) || (tc.grant.Maintenance.TaskClaim != nil && *got.Maintenance.TaskClaim != *tc.grant.Maintenance.TaskClaim) {
				t.Fatal("maintenance envelope changed", got.Maintenance)
			}
			h.mustRun(ExitOK, "grant", "show", "grant-maint")
			if len(h.api.requests()) != 2 {
				t.Fatal("text show fanned out", len(h.api.requests()))
			}
			text := h.stdout.String()
			for _, needle := range tc.textHas {
				if !strings.Contains(text, needle) {
					t.Fatalf("text missing %q\n%s", needle, text)
				}
			}
			for _, needle := range tc.textAbsent {
				if strings.Contains(text, needle) {
					t.Fatalf("text contains %q\n%s", needle, text)
				}
			}
		})
	}
}

func assertClaimBindingCase(t *testing.T, tc grantClaimBindingCase, got *apiv1.SessionTaskClaims) {
	t.Helper()
	if got == nil || got.State != tc.wantState || got.Reason != tc.wantReason || got.ClaimBinding == nil {
		t.Fatal("claims", tc.name, got)
	}
	b := got.ClaimBinding
	if b.State != tc.wantBindState || b.Reason != tc.wantBindReason || b.Name != tc.wantBindName || b.UID != tc.wantBindUID || b.Project != tc.wantBindProj {
		t.Fatalf("binding %+v", b)
	}
	if tc.clearItems {
		if len(got.Items) != 0 {
			t.Fatalf("unknown observation kept %d items", len(got.Items))
		}
		return
	}
	if len(got.Items) != len(tc.grant.TaskClaims.Items) {
		t.Fatalf("items %d want %d", len(got.Items), len(tc.grant.TaskClaims.Items))
	}
	for i := range tc.grant.TaskClaims.Items {
		item := tc.grant.TaskClaims.Items[i]
		if got.Items[i].Name != item.Name || got.Items[i].UID != item.UID || got.Items[i].Identity != item.Identity || got.Items[i].Owner != item.Owner || got.Items[i].Provenance.ReportedHead != item.Provenance.ReportedHead {
			t.Fatalf("item %d changed: %+v", i, got.Items[i])
		}
	}
}

func grantClaimBindingCases() []grantClaimBindingCase {
	nameA := canonicalClientClaimName('a', 'b')
	nameB := canonicalClientClaimName('c', 'd')
	uidA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	uidB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	owner := claimBindOwner()
	otherOwner := owner
	otherOwner.Name = "other-owner"
	otherOwner.UID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	refB := &apiv1.MaintenanceTaskClaim{Name: nameB, UID: uidB, Project: "sample"}
	refA := &apiv1.MaintenanceTaskClaim{Name: nameA, UID: uidA, Project: "sample"}
	itemA := claimBindItem(nameA, uidA, "sample", "issue:322", owner)
	itemB := claimBindItem(nameB, uidB, "sample", "issue:323", owner)
	leaked := claimBindItem(nameA, uidA, "sample", "issue:secret", owner)
	unknownParent := func(reason string) *apiv1.SessionTaskClaims {
		return &apiv1.SessionTaskClaims{State: "Unknown", Reason: reason, Items: []apiv1.TaskClaim{leaked}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}
	}
	noPositive := []string{"Observed (Exact)", "associated claim", "Unclaimed", uidB}
	meta := []string{"not approval", "not approval, permission, stop proof, or custody"}
	return []grantClaimBindingCase{
		{
			name: "unknown parent observed", grant: claimBindingGrant(unknownParent("Unavailable"), refB, "", ""),
			wantState: "Unknown", wantReason: "Unavailable", wantBindState: "Unknown", wantBindReason: "Unavailable",
			clearItems: true, textHas: append([]string{"Task claim association: Unknown (Unavailable)", "Task claims: Unknown (Unavailable)"}, meta...), textAbsent: append(noPositive, "issue:secret", uidA),
		},
		{
			name: "unknown parent scope observed", grant: claimBindingGrant(unknownParent("ScopeUnconfirmed"), refB, "", ""),
			wantState: "Unknown", wantReason: "ScopeUnconfirmed", wantBindState: "Unknown", wantBindReason: "ScopeUnconfirmed",
			clearItems: true, textHas: append([]string{"Task claim association: Unknown (ScopeUnconfirmed)"}, meta...), textAbsent: append(noPositive, "issue:secret"),
		},
		{
			name: "empty observed", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Unknown", wantReason: "NoJoinedClaims", wantBindState: "Unknown", wantBindReason: "NoJoinedClaims",
			clearItems: true, textHas: append([]string{"Task claim association: Unknown (NoJoinedClaims)", "Task claims: Unknown (NoJoinedClaims)"}, meta...), textAbsent: append(noPositive, "Complete"),
		},
		{
			name: "unrelated items", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemA, claimBindItem(canonicalClientClaimName('e', 'f'), "ffffffff-ffff-4fff-8fff-ffffffffffff", "sample", "issue:400", owner)}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "NotObserved",
			textHas: append([]string{"Task claim association: Unknown (NotObserved)", "Task claims: Available (2)", uidA}, meta...), textAbsent: append([]string{"Observed (Exact)", "associated claim", "Unclaimed"}, uidB),
		},
		{
			name: "duplicate exact", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemB, claimBindItem(nameB, uidB, "sample", "issue:324", owner)}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "Ambiguous",
			textHas: append([]string{"Task claim association: Unknown (Ambiguous)", "Task claims: Available (2)", "issue:323", "issue:324"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "envelope disagreement", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemA}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refA, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "Stale",
			textHas: append([]string{"Task claim association: Unknown (Stale)", uidA}, meta...), textAbsent: append([]string{"Observed (Exact)", "associated claim", "Unclaimed"}, uidB),
		},
		{
			name: "owner mismatch", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{claimBindItem(nameB, uidB, "sample", "issue:323", otherOwner)}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "OwnerMismatch",
			textHas: append([]string{"Task claim association: Unknown (OwnerMismatch)", "other-owner"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "project mismatch", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{claimBindItem(nameB, uidB, "other", "issue:323", owner)}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "ProjectMismatch",
			textHas: append([]string{"Task claim association: Unknown (ProjectMismatch)"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "missing reference", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemA}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, nil, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "NotObserved",
			textHas: append([]string{"Task claim association: Unknown (NotObserved)", uidA}, meta...), textAbsent: append([]string{"Observed (Exact)", "associated claim", "Unclaimed"}, uidB),
		},
		{
			name: "invalid reference", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemB}, ClaimBinding: exactClientBinding("issue:323", uidB, "sample")}, &apiv1.MaintenanceTaskClaim{Name: "issue:323", UID: uidB, Project: "sample"}, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "Invalid",
			textHas: append([]string{"Task claim association: Unknown (Invalid)"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "coordinator disagreement", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemB}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "other-session", claimBindSessionUID),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "IdentityUnconfirmed",
			textHas: append([]string{"Task claim association: Unknown (IdentityUnconfirmed)"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "stale partial", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{claimBindItem(nameB, uidA, "sample", "issue:323", owner)}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Unknown", wantBindReason: "Stale",
			textHas: append([]string{"Task claim association: Unknown (Stale)"}, meta...), textAbsent: []string{"Observed (Exact)", "associated claim", "Unclaimed"},
		},
		{
			name: "valid canonical", grant: claimBindingGrant(&apiv1.SessionTaskClaims{State: "Available", Reason: "Complete", Items: []apiv1.TaskClaim{itemA, itemB}, ClaimBinding: exactClientBinding(nameB, uidB, "sample")}, refB, "", ""),
			wantState: "Available", wantReason: "Complete", wantBindState: "Observed", wantBindReason: "Exact", wantBindName: nameB, wantBindUID: uidB, wantBindProj: "sample",
			textHas: append([]string{"Task claim association: Observed (Exact)", "associated claim:", nameB, uidB, "project sample", "issue:322", "issue:323"}, meta...), textAbsent: []string{"Unclaimed"},
		},
	}
}

func claimBindingGrant(claims *apiv1.SessionTaskClaims, ref *apiv1.MaintenanceTaskClaim, coordName, coordUID string) apiv1.Grant {
	if coordName == "" {
		coordName = claimBindSession
	}
	if coordUID == "" {
		coordUID = claimBindSessionUID
	}
	return apiv1.Grant{
		Name: "grant-maint", Type: "maintenance", Session: claimBindSession, TTL: "30m0s", Reason: "synthetic", Phase: "Pending",
		TaskClaims: claims,
		Maintenance: &apiv1.MaintenanceTask{
			Operation: "issue:322", Coordinator: apiv1.MaintenanceIdentity{Name: coordName, UID: coordUID, PodUID: claimBindPodUID}, TaskClaim: ref,
		},
	}
}

func claimBindOwner() apiv1.TaskClaimOwner {
	return apiv1.TaskClaimOwner{Namespace: "dev-agents", Name: claimBindSession, UID: claimBindSessionUID}
}

func claimBindItem(name, uid, project, taskKey string, owner apiv1.TaskClaimOwner) apiv1.TaskClaim {
	return apiv1.TaskClaim{
		Name: name, UID: uid,
		Identity:   apiv1.TaskClaimIdentity{Project: project, Repository: "dev-env", TaskKey: taskKey},
		Owner:      owner,
		Actor:      apiv1.TaskClaimActor{Kind: "session", ServiceAccount: "dev-agents/dev-env", PodName: claimBindSession, PodUID: claimBindPodUID},
		Provenance: apiv1.TaskClaimProvenance{ReportedHead: strings.Repeat("ab", 20)},
		CreatedAt:  testNow, OwnerState: "Running", StopState: "Unknown",
	}
}

func exactClientBinding(name, uid, project string) *apiv1.MaintenanceClaimBinding {
	return &apiv1.MaintenanceClaimBinding{State: "Observed", Reason: "Exact", Name: name, UID: uid, Project: project}
}
