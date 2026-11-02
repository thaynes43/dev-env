package homeretention

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCleanupPreflightExactSetHasNoExecutionAuthority(t *testing.T) {
	f := newFixture(t)
	report, err := Preflight(baselinePreflight(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Reasons) != 0 || !report.DryRun || report.DeleteEnabled || report.ExecutionAuthority {
		t.Fatalf("preflight granted authority: %+v", report)
	}
	second := rebind(t, "pvc-b", "session-b", "pod-b")
	left, right := baselinePreflight(t, f), baselinePreflight(t, second)
	left.Approval.UIDs = []string{second.record.UID, f.record.UID}
	left.Approval.Homes = append(left.Approval.Homes, right.Approval.Homes...)
	left.Fences = append(left.Fences, right.Fences...)
	left.RequestedUIDs = []string{f.record.UID, second.record.UID}
	report, err = Preflight(left)
	if err != nil || len(report.Reasons) != 0 || report.ExecutionAuthority {
		t.Fatalf("exact set reorder: %+v %v", report, err)
	}
}

func TestOlderExactApprovalWithFreshEvidencePasses(t *testing.T) {
	f := newFixture(t)
	req := baselinePreflight(t, f)
	req.Approval.ApprovedAt = formatTime(req.Now.Add(-req.Policy.ObservationMaxAge - time.Second))
	report, err := Preflight(req)
	if err != nil || len(report.Reasons) != 0 || report.DeleteEnabled || report.ExecutionAuthority || !report.DryRun || hasReason(report, "approval_stale") {
		t.Fatalf("older exact approval required another stamp: %+v %v", report, err)
	}
	req.ConsumedApprovalIDs = []string{req.Approval.ApprovalID}
	report, err = Preflight(req)
	if err != nil || !hasReason(report, "approval_replayed") || report.ExecutionAuthority || report.DeleteEnabled {
		t.Fatalf("replayed older approval was accepted: %+v %v", report, err)
	}
	future := baselinePreflight(t, f)
	future.Approval.ApprovedAt = formatTime(future.Now.Add(time.Minute))
	report, err = Preflight(future)
	if err != nil || !hasReason(report, "timestamp_future") || report.ExecutionAuthority {
		t.Fatalf("future approval was accepted: %+v %v", report, err)
	}
	stale := baselinePreflight(t, f)
	stale.Approval.ApprovedAt = formatTime(stale.Now.Add(-stale.Policy.ObservationMaxAge - time.Second))
	old := formatTime(stale.Now.Add(-6 * time.Minute))
	stale.Fences[0].ObservedAt = old
	payload := stale.Fences[0].Observation.Payload
	payload.ObservedAt = old
	stale.Fences[0].Observation.Payload = payload
	stale.Fences[0].Observation.Signature = f.sign(t, "observation", payload)
	report, err = Preflight(stale)
	if err != nil || !hasReason(report, "fence_stale") || hasReason(report, "approval_stale") || report.ExecutionAuthority || report.DeleteEnabled {
		t.Fatalf("stale observation was treated as an approval expiry: %+v %v", report, err)
	}
}

func TestCleanupPreflightRefusesReplacedStaleAndReplayed(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		code string
		edit func(*PreflightRequest)
	}{
		{name: "replaced pvc", code: "pvc_replaced", edit: func(req *PreflightRequest) {
			req.Fences[0].Binding.PVCUID = "pvc-replacement"
		}},
		{name: "replaced pod", code: "pod_replaced", edit: func(req *PreflightRequest) {
			req.Fences[0].Binding.PodUID = "pod-replacement"
		}},
		{name: "late writer revision", code: "late_write", edit: func(req *PreflightRequest) {
			req.Fences[0].WriterRevision = "writer-later"
		}},
		{name: "late home revision", code: "late_write", edit: func(req *PreflightRequest) {
			req.Fences[0].Binding.HomeRevision = "revision-later"
		}},
		{name: "resource version", code: "resource_version_mismatch", edit: func(req *PreflightRequest) {
			req.Fences[0].PodResourceVersion = "pod-rv-later"
		}},
		{name: "new mount", code: "active_mount_present", edit: func(req *PreflightRequest) {
			req.Fences[0].ActiveMountPodUIDs = []string{"pod-new-mount"}
		}},
		{name: "stale proof id", code: "stale_proof", edit: func(req *PreflightRequest) {
			req.Approval.Homes[0].ProofID = "proof-stale"
		}},
		{name: "replayed approval", code: "approval_replayed", edit: func(req *PreflightRequest) {
			req.ConsumedApprovalIDs = []string{req.Approval.ApprovalID}
		}},
		{name: "mismatched set", code: "approved_set_mismatch", edit: func(req *PreflightRequest) {
			req.RequestedUIDs = []string{req.Approval.UIDs[0], "pvc-extra"}
		}},
		{name: "future approval", code: "timestamp_future", edit: func(req *PreflightRequest) {
			req.Approval.ApprovedAt = formatTime(req.Now.Add(time.Minute))
		}},
		{name: "future fence", code: "timestamp_future", edit: func(req *PreflightRequest) {
			req.Fences[0].ObservedAt = formatTime(req.Now.Add(time.Minute))
		}},
		{name: "not approved", code: "disposition_not_approved", edit: func(req *PreflightRequest) {
			req.Approval.Homes[0].OwnerDisposition = OwnerDispositionRetain
		}},
		{name: "cached fence", code: "fence_not_uncached", edit: func(req *PreflightRequest) {
			req.Fences[0].Uncached = false
		}},
		{name: "missing artifact", code: "required_artifact_missing", edit: func(req *PreflightRequest) {
			req.Approval.Homes[0].RequiredArtifactIDs = []string{"missing-artifact"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := baselinePreflight(t, f)
			tc.edit(&req)
			report, err := Preflight(req)
			if err != nil {
				t.Fatal(err)
			}
			if report.ExecutionAuthority || report.DeleteEnabled || !report.DryRun || !hasReason(report, tc.code) {
				t.Fatalf("missing %s: %+v", tc.code, report)
			}
		})
	}
}

func TestPreflightRefusesActiveHeldSpecialAndIncompleteInventory(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name, code string
		edit       func(*testing.T, *fixture, *PreflightRequest)
	}{
		{name: "active", code: "active_session", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].State = "active"
			matchObservation(t, f, req)
		}},
		{name: "hold", code: "owner_hold", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Hold = "owner-review"
			matchObservation(t, f, req)
		}},
		{name: "fixture", code: "special_disposition", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Disposition = "fixture"
			matchObservation(t, f, req)
		}},
		{name: "canary", code: "special_disposition", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Disposition = "canary"
			matchObservation(t, f, req)
		}},
		{name: "unknown disposition", code: "disposition_unknown", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Disposition = "custom"
			matchObservation(t, f, req)
		}},
		{name: "nil worktrees", code: "repository_inventory_incomplete", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Repositories[0].Worktrees = nil
			resignFenceProof(t, f, req)
		}},
		{name: "nil refs", code: "repository_inventory_incomplete", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Repositories[0].Refs = nil
			resignFenceProof(t, f, req)
		}},
		{name: "nil stashes", code: "repository_inventory_incomplete", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Repositories[0].Stashes = nil
			resignFenceProof(t, f, req)
		}},
		{name: "nil unpushed", code: "repository_inventory_incomplete", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Repositories[0].UnpushedRefs = nil
			resignFenceProof(t, f, req)
		}},
		{name: "missing head", code: "worktree_head_missing", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Repositories[0].Worktrees[0].HeadRef = "HEAD:/missing"
			resignFenceProof(t, f, req)
		}},
		{name: "observation before capture", code: "current_observation_stale_or_invalid", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			earlier := formatTime(f.now.Add(-3 * time.Minute))
			req.Fences[0].ObservedAt = earlier
			req.Approval.ApprovedAt = earlier
			payload := req.Fences[0].Observation.Payload
			payload.ObservedAt = earlier
			req.Fences[0].Observation.Payload = payload
			req.Fences[0].Observation.Signature = f.sign(t, "observation", payload)
		}},
		{name: "artifact requirement none", code: "artifact_requirement_inconsistent", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			req.Fences[0].Proof.Payload.Inventory.Requirements.Handoff = "none"
			resignFenceProof(t, f, req)
		}},
		{name: "true fetch bool without receipts", code: "publication_receipt_missing_or_invalid", edit: func(t *testing.T, f *fixture, req *PreflightRequest) {
			repo := &req.Fences[0].Proof.Payload.Inventory.Repositories[0]
			repo.FetchSucceeded = true
			for i := range repo.Refs {
				repo.Refs[i].Receipts = []Signed[PublicationPayload]{}
			}
			resignFenceProof(t, f, req)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := newFixture(t)
			req := baselinePreflight(t, base)
			tc.edit(t, &base, &req)
			report, err := Preflight(req)
			if err != nil || report.ExecutionAuthority || report.DeleteEnabled || !report.DryRun || !hasReason(report, tc.code) || len(report.Reasons) == 0 {
				t.Fatalf("missing %s: %+v %v", tc.code, report, err)
			}
		})
	}
	passed := baselinePreflight(t, f)
	passed.Fences[0].Proof.Payload.Inventory.Repositories[0].FetchSucceeded = false
	resignFenceProof(t, &f, &passed)
	report, err := Preflight(passed)
	if err != nil || len(report.Reasons) != 0 || report.ExecutionAuthority || report.DeleteEnabled {
		t.Fatalf("false legacy bool hid receipts: %+v %v", report, err)
	}
}

