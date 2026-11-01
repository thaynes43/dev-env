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
	"strings"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
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
		record := Record{UID: claim.uid, Binding: Binding{ExecutorBinding: ExecutorBinding{PVCUID: claim.uid}}, State: "unknown", Disposition: "normal", Provenance: &Provenance{Source: "legacy-snapshot", SnapshotSHA256: hex.EncodeToString(digest[:]), CapturedAt: at, BindingStatus: "unknown", ClockBindingStatus: "unknown", Exceptions: []string{"legacy_evidence_unsigned", "home_revision_unknown", "writer_generation_unknown"}}}
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
		record.Provenance.ClockBindingStatus = "conflict"
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
	// lastExecutor.generation is the Session generation at observation. It is
	// neither the provider attempt nor a writer generation and cannot fill either.
	bound := !conflict && historySession != "" && historyHome != "" && historyClaim != "" && validText(podUID)
	if conflict {
		record.Provenance.BindingStatus = "conflict"
		record.Provenance.ClockBindingStatus = "conflict"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "private_home_identity_conflict")
	} else if !bound {
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_executor_binding_missing")
	} else {
		record.Binding.ExecutorBinding = ExecutorBinding{SessionUID: session.uid, PVCUID: claim.uid, PodUID: podUID}
		record.Provenance.BindingStatus = "matched_legacy_metadata"
	}
	// Retain the alternate terminal observation even if an invalid primary task
	// clock prevents selecting it. This is evidence, not a silent clock fallback.
	if bound && legacyTerminalMatches(history, podUID) {
		terminal, err := legacyMap(history, "executorTerminal")
		if err != nil {
			return err
		}
		observed, err := legacyString(terminal, "observedAt")
		if err != nil {
			return err
		}
		record.Provenance.TerminalObservedAt = observed
		record.Provenance.TerminalClockPath = fmt.Sprintf("$.sessions[%d].privateHistory.executorTerminal.observedAt", session.index)
	}
	agent, err := legacyMap(session.object, "agent")
	if err != nil {
		return err
	}
	activity, err := legacyString(agent, "status")
	if err != nil {
		return err
	}
	phase, err := legacyString(session.object, "phase")
	if err != nil {
		return err
	}
	reportedActive := false
	switch strings.ToLower(activity) {
	case "busy", "waiting", "idle", "pending", "running", "resuming", "starting":
		reportedActive = true
	}
	if phase == "Running" || phase == "Resuming" {
		reportedActive = true
	}
	exited := activity == "exited" || activity == "failed" || activity == "interrupted"
	if reportedActive {
		record.State = "active"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_active_executor_reported")
	}
	retained, err := legacyMap(history, "taskTerminal")
	if err != nil {
		return err
	}
	agentPresent, err := legacyCompletionPresent(agent)
	if err != nil {
		return err
	}
	retainedPresent, err := legacyCompletionPresent(retained)
	if err != nil {
		return err
	}
	if agentPresent || retainedPresent {
		taskParent, taskField := agent, "agent"
		if retainedPresent {
			taskParent, taskField = retained, "privateHistory.taskTerminal"
		}
		task, err := legacyMap(taskParent, "task")
		if err != nil {
			return err
		}
		finished, source, sourceShapeOK, err := legacyTask(task)
		if err != nil {
			return err
		}
		record.Provenance.RecordedClockAt = finished
		record.Provenance.RecordedClockSource = "task_finished_at"
		record.Provenance.ClockPath = fmt.Sprintf("$.sessions[%d].%s.task.finishedAt", session.index, taskField)
		conflictingTasks := false
		if agentPresent && retainedPresent {
			agentTask, err := legacyMap(agent, "task")
			if err != nil {
				return err
			}
			agentFinished, agentSource, agentShapeOK, err := legacyTask(agentTask)
			if err != nil {
				return err
			}
			conflictingTasks = agentFinished != finished || agentSource != source || agentShapeOK != sourceShapeOK
		}
		if conflictingTasks {
			record.Provenance.ClockBindingStatus = "conflict"
			record.Provenance.RecordedClockAt = ""
			record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_task_records_conflict")
			finished = ""
		} else if bound && sourceShapeOK && source.SessionUID == session.uid && source.PodUID == podUID {
			spec, err := legacyMap(session.object, "spec")
			if err != nil {
				return err
			}
			provider, err := legacyString(spec, "agent")
			if err != nil {
				return err
			}
			model, err := legacyString(spec, "model")
			if err != nil {
				return err
			}
			if (provider == "" || provider == source.Provider) && (model == "" || model == source.RequestedModel) {
				record.Binding.Attempt = source.AttemptID
				record.Provenance.ClockBindingStatus = "recorded_attempt"
			} else {
				record.Provenance.ClockBindingStatus = "conflict"
				record.Provenance.Exceptions = append(record.Provenance.Exceptions, "task_exit_source_provider_or_model_conflict")
			}
		} else {
			if raw, present := task["exitSource"]; present && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				record.Provenance.ClockBindingStatus = "conflict"
			} else if record.Provenance.ClockBindingStatus != "conflict" {
				record.Provenance.ClockBindingStatus = "unbound_legacy"
			}
			record.Provenance.Exceptions = append(record.Provenance.Exceptions, "task_exit_source_missing_foreign_or_invalid")
		}
		if legacyPredatesSession(session.object, finished) {
			record.Binding.Attempt = ""
			record.Provenance.ClockBindingStatus = "conflict"
			record.Provenance.Exceptions = append(record.Provenance.Exceptions, "task_clock_predates_session")
		}
		record.TaskCompletion = &Completion{Executor: record.Binding.ExecutorBinding, FinishedAt: finished}
		if record.Binding.Attempt == "" && record.Provenance.BindingStatus != "conflict" {
			record.Provenance.BindingStatus = "task_attempt_unknown"
		}
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "legacy_task_attempt_association_unsigned")
		if bound && !reportedActive && (exited || legacyTerminalMatches(history, podUID)) {
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
	terminalPhase, err := legacyString(terminal, "phase")
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
	if !bound || terminalPod != podUID || version != 1 || (terminalPhase != "Failed" && terminalPhase != "Succeeded") {
		record.Provenance.ClockBindingStatus = "conflict"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "terminal_identity_version_or_phase_unverified")
		return nil
	}
	if legacyPredatesSession(session.object, observed) {
		record.Provenance.ClockBindingStatus = "conflict"
		record.Provenance.Exceptions = append(record.Provenance.Exceptions, "terminal_clock_predates_session")
		return nil
	}
	record.TerminalRecord = &TerminalRecord{Executor: record.Binding.ExecutorBinding, ObservedAt: observed, Source: "actual_executor_terminal"}
	record.Provenance.ClockBindingStatus = "recorded_terminal"
	if !reportedActive {
		record.State = "stopped"
	}
	return nil
}

