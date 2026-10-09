package agentd

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Step is the outcome of one boot step (protocol.Step).
type Step = protocol.Step

// Step states.
const (
	StepOK   = "ok"
	StepWarn = "warn"
	StepSkip = "skip"
	StepFail = "fail"
)

// newStep builds a Step from its notes and error: a note that starts with
// "WARN" or an error raises the state.
func newStep(name string, notes []string, err error) Step {
	st := Step{Name: name, State: StepOK, Notes: notes}
	for _, n := range notes {
		if strings.HasPrefix(n, "WARN") {
			st.State = StepWarn
		}
	}
	if err != nil {
		st.State = StepFail
		st.Notes = append(st.Notes, err.Error())
	}
	return st
}

func skipStep(name, why string) Step { return Step{Name: name, State: StepSkip, Notes: []string{why}} }

// Render is boot step 1 (DESIGN-001 3.6), a port of v1's dev-init.sh: it links
// and renders the GitOps config into the session's home. Every step is
// idempotent, so a resume on the same volume runs it again. It never touches
// provider history or caches. Codex auth changes only when its keeper access
// projection is explicitly configured (D-78).
//
// Dropped from dev-init, because the agent image bakes them in (plan 01): the
// Codex standalone and kubectl-cnpg downloads, and the agent-run,
// declare-activity, pve, hw-ssh and claude-login-check links. Dropped because
// v2 has no code-server: the workspace README.
func Render(ctx context.Context, r Runner, s Settings, sess protocol.Session) []Step {
	cfg := s.ConfigDir
	steps := []Step{
		renderRuntimeDir(s),
		renderClaudeMD(s, cfg),
		renderSubagents(s, cfg),
	}
	steps = append(steps, renderMCP(ctx, r, s, cfg))
	steps = append(steps,
		renderDefaultModel(s, sess),
		renderBypassPrompt(s),
		renderCodexConfig(s, cfg),
		renderCodexAgentsMD(s, cfg),
		renderCodexAccess(s, time.Now()),
		renderGit(ctx, r, s),
		renderGHWrapper(s),
		renderPlaywright(s),
		renderCorepackShims(s),
		renderHWSSHKey(s),
		renderShellProfile(s),
		renderMemoryLink(s, sess),
		renderClaudeState(s, sess),
	)
	return steps
}

// renderRuntimeDir makes $XDG_RUNTIME_DIR with mode 0700. Claude refuses a
// messaging-socket directory that is group- or world-writable, and the
// directory is a tmpfs that starts empty on every container start.
func renderRuntimeDir(s Settings) Step {
	const name = "runtime-dir"
	if s.RuntimeDir == "" {
		return skipStep(name, "XDG_RUNTIME_DIR is not set; cross-session messaging is off")
	}
	if err := os.MkdirAll(s.RuntimeDir, 0o700); err != nil {
		return newStep(name, nil, err)
	}
	return newStep(name, nil, os.Chmod(s.RuntimeDir, 0o700))
}

// renderClaudeMD links ~/.claude/CLAUDE.md to the mounted file, so a
// ConfigMap update reaches it without a copy.
func renderClaudeMD(s Settings, cfg string) Step {
	const name = "claude-md"
	src := filepath.Join(cfg, "claude", "CLAUDE.md")
	if !exists(src) {
		return skipStep(name, src+" is not mounted")
	}
	return newStep(name, nil, symlinkForce(src, filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")))
}

// managedMarker marks a subagent file that agentd (or v1's dev-init) installed.
// Hand-made agents have no marker and are never touched.
var managedMarker = regexp.MustCompile(`(?m)^# dev-env-managed`)

// renderSubagents copies config/claude/agent-<name>.md to
// ~/.claude/agents/<name>.md (copies, not links: the CLI scans plain files),
// and removes a managed copy whose source has left the ConfigMap.
func renderSubagents(s Settings, cfg string) Step {
	const name = "subagents"
	dst := filepath.Join(s.ClaudeConfigDir, "agents")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return newStep(name, nil, err)
	}
	var notes []string
	current, _ := filepath.Glob(filepath.Join(dst, "*.md"))
	for _, f := range current {
		data, err := os.ReadFile(f)
		if err != nil || !managedMarker.Match(data) {
			continue
		}
		if exists(filepath.Join(cfg, "claude", "agent-"+filepath.Base(f))) {
			continue
		}
		if err := os.Remove(f); err == nil {
			notes = append(notes, fmt.Sprintf("subagent %q removed (no longer in the config)", filepath.Base(f)))
		}
	}
	srcs, _ := filepath.Glob(filepath.Join(cfg, "claude", "agent-*.md"))
	for _, src := range srcs {
		base := strings.TrimPrefix(filepath.Base(src), "agent-")
		data, err := os.ReadFile(src)
		if err == nil {
			err = writeFileAtomic(filepath.Join(dst, base), data, 0o644)
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("WARN subagent %q install failed: %v", base, err))
			continue
		}
		notes = append(notes, fmt.Sprintf("subagent %q installed", base))
	}
	return newStep(name, notes, nil)
}

