package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestList(t *testing.T) {
	h := newHarness(t)
	running := runningOn(testSession("haynes-ops-1006-205500", ""), "talosw02")
	running.AgentStatus = &apiv1.AgentStatus{State: "busy"}
	running.Outdated = true
	pending := pendingOn(testSession("dev-env-1006-205900", ""), unschedulable, "0/7 nodes are available: 3 Insufficient cpu")
	pending.CreatedAt = testNow.Add(-40 * time.Second)
	pending.Parent = "haynes-ops-1006-205500"
	reaping := runningOn(testSession("haynes-ops-1005-090000", ""), "talosw03")
	reaping.Reaping = true
	reaping.CreatedAt = testNow.Add(-36 * time.Hour)
	failed := testSession("old-1001-000000", "Failed")
	failed.CreatedAt = testNow.Add(-5 * 24 * time.Hour)
	failed.Conditions = []apiv1.Condition{{Type: conditionPodReady, Status: "False", Reason: "PodEnded", Message: "the pod ended (Evicted): low memory; its volume is kept"}}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{pending, running, reaping, failed}})
	}

	h.mustRun(ExitOK, "list")
	out := h.stdout.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("list printed %d lines, want a header and 4 rows:\n%s", len(lines), out)
	}
	if fields := strings.Fields(lines[0]); strings.Join(fields, " ") != "NAME PHASE AGENT NODE AGE PARENT NOTE" {
		t.Errorf("header = %q", lines[0])
	}
	contains(t, "list", out,
		"dev-env-1006-205900", "40s", "haynes-ops-1006-205500  ", "0/7 nodes are available: 3 Insufficient cpu",
		"busy", "talosw02", "5m", "outdated",
		"36h", "reaping",
		"5d", "the pod ended (Evicted)")
	if got := h.api.requests()[0]; got.method != http.MethodGet || got.path != apiv1.SessionsPath || len(got.query) != 0 {
		t.Errorf("request = %s %s %v, want GET /v1/sessions with no filters", got.method, got.path, got.query)
	}

	h.mustRun(ExitOK, "list", "--repo", "haynes-ops", "--state", "pending", "--mine")
	reqs := h.api.requests()
	q := reqs[len(reqs)-1].query
	if q[apiv1.FilterRepo][0] != "haynes-ops" || q[apiv1.FilterState][0] != "pending" || q[apiv1.FilterMine][0] != "true" {
		t.Errorf("filters = %v, want repo, state and mine", q)
	}

	h.mustRun(ExitOK, "list", "-o", "name")
	if got := h.stdout.String(); got != "dev-env-1006-205900\nhaynes-ops-1006-205500\nhaynes-ops-1005-090000\nold-1001-000000\n" {
		t.Errorf("-o name printed %q", got)
	}

	h.mustRun(ExitOK, "list", "-o", "json")
	var l apiv1.SessionList
	if err := json.Unmarshal(h.stdout.Bytes(), &l); err != nil || len(l.Sessions) != 4 {
		t.Errorf("-o json printed %d sessions (%v), want 4", len(l.Sessions), err)
	}

	h.mustRun(ExitUsage, "list", "haynes-ops")
	contains(t, "stderr", h.stderr.String(), "list takes no arguments")
}

func TestListWithNoSessions(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{}})
	}
	h.mustRun(ExitOK, "list")
	if got := h.stdout.String(); got != "No sessions.\n" {
		t.Errorf("list printed %q", got)
	}
	h.mustRun(ExitOK, "list", "-o", "name")
	if h.stdout.Len() != 0 {
		t.Errorf("-o name printed %q, want nothing", h.stdout.String())
	}
}

func TestShow(t *testing.T) {
	h := newHarness(t)
	s := runningOn(testSession(name, ""), "talosw02")
	s.Prompt = "fix the typo\nthen open a PR"
	s.Base = "origin/main"
	s.Revision = "2.0.3-7f3a9c"
	s.Outdated = true
	s.Limits = &apiv1.Limits{Timeout: "40m0s"}
	beat := testNow.Add(-30 * time.Second)
	s.AgentStatus = &apiv1.AgentStatus{
		State: "exited", Branch: "agent/fix-typo", Head: "abc1234", LastHeartbeat: &beat,
		Task: &apiv1.TaskResult{ExitCode: 0, FinishedAt: testNow.Add(-time.Minute), NumTurns: 12},
	}
	s.Usage = &apiv1.Usage{CostUSD: "1.25", InputTokens: 1000, OutputTokens: 200}
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.URL.Path != apiv1.SessionPath(name) {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "session "+strings.TrimPrefix(r.URL.Path, "/v1/sessions/")+": not found")
			return
		}
		writeTestJSON(w, http.StatusOK, s)
	}

	h.mustRun(ExitOK, "show", name)
	contains(t, "show", h.stdout.String(),
		"name:", name,
		"phase:", "Running on talosw02",
		"repo:", "haynes-ops from origin/main",
		"claude claude-opus-5-5, effort xhigh, task mode",
		"timeout 40m0s, max turns none",
		"2.0.3-7f3a9c (outdated",
		"agent state:", "exited",
		"agent/fix-typo at abc1234",
		"last heartbeat:", "30s ago",
		"exit 0 at 2026-10-06T20:59:00Z, 12 turns",
		"$1.25, 1000 tokens in, 200 out",
		"condition:", "PodReady=True",
		"prompt:\n  fix the typo\n  then open a PR\n")

	h.mustRun(ExitOK, "show", name, "-o", "json")
	var got apiv1.Session
	if err := json.Unmarshal(h.stdout.Bytes(), &got); err != nil || got.Prompt != s.Prompt {
		t.Errorf("-o json: %v, prompt %q", err, got.Prompt)
	}

	h.mustRun(ExitNotFound, "show", "nope-1006-000000")
	contains(t, "stderr", h.stderr.String(), "session nope-1006-000000: not found (404 not_found)")

	h.mustRun(ExitUsage, "show")
	h.mustRun(ExitUsage, "show", "a", "b")
}

