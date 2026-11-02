package homeretention

import (
	"slices"
	"sort"
	"time"
)

// ApprovedHome is one UID in a specifically approved batch. Resource versions
// and the writer revision are the approver's exact fence, not a later read.
type ApprovedHome struct {
	UID                    string
	Binding                Binding
	ProofID                string
	SessionResourceVersion string
	PVCResourceVersion     string
	PodResourceVersion     string
	WriterRevision         string
	OwnerDisposition       string
	RequiredArtifactIDs    []string
}

// CleanupApproval is the exact approved set. It does not authorize deletion.
type CleanupApproval struct {
	BatchID    string
	ApprovalID string
	ApprovedAt string
	UIDs       []string
	Homes      []ApprovedHome
}

// LiveFence is a fresh caller-supplied uncached read. This package cannot
// perform that read and refuses a fence that does not say it was uncached.
type LiveFence struct {
	UID                    string
	Binding                Binding
	SessionResourceVersion string
	PVCResourceVersion     string
	PodResourceVersion     string
	WriterRevision         string
	MountsComplete         bool
	ActiveMountPodUIDs     []string
	ObservedAt             string
	Uncached               bool
	Proof                  *Signed[ProofPayload]
	Observation            *Signed[ObservationPayload]
	LatestProofID          string
	OwnerDisposition       string
	State                  string
	Hold                   string
	Disposition            string
}

// PreflightRequest compares one approved batch with fresh fences. Consumed
// approval IDs reject a replay. The verifier is the existing receipt boundary.
type PreflightRequest struct {
	Now                 time.Time
	Policy              Policy
	Approval            CleanupApproval
	RequestedUIDs       []string
	Fences              []LiveFence
	ConsumedApprovalIDs []string
	Verifier            EvidenceVerifier
}

// PreflightReason is one refusal. Execution is never authorized.
type PreflightReason struct {
	UID  string `json:"uid,omitempty"`
	Code string `json:"code"`
}

// PreflightReport returns reasons only. DeleteEnabled and ExecutionAuthority
// stay false. No finalizer is removed and no Kubernetes call is made.
type PreflightReport struct {
	Version            string            `json:"version"`
	DryRun             bool              `json:"dryRun"`
	DeleteEnabled      bool              `json:"deleteEnabled"`
	ExecutionAuthority bool              `json:"executionAuthority"`
	Reasons            []PreflightReason `json:"reasons"`
}

// Preflight validates an exact approved UID set against fresh uncached
// identity, resourceVersion, mount, and writer fences. It also checks proof,
// required artifacts, and explicit disposition. A future timestamp or a
// replayed approval is refused. The approval batch is not expired on the
// observation clock: an older unconsumed exact stamp still matches fresh
// fences, observations, proofs, and receipts. A passing report is still not
// permission to delete.
func Preflight(req PreflightRequest) (PreflightReport, error) {
	report := PreflightReport{Version: Version, DryRun: true, DeleteEnabled: false, ExecutionAuthority: false, Reasons: []PreflightReason{}}
	if req.Now.IsZero() {
		return PreflightReport{}, errPreflight("explicit evaluation time required")
	}
	if req.Policy.WarningLead <= 0 || req.Policy.WarningLead > Retention || req.Policy.ObservationMaxAge <= 0 || req.Policy.ObservationMaxAge > time.Hour {
		return PreflightReport{}, errPreflight("warning lead must be positive and at most 168h")
	}
	var reasons []PreflightReason
	add := func(uid, code string) { reasons = append(reasons, PreflightReason{UID: uid, Code: code}) }
	approval := req.Approval
	if !validText(approval.BatchID) || !validText(approval.ApprovalID) || approval.UIDs == nil || approval.Homes == nil {
		add("", "approval_incomplete")
	}
	approvedAt, approvedOK := parseTime(approval.ApprovedAt)
	if !approvedOK {
		add("", "approval_incomplete")
	} else if approvedAt.After(req.Now) {
		add("", "timestamp_future")
	}
	for _, used := range req.ConsumedApprovalIDs {
		if used == approval.ApprovalID && approval.ApprovalID != "" {
			add("", "approval_replayed")
			break
		}
	}
	if !sameExplicitSet(approval.UIDs, req.RequestedUIDs) || !sameExplicitSet(approval.UIDs, homeUIDs(approval.Homes)) {
		add("", "approved_set_mismatch")
	}
	if duplicated(approval.UIDs) || duplicated(req.RequestedUIDs) || duplicated(homeUIDs(approval.Homes)) {
		add("", "approved_set_invalid")
	}
	fences := map[string]LiveFence{}
	for _, fence := range req.Fences {
		if !validText(fence.UID) {
			add("", "fence_outside_approval")
			continue
		}
		if _, exists := fences[fence.UID]; exists {
			add(fence.UID, "fence_duplicate")
			continue
		}
		if !containsUID(approval.UIDs, fence.UID) {
			add(fence.UID, "fence_outside_approval")
		}
		fences[fence.UID] = fence
	}
	homes := map[string]ApprovedHome{}
	for _, home := range approval.Homes {
		if !validText(home.UID) || !containsUID(approval.UIDs, home.UID) {
			add(home.UID, "approved_set_invalid")
			continue
		}
		if _, exists := homes[home.UID]; exists {
			add(home.UID, "approved_set_invalid")
			continue
		}
		homes[home.UID] = home
	}
	for _, uid := range approval.UIDs {
		if !validText(uid) {
			add("", "approved_set_invalid")
			continue
		}
		home, ok := homes[uid]
		if !ok {
			add(uid, "approved_set_mismatch")
			continue
		}
		fence, ok := fences[uid]
		if !ok {
			add(uid, "fence_missing")
			continue
		}
		reasons = append(reasons, fenceReasons(req, home, fence, approvedAt, approvedOK)...)
	}
	report.Reasons = normalizeReasons(reasons)
	return report, nil
}