// renderMCP copies mcp.json to ~/.config/dev-env/mcp.json (unexpanded, as v1
// does) and registers each server with the Claude CLI at user scope (D-14).
func renderMCP(ctx context.Context, r Runner, s Settings, cfg string) Step {
	const name = "mcp"
	src := filepath.Join(cfg, "claude", "mcp.json")
	data, err := os.ReadFile(src)
	if isNotExist(err) {
		return skipStep(name, src+" is not mounted")
	}
	if err != nil {
		return newStep(name, nil, err)
	}
	if err := writeFileAtomic(filepath.Join(s.Home, ".config", "dev-env", "mcp.json"), data, 0o644); err != nil {
		return newStep(name, nil, err)
	}
	mc, err := readMCPConfig(src)
	if err != nil {
		return newStep(name, nil, err)
	}
	if _, err := r.LookPath(s.ClaudeBin); err != nil {
		return newStep(name, []string{"WARN the claude CLI is not on PATH; no MCP server registered"}, nil)
	}
	return newStep(name, registerMCP(ctx, r, s, mc), nil)
}

// renderDefaultModel re-asserts settings.json's model: $DEV_ENV_CLAUDE_MODEL,
// else the session's own model for a Claude session. Full ids only.
func renderDefaultModel(s Settings, sess protocol.Session) Step {
	const name = "claude-default-model"
	model := s.DefaultModel
	if model == "" && sess.Agent == protocol.AgentClaude {
		model = sess.Model
	}
	if model == "" {
		return skipStep(name, "no default model: DEV_ENV_CLAUDE_MODEL is unset and the session is not Claude's")
	}
	if err := protocol.ValidateClaudeModel(model); err != nil {
		return newStep(name, []string{"WARN default model not set: " + err.Error()}, nil)
	}
	note, err := assertDefaultModel(filepath.Join(s.ClaudeConfigDir, "settings.json"), model)
	return newStep(name, []string{note}, err)
}

// renderBypassPrompt seeds settings.json's skipDangerousModePermissionPrompt,
// so a TUI started with --dangerously-skip-permissions opens at its prompt
// rather than on the bypass warning (D-23, D-58): nobody is attached to accept
// it, and messages pasted into the pane would answer it.
func renderBypassPrompt(s Settings) Step {
	const name = "claude-bypass-prompt"
	changed, err := assertSettingsKey(filepath.Join(s.ClaudeConfigDir, "settings.json"), keySkipBypassPrompt, true)
	if err != nil {
		return newStep(name, nil, err)
	}
	if !changed {
		return newStep(name, []string{keySkipBypassPrompt + " already set"}, nil)
	}
	return newStep(name, []string{keySkipBypassPrompt + " set"}, nil)
}

// codexGeneratedHeader separates the GitOps config.toml from the generated
// MCP tables.
const codexGeneratedHeader = "\n# ── MCP servers: GENERATED by agentd from config/claude/mcp.json — edit mcp.json, not this ──\n\n"

// renderCodexConfig writes ~/.codex/config.toml: the GitOps file plus
// [mcp_servers.*] rendered from mcp.json, so both agents keep one MCP list
// (D-12). It references secrets by variable name only.
func renderCodexConfig(s Settings, cfg string) Step {
	const name = "codex-config"
	base, err := os.ReadFile(filepath.Join(cfg, "codex", "config.toml"))
	if isNotExist(err) {
		return skipStep(name, "config/codex/config.toml is not mounted")
	}
	if err != nil {
		return newStep(name, nil, err)
	}
	var notes []string
	var servers string
	mc, err := readMCPConfig(filepath.Join(cfg, "claude", "mcp.json"))
	switch {
	case err == nil:
		e := newExpander(s.Getenv)
		servers, err = renderCodexMCP(mc, e)
		if err != nil {
			return newStep(name, nil, err)
		}
		for _, w := range e.warnings() {
			notes = append(notes, "WARN codex MCP: "+w)
		}
	case isNotExist(err):
		notes = append(notes, "no mcp.json: no MCP servers for codex")
	default:
		return newStep(name, nil, err)
	}
	var buf bytes.Buffer
	buf.Write(base)
	buf.WriteString(codexGeneratedHeader)
	buf.WriteString(servers)
	if err := writeFileAtomic(filepath.Join(s.Home, ".codex", "config.toml"), buf.Bytes(), 0o600); err != nil {
		return newStep(name, notes, err)
	}
	notes = append(notes, fmt.Sprintf("config.toml rendered (%d MCP servers)", strings.Count(servers, "[mcp_servers.")))
	return newStep(name, notes, nil)
}

