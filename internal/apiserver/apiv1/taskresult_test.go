package apiv1

import (
	"strings"
	"testing"
)

// history-04 (DESIGN-021): the task's own result, apart from the pod's phase.
func TestTaskResultVerdict(t *testing.T) {
	for name, c := range map[string]struct {
		task          TaskResult
		state, reason string
		says          []string
	}{
		"success":              {TaskResult{ExitCode: 0, Subtype: "success", NumTurns: 2}, TaskSucceeded, "TaskSucceeded", []string{"exited 0", "success", "2 turns"}},
		"one turn":             {TaskResult{ExitCode: 0, Subtype: "success", NumTurns: 1}, TaskSucceeded, "TaskSucceeded", []string{"after 1 turn"}},
		"exit 1 with error":    {TaskResult{ExitCode: 1, Subtype: "error_max_turns", IsError: true, NumTurns: 3}, TaskFailed, "TaskExitedNonZero", []string{"exited 1", "error_max_turns", "error"}},
		"exit 1, success":      {TaskResult{ExitCode: 1, Subtype: "success"}, TaskFailed, "TaskExitedNonZero", []string{"exited 1"}},
		"exit 0 with is_error": {TaskResult{ExitCode: 0, Subtype: "success", IsError: true}, TaskFailed, "TaskReportedError", []string{"exited 0", "error"}},
		"exit 0, error subtype": {TaskResult{ExitCode: 0, Subtype: "error_during_execution"}, TaskFailed, "TaskReportedError",
			[]string{"error_during_execution"}},
		"codex provider error": {TaskResult{ExitCode: 0, Subtype: "provider_error", IsError: true}, TaskFailed, "TaskReportedError", []string{"provider_error"}},
		"timed out":            {TaskResult{ExitCode: 143, TimedOut: true, Subtype: "success"}, TaskTimedOut, "TaskTimedOut", []string{"limits.timeout", "exited 143"}},
		"no result event":      {TaskResult{ExitCode: 0}, TaskUnknown, "TaskResultMissing", []string{"exited 0", "no success result"}},
		"unrecognized subtype": {TaskResult{ExitCode: 0, Subtype: "partial"}, TaskUnknown, "TaskResultMissing", []string{"partial"}},
	} {
		t.Run(name, func(t *testing.T) {
			v := c.task.Verdict()
			if v.State != c.state || v.Reason != c.reason {
				t.Fatalf("verdict %+v; want %s %s", v, c.state, c.reason)
			}
			for _, s := range c.says {
				if !strings.Contains(v.Message, s) {
					t.Errorf("message %q lacks %q", v.Message, s)
				}
			}
		})
	}
}

func TestSessionTaskVerdictIsTaskModeOnly(t *testing.T) {
	task := &TaskResult{ExitCode: 0, Subtype: "success"}
	if v := (Session{Mode: "task", AgentStatus: &AgentStatus{Task: task}}).TaskVerdict(); v == nil || v.State != TaskSucceeded {
		t.Fatalf("task session verdict %+v", v)
	}
	for name, s := range map[string]Session{
		"no agent status": {Mode: "task"},
		"no task yet":     {Mode: "task", AgentStatus: &AgentStatus{State: "busy"}},
		"remote mode":     {Mode: "remote", AgentStatus: &AgentStatus{Task: task}},
	} {
		if v := s.TaskVerdict(); v != nil {
			t.Errorf("%s: verdict %+v, want none", name, v)
		}
	}
}
