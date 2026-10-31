package agentrun

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const rcTestLink = "https://claude.ai/code/session_synthetic268"
const rcTestText = "SYNTHETIC_PRIVATE_HANDOFF_268"

func rcView(level int) apiv1.Session {
	v := testSession("remote-268", "Pending")
	v.Base, v.Mode, v.IdempotencyKey = "main", "remote", "agent-run-testkey"
	if level >= 1 {
		v.Conditions = append(v.Conditions, apiv1.Condition{Type: "PodScheduled", Status: "True"})
	}
	if level >= 2 {
		v = runningOn(v, "synthetic-node")
	}
	if level >= 3 {
		v.AgentStatus = &apiv1.AgentStatus{State: "running", ConversationID: "synthetic-conversation"}
	}
	if level >= 4 {
		v.RemoteControl = &apiv1.RemoteControl{State: "registered"}
	}
	if level >= 5 {
		v.RemoteControl.URL = rcTestLink
	}
	return v
}

func rcArgs(h *harness, extra ...string) []string {
	args := []string{"rc", "start", "--project", "sample", "--agent", "claude", "--model", "claude-opus-5-5", "--handoff", h.write("handoff.txt", rcTestText), "-o", "json"}
	return append(args, extra...)
}

func rcResult(t *testing.T, h *harness) apiv1.RCLaunchResult {
	t.Helper()
	raw := h.stdout.Bytes()
	if len(raw) > 32<<10 || !json.Valid(raw) || !strings.HasPrefix(string(raw), `{"link":`) {
		t.Fatalf("unbounded, invalid, or link not first: %d bytes", len(raw))
	}
	var v apiv1.RCLaunchResult
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != "rc-launch.v1" || v.PollIntervalMs != 2000 || len(v.Phases) != 11 {
		t.Fatalf("incomplete contract: %+v", v)
	}
	for name, phase := range v.Phases {
		switch phase.State {
		case "ok", "error", "blocked", "unknown", "not_applicable":
		default:
			t.Errorf("invalid %s state: %s", name, phase.State)
		}
		if phase.Reason == "" || phase.EvidenceSource == "" || (phase.FirstObservedAt != nil && phase.FirstObservedAt.Location() != time.UTC) || phase.SourceAt != nil || (phase.SinceRequestMs == nil) != (phase.FirstObservedAt == nil) || sinceMs(phase) > v.TotalMs {
			t.Errorf("invalid %s phase: %+v", name, phase)
		}
		if phase.Reason == "not_observed" && phase.FirstObservedAt != nil {
			t.Errorf("unobserved %s phase has a timestamp", name)
		}
	}
	if v.Phases["provider_auth_ok"].FirstObservedAt != nil || v.Phases["cli_recorded"].FirstObservedAt != nil {
		t.Error("unavailable provider/input evidence has a timestamp")
	}
	if v.Error != nil && v.Error.Message == "" {
		t.Error("error lacks a fixed diagnostic message")
	}
	if strings.Contains(string(raw)+h.stderr.String(), rcTestText) {
		t.Fatal("private handoff appeared in output")
	}
	return v
}

