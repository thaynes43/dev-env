package apiserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func portalOutcomeFixture(t *testing.T, sessions ...v1alpha1.AgentSession) *fixture {
	t.Helper()
	f := newFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "portal-pod", Namespace: sessionNS, UID: types.UID("pod-uid")}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-v2-portal", Containers: []corev1.Container{{Name: "portal", Image: "x"}}}}
	f.srv.Live = &portalLive{pod: pod, rv: "7", sessions: sessions}
	id := saIdentity(portalReaderRef)
	id.PodName, id.PodUID = pod.Name, string(pod.UID)
	f.auth["tok-portal"] = id
	f.srv.Policy.PortalReaderEnabled = true
	return f
}

func outcomeStatus(sessionUID, podUID string) *v1alpha1.TaskOutcomeStatus {
	return &v1alpha1.TaskOutcomeStatus{SessionUID: sessionUID, PodUID: podUID, Record: v1alpha1.TaskOutcomeRecordStatus{
		PRURLs: []string{"https://github.com/thaynes43/dev-env/pull/759"}, HeadSHAs: []string{strings.Repeat("a", 40)}, Summary: "Ready for lead review"}}
}

// The portal reader sends a task's retained outcome only to a portal that
// asks for it, so an older strict portal keeps reading. It sends only a record
// bound to this Session and valid by the operator's own check.
func TestPortalReaderTaskOutcomeIsNegotiated(t *testing.T) {
	sess := func(name string, mode v1alpha1.SessionMode, outcome *v1alpha1.TaskOutcomeStatus) v1alpha1.AgentSession {
		s := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: types.UID("uid-" + name)},
			Spec:   v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: "claude", Mode: mode, Model: "claude-opus-5-5", Parent: humanSA},
			Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseSuspended, TaskOutcome: outcome}}
		if mode == v1alpha1.ModeTask {
			s.Spec.Prompt = "SECRET-PROMPT"
			s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded", Message: "SECRET-RESULT-MESSAGE"}
		}
		return s
	}
	invalid := outcomeStatus("uid-e-invalid", "pod-original")
	invalid.Record.PRURLs = []string{"https://github.com/someone/dev-env/pull/1"}
	f := portalOutcomeFixture(t,
		sess("a-done", v1alpha1.ModeTask, outcomeStatus("uid-a-done", "pod-original")),
		sess("b-none", v1alpha1.ModeTask, nil),
		sess("c-foreign", v1alpha1.ModeTask, outcomeStatus("uid-another-session", "pod-original")),
		sess("d-remote", v1alpha1.ModeRemote, outcomeStatus("uid-d-remote", "pod-original")),
		sess("e-invalid", v1alpha1.ModeTask, invalid),
	)
	read := func(path string, optIn bool) (string, map[string]map[string]json.RawMessage) {
		t.Helper()
		h := http.Header{}
		if optIn {
			h.Set(apiv1.TaskOutcomeHeader, apiv1.TaskOutcomeV1)
		}
		w := f.doHeader(http.MethodGet, path, "tok-portal", nil, h)
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var page struct {
			Sessions []struct {
				Name     string                     `json:"name"`
				Activity map[string]json.RawMessage `json:"activity"`
			} `json:"sessions"`
		}
		if json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Sessions) != 5 {
			t.Fatal(w.Body.String())
		}
		out := map[string]map[string]json.RawMessage{}
		for _, s := range page.Sessions {
			out[s.Name] = s.Activity
		}
		return w.Body.String(), out
	}
	// An older portal does not ask: no row carries the key.
	for _, path := range []string{apiv1.SessionsPath + "?limit=50&view=activity", apiv1.SessionsPath + "?limit=50"} {
		if body, _ := read(path, false); strings.Contains(body, "taskOutcome") || strings.Contains(body, "Ready for lead review") {
			t.Fatalf("%s sent an outcome without the header: %s", path, body)
		}
	}
	// Asking without view=activity keeps the six base keys.
	if body, rows := read(apiv1.SessionsPath+"?limit=50", true); strings.Contains(body, "taskOutcome") || rows["a-done"] != nil {
		t.Fatalf("the outcome came without the activity view: %s", body)
	}
	body, rows := read(apiv1.SessionsPath+"?limit=50&view=activity", true)
	for _, name := range []string{"b-none", "c-foreign", "d-remote", "e-invalid"} {
		if _, has := rows[name]["taskOutcome"]; has {
			t.Errorf("%s carries an outcome it should not", name)
		}
	}
	if strings.Contains(body, "SECRET-PROMPT") || strings.Contains(body, "SECRET-RESULT-MESSAGE") || strings.Contains(body, "someone") || strings.Contains(body, "uid-another-session") {
		t.Fatalf("the outcome view leaked: %s", body)
	}
	raw, ok := rows["a-done"]["taskOutcome"]
	if !ok {
		t.Fatalf("the opted-in read lost the outcome: %s", body)
	}
	keys := func(r json.RawMessage) string {
		var m map[string]json.RawMessage
		if json.Unmarshal(r, &m) != nil {
			t.Fatal(string(r))
		}
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	var record struct {
		Record json.RawMessage `json:"record"`
	}
	if keys(raw) != "podUID,record,sessionUID" || json.Unmarshal(raw, &record) != nil || keys(record.Record) != "headSHAs,prURLs,summary" {
		t.Fatalf("outcome keys %s", raw)
	}
	var got protocol.TaskOutcome
	if json.Unmarshal(raw, &got) != nil || got.SessionUID != "uid-a-done" || got.PodUID != "pod-original" || got.Record.Summary != "Ready for lead review" ||
		strings.Join(got.Record.PRURLs, ",") != "https://github.com/thaynes43/dev-env/pull/759" || strings.Join(got.Record.HeadSHAs, ",") != strings.Repeat("a", 40) {
		t.Fatalf("outcome %s", raw)
	}
	var result string
	if json.Unmarshal(rows["a-done"]["taskResult"], &result) != nil || result != "Succeeded" {
		t.Fatalf("the process exit changed: %s", rows["a-done"]["taskResult"])
	}
}

