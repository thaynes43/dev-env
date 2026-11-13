package agentd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

// TmuxSession is the tmux session the agent runs in (DESIGN-001 3.6). Tom
// attaches with `kubectl exec -it <pod> -- tmux attach -t agent`.
const TmuxSession = "agent"

// State files in ~/.agentd. They live on the session volume, so a resume sees
// what the last pod did.
const (
	bootFile = "boot.json"
	// launchFile is the session's first launch. Interactive/shared Sessions
	// may resume it; private tasks retain it without automatic resume.
	launchFile = "launch.json"
	// resumeFile is the launch of a later boot's TUI, which resumes the
	// conversation of launchFile (D-58).
	resumeFile = "resume.json"
	resultFile = "task-result.json"
	// tuiExitFile records how a TUI run ended (D-58).
	tuiExitFile = "tui-exit.json"
	pidFile     = "agent.pid"
	eventsFile  = "task-events.jsonl"
	// taskSourceFile is a private project task's source pin, written once
	// before its worktree is created (DESIGN-006).
	taskSourceFile = "task-source.json"
	// startRefusedFile records run-agent's refusal to spawn a summoned
	// session's provider past its start deadline (DESIGN-064 5.9, N2).
	startRefusedFile = "start-refused.json"
	// startHandoffFile records the tmux handoff of a summoned session's
	// first launch: written before tmux is asked, updated when it answers
	// (DESIGN-064 5.9, OP780 fix round 3).
	startHandoffFile = "start-handoff.json"
)

// Launch is everything `agentd run-agent` needs to run the agent: agentd
// writes it, and the tmux pane reads it, so no prompt is ever quoted through a
// shell (v1 passed it through `tmux send-keys`).
type Launch struct {
	UsageSource           *protocol.UsageSource     `json:"usageSource,omitempty"`
	CommonInputs          *CommonInstructionBinding `json:"commonInputs,omitempty"`
	NativeThreadConfirmed bool                      `json:"nativeThreadConfirmed,omitempty"`
	Provider              string                    `json:"provider,omitempty"`
	SessionUID            string                    `json:"sessionUID,omitempty"`
	PodUID                string                    `json:"podUID,omitempty"`
	CaptureOutput         bool                      `json:"captureOutput,omitempty"`
	CaptureModel          string                    `json:"captureModel,omitempty"`
	// NativeInvocationID identifies one platform-authored native invocation.
	// A continuation has its own ID and a consumed causal predecessor proof.
	NativeInvocationID string `json:"nativeInvocationID,omitempty"`
	ChildDecisions     bool   `json:"childDecisions,omitempty"`
	// CoordinatorInstructions records that this launch was built with the
	// pod's instruction capability. Registration runs only when this is set
	// and the pod setting is still on. Absence preserves a legacy launch.
	CoordinatorInstructions bool                `json:"coordinatorInstructions,omitempty"`
	Continuation            *nativeContinuation `json:"continuation,omitempty"`
	// WorkspaceOwner is the admitted writer generation. It is absent for
	// every existing private workspace launch.
	WorkspaceOwner *taskOwner `json:"workspaceOwner,omitempty"`
	Session        string     `json:"session"`
	// Argv is the agent CLI and its arguments. The prompt is not among them:
	// it goes to the CLI's stdin.
	Argv   []string `json:"argv"`
	Prompt string   `json:"prompt,omitempty"`
	Dir    string   `json:"dir"`
	// Env is added to the pane's environment; Unset is removed from it.
	Env   []string `json:"env,omitempty"`
	Unset []string `json:"unset,omitempty"`
	// LogPath is the readable task log; EventsPath the CLI's raw output: its
	// stream-json events, and any stderr lines among them.
	LogPath    string `json:"logPath"`
	EventsPath string `json:"eventsPath"`
	// Timeout is limits.timeout; 0 is none.
	Timeout        time.Duration `json:"timeout,omitempty"`
	ConversationID string        `json:"conversationId"`
	BootID         string        `json:"bootId"`
	CreatedAt      time.Time     `json:"createdAt"`
	// TUI runs the CLI interactively on the pane's terminal: a local session,
	// or any session resumed after its first boot (D-58). A task (-p) runs
	// with the prompt on stdin and its output parsed instead.
	TUI bool `json:"tui,omitempty"`
	// Resume is set on a later boot's launch: it resumes ConversationID.
	Resume bool `json:"resume,omitempty"`
	// ExplicitClaudeRC is set only by an accepted explicit user continuation
	// of one retained private Claude task. Automatic resume never sets it.
	ExplicitClaudeRC *ExplicitClaudeRCLaunch `json:"explicitClaudeRC,omitempty"`
	// StartDeadline is the pod's start deadline (AGENTD_START_DEADLINE, in
	// protocol.ParseStartDeadline's form), copied by BuildLaunch only into a
	// summoned session's first launch (DESIGN-064 5.9, N2, OP780). run-agent
	// checks it again immediately before it spawns the provider and, past
	// it, starts nothing. Absent, as in every ordinary launch and every
	// record an older agentd wrote, run-agent spawns as before.
	StartDeadline string `json:"startDeadline,omitempty"`
}

