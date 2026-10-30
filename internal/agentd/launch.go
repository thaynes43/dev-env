package agentd

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// TmuxSession is the tmux session the agent runs in (DESIGN-001 3.6). Tom
// attaches with `kubectl exec -it <pod> -- tmux attach -t agent`.
const TmuxSession = "agent"

// State files in ~/.agentd. They live on the session volume, so a resume sees
// what the last pod did.
const (
	bootFile = "boot.json"
	// launchFile is the session's first launch: the record that its agent was
	// started, and the conversation every later boot resumes (D-42, D-58).
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
	NativeInvocationID string              `json:"nativeInvocationID,omitempty"`
	ChildDecisions     bool                `json:"childDecisions,omitempty"`
	Continuation       *nativeContinuation `json:"continuation,omitempty"`
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
var unsetForAgent = []string{
	"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "DISABLE_GROWTHBOOK", "DISABLE_TELEMETRY", "DO_NOT_TRACK",
	"GH_TOKEN",
	protocol.SessionEnv,
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

// BuildLaunch builds a first Claude launch on its admitted static token, or an
// explicitly enabled project task on native Codex with keeper access material.
// Managed Codex never admits interactive first launches or enrollment reuse.
func BuildLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	if sess.Agent == protocol.AgentCodex {
		return buildCodexLaunch(s, sess, ws, nil, bootID, now)
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
	return l, nil
}

// BuildResume builds a later boot's launch (D-58): the TUI, resuming the
// conversation the first launch started. A task's prompt never runs again
// (D-42); its conversation comes back as an interactive session instead, with
// the task's guard. The model and effort are the session's, which spec fixes
// at create (D-39).
func BuildResume(s Settings, sess protocol.Session, ws protocol.Workspace, first Launch, bootID string, now time.Time) (Launch, error) {
	if terminalPrivateTask(s, sess) {
		return Launch{}, errors.New("a terminal private task cannot automatically resume its provider")
	}
	if sess.Agent == protocol.AgentCodex {
		return buildCodexLaunch(s, sess, ws, &first, bootID, now)
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
// effort and no approval prompts (D-23).
func claudeArgs(s Settings, sess protocol.Session) []string {
	argv := []string{s.ClaudeBin, "--model", sess.Model}
	if sess.Effort != "" {
		argv = append(argv, "--effort", sess.Effort)
	}
	return append(argv, "--dangerously-skip-permissions")
}

// remoteControlArgs register the CLI's Remote Control entry under the
// session's name (backlog 03: the Remote Control name is the session id).
func remoteControlArgs(sess protocol.Session) []string {
	return []string{"--remote-control", sess.Name}
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
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}}); err == nil {
		return fmt.Errorf("tmux session %q already exists", TmuxSession)
	}
	_, err = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{
		"new-session", "-d", "-s", TmuxSession, "-x", "200", "-y", "50", "-c", l.Dir,
		self, "run-agent", "--launch", s.statePath(file),
	}})
	if err != nil {
		// Legacy starts retain their existing retry behavior. A native Codex
		// start may have succeeded despite an ambiguous tmux response, so its
		// durable unconfirmed launch must survive and prevent prompt replay.
		if l.Provider != protocol.AgentCodex {
			_ = os.Remove(s.statePath(file))
		}
		return fmt.Errorf("tmux: %s", cmdDetail(err))
	}
	return nil
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

func terminalPrivateTask(s Settings, sess protocol.Session) bool {
	if sess.Mode != protocol.ModeTask || sess.Workspace != nil {
		return false
	}
	_, err := os.Lstat(s.statePath(resultFile))
	// Unreadable or malformed retained state never permits another invocation.
	return !errors.Is(err, os.ErrNotExist)
}
