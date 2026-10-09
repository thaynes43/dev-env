package keeper

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const codexInitialChallengeBound = 60 * time.Second

func codexLoginEnvironment(home, tmp string) []string {
	return []string{"HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + tmp, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "NO_COLOR=1", "RUST_LOG=off"}
}

func (w *codexPromptWriter) confirmChallenge() {
	if w.urlSeen && (!w.Device || w.codeSeen) && w.Challenge != nil {
		select {
		case <-w.Challenge:
		default:
			close(w.Challenge)
		}
	}
}

func (w *codexPromptWriter) fixedState() (bool, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.urlSeen && (!w.Device || w.codeSeen), w.failureCode
}

// Classify a small pinned native error vocabulary into fixed codes. Even a line
// containing synthetic or provider token material contributes no text to output.
func fixedCodexNativeFailure(line string) string {
	switch {
	case strings.HasPrefix(line, "Error loading configuration:"):
		return "NativeConfigurationUnavailable"
	case strings.HasPrefix(line, "Error logging in with device code:"):
		switch {
		case strings.Contains(line, "device code login is not enabled"):
			return "DeviceEndpointUnavailable"
		case strings.Contains(line, "device code request failed with status"):
			return "DeviceEndpointRefused"
		case strings.Contains(line, "error sending request for url"):
			return "NativeNetworkUnavailable"
		case strings.Contains(line, "Read-only file system"), strings.Contains(line, "Permission denied"):
			return "NativeFilesystemUnavailable"
		default:
			return "NativeLoginFailed"
		}
	}
	return ""
}

// The wrapper starts exactly one native invocation. Native device polling after
// challenge presentation remains part of that invocation. The initial challenge
// bound does not replace the original fifteen-minute owner ceremony bound.
func runCodexLoginProcess(cmd *exec.Cmd, prompt *codexPromptWriter, cancel context.CancelFunc, report *CodexLoginReport, initialBound time.Duration) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var terminal *os.File
	var foreground int
	if f, ok := cmd.Stdin.(*os.File); ok {
		if pgrp, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP); err == nil && pgrp > 0 {
			terminal, foreground = f, pgrp
			// Native browser input must retain its controlling terminal: a new
			// background process group would receive SIGTTIN on a tty read.
			cmd.SysProcAttr.Foreground, cmd.SysProcAttr.Ctty = true, int(f.Fd())
		}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err := cmd.Start(); err != nil {
		report.FailureCode = "NativeStartUnavailable"
		if terminal != nil && restoreCodexForeground(terminal, foreground) != nil {
			report.FailureCode = "NativeTerminalRestoreUnavailable"
		}
		return ErrCodexHelper
	}
	report.NativeStarted = true
	finished, monitored := make(chan struct{}), make(chan bool, 1)
	go func() {
		timer := time.NewTimer(initialBound)
		defer timer.Stop()
		select {
		case <-finished:
			monitored <- false
		case <-prompt.Challenge:
			monitored <- false
		case <-timer.C:
			cancel()
			monitored <- true
		}
	}()
	err := cmd.Wait()
	close(finished)
	initialTimedOut := <-monitored
	// Wait reaps the direct child. A group kill is only a request; observe the
	// exact group absent before allowing attempt cancellation/staging cleanup.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	report.NativeGroupAbsent = waitCodexGroupAbsent(cmd.Process.Pid, time.Second)
	terminalRestored := terminal == nil || restoreCodexForeground(terminal, foreground) == nil
	if cmd.ProcessState != nil {
		exit := cmd.ProcessState.ExitCode()
		report.NativeExitCode = &exit
	}
	report.ChallengePresented, report.FailureCode = prompt.fixedState()
	if !report.NativeGroupAbsent {
		report.FailureCode = "NativeGroupTerminationUnconfirmed"
		return ErrCodexHelper
	}
	if !terminalRestored {
		report.FailureCode = "NativeTerminalRestoreUnavailable"
		return ErrCodexHelper
	}
	if initialTimedOut {
		report.FailureCode = "InitialChallengeTimeout"
	}
	if err != nil || !report.ChallengePresented {
		if report.FailureCode == "" {
			report.FailureCode = "NativeFailedBeforeChallenge"
			if report.ChallengePresented {
				report.FailureCode = "NativeLoginFailed"
			}
		}
		return ErrCodexHelper
	}
	if report.FailureCode != "" {
		return errors.New("native login returned contradictory fixed failure metadata")
	}
	return nil
}

// Probe only this invocation's process group with a finite timer. Zombies may
// keep it observable; they never count as proof that the group is absent.
func waitCodexGroupAbsent(pgid int, bound time.Duration) bool {
	timer := time.NewTimer(bound)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-tick.C:
		}
	}
}

func restoreCodexForeground(terminal *os.File, foreground int) error {
	// The helper is temporarily behind the native foreground group. Block
	// SIGTTOU on this thread only while restoring the original tty owner.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var blocked, previous unix.Sigset_t
	blocked.Val[(unix.SIGTTOU-1)/64] = 1 << ((unix.SIGTTOU - 1) % 64)
	if err := unix.PthreadSigmask(unix.SIG_BLOCK, &blocked, &previous); err != nil {
		return err
	}
	defer func() { _ = unix.PthreadSigmask(unix.SIG_SETMASK, &previous, nil) }()
	return unix.IoctlSetPointerInt(int(terminal.Fd()), unix.TIOCSPGRP, foreground)
}
