package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessionraise"
)

func TestRaiseCommandSendsTheUIDAndTheWords(t *testing.T) {
	h := newHarness(t)
	h.vars[envAgentdTokenFile] = h.write("projected-token", "tok")
	cur := testSession(name, "Running")
	cur.UID = "uid-1"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			writeTestJSON(w, http.StatusOK, cur)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionRaisePath(name):
			var req apiv1.RaiseRequest
			if err := json.Unmarshal(body, &req); err != nil || req.ExpectUID != "uid-1" || req.Profile != "workbench" || req.Owner.Words != "yes please" || req.Owner.Where != sessionraise.WherePhone || req.Owner.SaidAt != "2026-11-13T18:00:00Z" || req.Owner.Ref != "op:830" {
				t.Errorf("post %s", body)
			}
			cur.Raise = &apiv1.SessionRaise{State: sessionraise.StatePending, Profile: "workbench"}
			writeTestJSON(w, http.StatusAccepted, cur)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
		}
	}
	h.mustRun(ExitOK, "raise", name, "--profile", "workbench", "--words", "yes please", "--said-at", "2026-11-13T18:00:00Z", "--ref", "op:830")
	if !strings.Contains(h.stdout.String(), "two minutes") {
		t.Fatalf("stdout: %s", h.stdout.String())
	}
}

func TestRaiseCommandTakeBack(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	cur.UID = "uid-1"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			writeTestJSON(w, http.StatusOK, cur)
		case r.Method == http.MethodDelete && r.URL.Path == apiv1.SessionRaisePath(name):
			var req apiv1.TakeBackRequest
			if err := json.Unmarshal(body, &req); err != nil || req.ExpectUID != "uid-1" {
				t.Errorf("delete %s", body)
			}
			writeTestJSON(w, http.StatusOK, cur)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
		}
	}
	h.mustRun(ExitOK, "raise", name, "--take-back")
	if !strings.Contains(h.stdout.String(), "Took back") {
		t.Fatalf("stdout: %s", h.stdout.String())
	}
}

func TestRaiseCommandHasNoRestart(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		t.Errorf("restart called the API: %s %s", r.Method, r.URL.Path)
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
	}
	h.mustRun(ExitUsage, "raise", name, "--restart")
}

func TestRaiseHelpFits(t *testing.T) {
	text := commandHelp["raise"]
	body := text
	if i := strings.Index(text, "\n\n"); i >= 0 {
		body = text[i+2:]
	}
	n := len(strings.Fields(body))
	if n == 0 || n > 80 {
		t.Fatalf("help words: %d\n%s", n, body)
	}
	if !strings.Contains(text, "does not restart") {
		t.Fatal("help does not say this build does not restart")
	}
}

func TestRaiseShowListAndFleet(t *testing.T) {
	s := apiv1.Session{Name: "raised", Profile: "full", Raise: &apiv1.SessionRaise{
		State: sessionraise.StateActive, Profile: "workbench", StartSessions: true,
		PerformedBy: "session/bench", PerformedAt: "2026-11-13T19:00:00Z", ActiveFrom: "2026-11-13T19:02:00Z",
		Owner:      &apiv1.RaiseOwner{Words: "yes", Where: sessionraise.WherePhone, Session: "bench", SaidAt: "2026-11-13T18:55:00Z"},
		PodAccount: "dev-env-agent",
	}}
	var lines []string
	row := func(k, format string, args ...any) {
		lines = append(lines, k)
	}
	(&app{}).printRaise(row, s)
	got := strings.Join(lines, " ")
	if !strings.Contains(got, "raised") || !strings.Contains(got, "on Tom's word") || !strings.Contains(got, "pod runs as") {
		t.Fatalf("show labels: %s", got)
	}
	if note(s) != "raised: workbench; raised: may start sessions" {
		t.Fatalf("note: %s", note(s))
	}
	plain := testSession("plain", "Running")
	if strings.Contains(note(plain), "raise") {
		t.Fatalf("unraised note: %s", note(plain))
	}
	header := fleetRaised([]apiv1.FleetSession{{Name: "raised", Raise: s.Raise}, {Name: "plain"}})
	if header != "raised (workbench); raised (may start sessions)" {
		t.Fatalf("fleet: %s", header)
	}
}
