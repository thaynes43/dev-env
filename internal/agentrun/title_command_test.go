package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestTitleCommandReadsExpectThenPuts(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	cur.Title = "Old"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			writeTestJSON(w, http.StatusOK, cur)
		case r.Method == http.MethodPut && r.URL.Path == apiv1.SessionTitlePath(name):
			var req struct {
				Title  string `json:"title"`
				Expect string `json:"expect"`
			}
			if err := json.Unmarshal(body, &req); err != nil || req.Title != "Cafe" || req.Expect != "Old" {
				t.Errorf("put %s", body)
			}
			cur.Title = req.Title
			writeTestJSON(w, http.StatusOK, cur)
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
		}
	}
	h.mustRun(ExitOK, "title", name, "Cafe")
	contains(t, "stdout", h.stdout.String(), "Opus | Cafe\nIt is typed when the session is idle with an empty input line.")
	puts := 0
	for _, r := range h.api.requests() {
		if r.method == http.MethodPut {
			puts++
		}
	}
	if puts != 1 || len(h.api.requests()) != 2 {
		t.Fatalf("requests %+v", h.api.requests())
	}
}

func TestTitleCommandClear(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	cur.Title = "Old"
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method == http.MethodGet {
			writeTestJSON(w, http.StatusOK, cur)
			return
		}
		var req struct {
			Title  string `json:"title"`
			Expect string `json:"expect"`
		}
		if json.Unmarshal(body, &req) != nil || req.Title != "" || req.Expect != "Old" {
			t.Errorf("clear %s", body)
		}
		cur.Title = ""
		writeTestJSON(w, http.StatusOK, cur)
	}
	h.mustRun(ExitOK, "title", name, "--clear")
	contains(t, "stdout", h.stdout.String(), name+"\nIt is typed when the session is idle with an empty input line.")
}

func TestTitleCommandExplainsTitleChanged(t *testing.T) {
	h := newHarness(t)
	cur := testSession(name, "Running")
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet {
			writeTestJSON(w, http.StatusOK, cur)
			return
		}
		apiError(w, http.StatusConflict, apiv1.CodeConflict, "title_changed: the stored title is not the one this request read, so nothing was written")
	}
	h.mustRun(ExitFailed, "title", name, "Cafe")
	if !strings.Contains(h.stderr.String(), "title_changed") || strings.Contains(strings.ToLower(h.stderr.String()), "try again") || strings.Contains(strings.ToLower(h.stderr.String()), "retry") {
		t.Fatalf("stderr %q", h.stderr.String())
	}
}

func TestTitleCommandMissingRouteDoesNothingElse(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet {
			writeTestJSON(w, http.StatusOK, testSession(name, "Running"))
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such route")
	}
	h.mustRun(ExitFailed, "title", name, "Cafe")
	contains(t, "stderr", h.stderr.String(), "this operator has no session title route; nothing was changed")
	puts := 0
	for _, r := range h.api.requests() {
		if r.method == http.MethodPut {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("puts %d", puts)
	}
}

func TestTitleCommandSessionNotFound(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "session "+name+": not found")
	}
	h.mustRun(ExitNotFound, "title", name, "Cafe")
	if h.stderr.String() != "agent-run: no such session\n" || len(h.api.requests()) != 1 {
		t.Fatalf("stderr %q requests %d", h.stderr.String(), len(h.api.requests()))
	}
}

func TestTitleCommandUsage(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage, "title")
	h.mustRun(ExitUsage, "title", name)
	h.mustRun(ExitUsage, "title", name, "--clear", "Cafe")
	h.mustRun(ExitUsage, "title", name, "   ")
	h.mustRun(ExitUsage, "title", name, "bad|title")
	if len(h.api.requests()) != 0 {
		t.Fatalf("usage sent %d requests", len(h.api.requests()))
	}
}

func TestShowRenameLine(t *testing.T) {
	h := newHarness(t)
	s := runningOn(testSession(name, ""), "talosw02")
	s.Agent, s.Mode, s.Title = "claude", "remote", "Café"
	since := testNow.Add(-2 * time.Minute)
	s.RemoteControl = &apiv1.RemoteControl{
		Name: name, State: "registered", ProviderTitle: "Someone",
		Rename: &apiv1.RemoteRename{State: "Held", Reason: "busy", Since: since, Attempts: 1},
	}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "show", h.stdout.String(),
		"registered; recorded title Café; provider title Someone; held for 2m: busy; it is typed when the session is idle with an empty input line")
	if strings.Contains(h.stdout.String(), "claude.ai") {
		t.Fatal("show printed the link")
	}

	s.RemoteControl.Rename.State = "Applied"
	s.RemoteControl.Rename.Reason = "confirmed"
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "applied", h.stdout.String(), "provider title Someone; applied")

	s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Typed", Since: time.Date(2026, 11, 13, 14, 12, 0, 0, time.UTC)}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "typed", h.stdout.String(), "typed at 14:12Z, not yet confirmed")

	s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Failed", Reason: "left-in-input", Since: since}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "left", h.stdout.String(), "failed: the rename text is in the session's input line, unsent; its owner may send or delete it")

	s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Failed", Reason: "not-confirmed", Since: since}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "not-confirmed", h.stdout.String(), "failed: the provider did not confirm the rename")

	s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Failed", Reason: "invalid-title", Since: since}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "invalid", h.stdout.String(), "failed: the recorded title cannot be typed")

	s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Unknown", Reason: "record-shape", Since: since}
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "unknown", h.stdout.String(), "unknown: the CLI's record has a shape agentd does not know, so nothing is typed")

	for _, reason := range []string{"state", "command-shadowed", "interrupted"} {
		s.RemoteControl.Rename = &apiv1.RemoteRename{State: "Held", Reason: reason, Since: since}
		serveSession(h, s)
		h.mustRun(ExitOK, "show", name)
		contains(t, reason, h.stdout.String(), "held for 2m: "+reason+"; it is typed when the session is idle with an empty input line")
	}
	s.TitleRename = "off"
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "off", h.stdout.String(), "not enabled for this session")

	s.TitleRename = ""
	created := "Old"
	s.Title = "New"
	s.CreatedTitle = &created
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "created", h.stdout.String(), "title at create:")
	if !strings.Contains(h.stdout.String(), "Old") {
		t.Fatalf("show dropped the create-time title:\n%s", h.stdout.String())
	}

	s.Title = "Old"
	s.CreatedTitle = nil
	s.TitleIgnored = true
	serveSession(h, s)
	h.mustRun(ExitOK, "show", name)
	contains(t, "ignored", h.stdout.String(), "the title annotation was ignored")
}
