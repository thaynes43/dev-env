package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
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
	var launchID string
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.CodexHostsSelectPath {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
			return
		}
		var req apiv1.CodexHostSelectRequest
		if json.Unmarshal(body, &req) != nil || req.Repo != "dev-env" || req.Profile != "dev" || !apiv1.ValidCodexLaunchID(req.LaunchID) {
			t.Errorf("select body rejected")
		}
		launchID = req.LaunchID
		if r.URL.Query().Get("check") != "true" {
			t.Error("check was not set")
		}
		writeTestJSON(w, http.StatusOK, codexChoice("use", "paired", ""))
	}
	h.mustRun(ExitOK, "codex-host", "select", "--repo", "dev-env", "--profile", "dev", "--check")
	if launchID == "" || strings.Contains(h.stdout.String(), launchID) || strings.Contains(h.stderr.String(), launchID) {
		t.Fatal("the launch id was missing or printed")
	}
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
			var launchID string
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				switch {
				case r.URL.Path == apiv1.CodexHostsSelectPath:
					var req apiv1.CodexHostSelectRequest
					if json.Unmarshal(body, &req) != nil || !apiv1.ValidCodexLaunchID(req.LaunchID) {
						t.Errorf("select body rejected")
					}
					launchID = req.LaunchID
					writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, ""))
				case r.URL.Path == apiv1.CodexHostsReleasePath:
					release++
					var req apiv1.CodexHostReleaseRequest
					if json.Unmarshal(body, &req) != nil || req.Host != threadHostName || req.LaunchID != launchID || launchID == "" {
						t.Errorf("release body rejected")
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
				if json.Unmarshal(body, &req) != nil || req.Repo != "haynes-ops" || req.Profile != "" || !apiv1.ValidCodexLaunchID(req.LaunchID) || r.URL.Query().Get("check") != "true" {
					t.Errorf("select rejected")
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
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				if r.URL.Path == apiv1.CodexHostsSelectPath && r.URL.Query().Get("check") != "true" {
					t.Errorf("interactive select was not a check")
				}
				var req apiv1.CodexHostSelectRequest
				if json.Unmarshal(body, &req) != nil || !apiv1.ValidCodexLaunchID(req.LaunchID) {
					t.Errorf("select rejected")
				}
				writeTestJSON(w, http.StatusOK, codexChoice(action, "paired", ""))
			}
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

func TestCodexInteractiveRestoreDoesNotSelect(t *testing.T) {
	h := newHarness(t)
	id := rescueSession + "/20261006-2104"
	selected := false
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch {
		case r.URL.Path == apiv1.CodexHostsSelectPath:
			selected = true
			writeTestJSON(w, http.StatusOK, codexChoice("use", "paired", ""))
		case r.Method == http.MethodGet && r.URL.Path == apiv1.RescuesPath:
			writeTestJSON(w, http.StatusOK, apiv1.RescueList{Rescues: []apiv1.Rescue{rescueAt("20261006-2104", repoWith("haynes-ops", "refs/heads/agent/"+rescueSession))}})
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
			got := decodeCreate(h.t, body)
			if got.Restore != id || got.Agent != "codex" || got.Mode != "remote" {
				t.Errorf("create %+v", got)
			}
			writeTestJSON(w, http.StatusCreated, runningOn(testSession(name, ""), "talosw02"))
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
	h.mustRun(ExitOK, "rescue", "restore", id, "--interactive", "--agent", "codex", "--model", "gpt-6-luna", "--wait", "0")
	if selected {
		t.Fatal("a restore asked select")
	}
}

func TestThreadStartReleasesWhenCancelled(t *testing.T) {
	h, c := threadAPIHarness(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var launchID string
	var release int
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case apiv1.CodexHostsSelectPath:
			var req apiv1.CodexHostSelectRequest
			if json.Unmarshal(body, &req) != nil || !apiv1.ValidCodexLaunchID(req.LaunchID) {
				t.Errorf("select rejected")
			}
			launchID = req.LaunchID
			writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, ""))
		case apiv1.SessionThreadsPath(threadHostName):
			cancel()
		case apiv1.CodexHostsReleasePath:
			release++
			var req apiv1.CodexHostReleaseRequest
			if json.Unmarshal(body, &req) != nil || req.Host != threadHostName || req.LaunchID != launchID || launchID == "" {
				t.Errorf("release rejected")
			}
			writeTestJSON(w, http.StatusOK, apiv1.CodexHostReleaseResponse{Host: threadHostName, Released: true})
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
	args := []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	if code := Run(ctx, args, h.env); code == ExitOK || release != 1 || c.calls != 0 {
		t.Fatalf("exit %d release %d execs %d", code, release, c.calls)
	}
}

func TestThreadStartSelectionUnreachable(t *testing.T) {
	args := func(h *harness) []string {
		return []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	}
	exact := func(t *testing.T, h *harness, line string) {
		t.Helper()
		want, err := threadlaunch.Encode(threadlaunch.NewRecorder("codex", h.env.Now).Finish(apiv1.ThreadFailed, "invalid_input", apiv1.ThreadPhaseHostReady, 0))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(h.stdout.Bytes(), want) {
			t.Fatalf("stdout %q\nwant %q", h.stdout.Bytes(), want)
		}
		if h.stderr.String() != "agent-run: "+line+"\n" {
			t.Fatalf("stderr %q", h.stderr.String())
		}
		for _, req := range h.api.requests() {
			if strings.Contains(req.path, "/threads") {
				t.Fatalf("a thread was started: %+v", h.api.requests())
			}
		}
	}
	t.Run("no credentials", func(t *testing.T) {
		h := newHarness(t)
		h.vars[envTokenFile] = filepath.Join(h.dir, "missing-token")
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, selectionNotUsedNoCreds)
		if len(h.api.requests()) != 0 {
			t.Fatalf("requests %+v", h.api.requests())
		}
	})
	t.Run("transport error", func(t *testing.T) {
		h := newHarness(t)
		h.api.srv.Close()
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, selectionNotUsedUnreached)
	})
	t.Run("404", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, hostSelectionOff)
		reqs := h.api.requests()
		if len(reqs) != 1 || reqs[0].path != apiv1.CodexHostsSelectPath {
			t.Fatalf("requests %+v", reqs)
		}
	})
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path != apiv1.CodexHostsSelectPath {
					t.Errorf("unexpected %s", r.URL.Path)
				}
				apiError(w, status, apiv1.CodeInternal, "the operator failed this request")
			}
			h.mustRun(ExitUsage, args(h)...)
			exact(t, h, selectionNotUsedNoAnswer)
			if len(h.api.requests()) != 1 {
				t.Fatalf("requests %+v", h.api.requests())
			}
		})
	}
	t.Run("mint refused", func(t *testing.T) {
		h := newHarness(t)
		inPod(h, http.StatusForbidden)
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, selectionNotUsedUnreached)
	})
	t.Run("no kubectl", func(t *testing.T) {
		h := newHarness(t)
		h.vars = map[string]string{}
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, selectionNotUsedUnreached)
	})
	t.Run("port-forward", func(t *testing.T) {
		h, l := laptopHarness(t, operatorDNS)
		l.fail = "port-forward"
		h.mustRun(ExitUsage, args(h)...)
		exact(t, h, selectionNotUsedUnreached)
	})
}

