package homeretention

// The legacy adapter fixtures below are invented metadata. They read no private
// snapshot, HOME, credentials, cluster, mount, process or network.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func legacyFixture() map[string]any {
	root := map[string]any{
		"at":       "2026-11-01T12:00:00Z",
		"claims":   []any{map[string]any{"name": "home-a", "namespace": "test", "uid": "pvc-a", "creationTimestamp": "2020-01-01T00:00:00Z", "ownerReferences": []any{map[string]any{"apiVersion": "dev-env.haynesops.com/v1alpha1", "kind": "AgentSession", "name": "session-a", "uid": "session-uid-a", "controller": true}}}},
		"sessions": []any{map[string]any{"name": "session-a", "namespace": "test", "uid": "session-uid-a", "phase": "Suspended", "agent": map[string]any{"status": "Succeeded", "task": map[string]any{"finishedAt": "2026-10-24T12:00:00Z", "exitCode": 0}}, "rescue": map[string]any{"cleanAndPushed": true, "unpushedRefCount": 0}, "privateHistory": map[string]any{"sessionUID": "session-uid-a", "homeUID": "pvc-a", "homeClaim": "home-a", "lastExecutor": map[string]any{"podUID": "pod-uid-a", "generation": uint64(7), "revision": "image-template-revision"}, "executorTerminal": map[string]any{"version": 1, "podUID": "pod-uid-a", "phase": "Succeeded", "observedAt": "2026-10-26T12:00:00Z"}}}},
		"pods":     []any{},
	}
	legacyTaskFixture(root)["exitSource"] = map[string]any{"sessionUID": "session-uid-a", "podUID": "pod-uid-a", "provider": "codex", "requestedModel": "gpt-6.1-sol", "attemptID": strings.Repeat("a", 64)}
	legacySession(root)["agent"].(map[string]any)["status"] = "exited"
	legacySession(root)["creationTimestamp"] = "2026-10-01T00:00:00Z"
	return root
}

func legacyTaskFixture(root map[string]any) map[string]any {
	return legacySession(root)["agent"].(map[string]any)["task"].(map[string]any)
}

func legacySession(root map[string]any) map[string]any {
	return root["sessions"].([]any)[0].(map[string]any)
}
func legacyHistory(root map[string]any) map[string]any {
	return legacySession(root)["privateHistory"].(map[string]any)
}
func legacyClaim(root map[string]any) map[string]any {
	return root["claims"].([]any)[0].(map[string]any)
}

func adaptLegacy(t *testing.T, root map[string]any, dispositions map[string]string) (Snapshot, []byte) {
	t.Helper()
	data, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := DecodeLegacySnapshot(bytes.NewReader(data), dispositions)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot, data
}

func evaluateLegacy(t *testing.T, snapshot Snapshot) Report {
	t.Helper()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	report, err := Evaluate(snapshot, now, DefaultPolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ImmediateCandidates) != 0 {
		t.Fatal("legacy evidence promoted preservation")
	}
	return report
}

func TestLegacyPrimaryClockBindingAndProvenance(t *testing.T) {
	root := legacyFixture()
	root["ignoredProviderMetadata"] = map[string]any{"newField": true}
	snapshot, data := adaptLegacy(t, root, nil)
	record := snapshot.Records[0]
	if record.Binding.SessionUID != "session-uid-a" || record.Binding.PVCUID != "pvc-a" || record.Binding.PodUID != "pod-uid-a" || record.Binding.Attempt != strings.Repeat("a", 64) || record.Binding.WriterGeneration != 0 || record.Binding.HomeRevision != "" {
		t.Fatalf("incorrect binding: %+v", record.Binding)
	}
	if record.TaskCompletion == nil || record.TaskCompletion.FinishedAt != "2026-10-24T12:00:00Z" || record.TerminalRecord != nil || record.Proof != nil || record.Observation != nil {
		t.Fatal("legacy completion or proof conversion incorrect")
	}
	digest := sha256.Sum256(data)
	p := record.Provenance
	if p == nil || p.SnapshotSHA256 != hex.EncodeToString(digest[:]) || p.CapturedAt != root["at"] || p.ClockPath != "$.sessions[0].agent.task.finishedAt" || p.BindingStatus != "matched_legacy_metadata" || p.RecordedClockAt != "2026-10-24T12:00:00Z" || p.RecordedClockSource != "task_finished_at" {
		t.Fatalf("incorrect provenance: %+v", p)
	}
	if p.ClockBindingStatus != "recorded_attempt" {
		t.Fatal("explicit provider attempt not retained")
	}
	report := evaluateLegacy(t, snapshot)
	if e := report.StoppedWorkQueue[0]; e.ClockSource != "task_finished_at" || e.Deadline != "2026-10-31T12:00:00Z" || !e.DecisionRequired {
		t.Fatalf("primary clock replaced: %+v", e)
	}
	second, err := DecodeLegacySnapshot(bytes.NewReader(data), nil)
	if err != nil || !reflect.DeepEqual(snapshot, second) {
		t.Fatal("same input produced different snapshot")
	}
}

