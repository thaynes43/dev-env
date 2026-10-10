package agentd

import (
	"context"
	"fmt"
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
	if s.ManagedChildDecisions {
		if result, err := ReadDecision(ctx, s); err == nil && result.Decision != nil {
			record := result.Decision
			st.Decision = &protocol.DecisionOutcome{ID: record.ID, SessionUID: record.SessionUID, PodUID: record.PodUID, WriterGeneration: record.WriterGeneration, ThreadID: record.ThreadID, State: record.State, AnswerDigest: protocol.AnswerAuthority(*record, record.Answer).Digest, At: record.CreatedAt}
		}
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

	// The first launch is the session's conversation; this boot's agent is a
	// resume of it after the first boot (D-58).
	var first Launch
	launched := readJSONFile(s.statePath(launchFile), &first) == nil
	var res taskResult
	finished := readJSONFile(s.statePath(resultFile), &res) == nil
	cur, hasCur := first, launched && first.BootID == boot.BootID
	var resumed Launch
	if readJSONFile(s.statePath(resumeFile), &resumed) == nil && resumed.BootID == boot.BootID && boot.BootID != "" {
		cur, hasCur = resumed, true
	}
	switch {
	case boot.AgentError != "":
		st.Agent = protocol.AgentState{State: protocol.AgentFailed, Error: boot.AgentError}
	case hasCur && workspaceLaunchPending(s, cur, now):
		created := cur.CreatedAt
		st.Agent = protocol.AgentState{State: protocol.AgentPending, StartedAt: &created}
	case hasCur && cur.TUI:
		st.Agent = tuiState(s, cur, now)
	case finished:
		started := res.StartedAt
		st.Agent = protocol.AgentState{State: protocol.AgentExited, StartedAt: &started}
	case launched:
		created := first.CreatedAt
		st.Agent = protocol.AgentState{StartedAt: &created}
		var p agentPid
		switch {
		case readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p):
			st.Agent.State = protocol.AgentBusy
		case first.BootID == boot.BootID && now.Sub(first.CreatedAt) < startWindow:
			st.Agent.State = protocol.AgentBusy
		default:
			st.Agent.State = protocol.AgentInterrupted
			st.Agent.Error = "the agent's process is gone and it left no result; agentd does not run a task's prompt twice (D-42), and the next boot resumes it (D-58)"
		}
	}
	if launched {
		st.Agent.ConversationID = first.ConversationID
	}
	// How the task ended stays in the status after a resume.
	if finished {
		task := res.TaskResult
		st.Agent.Task = &task
		st.Usage = res.Usage
	}
	if launched {
		if t := newestMtime(first.LogPath, first.EventsPath); !t.IsZero() {
			st.Agent.LastActivity = &t
		}
	}
	pid := 0
	var p agentPid
	if readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p) {
		pid = p.Pid
	}
	applyActivity(ctx, r, s, &st, pid, hasCur && cur.TUI, now)
	return st
}

// A shared run-agent can queue for the common Git lock before its final
// admission. Report pending only for the exact current unlaunched receipt,
// and bound the observation so an exited pane does not stay pending forever.
// This status is never stop proof or authority to take over its writer.
func workspaceLaunchPending(s Settings, l Launch, now time.Time) bool {
	if s.WorkspaceID == "" || s.Getenv == nil || l.WorkspaceOwner == nil || now.Before(l.CreatedAt) || now.Sub(l.CreatedAt) >= sharedGitAdminBudget+startWindow {
		return false
	}
	sess, err := LoadSession(s.Getenv)
	if err != nil || l.Session != sess.Name || l.Dir != s.WorktreePath(sess.Name) || workspacePreflight(s, sess) != nil {
		return false
	}
	var owner taskOwner
	return readWorkspaceJSON(s.ownerPath(sess.Name), &owner) == nil && ownerMatches(s, sess, owner, true) == nil &&
		owner.State == "owned" && !owner.Launched && sameOwner(owner, *l.WorkspaceOwner)
}

// tuiState is the state of this boot's TUI (D-58): busy while its process
// runs (plan 02's idle detection refines that), exited once it recorded its
// exit, and interrupted when it is gone without one.
func tuiState(s Settings, l Launch, now time.Time) protocol.AgentState {
	created := l.CreatedAt
	st := protocol.AgentState{StartedAt: &created}
	var p agentPid
	var exit tuiExit
	switch {
	case readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p):
		st.State = protocol.AgentBusy
	case readJSONFile(s.statePath(tuiExitFile), &exit) == nil && exit.BootID == l.BootID && !exit.StartedAt.Before(l.CreatedAt.Truncate(time.Second)):
		st.State = protocol.AgentExited
		if exit.ExitCode != 0 {
			st.Error = fmt.Sprintf("the TUI exited with code %d at %s", exit.ExitCode, exit.FinishedAt.Format(time.RFC3339))
		}
	case now.Sub(l.CreatedAt) < startWindow:
		st.State = protocol.AgentBusy
	default:
		st.State = protocol.AgentInterrupted
		st.Error = "the TUI's process is gone and it recorded no exit"
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
