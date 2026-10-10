package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func status(name string) protocol.Status {
	at := time.Date(2026, 10, 6, 17, 20, 0, 0, time.UTC)
	return protocol.Status{
		Session:  name,
		Agentd:   "agentd 2.0.0 (abc1234)",
		BootID:   "b1",
		BootedAt: at,
		Boot:     protocol.BootReady,
		Problems: []protocol.Step{{Name: "mcp", State: "warn", Notes: []string{"vexa: timed out"}}},
		Workspace: &protocol.Workspace{
			Clone: "/home/dev/repos/haynes-ops", Worktree: "/home/dev/work/" + name,
			Branch: "agent/" + name, Head: "0123456789abcdef",
		},
		Agent: protocol.AgentState{
			State: protocol.AgentExited, ConversationID: "9b1c", StartedAt: &at, LastActivity: &at,
			Task: &protocol.TaskResult{ExitCode: 0, FinishedAt: at.Add(time.Minute), Subtype: "success", NumTurns: 12},
		},
		Usage:      &protocol.Usage{CostUSD: 0.4217, InputTokens: 1200, OutputTokens: 3400},
		ObservedAt: at,
	}
}

func TestHeartbeat(t *testing.T) {
	f := newFixture(t)
	const name = "haynes-ops-1006-100000"
	tok := f.sessionPod(name, "full", 0)
	other := f.sessionPod("haynes-ops-1006-100001", "full", 0)
	path := protocol.HeartbeatPath(name)

	w := f.do(http.MethodPost, path, tok, status(name))
	if w.Code != http.StatusNoContent || w.Body.Len() != 0 {
		t.Fatalf("heartbeat: %d %s", w.Code, w.Body.String())
	}
	s := f.session(name)
	a := s.Status.Agent
	if a == nil || a.Status != protocol.AgentExited || a.Boot != protocol.BootReady || a.Branch != "agent/"+name ||
		a.Head != "0123456789abcdef" || a.ConversationID != "9b1c" || a.Agentd == "" {
		t.Fatalf("status.agent %+v", a)
	}
	if !a.LastHeartbeat.Time.Equal(f.now) || a.LastActivity == nil {
		t.Errorf("times %v %v", a.LastHeartbeat, a.LastActivity)
	}
	if len(a.Problems) != 1 || a.Problems[0] != "mcp: warn: vexa: timed out" {
		t.Errorf("problems %q", a.Problems)
	}
	if a.Task == nil || a.Task.Subtype != "success" || a.Task.NumTurns != 12 {
		t.Errorf("task %+v", a.Task)
	}
	if u := s.Status.Usage; u == nil || u.CostUSD != "0.4217" || u.InputTokens != 1200 || u.OutputTokens != 3400 {
		t.Errorf("usage %+v", u)
	}

	// The next beat clears what it no longer reports.
	next := status(name)
	next.Problems, next.Agent.Task = nil, nil
	next.Agent.State, next.Agent.Error = protocol.AgentFailed, "claude: not found"
	if w := f.do(http.MethodPost, path, tok, next); w.Code != http.StatusNoContent {
		t.Fatalf("second heartbeat: %d %s", w.Code, w.Body.String())
	}
	a = f.session(name).Status.Agent
	if a.Problems != nil || a.Task != nil || a.Message != "claude: not found" || a.Status != protocol.AgentFailed {
		t.Errorf("after the second beat %+v", a)
	}

	// A newer agentd's extra fields are ignored, not refused.
	if w := f.do(http.MethodPost, path, tok, `{"session":"`+name+`","boot":"ready","agent":{"state":"busy"},"fromTheFuture":1}`); w.Code != http.StatusNoContent {
		t.Errorf("unknown fields: %d %s", w.Code, w.Body.String())
	}

	// Only the session's own pod.
	wantError(t, f.do(http.MethodPost, path, other, status(name)), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodPost, path, tokHuman, status(name)), http.StatusForbidden, apiv1.CodeForbidden)
	wantError(t, f.do(http.MethodPost, path, tok, status("haynes-ops-1006-100001")), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
	wantError(t, f.do(http.MethodPost, path, "", status(name)), http.StatusUnauthorized, apiv1.CodeUnauthenticated)
}

