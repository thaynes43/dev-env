package apiserver

import (
	"context"
	"encoding/json"
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
	f.srv.Exec, f.srv.ManagedChildDecisions = ex, true
	return f, ex, sess, &pod
}

func TestDecisionAPIRoutesBindDirectChildAndTargetUIDs(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, change := range []string{"current", "disabled", "human", "client", "foreign-parent", "session-uid", "session-version", "parent", "pod-uid", "pod-owner", "pod-service-account"} {
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
					var response protocol.DecisionResult
					if json.Unmarshal([]byte(ex.out), &response) != nil {
						t.Fatal("invalid synthetic record")
					}
					at := response.Decision.CreatedAt.Add(time.Second)
					response.Decision.State, response.Decision.AnsweredAt, response.Decision.Answer = "Answered", &at, "recorded synthetic answer"
					b, _ := json.Marshal(response)
					ex.out = string(b)
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
				if w.Code != want || len(ex.cmds) != 1 || !strings.Contains(ex.cmds[0], command) || !strings.HasSuffix(ex.cmds[0], "--expected-pod-uid "+string(pod.UID)+" --expected-session-uid "+string(sess.UID)) {
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
