package agentrun

import (
	"strings"
	"testing"
)

func TestHelp(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitOK, "help")
	contains(t, "help", h.stdout.String(), "agent-run [--repo] <repo> -p \"<task>\"", "Exit codes:", DefaultAPIURL)

	for cmd := range commandHelp {
		h.mustRun(ExitOK, "help", cmd)
		contains(t, "help "+cmd, h.stdout.String(), "Usage: agent-run")
		if cmd == "version" || cmd == "help" {
			continue
		}
		// -h on the command prints the same text and sends nothing.
		args := []string{cmd, "-h"}
		if cmd == "run" {
			args = []string{"--repo", "x", "-h"}
		}
		help := h.stdout.String()
		h.mustRun(ExitOK, args...)
		if h.stdout.String() != help {
			t.Errorf("%q printed %q, want help %s", args, h.stdout.String(), cmd)
		}
	}
	if n := len(h.api.requests()); n != 0 {
		t.Errorf("help sent %d requests", n)
	}

	h.mustRun(ExitUsage, "help", "grant")
	contains(t, "stderr", h.stderr.String(), `no command "grant"`)
	h.mustRun(ExitUsage, "help", "a", "b")
}

func TestDispatch(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage)
	contains(t, "stderr", h.stderr.String(), "Usage:")
	if h.stdout.Len() != 0 {
		t.Errorf("stdout = %q, want usage on stderr only", h.stdout.String())
	}

	h.mustRun(ExitUsage, "lsit")
	contains(t, "stderr", h.stderr.String(), `unknown command "lsit"; did you mean "list"? For a repository named lsit, use --repo lsit`)

	// Any other first word is the repository, as in v1.
	h.mustRun(ExitUsage, "haynes-ops")
	contains(t, "stderr", h.stderr.String(), `say what to run: -p "<task>"`)
	h.mustRun(ExitUsage, "haynes-ops", "--interactive")
	contains(t, "stderr", h.stderr.String(), "arrives with plan 03")

	for verb, want := range map[string]string{
		"attach":       "attach takes one session name",
		"detach":       "detach takes one session name",
		"prune":        "prune is gone in v2: the operator reaps sessions itself",
		"sweep":        "sweep is gone in v2",
		"codex-remote": "plan 04",
	} {
		h.mustRun(ExitUsage, verb)
		contains(t, verb, h.stderr.String(), want)
	}

	h.mustRun(ExitOK, "version")
	if !strings.HasPrefix(h.stdout.String(), "agent-run ") {
		t.Errorf("version printed %q", h.stdout.String())
	}
}

func TestNearestCommand(t *testing.T) {
	for word, want := range map[string]string{
		"lsit":       "list",
		"reaap":      "reap",
		"fleat":      "fleet",
		"detatch":    "detach",
		"haynes-ops": "",
		"dev-env":    "",
		"hass":       "",
	} {
		if got := nearestCommand(word); got != want {
			t.Errorf("%q: %q, want %q", word, got, want)
		}
	}
}