func TestInteractiveSelectionFailures(t *testing.T) {
	args := []string{"haynes-ops", "--interactive", "--agent", "codex", "--model", "gpt-6-luna"}
	noCreate := func(t *testing.T, h *harness) {
		t.Helper()
		for _, req := range h.api.requests() {
			if req.path == apiv1.SessionsPath {
				t.Fatal("created a host")
			}
		}
	}
	t.Run("no credentials", func(t *testing.T) {
		h := newHarness(t)
		h.vars[envTokenFile] = filepath.Join(h.dir, "missing-token")
		h.mustRun(ExitAuth, args...)
		contains(t, "stderr", h.stderr.String(), "no token")
		noCreate(t, h)
		if len(h.api.requests()) != 0 {
			t.Fatalf("requests %+v", h.api.requests())
		}
	})
	t.Run("transport error", func(t *testing.T) {
		h := newHarness(t)
		h.api.srv.Close()
		h.mustRun(ExitRetry, args...)
		contains(t, "stderr", h.stderr.String(), "could not reach the API at "+h.api.srv.URL)
		if len(h.sleeps) != 2 {
			t.Fatalf("slept %d times, want the two retries create uses", len(h.sleeps))
		}
		noCreate(t, h)
	})
	t.Run("certificate", func(t *testing.T) {
		h := newHarness(t)
		delete(h.vars, envCAFile)
		h.mustRun(ExitAuth, args...)
		contains(t, "stderr", h.stderr.String(), "has a certificate agent-run does not trust")
		if len(h.sleeps) != 0 {
			t.Fatalf("a certificate failure was retried %d times", len(h.sleeps))
		}
		noCreate(t, h)
	})
	t.Run("503 is retried", func(t *testing.T) {
		h := newHarness(t)
		var selects int
		h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch {
			case r.URL.Path == apiv1.CodexHostsSelectPath:
				selects++
				if selects == 1 {
					apiError(w, http.StatusServiceUnavailable, apiv1.CodeUnavailable, "busy")
					return
				}
				writeTestJSON(w, http.StatusOK, codexChoice("create", "", ""))
			case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
				writeTestJSON(w, http.StatusCreated, runningOn(testSession(name, ""), "talosw02"))
			case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
				writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw02"))
			default:
				apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
			}
		}
		h.mustRun(ExitOK, args...)
		if selects < 2 {
			t.Fatalf("select attempts %d, want a retry of 503", selects)
		}
		var created bool
		for _, req := range h.api.requests() {
			if req.method == http.MethodPost && req.path == apiv1.SessionsPath {
				created = true
			}
		}
		if !created {
			t.Fatal("a retried select did not continue into create")
		}
	})
	t.Run("404", func(t *testing.T) {
		h := newHarness(t)
		serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
		h.mustRun(ExitOK, args...)
		contains(t, "stderr", h.stderr.String(), hostSelectionOff)
		var selected, created bool
		for _, req := range h.api.requests() {
			if req.path == apiv1.CodexHostsSelectPath {
				selected = true
				if len(req.query["check"]) != 1 || req.query["check"][0] != "true" {
					t.Fatal("the 404 select was not a check")
				}
			}
			if req.method == http.MethodPost && req.path == apiv1.SessionsPath {
				created = true
			}
		}
		if !selected || !created {
			t.Fatalf("selected %v created %v", selected, created)
		}
	})
	t.Run("5xx", func(t *testing.T) {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
			if r.URL.Path == apiv1.SessionsPath {
				t.Error("created a host")
			}
			apiError(w, http.StatusInternalServerError, apiv1.CodeInternal, "the operator failed this request")
		}
		h.mustRun(ExitFailed, args...)
		contains(t, "stderr", h.stderr.String(), "the operator failed")
		noCreate(t, h)
	})
}

