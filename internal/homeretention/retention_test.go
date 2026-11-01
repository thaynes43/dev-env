package homeretention

// All evidence below is synthetic. Deterministic fixture keys are not production
// keys; these tests inspect no HOME, git repository, cluster, network, or process.
import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	record Record
	now    time.Time
	trust  *TrustStore
	config TrustConfig
	keys   map[string]ed25519.PrivateKey
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), keys: map[string]ed25519.PrivateKey{}}
	f.config = TrustConfig{Version: Version, Keys: []TrustedKey{}}
	for index, purpose := range []string{"collector", "publication", "durable-copy"} {
		key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 1)}, ed25519.SeedSize))
		f.keys[purpose] = key
		config := TrustedKey{ID: purpose, Purpose: purpose, PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}
		if purpose == "publication" {
			config.Repository, config.Remote = "github:example/project", "github:example/project"
		}
		if purpose == "durable-copy" {
			config.Destination = "retained:results"
		}
		f.config.Keys = append(f.config.Keys, config)
	}
	var err error
	f.trust, err = NewTrustStore(f.config)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{ExecutorBinding: ExecutorBinding{SessionUID: "session-a", PVCUID: "pvc-a", PodUID: "pod-a", Attempt: "attempt-1"}, WriterGeneration: 1, HomeRevision: "revision-7"}
	clock := Clock{Source: "task_finished_at", At: formatTime(f.now.Add(-2 * time.Hour))}
	f.record = Record{UID: binding.PVCUID, Binding: binding, State: "stopped", Disposition: "normal", TaskCompletion: &Completion{Executor: binding.ExecutorBinding, FinishedAt: clock.At}, LatestProofID: "proof-7"}
	repo := Repository{Identity: "github:example/project", WorktreesComplete: true, RefsComplete: true, StashesComplete: true, FetchSucceeded: true, Stashes: []string{}, UnpushedRefs: []string{}, Worktrees: []Worktree{{Path: "/fixture/task", HeadRef: "HEAD:/fixture/task", SubmodulesComplete: true, SubmodulesClean: true}}, Refs: []Ref{}}
	commit := strings.Repeat("a", 40)
	for _, ref := range []Ref{{Name: "refs/heads/work", Kind: "branch", Commit: commit, Object: commit}, {Name: "HEAD:/fixture/task", Kind: "head", Commit: commit, Object: commit}, {Name: "refs/tags/v1", Kind: "tag", Commit: commit, Object: strings.Repeat("b", 40)}} {
		p := PublicationPayload{Version: Version, Binding: binding, Repository: repo.Identity, LocalRef: ref.Name, Commit: ref.Commit, Object: ref.Object, Remote: repo.Identity, RemoteRef: "refs/heads/preserved", RemoteCommit: ref.Commit, RemoteObject: ref.Object, Method: "authenticated_remote_query", VerifiedAt: formatTime(f.now.Add(-3 * time.Minute))}
		if ref.Kind == "tag" {
			p.RemoteRef = "refs/tags/v1"
		}
		ref.Receipts = []Signed[PublicationPayload]{{Payload: p, Signature: f.sign(t, "publication", p)}}
		repo.Refs = append(repo.Refs, ref)
	}
	inventory := Inventory{Complete: true, Repositories: []Repository{repo}, Requirements: Requirements{Source: "owner-decision:synthetic", Revision: "fixture-v1", Handoff: "required", Result: "required", History: "none", Recovery: "none"}, Artifacts: []Artifact{}}
	for _, kind := range []string{"handoff", "result"} {
		inventory.Artifacts = append(inventory.Artifacts, f.artifact(t, kind))
	}
	f.record.Proof = &Signed[ProofPayload]{Payload: ProofPayload{Version: Version, ID: "proof-7", Binding: binding, CapturedAt: formatTime(f.now.Add(-2 * time.Minute)), Clock: clock, Inventory: inventory}}
	f.record.Observation = &Signed[ObservationPayload]{Payload: ObservationPayload{Version: Version, Binding: binding, ObservedAt: formatTime(f.now.Add(-time.Minute)), Terminal: true, MountsComplete: true, ActiveMountPodUIDs: []string{}, LatestProofID: "proof-7"}}
	f.resign(t)
	return f
}

