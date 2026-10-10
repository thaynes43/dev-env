package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/codexauth"
)

func hostFixture(t *testing.T) (Settings, CodexHostOptions) {
	t.Helper()
	home := t.TempDir()
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), WorkspaceID: "fixture-workspace", PodUID: "fixture-pod", APIURL: "https://fixture.invalid", CodexBin: "fake-codex", CodexAccessFile: filepath.Join(home, "projection", "access.json")}
	for _, dir := range []string{s.StateDir, s.CodexHome, s.workspaceDir(), s.ReposDir(), filepath.Join(home, "codex"), s.WorkDir()} {
		if os.MkdirAll(dir, 0o700) != nil {
			t.Fatal("fixture directory")
		}
	}
	writeHostFixture(t, filepath.Join(s.workspaceDir(), "marker.json"), `{"version":1,"id":"fixture-workspace"}`)
	o := CodexHostOptions{Enabled: true, CatalogFile: filepath.Join(home, "catalog.json"), InstructionsFile: filepath.Join(home, "guard.txt")}
	writeHostFixture(t, o.CatalogFile, projectTestCatalog)
	writeHostFixture(t, o.InstructionsFile, "ROOT_REVIEWED_GUARD")
	now := time.Now().UTC().Truncate(time.Second)
	access, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeHostFixture(t, s.CodexAccessFile, string(access))
	return s, o
}
func writeHostFixture(t *testing.T, path, data string) {
	t.Helper()
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil || os.WriteFile(path, []byte(data), 0o600) != nil {
		t.Fatal("fixture write")
	}
}
func hostLiveConfig(t *testing.T, s Settings, o CodexHostOptions) {
	t.Helper()
	if codexHostConfig(s, o, true) != nil {
		t.Fatal("config preparation")
	}
	if _, err := codexHostSettings(s, true); err != nil {
		t.Fatal("settings preparation")
	}
	writeHostFixture(t, filepath.Join(s.CodexHome, "app-server-daemon", "settings.json"), `{"remoteControlEnabled":true,"updater":{"autoUpdateEnabled":false}}`)
	path := filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")
	writeHostFixture(t, path, "fixture executable")
	if os.Chmod(path, 0o700) != nil {
		t.Fatal("fixture executable mode")
	}
}
func hostDeps(t *testing.T, s Settings, o CodexHostOptions) (codexHostDeps, *[]string) {
	t.Helper()
	calls := []string{}
	live := codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture", StartTicks: 45}}
	return codexHostDeps{preflight: func(Settings) error { return nil }, process: func(Settings) (codexHostProcess, error) { return live, nil }, native: func(_ context.Context, _ Settings, _ string, args []string, w io.Writer) ([]byte, error) {
		calls = append(calls, strings.Join(args, " "))
		if len(args) == 1 && args[0] == "--version" {
			return []byte("codex-cli 0.160.1\n"), nil
		}
		if w != nil {
			_, _ = io.WriteString(w, "SYNTHETIC_PRIVATE_PAIR_CODE")
		}
		return nil, nil
	}, remote: func(context.Context, Settings, bool, int) (codexHostRemote, error) {
		return codexHostRemote{Version: managedCodexVersion, Connection: "connected", Idle: true}, nil
	}, sync: SyncCodexAccess, wait: waitCodexHostTick}, &calls
}

