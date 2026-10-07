package agentrun

import (
	"net/http"
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
		"/usr/bin/kubectl exec -n dev-agents " + name + " -c agent -it -- env TERM=screen-256color tmux attach-session -t agent",
		"/usr/bin/kubectl exec -n dev-agents " + name + " -c agent -- tmux detach-client -s agent",
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
	// and nothing is sent or run.
	h.vars["AGENTD_SESSION"] = `{"name":"other"}`
	n := len(h.api.requests())
	h.mustRun(ExitAuth, "attach", name)
	contains(t, "stderr", h.stderr.String(), "agent-run msg "+name)
	delete(h.vars, "AGENTD_SESSION")

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
