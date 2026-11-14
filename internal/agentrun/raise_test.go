package agentrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"text/tabwriter"
	"time"

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
		lines = append(lines, k+": "+fmt.Sprintf(format, args...))
	}
	(&app{}).printRaise(row, s)
	got := strings.Join(lines, " ")
	if !strings.Contains(got, "raised") || !strings.Contains(got, "on Tom's word") || !strings.Contains(got, "pod runs as") || !strings.Contains(got, "--restart") {
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
	if header != "raised (workbench), raised (may start sessions)" {
		t.Fatalf("fleet: %s", header)
	}
	lines = nil
	s.Raise.Profile = ""
	s.Raise.PodAccount = ""
	(&app{}).printRaise(row, s)
	startOnly := strings.Join(lines, " ")
	if strings.Contains(startOnly, "pod runs as") || strings.Contains(startOnly, "--restart") || !strings.Contains(startOnly, "raised") {
		t.Fatalf("start-sessions show: %s", startOnly)
	}
	lines = nil
	s.Raise.Owner.Session = ""
	s.Raise.Owner.Where = sessionraise.WhereOwn
	(&app{}).printRaise(row, s)
	own := strings.Join(lines, " ")
	if strings.Contains(own, "in ,") {
		t.Fatalf("empty session printed: %s", own)
	}
	if !strings.Contains(own, "Tom's own call,") {
		t.Fatalf("own call: %s", own)
	}
}

func TestRaiseAllowsProfileAndStartSessionsTogether(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	cur.UID = "uid-1"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			writeTestJSON(w, http.StatusOK, cur)
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionRaisePath(name):
			var req apiv1.RaiseRequest
			if err := json.Unmarshal(body, &req); err != nil || req.Profile != "workbench" || !req.StartSessions {
				t.Errorf("post %s", body)
			}
			writeTestJSON(w, http.StatusAccepted, cur)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
		}
	}
	h.mustRun(ExitUsage, "raise", name, "--words", "yes please", "--said-at", "2026-11-13T18:00:00Z")
	h.mustRun(ExitOK, "raise", name, "--profile", "workbench", "--start-sessions", "--words", "yes please", "--said-at", "2026-11-13T18:00:00Z")
}

func TestNoRaisePrintsTheAPISentence(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	cur.UID = "uid-1"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name) {
			writeTestJSON(w, http.StatusOK, cur)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "Session "+name+" has no raise to take back.")
	}
	h.mustRun(ExitNotFound, "raise", name, "--take-back")
	if strings.Contains(h.stderr.String(), "does not have the raise routes") || !strings.Contains(h.stderr.String(), "has no raise to take back") {
		t.Fatalf("stderr: %s", h.stderr.String())
	}
	h.stderr.Reset()
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name) {
			writeTestJSON(w, http.StatusOK, cur)
			return
		}
		http.Error(w, "404 page not found", http.StatusNotFound)
	}
	h.mustRun(ExitNotFound, "raise", name, "--take-back")
	if !strings.Contains(h.stderr.String(), "This operator does not have the raise routes.") {
		t.Fatalf("bare 404: %s", h.stderr.String())
	}
	h.stderr.Reset()
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name) {
			writeTestJSON(w, http.StatusOK, cur)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
	}
	h.mustRun(ExitNotFound, "raise", name, "--take-back")
	if !strings.Contains(h.stderr.String(), "This operator does not have the raise routes.") {
		t.Fatalf("json no route: %s", h.stderr.String())
	}
}

func TestShowOfAnUnraisedSessionMatchesThePlainProfile(t *testing.T) {
	h := newHarness(t)
	s := testSession("plain", "Running")
	s.Profile = "full"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath("plain") {
			writeTestJSON(w, http.StatusOK, s)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
	}
	h.mustRun(ExitOK, "show", "plain")
	got := h.stdout.Bytes()
	want := showWithoutRaise(s)
	if !bytes.Equal(got, want) {
		t.Fatalf("show differs from the output without this feature:\n got:\n%s\nwant:\n%s", got, want)
	}
}

// showWithoutRaise is the show text for a session that was never raised: the
// lines show printed before this feature, with no raise row and no
// "(at creation)" on the profile.
func showWithoutRaise(s apiv1.Session) []byte {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 1, ' ', 0)
	row := func(k, format string, args ...any) {
		_, _ = fmt.Fprintf(tw, "%s:\t%s\n", k, fmt.Sprintf(format, args...))
	}
	row("name", "%s", s.Name)
	row("phase", "%s", s.Phase)
	row("repo", "%s from %s", s.Repo, "origin/main")
	row("agent", "%s %s, %s, %s mode", s.Agent, s.Model, effortText(s.Effort), s.Mode)
	row("size", "%s", s.Size)
	row("profile", "%s", s.Profile)
	row("parent", "%s", s.Parent)
	row("created", "%s (%s ago)", s.CreatedAt.UTC().Format(time.RFC3339), age(testNow, s.CreatedAt))
	row("Task claims", "Unknown (%s)", "NotReported")
	row("Actual model", "%s", "Unknown")
	row("Requested model", "%s", s.Model)
	row("Initial task cost", "%s", "Unknown")
	row("Input tokens", "%s", "Unknown")
	row("Output tokens", "%s", "Unknown")
	row("Cache read tokens", "%s", "Unknown")
	row("Cache create tokens", "%s", "Unknown")
	_ = tw.Flush()
	return buf.Bytes()
}

func TestShowSaysProfileAtCreation(t *testing.T) {
	h := newHarness(t)
	s := testSession("raised", "Running")
	s.Profile = "full"
	s.Raise = &apiv1.SessionRaise{
		State: sessionraise.StateActive, Profile: "workbench",
		PerformedBy: "human/tom", PerformedAt: "2026-11-13T19:00:00Z", ActiveFrom: "2026-11-13T19:02:00Z",
		Owner: &apiv1.RaiseOwner{Words: "yes", Where: sessionraise.WhereOwn, SaidAt: "2026-11-13T18:55:00Z"},
	}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath("raised") {
			writeTestJSON(w, http.StatusOK, s)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
	}
	h.mustRun(ExitOK, "show", "raised")
	if !strings.Contains(h.stdout.String(), "full (at creation)") || strings.Contains(h.stdout.String(), "in ,") || !strings.Contains(h.stdout.String(), "active from 2026-11-13T19:02:00Z") {
		t.Fatalf("show:\n%s", h.stdout.String())
	}
}
