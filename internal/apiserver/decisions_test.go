package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func decisionAPIFixture(t *testing.T) (*fixture, *fakeExec, *v1alpha1.AgentSession, *corev1.Pod) {
	t.Helper()
	f := coordinatorFixture(t)
	f.sessionPod("own-task", "dev", 0)
	sess := f.session("own-task")
	sess.Spec.Parent, sess.Spec.Agent, sess.Spec.Mode = coordinatorSA, v1alpha1.AgentCodex, v1alpha1.ModeTask
	sess.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects"}
	if err := f.c.Update(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	f.runPod("own-task")
	var pod corev1.Pod
	if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: sess.Name}, &pod); err != nil {
		t.Fatal(err)
	}
	record := protocol.DecisionRecord{Version: 1, ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Session: sess.Name, SessionUID: string(sess.UID),
		PodUID: string(pod.UID), ThreadID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", WriterGeneration: 1,
		Question: protocol.DecisionQuestion{Question: "Synthetic owner decision?", Context: "SYNTHETIC-PRIVATE-CONTEXT"}, State: "Open", CreatedAt: time.Now().UTC()}
	b, err := json.Marshal(protocol.DecisionResult{Decision: &record})
	if err != nil {
		t.Fatal(err)
	}
	ex := &fakeExec{out: string(b)}
	ex.handle = func(cmd []string, input string) (string, int) {
		if len(cmd) < 3 || cmd[2] != "decision-answer" {
			return ex.out, ex.code
		}
		authority := f.session(sess.Name).Status.DecisionAnswer
		if authority == nil || authority.Phase != "Reserved" {
			t.Fatal("answer exec happened before durable operator reservation")
		}
		var result protocol.DecisionResult
		var answer protocol.DecisionAnswer
		if json.Unmarshal([]byte(ex.out), &result) != nil || json.Unmarshal([]byte(input), &answer) != nil || result.Decision == nil {
			return ex.out, ex.code
		}
		at := result.Decision.CreatedAt.Add(time.Second)
		result.Decision.State, result.Decision.Answer, result.Decision.AnsweredAt = "Answered", answer.Text, &at
		data, _ := json.Marshal(result)
		ex.out = string(data)
		return ex.out, ex.code
	}
	f.srv.Exec, f.srv.ManagedChildDecisions = ex, true
	return f, ex, sess, &pod
}

func TestDecisionAPIRoutesBindDirectChildAndTargetUIDs(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, change := range []string{"current", "disabled", "human", "client", "session", "foreign-parent", "session-uid", "session-version", "parent", "pod-uid", "pod-owner", "pod-service-account"} {
			t.Run(method+"/"+change, func(t *testing.T) {
				f, ex, sess, pod := decisionAPIFixture(t)
				token := tokCoordinator
				switch change {
				case "disabled":
					f.srv.ManagedChildDecisions = false
				case "human":
					token = tokHuman
				case "client":
					token = tokClient
				case "session":
					token = "tok-session-" + sess.Name
				case "foreign-parent":
					sess.Spec.Parent = "another-parent"
					if err := f.c.Update(context.Background(), sess); err != nil {
						t.Fatal(err)
					}
				default:
					f.srv.Live = &changedChildReader{Reader: f.c, kind: change}
				}
				var body any
				if method == http.MethodPost {
					body = protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "recorded synthetic answer"}

				}
				w := f.do(method, apiv1.SessionDecisionPath(sess.Name), token, body)
				if change != "current" {
					wantError(t, w, http.StatusForbidden, apiv1.CodeForbidden)
					if len(ex.cmds) != 0 {
						t.Fatal("disabled/foreign/changed decision reached target exec")
					}
					return
				}
				want := http.StatusOK
				command := "decision-read"
				if method == http.MethodPost {
					want, command = http.StatusAccepted, "decision-answer"
				}
				last := len(ex.cmds) - 1
				if w.Code != want || last < 0 || (method == http.MethodGet && len(ex.cmds) != 1) || (method == http.MethodPost && len(ex.cmds) != 2) || !strings.Contains(ex.cmds[last], command) || !strings.HasSuffix(ex.cmds[last], "--expected-pod-uid "+string(pod.UID)+" --expected-session-uid "+string(sess.UID)) {
					t.Fatal("exact decision route omitted its direct-child target UID fence", w.Code)
				}
				result := decode[protocol.DecisionResult](t, w)
				if result.Decision.Question.Context != "SYNTHETIC-PRIVATE-CONTEXT" {
					t.Fatal("bounded decision read changed context")
				}
			})
		}
	}
}