func rcServe(h *harness, createStatus int, view func(int) apiv1.Session, messageStatus int) {
	polls := 0
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionsPath:
			req := decodeCreate(h.t, b)
			if req.Project != "sample" || req.Agent != "claude" || req.Mode != "remote" || req.Model != "claude-opus-5-5" || req.IdempotencyKey == "" || req.Prompt != "" {
				h.t.Error("create lost project/remote/key or included prompt")
			}
			v := view(0)
			v.IdempotencyKey = req.IdempotencyKey
			writeTestJSON(w, createStatus, v)
		case r.Method == http.MethodGet && r.URL.Path == apiv1.SessionPath("remote-268"):
			if r.Header.Get(apiv1.SessionTaskClaimsHeader) != "" {
				h.t.Error("rc loop requested task claims")
			}
			polls++
			writeTestJSON(w, http.StatusOK, view(polls))
		case r.Method == http.MethodPost && r.URL.Path == apiv1.SessionMessagesPath("remote-268"):
			var msg apiv1.MessageRequest
			if json.Unmarshal(b, &msg) != nil || msg.Text != rcTestText {
				h.t.Error("handoff changed")
			}
			writeTestJSON(w, messageStatus, apiv1.MessageResult{Session: "remote-268", Delivered: true})
		default:
			h.t.Error("unexpected route", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func rcCountMessages(h *harness) int {
	n := 0
	for _, req := range h.api.requests() {
		if req.method == http.MethodPost && strings.HasSuffix(req.path, "/messages") {
			n++
		}
	}
	return n
}

func TestRCHappyPathAndKeyReplay(t *testing.T) {
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			h := newHarness(t)
			rcServe(h, status, func(poll int) apiv1.Session { return rcView(min(poll, 5)) }, http.StatusAccepted)
			wantExit, wantOutcome, wantMessages := ExitOK, "link_available", 1
			if status == http.StatusOK {
				wantExit, wantOutcome, wantMessages = ExitUnconfirmed, "unconfirmed", 0
			}
			h.mustRun(wantExit, rcArgs(h)...)
			r := rcResult(t, h)
			if r.Outcome != wantOutcome || r.Link == nil || *r.Link != rcTestLink || !r.RCRegistered || r.Polls != 5 || r.TotalMs != 10000 || r.DeadlineMs != 300000 || r.IdempotencyKey != "agent-run-testkey" || rcCountMessages(h) != wantMessages {
				t.Fatalf("unexpected result: %+v", r)
			}
			for _, phase := range []string{"provider_auth_ok", "rc_ready", "cli_recorded"} {
				if r.Phases[phase].State != "unknown" {
					t.Error("unearned readiness/input evidence", phase)
				}
			}
			if status == http.StatusOK {
				if r.Error == nil || r.Error.Code != "handoff_unconfirmed" || r.Phases["api_accepted"].Reason != "reused_by_key" || r.Phases["handoff"].State != "unknown" || r.Phases["handoff"].Reason != "reused_by_key_not_resent" {
					t.Fatal("replay uncertainty not recorded", r)
				}
			} else if r.Error != nil || r.Phases["handoff"].State != "ok" || r.Phases["handoff"].Reason != "transport_submitted" {
				t.Fatal("transport submission not recorded", r)
			}
			if r.Phases["pairing_required"].State != "not_applicable" {
				t.Fatal("pairing state")
			}
			if sinceMs(r.Phases["pod_scheduled"]) != 2000 || sinceMs(r.Phases["pod_ready"]) != 4000 || sinceMs(r.Phases["agent_started"]) != 6000 || sinceMs(r.Phases["link_available"]) != 10000 {
				t.Fatal("phase timings did not use the fake clock")
			}
			if r.Phases["pod_scheduled"].FirstObservedAt == nil || !r.Phases["pod_scheduled"].FirstObservedAt.Equal(testNow.Add(2*time.Second)) {
				t.Fatal("observed phase lacks its timestamp")
			}
		})
	}
}

func TestRCKeyReuseAfterUnconfirmedHandoffDoesNotSendChangedText(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(5) }, http.StatusInternalServerError)
	h.mustRun(ExitUnconfirmed, rcArgs(h)...)
	if rcResult(t, h).Error.Code != "handoff_unconfirmed" || rcCountMessages(h) != 1 {
		t.Fatal("missing initial unconfirmed message")
	}
	rcServe(h, http.StatusOK, func(int) apiv1.Session { return rcView(5) }, http.StatusAccepted)
	changedText := "SYNTHETIC_CHANGED_HANDOFF_268"
	h.mustRun(ExitUnconfirmed, rcArgs(h, "--handoff", h.write("changed.txt", changedText))...)
	r := rcResult(t, h)
	if r.Outcome != "unconfirmed" || r.Error.Code != "handoff_unconfirmed" || r.Link == nil || r.Phases["handoff"].Reason != "reused_by_key_not_resent" || rcCountMessages(h) != 1 || strings.Contains(h.stdout.String()+h.stderr.String(), changedText) {
		t.Fatal("key reuse resent or exposed the changed handoff", r)
	}
}

