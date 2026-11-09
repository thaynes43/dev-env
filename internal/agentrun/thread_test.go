package agentrun

import (
	"bytes"
	"context"
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

// The host must be a Running Codex remote session, and the caller must not be
// an ordinary session pod (D-19): none of these refusals execs anything.
func TestThreadStartCodexRefusesBeforeExec(t *testing.T) {
	claude := runningOn(codexHostView(""), "talosw02")
	claude.Agent = "claude"
	task := runningOn(codexHostView(""), "talosw02")
	task.Mode = "task"
	for name, tc := range map[string]struct {
		view apiv1.Session
		pod  bool
		code string
	}{
		"claude host":  {claude, false, "not_codex_host"},
		"codex task":   {task, false, "not_codex_host"},
		"not running":  {codexHostView("Suspended"), false, "host_not_running"},
		"session pod":  {runningOn(codexHostView(""), "talosw02"), true, "unauthenticated"},
		"not anywhere": {apiv1.Session{Name: "missing"}, false, "exec_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			h, c := threadHarness(t, tc.view, 0, nil)
			if tc.pod {
				h.vars[envAgentdTokenFile] = h.write("pod-token", "x")
			}
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