func TestCodexHostDisabledDoesNothing(t *testing.T) {
	d := codexHostDeps{}
	ctx := context.Background()
	s := Settings{}
	status, err := observeCodexHost(ctx, s, CodexHostOptions{}, d)
	if err != nil || status.Code != "Disabled" {
		t.Fatal("disabled observer")
	}
	if runCodexHost(ctx, s, CodexHostOptions{}, func(s CodexHostStatus) error {
		if s.Code != "Disabled" {
			t.Fatal("unexpected status")
		}
		return nil
	}, d) != nil {
		t.Fatal("disabled supervisor")
	}
	if stopCodexHost(ctx, s, CodexHostOptions{}, d) == nil || pairCodexHost(ctx, s, CodexHostOptions{}, io.Discard, d) == nil {
		t.Fatal("disabled control accepted")
	}
}
func TestCodexHostReadOnlySharedMountsAndPrivateMarker(t *testing.T) {
	s, _ := hostFixture(t)
	old := readWorkspaceMountInfo
	t.Cleanup(func() { readWorkspaceMountInfo = old })
	good := strings.ReplaceAll(string(workspaceMountTable(s.Home)), " rw - ceph", " ro - ceph") + "\n6 1 0:42 /retained/work /work ro - ceph workspace rw"
	readWorkspaceMountInfo = func() ([]byte, error) { return []byte(good), nil }
	if codexHostPreflight(s) != nil {
		t.Fatal("correct mount fixture refused")
	}
	for _, bad := range []string{strings.Replace(good, "/work ro - ceph", "/work rw - ceph", 1), strings.Replace(good, s.Home+" rw - ext4", s.Home+" ro - ext4", 1), strings.Replace(good, "/retained/work /work", "/foreign/work /work", 1)} {
		readWorkspaceMountInfo = func() ([]byte, error) { return []byte(bad), nil }
		if codexHostPreflight(s) == nil {
			t.Fatal("unsafe mount accepted")
		}
	}
	readWorkspaceMountInfo = func() ([]byte, error) { return []byte(good), nil }
	writeHostFixture(t, filepath.Join(s.workspaceDir(), "marker.json"), `{"version":1,"id":"wrong"}`)
	if codexHostPreflight(s) == nil {
		t.Fatal("foreign marker accepted")
	}
}
func TestCodexHostNativeProcessProof(t *testing.T) {
	s, _ := hostFixture(t)
	state := filepath.Join(s.CodexHome, "app-server-daemon")
	if os.MkdirAll(state, 0o700) != nil {
		t.Fatal("state")
	}
	fs := realCodexHostFS()
	read := fs.read
	fs.read = func(p string) ([]byte, error) {
		switch p {
		case "/proc/sys/kernel/random/boot_id":
			return []byte("fixture-boot\n"), nil
		case "/proc/locks":
			return nil, nil
		case "/proc/123/stat":
			return []byte("123 (name with spaces) S " + strings.Repeat("0 ", 18) + "45 0"), nil
		}
		return read(p)
	}
	if proof, err := inspectCodexHostProcess(s, fs); err != nil || proof.State != "Absent" {
		t.Fatal("fresh absence")
	}
	pid := filepath.Join(state, "daemon.pid")
	writeHostFixture(t, pid, "")
	if _, err := inspectCodexHostProcess(s, fs); err == nil {
		t.Fatal("empty reservation accepted")
	}
	record := `{"pid":123,"processStartTime":"opaque-native","processIdentity":{"bootId":"fixture-boot","startTicks":45}}`
	writeHostFixture(t, pid, record)
	socket := filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock")
	if os.MkdirAll(filepath.Dir(socket), 0o700) != nil {
		t.Fatal("socket dir")
	}
	if os.Symlink("/native-owned-socket", socket) != nil {
		t.Fatal("native symlink")
	}
	if proof, err := inspectCodexHostProcess(s, fs); err != nil || proof.State != "Live" {
		t.Fatal("matching live identity")
	}
	writeHostFixture(t, pid, strings.Replace(record, "fixture-boot", "old-boot", 1))
	if _, err := inspectCodexHostProcess(s, fs); err == nil {
		t.Fatal("stale socket is not proven absent")
	}
	if os.Remove(socket) != nil {
		t.Fatal("fixture cleanup")
	}
	if proof, err := inspectCodexHostProcess(s, fs); err != nil || proof.State != "Absent" {
		t.Fatal("different boot is stale")
	}
	writeHostFixture(t, pid, record)
	writeHostFixture(t, filepath.Join(state, "daemon-updater.pid"), record)
	if _, err := inspectCodexHostProcess(s, fs); err == nil {
		t.Fatal("live updater accepted")
	}
	if os.Remove(filepath.Join(state, "daemon-updater.pid")) != nil {
		t.Fatal("fixture cleanup")
	}
	lock := filepath.Join(state, "daemon.lock")
	writeHostFixture(t, lock, "")
	st, err := os.Stat(lock)
	if err != nil {
		t.Fatal(err)
	}
	stat := st.Sys().(*syscall.Stat_t)
	original := fs.read
	fs.read = func(p string) ([]byte, error) {
		if p == "/proc/locks" {
			return []byte(fmt.Sprintf("1: FLOCK ADVISORY WRITE 123 %x:%x:%d 0 EOF", unix.Major(stat.Dev), unix.Minor(stat.Dev), stat.Ino)), nil
		}
		return original(p)
	}
	if _, err := inspectCodexHostProcess(s, fs); err == nil {
		t.Fatal("active native lifecycle lock accepted")
	}
}
func TestCodexHostConfigPreservesNativeStateAndTrustProvenance(t *testing.T) {
	s, o := hostFixture(t)
	existingRoot := filepath.Join(s.Home, "codex", "sample")
	config := fmt.Sprintf("developer_instructions = 'ORIGINAL_RULES'\nproject_doc_fallback_filenames = ['README.rules']\n[mcp_servers.fixture]\ncommand='retained-command'\n[projects.%q]\ntrust_level='trusted'\n", existingRoot)
	writeHostFixture(t, filepath.Join(s.CodexHome, "config.toml"), config)
	writeHostFixture(t, filepath.Join(s.CodexHome, "history.sqlite"), "HISTORY_CANARY")
	if codexHostConfig(s, o, true) != nil {
		t.Fatal("compose refused")
	}
	var receipt codexHostConfigReceipt
	raw, _ := os.ReadFile(s.statePath("codex-host-config.json"))
	if json.Unmarshal(raw, &receipt) != nil || len(receipt.OwnedTrustRoots) != 0 {
		t.Fatal("claimed existing trust ownership")
	}
	data, _ := os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
	var parsed map[string]any
	if toml.Unmarshal(data, &parsed) != nil || parsed["developer_instructions"] != "ORIGINAL_RULES\n\nROOT_REVIEWED_GUARD" || parsed["mcp_servers"] == nil {
		t.Fatal("native settings were replaced")
	}
	newer := strings.Replace(projectTestCatalog, "sample", "another", 1)
	writeHostFixture(t, o.CatalogFile, newer)
	if codexHostConfig(s, o, true) != nil {
		t.Fatal("catalog update")
	}
	data, _ = os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
	if toml.Unmarshal(data, &parsed) != nil {
		t.Fatal("config")
	}
	projects := parsed["projects"].(map[string]any)
	if projects[existingRoot] == nil {
		t.Fatal("preexisting trust removed")
	}
	writeHostFixture(t, o.CatalogFile, projectTestCatalog)
	if codexHostConfig(s, o, true) != nil {
		t.Fatal("catalog return")
	}
	data, _ = os.ReadFile(filepath.Join(s.CodexHome, "config.toml"))
	if bytes.Contains(data, []byte(filepath.Join(s.Home, "codex", "another"))) {
		t.Fatal("generated retired trust retained")
	}
	history, _ := os.ReadFile(filepath.Join(s.CodexHome, "history.sqlite"))
	if string(history) != "HISTORY_CANARY" {
		t.Fatal("history changed")
	}
	if os.Chmod(s.statePath("codex-host-config.json"), 0o644) != nil {
		t.Fatal("mode fixture")
	}
	if codexHostConfig(s, o, true) == nil {
		t.Fatal("public private provenance accepted")
	}
}
func TestCodexHostSettingsPreserveAndRefuseMalformed(t *testing.T) {
	s, _ := hostFixture(t)
	path := filepath.Join(s.CodexHome, "app-server-daemon", "settings.json")
	writeHostFixture(t, path, `{"remoteControlEnabled":true,"futureSetting":{"keep":"CANARY"},"updater":{"autoUpdateEnabled":true,"futureUpdater":"CANARY"},"shutdownGraceSeconds":90}`)
	grace, err := codexHostSettings(s, true)
	if err != nil || grace != 90 {
		t.Fatal("settings compose")
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Contains(raw, []byte("futureUpdater")) || !bytes.Contains(raw, []byte("futureSetting")) {
		t.Fatal("unknown settings clobbered")
	}
	if _, err := codexHostSettings(s, false); err != nil {
		t.Fatal("matching live settings")
	}
	writeHostFixture(t, path, `{"updater":{"autoUpdateEnabled":"unknown"}}`)
	if _, err := codexHostSettings(s, true); err == nil {
		t.Fatal("malformed updater replaced")
	}
}
func TestCodexHostLiveNeverStartsAndVersionFailureNeverBecomesAbsence(t *testing.T) {
	s, o := hostFixture(t)
	hostLiveConfig(t, s, o)
	d, calls := hostDeps(t, s, o)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := runCodexHost(ctx, s, o, func(status CodexHostStatus) error {
		cancel()
		if status.Code != "Observed" {
			t.Fatal("readiness")
		}
		return nil
	}, d); err != nil {
		t.Fatal(err)
	}
	for _, call := range *calls {
		if call != "--version" {
			t.Fatal("live daemon lifecycle was modified")
		}
	}
	d.native = func(context.Context, Settings, string, []string, io.Writer) ([]byte, error) {
		return nil, errors.New("PRIVATE_NATIVE_ERROR")
	}
	status, err := observeCodexHost(context.Background(), s, o, d)
	if err == nil || status.Code != "Unknown" || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("failed version inferred absence/leaked diagnostics")
	}
}
func TestCodexHostFailedStartIsNotAutomaticallyRetried(t *testing.T) {
	s, o := hostFixture(t)
	d, _ := hostDeps(t, s, o)
	d.process = func(Settings) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
	starts := 0
	d.native = func(_ context.Context, _ Settings, _ string, args []string, _ io.Writer) ([]byte, error) {
		if args[0] == "--version" {
			return []byte("codex-cli 0.160.1"), nil
		}
		starts++
		return nil, errors.New("lost native ack")
	}
	first := startCodexHost(context.Background(), s, o, d)
	second := startCodexHost(context.Background(), s, o, d)
	if first == nil || second == nil || starts != 1 {
		t.Fatal("uncertain startup retried")
	}
}
func TestCodexHostPairOutputIsOnlyExplicitCallerAndStopBudget(t *testing.T) {
	s, o := hostFixture(t)
	hostLiveConfig(t, s, o)
	d, calls := hostDeps(t, s, o)
	var caller bytes.Buffer
	if pairCodexHost(context.Background(), s, o, &caller, d) != nil || caller.String() != "SYNTHETIC_PRIVATE_PAIR_CODE" {
		t.Fatal("pair caller route")
	}
	status, err := observeCodexHost(context.Background(), s, o, d)
	encoded, _ := json.Marshal(status)
	if err != nil || bytes.Contains(encoded, []byte("PAIR")) {
		t.Fatal("pair leaked into status")
	}
	o.DeclaredShutdown = true
	o.TerminationGrace = 70 * time.Second
	if stopCodexHost(context.Background(), s, o, d) == nil {
		t.Fatal("stop exceeds pod budget")
	}
	for _, call := range *calls {
		if strings.Contains(call, "stop") {
			t.Fatal("stop invoked with insufficient budget")
		}
	}
}

