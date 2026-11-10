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

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const statusThread = "0199a000-0000-7000-8000-000000000002"

func statusArgs(extra ...string) []string {
	return append([]string{"thread", "status", "--agent", "codex", "--host", threadHostName, "--thread", statusThread}, extra...)
}

// noLaunch fails the test if a status run started a thread or sent an input:
// no thread-start, no exec stdin (-i), nothing on stdin and no POST.
func noLaunch(t *testing.T, h *harness, c *capture) {
	t.Helper()
	if slices.Contains(c.argv, "thread-start") || slices.Contains(c.argv, "-i") || c.stdin != "" {
		t.Fatalf("a status exec can launch or carry input: %q stdin %q", c.argv, c.stdin)
	}
	for _, req := range h.api.requests() {
		if req.method != http.MethodGet {
			t.Fatalf("a status run sent %s %s", req.method, req.path)
		}
	}
}

// thread status execs agentd ctl thread-status in the host over the route
// thread start uses, with no stdin, and relays the host's result and exit:
// completed, response pending and failed.
func TestThreadStatusCodexExecsHost(t *testing.T) {
	for name, tc := range map[string]struct {
		outcome, code string
		exit          int
	}{
		"completed":        {apiv1.ThreadResponded, "", ExitOK},
		"response pending": {apiv1.ThreadVisible, "response_pending", ExitPending},
		"failed":           {apiv1.ThreadFailed, "turn_failed", ExitFailed},
	} {
		t.Run(name, func(t *testing.T) {
			h, c := threadHarness(t, runningOn(codexHostView(""), "talosw02"), tc.exit, hostResult(t, tc.outcome, tc.code))
			h.mustRun(tc.exit, statusArgs("--repo", "dev-env")...)
			want := "env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + threadHostName +
				" -c agent -- agentd ctl thread-status --thread " + statusThread + " --repo dev-env"
			if strings.Join(c.argv, " ") != want || c.calls != 1 {
				t.Fatalf("exec (%d)\n%s\nwant\n%s", c.calls, strings.Join(c.argv, " "), want)
			}
			noLaunch(t, h, c)
			r := threadResult(t, h)
			if r.Outcome != tc.outcome || r.ThreadID != statusThread || (r.Error == nil) != (tc.code == "") || (r.Error != nil && r.Error.Code != tc.code) {
				t.Fatalf("result %+v", r)
			}
		})
	}
	// Without --repo the host uses its own repo: the flag is not sent.
	h, c := threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, hostResult(t, apiv1.ThreadResponded, ""))
	h.mustRun(ExitOK, statusArgs()...)
	if slices.Contains(c.argv, "--repo") || c.argv[len(c.argv)-1] != statusThread {
		t.Fatalf("exec %q", c.argv)
	}
	// A workbench session is a session pod with exec: it keeps this route.
	h, c = threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, hostResult(t, apiv1.ThreadResponded, ""))
	h.vars[envAgentdTokenFile] = h.write("pod-token", "x")
	h.vars[envWorkbench] = "1"
	h.mustRun(ExitOK, statusArgs()...)
	if c.calls != 1 {
		t.Fatalf("execs %d", c.calls)
	}
	noLaunch(t, h, c)
}