func (f fixture) sign(t *testing.T, domain string, payload any) Signature {
	t.Helper()
	message, err := SigningBytes(domain, payload)
	if err != nil {
		t.Fatal(err)
	}
	purpose := domain
	if domain == "proof" || domain == "observation" {
		purpose = "collector"
	}
	return Signature{KeyID: purpose, Value: base64.StdEncoding.EncodeToString(ed25519.Sign(f.keys[purpose], message))}
}

func (f *fixture) resign(t *testing.T) {
	t.Helper()
	if f.record.Proof != nil {
		f.record.Proof.Signature = f.sign(t, "proof", f.record.Proof.Payload)
	}
	if f.record.Observation != nil {
		f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
	}
}

func (f fixture) artifact(t *testing.T, kind string) Artifact {
	t.Helper()
	a := Artifact{ID: kind + "-document", Kind: kind, SHA256: strings.Repeat("c", 64)}
	p := DurableCopyPayload{Version: Version, Binding: f.record.Binding, ArtifactID: a.ID, Kind: kind, SHA256: a.SHA256, Destination: "retained:results", Method: "durable_copy_verified", VerifiedAt: formatTime(f.now.Add(-3 * time.Minute))}
	a.Receipts = []Signed[DurableCopyPayload]{{Payload: p, Signature: f.sign(t, "durable-copy", p)}}
	return a
}

func (f fixture) evaluate(t *testing.T) Report {
	t.Helper()
	report, err := Evaluate(Snapshot{Version: Version, Records: []Record{f.record}}, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func requireReason(t *testing.T, report Report, reason string) Entry {
	t.Helper()
	if len(report.ImmediateCandidates) != 0 || len(report.StoppedWorkQueue) != 1 {
		t.Fatalf("expected one refused candidate: %+v", report)
	}
	entry := report.StoppedWorkQueue[0]
	for _, found := range entry.Reasons {
		if found == reason {
			return entry
		}
	}
	t.Fatalf("reason %q absent from %v", reason, entry.Reasons)
	return Entry{}
}

func TestImmediatePreservationAndExplicitEmptyInventory(t *testing.T) {
	for _, empty := range []bool{false, true} {
		f := newFixture(t)
		if empty {
			f.record.Proof.Payload.Inventory = Inventory{Complete: true, Repositories: []Repository{}, Requirements: Requirements{Source: "owner-decision:synthetic", Revision: "fixture-v1", Handoff: "none", Result: "none", History: "none", Recovery: "none"}, Artifacts: []Artifact{}}
			f.resign(t)
		}
		report := f.evaluate(t)
		if len(report.ImmediateCandidates) != 1 || len(report.StoppedWorkQueue) != 0 || !report.DryRun || report.DeleteEnabled {
			t.Fatalf("unexpected report: %+v", report)
		}
		if entry := report.ImmediateCandidates[0]; entry.UID != "pvc-a" || entry.Binding != f.record.Binding || entry.Status != "immediate_preserved" {
			t.Fatalf("incorrect UID manifest: %+v", entry)
		}
	}
}

func TestSignedInventoryRefusals(t *testing.T) {
	cases := []struct {
		name, reason string
		mutate       func(*fixture)
	}{
		{"staged", "staged_work_present", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].Staged = true }},
		{"unstaged", "unstaged_work_present", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].Unstaged = true }},
		{"untracked", "untracked_work_present", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].Untracked = true }},
		{"unpushed", "unpushed_work_present", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].UnpushedRefs = []string{"refs/heads/local"}
		}},
		{"stash", "stash_present", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].Stashes = []string{"stash@{0}"} }},
		{"operation", "git_operation_in_progress", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].OperationInProgress = true
		}},
		{"nested", "untracked_nested_repository", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].NestedRepository = true
		}},
		{"submodule", "submodule_checks_incomplete_or_dirty", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].SubmodulesClean = false
		}},
		{"submodule-incomplete", "submodule_checks_incomplete_or_dirty", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].SubmodulesComplete = false
		}},
		{"fetch", "repository_fetch_unverified", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].FetchSucceeded = false }},
		{"missing-repositories", "home_inventory_incomplete", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories = nil }},
		{"missing-artifacts", "home_inventory_incomplete", func(f *fixture) { f.record.Proof.Payload.Inventory.Artifacts = nil }},
		{"incomplete-refs", "repository_inventory_incomplete", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].RefsComplete = false }},
		{"missing-head", "worktree_head_missing", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Worktrees[0].HeadRef = "HEAD:/missing"
		}},
		{"duplicate-repository", "repository_identity_invalid_or_duplicate", func(f *fixture) {
			p := &f.record.Proof.Payload.Inventory
			p.Repositories = append(p.Repositories, p.Repositories[0])
		}},
		{"duplicate-ref", "local_ref_invalid_or_duplicate", func(f *fixture) {
			p := &f.record.Proof.Payload.Inventory.Repositories[0]
			p.Refs = append(p.Refs, p.Refs[0])
		}},
		{"duplicate-worktree", "worktree_invalid_or_duplicate", func(f *fixture) {
			p := &f.record.Proof.Payload.Inventory.Repositories[0]
			p.Worktrees = append(p.Worktrees, p.Worktrees[0])
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(&f)
			f.resign(t)
			requireReason(t, f.evaluate(t), tc.reason)
		})
	}
}

