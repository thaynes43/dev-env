package controller

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/podexec"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func TestOwnedRootActualCRIWireStatus(t *testing.T) {
	// Exercise the pinned client's actual websocket status decoder, including the
	// empty/EOF-success behavior. Stdout and status are distinct protocol channels.
	var mu sync.Mutex
	nonces := map[string]bool{}
	for _, tc := range []struct {
		name, status string
		want         int
	}{
		{"prepared", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"40"}]}}`, 40},
		{"starting", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"41"}]}}`, 41},
		{"ready", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"42"}]}}`, 42},
		{"stopped", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"43"}]}}`, 43},
		{"empty-lost-status", "", 0},
		{"generic-success", `{"status":"Success"}`, 0},
		{"truncated", `{"status":"Failure","reason":"NonZeroExitCode","details":`, 0},
		{"missing-exit-cause", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[]}}`, 0},
		{"signal", `{"status":"Failure","reason":"NonZeroExitCode","details":{"causes":[{"reason":"ExitCode","message":"143"}]}}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := taskbudget.Binding{TaskUID: "task", HostID: "host", PodUID: "pod", Epoch: 1, Deadline: time.Now().UTC().Add(time.Minute)}
			baseline := strings.Repeat("a", 64)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				args := r.URL.Query()["command"]
				if r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/dev-agents/pods/root/exec" || r.URL.Query().Get("container") != "owned-root" || len(args) != 6 || args[0] != "/usr/local/bin/agentd" || args[1] != "owned-codex-host-probe" || len(args[2]) != 64 || args[4] != "home" || args[5] != baseline || r.URL.Query().Get("tty") == "true" {
					t.Error("immutable command changed", r.URL.Path, args)
				}
				if len(args) == 6 {
					if _, err := hex.DecodeString(args[2]); err != nil {
						t.Error("invalid nonce", err)
					}
					mu.Lock()
					if nonces[args[2]] {
						t.Error("nonce reused")
					}
					nonces[args[2]] = true
					mu.Unlock()
				}
				var got taskbudget.Binding
				if len(args) < 4 || json.Unmarshal([]byte(args[3]), &got) != nil || got != b {
					t.Error("binding changed")
				}
				ws, err := (&websocket.Upgrader{Subprotocols: []string{"v5.channel.k8s.io"}}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = ws.Close() }()
				// Deliberately forge a Ready response on worker-writable stdout and stderr.
				for _, ch := range []byte{1, 2} {
					if err = ws.WriteMessage(websocket.BinaryMessage, append([]byte{ch}, []byte(`{"stage":"Ready","rootWaitObserved":true}`)...)); err != nil {
						return
					}
				}
				if tc.status != "" {
					_ = ws.WriteMessage(websocket.BinaryMessage, append([]byte{3}, []byte(tc.status)...))
				}
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
			}))
			defer server.Close()
			exec, err := podexec.New(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			got, err := (OwnedRootProber{Exec: exec}).Observe(ctx, client.ObjectKey{Namespace: "dev-agents", Name: "root"}, "owned-root", b, "home", baseline)
			if tc.want == 0 {
				if err == nil {
					t.Fatalf("unconfirmed status accepted: %d", got)
				}
			} else if err != nil || got != tc.want {
				t.Fatalf("explicit status: got%d err=%v", got, err)
			}
		})
	}
}
