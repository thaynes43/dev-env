package agentrun

import (
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// serveSession answers GET of one session with s.
func serveSession(h *harness, s apiv1.Session) {
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(s.Name) {
			writeTestJSON(w, http.StatusOK, s)
			return
		}
		apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such session")
	}
}

// --interactive creates a remote-mode session (Story110 T03): no prompt, no
// limits, the model as given, and hints for the pod-owned login and attach.
func TestCreateInteractive(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "haynes-ops", "--interactive", "--model", "claude-haiku-4-5")
	got := decodeCreate(t, h.api.requests()[0].body)
	if got.Mode != "remote" || got.Prompt != "" || got.Limits != nil || got.Agent != "claude" || got.Model != "claude-haiku-4-5" || got.Effort != "" {
		t.Errorf("create %+v", got)
	}
	contains(t, "stdout", h.stdout.String(), "agent-run login "+name+" url")
	contains(t, "stdout", h.stdout.String(), "agent-run attach "+name)
}

// --interactive with codex creates a remote-mode session too (Story110 T17): the
// daemon has no TUI and no login of its own, so the hint is agent-run pair.
func TestCreateInteractiveCodex(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "haynes-ops", "--interactive", "--agent", "codex", "--model", "gpt-6-luna")
	got := decodeCreate(t, h.api.requests()[0].body)
	if got.Mode != "remote" || got.Prompt != "" || got.Limits != nil || got.Agent != "codex" || got.Model != "gpt-6-luna" {
		t.Errorf("create %+v", got)
	}
	contains(t, "stdout", h.stdout.String(), "agent-run pair "+name)
	if strings.Contains(h.stdout.String(), "agent-run attach") || strings.Contains(h.stdout.String(), "agent-run login") {
		t.Errorf("a codex remote session has no TUI and no pod login: %q", h.stdout.String())
	}
}

// pair execs agentd's codex-pair control in the session's pod over the POST
// (SPDY) transport, like login. It takes no stdin and no code argument: the
// pairing code comes back on the exec's stdout only.
func TestPairExecsAgentdCtl(t *testing.T) {
	h := newHarness(t)
	h.kubectl = func([]string) int { return 0 }
	serveSession(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "pair", name)
	want := "env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + name + " -c agent -- agentd ctl codex-pair"
	if strings.Join(h.ran, "\n") != want {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(h.ran, "\n"), want)
	}
	h.mustRun(ExitUsage, "pair")
	contains(t, "stderr", h.stderr.String(), "pair takes one session name")
	h.mustRun(ExitUsage, "pair", name, "extra")
	h.kubectl = func([]string) int { return 1 }
	h.mustRun(ExitFailed, "pair", name)
	contains(t, "stderr", h.stderr.String(), "kubectl exec exited 1")
}

// login execs agentd's login control in the session's pod with kubectl. The
// code travels on stdin (-i), never as an argument.
func TestLoginExecsAgentdCtl(t *testing.T) {
	h := newHarness(t)
	h.kubectl = func([]string) int { return 0 }
	serveSession(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "login", name)
	h.mustRun(ExitOK, "login", name, "url")
	h.mustRun(ExitOK, "login", name, "code")
	base := "env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + name + " -c agent "
	want := []string{
		base + "-- agentd ctl login status",
		base + "-- agentd ctl login url",
		base + "-i -- agentd ctl login code",
	}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(h.ran, "\n"), strings.Join(want, "\n"))
	}
	// Every exec is the POST (SPDY) transport, never the default WebSocket GET.
	for _, line := range h.ran {
		if !strings.HasPrefix(line, "env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl ") {
			t.Errorf("exec without the SPDY transport: %q", line)
		}
	}
	h.mustRun(ExitUsage, "login", name, "token")
	contains(t, "stderr", h.stderr.String(), "url, code or status")
	h.mustRun(ExitUsage, "login")
	h.kubectl = func([]string) int { return 1 }
	h.mustRun(ExitFailed, "login", name, "url")
	contains(t, "stderr", h.stderr.String(), "kubectl exec exited 1")
}

// --local creates a local session: no prompt, no limits, and a hint to attach
// (D-58).
func TestCreateLocal(t *testing.T) {
	h := newHarness(t)
	serveCreate(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "haynes-ops", "--local", "--size", "s")
	got := decodeCreate(t, h.api.requests()[0].body)
	if got.Mode != "local" || got.Prompt != "" || got.Limits != nil || got.Size != "S" {
		t.Errorf("create %+v", got)
	}
	contains(t, "stdout", h.stdout.String(), "agent-run attach "+name)
}

