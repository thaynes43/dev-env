package agentd

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

// Frozen pre-change wrapper; do not generate compatibility fixtures through
// LegacyProjectRules, which is the production recognizer being exercised.
func frozenLegacyProjectRules(snapshot projectcatalog.Snapshot) []byte {
	return []byte(fmt.Sprintf("# Project sample\n\nThis is the permanent sample project. Repository anchors are read-only. Start implementation through the managed task launcher, which fetches and pins source.\n\nProject repositories:\n"+
		"- demo -> demo (default branch: main)\n"+
		"\n<!-- dev-env-project catalog-sha256=%s rules-sha256=%s -->\n\n## Project rules\n\nPROJECT_RULE_IDENTIFIER\nExact rules.\n", snapshot.CatalogRevision(), snapshot.RulesRevision()))
}

func TestProjectLegacyRulesResumePreservesBothProviderInputsAndWIP(t *testing.T) {
	for _, provider := range []string{protocol.AgentClaude, protocol.AgentCodex} {
		t.Run(provider, func(t *testing.T) {
			g, s, runner, sess := privateProjectFixture(t, projectTestCatalog)
			snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			legacy := frozenLegacyProjectRules(snapshot)
			if bytes.Equal(legacy, []byte(snapshot.ProjectRules())) {
				t.Fatal("resume fixture no longer differs from the current wrapper")
			}
			if err := writeWorkspaceJSON(s.statePath("project-snapshot.json"), snapshot); err != nil {
				t.Fatal(err)
			}
			if err := writeFileAtomic(s.statePath("project-rules.md"), legacy, 0o600); err != nil {
				t.Fatal(err)
			}
			beforeSnapshot, err := os.ReadFile(s.statePath("project-snapshot.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareProjectTask(&s, sess); err != nil {
				t.Fatal("legacy task failed preparation", err)
			}
			ws, step := PrepareRepo(context.Background(), runner, s, sess)
			if step.State != StepOK {
				t.Fatal(step.Notes)
			}
			writeFile(t, filepath.Join(ws.Worktree, "README.md"), "private pending work\n")
			writeFile(t, filepath.Join(ws.Worktree, "staged.txt"), "private staged work\n")
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
				writeFile(t, filepath.Join(s.CodexHome, "config.toml"), "developer_instructions = \"configured developer\"\nproject_doc_fallback_filenames = [\"REPO_RULES.md\"]\n")
				sess.Agent, sess.Model = protocol.AgentCodex, "gpt-6.1-sol"
			}
			first, err := BuildLaunch(s, sess, ws, "first", rescueNow)
			if err != nil {
				t.Fatal("legacy first-launch receipt could not be represented", err)
			}
			if provider == protocol.AgentCodex {
				first.ConversationID, first.NativeThreadConfirmed = "12345678-1234-1234-1234-123456789abc", true
			}
			resumed, err := BuildResume(s, sess, ws, first, "resume", rescueNow)
			if err != nil || !resumed.Resume || resumed.Prompt != "" || resumed.ConversationID != first.ConversationID {
				t.Fatal("legacy resume changed its conversation or replayed the prompt", err)
			}
			for _, launch := range []Launch{first, resumed} {
				if provider == protocol.AgentClaude {
					i := slices.Index(launch.Argv, "--append-system-prompt-file")
					if i < 0 || i+1 >= len(launch.Argv) || launch.Argv[i+1] != s.statePath("project-rules.md") {
						t.Fatal("Claude resume lost the saved private rule path")
					}
				} else {
					var scalar map[string]any
					count := 0
					for _, arg := range launch.Argv {
						if strings.HasPrefix(arg, "developer_instructions=") {
							count++
							if err := toml.Unmarshal([]byte(arg), &scalar); err != nil {
								t.Fatal(err)
							}
						}
					}
					want := strings.Join([]string{"configured developer", string(legacy), taskGuard(ws, s.WorkDir())}, "\n\n")
					if count != 1 || scalar["developer_instructions"] != want || !slices.Contains(launch.Argv, `project_doc_fallback_filenames=["REPO_RULES.md","CLAUDE.md"]`) {
						t.Fatal("Codex resume did not preserve exact saved rules and configured discovery")
					}
				}
			}
			afterRules, err := os.ReadFile(s.statePath("project-rules.md"))
			if err != nil || !bytes.Equal(afterRules, legacy) {
				t.Fatal("provider composition rewrote the legacy rule receipt")
			}
			afterSnapshot, err := os.ReadFile(s.statePath("project-snapshot.json"))
			if err != nil || !bytes.Equal(beforeSnapshot, afterSnapshot) || !maps.Equal(beforeWork, gitResumeSnapshot(t, g, ws)) {
				t.Fatal("legacy provider resume changed saved source or WIP")
			}
			modified := append(bytes.Clone(legacy), []byte("\nMODIFIED_RULES\n")...)
			if err := os.WriteFile(s.statePath("project-rules.md"), modified, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := prepareProjectTask(&s, sess); err == nil {
				t.Fatal("edited legacy rules became preparation authority")
			}
			if _, err := BuildResume(s, sess, ws, first, "refused", rescueNow); err == nil {
				t.Fatal("edited legacy rules became provider resume instructions")
			}
			preserved, _ := os.ReadFile(s.statePath("project-rules.md"))
			if !bytes.Equal(preserved, modified) || !maps.Equal(beforeWork, gitResumeSnapshot(t, g, ws)) {
				t.Fatal("refused legacy-rule edit was overwritten or changed WIP")
			}
		})
	}
}

func TestProjectOrphanLegacyRulesCannotGainSnapshotAuthorityOnRetry(t *testing.T) {
	_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
	snapshot, err := projectcatalog.ParseSnapshot(sess.ProjectSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	legacy := frozenLegacyProjectRules(snapshot)
	if err := writeFileAtomic(s.statePath("project-rules.md"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := StoreProjectSnapshot(s, snapshot); err == nil || exists(s.statePath("project-snapshot.json")) {
			t.Fatal("failed orphan legacy admission created its own snapshot authority")
		}
		preserved, err := os.ReadFile(s.statePath("project-rules.md"))
		if err != nil || !bytes.Equal(preserved, legacy) {
			t.Fatal("orphan legacy refusal rewrote the saved rules")
		}
	}
}
