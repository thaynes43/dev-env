package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/commonrules"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
)

func withCommonRules(t *testing.T, s *Settings, sess *protocol.Session) commonrules.Snapshot {
	t.Helper()
	s.RetainedCommonRules = true
	common := commonfixture.Snapshot(t, sess.ProjectSnapshot, "COMMON_RULE_IDENTIFIER\nexact common bytes\n", "HEADER_IDENTIFIER\n")
	raw, err := common.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	sess.CommonRules = raw
	return common
}

func TestRetainedCommonTwoProviderResumePreservesInputsAndWIP(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			g, s, runner, sess := privateProjectFixture(t, projectTestCatalog)
			sess.Agent = provider
			common := withCommonRules(t, &s, &sess)
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			ws, step := PrepareRepo(context.Background(), runner, s, sess)
			if step.State != StepOK {
				t.Fatal(step.Notes)
			}
			writeFile(t, filepath.Join(ws.Worktree, "README.md"), "pending local work\n")
			writeFile(t, filepath.Join(ws.Worktree, "staged.txt"), "staged private work\n")
			gitRun(t, g.env, ws.Worktree, "add", "staged.txt")
			beforeWork := gitResumeSnapshot(t, g, ws)
			if provider == protocol.AgentCodex {
				s.ManagedCodexTasks = true
				s.CodexAccessFile = filepath.Join(s.Home, "projection", "access.json")
				access, err := codexauth.Encode(syntheticAgentCodexAccess(t, rescueNow, 1), rescueNow)
				if err != nil {
					t.Fatal(err)
				}
				writeFile(t, s.CodexAccessFile, string(access))
				writeFile(t, filepath.Join(s.CodexHome, "config.toml"), "developer_instructions = \"original developer\"\nproject_doc_fallback_filenames = [\"REPO_RULES.md\"]\nproject_doc_max_bytes = 262144\n[mcp_servers.preserved]\ncommand = \"untouched\"\n")
				sess.Model = "gpt-6.1-sol"
			}
			first, err := BuildLaunch(s, sess, ws, "first", rescueNow)
			if err != nil {
				t.Fatal(err)
			}
			if first.CommonInputs == nil || first.CommonInputs.CommonRevision != common.CommonRevision || first.CommonInputs.Derivation != "RetainedLocalInstructionProjection" {
				t.Fatal("missing separate provenance", first.CommonInputs)
			}
			if provider == protocol.AgentCodex {
				first.ConversationID = "12345678-1234-1234-1234-123456789abc"
				first.NativeThreadConfirmed = true
			}
			saved := map[string][]byte{}
			for _, p := range []string{filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"), filepath.Join(s.CodexHome, "AGENTS.md"), s.statePath(commonProjectionFile), s.statePath(commonOwnershipFile), s.statePath("project-snapshot.json"), s.statePath("project-rules.md")} {
				saved[p], err = os.ReadFile(p)
				if err != nil {
					t.Fatal(err)
				}
			}
			if string(saved[filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")]) != common.Common.Text || string(saved[filepath.Join(s.CodexHome, "AGENTS.md")]) != common.CodexGlobal() {
				t.Fatal("global composition changed source bytes")
			}
			writeFile(t, filepath.Join(s.CodexHome, "config.toml"), "developer_instructions = \"later developer\"\nproject_doc_fallback_filenames = [\"LATER.md\"]\nproject_doc_max_bytes = 999999\n[mcp_servers.preserved]\ncommand = \"later unrelated\"\n")
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal(err)
			}
			resumed, err := BuildResume(s, sess, ws, first, "resume", rescueNow)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first.CommonInputs, resumed.CommonInputs) || resumed.Prompt != "" {
				t.Fatal("resume recaptured projection or prompt")
			}
			if provider == protocol.AgentCodex {
				firstInputs, resumeInputs := []string{}, []string{}
				for _, pair := range []struct {
					argv []string
					out  *[]string
				}{{first.Argv, &firstInputs}, {resumed.Argv, &resumeInputs}} {
					for _, a := range pair.argv {
						if strings.HasPrefix(a, "developer_instructions=") || strings.HasPrefix(a, "project_doc_") {
							*pair.out = append(*pair.out, a)
						}
					}
				}
				if !slices.Equal(firstInputs, resumeInputs) || !slices.Contains(resumeInputs, "project_doc_max_bytes=262144") || strings.Contains(strings.Join(resumeInputs, " "), "later developer") {
					t.Fatal("resume read changed configuration", resumeInputs)
				}
			} else if !slices.Contains(resumed.Argv, "--append-system-prompt-file") {
				t.Fatal("Claude lost retained project inputs")
			}
			for p, want := range saved {
				got, _ := os.ReadFile(p)
				if !bytes.Equal(got, want) {
					t.Fatal("resume rewrote original", p)
				}
			}
			cfg, _ := os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
			if !bytes.Contains(cfg, []byte("later unrelated")) || !maps.Equal(beforeWork, gitResumeSnapshot(t, g, ws)) {
				t.Fatal("resume rewrote provider state or WIP")
			}
			changed := commonfixture.Snapshot(t, sess.ProjectSnapshot, "changed current common", common.Header.Text)
			sess.CommonRules, _ = changed.Bytes()
			if _, err := BuildResume(s, sess, ws, first, "changed-source", rescueNow); err == nil {
				t.Fatal("changed source gained resume authority")
			}
			if !maps.Equal(beforeWork, gitResumeSnapshot(t, g, ws)) {
				t.Fatal("refusal changed WIP")
			}
		})
	}
}