// attach execs tmux in the session's pod with kubectl, after the API says the
// pod runs; detach detaches every client.
func TestAttachAndDetach(t *testing.T) {
	h := newHarness(t)
	h.vars["TERM"] = "screen-256color"
	h.kubectl = func([]string) int { return 0 }
	serveSession(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "attach", name)
	h.mustRun(ExitOK, "detach", name)
	want := []string{
		"env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + name + " -c agent -it -- env TERM=screen-256color tmux attach-session -t agent",
		"env KUBECTL_REMOTE_COMMAND_WEBSOCKETS=false /usr/bin/kubectl --context test-context exec -n dev-agents " + name + " -c agent -- tmux detach-client -s agent",
	}
	if strings.Join(h.ran, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(h.ran, "\n"), strings.Join(want, "\n"))
	}

	// kubectl's own failure is agent-run's.
	h.kubectl = func([]string) int { return 1 }
	h.mustRun(ExitFailed, "detach", name)
	contains(t, "stderr", h.stderr.String(), "kubectl exec exited 1")
}

func TestAttachRefusals(t *testing.T) {
	h := newHarness(t)
	h.kubectl = func([]string) int { return 0 }

	// A session with no running pod has nothing to attach to.
	suspended := testSession(name, "Suspended")
	serveSession(h, suspended)
	h.mustRun(ExitFailed, "attach", name)
	contains(t, "stderr", h.stderr.String(), name+" is Suspended")

	// An agent has no exec into another pod (D-19): it is told to use msg,
	// and nothing is sent or run. A session pod is known by its projected
	// token, its variable or its file, as connect finds it: agentd removes
	// AGENTD_SESSION from the agent's environment.
	n := len(h.api.requests())
	h.vars[envAgentdTokenFile] = h.write("projected-token", "tok")
	h.mustRun(ExitAuth, "attach", name)
	contains(t, "stderr", h.stderr.String(), "agent-run msg "+name)
	delete(h.vars, envAgentdTokenFile)
	h.env.SessionTokenFile = h.write("var/run/secrets/dev-env/token", "tok")
	h.mustRun(ExitAuth, "detach", name)
	h.env.SessionTokenFile = filepath.Join(h.dir, "no-session-token")

	// No kubectl.
	h.kubectl = nil
	h.mustRun(ExitFailed, "attach", name)
	contains(t, "stderr", h.stderr.String(), "kubectl is not on PATH")
	if len(h.api.requests()) != n || len(h.ran) != 0 {
		t.Errorf("requests %d (want %d), ran %q", len(h.api.requests()), n, h.ran)
	}

	h.mustRun(ExitUsage, "attach")
	h.mustRun(ExitUsage, "detach", "a", "b")
}

// The code is typed or piped on stdin. It is never an argument, an environment
// entry, output, or a request to the API. The fake kubectl reads the process's
// stdin, as the real child does, so a forwarding bug (no -i, stdin dropped)
// fails here (Story110 T03).
func TestLoginCodeStaysOffArgvEnvAndOutput(t *testing.T) {
	const canary = "canarycode0123456789abcdef#canarystate0123456789"
	h := newHarness(t)
	h.env.Stdin = strings.NewReader(canary + "\n")
	var got string
	h.kubectl = func(argv []string) int {
		// The child reads the stdin it was given, as kubectl exec -i would.
		if slices.Contains(argv, "-i") && h.childStdin != nil {
			b, _ := io.ReadAll(h.childStdin)
			got = string(b)
		}
		return 0
	}
	serveSession(h, runningOn(testSession(name, ""), "talosw02"))
	h.mustRun(ExitOK, "login", name, "code")
	if strings.TrimSpace(got) != canary {
		t.Errorf("the code did not reach the exec child's stdin: %q", got)
	}
	all := strings.Join(h.ran, "\n") + h.stdout.String() + h.stderr.String()
	for _, r := range h.api.requests() {
		all += r.path + string(r.body)
	}
	for k, v := range h.vars {
		all += k + "=" + v
	}
	if strings.Contains(all, "canary") {
		t.Errorf("the code leaked:\n%s", all)
	}
	if !strings.Contains(strings.Join(h.ran, " "), " -i -- agentd ctl login code") {
		t.Errorf("code is not forwarded on stdin: %v", h.ran)
	}
}
