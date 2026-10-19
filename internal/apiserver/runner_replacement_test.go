package apiserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// A real Runner with the production TLS/listener/shutdown implementation.
// Tests own only loopback sockets and synthetic requests; no provider/model,
// live cluster, credentials or CPU load. Tests are serial and channel bounded.
func replacementRunner(t *testing.T, dir string, h http.Handler) (*Runner, context.CancelFunc, <-chan error) {
	t.Helper()
	r := &Runner{Addr: "127.0.0.1:0", CertDir: dir, Handler: h, ErrorLog: log.New(io.Discard, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	t.Cleanup(cancel)
	until := time.NewTimer(2 * time.Second)
	defer until.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for r.ReadyCheck(nil) != nil {
		select {
		case e := <-done:
			t.Fatal(e)
		case <-until.C:
			t.Fatal("runner not ready")
		case <-tick.C:
		}
	}
	return r, cancel, done
}
func replacementHTTP(pool *x509.CertPool, route func() string) *http.Client {
	return &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, route())
	}, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, ServerName: "127.0.0.1"}, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: 2 * time.Second, MaxIdleConns: 1, IdleConnTimeout: time.Second}}
}

func TestRunnerDrainsExistingNativeRequestEnvelope(t *testing.T) {
	dir := t.TempDir()
	pool := writeCert(t, dir)
	entered := make(chan struct{})
	release := make(chan struct{})
	requestCanceled := make(chan struct{}, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bounded, done := context.WithTimeout(r.Context(), apiv1.NativeControlRequestTimeout)
		defer done()
		close(entered)
		select {
		case <-release:
			_, _ = w.Write([]byte("completed-within-original-native-envelope"))
		case <-bounded.Done():
			requestCanceled <- struct{}{}
		}
	})
	r, cancel, stopped := replacementRunner(t, dir, h)
	hc := replacementHTTP(pool, r.boundAddr)
	hc.Timeout = apiv1.NativeControlRequestTimeout
	hc.Transport.(*http.Transport).ResponseHeaderTimeout = apiv1.NativeControlRequestTimeout
	response := make(chan error, 1)
	go func() {
		resp, e := hc.Post("https://"+r.boundAddr()+"/native", "application/json", nil)
		if e == nil {
			_, e = io.ReadAll(resp.Body)
			_ = resp.Body.Close()
		}
		response <- e
	}()
	<-entered
	started := time.Now()
	cancel()
	// 11s is within the existing16s native request bound, beyond the old10s
	// server drain. A graceful replacement must not let the process exit first.
	delay := time.NewTimer(11 * time.Second)
	defer delay.Stop()
	select {
	case e := <-stopped:
		close(release)
		t.Fatalf("operator exited before allowed native request completed: elapsed=%v err=%v", time.Since(started), e)
	case <-requestCanceled:
		close(release)
		t.Fatal("request inherited manager cancellation")
	case <-delay.C:
	}
	close(release)
	select {
	case e := <-response:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not complete")
	}
	select {
	case e := <-stopped:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner did not finish its drain")
	}
}

func TestRunnerEndpointWithdrawalOrdering(t *testing.T) {
	dir := t.TempDir()
	pool := writeCert(t, dir)
	var requests atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusOK) })
	old, cancelOld, oldStopped := replacementRunner(t, dir, h)
	next, cancelNext, nextStopped := replacementRunner(t, dir, h)
	var withdrawn atomic.Bool
	hc := replacementHTTP(pool, func() string {
		if withdrawn.Load() {
			return next.boundAddr()
		}
		return old.boundAddr()
	})
	defer hc.CloseIdleConnections()
	read := func() error {
		resp, e := hc.Get("https://127.0.0.1/fresh-authority")
		if e == nil {
			_ = resp.Body.Close()
		}
		return e
	}
	if e := read(); e != nil {
		t.Fatal(e)
	}
	// Announcing Pod termination does not synchronously withdraw every service
	// route. Existing code closes its listener immediately at SIGTERM.
	cancelOld()
	if e := <-oldStopped; e != nil {
		t.Fatal(e)
	}
	hc.CloseIdleConnections()
	if e := read(); e == nil {
		t.Fatal("fixture did not retain the terminating endpoint before withdrawal")
	} else {
		t.Logf("fresh dial while endpoint withdrawal is pending fails: %v", e)
	}
	// Kube's preStop hook must keep this old listener serving fresh authority
	// until routes withdraw; after withdrawal the unchanged client reaches next.
	withdrawn.Store(true)
	if e := read(); e != nil {
		t.Fatal("new replica was unavailable after route withdrawal", e)
	}
	cancelNext()
	if e := <-nextStopped; e != nil {
		t.Fatal(e)
	}
}

// preStop must leave the API serving fresh authority until endpoint routing
// withdraws. A retained TCP connection is never retained permission: revocation
// must be observed even while the retiring replica is still serving.
func TestRunnerServingUntilWithdrawalRequiresFreshAuthority(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		name := "reused-connection"
		if fresh {
			name = "fresh-connection"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			pool := writeCert(t, dir)
			b := apiv1.TaskBudgetBinding{TaskUID: "drain", HostID: "host", PodUID: "original", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
			var revoked, withdrawn atomic.Bool
			var calls atomic.Int32
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request apiv1.TaskBudgetRequest
				if r.Method != http.MethodPost || r.URL.Path != apiv1.TaskBudgetPath(b.TaskUID)+"/observe" || r.Header.Get("Authorization") != "Bearer synthetic" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Binding.TaskUID != b.TaskUID || request.Binding.HostID != b.HostID || request.Binding.PodUID != b.PodUID || request.Binding.Epoch != b.Epoch || !request.Binding.Deadline.Equal(b.Deadline) {
					t.Error("immutable authority request changed")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				calls.Add(1)
				_ = json.NewEncoder(w).Encode(apiv1.TaskBudgetStatus{Binding: b, Observed: true, Admitted: true, Latched: revoked.Load()})
			})
			old, cancelOld, oldStopped := replacementRunner(t, dir, h)
			next, cancelNext, nextStopped := replacementRunner(t, dir, h)
			hc := replacementHTTP(pool, func() string {
				if withdrawn.Load() {
					return next.boundAddr()
				}
				return old.boundAddr()
			})
			defer hc.CloseIdleConnections()
			g := &apiv1.HTTPTaskBudgetGate{BaseURL: "https://127.0.0.1", Client: hc, Token: func(context.Context) (string, error) { return "synthetic", nil }}
			if stop, e := g.Observe(context.Background(), b); stop || e != nil {
				t.Fatal("initial authority", e)
			}
			// Deletion/preStop has started, but this endpoint has not withdrawn.
			// Keep serving; do not invent an authorization grace or cached allow.
			if fresh {
				hc.CloseIdleConnections()
			}
			if stop, e := g.Observe(context.Background(), b); stop || e != nil {
				t.Fatal("serving drain lost fresh authority", e)
			}
			revoked.Store(true)
			if stop, e := g.Observe(context.Background(), b); !stop || e != nil {
				t.Fatal("retiring endpoint concealed fresh revocation", e)
			}
			withdrawn.Store(true)
			cancelOld()
			if e := <-oldStopped; e != nil {
				t.Fatal(e)
			}
			hc.CloseIdleConnections()
			if stop, e := g.Observe(context.Background(), b); !stop || e != nil {
				t.Fatal("replacement concealed original revocation", e)
			}
			if calls.Load() != 4 {
				t.Fatal("authority result was cached or an acknowledged observation retried", calls.Load())
			}
			cancelNext()
			if e := <-nextStopped; e != nil {
				t.Fatal(e)
			}
		})
	}
}
