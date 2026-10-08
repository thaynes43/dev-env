package broker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// ConsoleRunner serves HTTP behind Traefik's TLS and isolated Authentik
// forward-auth path. Every broker replica serves it; only the leader reconciles.
type ConsoleRunner struct {
	Addr    string
	Handler http.Handler
	ready   atomic.Bool
	// listening reports the bound address to the focused lifecycle test.
	listening       func(string)
	shutdownTimeout time.Duration // zero keeps the production ten-second drain
}

func (r *ConsoleRunner) NeedLeaderElection() bool { return false }

func (r *ConsoleRunner) ReadyCheck(_ *http.Request) error {
	if !r.ready.Load() {
		return errors.New("the approval console is not listening")
	}
	return nil
}

func (r *ConsoleRunner) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", r.Addr)
	if err != nil {
		return errors.New("listen on the approval console port")
	}
	defer r.ready.Store(false)
	srv := &http.Server{
		Handler: r.Handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10,
	}
	drain := 10 * time.Second
	if r.shutdownTimeout > 0 {
		drain = r.shutdownTimeout
	}
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-wctx.Done()
		r.ready.Store(false)
		sctx, cancel := context.WithTimeout(context.Background(), drain)
		defer cancel()
		if err := srv.Shutdown(sctx); err != nil {
			_ = srv.Close()
		}
	}()
	r.ready.Store(true)
	if r.listening != nil {
		r.listening(ln.Addr().String())
	}
	err = srv.Serve(ln)
	stop()
	<-done
	if !errors.Is(err, http.ErrServerClosed) {
		return errors.New("serve the approval console")
	}
	return nil
}
