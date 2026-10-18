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

func TestOwnedBootstrapIsOneShotAndRefusesConflictOrUnknown(t *testing.T) {
	for _, scenario := range []string{"created", "bounded-clock-skew", "excess-clock-skew", "conflict", "unknown", "redirect", "wrong-binding"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != TaskBudgetsPath {
					t.Error("unexpected recovery/replay", r.Method, r.URL.Path)
				}
				b := TaskBudgetBinding{TaskUID: "campaign", Epoch: 1, HostID: "host", PodUID: "pod", Deadline: time.Now().Add(59 * time.Second)}
				switch scenario {
				case "bounded-clock-skew":
					b.Deadline = time.Now().Add(62 * time.Second)
				case "excess-clock-skew":
					b.Deadline = time.Now().Add(70 * time.Second)
				case "conflict":
					w.WriteHeader(http.StatusConflict)
					return
				case "unknown":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case "redirect":
					w.Header().Set("Location", "/wrong")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				case "wrong-binding":
					b.PodUID = "replacement"
				}
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(TaskBudgetStatus{Observed: true, Binding: b})
			}))
			defer srv.Close()
			gate := &HTTPTaskBudgetGate{BaseURL: srv.URL, Client: srv.Client(), Token: func(context.Context) (string, error) { return "synthetic", nil }}
			_, err := gate.BootstrapOwned(context.Background(), CreateTaskBudgetRequest{TaskUID: "campaign", Spec: TaskBudgetSpec{OverallSeconds: 60}}, "host", "pod")
			if (err == nil) != (scenario == "created" || scenario == "bounded-clock-skew") || calls != 1 {
				t.Fatal("bootstrap result/replay", calls, err)
			}
		})
	}
}
func TestOwnedStopRequiresCanonicalEvidenceAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"exact", "missing", "foreign", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			b := TaskBudgetBinding{TaskUID: "campaign", Epoch: 1, HostID: "host", PodUID: "pod", Deadline: time.Now().Add(time.Minute)}
			calls := 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var e TaskBudgetEvent
				if json.NewDecoder(r.Body).Decode(&e) != nil {
					t.Error("event")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				st := TaskBudgetStatus{Observed: true, Binding: b, LastEvidence: &e.Evidence}
				switch scenario {
				case "missing":
					st.LastEvidence = nil
				case "foreign":
					st.LastEvidence = &TaskBudgetEvidence{Kind: "other"}
				case "unknown":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode(st)
			}))
			defer srv.Close()
			gate := &HTTPTaskBudgetGate{BaseURL: srv.URL, Client: srv.Client(), Token: func(context.Context) (string, error) { return "synthetic", nil }}
			err := gate.StopOwned(context.Background(), b)
			if (err == nil) != (scenario == "exact") || calls != 1 {
				t.Fatal("stop acknowledgement/replay", calls, err)
			}
		})
	}
}
