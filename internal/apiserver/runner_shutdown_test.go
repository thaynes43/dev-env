package apiserver

import (
	"context"
	"crypto/tls"
	"io"
	"log"
	"net/http"
	"testing"
	"time"
)

func TestRunnerShutdownClosesStuckHandler(t *testing.T) {
	dir := t.TempDir()
	pool := writeCert(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan string, 1)
	started, check, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
	observed := make(chan error, 1)
	r := &Runner{
		Addr: "127.0.0.1:0", CertDir: dir, ErrorLog: log.New(io.Discard, "", 0), shutdownTimeout: 100 * time.Millisecond,
		Handler: http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			close(started)
			<-check
			observed <- req.Context().Err()
			<-req.Context().Done()
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
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}}
		resp, err := c.Get("https://" + addr + "/v1/fleet")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-requestDone:
		t.Fatal("request failed before entering the handler")
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	close(check)
	select {
	case err := <-observed:
		if err != nil {
			t.Fatal("manager cancellation interrupted the graceful drain")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not inspect its context")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("handler survived the forced connection close")
	}
	select {
	case err := <-done:
		if err != nil || r.ReadyCheck(nil) == nil {
			t.Fatalf("shutdown: %v; ready=%v", err, r.ReadyCheck(nil) == nil)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner survived the drain deadline")
	}
	select {
	case <-requestDone:
	case <-time.After(5 * time.Second):
		t.Fatal("request connection survived shutdown")
	}
}