func TestPublicationAndArtifactReceipts(t *testing.T) {
	cases := []struct {
		name, reason string
		mutate       func(*fixture)
	}{
		{"missing-publication", "publication_receipt_missing_or_invalid", func(f *fixture) { f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts = nil }},
		{"untrusted-publication", "publication_receipt_missing_or_invalid", func(f *fixture) {
			f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0].Signature.KeyID = "snapshot-invented-key"
		}},
		{"foreign-remote", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.Remote = "github:foreign/project"
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"foreign-repository", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.Repository = "github:foreign/project"
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"tracking-ref-only", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.Method = "remote_tracking_ref"
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"wrong-commit", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.RemoteCommit = strings.Repeat("d", 40)
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"annotated-tag-object", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[2].Receipts[0]
			r.Payload.RemoteObject = r.Payload.Commit
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"foreign-home-receipt", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.Binding.PVCUID = "foreign-pvc"
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"future-publication", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.VerifiedAt = formatTime(f.now)
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"stale-publication", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			r.Payload.VerifiedAt = formatTime(f.now.Add(-6 * time.Minute))
			r.Signature = f.sign(t, "publication", r.Payload)
		}},
		{"collector-as-publisher", "publication_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Repositories[0].Refs[0].Receipts[0]
			b, _ := SigningBytes("publication", r.Payload)
			r.Signature = Signature{KeyID: "collector", Value: base64.StdEncoding.EncodeToString(ed25519.Sign(f.keys["collector"], b))}
		}},
		{"unknown-history", "requirement_history_unknown", func(f *fixture) { f.record.Proof.Payload.Inventory.Requirements.History = "unknown" }},
		{"unknown-requirement-authority", "requirements_authority_unknown", func(f *fixture) { f.record.Proof.Payload.Inventory.Requirements.Source = "" }},
		{"unknown-recovery", "requirement_recovery_unknown", func(f *fixture) { f.record.Proof.Payload.Inventory.Requirements.Recovery = "" }},
		{"required-history", "required_history_missing", func(f *fixture) { f.record.Proof.Payload.Inventory.Requirements.History = "required" }},
		{"required-recovery", "required_recovery_missing", func(f *fixture) { f.record.Proof.Payload.Inventory.Requirements.Recovery = "required" }},
		{"missing-durable-copy", "durable_copy_receipt_missing_or_invalid", func(f *fixture) { f.record.Proof.Payload.Inventory.Artifacts[0].Receipts = nil }},
		{"foreign-destination", "durable_copy_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Artifacts[0].Receipts[0]
			r.Payload.Destination = "foreign:results"
			r.Signature = f.sign(t, "durable-copy", r.Payload)
		}},
		{"stale-durable-copy", "durable_copy_receipt_missing_or_invalid", func(f *fixture) {
			r := &f.record.Proof.Payload.Inventory.Artifacts[0].Receipts[0]
			r.Payload.VerifiedAt = formatTime(f.now.Add(-6 * time.Minute))
			r.Signature = f.sign(t, "durable-copy", r.Payload)
		}},
		{"wrong-content", "durable_copy_receipt_missing_or_invalid", func(f *fixture) { f.record.Proof.Payload.Inventory.Artifacts[0].SHA256 = strings.Repeat("e", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.mutate(&f)
			f.resign(t)
			requireReason(t, f.evaluate(t), tc.reason)
		})
	}
	f := newFixture(t)
	f.record.Proof.Payload.Inventory.Requirements.History = "required"
	f.record.Proof.Payload.Inventory.Requirements.Recovery = "required"
	f.record.Proof.Payload.Inventory.Artifacts = append(f.record.Proof.Payload.Inventory.Artifacts, f.artifact(t, "history"), f.artifact(t, "recovery"))
	f.resign(t)
	if len(f.evaluate(t).ImmediateCandidates) != 1 {
		t.Fatal("required artifacts with durable receipts refused")
	}
}