func TestRCPhaseTimeoutsAndGlobalDeadline(t *testing.T) {
	for level, stage := range rcStages {
		for _, deadline := range []bool{false, true} {
			name := stage.phase + "/cap"
			wait := "10m"
			wantMs := stage.budget.Milliseconds()
			wantCode := stage.timeout
			if deadline {
				name, wait, wantMs, wantCode = stage.phase+"/global", "3s", 3000, "launch_deadline"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t)
				rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(level) }, http.StatusAccepted)
				h.mustRun(ExitPending, rcArgs(h, "--wait", wait)...)
				r := rcResult(t, h)
				if r.Outcome != "timed_out" || r.Error == nil || r.Error.Code != wantCode || r.Error.Phase != stage.phase || r.TotalMs != wantMs || rcCountMessages(h) != 0 || r.SessionName != "remote-268" {
					t.Fatalf("wrong timeout: %+v", r)
				}
				for _, d := range h.sleeps {
					if d > 2*time.Second {
						t.Fatal("poll exceeds 2s")
					}
				}
			})
		}
	}
}

func TestRCZeroWait(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(5) }, http.StatusAccepted)
	h.mustRun(ExitPending, rcArgs(h, "--wait", "0")...)
	r := rcResult(t, h)
	if r.Outcome != "accepted" || r.SessionName != "remote-268" || r.Error.Code != "launch_deadline" || r.Polls != 0 || len(h.api.requests()) != 1 || len(h.sleeps) != 0 {
		t.Fatal("zero wait polled or submitted handoff")
	}
}

func TestRCBadOrAbsentLink(t *testing.T) {
	for _, link := range []string{"", "https://claude.ai/code/session_short", "https://example.com/code/session_synthetic268", "https://claude.ai/code/session_synthetic268?secret=bad", "https://claude.ai/code/session_synthetic268\n"} {
		t.Run(link, func(t *testing.T) {
			h := newHarness(t)
			rcServe(h, http.StatusCreated, func(int) apiv1.Session {
				v := rcView(4)
				v.RemoteControl.URL = link
				return v
			}, http.StatusAccepted)
			h.mustRun(ExitPending, rcArgs(h)...)
			r := rcResult(t, h)
			if r.Link != nil || r.Error.Code != "link_unavailable" || r.TotalMs != 10000 || !r.RCRegistered || rcCountMessages(h) != 0 {
				t.Fatalf("bad link accepted: %+v", r)
			}
		})
	}
}

func TestRCPodReadyIsNotAgentStarted(t *testing.T) {
	for _, agent := range []*apiv1.AgentStatus{nil, {State: "running"}, {State: "starting", ConversationID: "id"}} {
		h := newHarness(t)
		rcServe(h, http.StatusCreated, func(int) apiv1.Session {
			v := rcView(5)
			v.AgentStatus = agent
			return v
		}, http.StatusAccepted)
		h.mustRun(ExitPending, rcArgs(h)...)
		r := rcResult(t, h)
		if r.Error.Code != "agent_start_timeout" || r.Phases["agent_started"].State != "unknown" || rcCountMessages(h) != 0 {
			t.Fatal("PodReady or RC registration was treated as a running agent")
		}
	}
}

func TestRCRefusalsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		exit   int
	}{
		{401, "unauthenticated", ExitAuth},
		{403, "launch_forbidden", ExitAuth}, // including coordinator refusal
		{422, "project_unavailable", ExitFailed},
		{409, "request_conflict", ExitFailed}, // changed request under same key
	} {
		t.Run(tc.code, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
				decodeCreate(t, b)
				apiError(w, tc.status, "refused", rcTestText, apiv1.FieldError{Field: "project", Message: rcTestText})
			}
			h.mustRun(tc.exit, rcArgs(h, "--idempotency-key", "explicit-key")...)
			r := rcResult(t, h)
			if r.Error.Code != tc.code || r.IdempotencyKey != "explicit-key" || len(h.api.requests()) != 1 {
				t.Fatalf("wrong refusal: %+v", r)
			}
			if tc.code == "project_unavailable" && (r.Error.Phase != "project_inventory_ok" || r.Phases["project_inventory_ok"].State != "error" || r.Phases["api_accepted"].FirstObservedAt != nil) {
				t.Fatal("project admission error assigned to API acceptance", r)
			}
		})
	}
}

