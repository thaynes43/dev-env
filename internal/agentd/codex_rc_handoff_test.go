package agentd

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func TestCodexRCThreadAndHandoffExistingDaemonFixture(t *testing.T) {
	const thread = "11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"ok", "lost-thread-ack", "lost-turn-ack", "before-turn-drift", "wrong-peer", "bad-turn"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, d, deps, _ := rcControlFixture(t)
			socketHome, err := os.MkdirTemp("/tmp", "rct-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(socketHome) }()
			s.CodexHome = socketHome
			history := filepath.Join(socketHome, "existing-history-marker")
			if err := os.WriteFile(history, []byte("preserved existing history"), 0600); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(socketHome, "new-daemon-forbidden")
			s.CodexBin = filepath.Join(socketHome, "codex-forbidden")
			if err := os.WriteFile(s.CodexBin, []byte("#!/bin/sh\ntouch "+sentinel+"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(socketHome, "app-server-control")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
			if err != nil {
				t.Fatal(err)
			}
			d.Target.PID = os.Getpid()
			if mode == "wrong-peer" {
				d.Target.PID++
			}
			data, _ := json.Marshal(d.Target)
			if err := os.WriteFile(filepath.Join(s.ConfigDir, CodexRCTargetFile), data, 0600); err != nil {
				t.Fatal(err)
			}
			var starts, turns atomic.Int32
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = ws.Close() }()
				_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
				for {
					var req struct {
						ID     *int                       `json:"id"`
						Method string                     `json:"method"`
						Params map[string]json.RawMessage `json:"params"`
					}
					if ws.ReadJSON(&req) != nil {
						return
					}
					var result any
					switch req.Method {
					case "initialize":
						result = map[string]string{"userAgent": "codex/" + managedCodexVersion}
					case "initialized":
						continue
					case "remoteControl/status/read":
						result = map[string]string{"status": "connected", "installationId": "installation", "environmentId": "environment"}
					case "thread/start":
						starts.Add(1)
						if len(req.Params) != 3 || string(req.Params["environments"]) != "[]" || string(req.Params["cwd"]) != string(mustJSON(d.Target.Root)) || string(req.Params["model"]) != string(mustJSON(d.Request.Model)) {
							t.Error("thread parameter drift", req.Params)
						}
						if mode == "lost-thread-ack" {
							return
						}
						result = map[string]any{"thread": map[string]any{"id": thread, "environments": []any{}}, "cwd": d.Target.Root, "model": d.Request.Model}
					case "turn/start":
						turns.Add(1)
						if starts.Load() != 1 || len(req.Params) != 4 || string(req.Params["threadId"]) != string(mustJSON(thread)) || string(req.Params["model"]) != string(mustJSON(d.Request.Model)) || string(req.Params["effort"]) != `"medium"` {
							t.Error("turn parameter drift", req.Params)
						}
						var input []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						}
						if json.Unmarshal(req.Params["input"], &input) != nil || len(input) != 1 || input[0].Type != "text" || input[0].Text != d.Request.Handoff {
							t.Error("handoff drift")
						}
						if mode == "lost-turn-ack" {
							return
						}
						id := "1"
						if mode == "bad-turn" {
							id = ""
						}
						result = map[string]any{"turn": map[string]string{"id": id}}
					default:
						t.Error("unexpected RPC", req.Method)
						return
					}
					if ws.WriteJSON(map[string]any{"id": req.ID, "result": result, "error": nil}) != nil {
						return
					}
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer func() { _ = server.Close() }()
			checks := 0
			deps.process = func(Settings, codexHostFS) (codexHostProcess, error) {
				checks++
				if mode == "before-turn-drift" && checks >= 3 {
					return codexHostProcess{}, errCodexHostUnknown
				}
				return codexHostProcess{State: "Live", PID: d.Target.PID, Identity: codexHostIdentity{BootID: "boot", StartTicks: 55}}, nil
			}
			deps.start = startCodexRCThread
			rec, err := codexRCControl(context.Background(), s, sess, "start", d, deps)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ok" {
				if !rec.Acknowledged() || rec.ThreadID != thread || rec.TurnID != "1" {
					t.Fatal(rec)
				}
			} else if rec.State == "confirmed" {
				t.Fatal("false success", rec)
			}
			wantStarts, wantTurns := int32(1), int32(1)
			if mode == "wrong-peer" {
				wantStarts, wantTurns = 0, 0
			}
			if mode == "lost-thread-ack" || mode == "before-turn-drift" {
				wantTurns = 0
			}
			if starts.Load() != wantStarts || turns.Load() != wantTurns {
				t.Fatal(mode, starts.Load(), turns.Load())
			}
			// A restarted ctl process cannot replay even when the previous reply was lost.
			_, _ = codexRCControl(context.Background(), s, sess, "start", d, deps)
			if starts.Load() != wantStarts || turns.Load() != wantTurns {
				t.Fatal("duplicate RPC")
			}
			if exists(sentinel) || string(mustRead(t, history)) != "preserved existing history" {
				t.Fatal("daemon launch or history change")
			}
		})
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestCodexRCTurnPinnedSchema(t *testing.T) {
	for name, digest := range map[string]string{"TurnStartParams": "07771223642e1b61bd9aac0069fc0f98143a1c047724ca02c7ceb13653442738", "TurnStartResponse": "12c5151421bb061297c9790ff795a44aa4b9bdb91fbb68012dcf0a08cf4198b1"} {
		data, err := os.ReadFile(filepath.Join("testdata", "codex_rc_create", name+".json"))
		if err != nil || projectcatalog.Digest(data) != digest {
			t.Fatal("pinned schema changed", name, err)
		}
	}
}