func TestLegacyActualTerminalFallback(t *testing.T) {
	root := legacyFixture()
	delete(legacySession(root)["agent"].(map[string]any), "task")
	snapshot, _ := adaptLegacy(t, root, nil)
	record := snapshot.Records[0]
	if record.TaskCompletion != nil || record.TerminalRecord == nil || record.TerminalRecord.Source != "actual_executor_terminal" || record.Binding.Attempt != "" || record.Binding.WriterGeneration != 0 || record.Provenance.ClockBindingStatus != "recorded_terminal" {
		t.Fatal("actual terminal record not selected")
	}
	e := evaluateLegacy(t, snapshot).StoppedWorkQueue[0]
	if e.ClockSource != "executor_terminal_observed_at" || e.Deadline != "2026-11-02T12:00:00Z" {
		t.Fatalf("terminal fallback clock incorrect: %+v", e)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong-pod", func(r map[string]any) {
			legacyHistory(r)["executorTerminal"].(map[string]any)["podUID"] = "replacement-pod"
		}},
		{"wrong-version", func(r map[string]any) { legacyHistory(r)["executorTerminal"].(map[string]any)["version"] = 2 }},
		{"running-phase", func(r map[string]any) { legacyHistory(r)["executorTerminal"].(map[string]any)["phase"] = "Running" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := legacyFixture()
			delete(legacySession(r)["agent"].(map[string]any), "task")
			tc.mutate(r)
			s, _ := adaptLegacy(t, r, nil)
			if s.Records[0].TerminalRecord != nil {
				t.Fatal("unverified terminal record accepted")
			}
			e := evaluateLegacy(t, s).StoppedWorkQueue[0]
			if e.Deadline != "" || !e.DecisionRequired {
				t.Fatal("unverified terminal invented deadline")
			}
		})
	}
}

func TestLegacyInvalidAndAbsentClocksNeverFallBackToCreation(t *testing.T) {
	for _, value := range []any{"invalid", "2026-11-02T12:00:00Z", "", nil} {
		t.Run("present-task", func(t *testing.T) {
			r := legacyFixture()
			legacySession(r)["agent"].(map[string]any)["task"].(map[string]any)["finishedAt"] = value
			s, _ := adaptLegacy(t, r, nil)
			if s.Records[0].TerminalRecord != nil || s.Records[0].TaskCompletion == nil {
				t.Fatal("invalid present task fell back")
			}
			if p := s.Records[0].Provenance; p.TerminalObservedAt != "2026-10-26T12:00:00Z" || p.TerminalClockPath == "" {
				t.Fatal("unselected terminal timestamp provenance erased")
			}
			e := evaluateLegacy(t, s).StoppedWorkQueue[0]
			if e.Deadline != "" || !e.DecisionRequired {
				t.Fatal("invalid clock invented deadline")
			}
		})
	}
	r := legacyFixture()
	delete(legacySession(r)["agent"].(map[string]any), "task")
	delete(legacyHistory(r), "executorTerminal")
	s, _ := adaptLegacy(t, r, nil)
	e := evaluateLegacy(t, s).StoppedWorkQueue[0]
	if e.Deadline != "" || e.ClockSource != "unknown" {
		t.Fatal("PVC creation or capture time became a clock")
	}
}

