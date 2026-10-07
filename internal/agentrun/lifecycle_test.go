package agentrun

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// suspend posts to each session's suspend route and says what happens; a
// session already suspended is a 200 (D-60).
func TestSuspend(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionSuspendPath("a"):
			writeTestJSON(w, http.StatusAccepted, testSession("a", "Running"))
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionSuspendPath("b"):
			writeTestJSON(w, http.StatusOK, testSession("b", "Suspended"))
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such session")
		}
	}
	h.mustRun(ExitOK, "suspend", "a", "b")
	contains(t, "stdout", h.stdout.String(), "suspending a: the operator rescues its work", "agent-run resume a", "b is already suspended")
	h.mustRun(ExitNotFound, "suspend", "a", "nope")
	contains(t, "stderr", h.stderr.String(), "nope:")
	h.mustRun(ExitUsage, "suspend")
}

// resume posts, then waits through Suspended for the new pod, and says how to
// attach.
func TestResume(t *testing.T) {
	h := newHarness(t)
	var gets atomic.Int32
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionResumePath(name):
			writeTestJSON(w, http.StatusAccepted, testSession(name, "Suspended"))
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath(name):
			switch gets.Add(1) {
			case 1:
				writeTestJSON(w, http.StatusOK, testSession(name, "Suspended"))
			case 2:
				writeTestJSON(w, http.StatusOK, pendingOn(testSession(name, ""), "ContainerCreating", "ContainerCreating"))
			default:
				writeTestJSON(w, http.StatusOK, runningOn(testSession(name, ""), "talosw03"))
			}
		default:
			apiError(w, http.StatusNotFound, apiv1.CodeNotFound, "no such session")
		}
	}
	h.mustRun(ExitOK, "resume", name)
	contains(t, "stdout", h.stdout.String(), "resuming "+name, "Running on talosw03.", "agent-run attach "+name)
	if n := gets.Load(); n != 3 {
		t.Errorf("%d polls, want 3", n)
	}
	h.mustRun(ExitUsage, "resume")
	h.mustRun(ExitUsage, "resume", "a", "b")
}