func hostRPCFixture(t *testing.T, answer func(string) any, notification bool) Settings {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "host-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	dir := filepath.Join(home, "app-server-control")
	if os.Mkdir(dir, 0o700) != nil {
		t.Fatal("rpc directory")
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "app-server-control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var request struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			if request.ID == nil {
				continue
			}
			if notification && request.Method == "thread/loaded/list" {
				_ = conn.WriteJSON(map[string]any{"method": "turn/started", "params": map[string]string{"private": "SYNTHETIC_PRIVATE"}})
			}
			if conn.WriteJSON(map[string]any{"id": *request.ID, "result": answer(request.Method)}) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return Settings{CodexHome: home}
}
func hostRPCAnswer(method string) any {
	switch method {
	case "initialize":
		return map[string]any{"userAgent": "codex/0.160.1 fixture", "private": "PRIVATE_ACCOUNT"}
	case "remoteControl/status/read":
		return map[string]any{"status": "connected", "installationId": "PRIVATE_INSTALLATION"}
	case "thread/loaded/list":
		return map[string]any{"data": []string{}, "nextCursor": nil}
	}
	return nil
}
func TestCodexHostPassiveRPCBoundsRedactionAndChurn(t *testing.T) {
	s := hostRPCFixture(t, hostRPCAnswer, false)
	result, err := probeCodexHostRemote(context.Background(), s, true, os.Getpid())
	encoded, _ := json.Marshal(result)
	if err != nil || !result.Idle || bytes.Contains(encoded, []byte("PRIVATE")) {
		t.Fatal("passive ready/redaction")
	}
	for _, mode := range []string{"missing-data", "oversized", "version-mismatch", "churn"} {
		t.Run(mode, func(t *testing.T) {
			s := hostRPCFixture(t, func(method string) any {
				if mode == "missing-data" && method == "thread/loaded/list" {
					return map[string]any{}
				}
				if mode == "oversized" && method == "initialize" {
					return map[string]any{"userAgent": strings.Repeat("X", codexHostRPCBound+1)}
				}
				if mode == "version-mismatch" && method == "initialize" {
					return map[string]any{"userAgent": "codex/0.159.1"}
				}
				return hostRPCAnswer(method)
			}, mode == "churn")
			if _, err := probeCodexHostRemote(context.Background(), s, true, os.Getpid()); err == nil {
				t.Fatal("unknown RPC accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeCodexHostRemote(ctx, s, true, os.Getpid()); err == nil {
		t.Fatal("cancel ignored")
	}
}

func hostStopACK(s Settings) []byte {
	raw, _ := json.Marshal(map[string]any{"status": "stopped", "backend": "pid", "managedCodexPath": filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex"), "managedCodexVersion": managedCodexVersion, "socketPath": filepath.Join(s.CodexHome, "app-server-control", "app-server-control.sock"), "cliVersion": managedCodexVersion})
	return raw
}
func TestCodexHostCompletedLifecyclePermitsExactlyOneNewStart(t *testing.T) {
	s, o := hostFixture(t)
	hostLiveConfig(t, s, o)
	d, _ := hostDeps(t, s, o)
	process := codexHostProcess{State: "Absent"}
	starts, stops := 0, 0
	d.process = func(Settings) (codexHostProcess, error) { return process, nil }
	d.native = func(_ context.Context, _ Settings, _ string, args []string, _ io.Writer) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "--version":
			return []byte("codex-cli 0.160.1"), nil
		case "remote-control start --json":
			starts++
			process = codexHostProcess{State: "Live", PID: 123 + starts, Identity: codexHostIdentity{BootID: "fixture", StartTicks: uint64(45 + starts)}}
			return nil, nil
		case "remote-control stop --json":
			stops++
			process = codexHostProcess{State: "Absent"}
			return hostStopACK(s), nil
		}
		return nil, errors.New("unexpected native action")
	}
	if startCodexHost(context.Background(), s, o, d) != nil {
		t.Fatal("initial start")
	}
	receipt, err := hostLifecycle(s)
	if err != nil || receipt.Phase != "Confirmed" || !sameHostProcess(receipt.Process, process) {
		t.Fatal("unbound confirmed start")
	}
	o.DeclaredShutdown = true
	o.TerminationGrace = 90 * time.Second
	if stopCodexHost(context.Background(), s, o, d) != nil {
		t.Fatal("declared stop")
	}
	receipt, err = hostLifecycle(s)
	if err != nil || receipt.Phase != "Stopped" {
		t.Fatal("completed stop missing")
	}
	// A new Pod on the retained home may consume only completed native stop;
	// its new live identity becomes the sole confirmed lifecycle owner.
	s.PodUID = "next-fixture-pod"
	if startCodexHost(context.Background(), s, o, d) != nil || starts != 2 || stops != 1 {
		t.Fatal("completed lifecycle cannot restart")
	}
	receipt, err = hostLifecycle(s)
	if err != nil || receipt.PodUID != s.PodUID || !sameHostProcess(receipt.Process, process) {
		t.Fatal("next start not rebound")
	}
	process = codexHostProcess{State: "Absent"}
	if startCodexHost(context.Background(), s, o, d) == nil || starts != 2 {
		t.Fatal("PID absence alone replayed start")
	}
}
func TestCodexHostFailedOrUnknownStopCannotCompleteOrReplay(t *testing.T) {
	for _, mode := range []string{"failed-ack", "unknown-absence", "changed-owner", "not-running", "empty", "truncated", "wrong-backend", "unexpected-pid", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			s, o := hostFixture(t)
			hostLiveConfig(t, s, o)
			d, _ := hostDeps(t, s, o)
			live := codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture", StartTicks: 45}}
			process := live
			unknown := false
			d.process = func(Settings) (codexHostProcess, error) {
				if unknown {
					return codexHostProcess{}, errCodexHostUnknown
				}
				return process, nil
			}
			if saveHostLifecycle(s, "Confirmed", live) != nil {
				t.Fatal("confirmed fixture")
			}
			stopCalls, startCalls := 0, 0
			d.native = func(_ context.Context, _ Settings, _ string, args []string, _ io.Writer) ([]byte, error) {
				if args[0] == "--version" {
					return []byte("codex-cli 0.160.1"), nil
				}
				if args[1] == "stop" {
					stopCalls++
					if mode == "unknown-absence" {
						unknown = true
						return hostStopACK(s), nil
					}
					process = codexHostProcess{State: "Absent"}
					ack := hostStopACK(s)
					switch mode {
					case "not-running":
						return []byte(`{"status":"notRunning"}`), nil
					case "empty":
						return nil, nil
					case "truncated":
						return ack[:len(ack)-1], nil
					case "wrong-backend":
						return bytes.Replace(ack, []byte(`"pid"`), []byte(`"other"`), 1), nil
					case "unexpected-pid":
						return bytes.Replace(ack, []byte(`"status"`), []byte(`"pid":999,"status"`), 1), nil
					case "wrong-version":
						return bytes.ReplaceAll(ack, []byte(managedCodexVersion), []byte("0.159.1")), nil
					}
					return nil, errors.New("lost stop ACK")
				}
				startCalls++
				return nil, nil
			}
			if mode == "changed-owner" {
				process.PID++
			}
			o.DeclaredShutdown = true
			o.TerminationGrace = 90 * time.Second
			if stopCodexHost(context.Background(), s, o, d) == nil {
				t.Fatal("uncertain stop accepted")
			}
			receipt, err := hostLifecycle(s)
			if err != nil || receipt.Phase == "Stopped" {
				t.Fatal("uncertain lifecycle completed")
			}
			unknown = false
			process = codexHostProcess{State: "Absent"}
			if startCodexHost(context.Background(), s, o, d) == nil || startCalls != 0 {
				t.Fatal("uncertain stop replayed start")
			}
			process = live
			if stopCodexHost(context.Background(), s, o, d) == nil && mode != "changed-owner" {
				t.Fatal("uncertain stop replayed")
			}
			if mode != "changed-owner" && stopCalls != 1 {
				t.Fatal("stop action repeated")
			}
		})
	}
}

func TestCodexHostTransientObservationPreservesDaemon(t *testing.T) {
	for _, mode := range []string{"version-failure", "initial-process-unknown", "report-failure"} {
		t.Run(mode, func(t *testing.T) {
			s, o := hostFixture(t)
			hostLiveConfig(t, s, o)
			d, calls := hostDeps(t, s, o)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			phase := 0
			originalNative := d.native
			d.native = func(ctx context.Context, s Settings, bin string, args []string, w io.Writer) ([]byte, error) {
				if phase == 0 && mode == "version-failure" {
					return nil, errors.New("transient private diagnostic")
				}
				return originalNative(ctx, s, bin, args, w)
			}
			originalProcess := d.process
			d.process = func(s Settings) (codexHostProcess, error) {
				if phase == 0 && mode == "initial-process-unknown" {
					return codexHostProcess{}, errCodexHostUnknown
				}
				return originalProcess(s)
			}
			d.wait = func(context.Context) { phase++ }
			statuses := []string{}
			err := runCodexHost(ctx, s, o, func(status CodexHostStatus) error {
				statuses = append(statuses, status.Code)
				if len(statuses) == 2 {
					cancel()
				}
				if mode == "report-failure" {
					return errors.New("closed output")
				}
				return nil
			}, d)
			if err != nil || len(statuses) != 2 || statuses[1] != "Observed" {
				t.Fatal("transient observation killed supervisor or failed recovery")
			}
			if mode != "report-failure" && statuses[0] != "Unknown" {
				t.Fatal("unknown was not reported")
			}
			for _, call := range *calls {
				if call != "--version" {
					t.Fatal("transient observation restarted/stopped/replaced native")
				}
			}
			live, _ := originalProcess(s)
			if saveHostLifecycle(s, "Confirmed", live) != nil {
				t.Fatal("confirmed fixture")
			}
			d.remote = func(context.Context, Settings, bool, int) (codexHostRemote, error) {
				return codexHostRemote{Version: managedCodexVersion, Connection: "connected", Idle: false}, nil
			}
			o.DeclaredShutdown = true
			o.TerminationGrace = 90 * time.Second
			if stopCodexHost(context.Background(), s, o, d) == nil {
				t.Fatal("busy planned shutdown accepted")
			}
			for _, call := range *calls {
				if strings.Contains(call, "stop") {
					t.Fatal("busy native was stopped")
				}
			}
		})
	}
}

func TestCodexHostStopMustFitRemainingPodBudget(t *testing.T) {
	s, o := hostFixture(t)
	hostLiveConfig(t, s, o)
	d, calls := hostDeps(t, s, o)
	live, err := d.process(s)
	if err != nil || saveHostLifecycle(s, "Confirmed", live) != nil {
		t.Fatal("confirmed fixture")
	}
	o.DeclaredShutdown = true
	o.TerminationGrace = 90 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if stopCodexHost(ctx, s, o, d) == nil {
		t.Fatal("native grace exceeds remaining pod deadline")
	}
	for _, call := range *calls {
		if strings.Contains(call, "stop") {
			t.Fatal("stop started without enough remaining pod budget")
		}
	}
	receipt, err := hostLifecycle(s)
	if err != nil || receipt.Phase != "Confirmed" {
		t.Fatal("refused preflight consumed stop receipt")
	}
}
