//go:build linux

package hostexecutor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Opt-in regression: real pinned native, frozen public config/rules,
// synthetic access-only auth, loopback backend; NO turn or real provider call.
func TestPinnedNativeColdCatalogFallbackWithinOwnerBound(t *testing.T) {
	binary := os.Getenv("DEV_ENV_PINNED_NATIVE_BINARY")
	input := os.Getenv("DEV_ENV_FROZEN_INPUTS")
	if binary == "" || input == "" {
		t.Skip("explicit pinned binary and public frozen inputs required")
	}
	raw, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	var cm struct {
		Data map[string]string `json:"data"`
	}
	if json.Unmarshal(raw, &cm) != nil {
		t.Fatal("inputs")
	}
	var frozen struct {
		CodexConfig string `json:"codexConfig"`
		Common      struct {
			Header struct {
				Text string `json:"text"`
			} `json:"header"`
			Common struct {
				Text string `json:"text"`
			} `json:"common"`
		} `json:"commonRules"`
	}
	if json.Unmarshal([]byte(cm.Data["input.json"]), &frozen) != nil {
		t.Fatal("frozen")
	}
	var mu sync.Mutex
	var routes []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		routes = append(routes, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models") {
			select {
			case <-time.After(6 * time.Second):
			case <-r.Context().Done():
				return
			}
		}
		http.Error(w, "synthetic backend: no inference", 404)
	}))
	defer backend.Close()
	home := t.TempDir()
	ch := filepath.Join(home, ".codex")
	if e = os.MkdirAll(filepath.Join(ch, "app-server-daemon"), 0700); e != nil {
		t.Fatal(e)
	}
	// Only network isolation overrides are added; original public configuration
	// and common-rule bytes are retained unchanged. No real credential is copied.
	cfg := "chatgpt_base_url = " + `"` + backend.URL + "/backend-api/" + `"` + "\nopenai_base_url = " + `"` + backend.URL + "/v1" + `"` + "\n" + frozen.CodexConfig
	for name, data := range map[string]string{"config.toml": cfg, "AGENTS.md": frozen.Common.Header.Text + "\n" + frozen.Common.Common.Text, "app-server-daemon/settings.json": "{\"updater\":{\"autoUpdateEnabled\":false}}\n"} {
		if e = os.WriteFile(filepath.Join(ch, name), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "synthetic-account", "chatgpt_plan_type": "pro"}})
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"synthetic"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic-signature"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "tokens": map[string]string{"id_token": jwt, "access_token": jwt, "refresh_token": "", "account_id": "synthetic-account"}, "last_refresh": time.Now().UTC()})
	if e = os.WriteFile(filepath.Join(ch, "auth.json"), auth, 0600); e != nil {
		t.Fatal(e)
	}
	socket := filepath.Join(home, "n.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := nativeCommand(Config{NativeBinary: binary, Home: home, SocketPath: socket})
	diag, e := os.Create(filepath.Join(home, "native.log"))
	if e != nil {
		t.Fatal(e)
	}
	defer diag.Close()
	cmd.Stderr = diag
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		mu.Lock()
		defer mu.Unlock()
		t.Logf("loopback routes %v", routes)
		for _, r := range routes {
			if strings.Contains(r, "/responses") {
				t.Error("unexpected inference endpoint")
			}
		}
	}()
	root, e := identity(cmd.Process.Pid)
	if e != nil {
		t.Fatal(e)
	}
	if e = probeNative(ctx, socket, root, PinnedNativeVersion); e != nil {
		t.Fatal(e)
	}
	b := Binding{TaskUID: "frozen-diagnostic", HostID: "frozen-host", PodUID: "frozen-pod", Epoch: 1, Deadline: time.Now().Add(10 * time.Second)}
	obs := Observation{Binding: b, Stage: "Ready", Root: root, NativeVersion: PinnedNativeVersion}
	req := apiv1.NativeControlRequest{Binding: apiv1.TaskBudgetBinding{TaskUID: b.TaskUID, HostID: b.HostID, PodUID: b.PodUID, Epoch: 1, Deadline: b.Deadline}, ID: "frozen-thread-once", Operation: "thread/start"}
	bounded, stop := context.WithTimeout(ctx, apiv1.NativeControlExecutionTimeout)
	defer stop()
	started := time.Now()
	// Realistic bounded status/proof work precedes initialize and CAS.
	time.Sleep(400 * time.Millisecond)
	consumed := 0
	result, err := ControlNative(bounded, socket, obs, req, func(context.Context) error { consumed++; time.Sleep(300 * time.Millisecond); return nil })
	t.Logf("real frozen thread/start duration=%v consumed=%d resultBytes=%d error=%v context=%v", time.Since(started), consumed, len(result), err, bounded.Err())
	if err != nil {
		t.Fatal(err)
	}
	var returned struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if consumed != 1 || json.Unmarshal(result, &returned) != nil || returned.Thread.ID == "" {
		t.Fatal("missing once-only native thread acknowledgment")
	}
	if time.Since(started) < 5*time.Second {
		t.Fatal("cold catalog fallback was not exercised")
	}
}
