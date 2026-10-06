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
	bootFile   = "boot.json"
	launchFile = "launch.json"
	resultFile = "task-result.json"
	pidFile    = "agent.pid"
	eventsFile = "task-events.jsonl"
)

// Launch is everything `agentd run-agent` needs to run the agent: agentd
// writes it, and the tmux pane reads it, so no prompt is ever quoted through a
// shell (v1 passed it through `tmux send-keys`).
type Launch struct {
	Session string `json:"session"`
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

// taskGuard is v1's guard for a task, appended to the system prompt.
func taskGuard(ws protocol.Workspace, workDir string) string {
	return fmt.Sprintf("You are working in an isolated git worktree (%s) on branch %s. "+
		"Stay inside it. NEVER push to main: commit to %s, push that branch, and "+
		"open a PR with 'gh pr create' when the task is complete. If the task needs extra "+
		"git worktrees, create them under %s and 'git worktree remove' each once its PR "+
		"merges; never leave stranded worktrees behind. If blocked, write BLOCKED and the "+
		"reason as your final message.", ws.Worktree, ws.Branch, ws.Branch, workDir)
}

// BuildLaunch builds the task launch for a session (D-42). Plan 01 runs Claude
// task sessions on the static token; local and remote modes arrive with plans
// 02 and 03, Codex task pods with plan 04 (D-12: Codex runs in the hub until
// the keeper owns its login), opencode with plan 09.
func BuildLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	switch {
	case sess.Agent != protocol.AgentClaude:
		return Launch{}, fmt.Errorf("%w: %s sessions run from plan 04 (codex) or 09 (opencode)", ErrNotInPlan01, sess.Agent)
	case sess.Mode == protocol.ModeLocal:
		return Launch{}, fmt.Errorf("%w: local mode arrives with plan 02", ErrNotInPlan01)
	case sess.Mode == protocol.ModeRemote:
		return Launch{}, fmt.Errorf("%w: remote mode arrives with plan 03 (Remote Control on the keeper's login)", ErrNotInPlan01)
	case sess.Mode != protocol.ModeTask:
		return Launch{}, fmt.Errorf("mode %q is not task, local or remote", sess.Mode)
	}
	if err := protocol.ValidateClaudeModel(sess.Model); err != nil {
		return Launch{}, err
	}
	// V-05: a task runs on the plan's static token or not at all.
	if strings.TrimSpace(s.Getenv("CLAUDE_CODE_OAUTH_TOKEN")) == "" {
		return Launch{}, errors.New("plan credential unavailable: CLAUDE_CODE_OAUTH_TOKEN is not set, and no session falls back to a metered key")
	}
	conv, err := newUUID()
	if err != nil {
		return Launch{}, err
	}
	argv := []string{s.ClaudeBin, "--model", sess.Model}
	if sess.Effort != "" {
		argv = append(argv, "--effort", sess.Effort)
	}
	argv = append(argv, "--dangerously-skip-permissions", "--session-id", conv)
	if n := sess.MaxTurns(); n > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(int(n)))
	}
	argv = append(argv,
		"--append-system-prompt", taskGuard(ws, s.WorkDir()),
		"--output-format", "stream-json", "--verbose",
		"-p")
	return Launch{
		Session:        sess.Name,
		Argv:           argv,
		Prompt:         sess.Prompt,
		Dir:            ws.Worktree,
		Env:            []string{"CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=dev-env"},
		Unset:          unsetForAgent,
		LogPath:        s.LogPath(sess.Name),
		EventsPath:     s.statePath(eventsFile),
		Timeout:        sess.TimeoutDuration(),
		ConversationID: conv,
		BootID:         bootID,
		CreatedAt:      now.UTC(),
	}, nil
}

// StartAgent writes the launch file and starts the tmux session that runs
// `agentd run-agent` in the worktree. The launch file is the record that the
// task was started: agentd never starts it a second time (D-42).
func StartAgent(ctx context.Context, r Runner, s Settings, l Launch, self string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the launch holds the prompt.
	if err := writeFileAtomic(s.statePath(launchFile), data, 0o600); err != nil {
		return err
	}
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"has-session", "-t", "=" + TmuxSession}}); err == nil {
		return fmt.Errorf("tmux session %q already exists", TmuxSession)
	}
	_, err = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{
		"new-session", "-d", "-s", TmuxSession, "-x", "200", "-y", "50", "-c", l.Dir,
		self, "run-agent", "--launch", s.statePath(launchFile),
	}})
	if err != nil {
		// The task never ran, so a later boot may start it.
		_ = os.Remove(s.statePath(launchFile))
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