func TestRetainedCommonRefusesForeignPartialAndMissingHistoricalInputs(t *testing.T) {
	for _, kind := range []string{"foreign", "symlink", "preparing", "mode-drift", "hardlink", "missing-projection", "modified-global", "partial", "orphan-ownership"} {
		t.Run(kind, func(t *testing.T) {
			_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
			withCommonRules(t, &s, &sess)
			global := filepath.Join(s.ClaudeConfigDir, "CLAUDE.md")
			if kind == "foreign" || kind == "symlink" {
				if err := privateCommonDir(s.ClaudeConfigDir); err != nil {
					t.Fatal(err)
				}
				if kind == "foreign" {
					writeFile(t, global, "foreign bytes")
				} else {
					target := filepath.Join(s.Home, "foreign")
					writeFile(t, target, "foreign bytes")
					if err := os.Symlink(target, global); err != nil {
						t.Fatal(err)
					}
				}
				if err := prepareCommonTask(s, sess); err == nil {
					t.Fatal("foreign file adopted")
				}
				got, _ := os.ReadFile(global)
				if string(got) != "foreign bytes" {
					t.Fatal("foreign file changed")
				}
				return
			}
			if err := prepareCommonTask(s, sess); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "preparing", "partial":
				var receipt commonOwnership
				if err := readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt.State = "Preparing"
				if err := writeWorkspaceJSON(s.statePath(commonOwnershipFile), receipt); err != nil {
					t.Fatal(err)
				}
				if kind == "partial" {
					if err := os.Remove(filepath.Join(s.CodexHome, "AGENTS.md")); err != nil {
						t.Fatal(err)
					}
				}
			case "orphan-ownership":
				if err := os.Remove(s.statePath(commonOwnershipFile)); err != nil {
					t.Fatal(err)
				}
			case "mode-drift":
				if err := os.Chmod(global, 0644); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(global, filepath.Join(s.Home, "alias")); err != nil {
					t.Fatal(err)
				}
			case "modified-global":
				if err := os.WriteFile(global, []byte("modified owned bytes"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-projection":
				if _, err := retainedCommonArgs(s, sess, "guard", false, "rules"); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(s.statePath(commonProjectionFile)); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(global)
			receipt, _ := os.ReadFile(s.statePath(commonOwnershipFile))
			if kind == "missing-projection" {
				if _, err := retainedCommonArgs(s, sess, "guard", true, "rules"); err == nil {
					t.Fatal("missing projection reconstructed")
				}
			} else if err := prepareCommonTask(s, sess); err == nil {
				t.Fatal("uncertain state accepted")
			}
			got, _ := os.ReadFile(global)
			after, _ := os.ReadFile(s.statePath(commonOwnershipFile))
			if !bytes.Equal(got, before) || !bytes.Equal(receipt, after) {
				t.Fatal("refusal repaired state")
			}
		})
	}
}

func TestRetainedCommonConfigurationFIFORefusesImmediately(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.CodexHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(s.CodexHome, "config.toml"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := codexConfiguredInputs(s); err == nil {
		t.Fatal("FIFO configuration accepted")
	}
}

func TestRetainedCommonLegacyLaunchHasUnknownProvenance(t *testing.T) {
	s, sess, ws := launchFixture(t)
	launch, err := BuildLaunch(s, sess, ws, "legacy", rescueNow)
	if err != nil || launch.CommonInputs != nil {
		t.Fatal("legacy provenance fabricated", err)
	}
	if _, err := json.Marshal(launch); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedCommonFreshConfigSealsOnlyMountedInstructionProjection(t *testing.T) {
	_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
	sess.Agent = protocol.AgentCodex
	withCommonRules(t, &s, &sess)
	writeFile(t, filepath.Join(s.ConfigDir, "codex", "config.toml"), "developer_instructions = \"mounted original developer\"\nprofile = \"focused\"\n[profiles.focused]\nproject_doc_fallback_filenames = [\"NATIVE.md\"]\nproject_doc_max_bytes = 262144\n[mcp_servers.unrelated]\ncommand = \"synthetic-unrelated-not-captured\"\n")
	args, err := retainedCommonArgs(s, sess, "original guard", false, "saved project rules")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "mounted original developer") || !slices.Contains(args, "project_doc_max_bytes=262144") {
		t.Fatal("fresh mounted instructions dropped", args)
	}
	raw, err := os.ReadFile(s.statePath(commonProjectionFile))
	if err != nil || bytes.Contains(raw, []byte("synthetic-unrelated-not-captured")) {
		t.Fatal("whole config copied", err)
	}
	if _, err := os.Lstat(filepath.Join(s.CodexHome, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("provider config was created or copied")
	}
	writeFile(t, filepath.Join(s.ConfigDir, "codex", "config.toml"), "developer_instructions = \"changed mount\"\n")
	resumed, err := retainedCommonArgs(s, sess, "later guard", true, "saved project rules")
	if err != nil || !slices.Equal(args, resumed) {
		t.Fatal("resume recaptured mounted config", err, resumed)
	}
}

func TestRetainedCommonRenderSkipsOnlyOwnedInstructionConfiguration(t *testing.T) {
	_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
	withCommonRules(t, &s, &sess)
	if err := prepareCommonTask(s, sess); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(s.CodexHome, "config.toml")
	writeFile(t, cfg, "developer_instructions = \"original\"\n[mcp_servers.keep]\ncommand = \"keep\"\n")
	before, _ := os.ReadFile(cfg)
	for _, step := range []Step{renderClaudeMD(s, s.ConfigDir), renderCodexAgentsMD(s, s.ConfigDir), renderCodexConfig(s, s.ConfigDir)} {
		if step.State != StepSkip {
			t.Fatal("mutable rendering ran", step)
		}
	}
	got, _ := os.ReadFile(cfg)
	if !bytes.Equal(got, before) {
		t.Fatal("render replaced provider config")
	}
	defaultSettings, err := LoadSettings(envOf(map[string]string{"HOME": s.Home}))
	if err != nil || defaultSettings.RetainedCommonRules {
		t.Fatal("common route enabled by default", err)
	}
	explicit, err := LoadSettings(envOf(map[string]string{"HOME": s.Home, "AGENTD_ENABLE_RETAINED_COMMON_RULES": "true"}))
	if err != nil || !explicit.RetainedCommonRules {
		t.Fatal("explicit common setting ignored", err)
	}
}

func TestRetainedCommonRefusesForeignConfiguredInstructionAlias(t *testing.T) {
	_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
	sess.Agent = protocol.AgentCodex
	withCommonRules(t, &s, &sess)
	if err := prepareCommonTask(s, sess); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(s.Home, "foreign.toml")
	writeFile(t, target, "developer_instructions = \"foreign\"\n")
	if err := os.Symlink(target, filepath.Join(s.CodexHome, "config.toml")); err != nil {
		t.Fatal(err)
	}
	if _, err := retainedCommonArgs(s, sess, "guard", false, "project rules"); err == nil {
		t.Fatal("foreign config became retained instructions")
	}
	if exists(s.statePath(commonProjectionFile)) {
		t.Fatal("foreign projection retained")
	}
	got, _ := os.ReadFile(target)
	if !bytes.Contains(got, []byte("foreign")) {
		t.Fatal("foreign config overwritten")
	}
}
