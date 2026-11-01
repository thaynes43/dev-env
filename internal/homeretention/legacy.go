package homeretention

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

const MaxLegacyPods = 4000

type legacyObject map[string]json.RawMessage

type legacyEntity struct {
	object               legacyObject
	name, namespace, uid string
	index                int
}

// DecodeLegacyDispositions reads a separate UID-to-disposition policy map. It
// accepts no clear/normal/active override and grants no preservation authority.
func DecodeLegacyDispositions(r io.Reader) (map[string]string, error) {
	var dispositions map[string]string
	if err := decodeStrict(r, MaxTrustBytes, &dispositions); err != nil {
		return nil, err
	}
	if dispositions == nil || len(dispositions) > MaxRecords {
		return nil, errors.New("invalid or oversized legacy dispositions")
	}
	for uid, disposition := range dispositions {
		if !validText(uid) || !legacyDisposition(disposition) {
			return nil, errors.New("invalid legacy disposition UID or value")
		}
	}
	return dispositions, nil
}

func legacyDisposition(value string) bool {
	switch value {
	case "fixture", "canary", "pilot", "rescue_hold":
		return true
	}
	return false
}

// DecodeLegacySnapshot adapts a bounded, caller-supplied sanitized inventory.
// It never collects data or turns provider status/rescue flags into proof. The
// legacy metadata is only audit evidence: even a matched binding is unsigned.
// Provider metadata not used below is ignored, while all JSON member duplicates,
// nesting and byte bounds are checked before projection. dispositions is a
// separate exact-PVCUID map and may add a known protection, never remove one.
func DecodeLegacySnapshot(r io.Reader, dispositions map[string]string) (Snapshot, error) {
	if len(dispositions) > MaxRecords {
		return Snapshot{}, errors.New("legacy disposition count exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxSnapshotBytes+1))
	if err != nil {
		return Snapshot{}, fmt.Errorf("read legacy snapshot: %w", err)
	}
	if len(data) > MaxSnapshotBytes {
		return Snapshot{}, errors.New("legacy snapshot exceeds byte limit")
	}
	var root legacyObject
	if err := decodeStrict(bytes.NewReader(data), MaxSnapshotBytes, &root); err != nil {
		return Snapshot{}, err
	}
	at, err := legacyString(root, "at")
	if err != nil {
		return Snapshot{}, err
	}
	if _, ok := parseTime(at); !ok {
		return Snapshot{}, errors.New("legacy snapshot requires recorded capture at")
	}
	claims, err := legacyEntities(root, "claims", MaxRecords)
	if err != nil {
		return Snapshot{}, err
	}
	sessions, err := legacyEntities(root, "sessions", MaxRecords)
	if err != nil {
		return Snapshot{}, err
	}
	pods, err := legacyEntities(root, "pods", MaxLegacyPods)
	if err != nil {
		return Snapshot{}, err
	}
	mounts, err := legacyMounts(pods)
	if err != nil {
		return Snapshot{}, err
	}
	bySessionUID := map[string]legacyEntity{}
	for _, session := range sessions {
		bySessionUID[session.uid] = session
	}
	claimUIDs := map[string]bool{}
	for _, claim := range claims {
		claimUIDs[claim.uid] = true
	}
	for uid, disposition := range dispositions {
		if !claimUIDs[uid] {
			return Snapshot{}, errors.New("external disposition UID is absent from snapshot claims")
		}
		if !legacyDisposition(disposition) {
			return Snapshot{}, errors.New("unknown external legacy disposition")
		}
	}
	digest := sha256.Sum256(data)
	snapshot := Snapshot{Version: Version, Records: make([]Record, 0, len(claims))}
	for _, claim := range claims {
		record := Record{UID: claim.uid, Binding: Binding{ExecutorBinding: ExecutorBinding{PVCUID: claim.uid}}, State: "unknown", Disposition: "normal", Provenance: &Provenance{Source: "legacy-snapshot", SnapshotSHA256: hex.EncodeToString(digest[:]), CapturedAt: at, BindingStatus: "unknown", Exceptions: []string{"legacy_evidence_unsigned", "home_revision_unknown"}}}
		if err := legacyAssociate(&record, claim, bySessionUID); err != nil {
			return Snapshot{}, err
		}
		mount := mounts[claim.namespace+"\x00"+claim.name]
		if mount.active {
			record.State = "active"
			record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_active_mount_observed")
		}
		if mount.rescueHold {
			legacyProtect(&record, "rescue_hold")
		}
		if disposition, exists := dispositions[claim.uid]; exists {
			if record.Disposition != "normal" && record.Disposition != "" && record.Disposition != disposition {
				return Snapshot{}, errors.New("external disposition conflicts with existing special protection")
			}
			legacyProtect(&record, disposition)
		}
		record.Provenance.Exceptions = sortedUnique(record.Provenance.Exceptions)
		snapshot.Records = append(snapshot.Records, record)
	}
	sort.Slice(snapshot.Records, func(i, j int) bool { return snapshot.Records[i].UID < snapshot.Records[j].UID })
	return snapshot, validateSnapshot(snapshot)
}

func legacyAssociate(record *Record, claim legacyEntity, sessions map[string]legacyEntity) error {
	var owners []legacyObject
	if raw, exists := claim.object["ownerReferences"]; exists {
		if err := json.Unmarshal(raw, &owners); err != nil {
			return errors.New("invalid legacy ownerReferences")
		}
	}
	var controller legacyObject
	controllers := 0
	for _, owner := range owners {
		isController, err := legacyBool(owner, "controller")
		if err != nil {
			return err
		}
		if isController {
			controllers++
			controller = owner
		}
	}
	if controllers != 1 {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "claim_controller_missing_or_ambiguous")
		return nil
	}
	kind, err := legacyString(controller, "kind")
	if err != nil {
		return err
	}
	api, err := legacyString(controller, "apiVersion")
	if err != nil {
		return err
	}
	uid, err := legacyString(controller, "uid")
	if err != nil {
		return err
	}
	name, err := legacyString(controller, "name")
	if err != nil {
		return err
	}
	session, exists := sessions[uid]
	if kind != "AgentSession" || api != "dev-env.haynesops.com/v1alpha1" || !exists || session.name != name || session.namespace != claim.namespace {
		record.Provenance.BindingStatus = "conflict"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "claim_session_identity_conflict")
		return nil
	}
	canary, err := legacyBool(session.object, "projectCanary")
	if err != nil {
		return err
	}
	pilot, err := legacyBool(session.object, "privatePilot")
	if err != nil {
		return err
	}
	if canary {
		legacyProtect(record, "canary")
	}
	if pilot {
		legacyProtect(record, "pilot")
	}
	annotations, err := legacyMap(session.object, "holdAnnotations")
	if err != nil {
		return err
	}
	hold, err := legacyString(annotations, "dev-env.haynesops.com/home-expiry")
	if err != nil {
		return err
	}
	if hold == "hold" {
		record.Hold = "legacy_home_expiry_owner_hold"
	}
	history, err := legacyMap(session.object, "privateHistory")
	if err != nil {
		return err
	}
	historySession, err := legacyString(history, "sessionUID")
	if err != nil {
		return err
	}
	historyHome, err := legacyString(history, "homeUID")
	if err != nil {
		return err
	}
	historyClaim, err := legacyString(history, "homeClaim")
	if err != nil {
		return err
	}
	conflict := (historySession != "" && historySession != session.uid) || (historyHome != "" && historyHome != claim.uid) || (historyClaim != "" && historyClaim != claim.name)
	last, err := legacyMap(history, "lastExecutor")
	if err != nil {
		return err
	}
	podUID, err := legacyString(last, "podUID")
	if err != nil {
		return err
	}
	var generation uint64
	if raw, exists := last["generation"]; exists {
		if err := json.Unmarshal(raw, &generation); err != nil {
			return errors.New("invalid legacy executor generation")
		}
	}
	bound := !conflict && historySession != "" && historyHome != "" && historyClaim != "" && validText(podUID) && generation > 0
	if conflict {
		record.Provenance.BindingStatus = "conflict"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "private_home_identity_conflict")
	} else if !bound {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_executor_binding_missing")
	} else {
		record.Binding.ExecutorBinding = ExecutorBinding{SessionUID: session.uid, PVCUID: claim.uid, PodUID: podUID, Attempt: podUID + ":" + strconv.FormatUint(generation, 10)}
		record.Binding.WriterGeneration = generation
		record.Provenance.BindingStatus = "matched_legacy_metadata"
	}
	agent, err := legacyMap(session.object, "agent")
	if err != nil {
		return err
	}
	if _, taskPresent := agent["task"]; taskPresent {
		task, err := legacyMap(agent, "task")
		if err != nil {
			return err
		}
		finished, err := legacyString(task, "finishedAt")
		if err != nil {
			return err
		}
		record.Provenance.RecordedClockAt = finished
		record.Provenance.RecordedClockSource = "task_finished_at"
		record.TaskCompletion = &Completion{Executor: record.Binding.ExecutorBinding, FinishedAt: finished}
		record.Provenance.ClockPath = fmt.Sprintf("$.sessions[%d].agent.task.finishedAt", session.index)
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_task_attempt_association_unsigned")
		if bound {
			record.State = "stopped"
		}
		return nil
	}
	terminal, err := legacyMap(history, "executorTerminal")
	if err != nil {
		return err
	}
	if terminal == nil {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "completion_clock_unrecorded")
		return nil
	}
	record.Provenance.ClockPath = fmt.Sprintf("$.sessions[%d].privateHistory.executorTerminal.observedAt", session.index)
	terminalPod, err := legacyString(terminal, "podUID")
	if err != nil {
		return err
	}
	phase, err := legacyString(terminal, "phase")
	if err != nil {
		return err
	}
	observed, err := legacyString(terminal, "observedAt")
	if err != nil {
		return err
	}
	record.Provenance.RecordedClockAt = observed
	record.Provenance.RecordedClockSource = "executor_terminal_observed_at"
	var version int
	if raw, exists := terminal["version"]; exists {
		if err := json.Unmarshal(raw, &version); err != nil {
			return errors.New("invalid legacy terminal version")
		}
	}
	if !bound || terminalPod != podUID || version != 1 || (phase != "Failed" && phase != "Succeeded") {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "terminal_identity_version_or_phase_unverified")
		return nil
	}
	record.TerminalRecord = &TerminalRecord{Executor: record.Binding.ExecutorBinding, ObservedAt: observed, Source: "actual_executor_terminal"}
	record.State = "stopped"
	return nil
}

