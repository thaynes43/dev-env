package broker

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestConsoleRunnerDrainsAndClosesStuckHandler(t *testing.T) {
	for _, stuck := range []bool{false, true} {
		t.Run(map[bool]string{false: "graceful", true: "deadline"}[stuck], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listening := make(chan string, 1)
			started, check, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			observed := make(chan error, 1)
			r := &ConsoleRunner{
				Addr: "127.0.0.1:0", shutdownTimeout: 100 * time.Millisecond,
				Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
					close(started)
					<-check
					observed <- req.Context().Err()
					select {
					case <-release:
						_, _ = w.Write([]byte("finished"))
					case <-req.Context().Done():
					}
					close(stopped)
				}),
				listening: func(addr string) { listening <- addr },
			}
			done := make(chan error, 1)
			go func() { done <- r.Start(ctx) }()
			var addr string
			select {
			case addr = <-listening:
			case err := <-done:
				t.Fatalf("runner stopped before listening: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not listen")
			}
			if r.NeedLeaderElection() || r.ReadyCheck(nil) != nil {
				t.Fatal("console is not ready on every replica")
			}
			requestDone := make(chan error, 1)
			go func() {
				c := &http.Client{Timeout: 5 * time.Second}
				resp, err := c.Get("http://" + addr + "/approvals")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
				requestDone <- err
			}()
			select {
			case <-started:
			case err := <-requestDone:
				t.Fatalf("request failed before entering the handler: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not start")
			}
			cancel()
			close(check)
			select {
			case err := <-observed:
				if err != nil {
					t.Fatal("manager cancellation interrupted an in-flight decision")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not inspect its context")
			}
			if !stuck {
				close(release)
			}
			select {
			case <-stopped:
			case <-time.After(5 * time.Second):
				t.Fatal("handler outlived the drain deadline")
			}
			select {
			case err := <-done:
				if err != nil || r.ReadyCheck(nil) == nil {
					t.Fatalf("shutdown: %v; ready=%v", err, r.ReadyCheck(nil) == nil)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not stop")
			}
			select {
			case err := <-requestDone:
				if !stuck && err != nil {
					t.Fatalf("graceful response failed: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("request connection outlived shutdown")
			}
		})
	}
}
