package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Keep a real synthetic owned native leader live on its exact process group.
// No native provider or model is contacted by this fixture.
func liveDecisionFixture(t *testing.T) (Settings, protocol.Session, Launch) {
	t.Helper()
	s, sess, first, _ := nativeInvocationFixture(t)
	next := nextNativeInvocation(t, first)
	cli := fakeCLI(t, t.TempDir(), `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli 0.160.1'; exit 0; fi
exec sleep 30
`)
	next.Argv[0] = cli
	if err := admitNativeContinuation(context.Background(), s, sess, first, &next); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkspaceJSON(s.statePath(resumeFile), next); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: next.BootID, Boot: protocol.BootReady, BootedAt: next.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	lease, err := beginNativeInvocation(next, s.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.beforeSpawn(next, time.Now()); err != nil {
		lease.unlock()
		t.Fatal(err)
	}
	cmd := exec.Command(next.Argv[0], next.Argv[1:]...)
	cmd.Env = agentEnv(os.Environ(), next)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		lease.unlock()
		t.Fatal(err)
	}
	if err := lease.recordStarted(next, cmd.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(pidFile), agentPid{cmd.Process.Pid, procStartTime(cmd.Process.Pid)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = lease.afterWait(next, cmd.ProcessState, cmd.Process.Pid, time.Now())
		lease.unlock()
	})
	return s, sess, next
}

