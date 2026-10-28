package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func TestOwnedAllowanceReadOnlyProjectionFence(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), CodexAccessFile: filepath.Join(home, "projection")}
	raw, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if _, _, err := ownedAllowanceProjection(s, now); err == nil {
		t.Fatal("missing fence accepted")
	}
	if err := SyncCodexAccess(s, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	native, receipt, err := ownedAllowanceProjection(s, now)
	if err != nil || len(native) == 0 {
		t.Fatal("matching projection refused", err)
	}
	dest := filepath.Join(home, "observer")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := ownedAllowanceWriteAuth(dest, native); err != nil {
		t.Fatal(err)
	}
	if err := ownedAllowanceWriteAuth(dest, native); err == nil {
		t.Fatal("existing auth overwritten")
	}
	after, err := os.ReadFile(s.statePath("codex-access-generation.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("observer changed fence")
	}
	changed := syntheticAgentCodexAccess(t, now, 1)
	changed.LastRefresh = now.Add(-time.Minute)
	changedRaw, err := codexauth.Encode(changed, now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(changedRaw))
	if ownedAllowanceStillCurrent(s, now, receipt) {
		t.Fatal("changed fingerprint accepted")
	}
	newer, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 2), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(newer))
	if ownedAllowanceStillCurrent(s, now, receipt) {
		t.Fatal("uncommitted newer projection accepted")
	}
	s.CodexAccessFile = ""
	if _, _, err := ownedAllowanceProjection(s, now); err == nil {
		t.Fatal("missing projection accepted")
	}
}

func TestOwnedAllowanceRuntimeDirectoryOwnership(t *testing.T) {
	info, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	if !ownedAllowanceRuntimeDirectory(info, uid) || ownedAllowanceRuntimeDirectory(info, uid+1) {
		t.Fatal("runtime directory ownership was not enforced")
	}
}

type ownedFixtureOwner struct {
	listener net.Listener
	stopErr  error
	live     bool
}

func (o *ownedFixtureOwner) PID() int    { return os.Getpid() }
func (o *ownedFixtureOwner) Alive() bool { return o.live }
func (o *ownedFixtureOwner) Stop() error {
	o.live = false
	if o.listener != nil {
		_ = o.listener.Close()
	}
	return o.stopErr
}

func TestOwnedAllowanceSyntheticBindingAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "missing projection", "wrong session", "version", "binary change", "preexisting", "runtime symlink", "expired", "session drift", "fence drift", "stop uncertain"} {
		t.Run(mode, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "oa-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(home) })
			now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
			s := Settings{Home: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), CodexAccessFile: filepath.Join(home, "projection"), PodUID: "pod", RuntimeDir: home}
			a, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, s.CodexAccessFile, string(a))
			if err := SyncCodexAccess(s, now); err != nil {
				t.Fatal(err)
			}
			session := protocol.Session{Name: "fixture", SessionUID: "session-1", Agent: protocol.AgentCodex, Mode: protocol.ModeRemote}
			calls := 0
			current := OwnedAllowanceContext{Session: session, PodUID: s.PodUID}
			deps := ownedAllowanceDeps{binary: func() (os.FileInfo, error) { return os.Stat(s.CodexAccessFile) }, now: func() time.Time { return now }, nonce: func() (string, error) { return "fixture", nil }, version: func(context.Context, Settings) error { return nil }}
			deps.start = func(_ context.Context, observer string) (ownedAllowanceOwner, error) {
				calls++
				sock := filepath.Join(observer, ".codex", "app-server-control", "app-server-control.sock")
				ln, e := net.Listen("unix", sock)
				if e != nil {
					return nil, e
				}
				owner := &ownedFixtureOwner{listener: ln, live: true}
				if mode == "stop uncertain" {
					owner.stopErr = errors.New("secret synthetic stop error")
				}
				return owner, nil
			}
			deps.read = func(context.Context, Settings, int, time.Time, CodexAllowanceCapture) (providerallowance.ReadResponse, error) {
				if mode == "session drift" {
					current.PodUID = "replacement"
				}
				if mode == "fence drift" {
					raw, _ := codexauth.Encode(syntheticAgentCodexAccess(t, now, 2), now)
					writeFile(t, s.CodexAccessFile, string(raw))
				}
				return providerallowance.ReadResponse{}, nil
			}
			switch mode {
			case "missing projection":
				s.CodexAccessFile = ""
			case "wrong session":
				session.Agent = protocol.AgentClaude
			case "version":
				deps.version = func(context.Context, Settings) error { return errors.New("wrong") }
			case "binary change":
				deps.version = func(context.Context, Settings) error {
					if err := os.Rename(s.CodexAccessFile, s.CodexAccessFile+".old"); err != nil {
						t.Fatal(err)
					}
					writeFile(t, s.CodexAccessFile, string(a))
					return nil
				}
			case "runtime symlink":
				link := filepath.Join(home, "alias")
				if err := os.Symlink(home, link); err != nil {
					t.Fatal(err)
				}
				s.RuntimeDir = link
			case "expired":
				deps.now = func() time.Time { return now.Add(11 * 24 * time.Hour) }
			case "preexisting":
				if err := os.Mkdir(filepath.Join(home, "codex-owned-allowance-observer"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err = captureOwnedAllowance(context.Background(), s, session, func() (OwnedAllowanceContext, error) { return current, nil }, deps)
			if (err == nil) != (mode == "success") {
				t.Fatal("unexpected outcome", mode, err)
			}
			if (mode == "missing projection" || mode == "wrong session" || mode == "version" || mode == "binary change" || mode == "preexisting" || mode == "runtime symlink" || mode == "expired") && calls != 0 {
				t.Fatal("invalid request launched")
			}
			if mode == "stop uncertain" {
				if _, err := os.Stat(filepath.Join(home, "codex-owned-allowance-observer")); err != nil {
					t.Fatal("unproven cleanup removed")
				}
			}
		})
	}
}