func matchObservation(t *testing.T, f *fixture, req *PreflightRequest) {
	t.Helper()
	payload := req.Fences[0].Observation.Payload
	payload.State = req.Fences[0].State
	payload.Hold = req.Fences[0].Hold
	payload.Disposition = req.Fences[0].Disposition
	req.Fences[0].Observation.Payload = payload
	req.Fences[0].Observation.Signature = f.sign(t, "observation", payload)
}

func resignFenceProof(t *testing.T, f *fixture, req *PreflightRequest) {
	t.Helper()
	req.Fences[0].Proof.Signature = f.sign(t, "proof", req.Fences[0].Proof.Payload)
}

func TestPreflightAndCollectorDoNotImportKubernetes(t *testing.T) {
	for _, name := range []string{"collect.go", "decision.go", "preflight.go"} {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, forbidden := range []string{"k8s.io", "client-go", "time.NewTicker", "go func"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains %s", name, forbidden)
			}
		}
	}
}

func baselinePreflight(t *testing.T, f fixture) PreflightRequest {
	t.Helper()
	home, fence := approvedFence(f)
	return PreflightRequest{
		Now:           f.now,
		Policy:        DefaultPolicy(),
		Approval:      CleanupApproval{BatchID: "batch-synthetic", ApprovalID: "approval-" + f.record.UID, ApprovedAt: fence.ObservedAt, UIDs: []string{f.record.UID}, Homes: []ApprovedHome{home}},
		RequestedUIDs: []string{f.record.UID},
		Fences:        []LiveFence{fence},
		Verifier:      f.trust,
	}
}