func TestReap(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		n := strings.TrimPrefix(r.URL.Path, apiv1.SessionsPath+"/")
		if r.Method != http.MethodDelete || n == "gone-1006-000000" {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "session "+n+": not found")
			return
		}
		s := runningOn(testSession(n, ""), "talosw02")
		s.Reaping = true
		writeTestJSON(w, http.StatusAccepted, s)
	}

	h.mustRun(ExitOK, "reap", name)
	if got := h.api.requests()[0]; got.method != http.MethodDelete || got.path != apiv1.SessionPath(name) {
		t.Errorf("request = %s %s, want DELETE %s", got.method, got.path, apiv1.SessionPath(name))
	}
	contains(t, "stdout", h.stdout.String(), "reaping "+name+": the operator rescues its work to a bundle")
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q", h.stderr.String())
	}

	// --force is v1's; v2 says it changes nothing and reaps as always.
	h.mustRun(ExitOK, "reap", name, "--force")
	contains(t, "stderr", h.stderr.String(), "--force changes nothing in v2")

	// Several names: each is tried, and the exit code is the first failure's.
	h.mustRun(ExitNotFound, "reap", "a-1006-000000", "gone-1006-000000", "b-1006-000000")
	contains(t, "stdout", h.stdout.String(), "reaping a-1006-000000", "reaping b-1006-000000")
	contains(t, "stderr", h.stderr.String(), "gone-1006-000000: session gone-1006-000000: not found (404 not_found)")

	h.mustRun(ExitOK, "reap", "-o", "json", "a-1006-000000", "b-1006-000000")
	var l apiv1.SessionList
	if err := json.Unmarshal(h.stdout.Bytes(), &l); err != nil || len(l.Sessions) != 2 || !l.Sessions[0].Reaping {
		t.Errorf("-o json: %v, %+v", err, l)
	}

	before := len(h.api.requests())
	h.mustRun(ExitUsage, "reap")
	contains(t, "stderr", h.stderr.String(), "reap takes the names of the sessions to reap")
	if len(h.api.requests()) != before {
		t.Error("reap with no name sent a request")
	}
}

func TestFleet(t *testing.T) {
	h := newHarness(t)
	beat := testNow.Add(-20 * time.Second)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{
			Revision: "2.0.3-7f3a9c",
			Phases:   map[string]int{"Suspended": 2, "Running": 3, "Pending": 1, "Weird": 1},
			Sessions: []apiv1.FleetSession{
				{Name: "a-1006-205900", Phase: "Pending", Pending: "0/7 nodes are available: 3 Insufficient memory", CreatedAt: testNow.Add(-time.Minute)},
				{Name: "b-1006-200000", Phase: "Running", Node: "talosw02", AgentState: "busy", LastHeartbeat: &beat, Outdated: true, CreatedAt: testNow.Add(-time.Hour)},
			},
			Nodes:    []apiv1.FleetNode{{Name: "talosw02", Sessions: 2}, {Name: "talosw03", Sessions: 1}},
			Outdated: []string{"b-1006-200000"},
		})
	}
	h.mustRun(ExitOK, "fleet")
	contains(t, "fleet", h.stdout.String(),
		"revision: 2.0.3-7f3a9c\n",
		"phases:   Pending 1, Running 3, Suspended 2, Weird 1\n",
		"nodes:    talosw02 2, talosw03 1\n",
		"outdated: b-1006-200000\n",
		"NAME", "HEARTBEAT",
		"a-1006-205900", "0/7 nodes are available: 3 Insufficient memory",
		"b-1006-200000", "busy", "20s ago", "1h", "outdated")
	if got := h.api.requests()[0]; got.method != http.MethodGet || got.path != apiv1.FleetPath {
		t.Errorf("request = %s %s, want GET /v1/fleet", got.method, got.path)
	}

	h.mustRun(ExitOK, "fleet", "-o", "json")
	var f apiv1.Fleet
	if err := json.Unmarshal(h.stdout.Bytes(), &f); err != nil || f.Revision != "2.0.3-7f3a9c" {
		t.Errorf("-o json: %v, %+v", err, f)
	}
	h.mustRun(ExitUsage, "fleet", "extra")
}