// The portal reads at most PortalSessionPageMaxBytes per page of 50. A page of
// task sessions with every portal field at its bound, outcome included, still
// fits. (Without outcomes it was 194401 bytes; with them, 287101, over the
// earlier 256 KiB bound.)
func TestPortalReaderWorstOutcomePageFitsThePortalReadBound(t *testing.T) {
	now := time.Date(2026, 10, 31, 4, 0, 0, 0, time.UTC)
	model := "claude-" + strings.Repeat("m", protocol.MaxUsageModelID-len("claude-"))
	stamp := metav1.NewTime(now.Add(-time.Minute))
	max := protocol.MaxUsageTokens
	cost := strings.Repeat("9", 31) + "." + strings.Repeat("9", 32)
	var sessions []v1alpha1.AgentSession
	for i := 0; i < 50; i++ {
		uid := strings.Repeat("u", 125) + fmt.Sprintf("%03d", i)
		pod := strings.Repeat("p", 125) + fmt.Sprintf("%03d", i)
		var served []protocol.ModelUsage
		for j := 0; j < protocol.MaxServedModels; j++ {
			m := servedModel(model[:len(model)-1]+fmt.Sprint(j), max, cost)
			m.OutputTokens, m.CacheReadInputTokens, m.CacheCreationInputTokens = reportedTokens(max), reportedTokens(max), reportedTokens(max)
			served = append(served, m)
		}
		u := providerObservation(uid, pod, protocol.AgentClaude, model, max, max, cost, served...)
		u.CacheReadInputTokens, u.CacheCreationInputTokens = reportedTokens(max), reportedTokens(max)
		if err := u.Validate(); err != nil {
			t.Fatal("worst usage fixture is invalid:", err)
		}
		repo := strings.Repeat("r", 92) + fmt.Sprintf("%08d", i)
		pr := func(n int) string {
			return "https://github.com/thaynes43/" + strings.Repeat("x", 256-len("https://github.com/thaynes43/")-len("/pull/999999")) + fmt.Sprintf("/pull/99999%d", n)
		}
		outcome := &v1alpha1.TaskOutcomeStatus{SessionUID: uid, PodUID: pod, Record: v1alpha1.TaskOutcomeRecordStatus{
			PRURLs: []string{pr(1), pr(2), pr(3)}, HeadSHAs: []string{strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)}, Summary: strings.Repeat("€", 200)}}
		s := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("n", 60) + fmt.Sprintf("-%02d", i), Namespace: sessionNS, UID: types.UID(uid)},
			Spec: v1alpha1.AgentSessionSpec{Repo: repo, Agent: "claude", Mode: v1alpha1.ModeTask, Model: model, Prompt: "p", Parent: "l" + strings.Repeat("x", 252)},
			Status: v1alpha1.AgentSessionStatus{Phase: v1alpha1.PhaseSuspended, Agent: &v1alpha1.AgentStatus{Status: protocol.AgentExited, LastActivity: &stamp, LastHeartbeat: &stamp},
				TaskResult:    &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultSucceeded},
				RemoteControl: &v1alpha1.RemoteControlStatus{State: "a" + strings.Repeat("b", 31), URL: "https://claude.ai/code/session_" + strings.Repeat("s", 128)},
				TaskOutcome:   outcome}}
		sessions = append(sessions, *withObservation(&s, u))
	}
	f := portalOutcomeFixture(t, sessions...)
	f.now = now
	w := f.doHeader(http.MethodGet, apiv1.SessionsPath+"?limit=50&view=activity", "tok-portal", nil, http.Header{apiv1.TaskOutcomeHeader: {apiv1.TaskOutcomeV1}})
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Count(body, `"taskOutcome"`) != 50 || strings.Count(body, `"scope":"InitialManagedInvocation"`) != 50 || strings.Count(body, `"url":"https://claude.ai`) != 50 {
		t.Fatalf("worst page lost fields: %d %d %d %d", w.Code, strings.Count(body, `"taskOutcome"`), strings.Count(body, `"scope":"InitialManagedInvocation"`), strings.Count(body, `"url":"https://claude.ai`))
	}
	if len(body) > apiv1.PortalSessionPageMaxBytes {
		t.Fatalf("worst outcome page is %d bytes, over the portal's %d-byte read bound", len(body), apiv1.PortalSessionPageMaxBytes)
	}
	t.Logf("worst page with outcomes: %d bytes of %d", len(body), apiv1.PortalSessionPageMaxBytes)
}
