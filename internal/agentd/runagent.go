package agentd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// drainWait is how long run-agent reads the CLI's output after it exits.
var drainWait = 5 * time.Second

// maxStreamLine is the longest stream-json line run-agent parses.
var maxStreamLine = 16 << 20

// taskResult is ~/.agentd/task-result.json, written when the agent exits.
type taskResult struct {
	protocol.TaskResult
	StartedAt      time.Time       `json:"startedAt"`
	ConversationID string          `json:"conversationId"`
	BootID         string          `json:"bootId"`
	Usage          *protocol.Usage `json:"usage,omitempty"`
}

// agentPid is ~/.agentd/agent.pid: the agent CLI's pid and its start time, so
// a pid reused after a container restart is never mistaken for the agent.
type agentPid struct {
	Pid   int    `json:"pid"`
	Start string `json:"start"`
}

// tuiExit is ~/.agentd/tui-exit.json: how the latest TUI run ended (D-58).
type tuiExit struct {
	ExitCode       int       `json:"exitCode"`
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
	ConversationID string    `json:"conversationId"`
	BootID         string    `json:"bootId"`
}

// RunAgent is `agentd run-agent --launch <file>`, the process in the tmux
// pane (D-42). It runs the agent CLI with the prompt on stdin, writes a
// readable log to the pane and the task log, keeps the raw stream-json, stops
// the task at its timeout, forwards a SIGTERM, SIGINT or SIGHUP it receives to
// the CLI, and writes the result file when the CLI exits. It returns the
// CLI's exit code. A TUI launch (D-58) runs the CLI on the pane's terminal
// instead (runTUI).
func RunAgent(launchPath string, pane io.Writer, signals <-chan os.Signal, stopGrace time.Duration) int {
	var l Launch
	if err := readJSONFile(launchPath, &l); err != nil {
		_, _ = fmt.Fprintf(pane, "agentd run-agent: %v\n", err)
		return 1
	}
	if len(l.Argv) == 0 {
		_, _ = fmt.Fprintln(pane, "agentd run-agent: the launch has no command")
		return 1
	}
	if err := validateWorkspaceLaunch(l); err != nil {
		_, _ = fmt.Fprintf(pane, "agentd run-agent: shared writer admission: %v\n", err)
		return 1
	}
	if l.Provider == protocol.AgentCodex {
		if err := verifyManagedCodexCLI(l); err != nil {
			_, _ = fmt.Fprintln(pane, err)
			return 1
		}
	}
	stateDir := filepath.Dir(launchPath)
	if l.TUI {
		return runTUI(l, stateDir, os.Stdin, pane, signals, stopGrace)
	}

	logf, err := os.OpenFile(l.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintf(pane, "agentd run-agent: task log: %v\n", err)
		return 1
	}
	defer func() { _ = logf.Close() }()
	events, err := os.OpenFile(l.EventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		_, _ = fmt.Fprintf(pane, "agentd run-agent: events file: %v\n", err)
		return 1
	}
	defer func() { _ = events.Close() }()
	out := &lockedWriter{w: io.MultiWriter(pane, logf)}

	// stdout and stderr share one pipe that agentd owns, so a child of the CLI
	// that keeps it open cannot hold up the result once the CLI has exited.
	pr, pw, err := os.Pipe()
	if err != nil {
		_, _ = fmt.Fprintf(out, "agentd run-agent: %v\n", err)
		return 1
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = agentEnv(os.Environ(), l)
	cmd.Stdin = strings.NewReader(l.Prompt)
	cmd.Stdout, cmd.Stderr = pw, pw
	if l.Provider == protocol.AgentCodex {
		cmd.Stderr = out
		cmd.WaitDelay = drainWait
	}
	// Its own process group, so a timeout can stop the CLI's children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	started := time.Now().UTC()
	_, _ = fmt.Fprintf(out, "[agentd] task %s started %s, conversation %s\n", l.Session, started.Format(time.RFC3339), l.ConversationID)
	if err := validateWorkspaceLaunch(l); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_, _ = fmt.Fprintln(out, "agentd: shared writer proof changed before spawn")
		return 1
	}
	if err := cmd.Start(); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		_, _ = fmt.Fprintf(out, "agentd run-agent: start %s: %v\n", l.Argv[0], err)
		writeResult(stateDir, l, started, 127, false, streamResult{})
		_, _ = fmt.Fprintf(out, "TASK-EXIT:%d\n", 127)
		return 127
	}
	_ = pw.Close()
	pid := cmd.Process.Pid
	_ = writeJSONFile(filepath.Join(stateDir, pidFile), agentPid{Pid: pid, Start: procStartTime(pid)})

	var res streamResult
	var codex codexStream
	protocolRefused := false
	parseFailed := make(chan error, 1)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		sc := bufio.NewScanner(pr)
		if l.Provider == protocol.AgentCodex {
			sc.Split(codexJSONLine)
		}
		sc.Buffer(make([]byte, 0, min(64<<10, maxStreamLine)), maxStreamLine)
		for sc.Scan() {
			line := sc.Bytes()
			_, _ = events.Write(append(append([]byte(nil), line...), '\n'))
			if l.Provider == protocol.AgentCodex {
				if err := codex.line(line, launchPath, &l, out, &res); err != nil {
					codex.failed = true
					protocolRefused = true
					parseFailed <- err
					_, _ = io.Copy(io.Discard, pr)
					return
				}
			} else {
				renderStreamLine(line, out, &res)
			}
		}
		if err := sc.Err(); err != nil {
			if l.Provider == protocol.AgentCodex {
				codex.failed = true
				protocolRefused = true
				parseFailed <- errors.New("native Codex output is truncated or exceeds the line bound")
				_, _ = io.Copy(io.Discard, pr)
				return
			}
			// A line too long to parse: keep the rest of the output in the
			// events file, unparsed, so the CLI never blocks on a full pipe.
			_, _ = fmt.Fprintf(out, "[agentd] output no longer parsed: %v\n", err)
			_, _ = io.Copy(events, pr)
		}
	}()
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var mu sync.Mutex
	timedOut := false
	var killTimer *time.Timer
	stop := func(why string) {
		mu.Lock()
		defer mu.Unlock()
		if killTimer != nil {
			return
		}
		_, _ = fmt.Fprintf(out, "[agentd] %s: SIGTERM to the agent (pid %d)\n", why, pid)
		_ = syscall.Kill(pid, syscall.SIGTERM)
		killTimer = time.AfterFunc(stopGrace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
	}
	exited := make(chan struct{})
	var timeout <-chan time.Time
	if l.Timeout > 0 {
		t := time.NewTimer(l.Timeout)
		defer t.Stop()
		timeout = t.C
	}
	go func() {
		for {
			select {
			case <-exited:
				return
			case s := <-signals:
				stop("received " + s.String())
			case <-timeout:
				mu.Lock()
				timedOut = true
				mu.Unlock()
				stop("limits.timeout " + l.Timeout.String() + " reached")
				timeout = nil
			}
		}
	}()

	var werr error
	select {
	case werr = <-waitDone:
	case <-parseFailed:
		stop("native Codex protocol refused")
		werr = <-waitDone
	}
	close(exited)
	mu.Lock()
	if killTimer != nil {
		killTimer.Stop()
	}
	mu.Unlock()
	// Drain what the CLI wrote; a leftover child holding the pipe is killed.
	select {
	case <-readDone:
	case <-time.After(drainWait):
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		select {
		case <-readDone:
		case <-time.After(drainWait):
			_ = pr.Close()
			<-readDone
		}
	}
	_ = pr.Close()
	code := exitCode(cmd, werr)
	if l.Provider == protocol.AgentCodex && (protocolRefused || !codex.thread || !codex.terminal) {
		res.Seen, res.IsError, res.Subtype = true, true, "native_protocol_refused"
		if code == 0 {
			code = 1
		}
	}
	if l.Provider == protocol.AgentCodex && codex.failed && code == 0 {
		code = 1
	}
	mu.Lock()
	to := timedOut
	mu.Unlock()
	writeResult(stateDir, l, started, code, to, res)
	_ = os.Remove(filepath.Join(stateDir, pidFile))
	_, _ = fmt.Fprintf(out, "TASK-EXIT:%d\n", code)
	return code
}

