//go:build linux

package usagecollector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A finite vendor command can close stdout before its normal shutdown finishes.
// The FIFO holds that shutdown open so the test does not depend on winning the
// small scheduling race between EOF and the process's exit.
func TestMetadataWaitsForNaturalExitAfterStdoutEOF(t *testing.T) {
	testFiniteCommandNaturalExit(t, "finite metadata\n", func(ctx context.Context, executable string) error {
		raw, err := runMetadata(ctx, executable, nil, false)
		if err == nil && string(raw) != "finite metadata\n" {
			return fmt.Errorf("metadata changed: %q", raw)
		}
		return err
	})
}

const finiteClaudeStream = `{"type":"assistant","usage_report":{"rate_limits":{"limits":[{"kind":"weekly_all","group":"weekly","percent":5,"resets_at":null,"scope":null,"severity":"normal","is_active":true}]}}}` + "\n" +
	`{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}` + "\n"

func TestClaudeBuiltinWaitsForNaturalExitAfterStdoutEOF(t *testing.T) {
	testFiniteCommandNaturalExit(t, finiteClaudeStream, func(ctx context.Context, executable string) error {
		capture, err := collectClaudeBuiltin(ctx, executable, time.Now)
		if err == nil && len(capture.Limits) != 1 {
			return fmt.Errorf("quota report changed")
		}
		return err
	})
}

func testFiniteCommandNaturalExit(t *testing.T, output string, run func(context.Context, string) error) {
	t.Helper()
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	for _, path := range []string{ready, release} {
		if err := unix.Mkfifo(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	readyFile, err := os.OpenFile(ready, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer readyFile.Close()
	releaseFile, err := os.OpenFile(release, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFile.Close()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	executable := filepath.Join(dir, "metadata-fixture")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %s\nprintf r > %s\nexec 1>&-\nIFS= read -r ignored < %s\nexit 0\n", quote(output), quote(ready), quote(release))
	if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	completed := make(chan error, 1)
	go func() { completed <- run(ctx, executable) }()
	prepared := make(chan struct{})
	go func() {
		var b [1]byte
		_, _ = readyFile.Read(b[:])
		close(prepared)
	}()
	select {
	case <-prepared:
	case err := <-completed:
		t.Fatalf("finite command ended before its controlled shutdown: %v", err)
	case <-ctx.Done():
		t.Fatal("fixture did not prepare its controlled shutdown")
	}
	select {
	case err := <-completed:
		t.Fatalf("stdout EOF killed a command before normal exit: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := releaseFile.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatalf("successful finite command refused: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("normal command shutdown did not finish")
	}
}

func TestFiniteCommandsRejectNonzeroExitAfterValidOutput(t *testing.T) {
	for _, kind := range []string{"metadata", "claude"} {
		t.Run(kind, func(t *testing.T) {
			output := "finite metadata\n"
			if kind == "claude" {
				output = finiteClaudeStream
			}
			executable := filepath.Join(t.TempDir(), "finite-command-fixture")
			body := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(output, "'", "'\\''") + "'\nexit 7\n"
			if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var err error
			if kind == "claude" {
				_, err = collectClaudeBuiltin(ctx, executable, time.Now)
			} else {
				_, err = runMetadata(ctx, executable, nil, false)
			}
			if err == nil {
				t.Fatal("valid output concealed failed command exit")
			}
		})
	}
}

func TestMetadataClosedStdoutDoesNotDefeatDeadline(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "metadata-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'finite metadata\\n'\nexec 1>&-\nexec /bin/sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := runMetadata(ctx, executable, nil, false); err == nil || time.Since(started) > time.Second {
		t.Fatalf("closed stdout bypassed finite command deadline: %v", err)
	}
}
