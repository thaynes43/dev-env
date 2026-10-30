package agentrun

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func freshRequest(h *harness) apiv1.CodexRCRequest {
	return apiv1.CodexRCRequest{RequestID: "launch-272", SessionUID: "session-uid", PodUID: "pod-uid", Project: "sample", Repository: "repo", Model: "gpt-6.1-sol", Effort: "medium", Handoff: rcTestText, Deadline: h.now.Add(25 * time.Second)}
}
func freshReceipt(h *harness, req apiv1.CodexRCRequest) apiv1.CodexRCReceipt {
	target := apiv1.CodexRCTarget{SessionName: "rc-host", SessionUID: req.SessionUID, PodUID: req.PodUID, Project: req.Project, Repository: req.Repository, SnapshotSHA256: strings.Repeat("a", 64), Root: "/home/dev/codex/sample/repo", RootDevice: 1, RootInode: 2, PreparationSHA256: strings.Repeat("b", 64), PID: 200, BootID: "boot", StartTicks: 4, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("c", 64), VerifiedAt: h.now, ExpiresAt: h.now.Add(5 * time.Minute)}
	rec := apiv1.CodexRCReceipt{Caller: "human", RequestID: req.RequestID, ClientSHA256: apiv1.CodexRCClientHash("human", req), DispatchSHA256: strings.Repeat("d", 64), Target: target, Model: req.Model, Effort: req.Effort, State: "confirmed", Reason: "thread_and_turn_acknowledged", ThreadID: "new-thread", TurnID: "new-turn", StartedAt: h.now, FinishedAt: h.now}
	rec.DispatchSHA256 = apiv1.CodexRCHash(apiv1.CodexRCDispatch{Caller: rec.Caller, Request: req, Target: target, ClientSHA256: rec.ClientSHA256})
	return rec
}
func freshArgs(h *harness, req apiv1.CodexRCRequest) []string {
	raw, _ := json.Marshal(req)
	return []string{"rc", "fresh", "--session", "rc-host", "--request", h.write("request.json", string(raw)), "-o", "json"}
}
func TestCodexRCFreshOneShotACK(t *testing.T) {
	h := newHarness(t)
	req := freshRequest(h)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Method != "POST" || r.URL.Path != "/v1/sessions/rc-host/codex-thread" {
			t.Fatal("unexpected operation", r.Method, r.URL.Path)
		}
		var got apiv1.CodexRCRequest
		if apiv1.DecodeCodexRC(body, &got, apiv1.CodexRCMaxRequest) != nil || got != req {
			t.Fatal("request changed")
		}
		writeTestJSON(w, http.StatusCreated, freshReceipt(h, req))
	}
	h.mustRun(ExitOK, freshArgs(h, req)...)
	if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || strings.Contains(h.stdout.String()+h.stderr.String(), rcTestText) || !strings.Contains(h.stdout.String(), `"phoneRegistration": "unobserved"`) {
		t.Fatal("request retried, handoff leaked, or phone inferred", h.stdout.String(), h.stderr.String())
	}
}
func TestCodexRCFreshRefusalAndUncertaintyNeverRetry(t *testing.T) {
	for _, status := range []int{301, 307, 400, 401, 403, 409, 429, 500, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			req := freshRequest(h)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
				w.Header().Set("Location", h.api.srv.URL+"/unexpected")
				writeTestJSON(w, status, apiv1.ErrorResponse{Error: apiv1.Error{Code: "refused", Message: "fixed refusal"}})
			}
			exit := h.run(freshArgs(h, req)...)
			if exit == ExitOK || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
				t.Fatalf("unexpected exit/retry %d", exit)
			}
			if status >= 500 && exit != ExitUnconfirmed {
				t.Fatalf("uncertain write hinted retry: %d", exit)
			}
		})
	}
}
func TestCodexRCFreshRejectsWrongACK(t *testing.T) {
	for _, mode := range []string{"thread missing", "turn missing", "wrong model", "wrong target", "wrong hash", "wrong dispatch", "oversize", "duplicate", "unknown field", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			req := freshRequest(h)
			rec := freshReceipt(h, req)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
				switch mode {
				case "thread missing":
					rec.ThreadID = ""
				case "turn missing":
					rec.TurnID = ""
				case "wrong model":
					rec.Model = "gpt-6-astra"
				case "wrong target":
					rec.Target.SessionUID = "other"
				case "wrong dispatch":
					rec.DispatchSHA256 = ""
				case "wrong hash":
					rec.ClientSHA256 = strings.Repeat("e", 64)
				}
				raw, _ := json.Marshal(rec)
				switch mode {
				case "oversize":
					raw = append(raw, []byte(strings.Repeat(" ", 8193))...)
				case "duplicate":
					raw = append([]byte(`{"state":"confirmed",`), raw[1:]...)
				case "unknown field":
					raw = append([]byte(`{"extra":true,`), raw[1:]...)
				case "malformed":
					raw = []byte(`{`)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(raw)
			}
			h.mustRun(ExitUnconfirmed, freshArgs(h, req)...)
			if len(h.api.requests()) != 1 {
				t.Fatal("retried")
			}
		})
	}
}
func TestCodexRCFreshInvalidInputIsLocal(t *testing.T) {
	for _, mode := range []string{"expired", "oversize handoff", "existing thread", "duplicate field", "missing UID"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			req := freshRequest(h)
			switch mode {
			case "expired":
				req.Deadline = h.now
			case "oversize handoff":
				req.Handoff = strings.Repeat("a", 4097)
			case "missing UID":
				req.PodUID = ""
			}
			args := freshArgs(h, req)
			raw, _ := json.Marshal(req)
			switch mode {
			case "existing thread":
				raw = append([]byte(`{"threadId":"old",`), raw[1:]...)
			case "duplicate field":
				raw = append([]byte(`{"model":"gpt-6.1-sol",`), raw[1:]...)
			}
			args[5] = h.write("request.json", string(raw))
			h.mustRun(ExitUsage, args...)
			if len(h.api.requests()) != 0 {
				t.Fatal("invalid input reached API")
			}
		})
	}
}

func TestCodexRCFreshHandoffHTMLDoesNotExpandPastWireLimit(t *testing.T) {
	h := newHarness(t)
	req := freshRequest(h)
	req.Handoff = strings.Repeat("<&", 2048)
	// A valid bounded input document must remain valid after typed re-encoding.
	raw, _ := json.Marshal(req)
	raw = []byte(strings.ReplaceAll(strings.ReplaceAll(string(raw), `\u003c`, "<"), `\u0026`, "&"))
	args := freshArgs(h, freshRequest(h))
	args[5] = h.write("request.json", string(raw))
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
		var got apiv1.CodexRCRequest
		if apiv1.DecodeCodexRC(b, &got, apiv1.CodexRCMaxRequest) != nil || got != req {
			t.Error("valid handoff expanded or changed")
		}
		writeTestJSON(w, http.StatusCreated, freshReceipt(h, req))
	}
	h.mustRun(ExitOK, args...)
	if len(h.api.requests()) != 1 {
		t.Fatal("request not sent exactly once")
	}
}
