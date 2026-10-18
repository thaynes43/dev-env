package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBudgetClientExactConfirmationAndFailClosedObservation(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "task-a", Epoch: 1, Deadline: time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC), HostID: "host-a", PodUID: "pod-a"}
	for _, scenario := range []string{"ready", "latch", "epoch", "pod", "deadline", "omitted-observation", "omitted-admission", "unknown-json", "redirect", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			st := TaskBudgetStatus{Binding: b, Observed: true, Admitted: true}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer synthetic-token" {
					t.Error("token missing")
				}
				switch scenario {
				case "latch":
					st.Latched = true
				case "epoch":
					st.Binding.Epoch++
				case "pod":
					st.Binding.PodUID = "other-pod"
				case "deadline":
					st.Binding.Deadline = st.Binding.Deadline.Add(time.Second)
				case "omitted-observation":
					st.Observed = false
				case "omitted-admission":
					st.Admitted = false
				case "unknown-json":
					_, _ = w.Write([]byte(`{"extra":true}`))
					return
				case "redirect":
					w.Header().Set("Location", "https://other.invalid/")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				case "unavailable":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(st)
			}))
			defer srv.Close()
			g := &HTTPTaskBudgetGate{BaseURL: srv.URL, Client: srv.Client(), Token: func(context.Context) (string, error) { return "synthetic-token", nil }}
			err := g.Admit(context.Background(), b)
			if (err == nil) != (scenario == "ready") {
				t.Fatalf("admit %s: %v", scenario, err)
			}
			latched, err := g.Observe(context.Background(), b)
			validObservation := scenario == "ready" || scenario == "latch" || scenario == "omitted-admission"
			if validObservation && err != nil {
				t.Fatal(err)
			}
			if !validObservation && (!latched || !errors.Is(err, ErrTaskBudgetGate)) {
				t.Fatal("uncertain observation did not stop")
			}
			if scenario == "latch" && !latched {
				t.Fatal("latch lost")
			}
		})
	}
	var nilGate *HTTPTaskBudgetGate
	if err := nilGate.Admit(context.Background(), b); !errors.Is(err, ErrTaskBudgetGate) {
		t.Fatal("nil gate admitted")
	}
}

func TestBudgetGateRefusesAmbiguousOrOversizedStopAuthority(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "campaign", Epoch: 1, HostID: "host", PodUID: "root", Deadline: time.Now().Add(time.Minute)}
	original, _ := json.Marshal(TaskBudgetStatus{Binding: b, Observed: true, Admitted: true})
	for _, scenario := range []string{"missing-latch", "null-latch", "duplicate-latch", "case-latch", "duplicate-binding", "case-binding", "oversize"} {
		t.Run(scenario, func(t *testing.T) {
			raw := string(original)
			switch scenario {
			case "missing-latch":
				raw = strings.Replace(raw, `"latched":false,`, "", 1)
			case "null-latch":
				raw = strings.Replace(raw, `"latched":false`, `"latched":null`, 1)
			case "duplicate-latch":
				raw = strings.Replace(raw, `"latched":false`, `"latched":true,"latched":false`, 1)
			case "case-latch":
				raw = strings.Replace(raw, `"latched":false`, `"Latched":false`, 1)
			case "duplicate-binding":
				raw = strings.Replace(raw, `"epoch":1`, `"epoch":2,"epoch":1`, 1)
			case "case-binding":
				raw = strings.Replace(raw, `"epoch":1`, `"Epoch":1`, 1)
			case "oversize":
				raw += strings.Repeat(" ", 64<<10)
			}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) }))
			defer srv.Close()
			g := &HTTPTaskBudgetGate{BaseURL: srv.URL, Client: srv.Client(), Token: func(context.Context) (string, error) { return "synthetic", nil }}
			latched, err := g.Observe(context.Background(), b)
			if !latched || !errors.Is(err, ErrTaskBudgetGate) {
				t.Fatalf("uncertainty did not stop: %t %v", latched, err)
			}
		})
	}
}