func fenceReasons(req PreflightRequest, home ApprovedHome, fence LiveFence, approvedAt time.Time, approvedOK bool) []PreflightReason {
	var reasons []PreflightReason
	add := func(code string) { reasons = append(reasons, PreflightReason{UID: home.UID, Code: code}) }
	if !fence.Uncached {
		add("fence_not_uncached")
	}
	observed, observedOK := parseTime(fence.ObservedAt)
	switch {
	case !observedOK:
		add("fence_stale")
	case observed.After(req.Now):
		add("timestamp_future")
	case req.Now.Sub(observed) > req.Policy.ObservationMaxAge || (approvedOK && observed.Before(approvedAt)):
		add("fence_stale")
	}
	if !validText(home.SessionResourceVersion) || !validText(home.PVCResourceVersion) || !validText(home.PodResourceVersion) || !validText(home.WriterRevision) {
		add("approval_incomplete")
	}
	if fence.Binding.PVCUID != home.Binding.PVCUID || fence.UID != home.UID || home.Binding.PVCUID != home.UID || fence.Binding.PVCUID != fence.UID {
		add("pvc_replaced")
	}
	if fence.Binding.PodUID != home.Binding.PodUID {
		add("pod_replaced")
	}
	if fence.Binding.SessionUID != home.Binding.SessionUID {
		add("session_replaced")
	}
	if fence.SessionResourceVersion != home.SessionResourceVersion || fence.PVCResourceVersion != home.PVCResourceVersion || fence.PodResourceVersion != home.PodResourceVersion {
		add("resource_version_mismatch")
	}
	if fence.Binding.WriterGeneration != home.Binding.WriterGeneration || fence.Binding.HomeRevision != home.Binding.HomeRevision || fence.WriterRevision != home.WriterRevision {
		add("late_write")
	}
	if fence.Binding.Attempt != home.Binding.Attempt || !validBinding(home.Binding) || fence.Binding != home.Binding {
		if fence.Binding.WriterGeneration == home.Binding.WriterGeneration && fence.Binding.HomeRevision == home.Binding.HomeRevision && fence.Binding.PodUID == home.Binding.PodUID && fence.Binding.PVCUID == home.Binding.PVCUID && fence.Binding.SessionUID == home.Binding.SessionUID {
			add("identity_mismatch")
		}
	}
	if !fence.MountsComplete || fence.ActiveMountPodUIDs == nil {
		add("mount_enumeration_incomplete")
	}
	if len(fence.ActiveMountPodUIDs) > 0 {
		add("active_mount_present")
	}
	if home.OwnerDisposition != OwnerDispositionApproveExactCleanup || fence.OwnerDisposition != OwnerDispositionApproveExactCleanup {
		add("disposition_not_approved")
	}
	if home.RequiredArtifactIDs == nil {
		add("approval_incomplete")
	}
	if fence.Proof == nil || fence.Observation == nil || req.Verifier == nil {
		add("proof_untrusted")
		return reasons
	}
	proof := fence.Proof.Payload
	authority := verifier{source: req.Verifier}
	if _, ok := authority.verify("proof", proof, fence.Proof.Signature); !ok || proof.Version != Version {
		add("proof_untrusted")
	}
	if proof.Binding != home.Binding || proof.Binding != fence.Binding {
		add("proof_binding_mismatch")
	}
	captured, capturedOK := parseTime(proof.CapturedAt)
	switch {
	case !capturedOK:
		add("stale_proof")
	case captured.After(req.Now):
		add("timestamp_future")
	case req.Now.Sub(captured) > req.Policy.ObservationMaxAge:
		add("stale_proof")
	}
	if proof.ID != home.ProofID || proof.ID != fence.LatestProofID || !validText(proof.ID) {
		add("stale_proof")
	}
	observation := fence.Observation.Payload
	if _, ok := authority.verify("observation", observation, fence.Observation.Signature); !ok || observation.Version != Version || observation.Binding != fence.Binding || observation.LatestProofID != proof.ID || !observation.Terminal || observation.State != fence.State || observation.Hold != fence.Hold || observation.Disposition != fence.Disposition || !slices.Equal(observation.ActiveMountPodUIDs, fence.ActiveMountPodUIDs) || observation.MountsComplete != fence.MountsComplete {
		add("observation_mismatch")
	}
	observedAt, observedAtOK := parseTime(observation.ObservedAt)
	if !observedAtOK || observedAt.After(req.Now) {
		add("timestamp_future")
	} else if observedAt != observed {
		add("observation_mismatch")
	}
	if observationBeforeCapture(observedAt, captured, observedAtOK, capturedOK) {
		add("current_observation_stale_or_invalid")
	}
	safety, _ := homeSafety(fence.State, fence.Hold, fence.Disposition)
	for _, reason := range safety {
		add(reason)
	}
	if !proof.Inventory.Complete || proof.Inventory.Repositories == nil || proof.Inventory.Artifacts == nil {
		add("inventory_not_preserved")
	}
	for _, reason := range authority.inventoryReasons(home.Binding, proof.Inventory, captured, capturedOK, req.Now, req.Policy.ObservationMaxAge) {
		add(reason)
	}
	if !requiredArtifactIDsPresent(proof.Inventory.Artifacts, home.RequiredArtifactIDs) {
		add("required_artifact_missing")
	}
	return reasons
}