func TestHeartbeatCaps(t *testing.T) {
	st := status("s")
	st.Agent.Error = strings.Repeat("é", maxMessage)
	for i := 0; i < 40; i++ {
		st.Problems = append(st.Problems, protocol.Step{Name: "step", State: "fail"})
	}
	st.Usage.CostUSD = -1
	a := agentStatus(st, metav1.Now())
	if len(a.Message) > maxMessage+len("…") || !strings.HasSuffix(a.Message, "…") {
		t.Errorf("message of %d bytes", len(a.Message))
	}
	if len(a.Problems) != maxProblems {
		t.Errorf("%d problems", len(a.Problems))
	}
	if u := usageStatus(*st.Usage); u.CostUSD != "0" {
		t.Errorf("a negative cost became %q", u.CostUSD)
	}
	if u := usageStatus(protocol.Usage{CostUSD: 0.0000001}); u.CostUSD != "0.0000001" {
		t.Errorf("a small cost became %q, which the schema's pattern refuses", u.CostUSD)
	}
}

func TestDecisionHeartbeatDiscoveryUsesExistingOutcomeWithoutPrivateContext(t *testing.T) {
	for _, change := range []string{"valid", "disabled", "private", "claude", "wrong-session", "wrong-pod", "zero-generation", "malformed-id", "future"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t)
			const name = "decision-task"
			token := f.sessionPod(name, "dev", 0)
			sess := f.session(name)
			sess.Spec.Agent = v1alpha1.AgentCodex
			sess.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "projects"}
			switch change {
			case "private":
				sess.Spec.Workspace = nil
			case "claude":
				sess.Spec.Agent = v1alpha1.AgentClaude
			}
			if err := f.c.Update(context.Background(), sess); err != nil {
				t.Fatal(err)
			}
			var pod corev1.Pod
			if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: name}, &pod); err != nil {
				t.Fatal(err)
			}
			f.srv.ManagedChildDecisions = change != "disabled"
			st := status(name)
			st.Decision = &protocol.DecisionOutcome{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SessionUID: string(sess.UID), PodUID: string(pod.UID), WriterGeneration: 3, At: f.now}
			switch change {
			case "wrong-session":
				st.Decision.SessionUID = "changed"
			case "wrong-pod":
				st.Decision.PodUID = "changed"
			case "zero-generation":
				st.Decision.WriterGeneration = 0
			case "malformed-id":
				st.Decision.ID = strings.Repeat("x", 36)
			case "future":
				st.Decision.At = f.now.Add(time.Second)
			}
			w := f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st)
			switch change {
			case "wrong-session", "wrong-pod", "zero-generation", "malformed-id":
				wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
				if f.session(name).Status.Outcome != nil {
					t.Fatal("foreign decision outcome persisted")
				}
			case "valid", "future":
				if w.Code != http.StatusNoContent {
					t.Fatal(w.Code, w.Body.String())
				}
				got := f.session(name).Status.Outcome
				if got == nil || got.State != v1alpha1.OutcomeEscalated || got.Note != "decision/"+st.Decision.ID || !got.At.Time.Equal(f.now) {
					t.Fatal("existing escalated Outcome did not discover exact question")
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), "question") || strings.Contains(string(raw), "answer") || strings.Contains(string(raw), "context") {
					t.Fatal("private decision context escaped into status")
				}
				st.Decision = nil
				if f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st).Code != http.StatusNoContent || f.session(name).Status.Outcome == nil {
					t.Fatal("omitted decision cleared pending escalation")
				}
				saved := f.session(name)
				saved.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeEscalated, Note: "unrelated escalation"}
				if err := f.c.Status().Update(context.Background(), saved); err != nil {
					t.Fatal(err)
				}
				if f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st).Code != http.StatusNoContent || f.session(name).Status.Outcome.Note != "unrelated escalation" {
					t.Fatal("decision heartbeat cleared unrelated Outcome")
				}
			default:
				if w.Code != http.StatusNoContent || f.session(name).Status.Outcome != nil {
					t.Fatal("disabled/inapplicable decision changed legacy status")
				}
			}
		})
	}
}