func TestRCHandoffRejectionAndLostReplyNeverResent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status, exit int
		code         string
	}{
		{"not_addressable", 409, ExitFailed, "handoff_rejected"},
		{"server_failure", 500, ExitUnconfirmed, "handoff_unconfirmed"},
		{"lost_reply", 0, ExitUnconfirmed, "handoff_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(5) }, tc.status)
			serve := h.api.handle
			h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
				if strings.HasSuffix(r.URL.Path, "/messages") {
					if tc.status == 0 {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					apiError(w, tc.status, "message", rcTestText)
					return
				}
				serve(w, r, b)
			}
			h.mustRun(tc.exit, rcArgs(h)...)
			r := rcResult(t, h)
			if r.Error.Code != tc.code || r.Link == nil || rcCountMessages(h) != 1 || r.Phases["cli_recorded"].State != "unknown" {
				t.Fatalf("message not one-shot: %+v", r)
			}
		})
	}
}

func TestRCAuthOwnerAction(t *testing.T) {
	for msg, code := range map[string]string{"login-conflict: private synthetic detail": "login_conflict", "login-conflict: auth_projection_unavailable": "login_conflict", "claude authentication requires a current keeper projection": "auth_projection_unavailable", "login-required: waiting for the keeper's Claude projection; no login is run in this pod": "auth_projection_unavailable"} {
		h := newHarness(t)
		rcServe(h, http.StatusCreated, func(int) apiv1.Session {
			v := rcView(5)
			v.AgentStatus = &apiv1.AgentStatus{Message: msg + " " + rcTestText}
			return v
		}, http.StatusAccepted)
		h.mustRun(ExitPending, rcArgs(h)...)
		r := rcResult(t, h)
		if r.Outcome != "action_required" || r.Error.Code != code || r.Link == nil || !r.RCRegistered || rcCountMessages(h) != 0 {
			t.Fatal("missing owner action", r)
		}
		wantMs := int64(0)
		if code == "auth_projection_unavailable" {
			wantMs = 30000
		}
		if r.TotalMs != wantMs || r.Phases["agent_started"].State != "blocked" {
			t.Fatal("wrong authentication settle interval", r)
		}
	}
}

func TestRCAuthProjectionRecoversAfterOnePoll(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(poll int) apiv1.Session {
		v := rcView(5)
		if poll == 0 {
			v.AgentStatus = &apiv1.AgentStatus{Message: "login-required: waiting for the keeper's Claude projection; no login is run in this pod"}
		}
		return v
	}, http.StatusAccepted)
	h.mustRun(ExitOK, rcArgs(h)...)
	r := rcResult(t, h)
	if r.Outcome != "link_available" || r.Error != nil || r.Polls != 1 || r.TotalMs != 2000 || rcCountMessages(h) != 1 {
		t.Fatal("transient keeper projection stopped startup", r)
	}
}

func TestRCLocalValidationAndCodexNoRequests(t *testing.T) {
	for name, value := range map[string]string{"empty": "", "whitespace": "\n\t ", "utf8": "\xff", "large": strings.Repeat("x", 8193), "esc": "unsafe\x1b", "cr": "unsafe\r", "del": "unsafe\x7f", "c1": "unsafe\u0085"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			args := rcArgs(h, "--handoff", h.write("invalid.txt", value))
			h.mustRun(ExitUsage, args...)
			if rcResult(t, h).Error.Code != "invalid_input" || len(h.api.requests()) != 0 {
				t.Fatal("invalid handoff sent")
			}
		})
	}
	for _, extra := range [][]string{{"--model", "opus"}, {"--model", "claude-opus-latest"}, {"--effort", "none"}, {"--wait", "-1s"}, {"--wait", "10m1ns"}, {"--idempotency-key", "bad/key"}, {"--project", "../sample"}, {"--repo", "owner/repo"}, {"--unknown", rcTestText}} {
		h := newHarness(t)
		h.mustRun(ExitUsage, rcArgs(h, extra...)...)
		if rcResult(t, h).Error.Code != "invalid_input" {
			t.Fatal("invalid local input classified incorrectly")
		}
		if len(h.api.requests()) != 0 {
			t.Fatal("invalid input sent")
		}
	}
	h := newHarness(t)
	h.mustRun(ExitFailed, rcArgs(h, "--agent", "codex", "--model", "gpt-6.1-sol")...)
	r := rcResult(t, h)
	if r.Outcome != "blocked" || r.Error.Code != "codex_adapter_not_implemented" || r.Error.Message != "Codex remote launch is not implemented in 2a; no inventory was probed" || len(h.api.requests()) != 0 || len(h.ran) != 0 {
		t.Fatal("Codex made a request")
	}
}

