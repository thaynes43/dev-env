package homeretention

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

type trustedKey struct {
	config TrustedKey
	public ed25519.PublicKey
}

// TrustStore can only be built from separately supplied trusted configuration.
// A nil store trusts nobody and cannot produce immediate candidates.
type TrustStore struct{ keys map[string]trustedKey }

func NewTrustStore(config TrustConfig) (*TrustStore, error) {
	if config.Version != Version || config.Keys == nil || len(config.Keys) > MaxTrustedKeys {
		return nil, errors.New("unsupported or unbounded trust configuration")
	}
	store := &TrustStore{keys: map[string]trustedKey{}}
	purposes := map[string]string{}
	for _, key := range config.Keys {
		public, err := base64.StdEncoding.DecodeString(key.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize || !validText(key.ID) {
			return nil, errors.New("invalid trusted key")
		}
		if _, exists := store.keys[key.ID]; exists {
			return nil, errors.New("duplicate trusted key ID")
		}
		if purpose, exists := purposes[string(public)]; exists && purpose != key.Purpose {
			return nil, errors.New("public key reused across trust purposes")
		}
		switch key.Purpose {
		case "collector":
			if key.Repository != "" || key.Remote != "" || key.Destination != "" {
				return nil, errors.New("collector key has foreign authority scopes")
			}
		case "publication":
			if !validText(key.Repository) || !validText(key.Remote) || key.Destination != "" {
				return nil, errors.New("publication key requires exact repository and remote scope")
			}
		case "durable-copy":
			if !validText(key.Destination) || key.Repository != "" || key.Remote != "" {
				return nil, errors.New("durable-copy key requires exact destination scope")
			}
		default:
			return nil, errors.New("unsupported trusted key purpose")
		}
		purposes[string(public)] = key.Purpose
		store.keys[key.ID] = trustedKey{config: key, public: public}
	}
	return store, nil
}

// SigningBytes is the v1 signed contract: the ASCII domain prefix below followed
// by encoding/json.Marshal of the concrete typed payload (all fields, struct
// declaration order, original array order). No whitespace or map canonicalizer
// is involved. Sign payloads, never an envelope or raw input JSON. Signature
// values and public keys use padded standard base64. Domains are separated even
// when proof and observation share a collector key.
func SigningBytes(domain string, payload any) ([]byte, error) {
	valid := false
	switch domain {
	case "proof":
		_, valid = payload.(ProofPayload)
	case "observation":
		_, valid = payload.(ObservationPayload)
	case "publication":
		_, valid = payload.(PublicationPayload)
	case "durable-copy":
		_, valid = payload.(DurableCopyPayload)
	}
	if !valid {
		return nil, errors.New("signing domain does not match concrete payload type")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return append([]byte("dev-env/home-retention/v1/"+domain+"\n"), encoded...), nil
}

func (store *TrustStore) verify(domain string, payload any, signature Signature) (TrustedKey, bool) {
	if store == nil {
		return TrustedKey{}, false
	}
	key, exists := store.keys[signature.KeyID]
	purpose := domain
	if domain == "proof" || domain == "observation" {
		purpose = "collector"
	}
	if !exists || key.config.Purpose != purpose {
		return TrustedKey{}, false
	}
	decoded, err := base64.StdEncoding.DecodeString(signature.Value)
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return TrustedKey{}, false
	}
	message, err := SigningBytes(domain, payload)
	if err != nil || !ed25519.Verify(key.public, message, decoded) {
		return TrustedKey{}, false
	}
	return key.config, true
}

func validText(value string) bool {
	if value == "" || len(value) > 1024 || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validExecutor(binding ExecutorBinding) bool {
	return validText(binding.SessionUID) && validText(binding.PVCUID) && validText(binding.PodUID) && validText(binding.Attempt)
}

func validBinding(binding Binding) bool {
	return validExecutor(binding.ExecutorBinding) && binding.WriterGeneration > 0 && validText(binding.HomeRevision)
}

func parseTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	return t, err == nil && !t.IsZero()
}

func validObject(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value && strings.Trim(value, "0") != ""
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (store *TrustStore) preservation(record Record, clock Clock, completed, now time.Time, maxAge time.Duration) []string {
	reasons := []string{}
	add := func(reason string) { reasons = append(reasons, reason) }
	if !validBinding(record.Binding) || record.Binding.PVCUID != record.UID {
		add("identity_unknown_or_mismatched")
	}
	if record.State != "stopped" {
		add("stopped_state_unknown")
	}
	if record.Proof == nil {
		add("proof_missing")
		return reasons
	}
	p := record.Proof.Payload
	if p.Version != Version || !validText(p.ID) {
		add("proof_version_or_id_invalid")
	}
	if _, ok := store.verify("proof", p, record.Proof.Signature); !ok {
		add("proof_signature_untrusted")
	}
	if p.Binding != record.Binding {
		add("proof_binding_mismatch")
	}
	if p.Clock != clock {
		add("proof_completion_clock_mismatch")
	}
	captured, capturedOK := parseTime(p.CapturedAt)
	if !capturedOK || captured.After(now) || now.Sub(captured) > maxAge || completed.IsZero() || captured.Before(completed) {
		add("proof_capture_time_invalid")
	}
	if record.LatestProofID != p.ID {
		add("proof_superseded_or_unknown")
	}
	if record.Observation == nil {
		add("current_observation_missing")
	} else {
		o := record.Observation.Payload
		if o.Version != Version {
			add("observation_version_invalid")
		}
		if _, ok := store.verify("observation", o, record.Observation.Signature); !ok {
			add("observation_signature_untrusted")
		}
		if o.Binding != record.Binding {
			add("current_identity_or_revision_changed")
		}
		observed, ok := parseTime(o.ObservedAt)
		if !ok || observed.After(now) || now.Sub(observed) > maxAge || !capturedOK || observed.Before(captured) {
			add("current_observation_stale_or_invalid")
		}
		if !o.Terminal {
			add("executor_terminal_unverified")
		}
		if !o.MountsComplete || o.ActiveMountPodUIDs == nil {
			add("mount_enumeration_incomplete")
		}
		if len(o.ActiveMountPodUIDs) != 0 {
			add("active_mount_present")
		}
		if o.LatestProofID != p.ID {
			add("proof_superseded_or_unknown")
		}
	}
	if !p.Inventory.Complete || p.Inventory.Repositories == nil || p.Inventory.Artifacts == nil {
		add("home_inventory_incomplete")
	}
	seenRepos := map[string]bool{}
	for _, repo := range p.Inventory.Repositories {
		if !validText(repo.Identity) || seenRepos[repo.Identity] {
			add("repository_identity_invalid_or_duplicate")
		}
		seenRepos[repo.Identity] = true
		if !repo.WorktreesComplete || !repo.RefsComplete || !repo.StashesComplete || repo.Worktrees == nil || repo.Refs == nil || repo.Stashes == nil || repo.UnpushedRefs == nil {
			add("repository_inventory_incomplete")
		}
		if !repo.FetchSucceeded {
			add("repository_fetch_unverified")
		}
		if len(repo.Stashes) > 0 {
			add("stash_present")
		}
		if len(repo.UnpushedRefs) > 0 {
			add("unpushed_work_present")
		}
		refs := map[string]Ref{}
		for _, ref := range repo.Refs {
			_, duplicate := refs[ref.Name]
			validKind := (ref.Kind == "branch" && strings.HasPrefix(ref.Name, "refs/heads/")) || (ref.Kind == "tag" && strings.HasPrefix(ref.Name, "refs/tags/")) || (ref.Kind == "head" && strings.HasPrefix(ref.Name, "HEAD:"))
			if !validText(ref.Name) || duplicate || !validKind || !validObject(ref.Commit) || !validObject(ref.Object) || (ref.Kind != "tag" && ref.Object != ref.Commit) {
				add("local_ref_invalid_or_duplicate")
			}
			refs[ref.Name] = ref
			if !store.published(record.Binding, repo.Identity, ref, captured, capturedOK, now, maxAge) {
				add("publication_receipt_missing_or_invalid")
			}
		}
		paths := map[string]bool{}
		for _, worktree := range repo.Worktrees {
			if !validText(worktree.Path) || paths[worktree.Path] {
				add("worktree_invalid_or_duplicate")
			}
			paths[worktree.Path] = true
			head, found := refs[worktree.HeadRef]
			if !found || head.Kind != "head" || worktree.HeadRef != "HEAD:"+worktree.Path {
				add("worktree_head_missing")
			}
			if worktree.Staged {
				add("staged_work_present")
			}
			if worktree.Unstaged {
				add("unstaged_work_present")
			}
			if worktree.Untracked {
				add("untracked_work_present")
			}
			if worktree.OperationInProgress {
				add("git_operation_in_progress")
			}
			if worktree.NestedRepository {
				add("untracked_nested_repository")
			}
			if !worktree.SubmodulesComplete || !worktree.SubmodulesClean {
				add("submodule_checks_incomplete_or_dirty")
			}
		}
	}
	requirements := map[string]string{"handoff": p.Inventory.Requirements.Handoff, "result": p.Inventory.Requirements.Result, "history": p.Inventory.Requirements.History, "recovery": p.Inventory.Requirements.Recovery}
	if !validText(p.Inventory.Requirements.Source) || !validText(p.Inventory.Requirements.Revision) {
		add("requirements_authority_unknown")
	}
	counts := map[string]int{}
	seenArtifacts := map[string]bool{}
	for _, artifact := range p.Inventory.Artifacts {
		if !validText(artifact.ID) || seenArtifacts[artifact.ID] || !validDigest(artifact.SHA256) {
			add("artifact_identity_or_digest_invalid")
		}
		seenArtifacts[artifact.ID] = true
		if requirements[artifact.Kind] != "required" {
			add("artifact_requirement_inconsistent")
		}
		counts[artifact.Kind]++
		if !store.copied(record.Binding, artifact, captured, capturedOK, now, maxAge) {
			add("durable_copy_receipt_missing_or_invalid")
		}
	}
	for kind, requirement := range requirements {
		switch requirement {
		case "none":
		case "required":
			if counts[kind] == 0 {
				add("required_" + kind + "_missing")
			}
		default:
			add("requirement_" + kind + "_unknown")
		}
	}
	return reasons
}

func (store *TrustStore) published(binding Binding, repository string, ref Ref, captured time.Time, capturedOK bool, now time.Time, maxAge time.Duration) bool {
	for _, receipt := range ref.Receipts {
		p := receipt.Payload
		key, verified := store.verify("publication", p, receipt.Signature)
		if !verified || p.Version != Version || p.Binding != binding || p.Repository != repository || key.Repository != repository || key.Remote != p.Remote {
			continue
		}
		if p.LocalRef != ref.Name || p.Commit != ref.Commit || p.Object != ref.Object || p.RemoteCommit != ref.Commit || p.RemoteObject != ref.Object || !validText(p.RemoteRef) {
			continue
		}
		if !strings.HasPrefix(p.RemoteRef, "refs/") || (p.Method != "authenticated_push" && p.Method != "authenticated_remote_query") {
			continue
		}
		verifiedAt, ok := parseTime(p.VerifiedAt)
		if ok && capturedOK && !verifiedAt.After(captured) && now.Sub(verifiedAt) <= maxAge {
			return true
		}
	}
	return false
}

func (store *TrustStore) copied(binding Binding, artifact Artifact, captured time.Time, capturedOK bool, now time.Time, maxAge time.Duration) bool {
	for _, receipt := range artifact.Receipts {
		p := receipt.Payload
		key, verified := store.verify("durable-copy", p, receipt.Signature)
		if !verified || p.Version != Version || p.Binding != binding || p.ArtifactID != artifact.ID || p.Kind != artifact.Kind || p.SHA256 != artifact.SHA256 || p.Destination != key.Destination || p.Method != "durable_copy_verified" {
			continue
		}
		verifiedAt, ok := parseTime(p.VerifiedAt)
		if ok && capturedOK && !verifiedAt.After(captured) && now.Sub(verifiedAt) <= maxAge {
			return true
		}
	}
	return false
}
