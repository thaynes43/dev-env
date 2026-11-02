//go:build linux

package usagecollector

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
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
		return nil, ErrCapture
	}
	if cmd.Start() != nil {
		_ = out.Close()
		return nil, ErrCapture
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
	// Kill before Wait: retain the zombie leader's group identity until every
	// descendant has received termination, then reap the direct child.
	clean := stopOwnedGroup(cmd)
	close(finished)
	<-watcher
	if !clean {
		return nil, ErrCleanup
	}
	if err != nil || len(raw) > 16<<10 || ctx.Err() != nil || !cmd.ProcessState.Success() {
		return nil, ErrCapture
	}
	return raw, nil
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
