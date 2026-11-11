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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/threadlaunch"
)

const trustLayerVersion = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// trustRetryVersion is a second user-layer version, distinct from the one
// the first read returns, so a conflict retry can be checked on the request.
const trustRetryVersion = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(git, "init", "-q", dir)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"HOME=" + t.TempDir(),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v %s", dir, err, out)
	}
}

// trustWriteResult is a ConfigWriteResponse: the fields the 0.160.1 schema requires.
func trustWriteResult() map[string]any {
	return map[string]any{
		"filePath": "/home/dev/.codex/config.toml",
		"status":   "ok",
		"version":  trustLayerVersion,
	}
}

func trustReadResult(folder string, trusted bool) map[string]any {
	config := map[string]any{}
	if trusted {
		config["projects"] = map[string]any{folder: map[string]any{"trust_level": "trusted"}}
	}
	return map[string]any{
		"config":  config,
		"origins": map[string]any{},
		"layers": []any{map[string]any{
			"name":    map[string]any{"type": "user", "file": "/home/dev/.codex/config.toml", "profile": nil},
			"version": trustLayerVersion,
			"config":  map[string]any{},
		}},
	}
}

type trustReply struct {
	result any
	fault  map[string]any
	drop   bool
}

// trustDaemon is an in-process app-server. It answers initialize itself and
// asks reply for every other request. No client, account or model is started.
func trustDaemon(t *testing.T, reply func(method string, params map[string]any) trustReply) (string, func() []map[string]any) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "trust-rpc-")
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
	var mu sync.Mutex
	seen := []map[string]any{}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			var request struct {
				ID     *int           `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if conn.ReadJSON(&request) != nil {
				return
			}
			mu.Lock()
			seen = append(seen, map[string]any{"method": request.Method, "params": request.Params})
			mu.Unlock()
			if request.ID == nil {
				continue
			}
			if request.Method == "initialize" {
				if conn.WriteJSON(map[string]any{"id": *request.ID, "result": map[string]any{
					"userAgent": "codex/0.160.1 fixture", "codexHome": home,
					"platformFamily": "unix", "platformOs": "linux",
				}}) != nil {
					return
				}
				continue
			}
			answer := reply(request.Method, request.Params)
			if answer.drop {
				return
			}
			body := map[string]any{"id": *request.ID}
			if answer.fault != nil {
				body["error"] = answer.fault
			} else {
				body["result"] = answer.result
			}
			if conn.WriteJSON(body) != nil {
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return home, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, len(seen))
		copy(out, seen)
		return out
	}
}

func trustDeps(f *threadFake) codexThreadDeps {
	d := f.deps()
	d.daemon = func(Settings) (codexHostProcess, error) {
		return codexHostProcess{State: "Live", PID: os.Getpid()}, nil
	}
	d.remote = func(_ context.Context, _ Settings, pid int) (string, error) {
		if pid != os.Getpid() {
			return "", os.ErrInvalid
		}
		return "connected", nil
	}
	d.trust = writeCodexProjectTrust
	return d
}

func countMethod(seen []map[string]any, method string) int {
	n := 0
	for _, r := range seen {
		if r["method"] == method {
			n++
		}
	}
	return n
}

func writeExpectedVersions(seen []map[string]any) []any {
	var out []any
	for _, r := range seen {
		if r["method"] != "config/value/write" {
			continue
		}
		params, _ := r["params"].(map[string]any)
		out = append(out, params["expectedVersion"])
	}
	return out
}

// launchTrust runs one thread start against the fake daemon. prepare may
// change the boot record after threadHost writes it and before the launch.
func launchTrust(t *testing.T, prepare func(*testing.T, Settings), reply func(cwd, method string) trustReply) (apiv1.ThreadLaunchResult, []map[string]any) {
	t.Helper()
	s, sess, cwd := threadHost(t)
	if prepare != nil {
		prepare(t, s)
	}
	home, seen := trustDaemon(t, func(method string, _ map[string]any) trustReply {
		return reply(cwd, method)
	})
	s.CodexHome = home
	f := newThreadFake(cwd)
	got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(f))
	if got.Outcome == apiv1.ThreadResponded {
		if len(f.launches) != 1 {
			t.Fatalf("responded with %d launches", len(f.launches))
		}
	} else if len(f.launches) != 0 {
		t.Fatalf("%s started %d launches", got.Outcome, len(f.launches))
	}
	return got, seen()
}

func trustVersionConflict() map[string]any {
	return map[string]any{
		"code":    -32600,
		"message": "Configuration was modified since last read. Fetch latest version and retry.",
		"data":    map[string]any{"config_write_error_code": "configVersionConflict"},
	}
}

func pinnedSchema(t *testing.T, name, digest string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_thread_launch", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		t.Fatal("pinned schema changed", name)
	}
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		t.Fatal("schema JSON", name)
	}
	return schema
}

// A repository the bootstrap record names as Ready is trusted through
// config/value/write, and the launch then proceeds. A second launch sees the
// folder already trusted and does not write again.
func TestCodexRepoTrustLaunchProceeds(t *testing.T) {
	s, sess, cwd := threadHost(t)
	var trustMu sync.Mutex
	trusted := false
	home, seen := trustDaemon(t, func(method string, _ map[string]any) trustReply {
		switch method {
		case "config/read":
			trustMu.Lock()
			on := trusted
			trustMu.Unlock()
			return trustReply{result: trustReadResult(cwd, on)}
		case "config/value/write":
			trustMu.Lock()
			trusted = true
			trustMu.Unlock()
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	s.CodexHome = home
	f := newThreadFake(cwd)
	got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(f))
	if got.Outcome != apiv1.ThreadResponded || len(f.launches) != 1 {
		t.Fatalf("got %s/%+v launches %d", got.Outcome, got.Error, len(f.launches))
	}
	paramsSchema := pinnedSchema(t, "ConfigValueWriteParams", "b9032e35bf64d4628bfc72e22b58b71bbcf754aeb720a86eacb091e0137547c9")
	responseSchema := pinnedSchema(t, "ConfigWriteResponse", "1d690287d9e8a0c552f664038d3810dfc3903ec8042eb2c0f772856414f59954")
	var writeParams, readParams map[string]any
	for _, r := range seen() {
		switch r["method"] {
		case "config/value/write":
			writeParams, _ = r["params"].(map[string]any)
		case "config/read":
			if readParams == nil {
				readParams, _ = r["params"].(map[string]any)
			}
		}
	}
	if readParams["includeLayers"] != true || readParams["cwd"] != nil {
		t.Fatalf("config/read params %v", readParams)
	}
	for _, key := range paramsSchema["required"].([]any) {
		if _, ok := writeParams[key.(string)]; !ok {
			t.Fatalf("write params %v lack required %v", writeParams, key)
		}
	}
	if writeParams["keyPath"] != "projects."+strconvQuote(cwd)+".trust_level" || writeParams["mergeStrategy"] != "replace" || writeParams["value"] != "trusted" || writeParams["expectedVersion"] != trustLayerVersion || writeParams["filePath"] != nil {
		t.Fatalf("write params %v", writeParams)
	}
	strategies := paramsSchema["definitions"].(map[string]any)["MergeStrategy"].(map[string]any)["enum"].([]any)
	if !containsAny(strategies, writeParams["mergeStrategy"]) {
		t.Fatalf("mergeStrategy %v not in %v", writeParams["mergeStrategy"], strategies)
	}
	wrote := trustWriteResult()
	for _, key := range responseSchema["required"].([]any) {
		if _, ok := wrote[key.(string)]; !ok {
			t.Fatalf("write result %v lacks required %v", wrote, key)
		}
	}
	if wrote["status"] != "ok" {
		t.Fatalf("status %v", wrote["status"])
	}
	againFake := newThreadFake(cwd)
	again := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(againFake))
	if again.Outcome != apiv1.ThreadResponded || countMethod(seen(), "config/value/write") != 1 {
		t.Fatalf("second launch %s writes %d", again.Outcome, countMethod(seen(), "config/value/write"))
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func containsAny(list []any, v any) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Provenance is the session's boot record, not a Git directory. A repository
// the record does not name is refused, one it names as Ready is accepted
// above, and a record that cannot be read is refused. Preserved is not Ready.
func TestCodexRepoTrustProvenance(t *testing.T) {
	t.Run("not named", func(t *testing.T) {
		s, _, _ := threadHost(t)
		extra := s.ClonePath("extra")
		gitInitRepo(t, extra)
		err := ensureCodexRepoTrust(context.Background(), s, os.Getpid(), extra, func(context.Context, Settings, int, string) error {
			t.Fatal("a repository the record does not name read config")
			return nil
		})
		if !errors.Is(err, errFolderNotQualified) {
			t.Fatalf("err %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(s.CodexHome, "config.toml")); !os.IsNotExist(statErr) {
			t.Fatalf("config.toml err %v", statErr)
		}
	})
	t.Run("preserved", func(t *testing.T) {
		s, _, cwd := threadHost(t)
		rec := bootRecord{BootID: "b1", Boot: protocol.BootReady, ProjectBootstrap: &protocol.ProjectBootstrap{
			State:        protocol.BootstrapPartial,
			Repositories: []protocol.ProjectBootstrapRepository{{Name: "dev-env", State: protocol.BootstrapPreserved, Reason: protocol.ReasonWorkingTreeDiffers}},
		}}
		if err := writeJSONFile(s.statePath(bootFile), rec); err != nil {
			t.Fatal(err)
		}
		err := ensureCodexRepoTrust(context.Background(), s, os.Getpid(), cwd, func(context.Context, Settings, int, string) error {
			t.Fatal("a preserved repository read config")
			return nil
		})
		if !errors.Is(err, errFolderNotQualified) {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("unreadable", func(t *testing.T) {
		s, _, cwd := threadHost(t)
		path := s.statePath(bootFile)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(t.TempDir(), "other.json"), path); err != nil {
			t.Fatal(err)
		}
		err := ensureCodexRepoTrust(context.Background(), s, os.Getpid(), cwd, func(context.Context, Settings, int, string) error {
			t.Fatal("an unreadable record read config")
			return nil
		})
		if !errors.Is(err, errFolderNotQualified) {
			t.Fatalf("err %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		err = ensureCodexRepoTrust(context.Background(), s, os.Getpid(), cwd, func(context.Context, Settings, int, string) error {
			t.Fatal("invalid JSON read config")
			return nil
		})
		if !errors.Is(err, errFolderNotQualified) {
			t.Fatalf("invalid JSON err %v", err)
		}
	})
}

// A version conflict is retried and then refused. The rejected writes do not
// count as a change.
func TestCodexRepoTrustVersionConflictRefused(t *testing.T) {
	s, sess, cwd := threadHost(t)
	home, seen := trustDaemon(t, func(method string, _ map[string]any) trustReply {
		switch method {
		case "config/read":
			return trustReply{result: trustReadResult(cwd, false)}
		case "config/value/write":
			return trustReply{fault: map[string]any{
				"code":    -32600,
				"message": "Configuration was modified since last read. Fetch latest version and retry.",
				"data":    map[string]any{"config_write_error_code": "configVersionConflict"},
			}}
		}
		return trustReply{}
	})
	s.CodexHome = home
	f := newThreadFake(cwd)
	got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(f))
	if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_config_invalid" || got.Error.Message != threadlaunch.Message("codex_config_invalid") || got.TotalMs != 0 || len(f.launches) != 0 {
		t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
	}
	if !strings.HasPrefix(got.Error.Message, "Nothing changed:") {
		t.Fatalf("message %q", got.Error.Message)
	}
	// One attempt plus three retries, each rejected before the edit is applied.
	if countMethod(seen(), "config/value/write") != codexTrustVersionRetries+1 || countMethod(seen(), "config/read") != codexTrustVersionRetries+1 {
		t.Fatalf("reads %d writes %d", countMethod(seen(), "config/read"), countMethod(seen(), "config/value/write"))
	}
}

// A failure before the write says nothing changed. A failure after the write
// was sent says trust may have been written.
func TestCodexRepoTrustWriteErrors(t *testing.T) {
	t.Run("nothing changed", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		home, seen := trustDaemon(t, func(method string, _ map[string]any) trustReply {
			if method == "config/read" {
				return trustReply{fault: map[string]any{"code": -32603, "message": "failed to read configuration layers: fixture"}}
			}
			return trustReply{}
		})
		s.CodexHome = home
		f := newThreadFake(cwd)
		got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(f))
		if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_config_invalid" || got.Error.Message != threadlaunch.Message("codex_config_invalid") || len(f.launches) != 0 || countMethod(seen(), "config/value/write") != 0 {
			t.Fatalf("got %s/%+v writes %d", got.Outcome, got.Error, countMethod(seen(), "config/value/write"))
		}
	})
	t.Run("trust may have been written", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		home, seen := trustDaemon(t, func(method string, _ map[string]any) trustReply {
			switch method {
			case "config/read":
				return trustReply{result: trustReadResult(cwd, false)}
			case "config/value/write":
				return trustReply{drop: true}
			}
			return trustReply{}
		})
		s.CodexHome = home
		f := newThreadFake(cwd)
		got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), trustDeps(f))
		if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_trust_unconfirmed" || got.Error.Message != threadlaunch.Message("codex_trust_unconfirmed") || len(f.launches) != 0 || countMethod(seen(), "config/value/write") != 1 {
			t.Fatalf("got %s/%+v writes %d", got.Outcome, got.Error, countMethod(seen(), "config/value/write"))
		}
		if !strings.HasPrefix(got.Error.Message, "Trust may have been written:") {
			t.Fatalf("message %q", got.Error.Message)
		}
	})
}

// A path outside the rule stops at once, before the wrapper and before any
// wait, with folder_not_qualified.
func TestCodexRepoTrustRefusesUnqualified(t *testing.T) {
	t.Run("not a repository", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		if err := os.RemoveAll(filepath.Join(cwd, ".git")); err != nil {
			t.Fatal(err)
		}
		f := newThreadFake(cwd)
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "folder_not_qualified" || got.TotalMs != 0 || len(f.launches) != 0 {
			t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
		}
	})
	t.Run("invalid git dir", func(t *testing.T) {
		s, sess, cwd := threadHost(t)
		gitDir := filepath.Join(cwd, ".git")
		if err := os.RemoveAll(gitDir); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		f := newThreadFake(cwd)
		got := launchThread(t, f, s, sess, goodThreadRequest())
		if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "folder_not_qualified" || got.TotalMs != 0 || len(f.launches) != 0 {
			t.Fatalf("got %s/%+v total %d launches %d", got.Outcome, got.Error, got.TotalMs, len(f.launches))
		}
	})
	s, _, cwd := threadHost(t)
	outside := []string{
		s.ReposDir(),
		filepath.Dir(s.ReposDir()),
		filepath.Join(cwd, "nested"),
	}
	if err := os.Mkdir(filepath.Join(cwd, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	gitInitRepo(t, other)
	link := s.ClonePath("linked")
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	outside = append(outside, other, link)
	for _, path := range outside {
		t.Run(path, func(t *testing.T) {
			if err := ensureCodexRepoTrust(context.Background(), s, 0, path, nil); !errors.Is(err, errFolderNotQualified) {
				t.Fatalf("path %s err %v", path, err)
			}
		})
	}
}

// A path named only in the handoff is not trusted. The clone is.
func TestCodexRepoTrustIgnoresHandoffPath(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	d := f.deps()
	var trusted []string
	d.trust = func(_ context.Context, _ Settings, _ int, folder string) error {
		trusted = append(trusted, folder)
		return nil
	}
	req := goodThreadRequest()
	req.Handoff = "Make a worktree, then summarize the open PRs.\nNotes live in /tmp/not-a-repo."
	got := startCodexThread(context.Background(), s, sess, req, d)
	if got.Outcome != apiv1.ThreadResponded || len(trusted) != 1 || trusted[0] != cwd {
		t.Fatalf("got %s/%+v trusted %v", got.Outcome, got.Error, trusted)
	}
}

// thread status resolves the folder and does not call the trust writer.
func TestCodexThreadStatusDoesNotWriteTrust(t *testing.T) {
	s, sess, cwd := threadHost(t)
	f := newThreadFake(cwd)
	var calls []string
	d := statusDeps(t, f, &calls)
	called := false
	d.trust = func(context.Context, Settings, int, string) error {
		called = true
		return errors.New("status must not write trust")
	}
	got := observeCodexThread(context.Background(), s, sess, CodexThreadStatusRequest{ThreadID: newThread}, d)
	if called || (got.Error != nil && (got.Error.Code == "codex_config_invalid" || got.Error.Code == "codex_trust_unconfirmed")) {
		t.Fatalf("status %s/%+v called %v", got.Outcome, got.Error, called)
	}
}

// A private host rejects a trusted root outside ~/codex/<project> on every
// status read. The launch stops before writing, so that read keeps working.
func TestCodexRepoTrustPrivateHostDoesNotWrite(t *testing.T) {
	s, sess, cwd := threadHost(t)
	s.PrivateCodexHost = true
	if err := os.MkdirAll(s.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte("model = \"gpt-6-astra\"\n")
	path := filepath.Join(s.CodexHome, "config.toml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	f := newThreadFake(cwd)
	called := false
	d := f.deps()
	d.trust = func(context.Context, Settings, int, string) error {
		called = true
		return nil
	}
	got := startCodexThread(context.Background(), s, sess, goodThreadRequest(), d)
	if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != "codex_config_invalid" || got.Error.Message != threadlaunch.Message("codex_config_invalid") || got.TotalMs != 0 || len(f.launches) != 0 || called {
		t.Fatalf("got %s/%+v total %d launches %d called %v", got.Outcome, got.Error, got.TotalMs, len(f.launches), called)
	}
	if file, err := os.ReadFile(path); err != nil || string(file) != string(original) {
		t.Fatalf("private host config changed: %v %q", err, file)
	}
}

func refuseTrust(t *testing.T, got apiv1.ThreadLaunchResult, seen []map[string]any, code string, writes int) {
	t.Helper()
	if got.Outcome != apiv1.ThreadFailed || got.Error == nil || got.Error.Code != code || got.Error.Message != threadlaunch.Message(code) || countMethod(seen, "config/value/write") != writes {
		t.Fatalf("got %s/%+v writes %d", got.Outcome, got.Error, countMethod(seen, "config/value/write"))
	}
}

// (a) No user layer is refused, and no config/value/write is sent.
func TestCodexRepoTrustNoUserLayer(t *testing.T) {
	got, seen := launchTrust(t, nil, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			result := trustReadResult(cwd, false)
			result["layers"] = []any{}
			return trustReply{result: result}
		case "config/value/write":
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	refuseTrust(t, got, seen, "codex_config_invalid", 0)
}

// (b) Two user layers are refused, and no config/value/write is sent.
func TestCodexRepoTrustTwoUserLayers(t *testing.T) {
	got, seen := launchTrust(t, nil, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			one := trustReadResult(cwd, false)
			two := trustReadResult(cwd, false)
			one["layers"] = []any{one["layers"].([]any)[0], two["layers"].([]any)[0]}
			return trustReply{result: one}
		case "config/value/write":
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	refuseTrust(t, got, seen, "codex_config_invalid", 0)
}

// (c) A user-layer version that is not the sha256 form is refused, and no
// config/value/write is sent.
func TestCodexRepoTrustVersionFormRefused(t *testing.T) {
	got, seen := launchTrust(t, nil, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			result := trustReadResult(cwd, false)
			result["layers"].([]any)[0].(map[string]any)["version"] = "not-a-version"
			return trustReply{result: result}
		case "config/value/write":
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	refuseTrust(t, got, seen, "codex_config_invalid", 0)
}

// (d) A version conflict is retried. The reread returns a new version, and
// the retry's config/value/write carries that version as expectedVersion.
func TestCodexRepoTrustConflictRetryUsesNewVersion(t *testing.T) {
	reads, writes := 0, 0
	got, seen := launchTrust(t, nil, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			reads++
			version := trustLayerVersion
			if reads > 1 {
				version = trustRetryVersion
			}
			result := trustReadResult(cwd, false)
			result["layers"].([]any)[0].(map[string]any)["version"] = version
			return trustReply{result: result}
		case "config/value/write":
			writes++
			if writes == 1 {
				return trustReply{fault: trustVersionConflict()}
			}
			ok := trustWriteResult()
			ok["version"] = trustRetryVersion
			return trustReply{result: ok}
		}
		return trustReply{}
	})
	versions := writeExpectedVersions(seen)
	if got.Outcome != apiv1.ThreadResponded || got.Error != nil || len(versions) != 2 || versions[0] != trustLayerVersion || versions[1] != trustRetryVersion {
		t.Fatalf("got %s/%+v versions %v", got.Outcome, got.Error, versions)
	}
}

// (e) A write result that is not a confirmed ok is codex_trust_unconfirmed.
// Each case sent one config/value/write.
func TestCodexRepoTrustUnconfirmedWriteResults(t *testing.T) {
	cases := []struct {
		name   string
		result any
	}{
		{name: "okOverridden", result: map[string]any{"filePath": "/home/dev/.codex/config.toml", "status": "okOverridden", "version": trustLayerVersion}},
		{name: "malformed", result: []any{}},
		{name: "relativeFilePath", result: map[string]any{"filePath": "config.toml", "status": "ok", "version": trustLayerVersion}},
		{name: "badVersion", result: map[string]any{"filePath": "/home/dev/.codex/config.toml", "status": "ok", "version": "not-a-version"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, seen := launchTrust(t, nil, func(cwd, method string) trustReply {
				switch method {
				case "config/read":
					return trustReply{result: trustReadResult(cwd, false)}
				case "config/value/write":
					return trustReply{result: tc.result}
				}
				return trustReply{}
			})
			refuseTrust(t, got, seen, "codex_trust_unconfirmed", 1)
		})
	}
}

// (f) A regular boot record reached through a symlinked ancestor is refused
// before any config/value/write. The final file is not a symlink, so the
// refusal is the ancestor check.
func TestCodexRepoTrustBootRecordAncestorSymlink(t *testing.T) {
	got, seen := launchTrust(t, func(t *testing.T, s Settings) {
		t.Helper()
		held := s.StateDir + "-real"
		if err := os.Rename(s.StateDir, held); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(held, s.StateDir); err != nil {
			t.Fatal(err)
		}
		parent, err := os.Lstat(s.StateDir)
		if err != nil || parent.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("state dir %v %v", parent, err)
		}
		final, err := os.Lstat(filepath.Join(s.StateDir, bootFile))
		if err != nil || !final.Mode().IsRegular() || final.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("boot record %v %v", final, err)
		}
	}, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			return trustReply{result: trustReadResult(cwd, false)}
		case "config/value/write":
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	refuseTrust(t, got, seen, "folder_not_qualified", 0)
}

// (g) A boot record over the size limit is refused before any
// config/value/write. The bytes are still a Ready record, so the refusal is
// the limit.
func TestCodexRepoTrustBootRecordOverLimit(t *testing.T) {
	got, seen := launchTrust(t, func(t *testing.T, s Settings) {
		t.Helper()
		path := s.statePath(bootFile)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.TrimRight(string(body), "\n")
		if text == "" || text[len(text)-1] != '}' {
			t.Fatalf("boot record does not end as one object")
		}
		padded := text[:len(text)-1] + `,"pad":"` + strings.Repeat("a", codexBootstrapBound) + `"}`
		if len(padded) <= codexBootstrapBound {
			t.Fatalf("padded record is %d bytes", len(padded))
		}
		var boot bootRecord
		if json.Unmarshal([]byte(padded), &boot) != nil || boot.ProjectBootstrap == nil {
			t.Fatal("padded record is not a readable bootstrap")
		}
		ready := false
		for _, item := range boot.ProjectBootstrap.Repositories {
			if item.Name == "dev-env" && item.State == protocol.BootstrapReady {
				ready = true
			}
		}
		if !ready {
			t.Fatal("padded record does not name the repository Ready")
		}
		if err := os.WriteFile(path, []byte(padded), 0o600); err != nil {
			t.Fatal(err)
		}
	}, func(cwd, method string) trustReply {
		switch method {
		case "config/read":
			return trustReply{result: trustReadResult(cwd, false)}
		case "config/value/write":
			return trustReply{result: trustWriteResult()}
		}
		return trustReply{}
	})
	refuseTrust(t, got, seen, "folder_not_qualified", 0)
}
