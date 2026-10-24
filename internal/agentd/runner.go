package agentd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// Cmd is one external command: git, claude, tmux.
type Cmd struct {
	Name string
	Args []string
	// Dir is the working directory; empty means agentd's own.
	Dir string
	// Env is added to the runner's base environment, as KEY=VALUE. A later
	// entry wins over an earlier one with the same key.
	Env []string
	// UnsetEnv removes these keys from the inherited environment before Env
	// is applied. Private Git commands use it to prevent storage redirection.
	UnsetEnv []string
	// Stdin is the command's input; nil means /dev/null.
	Stdin io.Reader
	// StdoutMax, when positive, stops the process once stdout reaches this
	// many bytes. Result.Stdout holds at most that many, and StdoutOver is set.
	StdoutMax int
}

// Result is what a command printed and how it exited.
type Result struct {
	Stdout     []byte
	Stderr     []byte
	ExitCode   int
	StdoutOver bool
}

// Runner runs external commands. Tests use a fake; the daemon uses ExecRunner.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
	LookPath(name string) (string, error)
}

// CmdError is a command that ran and exited non-zero. Its message names the
// command and its first argument only: arguments can carry secrets (an MCP
// spec with a token, a credential helper), and messages end up in logs.
type CmdError struct {
	Name     string
	Sub      string
	ExitCode int
	Stderr   string
}

func (e *CmdError) Error() string {
	if e.Sub != "" {
		return fmt.Sprintf("%s %s: exit status %d", e.Name, e.Sub, e.ExitCode)
	}
	return fmt.Sprintf("%s: exit status %d", e.Name, e.ExitCode)
}

// Detail adds the command's stderr, trimmed. Use it only for commands whose
// output cannot hold a secret (git, tmux).
func (e *CmdError) Detail() string {
	msg := strings.TrimSpace(e.Stderr)
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	if msg == "" {
		return e.Error()
	}
	return e.Error() + ": " + msg
}

// ExitCodeOf returns the exit code of a CmdError, or -1 for any other error.
func ExitCodeOf(err error) int {
	var ce *CmdError
	if errors.As(err, &ce) {
		return ce.ExitCode
	}
	return -1
}

// cliTimeout bounds each claude or git call of the boot's config steps, so a
// stalled CLI (a network check, a lock on .claude.json) becomes a warning and
// the boot goes on.
var cliTimeout = 30 * time.Second

// runBounded runs c with its own time limit.
func runBounded(ctx context.Context, r Runner, limit time.Duration, c Cmd) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	res, err := r.Run(ctx, c)
	if err != nil && ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("%s %s: no answer within %s", c.Name, firstArg(c.Args), limit)
	}
	return res, err
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

// stopGrace is how long a command's process group has, after SIGTERM, to end
// on its own before Run sends SIGKILL. Git's signal handlers remove its lock
// files (index.lock, ref locks) and an unfinished `worktree add` on SIGTERM; a
// SIGKILL leaves them behind, and every later Git step there refuses them.
var stopGrace = 3 * time.Second

// waitDelay is how long Run waits for a stopped command, counted from its
// time limit. It is longer than stopGrace, so a stopped command still ends
// within waitDelay, the allowance sharedGitAdminBudget's headroom covers.
var waitDelay = 5 * time.Second

// cappedWriter keeps at most max bytes and stops the process group past that.
type cappedWriter struct {
	max  int
	buf  bytes.Buffer
	over atomic.Bool
	cmd  *exec.Cmd
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if w == nil || w.over.Load() {
		return len(p), nil
	}
	room := w.max - w.buf.Len()
	if room < 0 {
		room = 0
	}
	if len(p) > room {
		if room > 0 {
			_, _ = w.buf.Write(p[:room])
		}
		w.over.Store(true)
		if w.cmd != nil && w.cmd.Process != nil {
			_ = syscall.Kill(-w.cmd.Process.Pid, syscall.SIGKILL)
		}
		return len(p), nil
	}
	_, _ = w.buf.Write(p)
	return len(p), nil
}

// ExecRunner runs commands as child processes.
type ExecRunner struct {
	// BaseEnv is every command's environment before Cmd.Env; nil means
	// agentd's own environment.
	BaseEnv []string
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	base := r.BaseEnv
	if base == nil {
		base = os.Environ()
	}
	cmd.Env = make([]string, 0, len(base)+len(c.Env))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		remove := false
		for _, unset := range c.UnsetEnv {
			if key == unset {
				remove = true
				break
			}
		}
		if !remove {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, c.Env...)
	cmd.Stdin = c.Stdin
	// When ctx ends, stop the whole process group, not only the direct child.
	// SIGTERM comes first, so Git removes its locks and an unfinished worktree
	// before it exits (DESIGN-007). SIGKILL follows after stopGrace, and once
	// the direct child has exited, whatever is left of the group gets it at
	// once. Run stops waiting for output waitDelay after ctx ended: a
	// grandchild (node starts some) that holds the pipes open must not turn a
	// time limit into a hang.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stopping atomic.Bool
	exited := make(chan struct{})
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			return err
		}
		stopping.Store(true)
		go func() {
			timer := time.NewTimer(stopGrace)
			defer timer.Stop()
			select {
			case <-timer.C:
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			case <-exited:
			}
		}()
		return nil
	}
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	var capped, errCap *cappedWriter
	if c.StdoutMax > 0 {
		capped = &cappedWriter{max: c.StdoutMax, cmd: cmd}
		errCap = &cappedWriter{max: 64 << 10, cmd: cmd}
		cmd.Stdout, cmd.Stderr = capped, errCap
	} else {
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
	}
	err := cmd.Run()
	close(exited)
	if stopping.Load() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if capped != nil {
		res.Stdout = capped.buf.Bytes()
		res.Stderr = errCap.buf.Bytes()
		res.StdoutOver = capped.over.Load()
		if res.StdoutOver {
			err = nil
		}
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			sub := ""
			if len(c.Args) > 0 {
				sub = c.Args[0]
			}
			return res, &CmdError{Name: c.Name, Sub: sub, ExitCode: res.ExitCode, Stderr: string(res.Stderr)}
		}
		return res, fmt.Errorf("%s: %w", c.Name, err)
	}
	return res, nil
}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
