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
	BootID   string    `json:"bootId"`
	BootedAt time.Time `json:"bootedAt"`
	Boot     string    `json:"boot"`
	// CurrentStep is local boot progress, carried in the existing pending
	// agent's message so older operators need no status schema change.
	CurrentStep string              `json:"currentStep,omitempty"`
	Steps       []Step              `json:"steps,omitempty"`
	Workspace   *protocol.Workspace `json:"workspace,omitempty"`
	AgentError  string              `json:"agentError,omitempty"`
	// Daemon binds pod-local control to the binary actually supervising this boot.
	// Older daemons omit it; a side binary must not start a continuation for them.
	Daemon *daemonProcess `json:"daemon,omitempty"`
	// ProjectBootstrap is this boot's private catalog bootstrap, as names
	// and codes only (DESIGN-006, reason codes).
	ProjectBootstrap *protocol.ProjectBootstrap `json:"projectBootstrap,omitempty"`
}

type daemonProcess struct {
	SessionUID string `json:"sessionUID"`
	PodUID     string `json:"podUID"`
	Pid        int    `json:"pid"`
	Start      string `json:"start"`
}

// startWindow is how long after the launch file is written the agent counts
// as busy before its pid file appears.
const startWindow = time.Minute

// summonedInterrupted is a summoned session's interrupted message: its first
// run is gone without a result, and nothing starts it again (DESIGN-064 5.9).
const summonedInterrupted = "the summoned session's process is gone without a result; agentd never starts it again (DESIGN-064 5.9)"