// A task object is a completion record only when finishedAt is present. An
// explicit invalid/empty timestamp blocks fallback; null/absent task or a task
// object without finishedAt does not hide an actual recorded terminal clock.
func legacyCompletionPresent(parent legacyObject) (bool, error) {
	task, err := legacyMap(parent, "task")
	if err != nil {
		return false, err
	}
	_, present := task["finishedAt"]
	return present, nil
}

func legacyTerminalMatches(history legacyObject, podUID string) bool {
	terminal, err := legacyMap(history, "executorTerminal")
	if err != nil {
		return false
	}
	uid, err := legacyString(terminal, "podUID")
	if err != nil || uid != podUID {
		return false
	}
	phase, err := legacyString(terminal, "phase")
	if err != nil || (phase != "Failed" && phase != "Succeeded") {
		return false
	}
	var version int
	return json.Unmarshal(terminal["version"], &version) == nil && version == 1
}

func legacyPredatesSession(session legacyObject, value string) bool {
	created, err := legacyString(session, "creationTimestamp")
	if err != nil {
		return false
	}
	birth, birthOK := parseTime(created)
	clock, clockOK := parseTime(value)
	return birthOK && clockOK && clock.Before(birth)
}

func legacyTask(task legacyObject) (string, protocol.UsageSource, bool, error) {
	finished, err := legacyString(task, "finishedAt")
	if err != nil {
		return "", protocol.UsageSource{}, false, err
	}
	var source protocol.UsageSource
	raw, exists := task["exitSource"]
	if !exists || decodeStrict(bytes.NewReader(raw), MaxSnapshotBytes, &source) != nil {
		return finished, source, false, nil
	}
	return finished, source, source.Valid(), nil
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
