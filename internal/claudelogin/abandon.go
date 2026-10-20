package claudelogin

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Abandon is the deterministic cancel of a Claude login ceremony (NB-01b). The
// operator's cancel used to rely on closing its exec stream to hang the helper
// up, and live (LIVE-238a, D1) the hangup never arrived, so the helper and the
// provider's CLI stayed alive and the keeper's one attempt stayed held until its
// fifteen minute deadline. This is a separate, short exec of a fixed command in
// the same container: it finds the running `agentd claude-login` helpers by their
// command line and sends each SIGHUP, which the helper already treats as a cancel
// (it kills the CLI's process group, cancels the keeper's attempt and wipes its
// staging). It reads no credential, talks to no socket and prints only a fixed
// word.
//
// It is idempotent: no running helper is a success. It signals only processes
// whose argv is exactly `agentd claude-login` (the helper), never itself and never
// the idle entrypoint (`agentd claude-login-idle`) or the status command.

const (
	// AbandonDone: a helper was signalled and has ended.
	AbandonDone = "Abandoned"
	// AbandonNone: no helper was running.
	AbandonNone = "NoCeremony"
	// AbandonStuck: a helper was signalled and is still running after the wait.
	AbandonStuck = "Stuck"
	// AbandonWait bounds how long the command waits for the helper to leave.
	AbandonWait = 8 * time.Second
)

// AbandonOptions are the seams a test replaces. The zero value is production.
type AbandonOptions struct {
	ProcDir string              // "/proc" when empty
	Self    int                 // this process's pid; os.Getpid when zero
	Signal  func(pid int) error // SIGHUP when nil
	Wait    time.Duration       // AbandonWait when zero
	Poll    time.Duration       // 50ms when zero
	Sleep   func(time.Duration) // time.Sleep when nil
	Now     func() time.Time    // time.Now when nil
}

// Abandon signals every running `agentd claude-login` helper and waits for them
// to leave. It returns one of the fixed words above.
func Abandon(ctx context.Context, o AbandonOptions) string {
	if o.ProcDir == "" {
		o.ProcDir = "/proc"
	}
	if o.Self == 0 {
		o.Self = os.Getpid()
	}
	if o.Signal == nil {
		o.Signal = func(pid int) error { return syscall.Kill(pid, syscall.SIGHUP) }
	}
	if o.Wait <= 0 {
		o.Wait = AbandonWait
	}
	if o.Poll <= 0 {
		o.Poll = 50 * time.Millisecond
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	pids := helperPIDs(o.ProcDir, o.Self)
	if len(pids) == 0 {
		return AbandonNone
	}
	for _, pid := range pids {
		_ = o.Signal(pid)
	}
	deadline := o.Now().Add(o.Wait)
	for {
		alive := false
		for _, pid := range pids {
			if processAlive(o.ProcDir, pid) {
				alive = true
			}
		}
		if !alive {
			return AbandonDone
		}
		if !o.Now().Before(deadline) || ctx.Err() != nil {
			return AbandonStuck
		}
		o.Sleep(o.Poll)
	}
}

// helperPIDs lists the processes whose argv is exactly `agentd claude-login`.
func helperPIDs(procDir string, self int) []int {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self || pid <= 1 {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(procDir, e.Name(), "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		argv := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(argv) == 2 && filepath.Base(argv[0]) == "agentd" && argv[1] == "claude-login" {
			pids = append(pids, pid)
		}
	}
	return pids
}

// processAlive: the process exists and is not a zombie (a reaped-late child
// that has already exited).
func processAlive(procDir string, pid int) bool {
	raw, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	// "pid (comm) S ...": the state follows the last closing parenthesis.
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return false
	}
	return s[i+2] != 'Z' && s[i+2] != 'X'
}
