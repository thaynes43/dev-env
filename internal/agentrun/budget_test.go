package agentrun

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func budgetReceipt() apiv1.TaskBudgetStatus {
	return apiv1.TaskBudgetStatus{Observed: true, Binding: apiv1.TaskBudgetBinding{TaskUID: "campaign-a", HostID: "host-a", PodUID: "pod-a", Epoch: 1, Deadline: testNow.Add(45 * time.Minute)}, LastProgress: testNow, NextCheckpoint: testNow.Add(10 * time.Minute), UsageAccounting: "SupervisorElapsedEstimate; provider usage unavailable", StopConfirmed: true}
}

func TestBudgetCreateWireAndDefaults(t *testing.T) {
	h := newHarness(t)
	h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeTestJSON(w, http.StatusCreated, budgetReceipt())
	}
	h.mustRun(ExitOK, "budget", "create", "campaign-a", "--success", "verified result", "-o", "json")
	reqs := h.api.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].path != apiv1.TaskBudgetsPath || reqs[0].auth != "Bearer "+testToken {
		t.Fatalf("unexpected requests: %+v", reqs)
	}
	want := `{"taskUID":"campaign-a","spec":{"successCondition":"verified result","overallSeconds":2700,"effortSeconds":3600,"checkpointSeconds":600}}`
	if string(reqs[0].body) != want {
		t.Fatalf("body=%s", reqs[0].body)
	}
	contains(t, "receipt", h.stdout.String(), `"observed": true`, `"admitted": false`, `"taskUID": "campaign-a"`)
	if len(h.sleeps) != 0 || len(h.ran) != 0 {
		t.Fatal("unexpected retry or subprocess")
	}
}

func TestBudgetInvalidInputsSendNothing(t *testing.T) {
	cases := [][]string{
		{}, {"admit", "campaign-a"}, {"create", "campaign-a"}, {"create", "campaign-a", "--success", "   "},
		{"create", "../other", "--success", "result"}, {"create", "campaign-a", "--success", strings.Repeat("a", 1025)},
		{"create", "campaign-a", "--success", "result", "--overall", "45m1s"},
		{"create", "campaign-a", "--success", "result", "--effort", "1h1s"},
		{"create", "campaign-a", "--success", "result", "--checkpoint", "10m1s"},
		{"create", "campaign-a", "--success", "result", "--overall", "1.5s"},
		{"create", "campaign-a", "--success", "result", "--checkpoint", "0s"},
		{"create", "campaign-a", "--success", "result", "--effort", "-1s"},
		{"create", "campaign-a", "--success", "result", "--overall", "5m"},
		{"show", "campaign-a", "extra"}, {"show", "campaign-a", "--success", "result"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t)
			h.mustRun(ExitUsage, append([]string{"budget"}, args...)...)
			if len(h.api.requests()) != 0 || len(h.ran) != 0 {
				t.Fatal("invalid request had side effects")
			}
		})
	}
}