func TestDecisionAssignsPrivateIdentityAndDurablyRecordsOneAnswer(t *testing.T) {
	s, sess, current := liveDecisionFixture(t)
	question := protocol.DecisionQuestion{Question: "Which synthetic branch should continue?", Options: []string{"A", "B"}, Context: "SYNTHETIC-PRIVATE-DECISION-CONTEXT"}
	record, err := AskDecision(context.Background(), s, question, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if record.Session != sess.Name || record.SessionUID != sess.SessionUID || record.PodUID != s.PodUID || record.ThreadID != current.ConversationID ||
		record.WriterGeneration != s.writer.owner.Generation || record.ID == current.NativeInvocationID || record.State != "Open" {
		t.Fatal("decision identity was not assigned by this exact task platform")
	}
	fi, err := os.Stat(s.statePath(decisionRecordFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatal("question context was not stored privately", err)
	}
	if _, err := AskDecision(context.Background(), s, question, time.Now()); err == nil {
		t.Fatal("pending question was overwritten")
	}
	if _, err := AnswerDecision(context.Background(), s, protocol.DecisionAnswer{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", Text: "A"}, time.Now()); err == nil {
		t.Fatal("answer accepted a different question ID")
	}
	answer := protocol.DecisionAnswer{ID: record.ID, Text: "A\nwith synthetic context"}
	answered, err := AnswerDecision(context.Background(), s, answer, time.Now())
	if err != nil || answered.Decision.State != "Answered" || answered.Decision.Answer != answer.Text {
		t.Fatal("answer was not durably recorded", err)
	}
	if _, err := AnswerDecision(context.Background(), s, protocol.DecisionAnswer{ID: record.ID, Text: "B"}, time.Now()); err == nil {
		t.Fatal("a second different answer replaced the recorded one")
	}
	if _, err := AnswerDecision(context.Background(), s, answer, time.Now()); err != nil {
		t.Fatal("identical recorded answer could not be safely inspected", err)
	}
	result, err := ReadDecision(context.Background(), s)
	if err != nil || result.Decision.Question.Context != question.Context || result.Decision.Answer != answer.Text {
		t.Fatal("private durable decision context changed", err)
	}
}

func TestDecisionDispatchFencePastesOnceAndRefusesUncertainReplay(t *testing.T) {
	for _, failure := range []string{"", "paste-buffer", "send-keys"} {
		t.Run("ack-"+failure, func(t *testing.T) {
			s, sess, _ := liveDecisionFixture(t)
			record, err := AskDecision(context.Background(), s, protocol.DecisionQuestion{Question: "Synthetic owner choice?"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AnswerDecision(context.Background(), s, protocol.DecisionAnswer{ID: record.ID, Text: "EXACT-SYNTHETIC-ANSWER\nsecond line"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			var delivered string
			enter := 0
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				if c.Name != "tmux" {
					t.Fatal("decision dispatcher started another native command", c.Name)
				}
				if c.Args[0] == "load-buffer" {
					b, _ := io.ReadAll(c.Stdin)
					delivered = string(b)
				}
				if c.Args[0] == "send-keys" {
					enter++
				}
				if c.Args[0] == failure {
					return Result{}, errors.New("synthetic acknowledgement failure")
				}
				return Result{}, nil
			}}
			d := &Daemon{S: s, Session: sess, R: runner}
			old := deliverSettle
			deliverSettle = 0
			defer func() { deliverSettle = old }()
			err = d.dispatchDecision(context.Background())
			if (err == nil) != (failure == "") {
				t.Fatal("dispatch acknowledgement result changed", err)
			}
			if err := d.dispatchDecision(context.Background()); err != nil {
				t.Fatal("fenced dispatch was reconsidered", err)
			}
			stored, err := readPrivateDecision(s)
			if err != nil {
				t.Fatal(err)
			}
			want := "Delivered"
			if failure != "" {
				want = "Uncertain"
			}
			if stored.State != want || enter > 1 || !strings.Contains(delivered, "EXACT-SYNTHETIC-ANSWER\nsecond line") {
				t.Fatal("answer was replayed, changed or lost its durable fence")
			}
			if failure == "paste-buffer" && enter != 0 {
				t.Fatal("failed paste still submitted Enter")
			}
			if failure != "paste-buffer" && enter != 1 {
				t.Fatal("one exact answer did not submit exactly once")
			}
			if failure == "" {
				next, err := AskDecision(context.Background(), s, protocol.DecisionQuestion{Question: "Next synthetic owner choice?"}, time.Now())
				if err != nil || next.ID == record.ID {
					t.Fatal("same owned native TUI could not record its next distinct question", err)
				}
				archive := filepath.Join(s.StateDir, "decisions", record.ID+".json")
				b, err := os.ReadFile(archive)
				if err != nil {
					t.Fatal("completed private answer context was not retained", err)
				}
				var old privateDecision
				if json.Unmarshal(b, &old) != nil || old.State != "Delivered" || old.Answer != stored.Answer {
					t.Fatal("completed decision archive changed")
				}
			}
		})
	}
}

func TestDecisionReadAndAnswerRefuseChangedWriterAndPrivatePermissions(t *testing.T) {
	s, _, _ := liveDecisionFixture(t)
	record, err := AskDecision(context.Background(), s, protocol.DecisionQuestion{Question: "Synthetic choice?"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var owner taskOwner
	if err := readWorkspaceJSON(s.ownerPath(record.Session), &owner); err != nil {
		t.Fatal(err)
	}
	owner.Generation++
	if err := writeWorkspaceJSON(s.ownerPath(record.Session), owner); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDecision(context.Background(), s); err == nil {
		t.Fatal("foreign writer generation read old question context")
	}
	if _, err := AnswerDecision(context.Background(), s, protocol.DecisionAnswer{ID: record.ID, Text: "answer"}, time.Now()); err == nil {
		t.Fatal("foreign writer generation adopted old answer")
	}
	owner.Generation--
	if err := writeWorkspaceJSON(s.ownerPath(record.Session), owner); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.statePath(decisionRecordFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDecision(context.Background(), s); err == nil {
		t.Fatal("publicly readable private decision was accepted")
	}
}

func TestDecisionFinalDispatchRechecksStopAndWriterAfterResumeReservation(t *testing.T) {
	for _, change := range []string{"stop", "writer-generation"} {
		t.Run(change, func(t *testing.T) {
			s, sess, current := liveDecisionFixture(t)
			record, err := AskDecision(context.Background(), s, protocol.DecisionQuestion{Question: "Synthetic final-fence choice?"}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			answer := protocol.DecisionAnswer{ID: record.ID, Text: "RETAINED-SYNTHETIC-ANSWER"}
			if _, err := AnswerDecision(context.Background(), s, answer, time.Now()); err != nil {
				t.Fatal(err)
			}
			pending, err := readPrivateDecision(s)
			if err != nil {
				t.Fatal(err)
			}
			runner := &fakeRunner{handle: func(c Cmd) (Result, error) {
				t.Fatal("final refusal reached native transport", c.Name)
				return Result{}, nil
			}}
			d := &Daemon{S: s, Session: sess, R: runner}
			if err := d.reserveDecisionResume(context.Background(), pending, current); err != nil {
				t.Fatal(err)
			}
			if change == "stop" {
				if err := RequestWorkspaceStop(context.Background(), s, sess, time.Now()); err != nil {
					t.Fatal(err)
				}
			} else {
				var owner taskOwner
				if err := readWorkspaceJSON(s.ownerPath(sess.Name), &owner); err != nil {
					t.Fatal(err)
				}
				owner.Generation++
				if err := writeWorkspaceJSON(s.ownerPath(sess.Name), owner); err != nil {
					t.Fatal(err)
				}
			}
			if err := d.pasteDecisionAnswer(context.Background(), record.ID, current); err == nil {
				t.Fatal("late stop or writer replacement dispatched the recorded answer")
			}
			stored, err := readPrivateDecision(s)
			if err != nil || stored.State != "Uncertain" || stored.Answer != answer.Text {
				t.Fatal("final refusal lost its private answer or durable no-replay fence", err)
			}
			if err := d.dispatchDecision(context.Background()); err != nil {
				t.Fatal("uncertain answer was reconsidered", err)
			}
		})
	}
}
