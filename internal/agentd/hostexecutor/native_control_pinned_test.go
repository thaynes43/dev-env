//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Opt-in actual pinned daemon, ONE scripted loopback Responses exchange, no
// provider/model/auth/owner connection. All history is read through native RPC.
// The synthetic Observation tests native lifetime, not original-guard authority.
func TestPinnedNativeTurnSurvivesControlDisconnectAndHistoryReconnect(t *testing.T) {
	binary := os.Getenv("DEV_ENV_PINNED_NATIVE_BINARY")
	if binary == "" {
		t.Skip("set DEV_ENV_PINNED_NATIVE_BINARY to verified0.160.1 for bounded local native protocol proof")
	}
	version, err := exec.Command(binary, "--version").Output()
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli "+PinnedNativeVersion {
		t.Fatal("actual binary is not pinned", string(version), err)
	}
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	cancelled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.Error(w, "unexpected endpoint", http.StatusBadRequest)
			return
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "gpt-6-luna" {
			http.Error(w, "not pinned Luna", http.StatusBadRequest)
			return
		}
		if calls.Add(1) != 1 {
			http.Error(w, "no replay", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fixture\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		w.(http.Flusher).Flush()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			cancelled <- struct{}{}
			return
		}
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "PINNED_NATIVE_RECONNECT_FIXTURE"}}}
		for _, event := range []map[string]any{{"type": "response.output_item.done", "output_index": 0, "item": item}, {"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed", "model": "gpt-6-luna", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			raw, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], raw)
			w.(http.Flusher).Flush()
		}
	}))
	defer provider.Close()
	home := t.TempDir()
	codexHome := filepath.Join(home, ".codex")
	if err = os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("model = \"gpt-6-luna\"\nmodel_provider = \"inert_fixture\"\napproval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\n[analytics]\nenabled = false\n[model_providers.inert_fixture]\nname = \"Loopback inert fixture\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\nsupports_websockets = false\n", provider.URL+"/v1")
	if err = os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	// Unix path is short and isolated; no shared daemon enrollment/client.
	socket := filepath.Join(home, "n.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := nativeCommand(Config{NativeBinary: binary, Home: home, SocketPath: socket})
	for _, arg := range cmd.Args {
		if arg == "--remote-control" {
			t.Fatal("unapproved automatic remote enrollment")
		}
	}
	cmd.Env = append(cmd.Env, "RUST_LOG=off")
	var diagnostic strings.Builder
	cmd.Stderr = &diagnostic
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	root, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err = probeNative(ctx, socket, root, PinnedNativeVersion); err != nil {
		t.Fatalf("pinned daemon not ready: %v %s", err, diagnostic.String())
	}
	b := Binding{TaskUID: "inert-native", HostID: "inert-host", PodUID: "inert-pod", Epoch: 1, Deadline: time.Now().Add(15 * time.Second)}
	p := Observation{Binding: b, Stage: "Ready", Root: root, NativeVersion: PinnedNativeVersion}
	r := apiv1.NativeControlRequest{Binding: apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, HostID: b.HostID, PodUID: b.PodUID, Epoch: b.Epoch, Deadline: b.Deadline}, ID: "inert-owner", Operation: "thread/start"}
	consume := func(context.Context) error { return nil } // Real human/CAS authority is tested separately.
	result, err := ControlNative(ctx, socket, p, r, consume)
	if err != nil {
		t.Fatalf("thread start %v %s", err, diagnostic.String())
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &thread) != nil || thread.Thread.ID == "" {
		t.Fatal("missing thread")
	}
	r.Operation = "turn/start"
	r.ThreadID = thread.Thread.ID
	r.Text = "Synthetic provider fixture; no real model or tools."
	if _, err = ControlNative(ctx, socket, p, r, consume); err != nil {
		t.Fatalf("turn start %v %s", err, diagnostic.String())
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("loopback response never requested: %s", diagnostic.String())
	}
	// Both mutation client sockets have closed. A NEW passive connection sees
	// this original native turn before the scripted provider is released.
	r.Operation = "thread/read"
	r.Text = ""
	if _, err = ControlNative(ctx, socket, p, r, nil); err != nil {
		t.Fatalf("active turn disconnected: %v %s", err, diagnostic.String())
	}
	select {
	case <-cancelled:
		t.Fatal("native cancelled provider when control client disconnected")
	default:
	}
	close(release)
	for i := 0; i < 20; i++ {
		result, err = ControlNative(ctx, socket, p, r, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(result), "PINNED_NATIVE_RECONNECT_FIXTURE") && strings.Contains(string(result), `"completed"`) {
			if calls.Load() != 1 {
				t.Fatal("synthetic exchange replayed")
			}
			r.Operation = "thread/resume"
			resumed, resumeErr := ControlNative(ctx, socket, p, r, consume)
			if resumeErr != nil || !strings.Contains(string(resumed), "PINNED_NATIVE_RECONNECT_FIXTURE") || calls.Load() != 1 {
				t.Fatalf("same native thread resume/history failed: %v %s", resumeErr, resumed)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("history deadline")
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("completed native history unavailable: %s; %s", result, diagnostic.String())
}