func TestThreadStartReleaseTimeoutStartsAtRelease(t *testing.T) {
	t.Run("launch longer than the deadline still releases", func(t *testing.T) {
		releaseTimeoutStartsAtRelease(t)
	})
	t.Run("stuck release ends at the deadline", func(t *testing.T) {
		prev := codexHostReleaseTimeout
		codexHostReleaseTimeout = 200 * time.Millisecond
		t.Cleanup(func() { codexHostReleaseTimeout = prev })
		h, c := threadAPIHarness(t, nil)
		var release int
		h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
			switch r.URL.Path {
			case apiv1.CodexHostsSelectPath:
				writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, ""))
			case apiv1.SessionThreadsPath(threadHostName):
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(hostResult(t, apiv1.ThreadResponded, ""))
			case apiv1.CodexHostsReleasePath:
				release++
				<-r.Context().Done()
			default:
				apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
			}
		}
		args := []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
		started := time.Now()
		h.mustRun(ExitOK, args...)
		if time.Since(started) > 5*time.Second || release != 1 || c.calls != 0 {
			t.Fatalf("elapsed %s release %d execs %d", time.Since(started), release, c.calls)
		}
	})
}

func releaseTimeoutStartsAtRelease(t *testing.T) {
	t.Helper()
	prev := codexHostReleaseTimeout
	codexHostReleaseTimeout = 2 * time.Second
	t.Cleanup(func() { codexHostReleaseTimeout = prev })
	h, c := threadAPIHarness(t, nil)
	var release int
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.URL.Path {
		case apiv1.CodexHostsSelectPath:
			var req apiv1.CodexHostSelectRequest
			if json.Unmarshal(body, &req) != nil || !apiv1.ValidCodexLaunchID(req.LaunchID) {
				t.Errorf("select rejected")
			}
			writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, ""))
		case apiv1.SessionThreadsPath(threadHostName):
			time.Sleep(3 * time.Second)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(hostResult(t, apiv1.ThreadResponded, ""))
		case apiv1.CodexHostsReleasePath:
			release++
			writeTestJSON(w, http.StatusOK, apiv1.CodexHostReleaseResponse{Host: threadHostName, Released: true})
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route")
		}
	}
	args := []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	h.mustRun(ExitOK, args...)
	if release != 1 || c.calls != 0 {
		t.Fatalf("release %d execs %d", release, c.calls)
	}
}