// ErrNotInPlan01 marks a session kind that a later plan builds.
var ErrNotInPlan01 = errors.New("not built yet")

// unsetForAgent are removed from the agent's environment.
//   - ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN: no session may fall back to
//     metered billing (V-05).
//   - CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC, DISABLE_GROWTHBOOK,
//     DISABLE_TELEMETRY, DO_NOT_TRACK: the first two turn Remote Control off,
//     the last two switch the CLI to a check that wants a refresh token
//     (DESIGN-001 6.2). No pod should set them; agentd makes sure.
//   - GH_TOKEN: a minted token lasts 60 minutes, so a copy taken when the CLI
//     starts dies during a long task and shadows gh's other auth. gh gets a
//     fresh one from agentd's wrapper instead, and git from its credential
//     helper; both read /creds/gh_token at each call (D-42).
//   - AGENTD_SESSION: the agent does not need the session document.
//   - CURSOR_API_KEY: only a cursor pod's agent holds the Cursor key
//     (DESIGN-058); a profile's envFrom Secret must not hand it to Claude or
//     Codex. cursorUnset keeps it for the Cursor CLI.
var unsetForAgent = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_GROWTHBOOK", "DISABLE_TELEMETRY", "DO_NOT_TRACK",
	"GH_TOKEN",
	protocol.SessionEnv,
	cursorAPIKeyEnv,
}

// unsetForRemote is unsetForAgent plus the static plan token: a remote
// session's CLI runs on the pod's own Claude login, and a token in its
// environment would bypass it and cannot register Remote Control (backlog 03,
// DESIGN-001 6.2). The operator already leaves the token out of a remote pod;
// this covers a profile envFrom Secret that still carries it.
//
// It also drops CLAUDE_CODE_OAUTH_REFRESH_TOKEN, which the CLI honours as a
// refresh credential (CLI 2.1.299): a pod must never hold one, in projection
// mode or on a pod-owned login.
var unsetForRemote = append(append([]string{}, unsetForAgent...), "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN")

// unsetFor is the agent's unset list for the session's mode.
func unsetFor(sess protocol.Session) []string {
	if sess.Mode == protocol.ModeRemote {
		return unsetForRemote
	}
	return unsetForAgent
}

// interactiveGuard is the guard for an interactive session, appended to the
// system prompt: v1's task guard without the task's ending.
func interactiveGuard(ws protocol.Workspace, workDir string) string {
	return fmt.Sprintf("You are working in an isolated git worktree (%s) on branch %s. "+
		"Stay inside it. NEVER push to main: commit to %s, push that branch, and "+
		"open a PR with 'gh pr create' when the work is ready. If the work needs extra "+
		"git worktrees, create them under %s and 'git worktree remove' each once its PR "+
		"merges; never leave stranded worktrees behind.", ws.Worktree, ws.Branch, ws.Branch, workDir)
}

