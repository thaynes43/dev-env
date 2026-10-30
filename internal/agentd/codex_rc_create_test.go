package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestCodexRCStartPinnedSchema(t *testing.T) {
	for name, digest := range map[string]string{
		"ThreadStartParams":   "80a40a7fac15b4bf70efb7f893fb353acc0a0d30c68f54aee4f01923deca85de",
		"ThreadStartResponse": "92f5ddc37717b922de1dc0b0405e9a6d463f370554e2091610c6e595bc2616d4",
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", "codex_rc_create", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if hex.EncodeToString(sum[:]) != digest {
			t.Fatal("pinned schema changed", name)
		}
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if json.Unmarshal(raw, &schema) != nil || schema.Properties["cwd"] == nil {
			t.Fatal("cwd missing", name)
		}
		if name == "ThreadStartParams" && (len(schema.Required) != 0 || schema.Properties["projectId"] == nil || schema.Properties["model"] == nil) {
			t.Fatal("start no longer permits cwd/model without project ID")
		}
	}
}

// Only an inert local server is used. No native CLI, real host, credential,
// provider request, remote enrollment or private provider stream is involved.
func TestCodexRCStartExistingDaemonProtocol(t *testing.T) {
	for _, mode := range []string{"ok", "wrong-peer", "wrong-version", "wrong-installation", "wrong-environment", "disconnected", "unauthorized", "nil-authorizer", "cancel-before", "cancel-after-authorize", "lost-ack", "wrong-cwd", "wrong-model", "bad-thread", "server-request", "null-result", "rpc-error", "oversize", "missing-environments", "null-environments", "remote-environment"} {
		t.Run(mode, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "rc-create-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = os.RemoveAll(home) }()
			dir := filepath.Join(home, "app-server-control")
			if err = os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
			if err != nil {
				t.Fatal(err)
			}
			var starts, authorized atomic.Int32
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = ws.Close() }()
				_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
				initialized := false
				for {
					var req struct {
						ID     *int           `json:"id"`
						Method string         `json:"method"`
						Params map[string]any `json:"params"`
					}
					if ws.ReadJSON(&req) != nil {
						return
					}
					var result any
					switch req.Method {
					case "initialize":
						version := managedCodexVersion
						if mode == "wrong-version" {
							version = "0.0.0"
						}
						result = map[string]string{"userAgent": "codex/" + version}
					case "initialized":
						initialized = true
						continue
					case "remoteControl/status/read":
						if !initialized {
							t.Error("missing initialized notification")
						}
						status := map[string]string{"status": "connected", "installationId": "installation", "environmentId": "environment"}
						if mode == "wrong-installation" {
							status["installationId"] = "other"
						}
						if mode == "wrong-environment" {
							status["environmentId"] = "other"
						}
						if mode == "disconnected" {
							status["status"] = "connecting"
						}
						result = status
					case "thread/start":
						starts.Add(1)
						if authorized.Load() != 1 || len(req.Params) != 3 || req.Params["cwd"] != "/accepted/repo" || req.Params["model"] != "gpt-6.1-sol" {
							t.Error("incorrect or unauthorized mutation", req.Params)
						}
						envs, ok := req.Params["environments"].([]any)
						if !ok || len(envs) != 0 {
							t.Error("environment selection not explicitly disabled")
						}
						if mode == "lost-ack" {
							return
						}
						if mode == "server-request" {
							_ = ws.WriteJSON(map[string]any{"id": 99, "method": "item/commandExecution/requestApproval"})
						}
						if mode == "rpc-error" {
							_ = ws.WriteJSON(map[string]any{"id": req.ID, "error": map[string]any{"code": -1, "message": "PRIVATE_NATIVE_TEXT"}})
							return
						}
						if mode == "oversize" {
							_ = ws.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", codexHostRPCBound+1)))
							return
						}
						cwd, model, id := "/accepted/repo", "gpt-6.1-sol", "12345678-1234-1234-1234-123456789abc"
						if mode == "wrong-cwd" {
							cwd = "/other/repo"
						}
						if mode == "wrong-model" {
							model = "gpt-6-luna"
						}
						if mode == "bad-thread" {
							id = "../private"
						}
						thread := map[string]any{"id": id, "environments": []any{}}
						if mode == "missing-environments" {
							delete(thread, "environments")
						}
						if mode == "null-environments" {
							thread["environments"] = nil
						}
						if mode == "remote-environment" {
							thread["environments"] = []any{map[string]string{"environmentId": "other", "cwd": cwd}}
						}
						result = map[string]any{"thread": thread, "cwd": cwd, "model": model}
						if mode == "null-result" {
							result = nil
						}
					default:
						t.Error("unexpected operation", req.Method)
						return
					}
					if ws.WriteJSON(map[string]any{"id": req.ID, "result": result, "error": nil}) != nil {
						return
					}
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer func() { _ = server.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if mode == "cancel-before" {
				cancel()
			}
			authorize := func(context.Context) error {
				authorized.Add(1)
				if mode == "unauthorized" {
					return errors.New("denied")
				}
				if mode == "cancel-after-authorize" {
					cancel()
				}
				return nil
			}
			if mode == "nil-authorizer" {
				authorize = nil
			}
			pid := os.Getpid()
			if mode == "wrong-peer" {
				pid = os.Getppid()
			}
			receipt, err := startCodexRCThread(ctx, Settings{CodexHome: home}, pid, codexRCThreadSelection{InstallationID: "installation", EnvironmentID: "environment", Cwd: "/accepted/repo", Model: "gpt-6.1-sol"}, authorize)
			if (err == nil) != (mode == "ok") {
				t.Fatal(mode, err)
			}
			if mode == "ok" && receipt.ThreadID != "12345678-1234-1234-1234-123456789abc" {
				t.Fatal("missing fresh receipt")
			}
			if err != nil && (receipt != (codexRCThreadReceipt{}) || strings.Contains(err.Error(), "PRIVATE_NATIVE_TEXT")) {
				t.Fatal("untrusted result exported")
			}
			wantStarts := int32(0)
			switch mode {
			case "ok", "lost-ack", "wrong-cwd", "wrong-model", "bad-thread", "server-request", "null-result", "rpc-error", "oversize", "missing-environments", "null-environments", "remote-environment":
				wantStarts = 1
			}
			if starts.Load() != wantStarts {
				t.Fatal("mutation count/replay", starts.Load(), wantStarts)
			}
		})
	}
}

func TestCodexRCSelectionRefusals(t *testing.T) {
	base := codexRCThreadSelection{InstallationID: "i", EnvironmentID: "e", Cwd: "/accepted/repo", Model: "gpt-6.1-sol"}
	for _, cwd := range []string{"", "/", "relative", "/accepted/../elsewhere", "/accepted/repo/", "/accepted/\nrepo", "/accepted/\x00repo", string([]byte{'/', 0xff}), "/" + strings.Repeat("x", 4096)} {
		s := base
		s.Cwd = cwd
		if s.valid() {
			t.Fatal("invalid cwd accepted")
		}
	}
	for _, mode := range []string{"host", "environment", "model"} {
		s := base
		switch mode {
		case "host":
			s.InstallationID = ""
		case "environment":
			s.EnvironmentID = ""
		case "model":
			s.Model = "sol"
		}
		if s.valid() {
			t.Fatal(mode)
		}
	}
}