func TestLegacyAssociationConflictsRemainUnknown(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"owner-replacement-uid", func(r map[string]any) {
			legacyClaim(r)["ownerReferences"].([]any)[0].(map[string]any)["uid"] = "replacement-session"
		}},
		{"owner-replacement-name", func(r map[string]any) {
			legacyClaim(r)["ownerReferences"].([]any)[0].(map[string]any)["name"] = "replacement-name"
		}},
		{"namespace-mismatch", func(r map[string]any) { legacySession(r)["namespace"] = "other" }},
		{"non-controller", func(r map[string]any) {
			legacyClaim(r)["ownerReferences"].([]any)[0].(map[string]any)["controller"] = false
		}},
		{"home-replacement", func(r map[string]any) { legacyHistory(r)["homeUID"] = "replacement-pvc" }},
		{"session-binding-conflict", func(r map[string]any) { legacyHistory(r)["sessionUID"] = "replacement-session" }},
		{"home-name-conflict", func(r map[string]any) { legacyHistory(r)["homeClaim"] = "replacement-home" }},
		{"unknown-pod", func(r map[string]any) { delete(legacyHistory(r)["lastExecutor"].(map[string]any), "podUID") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := legacyFixture()
			tc.mutate(r)
			s, _ := adaptLegacy(t, r, nil)
			record := s.Records[0]
			if validExecutor(record.Binding.ExecutorBinding) || record.Provenance.BindingStatus == "matched_legacy_metadata" {
				t.Fatal("conflict guessed executor binding")
			}
			e := evaluateLegacy(t, s).StoppedWorkQueue[0]
			if e.Deadline != "" || !e.DecisionRequired {
				t.Fatal("conflict invented deadline")
			}
		})
	}
	r := legacyFixture()
	delete(legacySession(r), "privateHistory")
	s, _ := adaptLegacy(t, r, nil)
	p := s.Records[0].Provenance
	if p.RecordedClockAt != "2026-10-24T12:00:00Z" || p.ClockPath == "" || s.Records[0].TaskCompletion == nil {
		t.Fatal("unbound raw clock provenance erased")
	}
}

func TestLegacyExitSourceMustBeExplicitAndExact(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"foreign-session", func(r map[string]any) {
			legacyTaskFixture(r)["exitSource"].(map[string]any)["sessionUID"] = "foreign-session"
		}},
		{"foreign-pod", func(r map[string]any) { legacyTaskFixture(r)["exitSource"].(map[string]any)["podUID"] = "foreign-pod" }},
		{"short-attempt", func(r map[string]any) { legacyTaskFixture(r)["exitSource"].(map[string]any)["attemptID"] = "attempt" }},
		{"uppercase-attempt", func(r map[string]any) {
			legacyTaskFixture(r)["exitSource"].(map[string]any)["attemptID"] = strings.Repeat("A", 64)
		}},
		{"foreign-provider", func(r map[string]any) { legacyTaskFixture(r)["exitSource"].(map[string]any)["provider"] = "unknown" }},
		{"missing-model", func(r map[string]any) { delete(legacyTaskFixture(r)["exitSource"].(map[string]any), "requestedModel") }},
		{"oversized-model", func(r map[string]any) {
			legacyTaskFixture(r)["exitSource"].(map[string]any)["requestedModel"] = strings.Repeat("m", 257)
		}},
		{"expected-provider", func(r map[string]any) { legacySession(r)["spec"] = map[string]any{"agent": "claude"} }},
		{"expected-model", func(r map[string]any) { legacySession(r)["spec"] = map[string]any{"model": "other-model"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := legacyFixture()
			tc.mutate(r)
			s, _ := adaptLegacy(t, r, nil)
			record := s.Records[0]
			if record.Binding.Attempt != "" || record.Binding.WriterGeneration != 0 || record.TaskCompletion == nil || record.TerminalRecord != nil || record.Provenance.ClockBindingStatus != "conflict" {
				t.Fatalf("invalid source guessed task binding: %+v", record)
			}
			if record.Provenance.RecordedClockAt != "2026-10-24T12:00:00Z" {
				t.Fatal("foreign raw clock evidence erased")
			}
			e := evaluateLegacy(t, s).StoppedWorkQueue[0]
			if e.Deadline != "" {
				t.Fatal("foreign source established current attempt deadline")
			}
		})
	}
	r := legacyFixture()
	delete(legacyTaskFixture(r), "exitSource")
	s, _ := adaptLegacy(t, r, nil)
	record := s.Records[0]
	if record.Binding.Attempt != "" || record.Provenance.ClockBindingStatus != "unbound_legacy" || record.TaskCompletion == nil || record.Provenance.RecordedClockAt != "2026-10-24T12:00:00Z" {
		t.Fatal("missing-source raw age not retained as unbound")
	}
	if e := evaluateLegacy(t, s).StoppedWorkQueue[0]; e.Deadline != "" || e.ClockSource != "task_finished_at" {
		t.Fatal("legacy missing source was reconstructed")
	}
	if p := evaluateLegacy(t, s).StoppedWorkQueue[0].Provenance; p.RecordedDeadline != "2026-10-31T12:00:00Z" || !p.RecordedAgeEligible || p.ClockBindingStatus != "unbound_legacy" {
		t.Fatal("unbound recorded audit age was erased")
	}
	for _, generation := range []any{nil, 0, 7, "not-an-attempt"} {
		r := legacyFixture()
		last := legacyHistory(r)["lastExecutor"].(map[string]any)
		if generation == nil {
			delete(last, "generation")
		} else {
			last["generation"] = generation
		}
		s, _ := adaptLegacy(t, r, nil)
		if s.Records[0].Binding.Attempt != strings.Repeat("a", 64) || s.Records[0].Binding.WriterGeneration != 0 {
			t.Fatal("Session generation influenced attempt/writer generation")
		}
	}
}

