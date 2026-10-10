package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

const managedCodexVersion = "0.160.1"

var nativeThreadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// prepareProjectTask binds the received immutable platform snapshot before any
// clone or provider launch. Catalog parsing here validates saved state; only the
// server's named catalog resolver admits a new task.
func prepareProjectTask(s *Settings, sess protocol.Session) error {
	if len(sess.ProjectSnapshot) == 0 {
		return nil
	}
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil {
		return errors.New("invalid accepted project snapshot")
	}
	repo := snapshot.Selected()
	parts := strings.Split(repo.GitHub, "/")
	if repo.Name != sess.Repo || repo.DefaultBranch != sess.Base || len(parts) != 2 || s.RemoteBase != "https://github.com/"+parts[0] {
		return errors.New("accepted project snapshot does not bind the configured clone and base")
	}
	if _, err := StoreProjectSnapshot(*s, snapshot); err != nil {
		return err
	}
	s.TaskRepositoryURL = repo.URL()
	return nil
}

func providerProjectArgs(s Settings, sess protocol.Session, guard string) ([]string, error) {
	if len(sess.ProjectSnapshot) == 0 {
		return []string{"--append-system-prompt", guard}, nil
	}
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil {
		return nil, err
	}
	path, err := StoreProjectSnapshot(s, snapshot)
	if err != nil {
		return nil, err
	}
	if sess.Agent == protocol.AgentClaude {
		return ProjectRuleInputs(sess.Agent, path, "", guard, snapshot)
	}
	developer, fallbacks, err := codexConfiguredInstructions(s)
	if err != nil {
		return nil, err
	}
	return ProjectRuleInputs(sess.Agent, path, developer, guard, snapshot, fallbacks...)
}