func approvedFence(f fixture) (ApprovedHome, LiveFence) {
	ids := []string{}
	for _, artifact := range f.record.Proof.Payload.Inventory.Artifacts {
		ids = append(ids, artifact.ID)
	}
	home := ApprovedHome{
		UID: f.record.UID, Binding: f.record.Binding, ProofID: f.record.Proof.Payload.ID,
		SessionResourceVersion: "session-rv-1", PVCResourceVersion: "pvc-rv-1", PodResourceVersion: "pod-rv-1",
		WriterRevision: "writer-" + f.record.Binding.HomeRevision, OwnerDisposition: OwnerDispositionApproveExactCleanup, RequiredArtifactIDs: ids,
	}
	fence := LiveFence{
		UID: home.UID, Binding: home.Binding, SessionResourceVersion: home.SessionResourceVersion, PVCResourceVersion: home.PVCResourceVersion, PodResourceVersion: home.PodResourceVersion,
		WriterRevision: home.WriterRevision, MountsComplete: true, ActiveMountPodUIDs: []string{}, ObservedAt: f.record.Observation.Payload.ObservedAt, Uncached: true,
		Proof: f.record.Proof, Observation: f.record.Observation, LatestProofID: f.record.LatestProofID, OwnerDisposition: OwnerDispositionApproveExactCleanup,
		State: f.record.State, Hold: f.record.Hold, Disposition: f.record.Disposition,
	}
	return home, fence
}

func rebind(t *testing.T, uid, session, pod string) fixture {
	t.Helper()
	f := newFixture(t)
	binding := f.record.Binding
	binding.SessionUID, binding.PVCUID, binding.PodUID = session, uid, pod
	f.record.UID = uid
	f.record.Binding = binding
	f.record.TaskCompletion.Executor = binding.ExecutorBinding
	f.record.LatestProofID = "proof-" + uid
	f.record.Proof.Payload.ID = f.record.LatestProofID
	f.record.Proof.Payload.Binding = binding
	for i := range f.record.Proof.Payload.Inventory.Repositories {
		for j := range f.record.Proof.Payload.Inventory.Repositories[i].Refs {
			for k := range f.record.Proof.Payload.Inventory.Repositories[i].Refs[j].Receipts {
				receipt := &f.record.Proof.Payload.Inventory.Repositories[i].Refs[j].Receipts[k]
				receipt.Payload.Binding = binding
				receipt.Signature = f.sign(t, "publication", receipt.Payload)
			}
		}
	}
	for i := range f.record.Proof.Payload.Inventory.Artifacts {
		for j := range f.record.Proof.Payload.Inventory.Artifacts[i].Receipts {
			receipt := &f.record.Proof.Payload.Inventory.Artifacts[i].Receipts[j]
			receipt.Payload.Binding = binding
			receipt.Signature = f.sign(t, "durable-copy", receipt.Payload)
		}
	}
	f.record.Observation.Payload.Binding = binding
	f.record.Observation.Payload.LatestProofID = f.record.LatestProofID
	f.resign(t)
	return f
}

func hasReason(report PreflightReport, code string) bool {
	for _, reason := range report.Reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}