func TestLegacyRetainedTaskBindingAndConflicts(t *testing.T) {
	for _, mode := range []string{"matching", "retained-only", "time-conflict", "source-conflict", "retained-foreign"} {
		t.Run(mode, func(t *testing.T) {
			r := legacyFixture()
			data, err := json.Marshal(legacyTaskFixture(r))
			if err != nil {
				t.Fatal(err)
			}
			var retained map[string]any
			if err := json.Unmarshal(data, &retained); err != nil {
				t.Fatal(err)
			}
			legacyHistory(r)["taskTerminal"] = map[string]any{"task": retained}
			switch mode {
			case "retained-only":
				delete(legacySession(r)["agent"].(map[string]any), "task")
			case "time-conflict":
				retained["finishedAt"] = "2026-10-25T12:00:00Z"
			case "source-conflict":
				retained["exitSource"].(map[string]any)["attemptID"] = strings.Repeat("b", 64)
			case "retained-foreign":
				delete(legacySession(r)["agent"].(map[string]any), "task")
				retained["exitSource"].(map[string]any)["sessionUID"] = "foreign-session"
			}
			s, _ := adaptLegacy(t, r, nil)
			record := s.Records[0]
			if record.TaskCompletion == nil || record.TerminalRecord != nil {
				t.Fatal("retained task presence fell back to executor terminal")
			}
			valid := mode == "matching" || mode == "retained-only"
			if valid {
				if record.Binding.Attempt != strings.Repeat("a", 64) || record.Provenance.ClockPath != "$.sessions[0].privateHistory.taskTerminal.task.finishedAt" {
					t.Fatal("exact retained task not selected")
				}
			} else {
				if record.Binding.Attempt != "" || record.Provenance.ClockBindingStatus != "conflict" {
					t.Fatal("conflicting retained task accepted")
				}
			}
			e := evaluateLegacy(t, s).StoppedWorkQueue[0]
			if (e.Deadline != "") != valid {
				t.Fatal("retained task deadline mismatch")
			}
		})
	}
}

func TestLegacyTaskPresenceAndSessionCreation(t *testing.T) {
	for _, task := range []any{nil, map[string]any{}, map[string]any{"exitCode": 0}} {
		r := legacyFixture()
		legacySession(r)["agent"].(map[string]any)["task"] = task
		s, _ := adaptLegacy(t, r, nil)
		if s.Records[0].TaskCompletion != nil || s.Records[0].TerminalRecord == nil {
			t.Fatal("non-completion task metadata hid actual terminal fallback")
		}
		if e := evaluateLegacy(t, s).StoppedWorkQueue[0]; e.ClockSource != "executor_terminal_observed_at" || e.Deadline == "" {
			t.Fatal("terminal fallback provenance lost")
		}
	}
	r := legacyFixture()
	legacySession(r)["creationTimestamp"] = "2026-10-25T00:00:00Z"
	s, _ := adaptLegacy(t, r, nil)
	record := s.Records[0]
	if record.Binding.Attempt != "" || record.Provenance.ClockBindingStatus != "conflict" || record.Provenance.RecordedClockAt != "2026-10-24T12:00:00Z" {
		t.Fatal("pre-creation task clock was bound")
	}
	if e := evaluateLegacy(t, s).StoppedWorkQueue[0]; e.Deadline != "" {
		t.Fatal("pre-creation task invented deadline")
	}
	r = legacyFixture()
	delete(legacySession(r)["agent"].(map[string]any), "task")
	legacySession(r)["creationTimestamp"] = "2026-10-27T00:00:00Z"
	s, _ = adaptLegacy(t, r, nil)
	if s.Records[0].TerminalRecord != nil || s.Records[0].Provenance.ClockBindingStatus != "conflict" {
		t.Fatal("pre-creation terminal clock was bound")
	}
}