func TestBudgetCreateUnconfirmedNeverReplays(t *testing.T) {
	for _, scenario := range []string{"empty", "malformed", "unknown-field", "wrong-task", "admitted", "unobserved", "epoch", "deadline", "checkpoint", "latched", "negative-effort", "missing-host", "accepted", "unavailable", "lost-answer", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			h.api.handle = func(w http.ResponseWriter, r *http.Request, _ []byte) {
				st := budgetReceipt()
				code := http.StatusCreated
				switch scenario {
				case "empty":
					w.WriteHeader(code)
					return
				case "malformed":
					w.WriteHeader(code)
					_, _ = w.Write([]byte("{"))
					return
				case "unknown-field":
					w.WriteHeader(code)
					_, _ = w.Write([]byte(strings.TrimSuffix(mustJSON(t, st), "}") + `,"other":true}`))
					return
				case "wrong-task":
					st.Binding.TaskUID = "other"
				case "admitted":
					st.Admitted = true
				case "unobserved":
					st.Observed = false
				case "epoch":
					st.Binding.Epoch = 2
				case "deadline":
					st.Binding.Deadline = st.Binding.Deadline.Add(time.Second)
				case "checkpoint":
					st.NextCheckpoint = st.NextCheckpoint.Add(time.Second)
				case "latched":
					st.Latched = true
				case "negative-effort":
					st.EffortMilliseconds = -1
				case "missing-host":
					st.Binding.HostID = ""
				case "accepted":
					code = http.StatusAccepted
				case "unavailable":
					code = http.StatusServiceUnavailable
				case "lost-answer":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				case "redirect":
					http.Redirect(w, r, apiv1.TaskBudgetPath("campaign-a"), http.StatusTemporaryRedirect)
					return
				}
				writeTestJSON(w, code, st)
			}
			want := ExitUnconfirmed
			if scenario == "redirect" {
				want = ExitFailed
			}
			h.mustRun(want, "budget", "create", "campaign-a", "--success", "result")
			if len(h.api.requests()) != 1 || len(h.sleeps) != 0 || h.stdout.Len() != 0 {
				t.Fatalf("replayed or reported success: requests=%d sleeps=%v output=%s", len(h.api.requests()), h.sleeps, &h.stdout)
			}
		})
	}
}

func TestBudgetShowLatchedDoesNotAdmitOrReset(t *testing.T) {
	h := newHarness(t)
	st := budgetReceipt()
	st.Latched = true
	st.Reason = "Checkpoint"
	st.EffortMilliseconds = 1234
	h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) { writeTestJSON(w, http.StatusOK, st) }
	h.mustRun(ExitOK, "budget", "show", "campaign-a")
	contains(t, "retained status", h.stdout.String(), "latched: true", "reason: Checkpoint", "admitted: false", "effort milliseconds: 1234", "provider usage unavailable")
	r := h.api.requests()
	if len(r) != 1 || r[0].method != http.MethodGet || r[0].path != apiv1.TaskBudgetPath("campaign-a") || len(r[0].body) != 0 {
		t.Fatalf("unexpected request: %+v", r)
	}
}

// Ambiguous or absent safety fields must never become a zero-value authority.
func TestBudgetRejectsAmbiguousReceipts(t *testing.T) {
	raw := mustJSON(t, budgetReceipt())
	cases := map[string]string{
		"duplicate-latch": strings.Replace(raw, `"latched":false`, `"latched":true,"latched":false`, 1),
		"null-latch":      strings.Replace(raw, `"latched":false`, `"latched":null`, 1),
		"missing-latch":   strings.Replace(raw, `"latched":false,`, "", 1),
		"case-alias":      strings.Replace(raw, `"latched":false`, `"Latched":false`, 1),
		"null-stop":       strings.Replace(raw, `"stopConfirmed":true`, `"stopConfirmed":null`, 1),
		"negative-epoch":  strings.Replace(raw, `"epoch":1`, `"epoch":-1`, 1),
		"overflow-epoch":  strings.Replace(raw, `"epoch":1`, `"epoch":18446744073709551616`, 1),
	}
	for name, body := range cases {
		for _, verb := range []string{"create", "show"} {
			t.Run(name+"/"+verb, func(t *testing.T) {
				h := newHarness(t)
				code := http.StatusOK
				if verb == "create" {
					code = http.StatusCreated
				}
				h.api.handle = func(w http.ResponseWriter, _ *http.Request, _ []byte) {
					w.WriteHeader(code)
					_, _ = w.Write([]byte(body))
				}
				args := []string{"budget", verb, "campaign-a"}
				if verb == "create" {
					args = append(args, "--success", "result")
				}
				h.mustRun(ExitUnconfirmed, args...)
				if len(h.api.requests()) != 1 || h.stdout.Len() != 0 || len(h.sleeps) != 0 {
					t.Fatal("ambiguous response accepted or replayed")
				}
			})
		}
	}
}
