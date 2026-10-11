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

func TestRetainedCommonFailedFirstLaunchCannotRecaptureLostProjection(t *testing.T) {
	g, s, runner, sess := privateProjectFixture(t, projectTestCatalog)
	sess.Agent, sess.Model = protocol.AgentCodex, "gpt-6.1-sol"
	s.ManagedCodexTasks = true
	s.CodexAccessFile = filepath.Join(s.Home, "projection", "access.json")
	withCommonRules(t, &s, &sess)
	access, err := codexauth.Encode(syntheticAgentCodexAccess(t, rescueNow, 1), rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(access))
	if err := prepareProjectTask(&s, sess); err != nil {
		t.Fatal(err)
	}
	ws, step := PrepareRepo(context.Background(), runner, s, sess)
	if step.State != StepOK {
		t.Fatal(step.Notes)
	}
	writeFile(t, filepath.Join(ws.Worktree, "chain.txt"), "local-only commit\n")
	gitRun(t, g.env, ws.Worktree, "add", "chain.txt")
	gitRun(t, g.env, ws.Worktree, "commit", "-q", "-m", "local-only work")
	writeFile(t, filepath.Join(ws.Worktree, "README.md"), "dirty private work\n")
	writeFile(t, filepath.Join(ws.Worktree, "staged.txt"), "staged private work\n")
	writeFile(t, filepath.Join(ws.Worktree, "untracked.txt"), "untracked private work\n")
	gitRun(t, g.env, ws.Worktree, "add", "staged.txt")
	op := gitRun(t, g.env, ws.Worktree, "rev-parse", "--path-format=absolute", "--git-path", "CHERRY_PICK_HEAD")
	writeFile(t, op, gitRun(t, g.env, ws.Worktree, "rev-parse", "HEAD")+"\n")
	beforeWork := gitResumeSnapshot(t, g, ws)
	config := filepath.Join(s.CodexHome, "config.toml")
	// The bounded config is sealed before the resulting argv exceeds Linux's
	// individual argument limit. No launch.json or provider process is created.
	writeFile(t, config, "developer_instructions = \""+strings.Repeat("x", 128<<10)+"\"\n")
	if _, err := BuildLaunch(s, sess, ws, "first", rescueNow); err == nil {
		t.Fatal("oversized effective argv unexpectedly launched")
	}
	if !exists(s.statePath(commonProjectionFile)) || exists(s.statePath(launchFile)) {
		t.Fatal("fixture did not seal projection before first-launch failure")
	}
	if err := os.Remove(s.statePath(commonProjectionFile)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, config, "developer_instructions = \"changed current developer\"\n[mcp_servers.keep]\ncommand = \"preserved\"\n")
	before := map[string][]byte{}
	for _, path := range []string{config, s.CodexAccessFile, filepath.Join(s.CodexHome, "auth.json"), filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"), filepath.Join(s.CodexHome, "AGENTS.md"), s.statePath(commonOwnershipFile), s.statePath("project-snapshot.json"), s.statePath("project-rules.md")} {
		before[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := BuildLaunch(s, sess, ws, "retry", rescueNow); err == nil {
		t.Fatal("failed first launch recaptured missing historical projection")
	}
	if exists(s.statePath(commonProjectionFile)) || exists(s.statePath(launchFile)) || !maps.Equal(beforeWork, gitResumeSnapshot(t, g, ws)) {
		t.Fatal("refusal reconstructed inputs, published launch or changed WIP")
	}
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatal("refusal changed original state", path, err)
		}
	}
}

func commonInputState(t *testing.T, s Settings) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, path := range []string{filepath.Join(s.ClaudeConfigDir, "CLAUDE.md"), filepath.Join(s.CodexHome, "AGENTS.md"), filepath.Join(s.CodexHome, "config.toml"), s.statePath(commonOwnershipFile), s.statePath(commonProjectionFile), s.statePath(launchFile)} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			state[path] = "Missing"
		} else if err != nil {
			t.Fatal(err)
		} else {
			state[path] = "Present:" + string(data)
		}
	}
	return state
}

func TestRetainedCommonProjectionLatchRefusesUncertainState(t *testing.T) {
	for _, kind := range []string{"old-version", "missing-state", "preparing", "fresh-digest", "fresh-projection", "fresh-launch", "confirmed-missing", "confirmed-partial", "confirmed-digest-mismatch"} {
		t.Run(kind, func(t *testing.T) {
			_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
			withCommonRules(t, &s, &sess)
			if err := prepareCommonTask(s, sess); err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "confirmed-") {
				if _, err := retainedCommonArgs(s, sess, "original guard", false, "rules"); err != nil {
					t.Fatal(err)
				}
			}
			var owned commonOwnership
			if err := readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &owned); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "old-version":
				owned.Version, owned.ProjectionState = 1, ""
			case "missing-state":
				owned.ProjectionState = ""
			case "preparing":
				owned.ProjectionState, owned.ProjectionDigest = "Preparing", commonrules.Digest([]byte("original projection"))
			case "fresh-digest", "confirmed-digest-mismatch":
				owned.ProjectionDigest = commonrules.Digest([]byte("different projection"))
			case "fresh-projection":
				writeFile(t, s.statePath(commonProjectionFile), "unknown historical inputs\n")
			case "fresh-launch":
				writeFile(t, s.statePath(launchFile), "unknown historical launch\n")
			case "confirmed-missing":
				if err := os.Remove(s.statePath(commonProjectionFile)); err != nil {
					t.Fatal(err)
				}
			case "confirmed-partial":
				writeFile(t, s.statePath(commonProjectionFile), `{"version":`)
			}
			if err := writeWorkspaceJSON(s.statePath(commonOwnershipFile), owned); err != nil {
				t.Fatal(err)
			}
			before := commonInputState(t, s)
			if _, err := retainedCommonArgs(s, sess, "changed guard", false, "rules"); err == nil {
				t.Fatal("uncertain historical state accepted")
			}
			if _, err := commonLaunchBinding(s, sess); err == nil {
				t.Fatal("uncertain historical state gained a launch binding")
			}
			if !maps.Equal(before, commonInputState(t, s)) {
				t.Fatal("refusal repaired or changed uncertain state")
			}
		})
	}
}

func TestRetainedCommonProjectionDurabilityBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-intent", "intent-unconfirmed", "intent-readback", "projection-partial", "projection-unconfirmed", "projection-readback", "confirmation-unconfirmed", "confirmation-readback"} {
		t.Run(boundary, func(t *testing.T) {
			_, s, _, sess := privateProjectFixture(t, projectTestCatalog)
			sess.Agent = protocol.AgentCodex
			snapshot := withCommonRules(t, &s, &sess)
			if err := prepareCommonTask(s, sess); err != nil {
				t.Fatal(err)
			}
			owned := freshCommonOwnership(s, sess, snapshot)
			projection := commonProjection{Version: 1, Session: sess.Name, SessionUID: sess.SessionUID, Provider: sess.Agent, SnapshotDigest: commonrules.Digest(sess.CommonRules), ProjectDigest: snapshot.ProjectDigest, RenderedRulesDigest: commonrules.Digest([]byte("saved rules")), ConfiguredDeveloper: "original developer", Fallbacks: []string{"ORIGINAL.md"}, DocMaxBytes: 262144, Guard: "original guard"}
			data, err := json.Marshal(projection)
			if err != nil {
				t.Fatal(err)
			}
			digest := commonrules.Digest(data)
			attempted := false
			writes := commonProjectionWrites{
				ownership: func(path string, value any) error {
					receipt := value.(commonOwnership)
					if boundary == "before-intent" {
						return commonrules.ErrRefused
					}
					if boundary == "intent-readback" && receipt.ProjectionState == "Preparing" {
						receipt.ProjectionDigest = commonrules.Digest([]byte("different intent"))
					}
					if boundary == "confirmation-readback" && receipt.ProjectionState == "Confirmed" {
						receipt.ProjectionState = "Preparing"
					}
					if err := writeWorkspaceJSON(path, receipt); err != nil {
						t.Fatal(err)
					}
					if boundary == "intent-unconfirmed" && receipt.ProjectionState == "Preparing" || boundary == "confirmation-unconfirmed" && receipt.ProjectionState == "Confirmed" {
						return commonrules.ErrRefused
					}
					return nil
				},
				projection: func(path string, raw []byte) error {
					attempted = true
					var intent commonOwnership
					if readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &intent) != nil || intent.ProjectionState != "Preparing" || intent.ProjectionDigest != digest {
						t.Fatal("projection write preceded durable original intent")
					}
					switch boundary {
					case "projection-partial":
						raw = []byte(`{"version":`)
					case "projection-readback":
						changed := projection
						changed.ConfiguredDeveloper = "different persisted inputs"
						raw, err = json.Marshal(changed)
						if err != nil {
							t.Fatal(err)
						}
					}
					if err := projectSyncExclusiveFile(path, raw); err != nil {
						t.Fatal(err)
					}
					if boundary == "projection-partial" || boundary == "projection-unconfirmed" {
						return commonrules.ErrRefused
					}
					return nil
				},
			}
			if err := sealCommonProjection(s, owned, projection, writes); err == nil {
				t.Fatal("uncertain boundary was acknowledged")
			}
			if strings.HasPrefix(boundary, "intent-") || boundary == "before-intent" {
				if attempted || exists(s.statePath(commonProjectionFile)) {
					t.Fatal("unconfirmed intent attempted projection creation")
				}
			}
			var retained commonOwnership
			if err := readCommonJSON(s.statePath(commonOwnershipFile), commonOwnershipLimit, &retained); err != nil {
				t.Fatal(err)
			}
			wantState := "Preparing"
			switch boundary {
			case "before-intent":
				wantState = "Fresh"
			case "confirmation-unconfirmed":
				wantState = "Confirmed"
			}
			if retained.ProjectionState != wantState || boundary != "before-intent" && boundary != "intent-readback" && retained.ProjectionDigest != digest {
				t.Fatal("lost original projection state or digest", retained.ProjectionState)
			}
			writeFile(t, filepath.Join(s.CodexHome, "config.toml"), "developer_instructions = \"changed current developer\"\n")
			before := commonInputState(t, s)
			args, err := retainedCommonArgs(s, sess, "changed guard", false, "saved rules")
			switch wantState {
			case "Preparing":
				if err == nil || !maps.Equal(before, commonInputState(t, s)) {
					t.Fatal("interrupted projection was repaired or recaptured", err)
				}
			case "Confirmed":
				if err != nil || !strings.Contains(strings.Join(args, " "), "original developer") || strings.Contains(strings.Join(args, " "), "changed current developer") || !maps.Equal(before, commonInputState(t, s)) {
					t.Fatal("lost confirmation acknowledgement did not reuse original inputs", err)
				}
			case "Fresh":
				if err != nil || !strings.Contains(strings.Join(args, " "), "changed current developer") {
					t.Fatal("never-attempted projection could not be created", err)
				}
			}
		})
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