// taskGuard is v1's guard for a task, appended to the system prompt.
func taskGuard(ws protocol.Workspace, workDir string) string {
	return fmt.Sprintf("You are working in an isolated git worktree (%s) on branch %s. "+
		"Stay inside it. NEVER push to main: commit to %s, push that branch, and "+
		"open a PR with 'gh pr create' when the task is complete. If the task needs extra "+
		"git worktrees, create them under %s and 'git worktree remove' each once its PR "+
		"merges; never leave stranded worktrees behind. If blocked, write BLOCKED and the "+
		"reason as your final message.", ws.Worktree, ws.Branch, ws.Branch, workDir)
}

// sharedGuard restricts this writer to the worktree covered by its durable
// owner record. Other worktrees need a separate managed task and owner.
func sharedGuard(ws protocol.Workspace, task bool) string {
	guard := fmt.Sprintf("This task owns only the git worktree %s and branch %s in a workspace shared with other pods. "+
		"Make this task's edits, commits and pushes inside that worktree and on that branch. "+
		"Open a PR when the work is ready, read its review and merge it after the required checks pass. "+
		"Never push directly to main. Preserve every other task's files, branch and worktree. "+
		"The reference clones under /home/dev/repos and project roots under /home/dev/codex are managed shared paths; "+
		"do not edit those clones or create, remove, move, prune or repair git worktrees yourself. "+
		"Use the managed task workflow for another worktree and the managed Git operation for a shared-clone fetch or administrative change. "+
		"If that operation is unavailable, report the need and stop that dependent step. "+
		"Do not run shared-clone garbage collection, delete locks, change remotes or global repository configuration, "+
		"or claim another task because its pod, heartbeat or lock disappeared. "+
		"Use managed stop-and-preserve before suspending, transferring or cleaning up this task. "+
		"A successful PR merge does not authorize deleting this worktree or its ownership record.", ws.Worktree, ws.Branch)
	if task {
		guard += " If blocked, write BLOCKED and the concrete reason as your final message."
	}
	return guard
}

// BuildLaunch builds a first Claude launch on its admitted static token, an
// explicitly enabled project task on native Codex with keeper access material,
// or a cursor project task on the pod's Cursor key (DESIGN-058). Managed
// Codex never admits interactive first launches or enrollment reuse. Each
// agent is named: an agent this agentd does not know is refused, never run
// as Claude.
func BuildLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	l, err := buildFirstLaunch(s, sess, ws, bootID, now)
	if err != nil {
		return Launch{}, err
	}
	// A summoned session's first launch carries the start deadline to
	// run-agent's pre-spawn fence (DESIGN-064 5.9, N2). Only a summoned
	// session gets it: a scheduled session keeps today's launch, whose
	// deadline bounds the launch record alone (DESIGN-054).
	if sess.Caller != "" {
		l.StartDeadline = s.StartDeadline
	}
	return l, nil
}

func buildFirstLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	switch sess.Agent {
	case protocol.AgentCodex:
		return buildCodexLaunch(s, sess, ws, nil, bootID, now)
	case protocol.AgentCursor:
		return buildCursorLaunch(s, sess, ws, bootID, now)
	case protocol.AgentClaude:
	default:
		return Launch{}, fmt.Errorf("%w: agent %q has no launch in this agentd", ErrNotInPlan01, sess.Agent)
	}
	if err := claudeCredentialCheck(s, sess); err != nil {
		return Launch{}, err
	}
	conv, err := newUUID()
	if err != nil {
		return Launch{}, err
	}
	argv := claudeArgs(s, sess)
	argv = append(argv, "--session-id", conv)
	l := Launch{
		Session:        sess.Name,
		Dir:            ws.Worktree,
		Env:            []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"},
		Unset:          unsetFor(sess),
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		ConversationID: conv,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
	}
	// A first interactive conversation is bound only when both immutable
	// identities were supplied by the operator and downward API. Legacy
	// launches lacking either value keep their existing behavior.
	if (sess.Mode == protocol.ModeLocal || sess.Mode == protocol.ModeRemote) && apiv1.ValidContextUID(sess.SessionUID) && apiv1.ValidContextUID(s.PodUID) {
		l.Provider, l.SessionUID, l.PodUID = protocol.AgentClaude, sess.SessionUID, s.PodUID
	}
	if sess.CaptureOutput {
		if sess.Validate() != nil || !apiv1.ValidContextUID(s.PodUID) {
			return Launch{}, errors.New("managed capture requires original task and pod")
		}
		l.CaptureOutput, l.CaptureModel, l.SessionUID, l.PodUID, l.Provider = true, sess.Model, sess.SessionUID, s.PodUID, protocol.AgentClaude
	}
	if sess.Mode == protocol.ModeLocal || sess.Mode == protocol.ModeRemote {
		l.TUI = true
		if sess.Mode == protocol.ModeRemote {
			argv = append(argv, remoteControlArgs(sess)...)
		}
		guard := interactiveGuard(ws, s.WorkDir())
		if sess.Workspace != nil {
			guard = sharedGuard(ws, false)
		}
		rules, err := providerProjectArgs(s, sess, guard)
		if err != nil {
			return Launch{}, err
		}
		l.Argv = append(argv, rules...)
		return l, nil
	}
	if n := sess.MaxTurns(); n > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(int(n)))
	}
	guard := taskGuard(ws, s.WorkDir())
	if sess.Workspace != nil {
		guard = sharedGuard(ws, true)
	}
	ordinaryInstructions := s.CoordinatorInstructions && sess.Workspace == nil &&
		protocol.ValidTaskBudgetIdentifier(sess.SessionUID) && protocol.ValidTaskBudgetIdentifier(s.PodUID)
	if ordinaryInstructions {
		guard += "\n\n" + ordinaryInstructionPullGuidance
	}
	rules, err := providerProjectArgs(s, sess, guard)
	if err != nil {
		return Launch{}, err
	}
	argv = append(argv, rules...)
	l.CommonInputs, err = commonLaunchBinding(s, sess)
	if err != nil {
		return Launch{}, err
	}
	l.Argv = append(argv,
		"--output-format", "stream-json", "--verbose",
		"-p")
	l.Prompt = sess.Prompt
	l.Timeout = sess.TimeoutDuration()
	if err := bindInitialUsage(&l, sess, s.PodUID); err != nil {
		return Launch{}, err
	}
	if ordinaryInstructions && l.UsageSource != nil && l.UsageSource.Valid() && !l.ChildDecisions {
		l.CoordinatorInstructions = true
	}
	return l, nil
}

// BuildResume builds an interactive/shared later launch (D-58). Retained
// private tasks cannot automatically resume, even when their result is lost
// (DESIGN-025 24). StartExplicitClaudeRC is the only explicit continuation,
// and only after a separate request proves the original owner has stopped.
// The model and effort remain fixed at create (D-39).
func BuildResume(s Settings, sess protocol.Session, ws protocol.Workspace, first Launch, bootID string, now time.Time) (Launch, error) {
	if retainedPrivateTask(s, sess) {
		return Launch{}, errors.New("a terminal private task cannot automatically resume its provider")
	}
	if sess.Caller != "" {
		// DESIGN-064 5.9 and N3: nothing continues a summoned session, so no
		// path (a boot, a decision answer) can start its provider twice.
		return Launch{}, errors.New("a summoned session is never resumed: its provider starts once")
	}
	switch sess.Agent {
	case protocol.AgentCodex:
		return buildCodexLaunch(s, sess, ws, &first, bootID, now)
	case protocol.AgentCursor:
		// DESIGN-058: a cursor task runs once. Nothing here resumes a Cursor
		// conversation, and the prompt is never run a second time (D-42).
		return Launch{}, errors.New("a cursor task does not resume: its prompt is not run again (D-42), and no Cursor resume is built")
	case protocol.AgentClaude:
	default:
		return Launch{}, fmt.Errorf("%w: agent %q has no resume in this agentd", ErrNotInPlan01, sess.Agent)
	}
	if err := claudeCredentialCheck(s, sess); err != nil {
		return Launch{}, err
	}
	if err := validateCommonResume(s, sess, first); err != nil {
		return Launch{}, err
	}
	if first.ConversationID == "" {
		return Launch{}, errors.New("the first launch records no conversation id, so there is nothing to resume")
	}
	guard := interactiveGuard(ws, s.WorkDir())
	if sess.Mode == protocol.ModeTask {
		guard = taskGuard(ws, s.WorkDir())
	}
	if sess.Workspace != nil {
		guard = sharedGuard(ws, sess.Mode == protocol.ModeTask)
	}
	rules, err := providerProjectArgs(s, sess, guard, true)
	if err != nil {
		return Launch{}, err
	}
	argv := append(claudeArgs(s, sess), "--resume", first.ConversationID)
	if sess.Mode == protocol.ModeRemote {
		// The same phone entry and history come back (S-6).
		argv = append(argv, remoteControlArgs(sess)...)
	}
	argv = append(argv, rules...)
	common, err := commonLaunchBinding(s, sess)
	if err != nil {
		return Launch{}, err
	}
	resume := Launch{
		CommonInputs: common, Session: sess.Name,
		Argv:           argv,
		Dir:            ws.Worktree,
		Env:            []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"},
		Unset:          unsetFor(sess),
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		ConversationID: first.ConversationID,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
		TUI:            true,
		Resume:         true,
	}
	// Do not retrobind legacy or replaced-session launch records. The resume
	// itself still runs as before; only its optional history binding is absent.
	if first.Provider == protocol.AgentClaude && first.Session == sess.Name && first.SessionUID == sess.SessionUID && first.PodUID != "" && first.Dir == ws.Worktree && apiv1.ValidContextUID(sess.SessionUID) && apiv1.ValidContextUID(s.PodUID) {
		resume.Provider, resume.SessionUID, resume.PodUID = protocol.AgentClaude, sess.SessionUID, s.PodUID
	}
	return resume, nil
}

