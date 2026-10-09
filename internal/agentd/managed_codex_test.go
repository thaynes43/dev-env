package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func managedCodexFixture(t *testing.T) (Settings, protocol.Session, protocol.Workspace, time.Time) {
	t.Helper()
	s, sess, ws := launchFixture(t)
	s.ManagedCodexTasks, s.RemoteBase = true, "https://github.com/fixture"
	s.CodexAccessFile = filepath.Join(s.Home, "projection", "access.json")
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 9, 20, 0, 0, 0, time.UTC)
	access, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(access))
	catalog, err := projectcatalog.Parse([]byte(projectTestCatalog))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "demo")
	if err != nil {
		t.Fatal(err)
	}
	sess.ProjectSnapshot, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	sess.SessionUID, sess.Repo, sess.Base = "existing-session-uid", "demo", "main"
	sess.Agent, sess.Model, sess.Limits = protocol.AgentCodex, "gpt-6.1-sol", &protocol.Limits{Timeout: "1m"}
	return s, sess, ws, now
}

func TestManagedCodexNativeInputsAndEnvironment(t *testing.T) {
	s, sess, ws, now := managedCodexFixture(t)
	config := "developer_instructions = \"root developer\"\nproject_doc_fallback_filenames = [\"ROOT.md\"]\nprofile = \"task\"\n[profiles.task]\ndeveloper_instructions = \"profile developer\"\nproject_doc_fallback_filenames = [\"REPO_RULES.md\"]\n"
	writeFile(t, filepath.Join(s.CodexHome, "config.toml"), config)
	l, err := BuildLaunch(s, sess, ws, "boot", now)
	if err != nil {
		t.Fatal(err)
	}
	if l.Prompt != sess.Prompt || l.ConversationID != "" || l.NativeThreadConfirmed || l.Provider != protocol.AgentCodex || l.Timeout != time.Minute {
		t.Fatal("initial native task identity or prompt contract changed")
	}
	if !slices.Equal(l.Argv[:5], []string{"codex", "exec", "--json", "--color", "never"}) || strings.Contains(strings.Join(l.Argv, " "), sess.Prompt) {
		t.Fatal("native launch omitted JSONL or placed prompt in argv")
	}
	var scalar map[string]any
	count := 0
	for _, arg := range l.Argv {
		if strings.HasPrefix(arg, "developer_instructions=") {
			count++
			if toml.Unmarshal([]byte(arg), &scalar) != nil {
				t.Fatal("developer override is not valid native TOML")
			}
		}
	}
	developer, _ := scalar["developer_instructions"].(string)
	if count != 1 || !strings.HasPrefix(developer, "profile developer\n\n") || !strings.Contains(developer, "PROJECT_RULE_IDENTIFIER") || !strings.Contains(developer, "isolated git worktree") {
		t.Fatal("native developer scalar lost configured text, project rules or writer guard")
	}
	if !slices.Contains(l.Argv, `project_doc_fallback_filenames=["REPO_RULES.md","CLAUDE.md"]`) || !slices.Contains(l.Argv, `openai_base_url=""`) {
		t.Fatal("native repository discovery or fixed provider route changed")
	}
	parent := []string{"PATH=/fixture", "UNRELATED=retained"}
	for _, key := range codexUnset {
		parent = append(parent, key+"=SYNTHETIC_CANARY")
	}
	env := agentEnv(parent, l)
	if !slices.Contains(env, "UNRELATED=retained") || !slices.Contains(env, "CODEX_HOME="+s.CodexHome) {
		t.Fatal("native environment lost private home or normal task context")
	}
	for _, entry := range env {
		if strings.Contains(entry, "CANARY") {
			t.Fatal("native credential override survived scrubbing")
		}
	}
	after, _ := os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
	if string(after) != config {
		t.Fatal("provider composition rewrote native configuration")
	}
}