func TestLegacyReportedActivityAndStaleTask(t *testing.T) {
	for _, status := range []string{"busy", "waiting", "idle", "pending", "running", "resuming"} {
		r := legacyFixture()
		legacySession(r)["agent"].(map[string]any)["status"] = status
		s, _ := adaptLegacy(t, r, nil)
		if s.Records[0].State != "active" {
			t.Fatal("positive activity without pod was erased by stale task")
		}
		if e := evaluateLegacy(t, s).StoppedWorkQueue[0]; e.Status != "protected" || e.DispositionDecisionRequired {
			t.Fatal("reported activity became stopped disposition action")
		}
	}
	r := legacyFixture()
	legacySession(r)["agent"].(map[string]any)["status"] = "unknown"
	delete(legacyHistory(r), "executorTerminal")
	s, _ := adaptLegacy(t, r, nil)
	if s.Records[0].State != "unknown" {
		t.Fatal("unknown agent state certified stopped from stale task")
	}
	r = legacyFixture()
	legacySession(r)["phase"] = "Pending"
	legacySession(r)["spec"] = map[string]any{"operatingMode": "Running"}
	legacySession(r)["deletionTimestamp"] = "2026-11-01T11:00:00Z"
	s, _ = adaptLegacy(t, r, nil)
	if s.Records[0].State != "stopped" {
		t.Fatal("desired running mode hid deleting exited executor")
	}
	legacySession(r)["phase"] = "Resuming"
	s, _ = adaptLegacy(t, r, nil)
	if s.Records[0].State != "active" {
		t.Fatal("explicit resuming phase ignored")
	}
}

func legacyPod(phase string, hold bool) map[string]any {
	labels := map[string]any{}
	if hold {
		labels["dev-env.haynesops.com/hold"] = "true"
	}
	return map[string]any{"name": "foreign-pod", "namespace": "test", "uid": "foreign-pod-uid", "phase": phase, "claims": []string{"home-a"}, "labels": labels, "ownerReferences": []any{map[string]any{"kind": "OtherOwner", "uid": "foreign-owner"}}}
}

func TestLegacyActiveForeignMountsAndRescueHold(t *testing.T) {
	for _, phase := range []string{"Running", "Pending", "Unknown", ""} {
		r := legacyFixture()
		r["pods"] = []any{legacyPod(phase, false)}
		s, _ := adaptLegacy(t, r, nil)
		if s.Records[0].State != "active" {
			t.Fatal("foreign/current mount did not protect HOME")
		}
		e := evaluateLegacy(t, s).StoppedWorkQueue[0]
		if e.Status != "protected" || e.DispositionDecisionRequired {
			t.Fatal("active executor lost safety protection")
		}
	}
	r := legacyFixture()
	r["pods"] = []any{legacyPod("Running", true)}
	s, _ := adaptLegacy(t, r, nil)
	if s.Records[0].State != "stopped" || s.Records[0].Disposition != "rescue_hold" {
		t.Fatal("hold mistaken for running provider task")
	}
	e := evaluateLegacy(t, s).StoppedWorkQueue[0]
	if e.Status != "protected" || !e.DispositionDecisionRequired {
		t.Fatal("stopped rescue hold hidden from decision queue")
	}
	p := legacyPod("Running", false)
	p["name"], p["uid"] = "provider-pod", "provider-pod-uid"
	r["pods"] = append(r["pods"].([]any), p)
	s, _ = adaptLegacy(t, r, nil)
	if s.Records[0].State != "active" {
		t.Fatal("provider activity lost behind hold")
	}
}

