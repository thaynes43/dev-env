package agentrun

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestReportOwnSessionVerbs(t *testing.T) {
	for _, verb := range []string{"working", "done", "failed", "escalate"} {
		t.Run(verb, func(t *testing.T) {
			h := newHarness(t)
			h.vars["HOSTNAME"] = "rem-own-session"
			h.vars[envAgentdTokenFile] = h.write("projected-token", testToken)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/sessions/rem-own-session/outcome" {
					t.Errorf("request %s %s", r.Method, r.URL.Path)
				}
				var got map[string]string
				if err := json.Unmarshal(body, &got); err != nil || got["state"] != verb || got["note"] != "work report" {
					t.Errorf("body %s", body)
				}
				state := verb
				if verb == "working" {
					state = "running"
				}
				if verb == "escalate" {
					state = "escalated"
				}
				writeTestJSON(w, 200, apiv1.Outcome{State: state, Note: "work report", At: &h.now})
			}
			h.mustRun(ExitOK, "report", verb, "--note", "work report")
			if len(h.api.requests()) != 1 {
				t.Fatal("report must send one request")
			}
			contains(t, "report", h.stdout.String(), "work report")
		})
	}
}

func TestReportRequiresPodAndValidVerb(t *testing.T) {
	for _, args := range [][]string{{"report", "done"}, {"report", "invalid"}, {"report"}, {"report", "done", "other"}} {
		h := newHarness(t)
		want := ExitUsage
		if len(args) == 2 && args[1] == "done" {
			want = ExitAuth
		}
		h.mustRun(want, args...)
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid report sent a request")
		}
	}
}

func TestReportHelpAndShowOutcome(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "report")
	contains(t, "report help", h.stdout.String(), "own pod", "working", "done", "failed", "escalate", "1024", "1007", "409", "C0", "C1", "DEL", "Cf", "newline", "tab", "outcome is final:", "outcome write kept conflicting; read the session before retrying")
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, 200, apiv1.Session{Name: "rem-own-session", Outcome: &apiv1.Outcome{State: "escalated", Note: "needs an owner"}})
	}
	h.mustRun(ExitOK, "show", "rem-own-session")
	contains(t, "show outcome", h.stdout.String(), "escalated", "needs an owner")
}

func TestReportConflictPrintsStoredOutcome(t *testing.T) {
	h := newHarness(t)
	h.vars["HOSTNAME"] = "rem-own-session"
	h.vars[envAgentdTokenFile] = h.write("projected-token", testToken)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusConflict, apiv1.Outcome{State: "escalated", Note: "unreported: agent-failed", At: &h.now})
	}
	h.mustRun(ExitFailed, "report", "done")
	contains(t, "stored conflict outcome", h.stderr.String()+h.stdout.String(), "escalated", "unreported: agent-failed")
	if len(h.api.requests()) != 1 {
		t.Fatal("conflict was retried")
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusConflict, apiv1.ErrorResponse{Error: apiv1.Error{Code: apiv1.CodeConflict, Message: "outcome write kept conflicting"}})
	}
	h.mustRun(ExitFailed, "report", "done")
	contains(t, "ordinary conflict", h.stderr.String(), "outcome write kept conflicting")
}

func TestReportRefusalExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status, exit int
		code         string
		message      string
	}{
		{http.StatusForbidden, ExitAuth, apiv1.CodeForbidden, "only the session's own pod may report"},
		{http.StatusUnprocessableEntity, ExitFailed, apiv1.CodeInvalid, "note must not contain control or format characters"},
	} {
		t.Run(tc.message, func(t *testing.T) {
			h := newHarness(t)
			h.vars["HOSTNAME"] = "rem-own-session"
			h.vars[envAgentdTokenFile] = h.write("projected-token", testToken)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				writeTestJSON(w, tc.status, apiv1.ErrorResponse{Error: apiv1.Error{Code: tc.code, Message: tc.message}})
			}
			h.mustRun(tc.exit, "report", "done")
			contains(t, "refusal", h.stderr.String(), tc.message)
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Fatal("refused report retried or printed a confirmed outcome")
			}
		})
	}
}

func TestReportNoAnswerIsUnconfirmed(t *testing.T) {
	h := newHarness(t)
	h.vars["HOSTNAME"] = "rem-own-session"
	h.vars[envAgentdTokenFile] = h.write("projected-token", testToken)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}
	h.mustRun(ExitUnconfirmed, "report", "done")
	contains(t, "unconfirmed report", h.stderr.String(), "no confirmed outcome response", "read agent-run show before retrying")
	if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
		t.Fatal("unconfirmed report retried or printed a confirmed outcome")
	}
}