func legacyProtect(record *Record, disposition string) {
	if record.Disposition == "normal" || record.Disposition == "" {
		record.Disposition = disposition
		return
	}
	if record.Disposition != disposition {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "multiple_special_dispositions")
		if disposition == "rescue_hold" {
			record.Disposition = disposition
		}
	}
}

type legacyMount struct{ active, rescueHold bool }

func legacyMounts(pods []legacyEntity) (map[string]legacyMount, error) {
	mounts := map[string]legacyMount{}
	for _, pod := range pods {
		phase, err := legacyString(pod.object, "phase")
		if err != nil {
			return nil, err
		}
		labels, err := legacyMap(pod.object, "labels")
		if err != nil {
			return nil, err
		}
		hold, err := legacyString(labels, "dev-env.haynesops.com/hold")
		if err != nil {
			return nil, err
		}
		var names []string
		if raw, present := pod.object["claims"]; present {
			if err := json.Unmarshal(raw, &names); err != nil {
				return nil, errors.New("invalid legacy pod claims")
			}
		}
		if len(names) > MaxRecords {
			return nil, errors.New("legacy pod claim list exceeds limit")
		}
		for _, name := range names {
			if !validText(name) {
				return nil, errors.New("invalid legacy pod claim name")
			}
			key := pod.namespace + "\x00" + name
			mount := mounts[key]
			// Unknown/missing phases cannot establish absence of a writer.
			// A rescue hold mounts the stopped HOME but is not the provider
			// executor. Its named protection must still reach an owner decision.
			mount.active = mount.active || (hold != "true" && phase != "Succeeded" && phase != "Failed")
			mount.rescueHold = mount.rescueHold || hold == "true"
			mounts[key] = mount
		}
	}
	return mounts, nil
}

