package agentd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

func TestUpgradeRole(t *testing.T) {
	core := upgradeRoleCore(t)
	projects := map[string]string{
		"haynes-ops": "repository: haynes-ops\nadapter: synthetic Flux revision, Gatus status, and Renovate tier are fixture observations.\nThey are not credentials and they do not authorize deferral.\n",
		"dev-env":    "repository: dev-env\nruntimeHealth: NotApplicable\nreconciliation: NotApplicable\nThis source-only assignment does not report cluster runtime health.\n",
	}
	if projects["haynes-ops"] == projects["dev-env"] {
		t.Fatal("repo fixtures must differ")
	}
	var projections [][]byte
	for repo, project := range projects {
		home := t.TempDir()
		s := testSettings(t, home)
		rules := append(append([]byte{}, core...), '\n')
		writeFile(t, filepath.Join(s.ConfigDir, "claude", "CLAUDE.md"), string(rules))
		writeFile(t, filepath.Join(s.ConfigDir, "codex", "AGENTS.header.md"), "# codex preamble\n")
		projectPath := filepath.Join(s.ConfigDir, "project", repo, "project-rules.md")
		writeFile(t, projectPath, project)
		runner := &fakeRunner{
			missing: map[string]bool{"claude": true, "codex": true},
			handle: func(c Cmd) (Result, error) {
				t.Fatalf("%s fixture invoked %s", repo, c.Name)
				return Result{}, nil
			},
		}
		claude := renderClaudeMD(s, s.ConfigDir)
		codex := renderCodexAgentsMD(s, s.ConfigDir)
		mcp := renderMCP(context.Background(), runner, s, s.ConfigDir)
		if claude.State != StepOK || codex.State != StepOK || mcp.State != StepSkip {
			t.Fatalf("%s steps claude=%+v codex=%+v mcp=%+v", repo, claude, codex, mcp)
		}
		if len(runner.calls) != 0 {
			t.Fatalf("%s calls %+v", repo, runner.calls)
		}
		linked, err := os.ReadFile(filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"))
		if err != nil {
			t.Fatal(err)
		}
		agents, err := os.ReadFile(filepath.Join(s.Home, ".codex", "AGENTS.md"))
		if err != nil {
			t.Fatal(err)
		}
		prefix := []byte("# codex preamble\n\n")
		if !bytes.Equal(linked, rules) || !bytes.HasPrefix(agents, prefix) || !bytes.Equal(agents[len(prefix):], rules) {
			t.Fatalf("%s projection mismatch", repo)
		}
		if !bytes.Contains(linked, core) || !bytes.Contains(agents, core) {
			t.Fatalf("%s lost the role core", repo)
		}
		after, err := os.ReadFile(projectPath)
		if err != nil || string(after) != project {
			t.Fatal(err)
		}
		projections = append(projections, append([]byte(nil), linked...), append([]byte(nil), agents[len(prefix):]...))
		if sha256.Sum256(core) != sha256.Sum256(linked[:len(core)]) {
			t.Fatalf("%s core digest was not preserved", repo)
		}
	}
	for _, got := range projections[1:] {
		if !bytes.Equal(got, projections[0]) {
			t.Fatal("role projection differs across Claude, Codex, haynes-ops, or dev-env")
		}
	}
}

func upgradeRoleCore(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "upgrade-shepherd-role.md"))
	if err != nil {
		t.Fatal(err)
	}
	const start = "<!-- upgrade-shepherd-role-core:start -->\n"
	const end = "\n<!-- upgrade-shepherd-role-core:end -->"
	i := bytes.Index(b, []byte(start))
	j := bytes.Index(b, []byte(end))
	if i < 0 || j < 0 || j < i {
		t.Fatal("role core markers missing")
	}
	return b[i+len(start) : j]
}