func TestThreadStartReleaseKeepsPortForward(t *testing.T) {
	h, _ := laptopHarness(t, operatorDNS)
	ctx, cancel := context.WithCancel(context.Background())
	var forwardCtx context.Context
	var once sync.Once
	address := h.api.srv.URL
	h.env.PortForward = func(fwdCtx context.Context, _ []string) (string, func(), error) {
		once.Do(func() { forwardCtx = fwdCtx })
		return address, func() {}, nil
	}
	h.env.RunCapture = func(context.Context, []string, io.Reader, io.Writer) (int, []byte, error) {
		return 1, nil, errors.New("exec stopped")
	}
	var release int
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.URL.Path {
		case apiv1.CodexHostsSelectPath:
			writeTestJSON(w, http.StatusOK, codexChoice("use", threadHostName, ""))
		case apiv1.SessionPath(threadHostName):
			cancel()
			sess := testSession(threadHostName, "Running")
			sess.Agent, sess.Mode = "codex", "remote"
			writeTestJSON(w, http.StatusOK, sess)
		case apiv1.CodexHostsReleasePath:
			if forwardCtx == nil || forwardCtx.Err() != nil {
				t.Errorf("the select connection was already closed")
				http.Error(w, "closed", http.StatusBadGateway)
				return
			}
			release++
			writeTestJSON(w, http.StatusOK, apiv1.CodexHostReleaseResponse{Host: threadHostName, Released: true})
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.URL.Path)
		}
	}
	args := []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	if code := Run(ctx, args, h.env); release != 1 {
		t.Fatalf("exit %d release %d\nstderr: %s", code, release, h.stderr.String())
	}
	if forwardCtx == nil || forwardCtx.Err() == nil {
		t.Fatal("the command left the select connection open")
	}
}

func TestThreadStartSelectionRefusalIsReturned(t *testing.T) {
	args := func(h *harness) []string {
		return []string{"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", h.write("handoff.txt", threadHandoff), "--repo", "dev-env"}
	}
	for _, tc := range []struct {
		name, code, msg, want string
		status, exit          int
	}{
		{"unauthenticated", apiv1.CodeUnauthenticated, "no", "the API did not accept", http.StatusUnauthorized, ExitAuth},
		{"forbidden", apiv1.CodeForbidden, "A coordinator may only check host selection.", "not allowed", http.StatusForbidden, ExitAuth},
		{"invalid", apiv1.CodeInvalid, "launchId is 16 to 64 characters of a-z and 0-9", "the API refused", http.StatusUnprocessableEntity, ExitFailed},
		{"conflict", apiv1.CodeConflict, "session paired kept changing; try again", "the API refused", http.StatusConflict, ExitFailed},
		{"token path looks like a 5xx", apiv1.CodeUnauthenticated, "no", "the API did not accept", http.StatusUnauthorized, ExitAuth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			if tc.name == "token path looks like a 5xx" {
				h.vars[envTokenFile] = h.write("the API answered 5/token", testToken+"\n")
			}
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if r.URL.Path != apiv1.CodexHostsSelectPath {
					t.Errorf("unexpected %s", r.URL.Path)
				}
				apiError(w, tc.status, tc.code, tc.msg)
			}
			h.mustRun(tc.exit, args(h)...)
			if strings.Contains(h.stderr.String(), "Host selection was not used") || strings.Contains(h.stdout.String(), `"version"`) {
				t.Fatalf("a real refusal was hidden\nstdout %s\nstderr %s", h.stdout.String(), h.stderr.String())
			}
			contains(t, "stderr", h.stderr.String(), tc.want)
		})
	}
}