// runTUI runs the agent CLI interactively on the pane's terminal (D-58): its
// stdin, stdout and stderr are the pane's, and its process group becomes the
// terminal's foreground group, so it reads the keyboard of whoever attaches.
// Like a task, it records the CLI's pid for the pod's SIGTERM and the rescue's
// stop, and forwards the signals run-agent gets; when the CLI exits it records
// the exit in tui-exit.json. Nothing is parsed or logged from a TUI, whose
// screen is not a log.
func runTUI(l Launch, stateDir string, tty *os.File, pane io.Writer, signals <-chan os.Signal, stopGrace time.Duration) int {
	note := func(format string, a ...any) {
		line := "[agentd] " + fmt.Sprintf(format, a...) + "\n"
		_, _ = io.WriteString(pane, line)
		if f, err := os.OpenFile(l.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			_, _ = io.WriteString(f, time.Now().UTC().Format(time.RFC3339)+" "+line)
			_ = f.Close()
		}
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = agentEnv(os.Environ(), l)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, pane, pane
	// Its own process group, so a stop reaches the CLI's children too, made
	// the terminal's foreground group when there is a terminal: a background
	// group that reads the terminal is stopped (SIGTTIN).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if isTerminal(tty) {
		cmd.SysProcAttr.Foreground = true
		cmd.SysProcAttr.Ctty = 0
	}
	started := time.Now().UTC()
	what := "started"
	if l.Resume {
		what = "resumed"
	}
	note("%s: conversation %s %s in the TUI", l.Session, l.ConversationID, what)
	record := func(code int) {
		_ = writeJSONFile(filepath.Join(stateDir, tuiExitFile), tuiExit{
			ExitCode: code, StartedAt: started, FinishedAt: time.Now().UTC(), ConversationID: l.ConversationID, BootID: l.BootID,
		})
		_ = os.Remove(filepath.Join(stateDir, pidFile))
	}
	if err := validateWorkspaceLaunch(l); err != nil {
		note("shared writer proof changed before spawn")
		record(1)
		return 1
	}
	if err := cmd.Start(); err != nil {
		note("start %s: %v", l.Argv[0], err)
		record(127)
		return 127
	}
	pid := cmd.Process.Pid
	_ = writeJSONFile(filepath.Join(stateDir, pidFile), agentPid{Pid: pid, Start: procStartTime(pid)})

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	exited := make(chan struct{})
	var mu sync.Mutex
	var killTimer *time.Timer
	go func() {
		for {
			select {
			case <-exited:
				return
			case sig := <-signals:
				mu.Lock()
				if killTimer == nil {
					note("received %s: SIGTERM to the agent (pid %d)", sig, pid)
					_ = syscall.Kill(pid, syscall.SIGTERM)
					killTimer = time.AfterFunc(stopGrace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
				}
				mu.Unlock()
			}
		}
	}()
	werr := <-waitDone
	close(exited)
	mu.Lock()
	if killTimer != nil {
		killTimer.Stop()
	}
	mu.Unlock()
	code := exitCode(cmd, werr)
	record(code)
	note("%s: the TUI exited (%d)", l.Session, code)
	return code
}

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}