func TestManagedCodexPinnedWarningDoesNotManufactureTurn(t *testing.T) {
	s, sess, ws, now := managedCodexFixture(t)
	l, err := BuildLaunch(s, sess, ws, "boot", now)
	if err != nil {
		t.Fatal(err)
	}
	path := s.statePath(launchFile)
	if err := writeWorkspaceJSON(path, l); err != nil {
		t.Fatal(err)
	}
	var stream codexStream
	var result streamResult
	var output bytes.Buffer
	if stream.line([]byte(`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`), path, &l, &output, &result) != nil ||
		stream.line([]byte(`{"type":"item.completed","item":{"id":"item_0","type":"error","message":"synthetic startup warning"}}`), path, &l, &output, &result) != nil || stream.turn || stream.terminal || result.Seen {
		t.Fatal("pinned warning changed the native turn or resume identity contract")
	}
	if stream.line([]byte(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"synthetic misplaced output"}}`), path, &l, &output, &result) == nil {
		t.Fatal("real turn output accepted before turn.started")
	}
}

func TestManagedCodexAdmissionAndExactResume(t *testing.T) {
	for _, kind := range []string{"disabled", "mode", "model", "snapshot", "repo", "base", "owner", "uid", "max-turns", "projection", "shared-auth", "symlink-auth"} {
		t.Run(kind, func(t *testing.T) {
			s, sess, ws, now := managedCodexFixture(t)
			switch kind {
			case "disabled":
				s.ManagedCodexTasks = false
			case "mode":
				sess.Mode = protocol.ModeRemote
			case "model":
				sess.Model = "codex"
			case "snapshot":
				sess.ProjectSnapshot = nil
			case "repo":
				sess.Repo = "foreign"
			case "base":
				sess.Base = "foreign"
			case "owner":
				s.RemoteBase = "https://github.com/foreign"
			case "uid":
				sess.SessionUID = ""
			case "max-turns":
				sess.Limits.MaxTurns = 1
			case "projection":
				s.CodexAccessFile = ""
			case "shared-auth":
				s.CodexHome = filepath.Join(s.Home, "codex", "sample")
			case "symlink-auth":
				s.CodexHome = filepath.Join(s.Home, "linked-auth")
				if err := os.Symlink(t.TempDir(), s.CodexHome); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := BuildLaunch(s, sess, ws, "boot", now); err == nil {
				t.Fatal("invalid managed provider admission accepted")
			}
			if (kind == "shared-auth" || kind == "symlink-auth") && exists(filepath.Join(s.CodexHome, "auth.json")) {
				t.Fatal("private destination refusal installed access material")
			}
			if kind == "shared-auth" || kind == "symlink-auth" {
				if !errors.Is(SyncCodexAccess(s, now), ErrCodexAccess) || exists(filepath.Join(s.CodexHome, "auth.json")) {
					t.Fatal("render/periodic access sync bypassed the private destination fence")
				}
			}
		})
	}
	s, sess, ws, now := managedCodexFixture(t)
	first, err := BuildLaunch(s, sess, ws, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildResume(s, sess, ws, first, "resume", now); err == nil {
		t.Fatal("uncertain native launch replayed the initial prompt")
	}
	first.ConversationID, first.NativeThreadConfirmed = "12345678-1234-1234-1234-123456789abc", true
	resume, err := BuildResume(s, sess, ws, first, "resume", now)
	if err != nil || !resume.TUI || !resume.Resume || resume.Prompt != "" || resume.Dir != ws.Worktree || !slices.Equal(resume.Argv[:3], []string{"codex", "resume", first.ConversationID}) {
		t.Fatal("resume did not bind the exact native thread and worktree")
	}
	first.SessionUID = "replacement-session-uid"
	if _, err := BuildResume(s, sess, ws, first, "resume", now); err == nil {
		t.Fatal("resume crossed the Session UID")
	}
}

func TestManagedCodexThreadReceiptAndProtocolRefusal(t *testing.T) {
	for _, kind := range []string{"valid", "truncated", "second-thread", "failure", "missing-thread", "unknown", "durability"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "launch.json")
			l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid", Prompt: "initial prompt"}
			if kind == "durability" {
				path = filepath.Join(dir, "blocked", "launch.json")
				writeFile(t, filepath.Join(dir, "blocked"), "not a directory")
			}
			var stream codexStream
			var result streamResult
			var out bytes.Buffer
			thread := []byte(`{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`)
			if kind == "missing-thread" || kind == "unknown" {
				thread = []byte(`{"type":"turn.started"}`)
			}
			err := stream.line(thread, path, &l, &out, &result)
			if kind == "durability" || kind == "missing-thread" || kind == "unknown" {
				if err == nil || l.NativeThreadConfirmed {
					t.Fatal("uncertain thread acquired durable confirmation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var saved Launch
			if readJSONFile(path, &saved) != nil || !saved.NativeThreadConfirmed || saved.ConversationID != l.ConversationID {
				t.Fatal("thread output acknowledged before durable native identity")
			}
			if kind == "truncated" {
				_, _, err := codexJSONLine([]byte(`{"type":"turn.completed"}`), true)
				if err == nil {
					t.Fatal("truncated JSONL accepted")
				}
				return
			}
			if kind == "second-thread" {
				if stream.line(thread, path, &l, &out, &result) == nil || l.NativeThreadConfirmed {
					t.Fatal("second native thread retained resume authority")
				}
				return
			}
			if err := stream.line([]byte(`{"type":"turn.started"}`), path, &l, &out, &result); err != nil {
				t.Fatal(err)
			}
			end := []byte(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}`)
			if kind == "failure" {
				end = []byte(`{"type":"turn.failed","error":{"message":"synthetic provider error"}}`)
			}
			if err := stream.line(end, path, &l, &out, &result); err != nil {
				t.Fatal(err)
			}
			if !result.Seen || result.IsError != (kind == "failure") || !stream.terminal {
				t.Fatal("native terminal result changed")
			}
		})
	}
}