func requiredArtifactIDsPresent(artifacts []Artifact, ids []string) bool {
	if ids == nil {
		return false
	}
	byID := map[string]bool{}
	for _, artifact := range artifacts {
		if artifact.ID != "" {
			byID[artifact.ID] = true
		}
	}
	for _, id := range ids {
		if !byID[id] {
			return false
		}
	}
	return true
}

func homeUIDs(homes []ApprovedHome) []string {
	if homes == nil {
		return nil
	}
	out := make([]string, 0, len(homes))
	for _, home := range homes {
		out = append(out, home.UID)
	}
	return out
}

func sameExplicitSet(left, right []string) bool {
	if left == nil || right == nil || len(left) != len(right) || duplicated(left) || duplicated(right) {
		return false
	}
	a, b := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(a)
	sort.Strings(b)
	return slices.Equal(a, b)
}

func duplicated(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func containsUID(values []string, uid string) bool {
	return slices.Contains(values, uid)
}

func normalizeReasons(reasons []PreflightReason) []PreflightReason {
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].UID == reasons[j].UID {
			return reasons[i].Code < reasons[j].Code
		}
		return reasons[i].UID < reasons[j].UID
	})
	out := make([]PreflightReason, 0, len(reasons))
	for _, reason := range reasons {
		if len(out) == 0 || out[len(out)-1] != reason {
			out = append(out, reason)
		}
	}
	return out
}

type preflightError string

func (e preflightError) Error() string { return string(e) }

func errPreflight(text string) error { return preflightError(text) }
