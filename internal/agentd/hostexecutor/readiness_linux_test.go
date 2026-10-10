//go:build linux

package hostexecutor

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A finite inert Unix listener exercises the actual peer credential and native
// WebSocket wire boundary. No Codex CLI, credentials, provider or model is used.
func TestNativeReadinessPeerAndVersion(t *testing.T) {
	for _, tc := range []struct {
		name, version string
		wrongPeer     bool
		want          bool
	}{
		{"ready", PinnedNativeVersion, false, true}, {"version-mismatch", "0.159.1", false, false}, {"wrong-peer", PinnedNativeVersion, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "control.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			initialized := make(chan bool, 1)
			srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = ws.Close() }()
				_ = ws.SetReadDeadline(time.Now().Add(time.Second))
				_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
				var req struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if ws.ReadJSON(&req) != nil || req.ID != 1 || req.Method != "initialize" {
					return
				}
				if ws.WriteJSON(map[string]any{"id": 1, "result": map[string]string{"userAgent": "fixture/" + tc.version + " linux"}}) != nil {
					return
				}
				var note struct {
					Method string `json:"method"`
				}
				initialized <- ws.ReadJSON(&note) == nil && note.Method == "initialized"
			})}
			defer func() { _ = srv.Close() }()
			go func() { _ = srv.Serve(listener) }()
			root, err := identity(os.Getpid())
			if err != nil {
				t.Fatal(err)
			}
			if tc.wrongPeer {
				root.PID = os.Getppid()
				root, err = identity(root.PID)
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			defer cancel()
			if got := probeNative(ctx, path, root, PinnedNativeVersion); (got == nil) != tc.want {
				t.Fatalf("ready=%v err=%v", tc.want, got)
			}
			if tc.want {
				select {
				case ok := <-initialized:
					if !ok {
						t.Fatal("initialized missing")
					}
				case <-ctx.Done():
					t.Fatal("notification deadline")
				}
			}
		})
	}
}

func TestReadinessFailureStopsOwnedTree(t *testing.T) {
	c := config(t)
	c.ReadyVersion = PinnedNativeVersion
	r, err := Run(context.Background(), c, fixtureGate{home: c.Home, hold: true})
	if err == nil || r.State != "Stopped" || r.NativeReady || r.Reason != "native readiness refused" || !r.RootWaitObserved || !r.TreeReaped {
		t.Fatalf("failed readiness/stop proof: %+v %v", r, err)
	}
	if rootLive(r.Root) {
		t.Fatal("owned root survived readiness refusal")
	}
	if data, err := os.ReadFile(filepath.Join(c.Home, "wip")); err != nil || string(data) != "uncommitted work\n" {
		t.Fatal("WIP was lost")
	}
}

func serveReadinessFixture(home string) error {
	listener, err := net.Listen("unix", filepath.Join(home, "rc.sock"))
	if err != nil {
		return err
	}
	srv := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		_ = ws.SetReadDeadline(time.Now().Add(time.Second))
		_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if ws.ReadJSON(&req) != nil || req.ID != 1 || req.Method != "initialize" {
			return
		}
		if ws.WriteJSON(map[string]any{"id": 1, "result": map[string]string{"userAgent": "fixture/" + PinnedNativeVersion}}) != nil {
			return
		}
		var note struct {
			Method string `json:"method"`
		}
		_ = ws.ReadJSON(&note)
	})}
	go func() { _ = srv.Serve(listener) }()
	return nil
}

func TestOwnedTreeNativeReady(t *testing.T) {
	c := config(t)
	c.ReadyVersion = PinnedNativeVersion
	if os.WriteFile(filepath.Join(c.Home, "fixture-readiness"), nil, 0o600) != nil {
		t.Fatal("fixture marker")
	}
	r, err := Run(context.Background(), c, fixtureGate{home: c.Home})
	if err != nil || r.State != "Stopped" || !r.NativeReady || r.NativeVersion != PinnedNativeVersion || !r.TreeReaped {
		t.Fatalf("owned authenticated ready/stop: %+v %v", r, err)
	}
}

func TestNativeReadinessCanceled(t *testing.T) {
	root, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if probeNative(ctx, filepath.Join(t.TempDir(), "absent.sock"), root, PinnedNativeVersion) == nil || time.Since(start) > 100*time.Millisecond {
		t.Fatal("canceled readiness did not refuse promptly")
	}
}
