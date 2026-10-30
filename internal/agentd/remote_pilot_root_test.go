package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
	fixture "github.com/thaynes43/dev-env/internal/homeguard/testutil"
)

// Stage B consumes stage A's committed rendered Pod fields, not a separately
// assembled Session/env tuple. The real metadata verifier uses a TLS fake API;
// the existing Codex process/remote fakes prevent all real provider contact.
func TestRemotePilotRootStageB(t *testing.T) {
	for _, scenario := range []string{
		"unchanged", "Session UID mismatch", "PVC UID mismatch", "PV UID mismatch",
		"Session deleting", "PVC deleting", "same-name replaced Session before native",
		"PVC replaced before native", "PV replaced before native", "forbidden PV read",
	} {
		t.Run(scenario, func(t *testing.T) {
			f := fixture.Load(t)
			b := f.Binding(t)
			pod := fixture.LoadGolden(t).Selected
			if pod.Name != b.Session || pod.Namespace != b.Namespace || pod.SessionUID != b.SessionUID || pod.HomeClaim != b.Claim() || pod.Main.Image != f.RemoteImage || pod.Init.Image != f.RemoteImage || !pod.Init.HomeReadOnly || !slices.Equal(pod.Init.Args, []string{"home-verify"}) {
				t.Fatal("stage A link differs from Root fixture")
			}
			sess, err := LoadSession(func(k string) string { return pod.Main.Env[k] })
			if err != nil || sess.Name != b.Session || sess.SessionUID != b.SessionUID || sess.Repo != "dev-env" || !codexRemoteSession(sess) || sess.Prompt != "" || sess.Workspace != nil {
				t.Fatal("rendered native session changed", err)
			}
			var session, pvc, pv map[string]any
			fixture.Decode(t, f.Session, &session)
			fixture.Decode(t, f.PVC, &pvc)
			fixture.Decode(t, f.PV, &pv)
			meta := func(obj map[string]any) map[string]any { return obj["metadata"].(map[string]any) }
			meta(session)["annotations"] = map[string]string{homeguard.Annotation: b.String()}
			switch scenario {
			case "Session UID mismatch":
				meta(session)["uid"] = "mismatch"
			case "PVC UID mismatch":
				meta(pvc)["uid"] = "mismatch"
			case "PV UID mismatch":
				meta(pv)["uid"] = "mismatch"
			case "Session deleting":
				meta(session)["deletionTimestamp"] = "2026-10-30T00:00:00Z"
			case "PVC deleting":
				meta(pvc)["deletionTimestamp"] = "2026-10-30T00:00:00Z"
			}
			paths := []string{
				"/apis/dev-env.haynesops.com/v1alpha1/namespaces/" + b.Namespace + "/agentsessions/" + b.Session,
				"/api/v1/namespaces/" + b.Namespace + "/persistentvolumeclaims/" + b.Claim(),
				"/api/v1/persistentvolumes/" + b.PV,
			}
			var reads []string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads = append(reads, r.URL.Path)
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer offline-fixture" {
					t.Error("verifier exceeded fixed authenticated metadata GETs")
					w.WriteHeader(403)
					return
				}
				var obj any
				switch r.URL.Path {
				case paths[0]:
					obj = session
				case paths[1]:
					obj = pvc
				case paths[2]:
					if scenario == "forbidden PV read" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					obj = pv
				default:
					t.Error("unexpected Kubernetes path", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				_ = json.NewEncoder(w).Encode(obj)
			}))
			defer server.Close()
			verify := func(ctx context.Context, raw string) error {
				binding, err := homeguard.Parse(raw)
				if err != nil {
					return err
				}
				return verifyOriginalHomeAPI(ctx, server.Client(), server.URL, "offline-fixture", *binding)
			}
			world := newFakeCodexWorld(t)
			// Preserve existing local fake HOME/auth and process/tmux isolation;
			// only native is extended with a harmless argv-recording executable.
			world.s.WorkspaceID = ""
			argvFile := filepath.Join(t.TempDir(), "argv")
			bin := filepath.Join(t.TempDir(), "codex")
			writeHostFixture(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$REMOTE_PILOT_ARGV\"\nprintf '{\"status\":\"started\"}\\n'\n")
			if err := os.Chmod(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			world.s.CodexBin = bin
			deps := world.deps()
			fakeNative := deps.native
			deps.native = func(ctx context.Context, s Settings, binary string, args []string, out io.Writer) ([]byte, error) {
				if binary != bin || !slices.Equal(args, []string{"remote-control", "start", "--json"}) {
					t.Fatal("unexpected provider/pair/turn invocation")
				}
				if _, err := fakeNative(ctx, s, binary, args, out); err != nil {
					return nil, err
				}
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env = append(os.Environ(), "REMOTE_PILOT_ARGV="+argvFile)
				return cmd.Output()
			}
			checks := 0
			deps.home = func(ctx context.Context) error {
				checks++
				if checks == 2 { // immediately before the native spawn
					switch scenario {
					case "same-name replaced Session before native":
						meta(session)["uid"] = "replacement"
					case "PVC replaced before native":
						meta(pvc)["uid"] = "replacement"
					case "PV replaced before native":
						meta(pv)["uid"] = "replacement"
					}
				}
				return verify(ctx, pod.Main.Env[homeguard.Env])
			}
			ctx := context.Background()
			initErr := verify(ctx, pod.Init.Env[homeguard.Env]) // existing home-verify implementation
			if initErr == nil {
				old := codexRemoteDepsFor
				codexRemoteDepsFor = func() codexRemoteDeps { return deps }
				t.Cleanup(func() { codexRemoteDepsFor = old })
				tmux := &fakeRunner{} // existing fake; remote must never call it
				d := &Daemon{S: world.s, Session: sess, R: tmux, Now: time.Now}
				step, why := d.startAgent(ctx, protocol.Workspace{}, Step{}, "offline-root")
				if d.crm == nil || why != "" || step.State == StepFail {
					t.Fatal("rendered session did not reach supervised remote boundary", why)
				}
				err = d.crm.lastErr
				if len(tmux.lines()) != 0 {
					t.Fatal("remote start invoked tmux", tmux.lines())
				}
			} else {
				err = initErr
			}
			if scenario != "unchanged" {
				if !errors.Is(err, homeguard.ErrUnsafe) || len(world.calls) != 0 {
					t.Fatalf("unsafe native start: calls=%v err=%v", world.calls, err)
				}
				if _, err := os.Stat(argvFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("provider executable ran after guard refusal")
				}
				return
			}
			if err != nil || checks != 2 || !reflect.DeepEqual(world.calls, []string{bin + " remote-control start --json"}) {
				t.Fatalf("native boundary calls=%v checks=%d err=%v", world.calls, checks, err)
			}
			argv, err := os.ReadFile(argvFile)
			if err != nil || string(argv) != "remote-control\nstart\n--json\n" {
				t.Fatalf("native process argv=%q err=%v", argv, err)
			}
			if len(reads) != 9 {
				t.Fatalf("init and both native checks must each read all three identities: %v", reads)
			}
			for i, path := range reads {
				if path != paths[i%3] {
					t.Fatal("metadata check order changed")
				}
			}
		})
	}
}
