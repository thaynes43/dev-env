package agentd

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestPreflightLaterBootKeepsSettledOutcome(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
		start *protocol.StartRecord
	}{
		{preflightNoUsableModel, protocol.AgentFailed, &protocol.StartRecord{Failure: protocol.StartFailureNoAnswer}},
		{preflightStarted, protocol.AgentInterrupted, &protocol.StartRecord{Model: preflightFallback, FallbackCause: protocol.StartFailureModelRefused}},
		{preflightChecking, protocol.AgentInterrupted, nil},
	} {
		t.Run(tc.state, func(t *testing.T) {
			r := newLifecycleRig(t, "success")
			// These numeric identities are live, but belong to the old boot.
			// No process is started by this test or may be treated as owned.
			rec := startPreflight{State: tc.state, Pid: os.Getpid(), Start: procStartTime(os.Getpid()), GroupOwned: true,
				TaskPending: tc.state == preflightChecking, ConversationID: r.l.ConversationID, BootID: r.l.BootID}
			if tc.start != nil {
				rec.Model, rec.Failure, rec.FallbackCause = tc.start.Model, tc.start.Failure, tc.start.FallbackCause
			}
			for name, value := range map[string]any{
				startPreflightFile: rec,
				pidFile:            agentPid{Pid: rec.Pid, Start: rec.Start},
				bootFile:           bootRecord{BootID: "later-boot", Boot: protocol.BootReady},
			} {
				if err := writeJSONFile(r.s.statePath(name), value); err != nil {
					t.Fatal(err)
				}
			}
			st := lifecycleStatus(r)
			if st.Agent.State != tc.want || !reflect.DeepEqual(st.Start, tc.start) {
				t.Errorf("later boot: agent=%+v start=%+v; want %s with start=%+v", st.Agent, st.Start, tc.want, tc.start)
			}
			if rec.Failure != "" && !strings.Contains(st.Agent.Error, rec.Failure) {
				t.Errorf("later boot lost the failure in the agent error: %q", st.Agent.Error)
			}
			if _, owned := readPreflight(r.s, r.l); owned || preflightLive(r.s, r.l) {
				t.Error("historical outcome restored another boot's process ownership")
			}
			// The retained launch still closes the boot's start latch. History
			// cannot be confused with a new outcome: no second launch is made.
			runner := &fakeRunner{}
			sess := protocol.Session{Name: r.l.Session, Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Caller: summonedCaller}
			d := quietDaemon(r.s, sess, runner, loginNow.Add(3*time.Minute))
			step, why := d.startAgent(context.Background(), protocol.Workspace{}, Step{Name: "repo", State: StepOK}, "later-boot")
			if why != "" || step.State == StepFail || len(runner.calls) != 0 {
				t.Errorf("later boot tried to launch again: %+v %q %+v", step, why, runner.calls)
			}
			// Even the history reader must still match both launch identities.
			for _, other := range []Launch{
				{ConversationID: "different-conversation", BootID: r.l.BootID, Preflight: r.l.Preflight},
				{ConversationID: r.l.ConversationID, BootID: "different-launch-boot", Preflight: r.l.Preflight},
			} {
				if preflightFailed(r.s, other) || preflightStartRecord(r.s, other) != nil {
					t.Error("historical outcome was attributed to another launch")
				}
			}
		})
	}
}
