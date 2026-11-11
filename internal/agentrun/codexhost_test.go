package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func codexChoice(action, host, reason string) apiv1.CodexHostChoice {
	return apiv1.CodexHostChoice{Host: host, Action: action, Reason: reason}
}

func serveCodexChoice(choice apiv1.CodexHostChoice) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodPost && r.URL.Path == apiv1.CodexHostsSelectPath {
			writeTestJSON(w, http.StatusOK, choice)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
	}
}

func TestCodexHostSelectCommand(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.CodexHostsSelectPath {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
			return
		}
		var req apiv1.CodexHostSelectRequest
		if json.Unmarshal(body, &req) != nil || req.Repo != "dev-env" || req.Profile != "dev" {
			t.Errorf("select body %s", body)
		}
		if r.URL.Query().Get("check") != "true" {
			t.Error("check was not set")
		}
		writeTestJSON(w, http.StatusOK, codexChoice("use", "paired", "folder_trust_pending"))
	}
	h.mustRun(ExitOK, "codex-host", "select", "--repo", "dev-env", "--profile", "dev", "--check")
	var got apiv1.CodexHostChoice
	if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Action != "use" || got.Host != "paired" {
		t.Fatalf("stdout %s", h.stdout.String())
	}
	if len(h.api.requests()) != 1 {
		t.Fatalf("requests %d", len(h.api.requests()))
	}

	for _, action := range []string{"report", "resume", "create"} {
		h.api.handle = serveCodexChoice(codexChoice(action, "paired", "remote_unavailable"))
		h.mustRun(ExitOK, "codex-host", "select", "--repo", "dev-env")
		if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Action != action {
			t.Fatalf("%s stdout %s", action, h.stdout.String())
		}
	}

	h.api.handle = serveCodexChoice(codexChoice("", "", "login_required"))
	h.mustRun(ExitFailed, "codex-host", "select", "--repo", "dev-env")
	sentence, _ := apiv1.CodexHostSentence("login_required")
	contains(t, "stderr", h.stderr.String(), "login_required: "+sentence)
	if h.stdout.Len() != 0 {
		t.Fatalf("refusal wrote %s", h.stdout.String())
	}

	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no handler")
	}
	h.mustRun(ExitFailed, "codex-host", "select", "--repo", "dev-env")
	contains(t, "stderr", h.stderr.String(), "route_unsupported: "+apiv1.CodexHostRouteUnsupported)
}

func TestThreadStartWithoutHost(t *testing.T) {
	args := func(h *harness) []string {
		return []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	}
	t.Run("route absent", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun(ExitUsage, args(h)...)
		if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != "invalid_input" {
			t.Fatalf("result %+v", r.Error)
		}
		contains(t, "stderr", h.stderr.String(), hostSelectionOff)
		reqs := h.api.requests()
		if len(reqs) != 1 || reqs[0].path != apiv1.CodexHostsSelectPath {
			t.Fatalf("requests %+v", reqs)
		}
	})
	t.Run("use releases on every path", func(t *testing.T) {
		for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
			out := hostResult(t, apiv1.ThreadResponded, "")
			h, c := threadAPIHarness(t, nil)
			var release int
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				switch {
				case r.URL.Path == apiv1.CodexHostsSelectPath:
					writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, "folder_trust_pending"))
				case r.URL.Path == apiv1.CodexHostsReleasePath:
					release++
					var req apiv1.CodexHostReleaseRequest
					if json.Unmarshal(h.api.requests()[len(h.api.requests())-1].body, &req) != nil || req.Host != threadHostName {
						t.Errorf("release body")
					}
					writeTestJSON(w, http.StatusOK, apiv1.CodexHostReleaseResponse{Host: threadHostName, Released: true})
				case r.URL.Path == apiv1.SessionThreadsPath(threadHostName):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					_, _ = w.Write(out)
				default:
					apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
				}
			}
			want := ExitOK
			if status != http.StatusOK {
				want = ExitUnconfirmed
			}
			h.mustRun(want, args(h)...)
			if release != 1 || c.calls != 0 {
				t.Fatalf("status %d release %d execs %d", status, release, c.calls)
			}
		}
	})
	for _, tc := range []struct {
		action, sentence string
		exit             int
	}{
		{"report", "", ExitOK},
		{"resume", resumeNotBuilt, ExitFailed},
		{"create", commandCreatesNoHost, ExitFailed},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = serveCodexChoice(codexChoice(tc.action, "paired", "remote_unavailable"))
			h.mustRun(tc.exit, args(h)...)
			var got apiv1.CodexHostChoice
			if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Action != tc.action {
				t.Fatalf("stdout %s", h.stdout.String())
			}
			if tc.sentence != "" {
				contains(t, "stderr", h.stderr.String(), tc.sentence)
			}
			if len(h.api.requests()) != 1 {
				t.Fatalf("started something: %d requests", len(h.api.requests()))
			}
		})
	}
}

func TestInteractiveCodexSelect(t *testing.T) {
	args := []string{"haynes-ops", "--interactive", "--agent", "codex", "--model", "gpt-6-luna"}
	t.Run("create continues and omits the profile", func(t *testing.T) {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == apiv1.CodexHostsSelectPath:
				var req apiv1.CodexHostSelectRequest
				if json.Unmarshal(body, &req) != nil || req.Repo != "haynes-ops" || req.Profile != "" {
					t.Errorf("select %s", body)
				}
				writeTestJSON(w, http.StatusOK, codexChoice("create", "", ""))
			case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
				got := decodeCreate(h.t, body)
				if got.Profile != "" || got.Agent != "codex" || got.Mode != "remote" || got.Model != "gpt-6-luna" {
					t.Errorf("create %+v", got)
				}
				s := testSession(name, "Pending")
				w.Header().Set("Location", apiv1.SessionPath(name))
				writeTestJSON(w, http.StatusCreated, s)
			case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
				writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
			default:
				apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
			}
		}
		h.mustRun(ExitOK, args...)
		if strings.Contains(h.stderr.String(), hostSelectionOff) || strings.Contains(h.stdout.String(), `"action"`) {
			t.Fatalf("create printed the choice or the off line\n%s\n%s", h.stdout.String(), h.stderr.String())
		}
	})
	for _, action := range []string{"use", "resume", "report"} {
		t.Run(action, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = serveCodexChoice(codexChoice(action, "paired", "folder_trust_pending"))
			h.mustRun(ExitOK, args...)
			var got apiv1.CodexHostChoice
			if json.Unmarshal(h.stdout.Bytes(), &got) != nil || got.Action != action || got.Host != "paired" {
				t.Fatalf("stdout %s", h.stdout.String())
			}
			for _, req := range h.api.requests() {
				if req.path == apiv1.SessionsPath {
					t.Fatal("created a host")
				}
			}
		})
	}
}

func TestHeadlessCodexDoesNotSelect(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "--repo", "haynes-ops", "--agent", "codex", "--model", "gpt-6-luna", "-p", "fix the typo")
	if h.api.requests()[0].path != apiv1.SessionsPath {
		t.Fatalf("first request %s", h.api.requests()[0].path)
	}
	if strings.Contains(h.stderr.String(), "Host selection") {
		t.Fatal(h.stderr.String())
	}
}
