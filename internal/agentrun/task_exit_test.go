package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// A coordinator learns how a headless child ended by reading it: show prints
// the child's exit, the task verdict and the child's retained outcome, in each
// phase the API reports (OP402). The read itself exits 0, because the child's
// failure is data, not a failure of show.
//
// The fake API serves taskOutcome only to a reader that asks for it, as the
// operator does (internal/apiserver/server.go, withoutTaskOutcome). So this
// test also fails if agent-run stops sending that header.
//
// This test covers show's formatting, the opt-in header and exit handling. It
// is not evidence of persistence, of retrieval after a reap, or of recovery
// after a restart: TestEnvtestTaskOutcomeRetention in internal/apiserver runs
// those against a real kube-apiserver.
func TestShowReportsAFailedTaskChildsRealExitAndRetainedOutcome(t *testing.T) {
	head := strings.Repeat("b", 40)
	pr := "https://github.com/thaynes43/dev-env/pull/402"
	for _, phase := range []string{"Running", "Suspended", "Archived", "Failed"} {
		t.Run(phase, func(t *testing.T) {
			h := newHarness(t)
			s := testSession("child-task-1", phase)
			s.Agent, s.Model, s.Effort = "codex", "gpt-6-astra", "max"
			s.AgentStatus = &apiv1.AgentStatus{State: "exited", Task: &apiv1.TaskResult{
				ExitCode: 3, FinishedAt: testNow.Add(-time.Minute), Subtype: "error_during_execution", IsError: true, NumTurns: 1,
			}}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			var withOutcome map[string]any
			if err := json.Unmarshal(raw, &withOutcome); err != nil {
				t.Fatal(err)
			}
			withOutcome["taskOutcome"] = map[string]any{"sessionUID": "original-session", "podUID": "original-pod", "record": map[string]any{
				"summary": "Blocked: review.md was not written", "prURLs": []string{pr}, "headSHAs": []string{head},
			}}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.Method != http.MethodGet || r.URL.Path != apiv1.SessionPath(s.Name) {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get(apiv1.TaskOutcomeHeader) != apiv1.TaskOutcomeV1 {
					writeTestJSON(w, http.StatusOK, s)
					return
				}
				writeTestJSON(w, http.StatusOK, withOutcome)
			}

			h.mustRun(ExitOK, "show", s.Name)
			contains(t, "show", h.stdout.String(),
				"phase:", phase,
				"codex gpt-6-astra, effort max, task mode",
				"agent state:", "exited",
				"task:", "exit 3 at 2026-10-06T20:59:00Z, 1 turns, error error_during_execution",
				"task result:", "Failed (TaskExitedNonZero): the task exited 3 (error_during_execution, error) after 1 turn",
				"Task outcome:", "Blocked: review.md was not written", pr, head)

			h.mustRun(ExitOK, "show", s.Name, "-o", "json")
			var got apiv1.Session
			if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil {
				t.Fatalf("-o json: %v\n%s", err, h.stdout.String())
			}
			if got.AgentStatus == nil || got.AgentStatus.Task == nil || got.AgentStatus.Task.ExitCode != 3 || !got.AgentStatus.Task.IsError {
				t.Errorf("-o json lost the child's real exit: %+v", got.AgentStatus)
			}
			if got.TaskOutcome == nil || got.TaskOutcome.Record.Summary != "Blocked: review.md was not written" ||
				len(got.TaskOutcome.Record.PRURLs) != 1 || got.TaskOutcome.Record.PRURLs[0] != pr {
				t.Errorf("-o json lost the retained outcome: %+v", got.TaskOutcome)
			}
		})
	}
}
