//go:build portallocaltest

// Command localtest serves the real portal handler set on a loopback address
// with fixture data and one fixed test principal (DESIGN-055). It is built
// only with -tags portallocaltest and is in no image. Every page carries a
// local test banner. It opens no connection to any service.
//
//	go run -tags portallocaltest ./web/portal/localtest [-listen 127.0.0.1:0] [-fixtures scripts/trials/portal_product]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/web/portal/session"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("portal-localtest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "127.0.0.1:0", "loopback IP and port (127.0.0.0/8 or ::1); port 0 picks a free one")
	fixtures := flags.String("fixtures", filepath.Join("scripts", "trials", "portal_product"), "directory holding sessions.json and tasks.json")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	addr, err := loopbackAddr(*listen)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portal-localtest:", err)
		return 2
	}
	sessions, sessionsErr := readFixture(filepath.Join(*fixtures, "sessions.json"))
	tasks, tasksErr := readFixture(filepath.Join(*fixtures, "tasks.json"))
	if sessionsErr != nil || tasksErr != nil {
		_, _ = fmt.Fprintln(stderr, "portal-localtest: fixtures unavailable; run from the repository root or pass -fixtures")
		return 1
	}
	listener, err := net.Listen("tcp", addr.String())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "portal-localtest: listen failed:", err)
		return 1
	}
	bound, err := netip.ParseAddrPort(listener.Addr().String())
	if err != nil || !bound.Addr().Unmap().IsLoopback() {
		_ = listener.Close()
		_, _ = fmt.Fprintln(stderr, "portal-localtest: bound address is not loopback")
		return 1
	}
	instance, err := session.NewLocalTest(bound, session.LocalTestFixtures{Sessions: sessions, Tasks: tasks})
	if err != nil {
		_ = listener.Close()
		_, _ = fmt.Fprintln(stderr, "portal-localtest: fixtures invalid")
		return 1
	}
	defer instance.Close()
	server := &http.Server{
		Handler: instance.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0),
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.Serve(listener) }()
	_, _ = fmt.Fprintf(stdout, "Portal local test instance (fixture data, fixed test principal, not production): http://%s%s\n", bound, session.SessionsPath)
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		return 0
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return 0
		}
		_, _ = fmt.Fprintln(stderr, "portal-localtest: server stopped:", err)
		return 1
	}
}

// loopbackAddr accepts only a literal loopback IP and a port. Names (even
// localhost), wildcard and LAN addresses, and Unix socket paths are refused.
func loopbackAddr(raw string) (netip.AddrPort, error) {
	addr, err := netip.ParseAddrPort(raw)
	if err != nil {
		return netip.AddrPort{}, errors.New("-listen must be a loopback IP and port, such as 127.0.0.1:0 or [::1]:0")
	}
	if !addr.Addr().Unmap().IsLoopback() || addr.Addr().Zone() != "" {
		return netip.AddrPort{}, errors.New("-listen refuses non-loopback addresses")
	}
	return netip.AddrPortFrom(addr.Addr().Unmap(), addr.Port()), nil
}

func readFixture(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return io.ReadAll(io.LimitReader(file, 256<<10+1))
}
