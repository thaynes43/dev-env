//go:build linux

package agentd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/testsock"
)

// All child commands get an allowlisted environment: no KUBERNETES_* or
// KUBECONFIG, login projection, provider credential, user config or remote relay.
func pinnedCodexCommand(ctx context.Context, binary, home string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "CODEX_INTERNAL_APP_SERVER_REMOTE_CONTROL_DISABLED=1", "RUST_LOG=off"}
	return cmd
}

func pinnedCodexBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("DEV_ENV_PINNED_NATIVE_BINARY")
	if binary == "" {
		t.Skip("image CI supplies the complete pinned native package")
	}
	return binary
}

func TestPinnedCodexPreviousHostStarts(t *testing.T) {
	binary := pinnedCodexBinary(t)
	for _, version := range []string{previousPinnedCodexVersion, "0.168.0"} {
		t.Run(version, func(t *testing.T) {
			s, o := hostFixtureAt(t, testsock.ShortDir(t, "cu"))
			s.CodexBin = binary
			before := writeVersionedHostReceipt(t, s, "Stopped", version)
			// A stopped old package: no native process, real identifiers or auth.
			root := filepath.Join(s.CodexHome, "packages", "app-server-daemon")
			release := filepath.Join(root, "releases", version+"-x86_64-unknown-linux-musl")
			old := filepath.Join(release, "bin", "codex")
			writeHostFixture(t, old, "#!/bin/sh\n[ \"$1\" = --version ] || exit 1\nprintf 'codex-cli "+version+"\\n'\n")
			if err := os.Chmod(old, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(release, filepath.Join(root, "current")); err != nil {
				t.Fatal(err)
			}
			writeHostFixture(t, filepath.Join(s.CodexHome, "app-server-daemon", "settings.json"), `{"remoteControlEnabled":true,"updater":{"autoUpdateEnabled":false}}`)
			// Sentinels prove replacement leaves retained user data untouched.
			writeHostFixture(t, filepath.Join(s.CodexHome, "sessions", "fixture.jsonl"), "fixture-history\n")
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			updates, starts := 0, 0
			d := realCodexHostDeps()
			d.preflight = func(Settings) error { return nil }       // Mount layout is unit-tested.
			d.sync = func(Settings, time.Time) error { return nil } // No login or keeper.
			d.native = func(ctx context.Context, s Settings, bin string, args []string, _ io.Writer) ([]byte, error) {
				if strings.Join(args, " ") == "app-server daemon update --from-cli --yes" {
					updates++
					raw, _ := os.ReadFile(s.statePath("codex-host-start-intent.json"))
					if string(raw) != string(before) {
						t.Fatal("receipt changed before package update")
					}
				}
				if strings.Join(args, " ") == "remote-control start --json" {
					starts++
					// Real native lifecycle, explicit remote suppression: no enrollment.
					args = []string{"app-server", "daemon", "start"}
				}
				cmd := pinnedCodexCommand(ctx, bin, s.Home, args...)
				out, err := cmd.Output()
				if err != nil {
					t.Logf("native %s failed: %v", strings.Join(args, " "), err)
				}
				return out, err
			}
			d.remote = func(ctx context.Context, s Settings, _ bool, pid int) (codexHostRemote, error) {
				return probeCodexHostRemote(ctx, s, false, pid)
			}
			t.Cleanup(func() {
				if starts != 0 {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := pinnedCodexCommand(ctx, binary, s.Home, "app-server", "daemon", "stop").Run(); err != nil {
						t.Errorf("native cleanup: %v", err)
					}
				}
			})
			err := startCodexHost(ctx, s, o, d)
			if version != previousPinnedCodexVersion {
				if err == nil || starts != 0 || updates != 0 {
					t.Fatal("unknown version was not refused before native action")
				}
				raw, _ := os.ReadFile(s.statePath("codex-host-start-intent.json"))
				if string(raw) != string(before) {
					t.Fatal("refusal rewrote receipt")
				}
				return
			}
			if err != nil || starts != 1 || updates != 1 {
				t.Fatalf("previous host: starts=%d updates=%d err=%v", starts, updates, err)
			}
			saved, err := hostLifecycle(s)
			if err != nil || saved.CLI != managedCodexVersion || saved.Phase != "Confirmed" {
				t.Fatalf("new receipt: %+v %v", saved, err)
			}
			managed, err := codexHostManagedBin(s)
			if err != nil {
				t.Fatal(err)
			}
			out, err := pinnedCodexCommand(ctx, managed, s.Home, "--version").Output()
			if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+managedCodexVersion {
				t.Fatal("replacement is not the pinned package")
			}
			raw, _ := os.ReadFile(filepath.Join(s.CodexHome, "sessions", "fixture.jsonl"))
			if string(raw) != "fixture-history\n" {
				t.Fatal("history changed")
			}
			if _, err := os.Stat(filepath.Join(s.CodexHome, "auth.json")); !os.IsNotExist(err) {
				t.Fatal("native package migration touched auth")
			}
		})
	}
}

func TestCodexAuthLockIsNotHistory(t *testing.T) {
	rule, ok := classifyHistoryName(codexLayout, "auth.lock")
	if !ok || rule.class != "not_history" {
		t.Fatalf("auth writer lock unclassified: %+v", rule)
	}
}

// Kept local to the test: no account data is ever printed, including on error.
func nativeFixtureJSON(v any) []byte { b, _ := json.Marshal(v); return append(b, '\n') }

func TestPinnedCodexHomeLayout(t *testing.T) {
	binary := pinnedCodexBinary(t)
	home := testsock.ShortDir(t, "cl")
	codexHome := filepath.Join(home, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := pinnedCodexCommand(ctx, binary, home, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "codex-cli "+managedCodexVersion {
		t.Fatal("incorrect pinned binary")
	}
	if err := pinnedCodexCommand(ctx, binary, home, "app-server", "generate-json-schema", "--experimental", "--out", t.TempDir()).Run(); err != nil {
		t.Fatal("offline schema export failed")
	}
	// Same loopback Responses shape as hostexecutor's pinned reconnect harness.
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected endpoint", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]string{"type": "output_text", "text": "FIXTURE_OK"}}}
		for _, event := range []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp_fixture", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": "resp_fixture", "status": "completed", "model": "gpt-6-luna", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}},
		} {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], nativeFixtureJSON(event))
		}
	}))
	defer provider.Close()
	config := fmt.Sprintf("model = \"gpt-6-luna\"\nmodel_provider = \"inert_fixture\"\napproval_policy = \"never\"\nsandbox_mode = \"danger-full-access\"\ncli_auth_credentials_store = \"file\"\n[analytics]\nenabled = false\n[model_providers.inert_fixture]\nname = \"Loopback fixture\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nrequest_max_retries = 0\nstream_max_retries = 0\nsupports_websockets = false\n", provider.URL+"/v1")
	writeHostFixture(t, filepath.Join(codexHome, "config.toml"), config)
	execOut, err := pinnedCodexCommand(ctx, binary, home, "exec", "--json", "--skip-git-repo-check", "--model", "gpt-6-luna", "Reply with FIXTURE_OK; no tools.").Output()
	if err != nil || !strings.Contains(string(execOut), `"type":"turn.completed"`) {
		t.Fatal("loopback exec did not complete")
	}
	// Exercise the file auth writer entirely offline with a made-up API key.
	// No CLI login/logout, ChatGPT flow, real token, refresh or provider auth.
	cmd := pinnedCodexCommand(ctx, binary, home, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	for _, request := range []any{
		map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "fixture", "version": "1"}}},
		map[string]any{"method": "initialized"},
		map[string]any{"id": 2, "method": "account/login/start", "params": map[string]any{"type": "apiKey", "apiKey": "made-up-offline-fixture"}},
	} {
		if _, err = stdin.Write(nativeFixtureJSON(request)); err != nil {
			t.Fatal("offline auth writer input failed")
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	ack := false
	for scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil {
			t.Fatal("invalid offline auth writer response")
		}
		if response.ID == 2 {
			ack = len(response.Result) != 0 && len(response.Error) == 0
			break
		}
	}
	if !ack {
		t.Fatal("offline auth-file write was not confirmed")
	}
	_ = stdin.Close()
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	cmd.Process = nil // Cleanup below is replaced after the explicit bounded wait.
	entries, err := os.ReadDir(codexHome)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		// Names only, never contents or runtime identifiers below these entries.
		t.Log(entry.Name())
		seen[entry.Name()] = true
		if _, known := classifyHistoryName(codexLayout, entry.Name()); !known {
			t.Errorf("unclassified CODEX_HOME name: %s", entry.Name())
		}
	}
	if !seen["auth.lock"] || !seen["auth.json"] || !seen["sessions"] {
		t.Fatal("native layout actions were incomplete")
	}
}
