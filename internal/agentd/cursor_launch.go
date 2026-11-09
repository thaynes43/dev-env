package agentd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// cursorUnset is what a cursor agent's environment loses: everything a Claude
// or Codex agent loses except the Cursor API key, which the operator set on
// this pod for the CLI, plus every Claude and Codex credential and override.
// A Cursor task never runs on another provider's login (DESIGN-058).
var cursorUnset = func() []string {
	out := slices.DeleteFunc(slices.Clone(unsetForAgent), func(k string) bool { return k == cursorAPIKeyEnv })
	for _, k := range append(append([]string{}, codexUnset...), "CLAUDE_CODE_OAUTH_REFRESH_TOKEN") {
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}()

// buildCursorLaunch builds a cursor task's only launch (DESIGN-058): the
// pinned Cursor CLI in print mode, always-allow, with stream-json output, an
// explicit model and the task's worktree as its workspace. Cursor takes the
// prompt as its positional argument (https://cursor.com/docs/cli/headless),
// so the prompt is the last argument and stdin stays empty.
//
// Cursor reads the repository's CLAUDE.md and AGENTS.md itself and documents
// no file for user-level rules, so the prompt's first line names the
// platform's common rules and the task's project rules, and the platform
// guard follows it, as Claude gets it in the system prompt.
func buildCursorLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, bootID string, now time.Time) (Launch, error) {
	if err := sess.Validate(); err != nil {
		return Launch{}, err
	}
	if sess.Agent != protocol.AgentCursor || sess.Mode != protocol.ModeTask {
		return Launch{}, errors.New(protocol.CursorModeRefusal)
	}
	if len(sess.ProjectSnapshot) == 0 {
		return Launch{}, errors.New("a cursor task needs an accepted project snapshot")
	}
	if sess.MaxTurns() != 0 {
		return Launch{}, errors.New("the Cursor CLI has no max-turns flag; use limits.timeout")
	}
	if strings.TrimSpace(s.Getenv(cursorAPIKeyEnv)) == "" {
		return Launch{}, errors.New("cursor credential unavailable: " + cursorAPIKeyEnv + " is not set on this pod, so the task is not started")
	}
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil {
		return Launch{}, err
	}
	projectRules, err := StoreProjectSnapshot(s, snapshot)
	if err != nil {
		return Launch{}, err
	}
	common := filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")
	if _, err := os.Stat(common); err != nil {
		common = ""
	}
	prompt := cursorPrompt(common, projectRules, taskGuard(ws, s.WorkDir()), sess.Prompt)
	l := Launch{
		Provider: protocol.AgentCursor,
		Session:  sess.Name,
		Argv: []string{cursorCLIPath(s), "-p", "--force", "--trust",
			"--output-format", "stream-json", "--model", sess.Model, "--workspace", ws.Worktree, prompt},
		Dir:        ws.Worktree,
		Unset:      cursorUnset,
		LogPath:    s.LogPath(sess.Name),
		EventsPath: s.statePath(eventsFile),
		Timeout:    sess.TimeoutDuration(),
		BootID:     bootID,
		CreatedAt:  now.UTC(),
	}
	if err := bindInitialUsage(&l, sess, s.PodUID); err != nil {
		return Launch{}, err
	}
	return l, nil
}

// cursorPrompt is the prompt a cursor task gets: one line naming the rules to
// read, the platform guard, then the task. It always starts with that fixed
// line, so the positional argument can never read as a CLI flag.
func cursorPrompt(commonRules, projectRules, guard, task string) string {
	line := "Before you start, read and follow "
	switch {
	case commonRules != "" && projectRules != "":
		line += "the platform rules in " + commonRules + " and the project rules in " + projectRules + "."
	case commonRules != "":
		line += "the platform rules in " + commonRules + "."
	case projectRules != "":
		line += "the project rules in " + projectRules + "."
	default:
		line += "the repository's CLAUDE.md and AGENTS.md."
	}
	return line + "\n\n" + guard + "\n\n" + task
}