// No answer but one valid result for this thread is relayed. Status launched
// nothing, so a lost or invalid answer is a failure that is safe to repeat,
// never an unconfirmed launch.
func TestThreadStatusHostAnswers(t *testing.T) {
	otherThread := bytes.ReplaceAll(hostResult(t, apiv1.ThreadResponded, ""), []byte(statusThread), []byte("0199a000-0000-7000-8000-000000000009"))
	otherHost := bytes.ReplaceAll(hostResult(t, apiv1.ThreadResponded, ""), []byte(threadHostName), []byte("other-host"))
	cases := []struct {
		name   string
		code   int
		out    []byte
		stderr string
		want   string
	}{
		{"agentd without thread-status", 2, nil, "agentd: usage: ctl status | rescue\n", "status_unsupported"},
		{"crash", 2, nil, "panic: boom\n", "status_unavailable"},
		{"kubectl refused", 1, nil, "error: unable to upgrade connection: Forbidden\n", "exec_failed"},
		{"exit mismatch", 0, hostResult(t, apiv1.ThreadVisible, "response_pending"), "", "status_unavailable"},
		{"garbage", 1, []byte("not a result\n"), "", "status_unavailable"},
		{"two lines", 0, append(hostResult(t, apiv1.ThreadResponded, ""), hostResult(t, apiv1.ThreadResponded, "")...), "", "status_unavailable"},
		{"another thread", 0, otherThread, "", "status_unavailable"},
		{"another host", 0, otherHost, "", "status_unavailable"},
		// A launch's ends are not a status pass's: nothing was launched or waited for.
		{"unconfirmed", 6, hostResult(t, apiv1.ThreadUnconfirmed, "launch_unconfirmed"), "", "status_unavailable"},
		{"timed out", 7, hostResult(t, apiv1.ThreadTimedOut, "launch_deadline"), "", "status_unavailable"},
		{"blocked", 1, hostResult(t, apiv1.ThreadBlocked, "provider_not_qualified"), "", "status_unavailable"},
		{"visible without the thread", 7, bytes.ReplaceAll(hostResult(t, apiv1.ThreadVisible, "response_pending"), []byte(`"threadId":"`+statusThread+`",`), nil), "", "status_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, c := threadHarness(t, runningOn(codexHostView(""), "talosw02"), tc.code, tc.out)
			c.stderr = tc.stderr
			h.mustRun(ExitFailed, statusArgs()...)
			r := threadResult(t, h)
			if r.Outcome != apiv1.ThreadFailed || r.Error.Code != tc.want || r.Error.Message == "" || c.calls != 1 {
				t.Fatalf("result %s %+v, %d execs", r.Outcome, r.Error, c.calls)
			}
			noLaunch(t, h, c)
		})
	}
	h, _ := threadHarness(t, runningOn(codexHostView(""), "talosw02"), 0, nil)
	h.env.RunCapture = func(context.Context, []string, io.Reader, io.Writer) (int, []byte, error) {
		return 0, nil, errors.New("kubectl lost")
	}
	h.mustRun(ExitFailed, statusArgs()...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != "status_unavailable" {
		t.Fatalf("result %+v", r)
	}
	// A pass that stopped before it found the thread names none, and is relayed.
	early := bytes.ReplaceAll(hostResult(t, apiv1.ThreadFailed, "thread_not_found"), []byte(`"threadId":"`+statusThread+`",`), nil)
	h, _ = threadHarness(t, runningOn(codexHostView(""), "talosw02"), 1, early)
	h.mustRun(ExitFailed, statusArgs()...)
	if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != "thread_not_found" || r.ThreadID != "" {
		t.Fatalf("result %+v", r)
	}
}

// Every result agent-run makes itself for a status says its permission is the
// launch contract, not an observation, and that the client was not looked at.
func TestThreadStatusOwnResultsAreStatusResults(t *testing.T) {
	h, _ := threadHarness(t, codexHostView("Suspended"), 0, nil)
	h.mustRun(ExitFailed, statusArgs()...)
	r := threadResult(t, h)
	if r.Permission.Source != "launch_contract" || r.Phases[apiv1.ThreadPhaseClientStarted].Reason != "not_observed_by_status" || r.DeadlineMs != 0 {
		t.Fatalf("result %+v", r)
	}
	h = newHarness(t)
	h.mustRun(ExitUsage, "thread", "status", "--agent", "codex", "--host", "h-1")
	if r := threadResult(t, h); r.Permission.Source != "launch_contract" {
		t.Fatalf("permission %+v", r.Permission)
	}
}