func TestFreshnessIdentityAndProtection(t *testing.T) {
	cases := []struct {
		name, reason string
		mutate       func(*fixture)
	}{
		{"no-trust", "proof_signature_untrusted", func(f *fixture) { f.trust = nil }},
		{"unsigned-proof", "proof_signature_untrusted", func(f *fixture) { f.record.Proof.Signature.Value = "" }},
		{"unsigned-observation", "observation_signature_untrusted", func(f *fixture) { f.record.Observation.Signature.Value = "" }},
		{"missing-observation", "current_observation_missing", func(f *fixture) { f.record.Observation = nil }},
		{"late-revision", "proof_binding_mismatch", func(f *fixture) { f.record.Binding.HomeRevision = "late-write" }},
		{"late-generation", "proof_binding_mismatch", func(f *fixture) { f.record.Binding.WriterGeneration++ }},
		{"new-pod", "current_identity_or_revision_changed", func(f *fixture) {
			f.record.Observation.Payload.Binding.PodUID = "replacement-pod"
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"unknown-pod", "identity_unknown_or_mismatched", func(f *fixture) { f.record.Binding.PodUID = "" }},
		{"new-pvc", "current_identity_or_revision_changed", func(f *fixture) {
			f.record.Observation.Payload.Binding.PVCUID = "replacement-pvc"
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"unknown-pvc", "identity_unknown_or_mismatched", func(f *fixture) { f.record.Binding.PVCUID = "" }},
		{"alias-uid", "identity_unknown_or_mismatched", func(f *fixture) { f.record.UID = "display-name" }},
		{"active-mount", "active_mount_present", func(f *fixture) {
			f.record.Observation.Payload.ActiveMountPodUIDs = []string{"new-pod"}
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"unknown-mounts", "mount_enumeration_incomplete", func(f *fixture) {
			f.record.Observation.Payload.MountsComplete = false
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"missing-mount-array", "mount_enumeration_incomplete", func(f *fixture) {
			f.record.Observation.Payload.ActiveMountPodUIDs = nil
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"active-executor", "executor_terminal_unverified", func(f *fixture) {
			f.record.Observation.Payload.Terminal = false
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"stale-observation", "current_observation_stale_or_invalid", func(f *fixture) {
			f.record.Observation.Payload.ObservedAt = formatTime(f.now.Add(-6 * time.Minute))
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"future-observation", "current_observation_stale_or_invalid", func(f *fixture) {
			f.record.Observation.Payload.ObservedAt = formatTime(f.now.Add(time.Second))
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"observation-before-proof", "current_observation_stale_or_invalid", func(f *fixture) {
			f.record.Observation.Payload.ObservedAt = formatTime(f.now.Add(-3 * time.Minute))
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"superseded", "proof_superseded_or_unknown", func(f *fixture) {
			f.record.Observation.Payload.LatestProofID = "proof-8"
			f.record.Observation.Signature = f.sign(t, "observation", f.record.Observation.Payload)
		}},
		{"record-superseded", "proof_superseded_or_unknown", func(f *fixture) { f.record.LatestProofID = "proof-8" }},
		{"proof-before-finish", "proof_capture_time_invalid", func(f *fixture) {
			f.record.Proof.Payload.CapturedAt = formatTime(f.now.Add(-3 * time.Hour))
			f.record.Proof.Signature = f.sign(t, "proof", f.record.Proof.Payload)
		}},
		{"stale-proof", "proof_capture_time_invalid", func(f *fixture) {
			f.record.Proof.Payload.CapturedAt = formatTime(f.now.Add(-6 * time.Minute))
			f.record.Proof.Signature = f.sign(t, "proof", f.record.Proof.Payload)
		}},
		{"wrong-clock", "proof_completion_clock_mismatch", func(f *fixture) {
			f.record.Proof.Payload.Clock.At = formatTime(f.now.Add(-time.Hour))
			f.record.Proof.Signature = f.sign(t, "proof", f.record.Proof.Payload)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { f := newFixture(t); tc.mutate(&f); requireReason(t, f.evaluate(t), tc.reason) })
	}
	for _, protection := range []string{"active", "hold", "fixture"} {
		t.Run(protection, func(t *testing.T) {
			f := newFixture(t)
			reason := "active_session"
			switch protection {
			case "active":
				f.record.State = "active"
			case "hold":
				f.record.Hold = "owner-review"
				reason = "owner_hold"
			case "fixture":
				f.record.Disposition = "fixture"
				reason = "special_disposition"
			}
			e := requireReason(t, f.evaluate(t), reason)
			if e.Status != "protected" || e.DecisionRequired {
				t.Fatalf("protection lost: %+v", e)
			}
		})
	}
}

func TestRetentionClocksAndExactBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		status string
	}{{"before-warning", 144*time.Hour - time.Nanosecond, "retained"}, {"warning-equality", 144 * time.Hour, "warning"}, {"before-expiry", 168*time.Hour - time.Nanosecond, "warning"}, {"expiry-equality", 168 * time.Hour, "decision_required"}, {"expired", 200 * time.Hour, "decision_required"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.record.Proof = nil
			f.record.Observation = nil
			f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-tc.age))
			e := requireReason(t, f.evaluate(t), "proof_missing")
			if e.Status != tc.status || e.DecisionRequired != (tc.status == "decision_required") {
				t.Fatalf("incorrect deadline state: %+v", e)
			}
		})
	}
	for _, tc := range []struct {
		name, reason string
		mutate       func(*fixture)
	}{
		{"missing", "completion_clock_missing", func(f *fixture) { f.record.TaskCompletion = nil }},
		{"invalid-task-no-fallback", "completion_clock_invalid", func(f *fixture) {
			f.record.TaskCompletion.FinishedAt = "invalid"
			f.record.TerminalRecord = &TerminalRecord{Executor: f.record.Binding.ExecutorBinding, Source: "actual_executor_terminal", ObservedAt: formatTime(f.now.Add(-200 * time.Hour))}
		}},
		{"future", "completion_clock_future", func(f *fixture) { f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(time.Second)) }},
		{"wrong-attempt", "task_clock_binding_invalid", func(f *fixture) { f.record.TaskCompletion.Executor.Attempt = "old-attempt" }},
		{"absence-not-terminal", "terminal_clock_binding_or_source_invalid", func(f *fixture) {
			f.record.TaskCompletion = nil
			f.record.TerminalRecord = &TerminalRecord{Executor: f.record.Binding.ExecutorBinding, Source: "pod_absent", ObservedAt: formatTime(f.now.Add(-200 * time.Hour))}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.record.Proof = nil
			tc.mutate(&f)
			e := requireReason(t, f.evaluate(t), tc.reason)
			if !e.DecisionRequired || e.Deadline != "" {
				t.Fatalf("invented deadline: %+v", e)
			}
		})
	}
	f := newFixture(t)
	f.record.TaskCompletion = nil
	f.record.TerminalRecord = &TerminalRecord{Executor: f.record.Binding.ExecutorBinding, Source: "actual_executor_terminal", ObservedAt: formatTime(f.now.Add(-200 * time.Hour))}
	f.record.Proof = nil
	e := requireReason(t, f.evaluate(t), "proof_missing")
	if e.ClockSource != "executor_terminal_observed_at" || !e.DecisionRequired {
		t.Fatalf("fallback lost: %+v", e)
	}
	f = newFixture(t)
	f.record.Binding.HomeRevision = "late-write"
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-200 * time.Hour))
	e = requireReason(t, f.evaluate(t), "proof_binding_mismatch")
	if !e.DecisionRequired || e.Deadline == "" {
		t.Fatalf("late write reset old clock: %+v", e)
	}
	f = newFixture(t)
	f.record = Record{UID: "legacy-pvc"}
	e = requireReason(t, f.evaluate(t), "completion_clock_missing")
	if !e.DecisionRequired || e.UID != "legacy-pvc" {
		t.Fatal("legacy record hidden")
	}
}

func TestDeterministicUIDManifestAndCustomWarning(t *testing.T) {
	f := newFixture(t)
	a, b := Record{UID: "pvc-z"}, Record{UID: "pvc-b"}
	snapshot := Snapshot{Version: Version, Records: []Record{a, f.record, b}}
	first, err := Evaluate(snapshot, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Records = []Record{b, a, f.record}
	second, err := Evaluate(snapshot, f.now, DefaultPolicy(), f.trust)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.StoppedWorkQueue[0].UID != "pvc-b" {
		t.Fatal("manifest depends on input order")
	}
	f.record.Proof = nil
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-160 * time.Hour))
	policy := DefaultPolicy()
	policy.WarningLead = 4 * time.Hour
	report, err := Evaluate(Snapshot{Version: Version, Records: []Record{f.record}}, f.now, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoppedWorkQueue[0].Status != "retained" {
		t.Fatal("custom warning lead ignored")
	}
}

func TestUnknownDispositionRemainsInDecisionBatch(t *testing.T) {
	f := newFixture(t)
	f.record.Disposition = "unrecognized"
	entry := requireReason(t, f.evaluate(t), "disposition_unknown")
	if entry.Status != "retained" {
		t.Fatalf("unknown disposition silently protected: %+v", entry)
	}
	f.record.TaskCompletion.FinishedAt = formatTime(f.now.Add(-Retention))
	entry = requireReason(t, f.evaluate(t), "disposition_unknown")
	if entry.Status != "decision_required" || !entry.DecisionRequired {
		t.Fatalf("expired unknown disposition hidden: %+v", entry)
	}
}

func TestStrictSnapshotAndTrustBounds(t *testing.T) {
	for _, input := range []string{`{"version":"wrong","records":[]}`, `{"version":"home-retention.v1","records":null}`, `{"version":"home-retention.v1","records":[],"cleanAndPushed":true}`, `{"version":"home-retention.v1","records":[],"records":[]}`, `{"version":"home-retention.v1","records":[{"uid":"pvc","extra":true}]}`, `{"version":"home-retention.v1","records":[{"uid":"pvc"},{"uid":"pvc"}]}`, `{"version":"home-retention.v1","records":[]} {}`, strings.Repeat(" ", MaxSnapshotBytes+1), strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		if _, err := DecodeSnapshot(strings.NewReader(input)); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
	}
	snapshot := Snapshot{Version: Version, Records: make([]Record, MaxRecords+1)}
	data, _ := json.Marshal(snapshot)
	if _, err := DecodeSnapshot(bytes.NewReader(data)); err == nil {
		t.Fatal("record bound ignored")
	}
	f := newFixture(t)
	data, _ = json.Marshal(Snapshot{Version: Version, Records: []Record{f.record}})
	decoded, err := DecodeSnapshot(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Records[0], f.record) {
		t.Fatal("strict roundtrip changed signed payload")
	}
	for _, mutate := range []func(*TrustConfig){func(c *TrustConfig) { c.Version = "wrong" }, func(c *TrustConfig) { c.Keys = nil }, func(c *TrustConfig) { c.Keys = append(c.Keys, c.Keys[0]) }, func(c *TrustConfig) { c.Keys[1].PublicKey = c.Keys[0].PublicKey }, func(c *TrustConfig) { c.Keys[1].Remote = "" }, func(c *TrustConfig) { c.Keys[2].Destination = "" }, func(c *TrustConfig) { c.Keys[0].Purpose = "snapshot-signer" }} {
		config := f.config
		config.Keys = append([]TrustedKey(nil), f.config.Keys...)
		mutate(&config)
		if _, err := NewTrustStore(config); err == nil {
			t.Fatal("invalid trust configuration accepted")
		}
	}
	if _, err := DecodeTrust(strings.NewReader(strings.Repeat(" ", MaxTrustBytes+1))); err == nil {
		t.Fatal("trust bound ignored")
	}
	if _, err := SigningBytes("publication", f.record.Proof.Payload); err == nil {
		t.Fatal("cross-domain signing allowed")
	}
	for _, policy := range []Policy{{}, {WarningLead: 169 * time.Hour, ObservationMaxAge: time.Minute}, {WarningLead: time.Hour, ObservationMaxAge: 2 * time.Hour}} {
		if _, err := Evaluate(Snapshot{Version: Version, Records: []Record{}}, f.now, policy, nil); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	if _, err := Evaluate(Snapshot{Version: Version, Records: []Record{}}, time.Time{}, DefaultPolicy(), nil); err == nil {
		t.Fatal("implicit now accepted")
	}
}
