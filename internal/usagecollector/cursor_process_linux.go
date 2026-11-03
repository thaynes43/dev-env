//go:build linux

package usagecollector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

type CursorOptions struct {
	Executable          string
	Workspace           string
	AllowWorkspaceTrust bool
}

// CollectCursor owns one fresh interactive process, never an existing user's
// terminal. Only the version-qualified local builtin state machine can write
// input. The caller independently authorizes normal-client auth/cache behavior.
// AccountRef remains nil: an independently qualified metadata reader is required
// for binding, rather than copying caller assertions into provider evidence.
func CollectCursor(ctx context.Context, options CursorOptions, now func() time.Time) (capture providerallowance.CursorCLIUsageCapture, captureErr error) {
	fail := func() (providerallowance.CursorCLIUsageCapture, error) {
		return providerallowance.CursorCLIUsageCapture{}, refusedWithContext(ctx, "capture_refused")
	}
	if ctx == nil || ctx.Err() != nil || now == nil || !filepath.IsAbs(options.Executable) || !filepath.IsAbs(options.Workspace) || filepath.Clean(options.Workspace) != options.Workspace {
		return fail()
	}
	resolved, err := filepath.EvalSymlinks(options.Workspace)
	if err != nil || resolved != options.Workspace {
		return fail()
	}
	info, err := os.Lstat(options.Workspace)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return fail()
	}
	entries, err := os.ReadDir(options.Workspace)
	if err != nil || len(entries) != 0 {
		return fail()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	master, slave, err := cursorPTY()
	if err != nil {
		return capture, refusedWithContext(ctx, "builtin_terminal_unavailable")
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = slave.Close() }()
	cmd := exec.CommandContext(ctx, options.Executable, "--workspace", options.Workspace)
	cmd.Dir = options.Workspace
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	started := now()
	if started.IsZero() || cmd.Start() != nil {
		return capture, refusedWithContext(ctx, "command_start_failed")
	}
	_ = slave.Close()
	masterFD := int32(master.Fd())
	watcher := make(chan struct{})
	go func() {
		defer close(watcher)
		<-ctx.Done()
		_ = master.Close()
	}()
	defer func() {
		cancel()
		_ = master.Close()
		if !stopOwnedGroup(cmd) {
			capture, captureErr = providerallowance.CursorCLIUsageCapture{}, ErrCleanup
		}
		<-watcher
	}()
	protocol := cursorProtocol{workspace: options.Workspace, trust: options.AllowWorkspaceTrust}
	buffer := make([]byte, 4096)

	for {
		fds := []unix.PollFd{{Fd: masterFD, Events: unix.POLLIN}}
		ready, pollErr := unix.Poll(fds, 100)
		if pollErr != nil || ctx.Err() != nil {
			return capture, refusedWithContext(ctx, "builtin_terminal_unavailable")
		}
		var input string
		var report *providerallowance.CursorCLIUsageCapture
		var protocolErr error
		if ready == 0 {
			input, report, protocolErr = protocol.settle()
		} else {
			n, readErr := master.Read(buffer)
			if n > 0 {
				input, report, protocolErr = protocol.accept(buffer[:n])
			}
			if readErr != nil {
				return capture, refusedWithContext(ctx, "builtin_stream_failed")
			}
		}
		if ctx.Err() != nil {
			return fail()
		}
		if protocolErr != nil {
			return capture, protocolErr
		}
		if input != "" {
			written, e := master.WriteString(input)
			if e != nil || written != len(input) {
				return capture, refusedWithContext(ctx, "builtin_write_failed")
			}
		}
		if report != nil {
			received := now()
			if received.Before(started) || ctx.Err() != nil {
				return fail()
			}
			report.CollectionStartedAt = started.UTC().Format(time.RFC3339Nano)
			report.ReceivedAt = received.UTC().Format(time.RFC3339Nano)
			if _, e := providerallowance.MarshalCursorCLIUsage(*report); e != nil {
				return capture, refusedWithContext(ctx, "builtin_display_unqualified")
			}
			return *report, nil
		}
	}

}

func cursorPTY() (*os.File, *os.File, error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, ErrCapture
	}
	master := os.NewFile(uintptr(fd), "cursor-owned-pty")
	fail := func() (*os.File, *os.File, error) {
		_ = master.Close()
		return nil, nil, ErrCapture
	}
	if unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0) != nil {
		return fail()
	}
	index, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return fail()
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(index), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return fail()
	}
	if unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Row: 50, Col: 180}) != nil {
		_ = slave.Close()
		return fail()
	}
	return master, slave, nil
}