func TestDecisionAPIRefusesOverflowAndForeignResponseIdentity(t *testing.T) {
	for _, change := range []string{"overflow", "session", "pod", "id", "thread", "state", "answer", "timestamp"} {
		t.Run(change, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			if change == "overflow" {
				ex.out = strings.Repeat("x", protocol.MaxDecisionRecordBytes+1)
			} else {
				var response protocol.DecisionResult
				if json.Unmarshal([]byte(ex.out), &response) != nil {
					t.Fatal("invalid synthetic fixture")
				}
				switch change {
				case "session":
					response.Decision.SessionUID = "replacement-session"
				case "pod":
					response.Decision.PodUID = "replacement-pod"
				case "id":
					response.Decision.ID = strings.Repeat("x", 36)
				case "thread":
					response.Decision.ThreadID = strings.Repeat("x", 36)
				case "state":
					response.Decision.State = "Unknown"
				case "answer":
					response.Decision.Answer = "contradictory answer"
				case "timestamp":
					response.Decision.State = "Answered"
					response.Decision.Answer = "answer"
					at := response.Decision.CreatedAt.Add(-time.Second)
					response.Decision.AnsweredAt = &at
				}
				b, _ := json.Marshal(response)
				ex.out = string(b)
			}
			wantError(t, f.do(http.MethodGet, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, nil), http.StatusConflict, apiv1.CodeConflict)
		})
	}
}

func TestDecisionAPIAnswerRequiresDurableRecordResponse(t *testing.T) {
	f, ex, sess, _ := decisionAPIFixture(t)
	ex.out = "{}"
	read := f.do(http.MethodGet, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, nil)
	if read.Code != http.StatusOK || decode[protocol.DecisionResult](t, read).Decision != nil {
		t.Fatal("empty decision lookup changed")
	}
	answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "recorded synthetic answer"}
	wantError(t, f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer), http.StatusConflict, apiv1.CodeConflict)
}

func TestDecisionAuthorityReservedConfirmedAndUnknownExecNeverReplays(t *testing.T) {
	for _, failure := range []string{"none", "unknown-exec", "malformed-ack", "changed-question", "forged-local-answer"} {
		t.Run(failure, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "SYNTHETIC-APPROVED-ANSWER"}
			handler := ex.handle
			if failure == "forged-local-answer" {
				var local protocol.DecisionResult
				_ = json.Unmarshal([]byte(ex.out), &local)
				at := local.Decision.CreatedAt.Add(time.Second)
				local.Decision.State, local.Decision.Answer, local.Decision.AnsweredAt = "Answered", answer.Text, &at
				data, _ := json.Marshal(local)
				ex.out = string(data)
			} else if failure != "none" {
				ex.handle = func(cmd []string, input string) (string, int) {
					out, code := handler(cmd, input)
					if cmd[2] != "decision-answer" {
						return out, code
					}
					switch failure {
					case "unknown-exec":
						return out, 1
					case "malformed-ack":
						return "{", 0
					case "changed-question":
						var local protocol.DecisionResult
						_ = json.Unmarshal([]byte(out), &local)
						local.Decision.Question.Context = "ALTERED-AFTER-PARENT-RESERVATION"
						data, _ := json.Marshal(local)
						return string(data), 0
					}
					return out, code
				}
			}
			w := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			saved := f.session(sess.Name).Status.DecisionAnswer
			if failure == "none" {
				result := decode[protocol.DecisionResult](t, w)
				if w.Code != http.StatusAccepted || saved == nil || !protocol.DecisionAuthority(*saved).Confirms(*result.Decision) {
					t.Fatal("202 lacked durable confirmed authority and actual record", w.Code)
				}
			} else {
				if w.Code == http.StatusAccepted {
					t.Fatal("uncertain/forged answer received 202")
				}
				if failure == "forged-local-answer" {
					if saved != nil {
						t.Fatal("local answer manufactured operator authority")
					}
				} else if saved == nil || saved.Phase != "Reserved" {
					t.Fatal("unknown exec lost durable reservation")
				}
			}
			count := func() int {
				n := 0
				for _, cmd := range ex.cmds {
					if strings.Contains(cmd, "decision-answer") {
						n++
					}
				}
				return n
			}
			before := count()
			again := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			if count() != before || (failure == "none" && again.Code != http.StatusAccepted) || (failure != "none" && again.Code == http.StatusAccepted) {
				t.Fatal("unknown answer exec replayed or safe observation changed", again.Code)
			}
		})
	}
}