func TestFleetEmptyWithNoRevision(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.Fleet{RevisionError: "templates dev-env-system/dev-env-templates: not found", Phases: map[string]int{}})
	}
	h.mustRun(ExitOK, "fleet")
	contains(t, "fleet", h.stdout.String(),
		"revision: unknown: templates dev-env-system/dev-env-templates: not found",
		"phases:   no sessions",
		"No session holds or waits for a pod.")
}

// Each refusal of D-46 maps to a plain message and its exit code.
func TestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		msg    string
		fields []apiv1.FieldError
		exit   int
		want   []string
		tries  int
	}{
		{name: "401", status: 401, code: apiv1.CodeUnauthenticated, msg: "the token is not valid for the audience dev-env-operator", exit: ExitAuth,
			want: []string{"the API did not accept the token (401 unauthenticated)", "agent-run sent the token at", "(from DEV_ENV_API_TOKEN_FILE)", "audience dev-env-operator"}},
		{name: "403", status: 403, code: apiv1.CodeForbidden, msg: "a session's child runs on its parent's profile dev, not full", exit: ExitAuth,
			want: []string{"not allowed (403 forbidden): a session's child runs on its parent's profile dev, not full"}},
		{name: "409", status: 409, code: apiv1.CodeConflict, msg: `idempotency key "k" already created session x with a different request`, exit: ExitFailed,
			want: []string{"the API refused the request (409 conflict): idempotency key"}},
		{name: "422", status: 422, code: apiv1.CodeInvalid, msg: "agent: ...", exit: ExitFailed,
			fields: []apiv1.FieldError{{Field: "agent", Message: "codex sessions arrive in plan 04; plan 01 runs claude"}, {Field: "limits.timeout", Message: "must be positive"}, {Field: "", Message: "the session's repo is unknown"}, {Field: "weird", Message: "x"}},
			want:   []string{"the API refused the request (422 invalid):\n  --agent: codex sessions arrive in plan 04", "\n  --timeout: must be positive", "\n  the session's repo is unknown", "\n  weird: x"}},
		{name: "429", status: 429, code: apiv1.CodeLimitExceeded, msg: "session p already has 4 running children, the most at a time (DESIGN-001 3.4); one must finish first", exit: ExitRetry,
			want: []string{"one must finish first (429 limit_exceeded)"}, tries: 1},
		{name: "500", status: 500, code: apiv1.CodeInternal, msg: "the operator failed this request; its log has the cause", exit: ExitFailed,
			want: []string{"the operator failed (500 internal): the operator failed this request"}, tries: 1},
		{name: "503", status: 503, code: apiv1.CodeUnavailable, msg: "list sessions: the Kubernetes API server could not be reached", exit: ExitRetry,
			want: []string{"the operator is unavailable (503 unavailable): list sessions", "try again shortly"}, tries: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
				apiError(w, tc.status, tc.code, tc.msg, tc.fields...)
			}
			h.mustRun(tc.exit, "list")
			contains(t, "stderr", h.stderr.String(), tc.want...)
			want := tc.tries
			if want == 0 {
				want = 1
			}
			if n := len(h.api.requests()); n != want {
				t.Errorf("sent %d requests, want %d", n, want)
			}
		})
	}
}

// A proxy's or a load balancer's answer is not an API error document.
func TestRefusalThatIsNotAnAPIError(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>bad gateway</html>"))
	}
	h.mustRun(ExitRetry, "fleet")
	contains(t, "stderr", h.stderr.String(), "the API answered 502 Bad Gateway, not an API error: <html>bad gateway</html>")
	if n := len(h.api.requests()); n != 3 {
		t.Errorf("sent %d requests, want 3 (a 502 is retried)", n)
	}
}

func TestUnreachableAPI(t *testing.T) {
	h := newHarness(t)
	h.api.srv.Close()
	h.mustRun(ExitRetry, "list")
	contains(t, "stderr", h.stderr.String(), "could not reach the API at "+h.api.srv.URL)
	if len(h.sleeps) != 2 {
		t.Errorf("slept %d times, want 2 retries", len(h.sleeps))
	}
}

func TestBadAnswerBody(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}
	h.mustRun(ExitFailed, "list")
	contains(t, "stderr", h.stderr.String(), "the API answered GET /v1/sessions with a body that is not the expected JSON")
}

func TestTablesHaveNoTrailingSpaces(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		writeTestJSON(w, http.StatusOK, apiv1.SessionList{Sessions: []apiv1.Session{runningOn(testSession(name, ""), "talosw02")}})
	}
	h.mustRun(ExitOK, "list")
	for _, line := range strings.Split(h.stdout.String(), "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %q ends in a space", line)
		}
	}
	if !strings.HasSuffix(h.stdout.String(), "dev/dev-env\n") {
		t.Errorf("list = %q, want the row to end at its last value", h.stdout.String())
	}
}
