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
	// Stdin is the command's input; nil means /dev/null.
	Stdin io.Reader
}

// Result is what a command printed and how it exited.
type Result struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
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

// waitDelay is how long Run waits for a killed command's output pipes.
var waitDelay = 5 * time.Second

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
	cmd.Env = append(append([]string(nil), base...), c.Env...)
	cmd.Stdin = c.Stdin
	// When ctx ends, kill the whole process group, not only the direct child,
	// and stop waiting for output soon after: a grandchild (node starts some)
	// that holds the pipes open must not turn a time limit into a hang.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			sub := ""
			if len(c.Args) > 0 {
				sub = c.Args[0]
			}
			return res, &CmdError{Name: c.Name, Sub: sub, ExitCode: res.ExitCode, Stderr: stderr.String()}
		}
		return res, fmt.Errorf("%s: %w", c.Name, err)
	}
	return res, nil
}

// LookPath implements Runner.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
