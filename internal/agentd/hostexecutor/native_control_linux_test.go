//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Actual Unix credentials/WebSocket protocol, inert local server only: no CLI,
// credential, provider, model, shared daemon or real native history is touched.
func TestOwnerNativeProtocolAndLostAcknowledgement(t *testing.T) {
	for _, scenario := range []string{"start", "resume", "history", "turn", "interrupt", "lost-ack", "wrong-thread", "wrong-peer", "wrong-version", "cancel", "server-request", "oversize", "rpc-rejected", "rpc-timeout", "original-deadline", "cancel-after-dispatch"} {
		t.Run(scenario, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "n.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			dispatched := make(chan struct{})
			srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer func() { _ = ws.Close() }()
				_ = ws.SetReadDeadline(time.Now().Add(time.Second))
				var msg struct {
					ID     int                        `json:"id"`
					Method string                     `json:"method"`
					Params map[string]json.RawMessage `json:"params"`
				}
				if ws.ReadJSON(&msg) != nil {
					return
				}
				version := PinnedNativeVersion
				if scenario == "wrong-version" {
					version = "0.0.0"
				}
				_ = ws.WriteJSON(map[string]any{"id": msg.ID, "result": map[string]string{"userAgent": "codex/" + version}})
				if ws.ReadJSON(&msg) != nil || msg.Method != "initialized" {
					return
				}
				if ws.ReadJSON(&msg) != nil {
					return
				}
				calls.Add(1)
				close(dispatched)
				if msg.Method == "thread/start" || msg.Method == "thread/resume" || msg.Method == "turn/start" {
					if string(msg.Params["model"]) != `"gpt-6-luna"` {
						t.Error("model not pinned")
					}
				}
				if msg.Method == "thread/read" && string(msg.Params["includeTurns"]) != "true" {
					t.Error("history omitted")
				}
				if scenario == "lost-ack" || scenario == "cancel" {
					return
				}
				if scenario == "rpc-rejected" {
					_ = ws.WriteJSON(map[string]any{"id": msg.ID, "error": map[string]any{"code": -32000, "message": "NEVER_EXPORT_NATIVE_ERROR"}})
					return
				}
				if scenario == "rpc-timeout" || scenario == "original-deadline" || scenario == "cancel-after-dispatch" {
					_, _, _ = ws.ReadMessage()
					return
				}

				if scenario == "server-request" {
					_ = ws.WriteJSON(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval", "params": map[string]any{}})
					return
				}
				if scenario == "oversize" {
					_ = ws.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", NativeControlLimit+1)))
					return
				}
				_ = ws.WriteJSON(map[string]any{"method": "turn/started", "params": map[string]any{}})
				thread := "thread-a"
				if scenario == "wrong-thread" {
					thread = "unrelated"
				}
				result := map[string]any{"thread": map[string]any{"id": thread, "turns": []any{map[string]string{"id": "saved-turn", "status": "completed"}}}, "turn": map[string]string{"id": "turn-a", "status": "inProgress"}}
				_ = ws.WriteJSON(map[string]any{"id": msg.ID, "result": result, "error": nil})
			})}
			defer func() { _ = srv.Close() }()
			go func() { _ = srv.Serve(listener) }()
			root, _ := identity(os.Getpid())
			if scenario == "wrong-peer" {
				root, _ = identity(os.Getppid())
			}
			b := Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().Add(time.Minute)}
			if scenario == "original-deadline" {
				b.Deadline = time.Now().Add(100 * time.Millisecond)
			}
			p := Observation{Stage: "Ready", Binding: b, Root: root, NativeVersion: PinnedNativeVersion}
			req := apiv1.NativeControlRequest{Binding: apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, HostID: b.HostID, PodUID: b.PodUID, Epoch: b.Epoch, Deadline: b.Deadline}, ID: "once", Operation: "thread/start"}
			switch scenario {
			case "resume":
				req.Operation = "thread/resume"
				req.ThreadID = "thread-a"
			case "history", "wrong-thread":
				req.Operation = "thread/read"
				req.ThreadID = "thread-a"
			case "turn", "lost-ack", "cancel":
				req.Operation = "turn/start"
				req.ThreadID = "thread-a"
				req.Text = "inert protocol payload"
			case "interrupt":
				req.Operation = "turn/interrupt"
				req.ThreadID = "thread-a"
				req.TurnID = "turn-a"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if scenario == "cancel" {
				cancel()
			}
			if scenario == "rpc-timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
			}
			if scenario == "cancel-after-dispatch" {
				go func() { <-dispatched; cancel() }()
			}

			result, err := ControlNative(ctx, path, p, req, func(context.Context) error { return nil })
			want := scenario == "start" || scenario == "resume" || scenario == "history" || scenario == "turn" || scenario == "interrupt"
			if (err == nil) != want {
				t.Fatalf("result %s err %v", result, err)
			}
			if scenario == "rpc-rejected" && (NativeControlRefusalExit(err) != 86 || strings.Contains(err.Error(), "NEVER_EXPORT_NATIVE_ERROR")) {
				t.Fatal("native error exported or misclassified", err)
			}
			if scenario == "rpc-timeout" || scenario == "original-deadline" {
				if NativeControlRefusalExit(err) != 85 {
					t.Fatal("native deadline not classified", err)
				}
			}
			if calls.Load() > 1 {
				t.Fatal("native mutation replayed")
			}
			if want && calls.Load() != 1 {
				t.Fatal("missing native RPC")
			}
		})
	}
}
