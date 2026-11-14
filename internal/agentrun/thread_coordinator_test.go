package agentrun

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func coordinatorThreadArgs(h *harness) []string {
	return []string{"thread", "start", "--agent", "codex", "--coordinator", "--host", threadHostName, "--model", "gpt-6.1-sol", "--effort", "medium", "--name", "coordinator-unique-1", "--handoff", h.write("handoff.txt", threadHandoff)}
}
func TestCoordinatorLaunchUsesAuthorizedAPINoFallback(t *testing.T) {
	for _, answer := range []string{"confirmed", "forbidden", "lost-ack", "missing-host"} {
		t.Run(answer, func(t *testing.T) {
			status := http.StatusOK
			raw := hostResult(t, apiv1.ThreadResponded, "")
			exit := ExitOK
			switch answer {
			case "forbidden":
				status = 403
				raw = []byte(`{"error":{"code":"forbidden","message":"coordinator gate refused"}}`)
				exit = ExitFailed
			case "lost-ack":
				status = 503
				raw = []byte(`{"error":{"code":"unavailable","message":"uncertain"}}`)
				exit = apiv1.ThreadLaunchExit(apiv1.ThreadUnconfirmed)
			case "missing-host":
				status = 404
				raw = []byte(`{"error":{"code":"not_found","message":"host missing"}}`)
				exit = ExitFailed
			}
			h, c := threadAPIHarness(t, serveThread(status, raw))
			h.vars[envWorkbench] = "1"
			h.mustRun(exit, coordinatorThreadArgs(h)...)
			if c.calls != 0 || len(h.api.requests()) != 1 {
				t.Fatalf("fallback: execs=%d requests=%v", c.calls, h.api.requests())
			}
			var req apiv1.ThreadStartRequest
			if json.Unmarshal(h.api.requests()[0].body, &req) != nil || !req.Coordinator || req.Name != "coordinator-unique-1" || req.Model != "gpt-6.1-sol" || req.Effort != "medium" {
				t.Fatalf("purpose lost: %+v", req)
			}
		})
	}
}
func TestCoordinatorLaunchRequiresExplicitHostAndModel(t *testing.T) {
	for _, drop := range []string{"--host", "--name", "--model", "--effort"} {
		t.Run(drop, func(t *testing.T) {
			h, c := threadAPIHarness(t, serveThread(200, nil))
			args := coordinatorThreadArgs(h)
			var filtered []string
			for i := 0; i < len(args); i++ {
				if args[i] == drop {
					i++
					continue
				}
				filtered = append(filtered, args[i])
			}
			h.mustRun(ExitUsage, filtered...)
			if c.calls != 0 || len(h.api.requests()) != 0 {
				t.Fatal("invalid coordinator launch reached selection or exec")
			}
		})
	}
}

func TestCoordinatorLaunchRejectsDifferentModelOrEffort(t *testing.T) {
	for _, change := range []string{"model", "effort"} {
		t.Run(change, func(t *testing.T) {
			h, c := threadAPIHarness(t, serveThread(200, nil))
			args := coordinatorThreadArgs(h)
			for i := range args {
				if change == "model" && args[i] == "gpt-6.1-sol" {
					args[i] = "gpt-6-astra"
				}
				if change == "effort" && args[i] == "medium" {
					args[i] = "high"
				}
			}
			h.mustRun(ExitUsage, args...)
			if c.calls != 0 || len(h.api.requests()) != 0 {
				t.Fatal("coordinator launch changed the requested model or effort")
			}
		})
	}
}