func TestLegacyOrphansAndExternalDispositions(t *testing.T) {
	r := legacyFixture()
	r["sessions"] = []any{}
	legacyClaim(r)["ownerReferences"] = []any{}
	s, _ := adaptLegacy(t, r, nil)
	if s.Records[0].Disposition != "normal" {
		t.Fatal("orphan inferred to be a fixture")
	}
	s, _ = adaptLegacy(t, r, map[string]string{"pvc-a": "fixture"})
	if s.Records[0].Disposition != "fixture" || s.Records[0].State != "unknown" {
		t.Fatal("external fixture mapping changed state")
	}
	e := evaluateLegacy(t, s).StoppedWorkQueue[0]
	if !e.DispositionDecisionRequired {
		t.Fatal("clockless fixture hidden from decision")
	}
	for _, dispositions := range []map[string]string{{"absent-pvc": "fixture"}, {"pvc-a": "normal"}, {"pvc-a": "active"}} {
		data, _ := json.Marshal(r)
		if _, err := DecodeLegacySnapshot(bytes.NewReader(data), dispositions); err == nil {
			t.Fatal("invalid disposition override accepted")
		}
	}
	r = legacyFixture()
	legacySession(r)["projectCanary"] = true
	data, _ := json.Marshal(r)
	if _, err := DecodeLegacySnapshot(bytes.NewReader(data), map[string]string{"pvc-a": "fixture"}); err == nil {
		t.Fatal("existing canary protection override accepted")
	}
	legacySession(r)["privatePilot"] = true
	s, _ = adaptLegacy(t, r, nil)
	if s.Records[0].Disposition != "canary" {
		t.Fatal("native protection lost")
	}
}

func TestLegacyOwnerHoldIsPreservedWithDispositionDecision(t *testing.T) {
	r := legacyFixture()
	legacySession(r)["holdAnnotations"] = map[string]any{"dev-env.haynesops.com/home-expiry": "hold"}
	s, _ := adaptLegacy(t, r, nil)
	e := evaluateLegacy(t, s).StoppedWorkQueue[0]
	if s.Records[0].Hold == "" || e.Status != "protected" || !e.DispositionDecisionRequired {
		t.Fatalf("owner hold or overdue decision lost: %+v", e)
	}
}

func TestLegacyStrictBoundsAndDispositionDecoder(t *testing.T) {
	for _, input := range []string{`{"at":"2026-11-01T12:00:00Z","at":"2026-11-01T12:00:00Z","claims":[],"sessions":[],"pods":[]}`, `{"at":"2026-11-01T12:00:00Z","claims":[],"sessions":[],"pods":null}`, `{"at":"2026-11-01T12:00:00Z","claims":[],"sessions":[]}`, `{"at":"invalid","claims":[],"sessions":[],"pods":[]}`, strings.Repeat(" ", MaxSnapshotBytes+1), strings.Repeat("[", 34) + strings.Repeat("]", 34)} {
		if _, err := DecodeLegacySnapshot(strings.NewReader(input), nil); err == nil {
			t.Fatal("invalid legacy JSON accepted")
		}
	}
	for _, field := range []string{"claims", "sessions", "pods"} {
		r := legacyFixture()
		limit := MaxRecords
		if field == "pods" {
			limit = MaxLegacyPods
		}
		r[field] = make([]any, limit+1)
		data, _ := json.Marshal(r)
		if _, err := DecodeLegacySnapshot(bytes.NewReader(data), nil); err == nil {
			t.Fatal("legacy record bound ignored")
		}
	}
	r := legacyFixture()
	r["claims"] = append(r["claims"].([]any), legacyClaim(r))
	data, _ := json.Marshal(r)
	if _, err := DecodeLegacySnapshot(bytes.NewReader(data), nil); err == nil {
		t.Fatal("duplicate PVC UID accepted")
	}
	for _, input := range []string{`{"pvc-a":"fixture","pvc-a":"pilot"}`, `{"pvc-a":"normal"}`, `{"pvc-a":"unknown"}`, `null`, strings.Repeat(" ", MaxTrustBytes+1)} {
		if _, err := DecodeLegacyDispositions(strings.NewReader(input)); err == nil {
			t.Fatal("invalid disposition JSON accepted")
		}
	}
	dispositions, err := DecodeLegacyDispositions(strings.NewReader(`{"pvc-a":"fixture","pvc-b":"rescue_hold"}`))
	if err != nil || len(dispositions) != 2 {
		t.Fatal("valid external dispositions refused")
	}
}
