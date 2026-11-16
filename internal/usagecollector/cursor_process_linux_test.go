//go:build linux

package usagecollector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestCursorOwnPTYBuiltinAndBoundedCancellation(t *testing.T) {
	options := cursorBuiltinFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	report, err := CollectCursor(ctx, options, time.Now)
	if err != nil || report.AccountRef != nil || report.IncludedPercentDisplay == nil || *report.IncludedPercentDisplay != 61 || report.CollectionStartedAt == "" || report.ReceivedAt == "" || time.Since(start) > time.Second {
		t.Fatal("own normal builtin fixture failed or leaked", err)
	}
	if os.WriteFile(options.Executable, []byte("#!/bin/sh\n/bin/sleep 60\n"), 0700) != nil {
		t.Fatal("fixture write")
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	start = time.Now()
	if _, err = CollectCursor(ctx2, options, time.Now); err == nil || time.Since(start) > time.Second {
		t.Fatal("terminal inherited output defeated cancellation", err)
	}
}

// Force the syscall result instead of depending on a scheduler signal landing
// during a 100ms poll. The same process must complete the normal PTY handshake.
func TestCursorOwnPTYBuiltinAfterInterruptedPoll(t *testing.T) {
	options := cursorBuiltinFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	interrupted := false
	poll := func(fds []unix.PollFd, timeout int) (int, error) {
		if !interrupted {
			interrupted = true
			return -1, unix.EINTR
		}
		return unix.Poll(fds, timeout)
	}
	report, err := collectCursor(ctx, options, time.Now, poll)
	if err != nil {
		var detail *diagnosticError
		if errors.As(err, &detail) {
			t.Fatalf("interrupted poll aborted the builtin: reason=%s context=%v", detail.reason, ctx.Err())
		}
		t.Fatal(err)
	}
	if !interrupted || report.AccountRef != nil || report.IncludedPercentDisplay == nil || *report.IncludedPercentDisplay != 61 || report.CollectionStartedAt == "" || report.ReceivedAt == "" {
		t.Fatal("interrupted builtin failed or leaked")
	}
}

func TestCursorPollErrorsStillRefuse(t *testing.T) {
	for _, test := range []struct {
		name   string
		cancel bool
		errno  error
		reason string
	}{
		{name: "terminal-error", errno: unix.EBADF, reason: "builtin_terminal_unavailable"},
		{name: "cancelled-interruption", cancel: true, errno: unix.EINTR, reason: "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			options := cursorBuiltinFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			polls := 0
			poll := func(_ []unix.PollFd, _ int) (int, error) {
				polls++
				if test.cancel {
					cancel()
				}
				return -1, test.errno
			}
			_, err := collectCursor(ctx, options, time.Now, poll)
			requireBuiltinReason(t, "cursor", err, test.reason)
			if polls != 1 {
				t.Fatalf("terminal failure was polled again: %d calls", polls)
			}
		})
	}
}

func cursorBuiltinFixture(t *testing.T) CursorOptions {
	t.Helper()
	// Fixtures must not inherit in-cluster access from a session pod.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "KUBERNETES_") || key == "KUBECONFIG" {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if os.Mkdir(workspace, 0700) != nil {
		t.Fatal("private workspace")
	}
	fixture := filepath.Join(dir, "cursor-fixture")
	// No provider, login, model or user terminal. The fixture validates every
	// input and emits only the same separately observed local UI state order.
	script := "#!/bin/sh\nstty -echo -icanon\nprintf 'Cursor Agent v" + providerallowance.CursorCLIQualifiedVersion + "\\nPlan, search, build anything\\n%s\\n' \"$2\"\nfirst=$(dd bs=1 count=6 2>/dev/null)\n[ \"$first\" = /usage ] || exit 21\nprintf '→ /usage\\nShow plan and on-demand usage\\n'\nIFS= read -r second\n[ -z \"$second\" ] || exit 22\nprintf '%s' '" + strings.ReplaceAll(cursorScreen, "'", "'\\''") + "'\n/bin/sleep 60\n"
	if os.WriteFile(fixture, []byte(script), 0700) != nil {
		t.Fatal("fixture write")
	}
	return CursorOptions{Executable: fixture, Workspace: workspace}
}

func TestCursorRefusesNonemptyOrUnsafeWorkspaceBeforeProcess(t *testing.T) {
	workspace := t.TempDir()
	if os.Chmod(workspace, 0755) != nil {
		t.Fatal("chmod")
	}
	if _, err := CollectCursor(context.Background(), CursorOptions{Executable: "/never-start", Workspace: workspace}, time.Now); err == nil {
		t.Fatal("unsafe workspace accepted")
	}
	if os.Chmod(workspace, 0700) != nil || os.WriteFile(filepath.Join(workspace, "user-work"), nil, 0600) != nil {
		t.Fatal("setup")
	}
	if _, err := CollectCursor(context.Background(), CursorOptions{Executable: "/never-start", Workspace: workspace}, time.Now); err == nil {
		t.Fatal("existing work accepted")
	}
}