// renderCodexAgentsMD writes ~/.codex/AGENTS.md: the Codex preamble plus the
// shared CLAUDE.md, so Codex reads the same rules.
func renderCodexAgentsMD(s Settings, cfg string) Step {
	const name = "codex-agents-md"
	header, err := os.ReadFile(filepath.Join(cfg, "codex", "AGENTS.header.md"))
	if isNotExist(err) {
		return skipStep(name, "config/codex/AGENTS.header.md is not mounted")
	}
	if err != nil {
		return newStep(name, nil, err)
	}
	rules, err := os.ReadFile(filepath.Join(cfg, "claude", "CLAUDE.md"))
	if err != nil {
		return newStep(name, nil, err)
	}
	out := append(append(append([]byte(nil), header...), '\n'), rules...)
	return newStep(name, nil, writeFileAtomic(filepath.Join(s.Home, ".codex", "AGENTS.md"), out, 0o644))
}

// renderGit sets the commit identity and the credential helper. The helper
// reads the keeper's token file at each use (D-13), so no token is ever stored
// in .gitconfig. The unset-all first is v1's lesson of 2026-08-19: a stray
// `gh auth setup-git` leaves a second helper value, and plain `git config`
// then refuses to replace the multi-valued key.
func renderGit(ctx context.Context, r Runner, s Settings) Step {
	const name = "git"
	helper := fmt.Sprintf(`!f() { echo username=x-access-token; echo "password=$(cat %s)"; }; f`, s.GHTokenFile)
	var notes []string
	failed := false
	for _, args := range [][]string{
		{"config", "--global", "user.name", s.GitUserName},
		{"config", "--global", "user.email", s.GitUserEmail},
		{"config", "--global", "--unset-all", `credential.https://github.com.helper`},
		{"config", "--global", "--unset-all", `credential.https://gist.github.com.helper`},
		{"config", "--global", `credential.https://github.com.helper`, helper},
		{"config", "--global", "--replace-all", "safe.directory", filepath.Join(s.ReposDir(), "*")},
		// A linked worktree is checked by its own path, so ~/work too.
		{"config", "--global", "--add", "safe.directory", filepath.Join(s.WorkDir(), "*")},
	} {
		_, err := runBounded(ctx, r, cliTimeout, Cmd{Name: "git", Args: args})
		// git config --unset-all exits 5 when the key is not set.
		notSet := args[2] == "--unset-all" && ExitCodeOf(err) == 5
		if err != nil && !notSet {
			notes = append(notes, fmt.Sprintf("WARN git config %s failed: %v", args[2], err))
			failed = true
		}
	}
	if !failed {
		notes = append(notes, "identity "+s.GitUserName+", credential helper reads "+s.GHTokenFile)
	}
	return newStep(name, notes, nil)
}

// ghWrapperMarker marks the gh wrapper agentd writes; a gh it did not write
// is never replaced.
const ghWrapperMarker = "# dev-env-managed: agentd's gh wrapper"