// claudeArgs are the CLI's arguments every launch shares: the model, the
// effort and no approval prompts (D-23). Every Claude launch path (task,
// local, remote and so the standby, resume, rescue restore, the explicit RC
// continuation) builds its argv here, so --dangerously-skip-permissions is
// the always-allow launch contract (DESIGN-057 section 6a). Claude Code's
// critical-path check is not covered by it and stays on (Tom, 2026-11-09).
func claudeArgs(s Settings, sess protocol.Session) []string {
	argv := []string{s.ClaudeBin, "--model", sess.Model}
	if sess.Effort != "" {
		argv = append(argv, "--effort", sess.Effort)
	}
	return append(argv, "--dangerously-skip-permissions")
}

// remoteControlArgs register the CLI's Remote Control entry. With no role
// title the argument is the session name. With one, it is the composed
// display title (DESIGN-065). Codex does not use this. A composed value that
// does not start with a letter or a digit is not passed: the CLI's
// --remote-control takes an optional value, so a value that begins with a
// hyphen would be read as an option. The session name is passed instead.
func remoteControlArgs(sess protocol.Session) []string {
	name := sess.Name
	if title := remoteControlTitle(sess); title != "" {
		name = title
	}
	return []string{"--remote-control", name}
}

// remoteControlTitle is what a Claude remote session passes to
// --remote-control when it has role text. Empty means the session name is
// passed, and the rest of the argv stays as it is. Codex returns empty. A
// composed value that does not start with a letter or a digit is empty here
// too, so the status report and the argv agree on what was passed.
func remoteControlTitle(sess protocol.Session) string {
	if sess.Agent != protocol.AgentClaude || sess.Mode != protocol.ModeRemote {
		return ""
	}
	title := sessiontitle.Compose(sess.Model, sess.Title)
	if !sessiontitle.StartsWithLetterOrDigit(title) {
		return ""
	}
	return title
}