func TestDecisionHeartbeatsPreserveUnrelatedOutcomesAndChangeOnlyOwnedNote(t *testing.T) {
	for _, state := range []v1alpha1.OutcomeState{v1alpha1.OutcomeEscalated, v1alpha1.OutcomeDone} {
		for _, note := range []string{"unrelated outcome", "decision/cccccccc-cccc-4ccc-8ccc-cccccccccccc", "decision/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", "absent-outcome"} {
			for _, decisionState := range []string{"Open", "Delivered", "Omitted"} {
				t.Run(string(state)+"/"+note+"/"+decisionState, func(t *testing.T) {
					f, ex, sess, _ := decisionAPIFixture(t)
					var record protocol.DecisionResult
					_ = json.Unmarshal([]byte(ex.out), &record)
					at := record.Decision.CreatedAt.Add(time.Second)
					record.Decision.State, record.Decision.Answer, record.Decision.AnsweredAt = "Delivered", "SYNTHETIC-ANSWER", &at
					authority := v1alpha1.DecisionAnswerStatus(protocol.AnswerAuthority(*record.Decision, record.Decision.Answer))
					saved := f.session(sess.Name)
					saved.Status.DecisionAnswer = &authority
					original := &v1alpha1.OutcomeStatus{State: state, Note: note}
					if note != "absent-outcome" {
						saved.Status.Outcome = original
					}
					if err := f.c.Status().Update(context.Background(), saved); err != nil {
						t.Fatal(err)
					}
					st := status(sess.Name)
					if decisionState != "Omitted" {
						st.Decision = &protocol.DecisionOutcome{ID: record.Decision.ID, SessionUID: record.Decision.SessionUID, PodUID: record.Decision.PodUID, ThreadID: record.Decision.ThreadID, WriterGeneration: record.Decision.WriterGeneration, State: decisionState, AnswerDigest: authority.Digest, At: f.now}
					}
					w := f.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), "tok-session-"+sess.Name, st)
					if w.Code != http.StatusNoContent {
						t.Fatal("valid decision heartbeat refused", w.Code)
					}
					got := f.session(sess.Name).Status.Outcome
					owned := note == "absent-outcome" || note == "decision/"+record.Decision.ID
					if !owned || decisionState == "Omitted" {
						if note == "absent-outcome" {
							if got != nil {
								t.Fatal("omission created outcome")
							}
						} else if got == nil || *got != *original {
							t.Fatal("heartbeat changed an unrelated or omitted outcome")
						}
					} else if decisionState == "Delivered" {
						if got != nil {
							t.Fatal("matching confirmed delivered decision did not clear its own outcome")
						}
					} else if got == nil || got.State != v1alpha1.OutcomeEscalated || got.Note != "decision/"+record.Decision.ID || !got.At.Time.Equal(f.now) {
						t.Fatal("open decision did not update its own outcome")
					}
				})
			}
		}
	}
}

func TestDecisionHeartbeatCannotClobberOutcomeAcquiredAfterLiveRead(t *testing.T) {
	for _, state := range []string{"Open", "Delivered"} {
		t.Run(state, func(t *testing.T) {
			f, ex, sess, _ := decisionAPIFixture(t)
			var record protocol.DecisionResult
			_ = json.Unmarshal([]byte(ex.out), &record)
			at := record.Decision.CreatedAt.Add(time.Second)
			record.Decision.State, record.Decision.Answer, record.Decision.AnsweredAt = "Delivered", "SYNTHETIC-ANSWER", &at
			authority := v1alpha1.DecisionAnswerStatus(protocol.AnswerAuthority(*record.Decision, record.Decision.Answer))
			saved := f.session(sess.Name)
			saved.Status.DecisionAnswer = &authority
			if err := f.c.Status().Update(context.Background(), saved); err != nil {
				t.Fatal(err)
			}
			concurrent := &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone, Note: "concurrent unrelated completion"}
			conflicted := false
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				fresh := f.session(sess.Name)
				fresh.Status.Outcome = concurrent
				if err := f.c.Status().Update(ctx, fresh); err != nil {
					return err
				}
				err := c.SubResource(sub).Patch(ctx, obj, patch, opts...)
				conflicted = apierrors.IsConflict(err)
				return err
			}})
			st := status(sess.Name)
			st.Decision = &protocol.DecisionOutcome{ID: record.Decision.ID, SessionUID: record.Decision.SessionUID, PodUID: record.Decision.PodUID, ThreadID: record.Decision.ThreadID, WriterGeneration: record.Decision.WriterGeneration, State: state, AnswerDigest: authority.Digest, At: f.now}
			w := f.do(http.MethodPost, protocol.HeartbeatPath(sess.Name), "tok-session-"+sess.Name, st)
			got := f.session(sess.Name).Status.Outcome
			// The existing Kubernetes error mapper reports status-patch conflicts
			// as internal errors; the write must still refuse and preserve ownership.
			if !conflicted || w.Code != http.StatusInternalServerError || got == nil || *got != *concurrent {
				t.Fatal("stale heartbeat clobbered concurrently acquired outcome", w.Code)
			}
		})
	}
}
