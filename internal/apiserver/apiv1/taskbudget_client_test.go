package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
