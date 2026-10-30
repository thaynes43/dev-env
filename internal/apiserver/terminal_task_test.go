package apiserver

import (
	"net/http"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestTerminalTaskHeartbeatAcceptsOnlyOriginalExitedAttempt(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*protocol.Status)
	}{
		{"original", func(*protocol.Status) {}},
		{"other-pod", func(s *protocol.Status) { s.Agent.Task.ExitSource.PodUID = "other" }},
		{"other-session", func(s *protocol.Status) { s.Agent.Task.ExitSource.SessionUID = "other" }},
		{"other-provider", func(s *protocol.Status) { s.Agent.Task.ExitSource.Provider = protocol.AgentCodex }},
		{"other-model", func(s *protocol.Status) { s.Agent.Task.ExitSource.RequestedModel = "other-model" }},
		{"no-attempt", func(s *protocol.Status) { s.Agent.Task.ExitSource.AttemptID = "" }},
		{"busy", func(s *protocol.Status) { s.Agent.State = protocol.AgentBusy }},
		{"attached", func(s *protocol.Status) { s.Agent.Attached = 1 }},
		{"future-result", func(s *protocol.Status) { s.Agent.Task.FinishedAt = s.Agent.Task.FinishedAt.Add(time.Hour) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture(t)
			name := "terminal-task"
			token := f.sessionPod(name, "full", 0)
			u := fixtureUsage(f, name, token)
			st := status(name)
			st.Agent.State = protocol.AgentExited
			st.Agent.Attached = 0
			st.Agent.Task = &protocol.TaskResult{ExitSource: &u.Source, ExitCode: 1, FinishedAt: f.now, Subtype: "error_during_execution"}
			test.edit(&st)
			w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st)
			if w.Code != http.StatusNoContent {
				t.Fatalf("proof failure blocked ordinary outcome: %d %s", w.Code, w.Body.String())
			}
			got := f.session(name).Status.Agent.Task
			if got == nil || got.ExitCode != 1 {
				t.Fatal("outcome erased")
			}
			if (got.ExitSource != nil) != (test.name == "original") {
				t.Fatalf("wrong proof accepted: %+v", got.ExitSource)
			}
		})
	}
}
