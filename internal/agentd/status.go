package agentd

import (
	"context"
	"os"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/version"
)

// bootRecord is ~/.agentd/boot.json: what the latest boot did.
type bootRecord struct {
	BootID     string              `json:"bootId"`
	BootedAt   time.Time           `json:"bootedAt"`
	Boot       string              `json:"boot"`
	Steps      []Step              `json:"steps,omitempty"`
	Workspace  *protocol.Workspace `json:"workspace,omitempty"`
	AgentError string              `json:"agentError,omitempty"`
}

// startWindow is how long after the launch file is written the agent counts
// as busy before its pid file appears.
const startWindow = time.Minute

// CollectStatus builds the session's status from agentd's state files and the
// worktree. The heartbeat and `agentd ctl status` both use it, so the
// operator sees the same thing either way. It reads; it never changes state.
func CollectStatus(ctx context.Context, r Runner, s Settings, session string, now time.Time) protocol.Status {
	st := protocol.Status{
		Session:    session,
		Agentd:     version.Get().String("agentd"),
		Boot:       protocol.BootBooting,
		Agent:      protocol.AgentState{State: protocol.AgentPending},
		ObservedAt: now.UTC(),
	}
	var boot bootRecord
	if readJSONFile(s.statePath(bootFile), &boot) == nil {
		st.BootID, st.BootedAt, st.Boot = boot.BootID, boot.BootedAt, boot.Boot
		for _, step := range boot.Steps {
			if step.State == StepWarn || step.State == StepFail {
				st.Problems = append(st.Problems, step)
			}
		}
		if boot.Workspace != nil {
			ws := *boot.Workspace
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if b, err := s.git(ctx, r, ws.Worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
				ws.Branch = b
			}
			if h, err := s.git(ctx, r, ws.Worktree, "rev-parse", "HEAD"); err == nil {
				ws.Head = h
			}
			cancel()
			st.Workspace = &ws
		}
	}

	var l Launch
	launched := readJSONFile(s.statePath(launchFile), &l) == nil
	var res taskResult
	finished := readJSONFile(s.statePath(resultFile), &res) == nil
	switch {
	case finished:
		task := res.TaskResult
		started := res.StartedAt
		st.Agent = protocol.AgentState{State: protocol.AgentExited, ConversationID: res.ConversationID, StartedAt: &started, Task: &task}
		st.Usage = res.Usage
	case boot.AgentError != "":
		st.Agent = protocol.AgentState{State: protocol.AgentFailed, Error: boot.AgentError}
	case launched:
		created := l.CreatedAt
		st.Agent = protocol.AgentState{ConversationID: l.ConversationID, StartedAt: &created}
		var p agentPid
		switch {
		case readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p):
			st.Agent.State = protocol.AgentBusy
		case l.BootID == boot.BootID && now.Sub(l.CreatedAt) < startWindow:
			st.Agent.State = protocol.AgentBusy
		default:
			st.Agent.State = protocol.AgentInterrupted
			st.Agent.Error = "the agent's process is gone and it left no result; agentd does not start a task twice (D-42)"
		}
	}
	if launched {
		if t := newestMtime(l.LogPath, l.EventsPath); !t.IsZero() {
			st.Agent.LastActivity = &t
		}
	}
	return st
}

// newestMtime is the newest modification time of the paths that exist.
func newestMtime(paths ...string) time.Time {
	var t time.Time
	for _, p := range paths {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(t) {
			t = fi.ModTime().UTC()
		}
	}
	return t
}