// CollectStatus builds the session's status from agentd's state files and the
// worktree. The heartbeat and `agentd ctl status` both use it, so the
// operator sees the same thing either way. It reads; it never changes state.
func CollectStatus(ctx context.Context, r Runner, s Settings, sess protocol.Session, now time.Time) protocol.Status {
	st := protocol.Status{
		Session:    sess.Name,
		Model:      sess.Model,
		Effort:     sess.Effort,
		Agentd:     version.Get().String("agentd"),
		Boot:       protocol.BootBooting,
		Agent:      protocol.AgentState{State: protocol.AgentPending},
		ObservedAt: now.UTC(),
	}
	var boot bootRecord
	if readJSONFile(s.statePath(bootFile), &boot) == nil {
		st.BootID, st.BootedAt, st.Boot = boot.BootID, boot.BootedAt, boot.Boot
		st.ProjectBootstrap = boot.ProjectBootstrap
		for _, step := range boot.Steps {
			if step.State == StepWarn || step.State == StepFail {
				st.Problems = append(st.Problems, step)
			}
		}
	}
	// The first launch and task result survive restarts (D-58). Boot heartbeats
	// must retain this history: the operator replaces status.agent wholesale,
	// and private history reads its conversation ID even during boot.
	var first Launch
	launched := readJSONFile(s.statePath(launchFile), &first) == nil
	var res taskResult
	finished := readJSONFile(s.statePath(resultFile), &res) == nil
	if finished && launched && sess.Mode == protocol.ModeTask && res.TaskOutcome != nil && res.TaskOutcome.Validate() == nil && res.TaskOutcome.SessionUID == sess.SessionUID && res.TaskOutcome.PodUID == s.PodUID {
		st.TaskOutcome = res.TaskOutcome
	}
	// Before the agent step completes, missing credentials and previous process
	// state say nothing about this boot's agent. The keeper's Claude projection
	// is applied by startAgent, not by rendering.
	if st.Boot == protocol.BootBooting {
		if launched {
			st.Agent.ConversationID = first.ConversationID
		}
		if finished {
			st.Agent.Task = &res.TaskResult
		}
		st.Agent.Error = boot.CurrentStep
		if sess.Mode == protocol.ModeRemote {
			// Nothing has been passed yet: the title stays empty until the CLI is live.
			st.RemoteControl = &protocol.RemoteControl{Name: sess.Name, State: protocol.RemoteStarting}
		}
		return st
	}
	if s.ManagedChildDecisions {
		if result, err := ReadDecision(ctx, s); err == nil && result.Decision != nil {
			record := result.Decision
			st.Decision = &protocol.DecisionOutcome{ID: record.ID, SessionUID: record.SessionUID, PodUID: record.PodUID, WriterGeneration: record.WriterGeneration, ThreadID: record.ThreadID, State: record.State, AnswerDigest: protocol.AnswerAuthority(*record, record.Answer).Digest, At: record.CreatedAt}
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
		// The durable record names the task's source, not a boot's copy.
		// An unreadable record, or one that binds another session or
		// source, is a problem, not a task without one.
		source, err := sessionTaskSource(s, sess)
		ws.Source = source
		if err != nil {
			st.Problems = append(st.Problems, Step{Name: "task-source", State: StepWarn,
				Notes: []string{"the task source record is not reported; preserved: " + err.Error()}})
		}
		st.Workspace = &ws
	}

	// The first launch is the session's conversation; this boot's agent is a
	// resume of it after the first boot (D-58).
	cur, hasCur := first, launched && first.BootID == boot.BootID
	var resumed Launch
	if readJSONFile(s.statePath(resumeFile), &resumed) == nil && resumed.BootID == boot.BootID && boot.BootID != "" {
		cur, hasCur = resumed, true
	}
	// An uncertain explicit start is not hidden by the original task result.
	// Reading this record never starts or retries a provider invocation.
	explicitLaunch, explicitState, hasExplicit := explicitClaudeRCStatus(ctx, r, s, sess, now)
	if hasExplicit && explicitState != explicitClaudeRCUncertain {
		cur, hasCur = explicitLaunch, true
	}
	// A summoned session's first run is its only one (DESIGN-064 5.9,
	// OP780): after a later boot, which starts nothing, the status says how
	// that run ended, never that a login is wanted for a start that will not
	// happen.
	summonedRun := sess.Caller != "" && launched
	var refused startRefused
	// A summoned launch's state follows its tmux handoff record (story 780
	// fix round 3), never this boot's agent error or a later boot's start
	// window. Only a launch tmux started reads the branches below.
	var summoned protocol.AgentState
	summonedSettled := false
	if summonedRun {
		summoned, summonedSettled = summonedRunState(s, first, boot.BootID, now)
	}
	switch {
	case summonedSettled:
		st.Agent = summoned
	case boot.AgentError != "" && !summonedRun:
		st.Agent = protocol.AgentState{State: protocol.AgentFailed, Error: boot.AgentError}
	case launched && readJSONFile(s.statePath(startRefusedFile), &refused) == nil && refused.ConversationID == first.ConversationID && refused.BootID == first.BootID:
		// run-agent's start fence refused the provider spawn (N2): the
		// launch failed and no provider ran.
		created := first.CreatedAt
		st.Agent = protocol.AgentState{State: protocol.AgentFailed, StartedAt: &created, Error: "run-agent refused to start the provider: " + refused.Reason}
	case codexRemoteSession(sess):
		// Story110 T17: the agent is the phone daemon, read from its own state.
		st.RemoteControl, st.Agent = collectCodexRemote(ctx, s, sess, boot.BootID, now)
	case sess.Mode == protocol.ModeRemote && !hasCur && !summonedRun && !checkLogin(s).Usable(now):
		// Story110 T03: a remote pod without a usable login has not started
		// its agent. The message names the way forward, never a secret.
		msg := "no usable Claude login in this pod; run: agent-run login " + sess.Name + " url"
		if s.ClaudeLiveFile != "" {
			msg = projectionLoginStatus(s, now)
		}
		st.Agent = protocol.AgentState{State: protocol.AgentLoginRequired, Error: msg}
	case hasCur && workspaceLaunchPending(s, cur, now):
		created := cur.CreatedAt
		st.Agent = protocol.AgentState{State: protocol.AgentPending, StartedAt: &created}
	case hasCur && cur.TUI:
		st.Agent = tuiState(s, cur, now)
	case finished:
		started := res.StartedAt
		st.Agent = protocol.AgentState{State: protocol.AgentExited, StartedAt: &started}
	case summonedRun && first.TUI:
		// A later boot of a summoned TUI session: its first run's own exit
		// record says whether it exited; otherwise it was interrupted. This
		// boot started nothing, so the start window does not count (story
		// 780 fix round 3).
		st.Agent = tuiStateAt(s, first, now, false)
		if st.Agent.State == protocol.AgentInterrupted {
			st.Agent.Error = summonedInterrupted
		}
	case launched:
		created := first.CreatedAt
		st.Agent = protocol.AgentState{StartedAt: &created}
		var p agentPid
		switch {
		case !preflightBootChanged(first, boot.BootID) && readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p):
			st.Agent.State = protocol.AgentBusy
		case first.BootID == boot.BootID && now.Sub(first.CreatedAt) < startWindow:
			st.Agent.State = protocol.AgentBusy
		case summonedRun && preflightLive(s, first):
			// A summoned task's model pre-flight (DESIGN-064 6.9, OP791): its
			// run-agent lives and no provider has a pid yet, so it counts as
			// busy, as the start window does before the pid file appears.
			st.Agent.State = protocol.AgentBusy
		default:
			st.Agent.State = protocol.AgentInterrupted
			st.Agent.Error = "the agent's process is gone and it left no result; agentd does not run a task's prompt twice (D-42), and the next boot resumes it (D-58)"
			if retainedPrivateTask(s, sess) {
				st.Agent.Error = "the private task's process is gone without a result; its launch and HOME remain retained, and agentd never automatically resumes it"
			} else if sess.Caller != "" {
				st.Agent.Error = summonedInterrupted
			}
		}
	}
	if hasExplicit {
		switch explicitState {
		case explicitClaudeRCStarting, explicitClaudeRCSupervised:
			started := cur.CreatedAt
			st.Agent = protocol.AgentState{State: protocol.AgentBusy, StartedAt: &started}
		case explicitClaudeRCExited:
			st.Agent = tuiState(s, cur, now)
		default:
			st.Agent = protocol.AgentState{State: protocol.AgentInterrupted, Error: "explicit continuation ownership or exit is uncertain; no replay"}
			hasCur = false
		}
	}
	if launched {
		st.Agent.ConversationID = first.ConversationID
	}
	// How the task ended stays in the status after a resume.
	if finished {
		task := res.TaskResult
		st.Agent.Task = &task
		// Unclassified raw usage stays in the private result. Reporting it to an
		// older operator can manufacture dollars when only tokens were observed.
		if launched {
			st.UsageObservation = collectedInitialUsage(first, s.StateDir, res.UsageObservation)
		}
	}
	// How a summoned task's start went after its model pre-flight
	// (DESIGN-064 6.9, OP791): the model it started on, or why nothing did.
	if summonedRun && sess.Mode == protocol.ModeTask {
		st.Start = preflightStartRecord(s, first)
	}
	if launched {
		if t := newestMtime(first.LogPath, first.EventsPath); !t.IsZero() {
			st.Agent.LastActivity = &t
		}
	}
	pid := 0
	var p, live agentPid
	if readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p) {
		pid, live = p.Pid, p
	}
	applyActivity(ctx, r, s, &st, pid, hasCur && cur.TUI, now)
	switch {
	case sess.Mode == protocol.ModeRemote && sess.Agent == protocol.AgentClaude:
		rc := collectRemoteControl(s, sess, checkLogin(s).Usable(now), live, hasCur)
		rc.Title = remoteTitleForReport(sess, hasCur, st.Agent.State)
		if !hasExplicit && sess.Caller == "" {
			overlayRecordedTitle(s, sess, rc)
		}
		st.RemoteControl = rc
	case hasCur && hasExplicit && explicitState != explicitClaudeRCUncertain && explicitRCAccepted(s, sess, cur):
		rc := collectRemoteControl(s, sess, checkLogin(s).Usable(now), live, true)
		rc.Name = cur.ExplicitClaudeRC.Title
		if rc.URL == "" {
			rc.SessionID = ""
			if rc.State == protocol.RemoteRegistered {
				rc.State = protocol.RemoteStarting
			}
		}
		st.RemoteControl = rc
	}
	// A projected Claude session whose login the provider refused: say so in the
	// existing message. The state word is unchanged.
	if sess.Agent == protocol.AgentClaude && st.Agent.Error == "" {
		switch st.Agent.State {
		case protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting:
			st.Agent.Error = rejectionHeartbeatText(s, now)
		}
	}
	return st
}

