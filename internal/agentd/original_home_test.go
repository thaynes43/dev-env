package agentd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/homeguard"
)

func TestOriginalHomeProviderBoundary(t *testing.T) {
	for _, race := range []string{"unchanged", "deleted after init", "replaced before native", "PV changed before native", "suspended before retry", "forbidden"} {
		t.Run(race, func(t *testing.T) {
			b := homeguard.Binding{Namespace: "ns", Session: "remote", SessionUID: "session-uid", PVCUID: "pvc-uid", PV: "pv", PVUID: "pv-uid"}
			session := map[string]any{"metadata": map[string]any{"namespace": "ns", "name": "remote", "uid": "session-uid", "annotations": map[string]string{homeguard.Annotation: b.String()}}, "spec": map[string]any{"agent": "codex", "mode": "remote", "operatingMode": "Running"}}
			pvc := map[string]any{"metadata": map[string]any{"namespace": "ns", "name": "home-remote", "uid": "pvc-uid", "finalizers": []string{homeguard.RescueFinalizer}, "ownerReferences": []any{map[string]any{"apiVersion": "dev-env.haynesops.com/v1alpha1", "kind": "AgentSession", "name": "remote", "uid": "session-uid", "controller": true}}}, "spec": map[string]any{"volumeName": "pv"}, "status": map[string]any{"phase": "Bound"}}
			pv := map[string]any{"metadata": map[string]any{"name": "pv", "uid": "pv-uid", "finalizers": []string{"kubernetes.io/pv-protection"}}, "spec": map[string]any{"claimRef": map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "name": "home-remote", "namespace": "ns", "uid": "pvc-uid"}}, "status": map[string]any{"phase": "Bound"}}
			denied := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer synthetic" {
					t.Error("not fixed authenticated read")
				}
				if denied {
					w.WriteHeader(403)
					return
				}
				var obj any
				switch {
				case strings.Contains(r.URL.Path, "agentsessions/"):
					obj = session
				case strings.Contains(r.URL.Path, "persistentvolumeclaims/"):
					obj = pvc
				default:
					obj = pv
				}
				_ = json.NewEncoder(w).Encode(obj)
			}))
			defer server.Close()
			check := func(ctx context.Context) error {
				return verifyOriginalHomeAPI(ctx, server.Client(), server.URL, "synthetic", b)
			}
			// Real verifier succeeds after the synthetic mount/init boundary.
			if err := check(context.Background()); err != nil {
				t.Fatal("init", err)
			}
			world := newFakeCodexWorld(t)
			deps := world.deps()
			checks := 0
			deps.home = func(ctx context.Context) error {
				checks++
				if checks == 1 {
					switch race {
					case "deleted after init":
						pvc["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-30T00:00:00Z"
					case "forbidden":
						denied = true
					}
				}
				if checks == 2 {
					switch race {
					case "replaced before native":
						pvc["metadata"].(map[string]any)["uid"] = "replacement"
					case "PV changed before native":
						pv["spec"].(map[string]any)["claimRef"].(map[string]any)["uid"] = "replacement"
					}
				}
				return check(ctx)
			}
			remote := &codexRemote{deps: deps, boot: "fixture"}
			err := remote.ensure(context.Background(), world.s, time.Now())
			if race == "unchanged" || race == "suspended before retry" {
				if err != nil || len(world.calls) != 1 || !strings.HasSuffix(world.calls[0], "remote-control start --json") {
					t.Fatalf("native calls=%v err=%v", world.calls, err)
				}
				if race == "suspended before retry" {
					world.live = false
					session["spec"].(map[string]any)["operatingMode"] = "Suspended"
					if err := remote.ensure(context.Background(), world.s, time.Now().Add(10*time.Minute)); err == nil || len(world.calls) != 1 {
						t.Fatal("retry bypassed guard")
					}
				}
			} else if err == nil || len(world.calls) != 0 {
				t.Fatalf("unsafe native dispatch %v err=%v", world.calls, err)
			}
		})
	}
}
func TestOriginalHomeDefaultOff(t *testing.T) {
	t.Setenv(homeguard.Env, "")
	if err := VerifyOriginalHome(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestOriginalHomeDoesNotRestartActiveDaemon(t *testing.T) {
	w := newFakeCodexWorld(t)
	w.live = true
	d := w.deps()
	d.home = func(context.Context) error { t.Fatal("active remote was gated/restarted"); return nil }
	c := &codexRemote{deps: d}
	if err := c.ensure(context.Background(), w.s, time.Now()); err != nil || len(w.calls) != 0 {
		t.Fatal("active RC changed")
	}
}
