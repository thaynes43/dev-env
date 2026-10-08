package agentrun

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestForwardChild supplies a real, idle subprocess. It blocks on a signal,
// never spins, and lets the parent prove kill+wait rather than only mock it.
func TestForwardChild(t *testing.T) {
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[len(os.Args)-1], "forward-helper:") {
		return
	}
	mode := strings.TrimPrefix(os.Args[len(os.Args)-1], "forward-helper:")
	if mode == "exit" {
		_, _ = fmt.Fprintln(os.Stderr, "private-forward-error")
		os.Exit(1)
	}
	if mode == "output" {
		_, _ = fmt.Fprintln(os.Stdout, "private-token-output")
		_, _ = fmt.Fprintln(os.Stderr, "private-token-error")
		os.Exit(1)
	}
	if err := os.WriteFile(mode, []byte(fmt.Sprintf("%d", os.Getpid())), 0o600); err != nil {
		os.Exit(2)
	}
	if filepath.Base(mode) == "ready" {
		_, _ = fmt.Fprintln(os.Stdout, "Forwarding from 127.0.0.1:41234 -> 8443")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	os.Exit(0)
}

func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestForwardChild$", "--", "forward-helper:" + mode}
}

func assertChildGone(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscanf(string(raw), "%d", &pid); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("forward subprocess %d is still alive or unreaped", pid)
	}
}

func TestPortForwardProcessStopsAndIsReaped(t *testing.T) {
	for _, reason := range []string{"close", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			path := filepath.Join(t.TempDir(), "ready")
			address, stop, err := portForwardWithin(ctx, helperArgv(path), 3*time.Second)
			if err != nil || address != "https://127.0.0.1:41234" {
				t.Fatalf("address=%q, err=%v", address, err)
			}
			if reason == "cancel" {
				cancel()
			}
			stop()
			stop() // Cleanup can safely run after cancellation or more than once.
			assertChildGone(t, path)
		})
	}
}

func TestPortForwardStartupFailures(t *testing.T) {
	t.Run("exit before readiness", func(t *testing.T) {
		_, stop, err := portForwardWithin(t.Context(), helperArgv("exit"), 3*time.Second)
		if err == nil || stop != nil || strings.Contains(err.Error(), "private-") {
			t.Fatalf("stop=%v err=%v", stop != nil, err)
		}
	})
	t.Run("timeout kills and waits", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "silent")
		_, stop, err := portForwardWithin(t.Context(), helperArgv(path), 500*time.Millisecond)
		if err == nil || stop != nil {
			t.Fatalf("stop=%v err=%v", stop != nil, err)
		}
		assertChildGone(t, path)
	})
	t.Run("already canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		_, stop, err := portForwardWithin(ctx, helperArgv("exit"), 3*time.Second)
		if err == nil || stop != nil {
			t.Fatalf("stop=%v err=%v", stop != nil, err)
		}
	})
}

func TestCommandOutputErrorsDiscardCredentialOutput(t *testing.T) {
	raw, err := commandOutput(t.Context(), helperArgv("output"))
	if raw != nil || err == nil || strings.Contains(err.Error(), "private-") {
		t.Fatalf("raw=%q err=%v", raw, err)
	}
}

func TestForwardReadinessRejectsOtherAddresses(t *testing.T) {
	ready := make(chan string, 1)
	out := &forwardOutput{ready: ready}
	for _, line := range []string{
		"Forwarding from 0.0.0.0:41234 -> 8443\n",
		"Forwarding from 127.0.0.1:41234 -> 8080\n",
		"Forwarding from 127.0.0.1:0 -> 8443\n",
		"Forwarding from 127.0.0.1:65536 -> 8443\n",
	} {
		_, _ = out.Write([]byte(line))
	}
	select {
	case address := <-ready:
		t.Fatalf("accepted %q", address)
	default:
	}
	_, _ = out.Write([]byte("Forwarding from 127.0.0.1:"))
	_, _ = out.Write([]byte("41234 -> 8443\n"))
	if address := <-ready; address != "https://127.0.0.1:41234" {
		t.Errorf("address=%q", address)
	}
}
