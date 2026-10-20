package keeper

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/funcr"
)

// LIVE-238a, O3: while a login ceremony holds the reservation the worker's tick
// finds a non-Ready record by design. That is not a fault, so it must not log at
// ERROR; every other refresh failure still does.
func TestCodexTickDuringALoginCeremonyIsNotAnErrorLine(t *testing.T) {
	w, _, _ := codexFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := w.serveControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	saveCodexFixture(t, w, freshCodexRecord(t, w))
	if w.tick(ctx) != nil {
		t.Fatal("a fresh record must tick clean")
	}
	start := w.begin(ctx)
	if !start.OK {
		t.Fatalf("begin: %+v", start)
	}
	err = w.tick(ctx)
	if !errors.Is(err, errCodexLoginInProgress) || !errors.Is(err, errCodexRefresh) {
		t.Fatalf("tick during a ceremony = %v", err)
	}
	var mu sync.Mutex
	var lines []string
	w.Log = funcr.New(func(prefix, args string) { mu.Lock(); lines = append(lines, prefix+" "+args); mu.Unlock() }, funcr.Options{})
	w.logTickError(err)
	if len(lines) != 0 {
		t.Fatalf("a ceremony in progress logged at the default level: %v", lines)
	}
	// Any other refresh failure is still an ERROR.
	w.logTickError(errCodexRefresh)
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 1 || !strings.Contains(lines[0], "Codex authentication needs attention") {
		t.Fatalf("a real failure was not logged: %v", lines)
	}
	// Cancel restores the credential: the next tick is clean again.
	if c := w.cancelLogin(ctx, start.AttemptUID); !c.OK {
		t.Fatalf("cancel: %+v", c)
	}
	if err := w.tick(ctx); err != nil {
		t.Fatalf("tick after cancel = %v", err)
	}
}