func TestDecisionOwnAuthorityReadIsLiveUIDBoundAndHeartbeatCannotForgeIt(t *testing.T) {
	f, ex, sess, _ := decisionAPIFixture(t)
	token := "tok-session-" + sess.Name
	var local protocol.DecisionResult
	_ = json.Unmarshal([]byte(ex.out), &local)
	at := local.Decision.CreatedAt.Add(time.Second)
	local.Decision.State, local.Decision.Answer, local.Decision.AnsweredAt = "Delivered", "FORGED-LOCAL-ANSWER", &at
	authority := protocol.AnswerAuthority(*local.Decision, local.Decision.Answer)
	st := status(sess.Name)
	st.Decision = &protocol.DecisionOutcome{ID: local.Decision.ID, SessionUID: local.Decision.SessionUID, PodUID: local.Decision.PodUID, ThreadID: local.Decision.ThreadID, WriterGeneration: local.Decision.WriterGeneration, State: "Delivered", AnswerDigest: authority.Digest, At: f.now.Add(time.Hour)}
	data, _ := json.Marshal(st)
	var forged map[string]any
	_ = json.Unmarshal(data, &forged)
	forged["decisionAnswer"] = authority
	w := f.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), token, forged)
	got := f.session(sess.Name)
	if w.Code != http.StatusNoContent || got.Status.DecisionAnswer != nil || got.Status.Outcome == nil || !got.Status.Outcome.At.Time.Equal(f.now) || got.Status.Agent == nil {
		t.Fatal("own heartbeat forged authority, cleared escalation or rejected skew", w.Code)
	}
	result := decode[protocol.DecisionAuthorityResult](t, f.do(http.MethodGet, protocol.DecisionAuthorityPath(sess.Name), token, nil))
	if result.Authority != nil {
		t.Fatal("local forgery became operator authority")
	}
	// Real parent-confirmed authority clears only an exact delivered digest.
	converted := v1alpha1.DecisionAnswerStatus(authority)
	got.Status.DecisionAnswer = &converted
	if err := f.c.Status().Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	st.Decision.AnswerDigest = strings.Repeat("0", 64)
	if f.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), token, st).Code != http.StatusNoContent || f.session(sess.Name).Status.Outcome == nil {
		t.Fatal("foreign digest cleared escalation")
	}
	st.Decision.AnswerDigest = authority.Digest
	if f.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), token, st).Code != http.StatusNoContent || f.session(sess.Name).Status.Outcome != nil {
		t.Fatal("exact parent-confirmed answer could not complete discovery")
	}
	got = f.session(sess.Name)
	if got.Status.DecisionAnswer == nil || *got.Status.DecisionAnswer != converted {
		t.Fatal("heartbeat overwrote operator authority")
	}
	for _, change := range []string{"session-uid", "pod-uid", "pod-owner", "pod-service-account"} {
		f.srv.Live = &changedChildReader{Reader: f.c, kind: change, childReads: 1, podReads: 1}
		w := f.do(http.MethodGet, protocol.DecisionAuthorityPath(sess.Name), token, nil)
		if w.Code == http.StatusOK {
			t.Fatal("own authority read accepted changed live identity", change)
		}
	}
}

type lostAuthorityACKClient struct {
	client.Client
	phase string
}

func (c lostAuthorityACKClient) Status() client.SubResourceWriter {
	return lostAuthorityACKWriter{SubResourceWriter: c.Client.Status(), phase: c.phase}
}

type lostAuthorityACKWriter struct {
	client.SubResourceWriter
	phase string
}

func (w lostAuthorityACKWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if err := w.SubResourceWriter.Patch(ctx, obj, patch, opts...); err != nil {
		return err
	}
	if sess, ok := obj.(*v1alpha1.AgentSession); ok && sess.Status.DecisionAnswer != nil && sess.Status.DecisionAnswer.Phase == w.phase {
		return errors.New("synthetic applied write with lost acknowledgement")
	}
	return nil
}

func TestDecisionAuthorityAppliedWriteLostACKNeverRepeatsAnswerExec(t *testing.T) {
	for _, phase := range []string{"Reserved", "Confirmed"} {
		t.Run(phase, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			f.srv.Client = lostAuthorityACKClient{Client: f.c, phase: phase}
			answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "SYNTHETIC-ANSWER"}
			w := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			saved := f.session(sess.Name).Status.DecisionAnswer
			if w.Code == http.StatusAccepted || saved == nil || saved.Phase != phase {
				t.Fatal("lost write acknowledgement incorrectly confirmed response", w.Code)
			}
			before := 0
			for _, cmd := range ex.cmds {
				if strings.Contains(cmd, "decision-answer") {
					before++
				}
			}
			again := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			after := 0
			for _, cmd := range ex.cmds {
				if strings.Contains(cmd, "decision-answer") {
					after++
				}
			}
			want := 0
			if phase == "Confirmed" {
				want = 1
			}
			if before != want || after != before || (phase == "Reserved" && again.Code == http.StatusAccepted) || (phase == "Confirmed" && again.Code != http.StatusAccepted) {
				t.Fatal("lost authority ACK replayed exec or lost exact observation", again.Code)
			}
		})
	}
}

