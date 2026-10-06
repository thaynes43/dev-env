package agentd

import (
	"errors"
	"path/filepath"
)

// Settings is the pod-level configuration agentd reads from its environment.
// The per-session part is protocol.Session. Every path has a default that
// matches v1's layout, so haynes-ops can mount the same ConfigMaps (D-14).
type Settings struct {
	// Home is $HOME, the session volume (D-22).
	Home string
	// ClaudeConfigDir is $CLAUDE_CONFIG_DIR, else ~/.claude.
	ClaudeConfigDir string
	// ClaudeState is the CLI's state file: $CLAUDE_CONFIG_DIR/.claude.json when
	// that variable is set, else ~/.claude.json.
	ClaudeState string
	// ConfigDir holds the GitOps config (AGENTD_CONFIG_DIR, default
	// /opt/dev-env/config): claude/CLAUDE.md, claude/mcp.json,
	// claude/agent-*.md, codex/config.toml, codex/AGENTS.header.md.
	ConfigDir string
	// RuntimeDir is $XDG_RUNTIME_DIR: Claude's messaging sockets, mode 0700.
	RuntimeDir string
	// PlaywrightStaging holds the browsers baked into the image
	// (AGENTD_PLAYWRIGHT_STAGING, default /opt/dev-env/ms-playwright).
	PlaywrightStaging string
	// PlaywrightCache is $PLAYWRIGHT_BROWSERS_PATH, else ~/.cache/ms-playwright.
	PlaywrightCache string
	// Bashrc is the shell profile every shell sources (AGENTD_BASHRC, default
	// /opt/dev-env/scripts/bashrc.sh).
	Bashrc string
	// DefaultModel is $DEV_ENV_CLAUDE_MODEL: the model a bare `claude` in the
	// pod uses. Empty means the session's own model.
	DefaultModel string
	// GHTokenFile is the keeper's minted GitHub token (AGENTD_GH_TOKEN_FILE,
	// default /creds/gh_token, D-13).
	GHTokenFile string
	// GitUserName and GitUserEmail are the commit identity
	// (AGENTD_GIT_USER_NAME, AGENTD_GIT_USER_EMAIL; default haynes-dev-bot).
	GitUserName  string
	GitUserEmail string
	// SharedDir is the dev-env-shared mount (AGENTD_SHARED_DIR, default
	// ~/.shared, D-22): memory/, rescue/, logs/.
	SharedDir string
	// StateDir is agentd's own state on the session volume, ~/.agentd.
	StateDir string
	// OAuthAccountFile is the seam for plan 03 (AGENTD_OAUTH_ACCOUNT_FILE): a
	// JSON file with accountUuid and organizationUuid from the keeper's
	// Secret. When it exists, agentd seeds oauthAccount in .claude.json.
	OAuthAccountFile string
	// HWSSHKeyB64 is the hw-ssh private key, base64 on one line
	// (HW_SSH_PRIVATE_KEY_B64). A secret: never logged.
	HWSSHKeyB64 string
	// UserBin is ~/.local/bin; SystemBin is /usr/local/bin, where the image
	// puts its pinned tools.
	UserBin   string
	SystemBin string
	// ClaudeBin is the Claude Code CLI, "claude" on PATH.
	ClaudeBin string
	// Getenv reads the pod's environment, for ${VAR} references in mcp.json.
	Getenv func(string) string
}

// LoadSettings builds Settings from an environment lookup, usually os.Getenv.
func LoadSettings(getenv func(string) string) (Settings, error) {
	home := getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return Settings{}, errors.New("HOME must be an absolute path")
	}
	or := func(key, def string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return def
	}
	s := Settings{
		Home:              home,
		ConfigDir:         or("AGENTD_CONFIG_DIR", "/opt/dev-env/config"),
		RuntimeDir:        getenv("XDG_RUNTIME_DIR"),
		PlaywrightStaging: or("AGENTD_PLAYWRIGHT_STAGING", "/opt/dev-env/ms-playwright"),
		PlaywrightCache:   or("PLAYWRIGHT_BROWSERS_PATH", filepath.Join(home, ".cache", "ms-playwright")),
		Bashrc:            or("AGENTD_BASHRC", "/opt/dev-env/scripts/bashrc.sh"),
		DefaultModel:      getenv("DEV_ENV_CLAUDE_MODEL"),
		GHTokenFile:       or("AGENTD_GH_TOKEN_FILE", "/creds/gh_token"),
		GitUserName:       or("AGENTD_GIT_USER_NAME", "haynes-dev-bot[bot]"),
		GitUserEmail:      or("AGENTD_GIT_USER_EMAIL", "haynes-dev-bot[bot]@users.noreply.github.com"),
		SharedDir:         or("AGENTD_SHARED_DIR", filepath.Join(home, ".shared")),
		StateDir:          filepath.Join(home, ".agentd"),
		OAuthAccountFile:  getenv("AGENTD_OAUTH_ACCOUNT_FILE"),
		HWSSHKeyB64:       getenv("HW_SSH_PRIVATE_KEY_B64"),
		UserBin:           filepath.Join(home, ".local", "bin"),
		SystemBin:         "/usr/local/bin",
		ClaudeBin:         "claude",
		Getenv:            getenv,
	}
	if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		s.ClaudeConfigDir = dir
		s.ClaudeState = filepath.Join(dir, ".claude.json")
	} else {
		s.ClaudeConfigDir = filepath.Join(home, ".claude")
		s.ClaudeState = filepath.Join(home, ".claude.json")
	}
	return s, nil
}

// ReposDir is ~/repos, where each repo is cloned once (D-15).
func (s Settings) ReposDir() string { return filepath.Join(s.Home, "repos") }

// WorkDir is ~/work, where worktrees live (D-15).
func (s Settings) WorkDir() string { return filepath.Join(s.Home, "work") }

// ClonePath is ~/repos/<repo>.
func (s Settings) ClonePath(repo string) string { return filepath.Join(s.ReposDir(), repo) }

// WorktreePath is ~/work/<name>.
func (s Settings) WorktreePath(name string) string { return filepath.Join(s.WorkDir(), name) }