// renderGHWrapper writes ~/.local/bin/gh, which reads the keeper's token file
// on every call and runs the image's gh. A minted token lasts 60 minutes and the
// keeper re-mints it every 40 (D-13), so a GH_TOKEN copied when the agent
// started would be dead by the end of a long task; agentd removes that copy
// from the agent's environment (D-42). ~/.local/bin leads the image's PATH.
func renderGHWrapper(s Settings) Step {
	const name = "gh-wrapper"
	path := s.Getenv("PATH")
	real := findInPath(path, "gh", s.UserBin)
	if real == "" {
		return skipStep(name, "gh is not on PATH")
	}
	wrapper := filepath.Join(s.UserBin, "gh")
	if data, err := os.ReadFile(wrapper); err == nil && !bytes.Contains(data, []byte(ghWrapperMarker)) {
		return newStep(name, []string{"WARN " + wrapper + " was not written by agentd; left as it is, so gh may hold a stale token"}, nil)
	}
	script := fmt.Sprintf("#!/bin/sh\n%s (DESIGN-001 D-42).\n"+
		"# A minted token lasts 60 minutes, so read it on every call.\n"+
		"if [ -s %s ]; then GH_TOKEN=\"$(cat %s)\"; export GH_TOKEN; fi\n"+
		"exec %s \"$@\"\n", ghWrapperMarker, shellQuote(s.GHTokenFile), shellQuote(s.GHTokenFile), shellQuote(real))
	if err := writeFileAtomic(wrapper, []byte(script), 0o755); err != nil {
		return newStep(name, nil, err)
	}
	var notes []string
	if dirs := filepath.SplitList(path); len(dirs) == 0 || filepath.Clean(dirs[0]) != filepath.Clean(s.UserBin) {
		notes = append(notes, "WARN "+s.UserBin+" does not lead PATH, so the wrapper may not be the gh that runs")
	}
	notes = append(notes, wrapper+" runs "+real+" with a fresh token")
	return newStep(name, notes, nil)
}

// findInPath is the first executable named name in a PATH value, skipping the
// directory skip.
func findInPath(path, name, skip string) string {
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || filepath.Clean(dir) == filepath.Clean(skip) {
			continue
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// renderPlaywright makes the image's browsers available in the volume's
// browser cache. v1 copies each revision directory; agentd links it, because a
// session volume is new for every session and the browsers are about 670 MB
// (D-40). Revisions a repo installs for its own pinned Playwright sit beside
// the links. The small .links registry files are copied, so Playwright's
// cleanup knows the image's package still uses its browsers.
func renderPlaywright(s Settings) Step {
	const name = "playwright"
	entries, err := os.ReadDir(s.PlaywrightStaging)
	if isNotExist(err) {
		return skipStep(name, s.PlaywrightStaging+" is not in this image")
	}
	if err != nil {
		return newStep(name, nil, err)
	}
	if err := os.MkdirAll(s.PlaywrightCache, 0o755); err != nil {
		return newStep(name, nil, err)
	}
	var notes []string
	for _, e := range entries {
		src := filepath.Join(s.PlaywrightStaging, e.Name())
		dst := filepath.Join(s.PlaywrightCache, e.Name())
		if e.Name() == ".links" {
			notes = append(notes, copyMissingFiles(src, dst)...)
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			notes = append(notes, fmt.Sprintf("WARN playwright %s not linked: %v", e.Name(), err))
			continue
		}
		notes = append(notes, "linked "+e.Name())
	}
	return newStep(name, notes, nil)
}

// copyMissingFiles copies each regular file in src that dst lacks.
func copyMissingFiles(src, dst string) []string {
	entries, err := os.ReadDir(src)
	if err != nil {
		return []string{fmt.Sprintf("WARN %s: %v", src, err)}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return []string{fmt.Sprintf("WARN %s: %v", dst, err)}
	}
	var notes []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		to := filepath.Join(dst, e.Name())
		if exists(to) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err == nil {
			err = writeFileAtomic(to, data, 0o644)
		}
		if err != nil {
			notes = append(notes, fmt.Sprintf("WARN %s not copied: %v", e.Name(), err))
		}
	}
	return notes
}

// renderCorepackShims removes corepack symlinks in ~/.local/bin that shadow the
// image's pinned pnpm, pnpx, yarn or npm. ~/.local/bin leads PATH, so a
// one-off `corepack enable` would otherwise serve corepack's cached version
// for the rest of the volume's life (v1, 2026-09-13).
func renderCorepackShims(s Settings) Step {
	const name = "corepack-shims"
	var notes []string
	for _, cmd := range []string{"pnpm", "pnpx", "yarn", "npm"} {
		link := filepath.Join(s.UserBin, cmd)
		target, err := os.Readlink(link)
		if err != nil || !strings.Contains(target, "corepack") {
			continue
		}
		fi, err := os.Stat(filepath.Join(s.SystemBin, cmd))
		if err != nil || fi.Mode()&0o111 == 0 {
			continue
		}
		if err := os.Remove(link); err == nil {
			notes = append(notes, "removed the corepack shim "+link)
		}
	}
	if len(notes) == 0 {
		return Step{Name: name, State: StepOK}
	}
	return newStep(name, notes, nil)
}

// renderHWSSHKey writes the hw-ssh private key from HW_SSH_PRIVATE_KEY_B64 to
// ~/.ssh/dev-env-hw with mode 0600 (ssh refuses a readable key, and a Secret
// volume would be 0444 under fsGroup). It never logs the key or its decoding.
func renderHWSSHKey(s Settings) Step {
	const name = "hw-ssh-key"
	if s.HWSSHKeyB64 == "" {
		return skipStep(name, "HW_SSH_PRIVATE_KEY_B64 is not set; the SSH tier is off in this pod")
	}
	clean := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s.HWSSHKeyB64)
	key, err := base64.StdEncoding.DecodeString(clean)
	if err != nil || !bytes.Contains(key, []byte("PRIVATE KEY")) {
		return newStep(name, []string{"WARN HW_SSH_PRIVATE_KEY_B64 does not decode to a private key: it must be the base64 of the key file, on one line"}, nil)
	}
	dir := filepath.Join(s.Home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return newStep(name, nil, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return newStep(name, nil, err)
	}
	path := filepath.Join(dir, "dev-env-hw")
	// A key written earlier keeps its mode in writeFileAtomic; force 0600.
	if err := writeFileAtomic(path, key, 0o600); err != nil {
		return newStep(name, nil, err)
	}
	return newStep(name, []string{"written to " + path}, os.Chmod(path, 0o600))
}

// renderShellProfile makes every interactive shell source the mounted
// bashrc.sh (PATH; it exports no GH_TOKEN, D-49), and bridges login shells to
// .bashrc: a fresh volume has no skeleton files.
func renderShellProfile(s Settings) Step {
	const name = "shell-profile"
	if !exists(s.Bashrc) {
		return skipStep(name, s.Bashrc+" is not in this image")
	}
	line := fmt.Sprintf("[ -f %s ] && . %s", s.Bashrc, s.Bashrc)
	rc := filepath.Join(s.Home, ".bashrc")
	cur, err := os.ReadFile(rc)
	if err != nil && !isNotExist(err) {
		return newStep(name, nil, err)
	}
	if !bytes.Contains(cur, []byte(s.Bashrc)) {
		f, err := os.OpenFile(rc, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return newStep(name, nil, err)
		}
		_, werr := fmt.Fprintf(f, "\n%s\n", line)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return newStep(name, nil, werr)
		}
	}
	profile := filepath.Join(s.Home, ".bash_profile")
	if !exists(profile) {
		if err := os.WriteFile(profile, []byte("[ -f ~/.bashrc ] && . ~/.bashrc\n"), 0o644); err != nil {
			return newStep(name, nil, err)
		}
	}
	return newStep(name, nil, nil)
}

