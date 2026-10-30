package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thaynes43/dev-env/internal/agentd"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Exercise the actual boot collector and heartbeat storage together: replacing
// status.agent must not erase history that private_history reads during boot.
func TestHeartbeatBootPreservesPriorConversationAndTask(t *testing.T) {
	f := newFixture(t)
	const name = "haynes-ops-1006-100000"
	tok := f.sessionPod(name, "full", 0)
	path := protocol.HeartbeatPath(name)
	prior := status(name)
	if w := f.do(http.MethodPost, path, tok, prior); w.Code != http.StatusNoContent {
		t.Fatalf("prior heartbeat: %d %s", w.Code, w.Body.String())
	}
	want := f.session(name).Status.Agent.DeepCopy()
	s := agentd.Settings{StateDir: t.TempDir()}
	for file, value := range map[string]any{
		"launch.json":      agentd.Launch{BootID: prior.BootID, ConversationID: prior.Agent.ConversationID},
		"task-result.json": prior.Agent.Task,
		"boot.json":        map[string]string{"bootId": "restarted", "boot": protocol.BootBooting, "currentStep": "preparing repo"},
	} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s.StateDir, file), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	boot := agentd.CollectStatus(context.Background(), nil, s, protocol.Session{Name: name, Agent: protocol.AgentClaude, Mode: protocol.ModeRemote}, f.now)
	if w := f.do(http.MethodPost, path, tok, boot); w.Code != http.StatusNoContent {
		t.Fatalf("boot heartbeat: %d %s", w.Code, w.Body.String())
	}
	got := f.session(name)
	a := got.Status.Agent
	if a == nil || a.Boot != protocol.BootBooting || a.Status != protocol.AgentPending || a.Message != "preparing repo" ||
		a.ConversationID != want.ConversationID || !reflect.DeepEqual(a.Task, want.Task) {
		t.Fatalf("boot heartbeat erased history or inferred agent state: %+v, want history %+v", a, want)
	}
	if rc := got.Status.RemoteControl; rc == nil || rc.State != protocol.RemoteStarting {
		t.Fatalf("boot remote state: %+v", rc)
	}
}