// Read native configuration without rewriting it. A configured native profile
// can contribute developer text and fallback discovery just like the root.
func codexConfiguredInstructions(s Settings) (string, []string, error) {
	type instructions struct {
		Developer string   `toml:"developer_instructions"`
		Fallbacks []string `toml:"project_doc_fallback_filenames"`
	}
	var cfg struct {
		instructions
		Profile  string                  `toml:"profile"`
		Profiles map[string]instructions `toml:"profiles"`
	}
	f, err := os.Open(filepath.Join(s.CodexHome, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, errors.New("native Codex configuration is unreadable")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(data) > 256<<10 || toml.Unmarshal(data, &cfg) != nil {
		return "", nil, errors.New("native Codex configuration is invalid or exceeds its bound")
	}
	if cfg.Profile != "" {
		p, ok := cfg.Profiles[cfg.Profile]
		if !ok {
			return "", nil, errors.New("native Codex configuration names an unknown profile")
		}
		if p.Developer != "" {
			cfg.Developer = p.Developer
		}
		if p.Fallbacks != nil {
			cfg.Fallbacks = p.Fallbacks
		}
	}
	return cfg.Developer, cfg.Fallbacks, nil
}

var codexUnset = []string{
	"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CODEX_AUTH_JSON", "CODEX_AUTH_TOKEN", "OPENAI_AUTH_TOKEN",
	"OPENAI_ACCESS_TOKEN", "CHATGPT_ACCESS_TOKEN", "OPENAI_REFRESH_TOKEN", "CHATGPT_REFRESH_TOKEN", "CODEX_REFRESH_TOKEN",
	"OPENAI_BASE_URL", "OPENAI_API_BASE", "OPENAI_ORGANIZATION", "OPENAI_PROJECT", "CODEX_HOME", "CODEX_LOGIN_URL", "CODEX_REFRESH_TOKEN_URL_OVERRIDE", "CODEX_APP_SERVER_LOGIN_CLIENT_ID",
	"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CONFIG_DIR", "AGENTD_OAUTH_ACCOUNT_FILE", "AGENTD_CODEX_ACCESS_FILE",
	"AGENTD_CODEX_REFRESH_TOKEN_FILE", "CODEX_AUTH_FILE", "OPENAI_AUTH_FILE",
}

func managedCodexAdmission(s Settings, sess protocol.Session, now time.Time) error {
	if !s.ManagedCodexTasks || sess.Mode != protocol.ModeTask || len(sess.ProjectSnapshot) == 0 {
		return errors.New("managed codex tasks are disabled or lack an accepted project snapshot")
	}
	if err := protocol.ValidateCodexEffort(sess.Model, sess.Effort); err != nil {
		return err
	}
	if sess.SessionUID == "" || (sess.Workspace != nil && sess.Workspace.SessionUID != sess.SessionUID) {
		return errors.New("managed Codex requires the existing Session UID")
	}
	if err := prepareProjectTask(&s, sess); err != nil {
		return err
	}
	if sess.MaxTurns() != 0 {
		return errors.New("native Codex exec has no exact max-turns contract; use limits.timeout")
	}
	if s.CodexAccessFile == "" || SyncCodexAccess(s, now) != nil {
		return ErrCodexAccess
	}
	if noSymlinkComponents(s.CodexHome) != nil {
		return errors.New("managed Codex requires a private provider home")
	}
	return nil
}

func buildCodexLaunch(s Settings, sess protocol.Session, ws protocol.Workspace, first *Launch, bootID string, now time.Time) (Launch, error) {
	if err := managedCodexAdmission(s, sess, now); err != nil {
		return Launch{}, err
	}
	guard := taskGuard(ws, s.WorkDir())
	if sess.Workspace != nil {
		guard = sharedGuard(ws, true)
	}
	if s.ManagedChildDecisions {
		guard += "\n\n" + managedChildDecisionGuard
	}
	projectArgs, err := providerProjectArgs(s, sess, guard)
	if err != nil {
		return Launch{}, err
	}
	l := Launch{Provider: protocol.AgentCodex, Session: sess.Name, SessionUID: sess.SessionUID, Dir: ws.Worktree,
		Env: []string{"CODEX_HOME=" + s.CodexHome}, Unset: append(append([]string{}, unsetForAgent...), codexUnset...),
		LogPath: s.LogPath(sess.Name), EventsPath: s.statePath(eventsFile), BootID: bootID, CreatedAt: now.UTC()}
	if s.ManagedChildDecisions {
		if sess.Workspace == nil || s.PodUID == "" {
			return Launch{}, errors.New("managed child decisions require an exact shared task and Pod binding")
		}
		id, err := newUUID()
		if err != nil {
			return Launch{}, err
		}
		l.NativeInvocationID, l.PodUID, l.ChildDecisions = id, s.PodUID, true
	}
	argv := []string{s.CodexBin, "exec", "--json", "--color", "never"}
	if first != nil {
		if !first.NativeThreadConfirmed || first.Provider != protocol.AgentCodex || first.Session != sess.Name || first.SessionUID != sess.SessionUID || first.Dir != ws.Worktree || !nativeThreadID.MatchString(first.ConversationID) {
			return Launch{}, errors.New("native Codex identity or worktree is uncertain; the initial prompt cannot be replayed")
		}
		// Existing task-resume behavior returns the confirmed conversation in
		// its native TUI with no prompt, matching Claude's resume contract.
		argv = []string{s.CodexBin, "resume", first.ConversationID, "--no-daemon"}
		l.ConversationID, l.TUI, l.Resume, l.NativeThreadConfirmed = first.ConversationID, true, true, true
	} else {
		l.Prompt, l.Timeout = sess.Prompt, sess.TimeoutDuration()
	}
	argv = append(argv, "--model", sess.Model, "--dangerously-bypass-approvals-and-sandbox",
		"-c", `model_provider="openai"`, "-c", `openai_base_url=""`, "-c", `chatgpt_base_url="https://chatgpt.com/backend-api/"`, "-c", `forced_login_method="chatgpt"`, "-c", `cli_auth_credentials_store="file"`)
	if sess.Effort != "" {
		value, _ := json.Marshal(sess.Effort)
		argv = append(argv, "-c", "model_reasoning_effort="+string(value))
	}
	l.Argv = append(argv, projectArgs...)
	return l, nil
}

// Version checks do not authenticate or contact a provider. A changed native
// JSONL contract is refused before the task process is spawned.
func verifyManagedCodexCLI(l Launch) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.Argv[0], "--version")
	cmd.Env = agentEnv(os.Environ(), l)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+managedCodexVersion {
		return errors.New("managed Codex requires pinned native CLI " + managedCodexVersion)
	}
	return nil
}

// persistCodexThread makes the first launch's native identity durable before
// acknowledging it in the readable stream. A second, different identity is an
// ambiguity, never a new conversation or a reason to replay the prompt.
func persistCodexThread(path string, l *Launch, id string) error {
	if !nativeThreadID.MatchString(id) || l.ConversationID != "" {
		return errors.New("unexpected native Codex thread identity")
	}
	l.ConversationID, l.NativeThreadConfirmed = id, true
	if err := writeWorkspaceJSON(path, *l); err != nil {
		l.ConversationID, l.NativeThreadConfirmed = "", false
		return fmt.Errorf("native thread receipt is not durable: %w", err)
	}
	return nil
}
