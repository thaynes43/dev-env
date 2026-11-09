package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

const threadHostName = "codex-host-1"
const threadHandoff = "SYNTHETIC_PRIVATE_THREAD_HANDOFF_489: make a worktree first."

func codexHostView(phase string) apiv1.Session {
	v := testSession(threadHostName, phase)
	v.Agent, v.Mode, v.Model, v.Repo = "codex", "remote", "gpt-6-astra", "dev-env"
	return v
}

// hostResult is what a host's agentd prints: one result line.
func hostResult(t *testing.T, outcome, code string) []byte {
	t.Helper()
	rec := threadlaunch.NewRecorder("codex", func() time.Time { return testNow })
	rec.Result.Host, rec.Result.ThreadID, rec.Result.Source = threadHostName, "0199a000-0000-7000-8000-000000000002", "vscode"
	if outcome == apiv1.ThreadResponded {
		rec.Phase(apiv1.ThreadPhaseFirstResponse, "ok", "thread_items", "agent_message")
	}
	b, err := threadlaunch.Encode(rec.Finish(outcome, code, apiv1.ThreadPhaseFirstResponse, 5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type capture struct {
	argv   []string
	stdin  string
	stderr string // what the fake exec writes to stderr
	calls  int
}

// threadHarness serves the host view and answers the exec with code/out.
func threadHarness(t *testing.T, view apiv1.Session, code int, out []byte) (*harness, *capture) {
	t.Helper()
	h := newHarness(t)
	h.kubectl = func([]string) int { return 0 }
	serveSession(h, view)
	c := &capture{}
	h.env.RunCapture = func(_ context.Context, argv []string, stdin io.Reader, stderr io.Writer) (int, []byte, error) {
		c.calls++
		c.argv = argv
		b, _ := io.ReadAll(stdin)
		c.stdin = string(b)
		_, _ = io.WriteString(stderr, c.stderr)
		return code, out, nil
	}
	return h, c
}

func codexThreadArgs(h *harness, extra ...string) []string {
	args := []string{"thread", "start", "--agent", "codex", "--host", threadHostName, "--model", "gpt-6-astra", "--effort", "xhigh", "--handoff", h.write("handoff.txt", threadHandoff)}
	return append(args, extra...)
}

func threadResult(t *testing.T, h *harness) apiv1.ThreadLaunchResult {
	t.Helper()
	raw := h.stdout.Bytes()
	r, err := apiv1.DecodeThreadLaunch(bytes.TrimSuffix(raw, []byte("\n")))
	if err != nil || bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatalf("not one thread-launch.v1 line: %v %q", err, raw)
	}
	if strings.Contains(string(raw)+h.stderr.String(), "SYNTHETIC_PRIVATE") {
		t.Fatal("the handoff reached the output")
	}
	return r
}

// The Codex route execs agentd ctl thread-start in the host over pair's SPDY
// exec, with the handoff on stdin only, and relays the host's result.
func TestThreadStartCodexExecsHost(t *testing.T) {
	h, c := threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, hostResult(t, apiv1.ThreadResponded, ""))
	h.mustRun(ExitOK, codexThreadArgs(h, "--repo", "dev-env")...)
	want := "env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + threadHostName +
		" -c agent -i -- agentd ctl thread-start --model gpt-6-astra --effort xhigh --name thread-1006-210000 --wait 5m0s --repo dev-env"
	if strings.Join(c.argv, " ") != want {
		t.Fatalf("exec\n%s\nwant\n%s", strings.Join(c.argv, " "), want)
	}
	if c.stdin != threadHandoff || strings.Contains(strings.Join(c.argv, " "), "SYNTHETIC") {
		t.Fatal("the handoff must travel on stdin only")
	}
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadResponded || r.ThreadID == "" || r.Permission.Flag != "--dangerously-bypass-approvals-and-sandbox" {
		t.Fatalf("result %+v", r)
	}
	// A pending host result keeps its code; the caller never resends.
	h, _ = threadHarness(t, runningOn(codexHostView(""), "talosw02"), 7, hostResult(t, apiv1.ThreadVisible, "response_pending"))
	h.mustRun(ExitPending, codexThreadArgs(h)...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadVisible || r.Error.Code != "response_pending" {
		t.Fatalf("result %+v", r)
	}
}

// A host whose agentd predates thread-start, a result that disagrees with its
// exit code, or a lost exec are never reported as a launch.
func TestThreadStartCodexHostAnswers(t *testing.T) {
	cases := []struct {
		name          string
		code          int
		out           []byte
		stderr        string
		exit          int
		outcome, want string
	}{
		{"old agentd", 2, nil, "agentd: usage: ctl status | rescue\n", ExitFailed, apiv1.ThreadFailed, "host_unsupported"},
		{"crash", 2, nil, "panic: boom\n", ExitUnconfirmed, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		{"kubectl refused", 1, nil, "error: unable to upgrade connection: Forbidden\n", ExitFailed, apiv1.ThreadFailed, "exec_failed"},
		{"exit mismatch", 0, hostResult(t, apiv1.ThreadVisible, "response_pending"), "", ExitUnconfirmed, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		{"garbage", 1, []byte("not a result\n"), "", ExitUnconfirmed, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		{"two lines", 0, append(hostResult(t, apiv1.ThreadResponded, ""), hostResult(t, apiv1.ThreadResponded, "")...), "", ExitUnconfirmed, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
		{"trailing brace", 0, append(bytes.TrimSuffix(hostResult(t, apiv1.ThreadResponded, ""), []byte("\n")), '}', '\n'), "", ExitUnconfirmed, apiv1.ThreadUnconfirmed, "host_result_unconfirmed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := threadHarness(t, runningOn(codexHostView(""), "talosw02"), tc.code, tc.out)
			c.stderr = tc.stderr
			h.mustRun(tc.exit, codexThreadArgs(h)...)
			if r := threadResult(t, h); r.Outcome != tc.outcome || r.Error.Code != tc.want {
				t.Fatalf("result %+v", r.Error)
			}
		})
	}
	h, _ := threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, nil)
	h.env.RunCapture = func(context.Context, []string, io.Reader, io.Writer) (int, []byte, error) {
		return 0, nil, errors.New("kubectl lost")
	}
	h.mustRun(ExitUnconfirmed, codexThreadArgs(h)...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadUnconfirmed {
		t.Fatalf("result %+v", r)
	}
}

// On the exec path the host must be a Running Codex remote session: none of
// these refusals execs anything.
func TestThreadStartCodexRefusesBeforeExec(t *testing.T) {
	claude := runningOn(codexHostView(""), "talosw02")
	claude.Agent = "claude"
	task := runningOn(codexHostView(""), "talosw02")
	task.Mode = "task"
	for name, tc := range map[string]struct {
		view apiv1.Session
		code string
	}{
		"claude host":  {claude, "not_codex_host"},
		"codex task":   {task, "not_codex_host"},
		"not running":  {codexHostView("Suspended"), "host_not_running"},
		"not anywhere": {apiv1.Session{Name: "missing"}, "exec_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			h, c := threadHarness(t, tc.view, 0, nil)
			h.mustRun(ExitFailed, codexThreadArgs(h)...)
			if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != tc.code || c.calls != 0 {
				t.Fatalf("result %+v calls %d", r.Error, c.calls)
			}
		})
	}
}

func TestThreadStartInvalidInput(t *testing.T) {
	h := newHarness(t)
	long := h.write("long.txt", strings.Repeat("x", 4097))
	blank := h.write("blank.txt", " \n")
	nul := h.write("nul.txt", "a\x00b")
	ctrl := h.write("ctrl.txt", "a\x1bb")
	good := h.write("good.txt", threadHandoff)
	for name, args := range map[string][]string{
		"no verb":        {"thread"},
		"other verb":     {"thread", "stop"},
		"unknown agent":  {"thread", "start", "--agent", "gemini", "--handoff", good},
		"no host":        {"thread", "start", "--agent", "codex", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good},
		"alias model":    {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt", "--effort", "high", "--handoff", good},
		"no effort":      {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--handoff", good},
		"long handoff":   {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", long},
		"blank handoff":  {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", blank},
		"nul handoff":    {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", nul},
		"ctrl handoff":   {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", ctrl},
		"claude ctrl":    {"thread", "start", "--agent", "claude", "--project", "sample", "--model", "claude-opus-5-5", "--handoff", ctrl},
		"bad name":       {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good, "--name", "a b"},
		"codex project":  {"thread", "start", "--agent", "codex", "--host", "h-1", "--project", "p", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good},
		"long wait":      {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good, "--wait", "11m"},
		"text output":    {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good, "-o", "text"},
		"claude host":    {"thread", "start", "--agent", "claude", "--host", "h-1", "--project", "sample", "--model", "claude-opus-5-5", "--handoff", good},
		"claude project": {"thread", "start", "--agent", "claude", "--model", "claude-opus-5-5", "--handoff", good},
		"claude long":    {"thread", "start", "--agent", "claude", "--project", "sample", "--model", "claude-opus-5-5", "--handoff", long},
		"positional":     {"thread", "start", "--agent", "codex", "--host", "h-1", "--model", "gpt-6-astra", "--effort", "high", "--handoff", good, "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			h.mustRun(ExitUsage, args...)
			if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != "invalid_input" {
				t.Fatalf("result %+v", r.Error)
			}
		})
	}
	if len(h.api.requests()) != 0 {
		t.Fatal("invalid input reached the API")
	}
}

// Cursor and Grok are refused as not qualified before any request.
func TestThreadStartUnqualifiedProviders(t *testing.T) {
	for _, agent := range []string{"cursor", "grok"} {
		h := newHarness(t)
		h.mustRun(ExitFailed, "thread", "start", "--agent", agent, "--model", "grok-4.7-high", "--handoff", h.write("h.txt", threadHandoff))
		r := threadResult(t, h)
		if r.Outcome != apiv1.ThreadBlocked || r.Error.Code != "provider_not_qualified" || !strings.Contains(r.Error.Message, "not yet qualified") || len(h.api.requests()) != 0 || r.Permission.Mode != apiv1.ThreadPermissionNotQualified {
			t.Fatalf("%s: %+v", agent, r.Error)
		}
	}
}

// Claude is rc start's flow, translated: a registered link is visible, the
// first response is not observable, so it exits 7 and never claims success.
func TestThreadStartClaudeMapsRCStart(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(poll int) apiv1.Session { return rcView(min(poll, 5)) }, http.StatusAccepted)
	h.mustRun(ExitPending, "thread", "start", "--agent", "claude", "--project", "sample", "--model", "claude-opus-5-5", "--handoff", h.write("handoff.txt", rcTestText))
	raw := h.stdout.String()
	r, err := apiv1.DecodeThreadLaunch([]byte(strings.TrimSuffix(raw, "\n")))
	if err != nil || strings.Contains(raw, rcTestText) {
		t.Fatalf("result %v %q", err, raw)
	}
	if r.Permission.Mode != apiv1.ThreadPermissionAlwaysAllow || r.Permission.Flag != "--dangerously-skip-permissions" || !slices.Contains(r.Permission.NotBypassed, "claude_critical_path_check") {
		t.Fatalf("permission %+v", r.Permission)
	}
	if r.Outcome != apiv1.ThreadVisible || r.Error.Code != "response_not_observable" || r.Link == nil || *r.Link != rcTestLink || r.Provider != "claude" || r.SessionName != "remote-268" {
		t.Fatalf("result %+v", r)
	}
	for phase, state := range map[string]string{
		apiv1.ThreadPhaseHostReady: "ok", apiv1.ThreadPhasePaired: "not_applicable", apiv1.ThreadPhaseClientStarted: "ok",
		apiv1.ThreadPhaseThreadCreated: "ok", apiv1.ThreadPhaseSourceVisible: "ok", apiv1.ThreadPhaseHandoff: "ok", apiv1.ThreadPhaseFirstResponse: "unknown",
	} {
		if r.Phases[phase].State != state {
			t.Errorf("%s = %+v", phase, r.Phases[phase])
		}
	}
	if rcCountMessages(h) != 1 {
		t.Fatal("the handoff must be sent exactly once")
	}
	// A failed rc launch keeps rc's code and maps its phase.
	h = newHarness(t)
	rcServe(h, http.StatusCreated, func(poll int) apiv1.Session { return rcView(min(poll, 3)) }, http.StatusAccepted)
	h.mustRun(ExitPending, "thread", "start", "--agent", "claude", "--project", "sample", "--model", "claude-opus-5-5", "--handoff", h.write("handoff.txt", rcTestText), "--wait", "30s")
	r, err = apiv1.DecodeThreadLaunch(bytes.TrimSuffix(h.stdout.Bytes(), []byte("\n")))
	if err != nil || r.Outcome != apiv1.ThreadTimedOut || r.Error.Code != "launch_deadline" || r.Phases[apiv1.ThreadPhaseSourceVisible].State == "ok" {
		t.Fatalf("result %v %+v", err, r)
	}
}

// threadAPIHarness is an ordinary session pod: its API answers the thread
// route with answer, and any kubectl exec fails the test.
func threadAPIHarness(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, body []byte)) (*harness, *capture) {
	t.Helper()
	h := newHarness(t)
	h.vars[envAgentdTokenFile] = h.write("pod-token", "x")
	c := &capture{}
	h.env.RunCapture = func(context.Context, []string, io.Reader, io.Writer) (int, []byte, error) {
		c.calls++
		return 1, nil, errors.New("a session pod has no exec")
	}
	h.api.handle = answer
	return h, c
}

// serveThread answers the host's thread route with status and raw.
func serveThread(status int, raw []byte) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodPost || r.URL.Path != apiv1.SessionThreadsPath(threadHostName) {
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no route "+r.Method+" "+r.URL.Path)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	}
}

// From an ordinary session pod the Codex route is the operator's API (OP495):
// one POST with the handoff in the body, no kubectl, and the operator's
// thread-launch.v1 result printed unchanged.
func TestThreadStartCodexViaAPIInSessionPod(t *testing.T) {
	out := hostResult(t, apiv1.ThreadResponded, "")
	h, c := threadAPIHarness(t, serveThread(http.StatusOK, out))
	h.mustRun(ExitOK, codexThreadArgs(h, "--repo", "dev-env")...)
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != apiv1.SessionThreadsPath(threadHostName) || reqs[0].auth != "Bearer "+testToken || c.calls != 0 {
		t.Fatalf("requests %+v, execs %d", reqs, c.calls)
	}
	dec := json.NewDecoder(bytes.NewReader(reqs[0].body))
	dec.DisallowUnknownFields()
	var got apiv1.ThreadStartRequest
	if err := dec.Decode(&got); err != nil {
		t.Fatal(err)
	}
	want := apiv1.ThreadStartRequest{Model: "gpt-6-astra", Effort: "xhigh", Name: "thread-1006-210000", Repo: "dev-env", Wait: "5m0s", Handoff: threadHandoff}
	if got != want {
		t.Fatalf("request %+v, want %+v", got, want)
	}
	r := threadResult(t, h)
	if g, _ := json.Marshal(r); !bytes.Equal(append(g, '\n'), out) {
		t.Fatalf("the result changed on the way\n got %s\nwant %s", g, out)
	}

	// A pending result keeps its outcome and exit; agent-run never resends.
	h, _ = threadAPIHarness(t, serveThread(http.StatusOK, hostResult(t, apiv1.ThreadVisible, "response_pending")))
	h.mustRun(ExitPending, codexThreadArgs(h, "--wait", "90s")...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadVisible || r.Error.Code != "response_pending" || len(h.api.requests()) != 1 {
		t.Fatalf("result %+v", r)
	}
	if !strings.Contains(string(h.api.requests()[0].body), `"wait":"1m30s"`) {
		t.Fatalf("body %s", h.api.requests()[0].body)
	}

	// A workbench session keeps the exec route.
	h, c = threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, out)
	h.vars[envAgentdTokenFile] = h.write("pod-token", "x")
	h.vars[envWorkbench] = "1"
	h.mustRun(ExitOK, codexThreadArgs(h)...)
	for _, req := range h.api.requests() {
		if req.method == http.MethodPost {
			t.Fatalf("a workbench session used the API route: %+v", req)
		}
	}
	if c.calls != 1 || c.stdin != threadHandoff {
		t.Fatalf("execs %d", c.calls)
	}
}

// Every answer but one valid result is a refusal or unconfirmed, sent once:
// an operator without the route says so by name, a 5xx or an unreadable
// result is unconfirmed, and none is retried or falls back to kubectl.
func TestThreadStartCodexViaAPIAnswers(t *testing.T) {
	refusal := func(code, msg string) []byte {
		b, _ := json.Marshal(apiv1.ErrorResponse{Error: apiv1.Error{Code: code, Message: msg}})
		return b
	}
	otherHost := bytes.ReplaceAll(hostResult(t, apiv1.ThreadVisible, "response_pending"), []byte(threadHostName), []byte("other-host"))
	refused := threadlaunch.NewRecorder("codex", func() time.Time { return testNow })
	refused.Result.Host = threadHostName
	refused.Phase(apiv1.ThreadPhaseHostReady, "error", "api", "not_codex_host")
	hostRefusal, _ := threadlaunch.Encode(refused.Finish(apiv1.ThreadFailed, "not_codex_host", apiv1.ThreadPhaseHostReady, 5*time.Minute))
	for name, tc := range map[string]struct {
		status        int
		raw           []byte
		exit          int
		outcome, code string
	}{
		"old operator":    {http.StatusNotFound, refusal(apiv1.CodeNotFound, "no route POST /v1/sessions/"+threadHostName+"/threads"), ExitFailed, apiv1.ThreadFailed, "route_unsupported"},
		"not the API":     {http.StatusNotFound, []byte("404 page not found\n"), ExitFailed, apiv1.ThreadFailed, "route_unsupported"},
		"no such host":    {http.StatusNotFound, refusal(apiv1.CodeNotFound, "session "+threadHostName+": not found"), ExitFailed, apiv1.ThreadFailed, "host_not_found"},
		"unauthenticated": {http.StatusUnauthorized, refusal(apiv1.CodeUnauthenticated, "no"), ExitFailed, apiv1.ThreadFailed, "unauthenticated"},
		"forbidden":       {http.StatusForbidden, refusal(apiv1.CodeForbidden, "coordinator access is limited"), ExitFailed, apiv1.ThreadFailed, "caller_not_allowed"},
		"invalid":         {http.StatusUnprocessableEntity, refusal(apiv1.CodeInvalid, "wait: too long"), ExitUsage, apiv1.ThreadFailed, "invalid_input"},
		"budget":          {http.StatusConflict, refusal(apiv1.CodeConflict, "task budget latched"), ExitFailed, apiv1.ThreadFailed, "api_refused"},
		"unavailable":     {http.StatusServiceUnavailable, refusal(apiv1.CodeUnavailable, "this API cannot exec into pods"), ExitUnconfirmed, apiv1.ThreadUnconfirmed, "api_result_unconfirmed"},
		"internal":        {http.StatusInternalServerError, refusal(apiv1.CodeInternal, "boom"), ExitUnconfirmed, apiv1.ThreadUnconfirmed, "api_result_unconfirmed"},
		"not a result":    {http.StatusOK, []byte("{}\n"), ExitUnconfirmed, apiv1.ThreadUnconfirmed, "api_result_unconfirmed"},
		"other host":      {http.StatusOK, otherHost, ExitUnconfirmed, apiv1.ThreadUnconfirmed, "api_result_unconfirmed"},
		"host refused":    {http.StatusOK, hostRefusal, ExitFailed, apiv1.ThreadFailed, "not_codex_host"},
	} {
		t.Run(name, func(t *testing.T) {
			h, c := threadAPIHarness(t, serveThread(tc.status, tc.raw))
			h.mustRun(tc.exit, codexThreadArgs(h)...)
			r := threadResult(t, h)
			if r.Outcome != tc.outcome || r.Error.Code != tc.code || len(h.api.requests()) != 1 || c.calls != 0 {
				t.Fatalf("result %+v, %d requests, %d execs", r.Error, len(h.api.requests()), c.calls)
			}
			if tc.code == "route_unsupported" && (!strings.Contains(r.Error.Message, "/v1/sessions/<host>/threads") || !strings.Contains(r.Error.Message, "2.95.0")) {
				t.Fatalf("message %q names neither the route nor the version", r.Error.Message)
			}
		})
	}
}

// A request whose answer is lost after it was sent is unconfirmed and never
// repeated; one that never left agent-run failed and launched nothing.
func TestThreadStartCodexViaAPITransport(t *testing.T) {
	h, _ := threadAPIHarness(t, func(http.ResponseWriter, *http.Request, []byte) { panic(http.ErrAbortHandler) })
	h.mustRun(ExitUnconfirmed, codexThreadArgs(h)...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadUnconfirmed || r.Error.Code != "api_result_unconfirmed" || len(h.api.requests()) != 1 {
		t.Fatalf("result %+v, %d requests", r.Error, len(h.api.requests()))
	}
	h, _ = threadAPIHarness(t, serveThread(http.StatusOK, nil))
	h.vars[envAPIURL] = "https://127.0.0.1:1"
	h.mustRun(ExitFailed, codexThreadArgs(h)...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != "api_unreachable" || len(h.api.requests()) != 0 {
		t.Fatalf("result %+v", r.Error)
	}
}

// agent-run help thread says how a session pod is served.
func TestThreadHelpNamesTheAPIRoute(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "thread")
	contains(t, "help", h.stdout.String(), "ordinary session pod", "/v1/sessions/<host>/threads")
}