func legacyEntities(root legacyObject, field string, limit int) ([]legacyEntity, error) {
	raw, exists := root[field]
	if !exists {
		return nil, errors.New("legacy snapshot requires explicit inventory arrays")
	}
	var objects []legacyObject
	if err := json.Unmarshal(raw, &objects); err != nil || objects == nil || len(objects) > limit {
		return nil, errors.New("invalid or oversized legacy inventory array")
	}
	result := make([]legacyEntity, 0, len(objects))
	uids, names := map[string]bool{}, map[string]bool{}
	for index, object := range objects {
		name, err := legacyString(object, "name")
		if err != nil {
			return nil, err
		}
		namespace, err := legacyString(object, "namespace")
		if err != nil {
			return nil, err
		}
		uid, err := legacyString(object, "uid")
		if err != nil {
			return nil, err
		}
		if !validText(name) || !validText(namespace) || !validText(uid) || uids[uid] || names[namespace+"\x00"+name] {
			return nil, errors.New("legacy inventory identity missing or duplicated")
		}
		uids[uid], names[namespace+"\x00"+name] = true, true
		result = append(result, legacyEntity{object: object, name: name, namespace: namespace, uid: uid, index: index})
	}
	return result, nil
}

func legacyString(object legacyObject, field string) (string, error) {
	var value string
	if raw, exists := object[field]; exists {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", errors.New("invalid legacy string field")
		}
	}
	return value, nil
}

func legacyBool(object legacyObject, field string) (bool, error) {
	var value bool
	if raw, exists := object[field]; exists {
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, errors.New("invalid legacy boolean field")
		}
	}
	return value, nil
}

func legacyMap(object legacyObject, field string) (legacyObject, error) {
	var value legacyObject
	if raw, exists := object[field]; exists {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, errors.New("invalid legacy object field")
		}
	}
	return value, nil
}
