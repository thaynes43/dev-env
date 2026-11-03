//go:build linux

package usagecollector

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// runMetadata owns a finite normal vendor command and all same-group children.
// Even a failed child must prove cleanup before another provider can be started.
func runMetadata(ctx context.Context, executable string, args []string, claude bool) (raw []byte, retErr error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrCapture
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if claude {
		cmd.Env = normalClaudeEnv()
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, refused("command_start_failed", ErrCapture)
	}
	if cmd.Start() != nil {
		_ = out.Close()
		return nil, refused("command_start_failed", ErrCapture)
	}
	finished, watcher := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			_ = out.Close()
		case <-finished:
		}
	}()
	raw, err = io.ReadAll(io.LimitReader(out, (16<<10)+1))
	_ = out.Close()
	var waitErr error
	if err == nil && len(raw) <= 16<<10 && ctx.Err() == nil {
		waitErr = waitOwnedExit(ctx, cmd)
	}
	// Kill before Wait: retain the zombie leader's group identity until every
	// descendant has received termination, then reap the direct child.
	clean := stopOwnedGroup(cmd)
	close(finished)
	<-watcher
	if !clean {
		return nil, ErrCleanup
	}
	switch {
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, refused("cancelled", ErrCapture)
		}
		return nil, refused("deadline", ErrCapture)
	case len(raw) > 16<<10:
		return nil, refused("command_output_oversized", ErrCapture)
	case err != nil:
		return nil, refused("command_read_failed", ErrCapture)
	case waitErr != nil:
		return nil, refused("command_wait_failed", ErrCapture)
	case !cmd.ProcessState.Success():
		return nil, refused("command_failed", ErrCapture)
	}
	return raw, nil
}

// waitOwnedExit observes a finite command's natural exit without reaping its
// leader. EOF alone does not mean the process has exited. Keeping the zombie
// until stopOwnedGroup runs also retains the owned process-group identity while
// descendants are terminated. CommandContext's existing finite deadline and
// group cancellation bound this wait; no new command or retry is started.
func waitOwnedExit(ctx context.Context, cmd *exec.Cmd) error {
	if ctx == nil || ctx.Err() != nil || cmd == nil || cmd.Process == nil {
		return ErrCapture
	}
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err != unix.EINTR {
			return err
		}
		if ctx.Err() != nil {
			return ErrCapture
		}
	}
}

// This matches the existing Remote Control launch's static-token exclusion.
// Normal keeper-owned login/projection remains the sole credential source;
// this code never opens, copies, refreshes or requests a credential.
func normalClaudeEnv() []string {
	var result []string
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "CLAUDE_CODE_OAUTH_TOKEN=") {
			result = append(result, item)
		}
	}
	return result
}