// The host must be a Running Codex remote session, as for thread start: none
// of these refusals execs anything.
func TestThreadStatusRefusesBeforeExec(t *testing.T) {
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
			h.mustRun(ExitFailed, statusArgs()...)
			if r := threadResult(t, h); r.Outcome != apiv1.ThreadFailed || r.Error.Code != tc.code || c.calls != 0 {
				t.Fatalf("result %+v calls %d", r.Error, c.calls)
			}
			noLaunch(t, h, c)
		})
	}
}

// Status takes a provider, a host, a thread id and a repo. It takes nothing a
// launch takes: a handoff, model, effort, name or wait is a usage error.
func TestThreadStatusInvalidInput(t *testing.T) {
	h := newHarness(t)
	good := h.write("good.txt", threadHandoff)
	for name, args := range map[string][]string{
		"no agent":      {"thread", "status", "--host", "h-1", "--thread", statusThread},
		"unknown agent": {"thread", "status", "--agent", "gemini", "--host", "h-1", "--thread", statusThread},
		"no host":       {"thread", "status", "--agent", "codex", "--thread", statusThread},
		"bad host":      {"thread", "status", "--agent", "codex", "--host", "a/b", "--thread", statusThread},
		"no thread":     {"thread", "status", "--agent", "codex", "--host", "h-1"},
		"thread name":   {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", "thread-1109-180000"},
		"upper thread":  {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", strings.ToUpper(statusThread)},
		"bad repo":      {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--repo", "../x"},
		"positional":    {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "extra"},
		"text output":   {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "-o", "text"},
		"handoff":       {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--handoff", good},
		"model":         {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--model", "gpt-6-astra"},
		"effort":        {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--effort", "high"},
		"name":          {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--name", "t-1"},
		"wait":          {"thread", "status", "--agent", "codex", "--host", "h-1", "--thread", statusThread, "--wait", "1m"},
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

// Only Codex has an observable first response. Claude, Cursor and Grok are
// refused before any request.
func TestThreadStatusOtherProviders(t *testing.T) {
	for agent, want := range map[string][2]string{
		"claude": {apiv1.ThreadFailed, "provider_observation_unavailable"},
		"cursor": {apiv1.ThreadBlocked, "provider_not_qualified"},
		"grok":   {apiv1.ThreadBlocked, "provider_not_qualified"},
	} {
		h := newHarness(t)
		h.mustRun(ExitFailed, "thread", "status", "--agent", agent, "--host", "h-1", "--thread", statusThread)
		if r := threadResult(t, h); r.Outcome != want[0] || r.Error.Code != want[1] || r.Error.Message == "" || len(h.api.requests()) != 0 {
			t.Fatalf("%s: %s %+v", agent, r.Outcome, r.Error)
		}
	}
}

// An ordinary session pod has no exec, and thread start's API route carries a
// launch only: status is refused by name, with no request and no exec. It
// never falls back to the launch route.
func TestThreadStatusSessionPodHasNoRoute(t *testing.T) {
	h, c := threadAPIHarness(t, func(http.ResponseWriter, *http.Request, []byte) {
		t.Error("thread status reached the API from a session pod")
	})
	h.mustRun(ExitFailed, statusArgs()...)
	r := threadResult(t, h)
	if r.Outcome != apiv1.ThreadFailed || r.Error.Code != "status_route_unavailable" || !strings.Contains(r.Error.Message, "workbench") || len(h.api.requests()) != 0 || c.calls != 0 {
		t.Fatalf("result %+v, %d requests, %d execs", r.Error, len(h.api.requests()), c.calls)
	}
}

// agent-run help thread documents status as observe-only.
func TestThreadHelpNamesStatus(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help", "thread")
	contains(t, "help", h.stdout.String(), "agent-run thread status --agent codex --host <codex-host> --thread <id>", "never launches", "status_route_unavailable")
	h = newHarness(t)
	h.mustRun(ExitOK, "thread", "status", "--help")
	contains(t, "help", h.stdout.String(), "agent-run thread status")
}