func TestRCStatusOneReadNoMessage(t *testing.T) {
	for _, level := range []int{2, 5} {
		h := newHarness(t)
		rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(level) }, http.StatusAccepted)
		want := ExitPending
		if level == 5 {
			want = ExitOK
		}
		h.mustRun(want, "rc", "status", "remote-268", "-o", "json")
		r := rcResult(t, h)
		if len(h.api.requests()) != 1 || h.api.requests()[0].method != http.MethodGet || r.Phases["handoff"].State != "unknown" {
			t.Fatal("status mutated or claimed a handoff")
		}
		if r.Phases["api_accepted"].FirstObservedAt != nil || r.Phases["project_inventory_ok"].FirstObservedAt != nil || r.Phases["handoff"].FirstObservedAt != nil {
			t.Fatal("status invented create or handoff observation times", r)
		}
	}
}

func TestRCSessionEnded(t *testing.T) {
	for _, state := range []string{"Failed", "Suspended", "Archived", "Reaping"} {
		for _, command := range []string{"create", "follow", "status"} {
			t.Run(state+"/"+command, func(t *testing.T) {
				h := newHarness(t)
				rcServe(h, http.StatusCreated, func(poll int) apiv1.Session {
					v := rcView(5) // Even a retained valid link cannot revive a session.
					if command != "follow" || poll > 0 {
						v.Phase = state
						v.Reaping = state == "Reaping"
					} else {
						v.AgentStatus = nil
					}
					return v
				}, http.StatusAccepted)
				args := rcArgs(h)
				if command == "status" {
					args = []string{"rc", "status", "remote-268", "-o", "json"}
				}
				h.mustRun(ExitFailed, args...)
				r := rcResult(t, h)
				wantRequests := 1
				if command == "follow" {
					wantRequests = 2
				}
				if r.Outcome != "failed" || r.Error.Code != "session_ended" || r.SessionName != "remote-268" || r.Link == nil || rcCountMessages(h) != 0 || len(h.api.requests()) != wantRequests {
					t.Fatal("ended session reported a usable launch or kept polling", r)
				}
			})
		}
	}
}

func TestRCStatusTransientAuthProjectionRemainsPending(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session {
		v := rcView(2)
		v.AgentStatus = &apiv1.AgentStatus{Message: "login-required: waiting for the keeper's Claude projection"}
		return v
	}, http.StatusAccepted)
	h.mustRun(ExitPending, "rc", "status", "remote-268", "-o", "json")
	r := rcResult(t, h)
	if r.Outcome != "accepted" || r.Error != nil || len(h.api.requests()) != 1 {
		t.Fatal("single status read inferred persistent projection failure", r)
	}
}

func TestRCStatusUnavailableAndRedirectNotFollowed(t *testing.T) {
	for _, status := range []int{200, 302, 503} {
		h := newHarness(t)
		h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) {
			w.Header().Set("Location", h.api.srv.URL+"/must-not-follow")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(rcTestText))
		}
		h.mustRun(ExitFailed, "rc", "status", "remote-268", "-o", "json")
		if rcResult(t, h).Error.Code != "status_unavailable" || len(h.api.requests()) != 1 {
			t.Fatal("status retried or redirected")
		}
	}
}

