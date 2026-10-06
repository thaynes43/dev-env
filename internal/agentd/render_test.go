package agentd

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const fakeKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAAfakekeymaterial\n-----END OPENSSH PRIVATE KEY-----\n"

// renderFixture lays out the mounted config and the image paths of a session
// pod under a temporary home.
func renderFixture(t *testing.T) (Settings, protocol.Session) {
	t.Helper()
	home := t.TempDir()
	s := testSettings(t, home)
	cfg := s.ConfigDir
	writeFile(t, filepath.Join(cfg, "claude", "CLAUDE.md"), "# rules\n")
	writeFile(t, filepath.Join(cfg, "claude", "agent-opus-worker.md"), "---\nname: opus-worker\n# dev-env-managed\n---\n")
	writeFile(t, filepath.Join(cfg, "claude", "mcp.json"), `{"mcpServers":{"ha":{"type":"http","url":"http://ha:8086${HA_PATH}"},"pw":{"command":"playwright-mcp"}}}`)
	writeFile(t, filepath.Join(cfg, "codex", "config.toml"), "model = \"gpt-6-astra\"\n")
	writeFile(t, filepath.Join(cfg, "codex", "AGENTS.header.md"), "# codex preamble\n")
	writeFile(t, filepath.Join(home, "image", "ms-playwright", "chromium-1247", "chrome"), "bin")
	writeFile(t, filepath.Join(home, "image", "ms-playwright", ".links", "abc"), "/usr/lib/node_modules/playwright")
	writeFile(t, s.Bashrc, "export X=1\n")
	writeFile(t, filepath.Join(s.SystemBin, "pnpm"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(s.SystemBin, "pnpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(s.UserBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/home/dev/.cache/node/corepack/v1/pnpm/11.21.0/bin/pnpm.cjs", filepath.Join(s.UserBin, "pnpm")); err != nil {
		t.Fatal(err)
	}
	// A stale managed subagent and a hand-made one.
	writeFile(t, filepath.Join(s.ClaudeConfigDir, "agents", "old-worker.md"), "# dev-env-managed\n")
	writeFile(t, filepath.Join(s.ClaudeConfigDir, "agents", "mine.md"), "my own agent\n")
	if err := os.MkdirAll(s.SharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s.HWSSHKeyB64 = base64.StdEncoding.EncodeToString([]byte(fakeKey))
	writeFile(t, filepath.Join(s.SystemBin, "gh"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(s.SystemBin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	s.Getenv = envOf(map[string]string{"HA_PATH": "/secret-path-xyz", "PATH": s.UserBin + ":" + s.SystemBin})
	sess := protocol.Session{Name: "haynes-ops-1006-120000", Repo: "haynes-ops", Agent: protocol.AgentClaude, Mode: protocol.ModeTask, Model: "claude-opus-5-5", Prompt: "p"}
	return s, sess
}

func stepByName(t *testing.T, steps []Step, name string) Step {
	t.Helper()
	for _, st := range steps {
		if st.Name == name {
			return st
		}
	}
	t.Fatalf("no step %q", name)
	return Step{}
}

func TestRenderFullPod(t *testing.T) {
	s, sess := renderFixture(t)
	f := &fakeRunner{}
	steps := Render(context.Background(), f, s, sess)

	for _, st := range steps {
		if st.State == StepFail || st.State == StepWarn {
			t.Errorf("step %s: %s %q", st.Name, st.State, st.Notes)
		}
		for _, n := range st.Notes {
			if strings.Contains(n, "secret-path-xyz") || strings.Contains(n, "fakekeymaterial") {
				t.Errorf("step %s leaked a secret: %q", st.Name, n)
			}
		}
	}

	// Runtime dir, 0700.
	if fi, err := os.Stat(s.RuntimeDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("runtime dir: %v %v", fi, err)
	}
	// CLAUDE.md is a link into the config.
	if target, _ := os.Readlink(filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")); target != filepath.Join(s.ConfigDir, "claude", "CLAUDE.md") {
		t.Errorf("CLAUDE.md -> %q", target)
	}
	// Subagents: installed, stale managed one removed, hand-made one kept.
	agents := filepath.Join(s.ClaudeConfigDir, "agents")
	if !exists(filepath.Join(agents, "opus-worker.md")) || exists(filepath.Join(agents, "old-worker.md")) || !exists(filepath.Join(agents, "mine.md")) {
		t.Errorf("subagents: %q", stepByName(t, steps, "subagents").Notes)
	}
	// MCP: raw copy plus two registrations with the variable expanded.
	if data, _ := os.ReadFile(filepath.Join(s.Home, ".config", "dev-env", "mcp.json")); !strings.Contains(string(data), "${HA_PATH}") {
		t.Errorf("mcp.json copy = %s", data)
	}
	var adds []string
	for _, l := range f.lines() {
		if strings.HasPrefix(l, "claude mcp add-json") {
			adds = append(adds, l)
		}
	}
	if len(adds) != 2 || !strings.Contains(adds[0], "http://ha:8086/secret-path-xyz") {
		t.Errorf("mcp adds = %q", adds)
	}
	// Default model from the session.
	if m := readJSONMap(t, filepath.Join(s.ClaudeConfigDir, "settings.json")); m["model"] != "claude-opus-5-5" {
		t.Errorf("settings model = %v", m["model"])
	}
	// Codex config: base + generated servers; 0600 because a URL path is expanded.
	toml, _ := os.ReadFile(filepath.Join(s.Home, ".codex", "config.toml"))
	if !strings.HasPrefix(string(toml), "model = \"gpt-6-astra\"\n") || !strings.Contains(string(toml), "[mcp_servers.ha]") || !strings.Contains(string(toml), "[mcp_servers.pw]") {
		t.Errorf("config.toml = %s", toml)
	}
	if fi, _ := os.Stat(filepath.Join(s.Home, ".codex", "config.toml")); fi.Mode().Perm() != 0o600 {
		t.Errorf("config.toml mode = %v", fi.Mode().Perm())
	}
	if md, _ := os.ReadFile(filepath.Join(s.Home, ".codex", "AGENTS.md")); string(md) != "# codex preamble\n\n# rules\n" {
		t.Errorf("AGENTS.md = %q", md)
	}
	// Git: identity, helper reading the token file, safe.directory.
	gitCalls := strings.Join(f.lines(), "\n")
	for _, want := range []string{
		"git config --global user.name haynes-dev-bot[bot]",
		"git config --global --unset-all credential.https://github.com.helper",
		`password=$(cat ` + s.GHTokenFile + `)`,
		"git config --global --replace-all safe.directory " + filepath.Join(s.Home, "repos", "*"),
		"git config --global --add safe.directory " + filepath.Join(s.Home, "work", "*"),
		"git config --global user.email 304655321+haynes-dev-bot[bot]@users.noreply.github.com",
	} {
		if !strings.Contains(gitCalls, want) {
			t.Errorf("git calls lack %q", want)
		}
	}
	// gh wrapper: reads the token file on every call, runs the image's gh.
	wrapper, _ := os.ReadFile(filepath.Join(s.UserBin, "gh"))
	for _, want := range []string{ghWrapperMarker, "GH_TOKEN=\"$(cat '" + s.GHTokenFile + "')\"", "exec '" + filepath.Join(s.SystemBin, "gh") + "' \"$@\""} {
		if !strings.Contains(string(wrapper), want) {
			t.Errorf("gh wrapper lacks %q:\n%s", want, wrapper)
		}
	}
	// Playwright: revision linked, registry copied.
	if target, _ := os.Readlink(filepath.Join(s.PlaywrightCache, "chromium-1247")); target != filepath.Join(s.PlaywrightStaging, "chromium-1247") {
		t.Errorf("chromium link -> %q", target)
	}
	if !exists(filepath.Join(s.PlaywrightCache, ".links", "abc")) {
		t.Error(".links not copied")
	}
	// Corepack shim removed.
	if _, err := os.Lstat(filepath.Join(s.UserBin, "pnpm")); !isNotExist(err) {
		t.Errorf("corepack shim still there: %v", err)
	}
	// hw-ssh key, 0600 in a 0700 dir.
	key := filepath.Join(s.Home, ".ssh", "dev-env-hw")
	if data, _ := os.ReadFile(key); string(data) != fakeKey {
		t.Error("hw-ssh key content")
	}
	if fi, _ := os.Stat(key); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", fi.Mode().Perm())
	}
	// Shell profile.
	if rc, _ := os.ReadFile(filepath.Join(s.Home, ".bashrc")); !strings.Contains(string(rc), ". "+s.Bashrc) {
		t.Errorf(".bashrc = %q", rc)
	}
	// Memory link to the shared volume, keyed by the clone path.
	link := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(filepath.Join(s.Home, "repos", "haynes-ops")), "memory")
	if target, err := os.Readlink(link); err != nil || !strings.HasPrefix(target, filepath.Join(s.SharedDir, "memory")) {
		t.Errorf("memory link %s -> %q (%v)", link, target, err)
	}
	// .claude.json seeded for the worktree and the clone.
	projects := readJSONMap(t, s.ClaudeState)[keyProjects].(map[string]any)
	if _, ok := projects[s.WorktreePath(sess.Name)]; !ok {
		t.Errorf("worktree not trusted: %v", projects)
	}
}

func TestRenderIsIdempotent(t *testing.T) {
	s, sess := renderFixture(t)
	Render(context.Background(), &fakeRunner{}, s, sess)
	steps := Render(context.Background(), &fakeRunner{}, s, sess)
	for _, st := range steps {
		if st.State == StepFail || st.State == StepWarn {
			t.Errorf("second render: step %s: %s %q", st.Name, st.State, st.Notes)
		}
	}
	rc, _ := os.ReadFile(filepath.Join(s.Home, ".bashrc"))
	if n := strings.Count(string(rc), s.Bashrc); n != 2 { // one line names the path twice
		t.Errorf(".bashrc sources the profile %d/2 times: %q", n, rc)
	}
}

func TestRenderBareImage(t *testing.T) {
	// Nothing mounted, no claude on PATH: every step skips or warns, none fails.
	home := t.TempDir()
	s := testSettings(t, home)
	s.RuntimeDir = ""
	sess := protocol.Session{Name: "x-1", Repo: "x", Agent: protocol.AgentCodex, Mode: protocol.ModeTask, Model: "gpt-6-astra", Prompt: "p"}
	f := &fakeRunner{missing: map[string]bool{"claude": true}}
	steps := Render(context.Background(), f, s, sess)
	for _, st := range steps {
		if st.State == StepFail {
			t.Errorf("step %s failed: %q", st.Name, st.Notes)
		}
	}
	if st := stepByName(t, steps, "hw-ssh-key"); st.State != StepSkip {
		t.Errorf("hw-ssh-key = %v", st)
	}
	if st := stepByName(t, steps, "claude-default-model"); st.State != StepSkip {
		t.Errorf("default model = %v", st)
	}
}

func TestRenderWarnings(t *testing.T) {
	s, sess := renderFixture(t)
	s.HWSSHKeyB64 = base64.StdEncoding.EncodeToString([]byte("not a key"))
	s.DefaultModel = "opus"
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Name == "git" && c.Args[2] == "user.email" {
			return Result{}, &CmdError{Name: "git", Sub: "config", ExitCode: 255}
		}
		if c.Name == "git" && c.Args[2] == "--unset-all" {
			return Result{}, &CmdError{Name: "git", Sub: "config", ExitCode: 5} // not set: fine
		}
		return Result{}, nil
	}}
	steps := Render(context.Background(), f, s, sess)
	for name, want := range map[string]string{
		"hw-ssh-key":           "does not decode to a private key",
		"claude-default-model": "aliases are refused",
		"git":                  "git config user.email failed",
	} {
		st := stepByName(t, steps, name)
		if st.State != StepWarn || !strings.Contains(strings.Join(st.Notes, "\n"), want) {
			t.Errorf("step %s = %s %q, want a warning with %q", name, st.State, st.Notes, want)
		}
	}
	if exists(filepath.Join(s.Home, ".ssh", "dev-env-hw")) {
		t.Error("a bad key was written")
	}
}

func TestMemoryLinkKeepsRealDirectory(t *testing.T) {
	s, sess := renderFixture(t)
	real := filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(s.ClonePath(sess.Repo)), "memory")
	writeFile(t, filepath.Join(real, "note.md"), "kept")
	st := renderMemoryLink(s, sess)
	if st.State != StepWarn {
		t.Errorf("state = %s %q", st.State, st.Notes)
	}
	if data, _ := os.ReadFile(filepath.Join(real, "note.md")); string(data) != "kept" {
		t.Error("real memory directory touched")
	}
}

func TestGHWrapperLeavesAForeignGh(t *testing.T) {
	s, _ := renderFixture(t)
	writeFile(t, filepath.Join(s.UserBin, "gh"), "#!/bin/sh\necho mine\n")
	if st := renderGHWrapper(s); st.State != StepWarn {
		t.Errorf("state %s %q", st.State, st.Notes)
	}
	if data, _ := os.ReadFile(filepath.Join(s.UserBin, "gh")); string(data) != "#!/bin/sh\necho mine\n" {
		t.Error("a gh agentd did not write was replaced")
	}
	s.Getenv = envOf(map[string]string{"PATH": s.SystemBin + ":" + s.UserBin})
	if err := os.Remove(filepath.Join(s.UserBin, "gh")); err != nil {
		t.Fatal(err)
	}
	if st := renderGHWrapper(s); st.State != StepWarn || !strings.Contains(strings.Join(st.Notes, " "), "does not lead PATH") {
		t.Errorf("PATH order: %s %q", st.State, st.Notes)
	}
	s.Getenv = envOf(nil)
	if st := renderGHWrapper(s); st.State != StepSkip {
		t.Errorf("no gh: %s %q", st.State, st.Notes)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Errorf("shellQuote = %s", got)
	}
}

func TestClaudeProjectKey(t *testing.T) {
	if got := claudeProjectKey("/home/dev/repos/haynes-ops"); got != "-home-dev-repos-haynes-ops" {
		t.Errorf("key = %q", got)
	}
	if got := claudeProjectKey("/home/dev/repos/a.b_c"); got != "-home-dev-repos-a-b-c" {
		t.Errorf("key = %q", got)
	}
}