// claudeProjectKey is the CLI's directory name for a project path: every
// character that is not a letter or digit becomes "-"
// (/home/dev/repos/haynes-ops -> -home-dev-repos-haynes-ops).
func claudeProjectKey(path string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, path)
}

// renderMemoryLink links the repo's Claude memory to the shared volume
// (D-22), so every session on a repo sees the same memory, as in v1. The CLI
// keys a worktree's memory by the clone's path, which D-15 keeps at
// ~/repos/<repo>. A real memory directory already there is left alone.
func renderMemoryLink(s Settings, sess protocol.Session) Step {
	const name = "memory-link"
	if fi, err := os.Stat(s.SharedDir); err != nil || !fi.IsDir() {
		return skipStep(name, s.SharedDir+" is not mounted; memory stays on this volume")
	}
	key := claudeProjectKey(s.ClonePath(sess.Repo))
	shared := filepath.Join(s.SharedDir, "memory", key)
	if err := os.MkdirAll(shared, 0o755); err != nil {
		return newStep(name, nil, err)
	}
	link := filepath.Join(s.ClaudeConfigDir, "projects", key, "memory")
	if fi, err := os.Lstat(link); err == nil && fi.Mode()&fs.ModeSymlink == 0 {
		return newStep(name, []string{"WARN " + link + " is a real directory; not replaced by the shared link"}, nil)
	}
	return newStep(name, []string{link + " -> " + shared}, symlinkForce(shared, link))
}

// renderClaudeState seeds .claude.json for a cold home (D-11, R-02 P-3):
// onboarding done, the worktree and the clone trusted, and oauthAccount when
// the plan 03 seam provides it. It runs after the MCP registration, which may
// create the file.
func renderClaudeState(s Settings, sess protocol.Session) Step {
	const name = "claude-state"
	dirs := []string{s.WorktreePath(sess.Name), s.ClonePath(sess.Repo)}
	notes, err := seedClaudeState(s.ClaudeState, s.OAuthAccountFile, dirs)
	return newStep(name, notes, err)
}
