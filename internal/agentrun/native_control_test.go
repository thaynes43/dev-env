package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func TestBudgetNativeOneShotAndLostResponse(t *testing.T) {
	for _, scenario := range []string{"read", "lost", "redirect", "wrong-binding", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			req := apiv1.NativeControlRequest{Binding: budgetReceipt().Binding, ID: "explicit-once", Operation: "thread/read", ThreadID: "thread-a"}
			if scenario == "invalid" {
				req.Operation = "arbitrary/rpc"
			}
			body, _ := json.Marshal(req)
			h.env.Stdin = strings.NewReader(string(body))
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				if scenario == "lost" {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = conn.Close()
					return
				}
				if scenario == "redirect" {
					http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
					return
				}
				response := apiv1.NativeControlResponse{Binding: req.Binding, ID: req.ID, Operation: req.Operation, Result: json.RawMessage(`{"thread":{"id":"thread-a"}}`)}
				if scenario == "wrong-binding" {
					response.Binding.PodUID = "other"
				}
				writeTestJSON(w, http.StatusOK, response)
			}
			want := ExitUnconfirmed
			if scenario == "read" {
				want = ExitOK
			}
			if scenario == "invalid" {
				want = ExitUsage
			}
			if scenario == "redirect" {
				want = ExitFailed
			}
			h.mustRun(want, "budget", "native", "campaign-a", "--once")
			count := 1
			if scenario == "invalid" {
				count = 0
			}
			if len(h.api.requests()) != count || len(h.sleeps) != 0 || len(h.ran) != 0 {
				t.Fatal("request replayed or subprocess", h.api.requests())
			}
		})
	}
}

func TestNativeStopConfirmsEquivalentDeadlineOffset(t *testing.T) {
	h := newHarness(t)
	st := budgetReceipt()
	st.NativeStopRequested = true
	st.StopConfirmed = false
	r := apiv1.NativeControlRequest{Binding: st.Binding, ID: "owner-stop", Operation: "host/stop"}
	r.Binding.Deadline = r.Binding.Deadline.In(time.FixedZone("alternate", -4*60*60))
	body, _ := json.Marshal(r)
	h.env.Stdin = strings.NewReader(string(body))
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusAccepted, st) }
	h.mustRun(ExitOK, "budget", "native", "campaign-a", "--once")
	if len(h.api.requests()) != 1 {
		t.Fatal("stop repeated")
	}
}