func TestDecisionAuthorityRequiresParentStillLiveBeforeReservation(t *testing.T) {
	f, ex, sess, _ := decisionAPIFixture(t)
	handler := ex.handle
	ex.handle = func(cmd []string, input string) (string, int) {
		out, code := handler(cmd, input)
		if cmd[2] == "decision-read" {
			id := f.auth[tokCoordinator]
			var pod corev1.Pod
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: id.Namespace, Name: id.PodName}, &pod); err != nil {
				t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), &pod); err != nil {
				t.Fatal(err)
			}
		}
		return out, code
	}
	answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "SYNTHETIC-ANSWER"}
	w := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
	if w.Code != http.StatusForbidden || f.session(sess.Name).Status.DecisionAnswer != nil || len(ex.cmds) != 1 {
		t.Fatal("departed parent wrote authority or answer", w.Code)
	}
}

func TestDecisionAuthorityAcceptsUnrelatedStatusUpdatesAtBothExecWindows(t *testing.T) {
	f, ex, sess, _ := decisionAPIFixture(t)
	handler := ex.handle
	reads, answers := 0, 0
	ex.handle = func(cmd []string, input string) (string, int) {
		out, code := handler(cmd, input)
		saved := f.session(sess.Name)
		before := saved.ResourceVersion
		phase := "after-first-read"
		if cmd[2] == "decision-answer" {
			answers++
			phase = "after-answer-before-confirm"
		} else {
			reads++
		}
		saved.Status.Agent = &v1alpha1.AgentStatus{Status: "busy", Message: phase}
		saved.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, Note: phase}
		if err := f.c.Status().Update(context.Background(), saved); err != nil {
			t.Fatal(err)
		}
		if f.session(sess.Name).ResourceVersion == before {
			t.Fatal("fixture did not create unrelated RV change")
		}
		return out, code
	}
	answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "SYNTHETIC-ANSWER"}
	w := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
	if w.Code != http.StatusAccepted {
		t.Fatal("unrelated heartbeat/controller update blocked authority", w.Code, w.Body.String())
	}
	result := decode[protocol.DecisionResult](t, w)
	saved := f.session(sess.Name)
	if reads != 1 || answers != 1 || saved.Status.DecisionAnswer == nil || !protocol.DecisionAuthority(*saved.Status.DecisionAnswer).Confirms(*result.Decision) {
		t.Fatal("authority did not confirm exactly one recorded answer")
	}
	if saved.Status.Agent == nil || saved.Status.Agent.Message != "after-answer-before-confirm" || saved.Status.Outcome == nil || saved.Status.Outcome.State != v1alpha1.OutcomeDone || saved.Status.Outcome.Note != "after-answer-before-confirm" {
		t.Fatal("fresh authority patch clobbered unrelated status")
	}
}

func TestDecisionAuthorityConcurrentFieldChangeStillRefusesAtBothExecWindows(t *testing.T) {
	for _, stage := range []string{"reserve", "confirm"} {
		t.Run(stage, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			handler := ex.handle
			answers := 0
			var conflicting v1alpha1.DecisionAnswerStatus
			ex.handle = func(cmd []string, input string) (string, int) {
				out, code := handler(cmd, input)
				isAnswer := cmd[2] == "decision-answer"
				if isAnswer {
					answers++
				}
				if (stage == "reserve" && !isAnswer) || (stage == "confirm" && isAnswer) {
					saved := f.session(sess.Name)
					if saved.Status.DecisionAnswer != nil {
						conflicting = *saved.Status.DecisionAnswer
					} else {
						var record protocol.DecisionResult
						_ = json.Unmarshal([]byte(out), &record)
						conflicting = v1alpha1.DecisionAnswerStatus(protocol.AnswerAuthority(*record.Decision, "DIFFERENT-ANSWER"))
						conflicting.Phase = "Reserved"
					}
					conflicting.Digest = strings.Repeat("b", 64)
					saved.Status.DecisionAnswer = &conflicting
					if err := f.c.Status().Update(context.Background(), saved); err != nil {
						t.Fatal(err)
					}
				}
				return out, code
			}
			answer := protocol.DecisionAnswer{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", Text: "SYNTHETIC-ANSWER"}
			w := f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			saved := f.session(sess.Name)
			want := 0
			if stage == "confirm" {
				want = 1
			}
			if w.Code != http.StatusConflict || answers != want || saved.Status.DecisionAnswer == nil || *saved.Status.DecisionAnswer != conflicting {
				t.Fatal("authority CAS accepted or overwrote a concurrent answer", w.Code, answers)
			}
			_ = f.do(http.MethodPost, apiv1.SessionDecisionPath(sess.Name), tokCoordinator, answer)
			if answers != want {
				t.Fatal("concurrent authority conflict replayed answer exec")
			}
		})
	}
}
