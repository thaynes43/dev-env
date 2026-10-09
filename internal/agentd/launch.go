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
)

// Launch is everything `agentd run-agent` needs to run the agent: agentd
// writes it, and the tmux pane reads it, so no prompt is ever quoted through a
// shell (v1 passed it through `tmux send-keys`).
type Launch struct {
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

// BuildLaunch builds a session's first launch: a Claude task (D-42) or a local
// session's TUI (D-58), both on the static token. Remote mode arrives with plan
// 03, Codex pods with plan 04 (D-12: Codex runs in the hub until the keeper
// owns its login), opencode with plan 09.
func BuildLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	if err := claudeOnStaticToken(s, sess); err != nil {
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
		Unset:          unsetForAgent,
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		ConversationID: conv,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
	}
	if sess.Mode == protocol.ModeLocal {
		l.TUI = true
		l.Argv = append(argv, "--append-system-prompt", interactiveGuard(ws, s.WorkDir()))
		return l, nil
	}
	if n := sess.MaxTurns(); n > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(int(n)))
	}
	l.Argv = append(argv,
		"--append-system-prompt", taskGuard(ws, s.WorkDir()),
		"--output-format", "stream-json", "--verbose",
		"-p")
	l.Prompt = sess.Prompt
	l.Timeout = sess.TimeoutDuration()
	return l, nil
}

// BuildResume builds a later boot's launch (D-58): the TUI, resuming the
// conversation the first launch started. A task's prompt never runs again
// (D-42); its conversation comes back as an interactive session instead, with
// the task's guard. The model and effort are the session's, which spec fixes
// at create (D-39).
func BuildResume(s Settings, sess protocol.Session, ws protocol.Workspace, first Launch, bootID string, now time.Time) (Launch, error) {
	if err := claudeOnStaticToken(s, sess); err != nil {
		return Launch{}, err
	}
	if first.ConversationID == "" {
		return Launch{}, errors.New("the first launch records no conversation id, so there is nothing to resume")
	}
	guard := interactiveGuard(ws, s.WorkDir())
	if sess.Mode == protocol.ModeTask {
		guard = taskGuard(ws, s.WorkDir())
	}
	argv := append(claudeArgs(s, sess), "--resume", first.ConversationID, "--append-system-prompt", guard)
	return Launch{
		Session:        sess.Name,
		Argv:           argv,
		Dir:            ws.Worktree,
		Env:            []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"},
		Unset:          unsetForAgent,
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		ConversationID: first.ConversationID,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
		TUI:            true,
		Resume:         true,
	}, nil
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

// claudeOnStaticToken checks what agentd runs today: Claude task and local
// sessions, on the plan's static token (V-05).
func claudeOnStaticToken(s Settings, sess protocol.Session) error {
	switch {
	case sess.Agent != protocol.AgentClaude:
		return fmt.Errorf("%w: %s sessions run from plan 04 (codex) or 09 (opencode)", ErrNotInPlan01, sess.Agent)
	case sess.Mode == protocol.ModeRemote:
		return fmt.Errorf("%w: remote mode arrives with plan 03 (Remote Control on the keeper's login)", ErrNotInPlan01)
	case sess.Mode != protocol.ModeTask && sess.Mode != protocol.ModeLocal:
		return fmt.Errorf("mode %q is not task, local or remote", sess.Mode)
	}
	if err := protocol.ValidateClaudeModel(sess.Model); err != nil {
		return err
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
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the launch holds the prompt.
	if err := writeFileAtomic(s.statePath(file), data, 0o600); err != nil {
		return err
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}}); err == nil {
		return fmt.Errorf("tmux session %q already exists", TmuxSession)
	}
	_, err = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{
		"new-session", "-d", "-s", TmuxSession, "-x", "200", "-y", "50", "-c", l.Dir,
		self, "run-agent", "--launch", s.statePath(file),
	}})
	if err != nil {
		// The agent never ran, so a later boot may start it.
		_ = os.Remove(s.statePath(file))
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
