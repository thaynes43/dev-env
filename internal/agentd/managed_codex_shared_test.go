package agentd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func TestManagedCodexSharedRunAgentFinalizesOnceBeforeSpawn(t *testing.T) {
	g := newGitFixture(t, "demo")
	s, runner := g.settings(t)
	s, sess := sharedSettings(t, s, "task-native")
	sess.Agent, sess.Model, sess.SessionUID = protocol.AgentCodex, "gpt-6.1-sol", sess.Workspace.SessionUID
	w, err := acquireWorkspaceWriter(context.Background(), runner, s, sess, rescueNow)
	if err != nil {
		t.Fatal(err)
	}
	defer w.unlock()
	s.writer = w
	if err := os.MkdirAll(s.WorktreePath(sess.Name), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(s.StateDir, "synthetic-native-spawns")
	cli := fakeCLI(t, t.TempDir(), `if [ "$1" = "--version" ]; then printf '%s\n' 'codex-cli 0.160.1'; exit 0; fi
printf '%s\n' started >> "$SYNTHETIC_NATIVE_SPAWN_RECEIPT"
printf '%s\n' '{"type":"thread.started","thread_id":"12345678-1234-1234-1234-123456789abc"}' '{"type":"turn.started"}' '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1,"cached_input_tokens":0}}'
`)
	l := Launch{Provider: protocol.AgentCodex, Session: sess.Name, SessionUID: sess.SessionUID, Argv: []string{cli, "exec", "--json"},
		Dir: s.WorktreePath(sess.Name), Prompt: "synthetic task", Env: []string{"SYNTHETIC_NATIVE_SPAWN_RECEIPT=" + marker},
		LogPath: s.LogPath(sess.Name), EventsPath: s.statePath(eventsFile), BootID: "first"}
	if err := admitWorkspaceLaunch(context.Background(), s, sess, &l); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", s.Home)
	t.Setenv("AGENTD_WORKSPACE_ID", s.WorkspaceID)
	t.Setenv(protocol.PodUIDEnv, s.PodUID)
	t.Setenv(protocol.SessionEnv, string(data))
	t.Setenv(protocol.SessionFileEnv, "")
	path := writeLaunch(t, s, l)
	if code := RunAgent(path, &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code != 0 {
		t.Fatal("first exact shared native writer was rejected before spawn", code)
	}
	var owner taskOwner
	if readWorkspaceJSON(s.ownerPath(sess.Name), &owner) != nil || !owner.Launched || owner.Generation != w.owner.Generation || owner.PodUID != s.PodUID {
		t.Fatal("final spawn did not retain the exact launched writer receipt")
	}
	if code := RunAgent(path, &syncBuffer{}, make(chan os.Signal), 20*time.Millisecond); code == 0 {
		t.Fatal("the same first-launch reservation allowed another native spawn")
	}
	spawns, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(spawns), "started\n") != 1 {
		t.Fatal("shared native invocation did not finalize and spawn exactly once")
	}
}
