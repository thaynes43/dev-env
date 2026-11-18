package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeVersionedHostReceipt(t *testing.T, s Settings, phase, version string) []byte {
	t.Helper()
	live := codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture-boot", StartTicks: 45}}
	saved := codexHostLifecycle{Version: 1, Phase: phase, PodUID: s.PodUID, WorkspaceID: s.WorkspaceID, CLI: version, Process: live}
	if s.PrivateCodexHost {
		saved.Private = &codexHostPrivateBinding{Namespace: "fixture", Name: "catalog", UID: "fixture-uid", ResourceVersion: "1", CatalogRevision: "fixture-revision", GuardDigest: "fixture-guard"}
		saved.Preparation, _ = json.Marshal(codexHostPreparation{Version: 1, PodUID: s.PodUID, Binding: *saved.Private, ConfigDigest: strings.Repeat("a", 64), ConfigReceiptDigest: strings.Repeat("b", 64)})
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	writeHostFixture(t, s.statePath("codex-host-start-intent.json"), string(raw))
	return raw
}

func TestCodexHostReceiptVersions(t *testing.T) {
	for _, version := range []string{previousPinnedCodexVersion, managedCodexVersion, "0.168.0"} {
		for _, private := range []bool{false, true} {
			for _, phase := range []string{"Preparing", "Attempting", "Confirming", "Confirmed", "Stopping", "Stopped"} {
				name := version + "/shared/" + phase
				if private {
					name = version + "/private/" + phase
				}
				t.Run(name, func(t *testing.T) {
					s, o := hostFixture(t)
					s.PrivateCodexHost, s.ProjectCatalog = private, "fixture/catalog"
					before := writeVersionedHostReceipt(t, s, phase, version)
					valid := version != "0.168.0" && (private || phase != "Preparing" && phase != "Confirming")
					_, err := hostLifecycle(s)
					if (err == nil) != valid {
						t.Fatalf("receipt valid=%v: %v", valid, err)
					}
					d, calls := hostDeps(t, s, o)
					d.process = func(Settings) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
					status, err := observeCodexHost(context.Background(), s, o, d)
					want := "Unknown"
					if valid {
						want = "NeedsReview"
						if phase == "Stopped" {
							want = "Absent"
						}
					}
					if status.Code != want || (err == nil) != valid || valid && status.Phase != phase || len(*calls) != 0 {
						t.Fatalf("unexpected observation: %+v %v calls=%v", status, err, *calls)
					}
					if !valid || phase != "Stopped" {
						if startCodexHost(context.Background(), s, o, d) == nil || len(*calls) != 0 {
							t.Fatal("uncompleted or invalid receipt started native client")
						}
					}
					after, _ := os.ReadFile(s.statePath("codex-host-start-intent.json"))
					if !bytes.Equal(before, after) {
						t.Fatal("observation/refusal changed retained receipt")
					}
				})
			}
		}
	}
}

func TestCodexHostReceiptVersionDoesNotRelaxBinding(t *testing.T) {
	for _, version := range []string{previousPinnedCodexVersion, managedCodexVersion} {
		for _, fault := range []string{"workspace", "private-binding", "malformed", "live", "unknown-process"} {
			t.Run(version+"/"+fault, func(t *testing.T) {
				s, o := hostFixture(t)
				writeVersionedHostReceipt(t, s, "Stopped", version)
				switch fault {
				case "workspace":
					s.WorkspaceID = "other-workspace"
				case "private-binding":
					s.PrivateCodexHost = true
				case "malformed":
					writeHostFixture(t, s.statePath("codex-host-start-intent.json"), "{")
				}
				d, calls := hostDeps(t, s, o)
				d.process = func(Settings) (codexHostProcess, error) {
					if fault == "unknown-process" {
						return codexHostProcess{}, errCodexHostUnknown
					}
					state := "Absent"
					if fault == "live" {
						state = "Live"
					}
					return codexHostProcess{State: state}, nil
				}
				if startCodexHost(context.Background(), s, o, d) == nil || len(*calls) != 0 {
					t.Fatal("version compatibility weakened another guard")
				}
			})
		}
	}
}

func TestCodexHostPackageReplacement(t *testing.T) {
	for _, mode := range []string{"previous", "current", "other", "live", "unknown", "failed-update", "wrong-result", "wrong-image", "became-live", "broken-current"} {
		t.Run(mode, func(t *testing.T) {
			s, o := hostFixture(t)
			hostLiveConfig(t, s, o)
			before := writeVersionedHostReceipt(t, s, "Stopped", previousPinnedCodexVersion)
			version := previousPinnedCodexVersion
			if mode == "current" {
				version = managedCodexVersion
			}
			if mode == "other" {
				version = "0.168.0"
			}
			if mode == "broken-current" {
				if err := os.Remove(filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")); err != nil {
					t.Fatal(err)
				}
			}
			updates, checks := 0, 0
			native := func(_ context.Context, _ Settings, bin string, args []string, _ io.Writer) ([]byte, error) {
				if strings.Join(args, " ") == "--version" {
					if bin == s.CodexBin {
						if mode == "wrong-image" {
							return []byte("codex-cli " + previousPinnedCodexVersion), nil
						}
						return []byte("codex-cli " + managedCodexVersion), nil
					}
					return []byte("codex-cli " + version), nil
				}
				if strings.Join(args, " ") != "app-server daemon update --from-cli --yes" {
					t.Fatal(args)
				}
				updates++
				after, _ := os.ReadFile(s.statePath("codex-host-start-intent.json"))
				if !bytes.Equal(before, after) {
					t.Fatal("upgrade replaced receipt")
				}
				if mode == "failed-update" {
					return nil, errors.New("fixture failure")
				}
				if mode != "wrong-result" {
					version = managedCodexVersion
				}
				return nil, nil
			}
			absent := func() error {
				checks++
				if mode == "live" || mode == "unknown" || mode == "became-live" && checks > 1 {
					return errCodexHostUnknown
				}
				return nil
			}
			err := prepareCodexHostPackage(context.Background(), s, native, absent)
			if (err == nil) != (mode == "previous" || mode == "current") {
				t.Fatalf("result %v", err)
			}
			wantUpdates := 0
			if mode == "previous" || mode == "failed-update" || mode == "wrong-result" || mode == "became-live" {
				wantUpdates = 1
			}
			if updates != wantUpdates {
				t.Fatalf("updates %d want %d", updates, wantUpdates)
			}
			after, _ := os.ReadFile(s.statePath("codex-host-start-intent.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("package preparation rewrote receipt")
			}
		})
	}
}

func TestCodexRemotePreviousPackage(t *testing.T) {
	for _, version := range []string{previousPinnedCodexVersion, managedCodexVersion, "0.168.0"} {
		t.Run(version, func(t *testing.T) {
			w := newFakeCodexWorld(t)
			_, o := hostFixture(t)
			hostLiveConfig(t, w.s, o)
			d := w.deps()
			d.home = func(context.Context) error { return nil }
			original := d.native
			selected, updates := version, 0
			d.native = func(ctx context.Context, s Settings, bin string, args []string, out io.Writer) ([]byte, error) {
				switch strings.Join(args, " ") {
				case "--version":
					v := managedCodexVersion
					if bin != s.CodexBin {
						v = selected
					}
					return []byte("codex-cli " + v), nil
				case "app-server daemon update --from-cli --yes":
					updates++
					selected = managedCodexVersion
					return nil, nil
				}
				return original(ctx, s, bin, args, out)
			}
			c := codexRemote{deps: d, boot: "fixture"}
			err := c.ensure(context.Background(), w.s, time.Now())
			if (err == nil) != (version != "0.168.0") {
				t.Fatalf("remote start %v", err)
			}
			want := 0
			if version == previousPinnedCodexVersion {
				want = 1
			}
			if updates != want || version == "0.168.0" && w.starts() != 0 {
				t.Fatal("wrong upgrade/start count")
			}
		})
	}
}

func TestCodexHostLiveReceiptVersions(t *testing.T) {
	for _, version := range []string{previousPinnedCodexVersion, managedCodexVersion, "0.168.0"} {
		for _, phase := range []string{"Preparing", "Attempting", "Confirming", "Confirmed", "Stopping", "Stopped"} {
			t.Run(version+"/"+phase, func(t *testing.T) {
				s, o := hostFixture(t)
				hostLiveConfig(t, s, o)
				writeVersionedHostReceipt(t, s, phase, version)
				d, _ := hostDeps(t, s, o)
				d.process = func(Settings) (codexHostProcess, error) {
					return codexHostProcess{State: "Live", PID: 123, Identity: codexHostIdentity{BootID: "fixture-boot", StartTicks: 45}}, nil
				}
				status, err := observeCodexHost(context.Background(), s, o, d)
				want := "NeedsReview"
				if version == "0.168.0" || phase == "Preparing" || phase == "Confirming" {
					want = "Unknown"
				}
				if version != "0.168.0" && phase == "Confirmed" {
					want = "Observed"
				}
				if status.Code != want || (err != nil) != (want == "Unknown") {
					t.Fatalf("live receipt: %+v %v want %s", status, err, want)
				}
			})
		}
	}
}
