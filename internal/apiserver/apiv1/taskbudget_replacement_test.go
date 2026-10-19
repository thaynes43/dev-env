package apiv1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Real TLS and the standard HTTP transport, not a mocked RoundTripper. A
// successfully used connection disappears before its next response headers,
// as can happen while a serving replica is replaced. Only a NEW complete
// authenticated exact-binding response may authorize the next observation.
func TestBudgetObservationReusedConnectionReplacement(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "replacement", HostID: "host", PodUID: "original", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != TaskBudgetPath(b.TaskUID)+"/observe" || r.Header.Get("Authorization") != "Bearer synthetic-replacement" {
			t.Error("immutable request changed")
		}
		var request TaskBudgetRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || !sameOwnedWireBinding(request.Binding, b) {
			t.Error("original binding changed")
		}
		if n == 2 {
			c, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = c.Close()
			return
		}
		_ = json.NewEncoder(w).Encode(TaskBudgetStatus{Binding: b, Observed: true, Admitted: true})
	}))
	defer server.Close()
	hc := server.Client()
	hc.Timeout = 2 * time.Second
	g := &HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return "synthetic-replacement", nil }}
	if stop, err := g.Observe(context.Background(), b); stop || err != nil {
		t.Fatal("warm observation", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	stop, err := g.Observe(ctx, b)
	t.Logf("replacement observation calls=%d elapsed=%v stop=%v error=%v", calls.Load(), time.Since(started), stop, err)
	if stop || err != nil || calls.Load() != 3 {
		t.Fatal("lost old connection did not obtain fresh exact authority within the original observation bound")
	}
}

func TestBudgetObservationReplacementStillRefusesFreshDenial(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "replacement", HostID: "host", PodUID: "original", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, kind := range []string{"latched", "stop-requested", "identity", "unauthorized", "conflict", "unavailable", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 2 {
					c, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = c.Close()
					return
				}
				st := TaskBudgetStatus{Binding: b, Observed: true, Admitted: true}
				if n >= 3 {
					switch kind {
					case "latched":
						st.Latched = true
					case "stop-requested":
						st.NativeStopRequested = true
					case "identity":
						st.Binding.PodUID = "not-original"
					case "unauthorized":
						w.WriteHeader(http.StatusUnauthorized)
						return
					case "conflict":
						w.WriteHeader(http.StatusConflict)
						return
					case "unavailable":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					case "truncated":
						_, _ = w.Write([]byte(`{"observed":true`))
						return
					}
				}
				_ = json.NewEncoder(w).Encode(st)
			}))
			defer server.Close()
			hc := server.Client()
			hc.Timeout = 2 * time.Second
			g := &HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return "synthetic", nil }}
			if stop, e := g.Observe(context.Background(), b); stop || e != nil {
				t.Fatal(e)
			}
			stop, e := g.Observe(context.Background(), b)
			if !stop || (kind != "latched" && kind != "stop-requested" && e == nil) || calls.Load() != 3 {
				t.Fatalf("fresh refusal promoted or retried: stop=%v err=%v calls=%d", stop, e, calls.Load())
			}
		})
	}
}

func TestBudgetObservationReconnectKeepsOriginalCancellation(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "replacement", HostID: "host", PodUID: "original", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, kind := range []string{"caller-cancel", "caller-deadline"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			cancelRequest := make(chan context.CancelFunc, 1)
			release := make(chan struct{})
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 2 {
					if kind == "caller-cancel" {
						(<-cancelRequest)()
					}
					c, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = c.Close()
					return
				}
				if n >= 3 {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				_ = json.NewEncoder(w).Encode(TaskBudgetStatus{Binding: b, Observed: true, Admitted: true})
			}))
			defer server.Close()
			defer close(release)
			hc := server.Client()
			hc.Timeout = 2 * time.Second
			g := &HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return "synthetic", nil }}
			if stop, e := g.Observe(context.Background(), b); stop || e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			cancelRequest <- cancel
			started := time.Now()
			stop, e := g.Observe(ctx, b)
			if !stop || e == nil || ctx.Err() == nil || time.Since(started) > 300*time.Millisecond {
				t.Fatalf("original cancellation was extended: stop=%v err=%v context=%v elapsed=%v", stop, e, ctx.Err(), time.Since(started))
			}
			if kind == "caller-cancel" && calls.Load() != 2 || kind == "caller-deadline" && calls.Load() != 3 {
				t.Fatalf("unexpected attempt count %d", calls.Load())
			}
		})
	}
}

func TestBudgetLostConnectionDoesNotReplayAdmissionOrFreshConnection(t *testing.T) {
	b := TaskBudgetBinding{TaskUID: "replacement", HostID: "host", PodUID: "original", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
	for _, kind := range []string{"admit-reused", "observe-fresh"} {
		t.Run(kind, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if kind == "observe-fresh" || n == 2 {
					c, _, e := w.(http.Hijacker).Hijack()
					if e != nil {
						t.Error(e)
						return
					}
					_ = c.Close()
					return
				}
				_ = json.NewEncoder(w).Encode(TaskBudgetStatus{Binding: b, Observed: true, Admitted: true})
			}))
			defer server.Close()
			hc := server.Client()
			hc.Timeout = 2 * time.Second
			g := &HTTPTaskBudgetGate{BaseURL: server.URL, Client: hc, Token: func(context.Context) (string, error) { return "synthetic", nil }}
			if kind == "admit-reused" {
				if e := g.Admit(context.Background(), b); e != nil {
					t.Fatal(e)
				}
				if e := g.Admit(context.Background(), b); e == nil {
					t.Fatal("unknown admission accepted")
				}
				if calls.Load() != 2 {
					t.Fatal("admission replayed", calls.Load())
				}
			} else {
				if stop, e := g.Observe(context.Background(), b); !stop || e == nil {
					t.Fatal("fresh connection loss promoted")
				}
				if calls.Load() != 1 {
					t.Fatal("new connection blindly retried", calls.Load())
				}
			}
		})
	}
}
