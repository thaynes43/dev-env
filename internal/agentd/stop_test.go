package agentd

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// startAgentLike starts sh -c script as run-agent starts the CLI, the leader of
// its own process group, and records it in agent.pid. The process is reaped
// when it exits, so it never lingers as a zombie.
func startAgentLike(t *testing.T, s Settings, script string) int {
	t.Helper()
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
	})
	// Wait until sh has set its traps and started its children.
	time.Sleep(200 * time.Millisecond)
	if err := writeJSONFile(s.statePath(pidFile), agentPid{Pid: pid, Start: procStartTime(pid)}); err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestStopAgentWithNoAgent(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if st := stopAgent(s, time.Second); st.WasRunning || st.Killed || st.Running {
		t.Errorf("stop %+v", st)
	}
}

func TestStopAgentSIGTERM(t *testing.T) {
	s := testSettings(t, t.TempDir())
	pid := startAgentLike(t, s, "exec sleep 30")
	st := stopAgent(s, 5*time.Second)
	if !st.WasRunning || st.Killed || st.Running {
		t.Errorf("stop %+v", st)
	}
	if len(groupMembers(pid)) != 0 {
		t.Error("the agent's group still runs")
	}
}

// An agent that ignores SIGTERM is killed after the grace, and so is a
// background child it left in its group after it exited.
func TestStopAgentSIGKILLAfterTheGrace(t *testing.T) {
	for name, script := range map[string]string{
		"ignores TERM":   `trap "" TERM; sleep 30 & wait`,
		"leaves a child": `sh -c 'trap "" TERM; sleep 30' & wait`,
	} {
		t.Run(name, func(t *testing.T) {
			s := testSettings(t, t.TempDir())
			pid := startAgentLike(t, s, script)
			if len(groupMembers(pid)) < 2 {
				t.Fatalf("group %d has %v", pid, groupMembers(pid))
			}
			st := stopAgent(s, 300*time.Millisecond)
			if !st.WasRunning || !st.Killed || st.Running {
				t.Errorf("stop %+v", st)
			}
			if m := groupMembers(pid); len(m) != 0 {
				t.Errorf("left running: %v", m)
			}
		})
	}
}
