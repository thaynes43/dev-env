package agentd

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// stopKillWait is how long a stop waits for SIGKILL to take effect.
var stopKillWait = 5 * time.Second

// stopAgent stops the agent CLI before a final rescue (D-48), so nothing
// writes to the worktree between the rescue and the end of the pod. It sends
// SIGTERM to the CLI that run-agent recorded, as the pod's own shutdown does
// (D-42), and waits up to grace for it to exit. Then whatever is left of the
// CLI's process group (run-agent starts the CLI as its group's leader), the
// CLI itself or a background command it started, gets SIGKILL. run-agent is
// not in that group, so it still writes the task's result.
func stopAgent(s Settings, grace time.Duration) *protocol.AgentStop {
	st := &protocol.AgentStop{}
	var p agentPid
	if readJSONFile(s.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return st
	}
	st.WasRunning = true
	_ = syscall.Kill(p.Pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for pidAlive(p) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if pidAlive(p) || len(groupMembers(p.Pid)) > 0 {
		st.Killed = true
		// A process group's id is never reused while a member lives, so
		// this reaches the CLI's group only.
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
		deadline = time.Now().Add(stopKillWait)
		for (pidAlive(p) || len(groupMembers(p.Pid)) > 0) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	st.Running = pidAlive(p) || len(groupMembers(p.Pid)) > 0
	return st
}

// groupMembers lists the live processes in process group pgid, zombies left
// out, from /proc.
func groupMembers(pgid int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name (field 2) is in parentheses and may hold spaces;
		// after it come field 3 (state) and field 5 (the process group).
		i := strings.LastIndexByte(string(data), ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(string(data)[i+1:])
		if len(f) < 3 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if g, err := strconv.Atoi(f[2]); err == nil && g == pgid {
			out = append(out, pid)
		}
	}
	return out
}