// summonedHandoff is the handoff record of the summoned session's first
// launch l (story 780 fix round 3). A record that is missing, unreadable or
// truncated, names another launch or holds an unknown state counts as
// handoffAsked: a launch record written before any handoff record (fix round
// 2's code, or a boot that ended between the two writes), or a handoff whose
// update was lost.
func summonedHandoff(s Settings, l Launch) startHandoff {
	var h startHandoff
	if readJSONFile(s.statePath(startHandoffFile), &h) != nil || h.ConversationID != l.ConversationID || h.BootID != l.BootID {
		return startHandoff{State: handoffAsked}
	}
	switch h.State {
	case handoffStarted, handoffNothingStarted, handoffAmbiguous:
		return h
	}
	return startHandoff{State: handoffAsked}
}

// summonedRunState is the state of a summoned session's first launch, from
// its handoff record (DESIGN-064 5.9, story 780 fix round 3, coordinator
// rulings in PR 1100 comment 6840263445). settled is false only when tmux
// started the launch: its state is then read as for any launch, without the
// boot's agent error. Otherwise:
//
//   - nothing of the launch can have started, or run-agent's fence refused
//     its spawn: failed;
//   - tmux was asked and its answer is ambiguous or not recorded: busy while
//     a process is live, and inside the start window of the launch's own
//     boot; interrupted after it, and at once on any later boot. Never
//     failed, since the provider may have started.
func summonedRunState(s Settings, first Launch, bootID string, now time.Time) (st protocol.AgentState, settled bool) {
	created := first.CreatedAt
	st = protocol.AgentState{StartedAt: &created}
	h := summonedHandoff(s, first)
	var refused startRefused
	switch {
	case preflightLive(s, first):
		st.State = protocol.AgentBusy
		return st, true
	case readJSONFile(s.statePath(startRefusedFile), &refused) == nil && refused.ConversationID == first.ConversationID && refused.BootID == first.BootID:
		st.State, st.Error = protocol.AgentFailed, "run-agent refused to start the provider: "+refused.Reason
		return st, true
	case preflightFailed(s, first):
		// The model pre-flight found no usable model, so no provider of the
		// task started (DESIGN-064 6.9, OP791). Whatever tmux answered,
		// run-agent ran and recorded that.
		rec, _ := readPreflightRecord(s, first)
		st.State, st.Error = protocol.AgentFailed, "no usable model: the model pre-flight ended "+rec.Failure+"; the provider was not started (DESIGN-064 6.9)"
		return st, true
	case h.State == handoffNothingStarted:
		st.State, st.Error = protocol.AgentFailed, "the tmux start of the summoned session's first launch failed before anything could start ("+h.Reason+"); agentd never starts it again (DESIGN-064 5.9)"
		return st, true
	case h.State == handoffStarted:
		return protocol.AgentState{}, false
	}
	var p agentPid
	switch {
	case !preflightBootChanged(first, bootID) && readJSONFile(s.statePath(pidFile), &p) == nil && pidAlive(p):
		st.State = protocol.AgentBusy
	case first.BootID == bootID && now.Sub(first.CreatedAt) < startWindow:
		st.State = protocol.AgentBusy
	case preflightLive(s, first):
		// run-agent is in the task's model pre-flight (OP791).
		st.State = protocol.AgentBusy
	default:
		why := "tmux's answer is not recorded"
		if h.State == handoffAmbiguous {
			why = "tmux returned an error (" + h.Reason + ")"
		}
		st.State = protocol.AgentInterrupted
		st.Error = "the summoned session's first launch was handed to tmux, " + why + ", so its provider may have started, and no process of it is live; agentd never starts it again (DESIGN-064 5.9)"
	}
	return st, true
}

// remoteTitleForReport is status.remoteControl.title. It stays empty until
// this boot's CLI process is live, and it keeps the value passed at launch
// after that process exits. Empty also means the launch passed the session name.
func remoteTitleForReport(sess protocol.Session, hasCur bool, state string) string {
	if !hasCur || !cliHasLaunched(state) {
		return ""
	}
	return remoteControlTitle(sess)
}

// cliHasLaunched is true once this boot's agent process is or has been live.
// Pending, login-required and failed have not passed a launch argument.
// Exited and interrupted have, so the record of what was passed stays.
func cliHasLaunched(state string) bool {
	switch state {
	case protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting, protocol.AgentExited, protocol.AgentInterrupted:
		return true
	default:
		return false
	}
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
	return tuiStateAt(s, l, now, true)
}

// tuiStateAt is tuiState; window false leaves out the start window, for a
// boot that did not start the TUI (story 780 fix round 3).
func tuiStateAt(s Settings, l Launch, now time.Time, window bool) protocol.AgentState {
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
	case window && now.Sub(l.CreatedAt) < startWindow:
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