func TestRCHandoffBoundsPermitNewlineTabAnd8192Bytes(t *testing.T) {
	h := newHarness(t)
	args := rcArgs(h, "--wait", "0", "--handoff", h.write("max.txt", "\n\t"+strings.Repeat("x", 8190)))
	rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(0) }, http.StatusAccepted)
	h.mustRun(ExitPending, args...)
	rcResult(t, h)
}

func TestRCDeadlineDuringStatusRequest(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(0) }, http.StatusAccepted)
	serve := h.api.handle
	h.api.handle = func(w http.ResponseWriter, req *http.Request, body []byte) {
		if req.Method == http.MethodGet {
			<-req.Context().Done()
			return
		}
		serve(w, req, body)
	}
	// The injected sleep consumes 2s of the 3s budget; the GET must have only
	// 1s left rather than the ordinary 30s HTTP timeout.
	h.env.Sleep = func(ctx context.Context, d time.Duration) error { h.now = h.now.Add(d); return ctx.Err() }
	before := time.Now()
	h.mustRun(ExitPending, rcArgs(h, "--wait", "3s")...)
	r := rcResult(t, h)
	if r.Outcome != "timed_out" || r.Error.Code != "launch_deadline" || r.SessionName != "remote-268" {
		t.Fatal("deadline lost its outcome or handle", r)
	}
	if time.Since(before) > 2500*time.Millisecond {
		t.Fatal("request exceeded remaining deadline")
	}
}

func TestRCScheduledContainerCreatingUsesPodReadyBudget(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session {
		v := rcView(0)
		v.Node = "synthetic-node"
		v.Conditions = []apiv1.Condition{{Type: "PodReady", Status: "False", Reason: "ContainerCreating"}}
		return v
	}, http.StatusAccepted)
	h.mustRun(ExitPending, rcArgs(h, "--wait", "10m")...)
	r := rcResult(t, h)
	if r.Error.Code != "pod_ready_timeout" || r.TotalMs != 120000 || sinceMs(r.Phases["pod_scheduled"]) != 0 {
		t.Fatal("scheduled image pull was mistaken for scheduler timeout", r)
	}
}

func TestRCPodReadyBudgetStartsAtAcceptance(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(poll int) apiv1.Session {
		if poll < 25 {
			return rcView(0)
		}
		return rcView(1) // Scheduling takes 50s of the existing 120s budget.
	}, http.StatusAccepted)
	serve := h.api.handle
	h.api.handle = func(w http.ResponseWriter, req *http.Request, body []byte) {
		if req.Method == http.MethodPost {
			h.now = h.now.Add(5 * time.Second) // Acceptance is after the create exchange.
		}
		serve(w, req, body)
	}
	h.mustRun(ExitPending, rcArgs(h, "--wait", "10m")...)
	r := rcResult(t, h)
	if r.Outcome != "timed_out" || r.Error.Code != "pod_ready_timeout" || r.TotalMs != 125000 || sinceMs(r.Phases["api_accepted"]) != 5000 || sinceMs(r.Phases["pod_scheduled"]) != 55000 {
		t.Fatal("pod readiness budget did not start at acceptance", r)
	}
}

func TestRCHandoffLocalFailuresAreNotRetried(t *testing.T) {
	for _, tokenFailure := range []bool{true, false} {
		name := "dial"
		wantExit, wantCode := ExitPending, "status_unavailable"
		if tokenFailure {
			name, wantExit, wantCode = "token", ExitAuth, "unauthenticated"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			rcServe(h, http.StatusCreated, func(poll int) apiv1.Session { return rcView(min(poll, 5)) }, http.StatusAccepted)
			serve := h.api.handle
			h.api.handle = func(w http.ResponseWriter, req *http.Request, body []byte) {
				if req.Method == http.MethodGet && len(h.api.requests()) == 6 {
					if tokenFailure {
						if err := os.WriteFile(h.vars[envTokenFile], nil, 0o600); err != nil {
							t.Error(err)
						}
					} else {
						// Finish this GET but force the handoff to make a new,
						// refused dial. No message reaches the fake API.
						w.Header().Set("Connection", "close")
						if err := h.api.srv.Listener.Close(); err != nil {
							t.Error(err)
						}
					}
				}
				serve(w, req, body)
			}
			h.mustRun(wantExit, rcArgs(h)...)
			r := rcResult(t, h)
			if r.Outcome != "failed" || r.Error.Code != wantCode || r.Error.Phase != "handoff" || r.Link == nil || r.Phases["handoff"].State != "error" || r.Phases["handoff"].Reason != "not_sent" || rcCountMessages(h) != 0 || len(h.api.requests()) != 6 {
				t.Fatal("local handoff failure misclassified or retried", r)
			}
		})
	}
}

