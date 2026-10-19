package claudelogin

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// runNative starts exactly one `claude auth login` and waits for it. The CLI
// reads the caller's one-time code from cmd.Stdin. When that is the caller's
// terminal, the CLI is made the foreground group for the tty read and the
// original owner is restored afterwards. A watcher cancels the run if the CLI
// does not print its sign-in link within initialBound; the fifteen minute
// ceremony bound is the caller's context.
func runNative(cmd *exec.Cmd, prompt *promptWriter, cancel context.CancelFunc, report *Report, initialBound time.Duration) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var terminal *os.File
	var foreground int
	if f, ok := cmd.Stdin.(*os.File); ok {
		if pgrp, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP); err == nil && pgrp > 0 {
			terminal, foreground = f, pgrp
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
		if terminal != nil && restoreForeground(terminal, foreground) != nil {
			report.FailureCode = "NativeTerminalRestoreUnavailable"
		}
		return ErrHelper
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
	// exact group absent before the staging may be removed.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	report.NativeGroupAbsent = waitGroupAbsent(cmd.Process.Pid, time.Second)
	terminalRestored := terminal == nil || restoreForeground(terminal, foreground) == nil
	if cmd.ProcessState != nil {
		exit := cmd.ProcessState.ExitCode()
		report.NativeExitCode = &exit
	}
	report.ChallengePresented = prompt.presented()
	switch {
	case !report.NativeGroupAbsent:
		report.FailureCode = "NativeGroupTerminationUnconfirmed"
	case !terminalRestored:
		report.FailureCode = "NativeTerminalRestoreUnavailable"
	case initialTimedOut:
		report.FailureCode = "InitialChallengeTimeout"
	case !report.ChallengePresented:
		report.FailureCode = "NativeFailedBeforeChallenge"
	case err != nil:
		report.FailureCode = "NativeLoginFailed"
	}
	if report.FailureCode != "" {
		return ErrHelper
	}
	return nil
}

// waitGroupAbsent probes only this invocation's process group with a finite
// timer. Zombies may keep it observable; they never count as proof of absence.
func waitGroupAbsent(pgid int, bound time.Duration) bool {
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

func restoreForeground(terminal *os.File, foreground int) error {
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