// claudeCredentialCheck checks what agentd runs: Claude task and local
// sessions on the plan's static token (V-05), and remote sessions on the pod's
// own login, which needs no token here (agentd checks the login itself, see
// remote.go, and strips the token from the agent's environment).
func claudeCredentialCheck(s Settings, sess protocol.Session) error {
	switch {
	case sess.Agent != protocol.AgentClaude:
		return fmt.Errorf("%w: %s sessions run from plan 04 (codex) or 09 (opencode)", ErrNotInPlan01, sess.Agent)
	case sess.Mode != protocol.ModeTask && sess.Mode != protocol.ModeLocal && sess.Mode != protocol.ModeRemote:
		return fmt.Errorf("mode %q is not task, local or remote", sess.Mode)
	}
	if err := protocol.ValidateClaudeModel(sess.Model); err != nil {
		return err
	}
	if sess.Mode == protocol.ModeRemote {
		return nil
	}
	// V-05: a session runs on the plan's static token or not at all.
	if strings.TrimSpace(s.Getenv("CLAUDE_CODE_OAUTH_TOKEN")) == "" {
		return errors.New("plan credential unavailable: CLAUDE_CODE_OAUTH_TOKEN is not set, and no session falls back to a metered key")
	}
	return nil
}

// StartAgent writes the launch file and starts the tmux session that runs
// `agentd run-agent` in the worktree. A first launch goes to launch.json, the
// record that the agent was started: agentd never runs a task's prompt a
// second time (D-42), and every later boot resumes from it (D-58). A resume
// goes to resume.json.
func StartAgent(ctx context.Context, r Runner, s Settings, l Launch, self string) error {
	return startLaunch(ctx, r, s, l, self, false)
}

// startHandoff is ~/.agentd/start-handoff.json: how far the tmux start of
// the summoned session's first launch it names got (DESIGN-064 5.9, story
// 780 fix round 3, coordinator rulings in PR 1100 comment 6840263445).
// agentd writes it durably, state handoffAsked, before it asks tmux anything,
// and calls no tmux when that write fails. When tmux answers, agentd updates
// it to handoffStarted, handoffNothingStarted or handoffAmbiguous. An update
// that fails before the new record is in place leaves handoffAsked; one that
// fails after (the directory sync) leaves tmux's answer in place, true but
// of unconfirmed durability. The launch record always stays, so no boot
// starts the session again (summonedLaunched). The status follows this
// record (summonedRunState). Reason names the error without tmux's output.
type startHandoff struct {
	State          string    `json:"state"`
	Reason         string    `json:"reason,omitempty"`
	At             time.Time `json:"at"`
	ConversationID string    `json:"conversationId"`
	BootID         string    `json:"bootId"`
}

// The states of a startHandoff.
const (
	// handoffAsked: tmux may be starting the launch, and its answer is not
	// recorded.
	handoffAsked = "asked"
	// handoffStarted: tmux started the session that runs run-agent.
	handoffStarted = "started"
	// handoffNothingStarted: nothing of this launch can have started. tmux
	// was never asked (its session already existed), or the tmux client
	// itself could not be run (tmuxNeverRan).
	handoffNothingStarted = "nothing-started"
	// handoffAmbiguous: tmux returned another error, so its server may have
	// started run-agent and so the provider.
	handoffAmbiguous = "ambiguous"
)

// tmuxNeverRan reports whether err proves the tmux client never ran, so it
// sent its server nothing: it could not be found (*exec.Error), or the
// fork/exec that would start it failed (story 780 fix round 3, R2-F1b). Any
// other error, another path error included (Run also waits and copies the
// client's output), leaves open that the server created the session and
// started its pane.
func tmuxNeverRan(err error) bool {
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return true
	}
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && pathErr.Op == "fork/exec"
}