func TestOwnedAllowanceActualSyntheticRPC(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "oa-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	s := Settings{Home: home, RuntimeDir: home, StateDir: filepath.Join(home, ".agentd"), CodexHome: filepath.Join(home, ".codex"), CodexAccessFile: filepath.Join(home, "projection"), PodUID: "pod"}
	raw, err := codexauth.Encode(syntheticAgentCodexAccess(t, now, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, s.CodexAccessFile, string(raw))
	if err := SyncCodexAccess(s, now); err != nil {
		t.Fatal(err)
	}
	session := protocol.Session{Name: "fixture", SessionUID: "session-1", Agent: protocol.AgentCodex, Mode: protocol.ModeRemote}
	current := OwnedAllowanceContext{Session: session, PodUID: s.PodUID}
	var calls []allowanceRequest
	var mu sync.Mutex
	deps := ownedAllowanceDeps{binary: func() (os.FileInfo, error) { return os.Stat(s.CodexAccessFile) }, now: func() time.Time { return now }, nonce: func() (string, error) { return "fixture", nil }, version: func(context.Context, Settings) error { return nil }, read: ReadCodexNativeAllowance}
	deps.start = func(_ context.Context, observer string) (ownedAllowanceOwner, error) {
		path := filepath.Join(observer, ".codex", "app-server-control", "app-server-control.sock")
		ln, e := net.Listen("unix", path)
		if e != nil {
			return nil, e
		}
		server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ws, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
			if e != nil {
				return
			}
			defer func() { _ = ws.Close() }()
			for {
				var req allowanceRequest
				if ws.ReadJSON(&req) != nil {
					return
				}
				mu.Lock()
				calls = append(calls, req)
				mu.Unlock()
				if req.ID == nil {
					continue
				}
				var result any
				switch req.Method {
				case "initialize":
					result = map[string]any{"userAgent": "codex/0.160.1"}
				case "account/read":
					result = map[string]any{"account": map[string]string{"email": "SYNTHETIC_PRIVATE"}}
				case "account/rateLimits/read":
					result = map[string]any{"rateLimits": map[string]any{"primary": map[string]any{"usedPercent": 0}}}
				default:
					return
				}
				if ws.WriteJSON(map[string]any{"id": *req.ID, "result": result}) != nil {
					return
				}
			}
		})}
		go func() { _ = server.Serve(ln) }()
		return &ownedFixtureOwner{listener: ln, live: true}, nil
	}
	result, err := captureOwnedAllowance(context.Background(), s, session, func() (OwnedAllowanceContext, error) { return current, nil }, deps)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || strings.Contains(string(encoded), "SYNTHETIC_PRIVATE") {
		t.Fatal("private account payload escaped")
	}
	if _, err = providerallowance.DecodeReadResponse(encoded, providerallowance.FixtureParseLimits); err != nil {
		t.Fatal("noncanonical output", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	if len(calls) != len(want) {
		t.Fatalf("methods %v", calls)
	}
	for i, m := range want {
		if calls[i].Method != m {
			t.Fatal("wrong method sequence")
		}
	}
	if calls[2].Params["refreshToken"] != false || calls[3].Params["excludeResetCreditDetails"] != true || calls[3].Params["supportsLunaReserve"] != false {
		t.Fatal("wrong RPC flags")
	}
}