func TestManagedCodexAmbiguousTmuxStartRetainsNoReplayFence(t *testing.T) {
	s, sess, ws, now := managedCodexFixture(t)
	l, err := BuildLaunch(s, sess, ws, "first", now)
	if err != nil {
		t.Fatal(err)
	}
	broken := &fakeRunner{handle: func(c Cmd) (Result, error) { return Result{}, &CmdError{Name: "tmux", Sub: c.Args[0], ExitCode: 1} }}
	if StartAgent(context.Background(), broken, s, l, "agentd") == nil {
		t.Fatal("ambiguous tmux response accepted")
	}
	var saved Launch
	if readJSONFile(s.statePath(launchFile), &saved) != nil || saved.NativeThreadConfirmed {
		t.Fatal("ambiguous startup lost its unconfirmed durable fence")
	}
	if _, err := BuildResume(s, sess, ws, saved, "next", now); err == nil {
		t.Fatal("ambiguous startup acquired resume or prompt replay authority")
	}
	before, _ := os.ReadFile(s.statePath(launchFile))
	if StartAgent(context.Background(), broken, s, l, "agentd") == nil {
		t.Fatal("second initial native prompt admitted")
	}
	after, _ := os.ReadFile(s.statePath(launchFile))
	if !bytes.Equal(before, after) {
		t.Fatal("second start replaced the existing native fence")
	}
}

func TestManagedCodexRunAgentParsesOnlyNativeStdoutAndFailsClosed(t *testing.T) {
	thread := `{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}`
	turn := `{"type":"turn.started"}`
	complete := `{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2,"cached_input_tokens":0}}`
	for _, kind := range []string{"valid", "stderr-thread", "truncated", "missing-terminal", "provider-failure", "second-thread", "version"} {
		t.Run(kind, func(t *testing.T) {
			s := testSettings(t, t.TempDir())
			if err := os.MkdirAll(s.WorkDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			body := "printf '%s\\n' '" + thread + "' '" + turn + "' '" + complete + "'\n"
			switch kind {
			case "stderr-thread":
				body = "printf '%s\\n' '" + thread + "' >&2\nprintf '%s\\n' '" + turn + "' '" + complete + "'\n"
			case "truncated":
				body = "printf '%s\\n' '" + thread + "' '" + turn + "'\nprintf '%s' '" + complete + "'\n"
			case "missing-terminal":
				body = "printf '%s\\n' '" + thread + "' '" + turn + "'\n"
			case "provider-failure":
				body = "printf '%s\\n' '" + thread + "' '" + turn + "' '{\"type\":\"turn.failed\",\"error\":{\"message\":\"synthetic\"}}'\n"
			case "second-thread":
				body = "printf '%s\\n' '" + thread + "' '" + thread + "'\n"
			}
			version := managedCodexVersion
			if kind == "version" {
				version = "0.159.1"
			}
			cli := fakeCLI(t, dir, `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli `+version+`'; exit 0; fi
read -r prompt
[ "$prompt" = "synthetic task" ] || exit 9
`+body)
			l := Launch{Provider: protocol.AgentCodex, Session: "task", SessionUID: "session-uid", Argv: []string{cli, "exec", "--json"}, Dir: dir, Prompt: "synthetic task\n", LogPath: s.LogPath("task"), EventsPath: s.statePath(eventsFile), BootID: "first"}
			path := writeLaunch(t, s, l)
			pane := &syncBuffer{}
			code := RunAgent(path, pane, make(chan os.Signal), 20*time.Millisecond)
			if (code == 0) != (kind == "valid") {
				t.Fatal("native process result did not fail closed", kind, code)
			}
			var saved Launch
			if readJSONFile(path, &saved) != nil {
				t.Fatal("native launch receipt disappeared")
			}
			wantConfirmed := kind != "stderr-thread" && kind != "second-thread" && kind != "version"
			if saved.NativeThreadConfirmed != wantConfirmed {
				t.Fatal("stderr or contradictory native identity acquired resume authority")
			}
			if kind == "stderr-thread" {
				events, _ := os.ReadFile(l.EventsPath)
				if bytes.Contains(events, []byte(thread)) {
					t.Fatal("stderr was parsed as native stdout events")
				}
			}
		})
	}
}
