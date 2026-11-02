//go:build linux

package agentrun

import (
	"context"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestUsageCaptureCancelsAnOpenStdinPipe(t *testing.T) {
	h := newHarness(t)
	_, trust := usageCaptureFixture(t, h)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	// A partial document proves the reader has input, but the writer stays
	// open. Cancellation must return without waiting for EOF or closing stdin.
	if _, err := writer.Write([]byte(`{"schema":`)); err != nil {
		t.Fatal(err)
	}
	h.env.Stdin = reader
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- Run(ctx, []string{"usage", "capture", "-o", "json", "--input", "-", "--trust-file", trust}, h.env)
	}()
	// Synchronize with an actual drained pipe, rather than a timing assumption.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var pending int
		pending, err = unix.IoctlGetInt(int(reader.Fd()), unix.TIOCINQ)
		if err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("stdin read did not start")
		case <-tick.C:
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != ExitFailed || h.stdout.Len() != 0 || len(h.api.requests()) != 0 || len(h.ran) != 0 {
			t.Fatal("cancelled stdin performed an action")
		}
	case <-deadline.C:
		t.Fatal("cancelled stdin remained blocked on EOF")
	}
}
