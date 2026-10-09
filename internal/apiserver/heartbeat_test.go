package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

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
			case "wrong-session", "wrong-pod", "zero-generation", "malformed-id", "future":
				wantError(t, w, http.StatusUnprocessableEntity, apiv1.CodeInvalid)
				if f.session(name).Status.Outcome != nil {
					t.Fatal("foreign decision outcome persisted")
				}
			case "valid":
				if w.Code != http.StatusNoContent {
					t.Fatal(w.Code, w.Body.String())
				}
				got := f.session(name).Status.Outcome
				if got == nil || got.State != v1alpha1.OutcomeEscalated || got.Note != "decision/"+st.Decision.ID {
					t.Fatal("existing escalated Outcome did not discover exact question")
				}
				raw, _ := json.Marshal(got)
				if strings.Contains(string(raw), "question") || strings.Contains(string(raw), "answer") || strings.Contains(string(raw), "context") {
					t.Fatal("private decision context escaped into status")
				}
				st.Decision = nil
				if f.do(http.MethodPost, protocol.HeartbeatPath(name), token, st).Code != http.StatusNoContent || f.session(name).Status.Outcome != nil {
					t.Fatal("completed decision outcome did not clear")
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