func TestRCMissingTokenAndUnconfirmedCreate(t *testing.T) {
	for _, status := range []bool{false, true} {
		h := newHarness(t)
		h.vars[envTokenFile] = h.write("empty-token", "")
		args := rcArgs(h)
		if status {
			args = []string{"rc", "status", "remote-268", "-o", "json"}
		}
		h.mustRun(ExitAuth, args...)
		if rcResult(t, h).Error.Code != "unauthenticated" || len(h.api.requests()) != 0 {
			t.Fatal("local token error mistaken for a sent request")
		}
	}
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, b []byte) { apiError(w, 503, "unavailable", rcTestText) }
	h.mustRun(ExitUnconfirmed, rcArgs(h)...)
	r := rcResult(t, h)
	if r.Outcome != "unconfirmed" || r.Error.Code != "status_unavailable" || r.IdempotencyKey == "" || len(h.api.requests()) != 1 {
		t.Fatal("uncertain create retried or reported definite failure", r)
	}
}

func TestRCJSONBoundsAndUntrustedViewRedaction(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session {
		v := rcView(4)
		v.Prompt = rcTestText
		v.AgentStatus.Message = strings.Repeat(rcTestText, 10000)
		v.Conditions[0].Message = strings.Repeat(rcTestText, 10000)
		v.RemoteControl.URL = strings.Repeat(rcTestText, 10000)
		return v
	}, http.StatusAccepted)
	h.mustRun(ExitPending, rcArgs(h)...)
	rcResult(t, h)
	if h.stdout.Len() > 32<<10 {
		t.Fatal("large API messages expanded the result")
	}
}

func TestRCAuthenticationLostAfterCreate(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session { return rcView(0) }, http.StatusAccepted)
	originalSleep := h.env.Sleep
	h.env.Sleep = func(ctx context.Context, d time.Duration) error {
		h.write("api-token", "")
		return originalSleep(ctx, d)
	}
	h.mustRun(ExitAuth, rcArgs(h)...)
	r := rcResult(t, h)
	if r.Error.Code != "unauthenticated" || r.SessionName != "remote-268" || len(h.api.requests()) != 1 {
		t.Fatal("lost authentication classified as status or retried", r)
	}
}

func TestRCStatusRejectsUnboundedModelMetadata(t *testing.T) {
	h := newHarness(t)
	rcServe(h, http.StatusCreated, func(int) apiv1.Session {
		v := rcView(5)
		v.Model = "claude-5-" + strings.Repeat("x", 40000)
		return v
	}, http.StatusAccepted)
	h.mustRun(ExitFailed, "rc", "status", "remote-268", "-o", "json")
	if rcResult(t, h).Error.Code != "status_unavailable" {
		t.Fatal("unbounded metadata accepted")
	}
}

func TestRCDoesNotWidenOrdinaryProjectFlags(t *testing.T) {
	h := newHarness(t)
	h.mustRun(ExitUsage, "run", "--project", "sample", "--interactive", "--repo", "haynes-ops")
	if h.stderr.String() != "agent-run: --project selects a new task; it cannot combine with rescue restore, --local or --interactive\n" {
		t.Fatal("ordinary create failed for the wrong reason", h.stderr.String())
	}
	if len(h.api.requests()) != 0 {
		t.Fatal("ordinary remote create widened")
	}
}

// sinceMs is a phase's elapsed milliseconds, or -1 when it was not observed.
func sinceMs(p apiv1.RCLaunchPhase) int64 {
	if p.SinceRequestMs == nil {
		return -1
	}
	return *p.SinceRequestMs
}