// startLaunch is StartAgent. summoned is set only for a summoned session's
// first launch (spec.caller, DESIGN-064 5.9): its provider starts once, so a
// failed start never removes its launch record, and start-handoff.json,
// written before tmux is asked, says how far the start got. Every other
// launch writes no handoff record and keeps today's behaviour, the removal
// and so the retry on a later boot included.
func startLaunch(ctx context.Context, r Runner, s Settings, l Launch, self string, summoned bool) error {
	file := launchFile
	if l.Resume {
		file = resumeFile
	}
	if l.Provider == protocol.AgentCodex && !l.Resume {
		if _, err := os.Lstat(s.statePath(file)); err == nil {
			return errors.New("existing native Codex launch requires its confirmed resume identity")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the launch holds the prompt.
	var writeErr error
	if l.Provider == protocol.AgentCodex {
		writeErr = writeWorkspaceJSON(s.statePath(file), l)
	} else {
		writeErr = writeFileAtomic(s.statePath(file), data, 0o600)
	}
	if writeErr != nil {
		return writeErr
	}
	if summoned {
		// The evidence of the handoff exists before tmux is asked. Without
		// it, tmux is not asked: nothing starts, and the launch record stays.
		if err := writeStartHandoff(s, l, handoffAsked, ""); err != nil {
			return fmt.Errorf("the summoned launch's handoff record could not be written, so tmux was not asked to start it: %w", err)
		}
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}}); err == nil {
		exists := fmt.Errorf("tmux session %q already exists", TmuxSession)
		if summoned {
			// tmux was never asked to start this launch: nothing started.
			_ = writeStartHandoff(s, l, handoffNothingStarted, exists.Error())
		}
		return exists
	}
	_, err = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{
		"new-session", "-d", "-s", TmuxSession, "-x", "200", "-y", "50", "-c", l.Dir,
		self, "run-agent", "--launch", s.statePath(file),
	}})
	if err != nil {
		switch {
		case summoned:
			// DESIGN-064 5.9 (OP780 fix rounds 2 and 3): the tmux server may
			// have started run-agent despite the error, and a summoned
			// session's provider starts once. Its launch record stays, so no
			// later boot starts it again (summonedLaunched). A failed update
			// leaves handoffAsked, or tmux's answer if it was already in
			// place (startHandoff).
			state := handoffAmbiguous
			if tmuxNeverRan(err) {
				state = handoffNothingStarted
			}
			_ = writeStartHandoff(s, l, state, err.Error())
		case l.Provider != protocol.AgentCodex:
			// Legacy starts retain their existing retry behavior. A native
			// Codex start may have succeeded despite an ambiguous tmux
			// response, so its durable unconfirmed launch must survive and
			// prevent prompt replay.
			_ = os.Remove(s.statePath(file))
		}
		return fmt.Errorf("tmux: %s", cmdDetail(err))
	}
	if summoned {
		// A failed update leaves handoffAsked, or the answer if it was
		// already in place (startHandoff).
		_ = writeStartHandoff(s, l, handoffStarted, "")
	}
	return nil
}

// writeStartHandoff durably writes start-handoff.json for l: the file and its
// directory are synced before it returns, and a non-regular file in its place
// is refused, not replaced.
func writeStartHandoff(s Settings, l Launch, state, reason string) error {
	return writeWorkspaceJSON(s.statePath(startHandoffFile), startHandoff{State: state, Reason: reason, At: time.Now().UTC(), ConversationID: l.ConversationID, BootID: l.BootID})
}

// newUUID is a random (version 4) UUID, the form `claude --session-id` wants.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// newBootID names one boot of agentd.
func newBootID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)
}

// readJSONFile decodes a state file.
func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// writeJSONFile writes a state file atomically, mode 0600.
func writeJSONFile(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(data, '\n'), 0o600)
}

// summonedLaunched reports whether a summoned session (DESIGN-064, spec.caller
// set) already has its first launch: a saved launch record, or a task result,
// whatever the mode. Such a session starts nothing again on any boot
// (DESIGN-064 5.9, OP780): no TUI resume and no provider. As for a retained
// private task, a record agentd cannot stat counts as present.
func summonedLaunched(s Settings, sess protocol.Session) bool {
	if sess.Caller == "" {
		return false
	}
	for _, name := range []string{launchFile, resultFile} {
		if _, err := os.Lstat(s.statePath(name)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func retainedPrivateTask(s Settings, sess protocol.Session) bool {
	if sess.Mode != protocol.ModeTask || sess.Workspace != nil || !sess.PrivateHistoryRetained {
		return false
	}
	// A saved launch is enough to prohibit automatic TUI resume, including
	// an interrupted task with no result. Constructing an unsaved launch in
	// isolation is not proof that an original invocation ever ran.
	for _, name := range []string{launchFile, resultFile} {
		if _, err := os.Lstat(s.statePath(name)); !errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}
