package agentd

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestTerminalTaskExitProofRequiresWaitAndOriginalReceipt(t *testing.T) {
	for _, test := range []struct {
		name            string
		waited, receipt bool
	}{
		{"waited", true, true}, {"spawn-failed", false, true}, {"receipt-missing", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, l := captureLaunch(t)
			l.CaptureOutput = false
			if err := bindInitialUsage(&l, protocol.Session{SessionUID: l.SessionUID, Agent: l.Provider, Mode: protocol.ModeTask, Model: "fixture-model"}, l.PodUID); err != nil {
				t.Fatal(err)
			}
			if test.receipt {
				if err := beginInitialUsage(l, s.StateDir); err != nil {
					t.Fatal(err)
				}
			}
			writeResult(s.StateDir, l, l.CreatedAt, 1, false, streamResult{}, test.waited)
			var got taskResult
			if err := readJSONFile(s.statePath(resultFile), &got); err != nil {
				t.Fatal(err)
			}
			if want := test.waited && test.receipt; (got.ExitSource != nil) != want {
				t.Fatalf("exit source=%+v want proof=%v", got.ExitSource, want)
			}
			if got.ExitSource != nil && !reflect.DeepEqual(got.ExitSource, l.UsageSource) {
				t.Fatal("original attempt changed")
			}
			if got.ExitCode != 1 || got.ConversationID != l.ConversationID {
				t.Fatal("outcome lost")
			}
		})
	}
}

func TestTerminalPrivateTaskNeverAutomaticallyResumes(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		for _, state := range []string{"result", "malformed-result", "saved-launch", "interrupted-launch"} {
			t.Run(provider+"/"+state, func(t *testing.T) {
				s, sess, ws := launchFixture(t)
				sess.Agent = provider
				sess.PrivateHistoryRetained = true
				if err := os.MkdirAll(s.StateDir, 0700); err != nil {
					t.Fatal(err)
				}
				first := Launch{Session: sess.Name, ConversationID: "original", BootID: "original-boot"}
				if state != "result" && state != "malformed-result" {
					if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
						t.Fatal(err)
					}
				}
				if state == "result" {
					if err := writeJSONFile(s.statePath(resultFile), taskResult{TaskResult: protocol.TaskResult{ExitCode: 0, FinishedAt: time.Now()}}); err != nil {
						t.Fatal(err)
					}
				}
				if state == "malformed-result" {
					if err := os.WriteFile(s.statePath(resultFile), []byte("{broken"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				runner := &fakeRunner{}
				d := &Daemon{S: s, R: runner, Session: sess}
				step, why := d.startAgent(context.Background(), ws, Step{State: StepOK}, "rebuilt-pod")
				if why != "" || step.State == StepFail || len(runner.calls) != 0 {
					t.Fatalf("saved private task ran again: %+v %q calls=%+v", step, why, runner.calls)
				}
				if _, err := BuildResume(s, sess, ws, first, "rebuilt-pod", time.Now()); err == nil {
					t.Fatal("BuildResume accepted a retained private task")
				}
			})
		}
	}
}

func TestTerminalTaskResumeGateAndInterruptedMessage(t *testing.T) {
	for _, retained := range []bool{false, true} {
		for _, mode := range []string{protocol.ModeTask, protocol.ModeLocal, protocol.ModeRemote} {
			for _, shared := range []bool{false, true} {
				t.Run(fmt.Sprintf("retained=%t/%s/shared=%t", retained, mode, shared), func(t *testing.T) {
					s, sess, ws := launchFixture(t)
					sess.Mode, sess.PrivateHistoryRetained = mode, retained
					if shared {
						sess.Workspace = &protocol.WorkspaceBinding{ID: "shared", SessionUID: sess.SessionUID}
					}
					first := Launch{Session: sess.Name, ConversationID: "11111111-2222-4333-8444-555555555555", BootID: "earlier", CreatedAt: time.Now().Add(-time.Hour)}
					if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "new", Boot: protocol.BootReady}); err != nil {
						t.Fatal(err)
					}
					if err := writeJSONFile(s.statePath(launchFile), first); err != nil {
						t.Fatal(err)
					}
					blocked := retained && mode == protocol.ModeTask && !shared
					if got := retainedPrivateTask(s, sess); got != blocked {
						t.Fatalf("resume blocked=%t want %t", got, blocked)
					}
					if mode == protocol.ModeTask && !shared {
						_, err := BuildResume(s, sess, ws, first, "new", time.Now())
						if (err != nil) != blocked {
							t.Fatalf("BuildResume error=%v blocked=%t", err, blocked)
						}
						st := CollectStatus(context.Background(), &fakeRunner{}, s, sess, time.Now())
						if st.Agent.State != protocol.AgentInterrupted || strings.Contains(st.Agent.Error, "D-58") == blocked {
							t.Fatalf("Interrupted message changed outside gate: %+v", st.Agent)
						}
					}
				})
			}
		}
	}
}