// exitCode is the CLI's exit code, or 128+signal when a signal ended it.
func exitCode(cmd *exec.Cmd, err error) int {
	if cmd.ProcessState == nil {
		return 1
	}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return 1
	}
	return cmd.ProcessState.ExitCode()
}

func writeResult(stateDir string, l Launch, started time.Time, code int, timedOut bool, res streamResult) {
	tr := taskResult{
		TaskResult: protocol.TaskResult{
			ExitCode:   code,
			FinishedAt: time.Now().UTC(),
			TimedOut:   timedOut,
			Subtype:    res.Subtype,
			IsError:    res.IsError,
			NumTurns:   res.NumTurns,
		},
		StartedAt:      started,
		ConversationID: l.ConversationID,
		BootID:         l.BootID,
	}
	if res.Seen {
		u := res.Usage
		tr.Usage = &u
	}
	_ = writeJSONFile(filepath.Join(stateDir, resultFile), tr)
}

// agentEnv is the pane's environment minus Launch.Unset, plus Launch.Env.
func agentEnv(base []string, l Launch) []string {
	out := make([]string, 0, len(base)+len(l.Env))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if slices.Contains(l.Unset, k) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, l.Env...)
}

// procStartTime is field 22 of /proc/<pid>/stat, the process's start time in
// clock ticks since boot; "" when the process is gone.
func procStartTime(pid int) string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	// The command name (field 2) is in parentheses and may hold spaces.
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return ""
	}
	fields := strings.Fields(string(data)[i+1:])
	// fields[0] is field 3 (state); field 22 is fields[19].
	if len(fields) < 20 || fields[0] == "Z" {
		return ""
	}
	return fields[19]
}

// pidAlive reports whether the recorded agent process is still the same live
// process.
func pidAlive(p agentPid) bool {
	return p.Pid > 0 && p.Start != "" && procStartTime(p.Pid) == p.Start
}

// lockedWriter serialises writes from the stdout renderer and the stderr copy.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lockedWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.w.Write(p)
}
