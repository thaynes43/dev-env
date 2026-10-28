package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

func captureFixture(t *testing.T) (Settings, CodexHostOptions, allowanceCaptureDeps, *int) {
	t.Helper()
	home := t.TempDir()
	s := Settings{Home: home, CodexHome: filepath.Join(home, ".codex"), PodUID: "pod", WorkspaceID: "workspace", CodexBin: "fixture"}
	bin := filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	live := codexHostProcess{State: "Live", PID: 42, Identity: codexHostIdentity{BootID: "boot", StartTicks: 100}}
	calls := new(int)
	d := allowanceCaptureDeps{
		host: codexHostDeps{preflight: func(Settings) error { return nil }, process: func(Settings) (codexHostProcess, error) { return live, nil }, native: func(context.Context, Settings, string, []string, io.Writer) ([]byte, error) {
			return []byte("codex-cli 0.160.1"), nil
		}},
		lifecycle: func(Settings) (codexHostLifecycle, error) {
			return codexHostLifecycle{Phase: "Confirmed", PodUID: s.PodUID, WorkspaceID: s.WorkspaceID, Process: live}, nil
		},
		read: func(_ context.Context, _ Settings, pid int, _ time.Time, c CodexAllowanceCapture) (providerallowance.ReadResponse, error) {
			*calls++
			if pid != 42 || !c.valid() || c.Sequence != 1 {
				t.Fatal("invalid capture identity")
			}
			return providerallowance.ReadResponse{}, nil
		},
		nonce: func() (string, error) { return "test-invocation", nil }, now: func() time.Time { return time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC) }, preparationAbsent: func(Settings) bool { return true },
	}
	return s, CodexHostOptions{Enabled: true}, d, calls
}

func TestAllowanceCaptureRefusesBeforeRead(t *testing.T) {
	for _, name := range []string{"disabled", "cancelled", "missing pod", "preflight", "absent", "retained", "foreign pod", "foreign workspace", "replaced", "legacy", "version", "churn before", "private pending", "bad context"} {
		t.Run(name, func(t *testing.T) {
			s, o, d, calls := captureFixture(t)
			ctx := context.Background()
			switch name {
			case "disabled":
				o.Enabled = false
			case "cancelled":
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			case "missing pod":
				s.PodUID = ""
			case "preflight":
				d.host.preflight = func(Settings) error { return errors.New("private") }
			case "absent":
				d.host.process = func(Settings) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
			case "retained", "foreign pod", "foreign workspace", "replaced", "legacy":
				prior := d.lifecycle
				d.lifecycle = func(s Settings) (codexHostLifecycle, error) {
					v, e := prior(s)
					switch name {
					case "retained":
						v.Phase = "Attempting"
					case "foreign pod":
						v.PodUID = "other"
					case "foreign workspace":
						v.WorkspaceID = "other"
					case "replaced":
						v.Process.Identity.StartTicks++
					case "legacy":
						return v, errors.New("unbound")
					}
					return v, e
				}
			case "version":
				d.host.native = func(context.Context, Settings, string, []string, io.Writer) ([]byte, error) {
					return []byte("codex-cli 0.159.1"), nil
				}
			case "churn before":
				prior := d.host.process
				n := 0
				d.host.process = func(s Settings) (codexHostProcess, error) {
					p, e := prior(s)
					n++
					if n == 2 {
						p.Identity.StartTicks++
					}
					return p, e
				}
			case "private pending":
				s.PrivateCodexHost = true
				d.preparationAbsent = func(Settings) bool { return false }
			case "bad context":
				d.nonce = func() (string, error) { return "bad/identifier", nil }
			}
			if _, err := captureCodexNativeAllowance(ctx, s, o, d); err == nil || *calls != 0 {
				t.Fatal("invalid context reached reader")
			}
		})
	}
}
func TestAllowanceCaptureRechecksAfterRead(t *testing.T) {
	for _, name := range []string{"stable", "churn", "lifecycle", "read error"} {
		t.Run(name, func(t *testing.T) {
			s, o, d, calls := captureFixture(t)
			if name == "churn" {
				prior := d.host.process
				n := 0
				d.host.process = func(s Settings) (codexHostProcess, error) {
					p, e := prior(s)
					n++
					if n == 3 {
						p.PID++
					}
					return p, e
				}
			}
			if name == "lifecycle" {
				prior := d.lifecycle
				n := 0
				d.lifecycle = func(s Settings) (codexHostLifecycle, error) {
					v, e := prior(s)
					n++
					if n == 3 {
						v.PodUID = "new-pod"
					}
					return v, e
				}
			}
			if name == "read error" {
				d.read = func(context.Context, Settings, int, time.Time, CodexAllowanceCapture) (providerallowance.ReadResponse, error) {
					*calls++
					return providerallowance.ReadResponse{}, errors.New("SECRET")
				}
			}
			_, err := captureCodexNativeAllowance(context.Background(), s, o, d)
			if *calls != 1 || (err == nil) != (name == "stable") || err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unexpected capture outcome", err)
			}
		})
	}
}
func TestAllowanceCaptureSyntheticNativeSequence(t *testing.T) {
	f := fakeAllowanceHost(t, "credits-zero")
	s, o, d, _ := captureFixture(t)
	// Use only a synthetic local fixture; the reader checks its peer PID/UID.
	s.CodexHome = f.settings.CodexHome
	bin := filepath.Join(s.CodexHome, "packages", "app-server-daemon", "current", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	live := codexHostProcess{State: "Live", PID: os.Getpid(), Identity: codexHostIdentity{BootID: "fixture", StartTicks: 1}}
	d.host.process = func(Settings) (codexHostProcess, error) { return live, nil }
	d.lifecycle = func(Settings) (codexHostLifecycle, error) {
		return codexHostLifecycle{Phase: "Confirmed", PodUID: s.PodUID, WorkspaceID: s.WorkspaceID, Process: live}, nil
	}
	d.read = ReadCodexNativeAllowance
	result, err := captureCodexNativeAllowance(context.Background(), s, o, d)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil || strings.Contains(string(raw), "SYNTHETIC_PRIVATE") || strings.Contains(string(raw), "SYNTHETIC_SECRET") {
		t.Fatal("private account data escaped")
	}
	calls := f.recorded()
	want := []string{"initialize", "initialized", "account/read", "account/rateLimits/read"}
	if len(calls) != len(want) {
		t.Fatal(calls)
	}
	for i, v := range want {
		if calls[i].Method != v {
			t.Fatal(calls)
		}
	}
	if calls[2].Params["refreshToken"] != false || calls[3].Params["excludeResetCreditDetails"] != true || calls[3].Params["supportsLunaReserve"] != false {
		t.Fatal("unexpected RPC parameters")
	}
}
