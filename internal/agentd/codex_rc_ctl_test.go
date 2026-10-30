package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/projectcatalog"
)

func rcControlFixture(t *testing.T) (Settings, protocol.Session, apiv1.CodexRCDispatch, codexRCDeps, *int) {
	t.Helper()
	base := t.TempDir()
	s := Settings{Home: filepath.Join(base, "home"), StateDir: filepath.Join(base, "home", ".agentd"), ConfigDir: filepath.Join(base, "config"), PodUID: "pod"}
	root := filepath.Join(s.Home, "work", "repo")
	for _, dir := range []string{s.StateDir, root, filepath.Join(s.ConfigDir, "codex")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err := projectcatalog.Parse([]byte(`{"version":1,"repositories":{"repo":{"github":"thaynes43/dev-env"}},"projects":{"sample":{"repositories":[{"name":"repo"}],"rules":"rule"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot("sample", "repo")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(snapshot)
	if err := os.WriteFile(s.statePath("project-snapshot.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(root)
	st := info.Sys().(*syscall.Stat_t)
	now := time.Now().UTC()
	target := apiv1.CodexRCTarget{SessionName: "host", SessionUID: "session", PodUID: "pod", Project: "sample", Repository: "repo", SnapshotSHA256: apiv1.CodexRCHash(snapshot), Root: root, RootDevice: uint64(st.Dev), RootInode: st.Ino, PreparationSHA256: strings.Repeat("a", 64), PID: 222, BootID: "boot", StartTicks: 55, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("b", 64), VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute)}
	preparation, _ := json.Marshal(PrivateProjectReport{Version: 1, State: "Ready", CatalogRevision: snapshot.CatalogRevision(), Repositories: []PrivateProjectSource{{Repository: snapshot.Selected(), State: "Ready"}}})
	if err := os.WriteFile(s.statePath(privateProjectBootstrapFile), preparation, 0600); err != nil {
		t.Fatal(err)
	}
	target.PreparationSHA256 = projectcatalog.Digest(preparation)
	data, _ = json.Marshal(target)
	if err := os.WriteFile(filepath.Join(s.ConfigDir, CodexRCTargetFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	d := apiv1.CodexRCDispatch{Target: target, ClientSHA256: strings.Repeat("c", 64), Request: apiv1.CodexRCRequest{RequestID: "request", SessionUID: "session", PodUID: "pod", Project: "sample", Repository: "repo", Model: "gpt-6.1-sol", Effort: "medium", Handoff: "initial bounded handoff", Deadline: now.Add(25 * time.Second)}}
	mutations := new(int)
	deps := realCodexRCDeps()
	deps.now = func() time.Time { return now }
	deps.process = func(Settings, codexHostFS) (codexHostProcess, error) {
		return codexHostProcess{State: "Live", PID: 222, Identity: codexHostIdentity{BootID: "boot", StartTicks: 55}}, nil
	}
	deps.start = func(ctx context.Context, _ Settings, pid int, selection codexRCThreadSelection, consume func(context.Context) error) (codexRCThreadReceipt, error) {
		if pid != 222 || selection.Cwd != root || selection.Model != d.Request.Model || selection.Effort != d.Request.Effort || selection.Handoff != d.Request.Handoff {
			t.Fatal("binding drift")
		}
		if err := consume(ctx); err != nil {
			return codexRCThreadReceipt{}, err
		}
		*mutations++
		if err := selection.BeforeTurn(ctx); err != nil {
			return codexRCThreadReceipt{ThreadID: "thread"}, err
		}
		*mutations++
		return codexRCThreadReceipt{ThreadID: "thread", TurnID: "turn", Cwd: root, Model: d.Request.Model}, nil
	}
	return s, protocol.Session{Name: "host", SessionUID: "session", Agent: "codex", Mode: "remote"}, d, deps, mutations
}
func TestCodexRCControlQualificationAndOneUse(t *testing.T) {
	s, sess, d, deps, count := rcControlFixture(t)
	rec, err := codexRCControl(context.Background(), s, sess, "preflight", d, deps)
	if err != nil || rec.State != "qualified" || *count != 0 || exists(s.statePath(codexRCOnceFile)) {
		t.Fatal(rec, err, *count)
	}
	rec, err = codexRCControl(context.Background(), s, sess, "start", d, deps)
	if err != nil || rec.State != "confirmed" || *count != 2 {
		t.Fatal(rec, err, *count)
	}
	fence := mustRead(t, s.statePath(codexRCOnceFile))
	// New invocation/process and new request ID cannot replay a native mutation.
	for _, key := range []string{"request", "different"} {
		d.Request.RequestID = key
		rec, err = codexRCControl(context.Background(), s, sess, "start", d, deps)
		if err != nil || rec.State == "confirmed" || *count != 2 {
			t.Fatal(rec, err, *count)
		}
	}
	if string(fence) != string(mustRead(t, s.statePath(codexRCOnceFile))) {
		t.Fatal("fence replaced")
	}
}
func TestCodexRCControlRefusesMismatchesWithoutMutation(t *testing.T) {
	for _, mode := range []string{"missing-config", "session", "pod", "mode", "repo", "root", "inode", "snapshot", "preparation", "process", "boot", "ticks", "pairing", "expires", "symlink", "state-mode"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, d, deps, count := rcControlFixture(t)
			switch mode {
			case "missing-config":
				_ = os.Remove(filepath.Join(s.ConfigDir, CodexRCTargetFile))
			case "session":
				sess.SessionUID = "new"
			case "pod":
				s.PodUID = "new"
			case "mode":
				sess.Mode = "task"
			case "repo":
				d.Request.Repository = "other"
			case "root":
				d.Target.Root = "/tmp"
			case "inode":
				d.Target.RootInode++
			case "snapshot":
				_ = os.WriteFile(s.statePath("project-snapshot.json"), []byte(`{}`), 0600)
			case "preparation":
				_ = os.WriteFile(s.statePath(privateProjectBootstrapFile), []byte(`{}`), 0600)
			case "process":
				deps.process = func(Settings, codexHostFS) (codexHostProcess, error) { return codexHostProcess{State: "Absent"}, nil }
			case "boot":
				d.Target.BootID = "other"
			case "ticks":
				d.Target.StartTicks++
			case "pairing":
				d.Target.PairingSource = "pair-code-issued"
			case "expires":
				d.Target.ExpiresAt = deps.now()
			case "symlink":
				_ = os.Rename(d.Target.Root, d.Target.Root+"-original")
				_ = os.Symlink(d.Target.Root+"-original", d.Target.Root)
			case "state-mode":
				_ = os.Chmod(s.StateDir, 0755)
			}
			if mode != "missing-config" {
				data, _ := json.Marshal(d.Target)
				if err := os.WriteFile(filepath.Join(s.ConfigDir, CodexRCTargetFile), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			rec, err := codexRCControl(context.Background(), s, sess, "start", d, deps)
			if err != nil || rec.State != "refused" || *count != 0 || exists(s.statePath(codexRCOnceFile)) {
				t.Fatal(rec, err, *count)
			}
		})
	}
}
func TestCodexRCControlLostAckAndDurabilityFailureNeverReplay(t *testing.T) {
	for _, mode := range []string{"lost-thread-ack", "lost-turn-ack", "fsync"} {
		t.Run(mode, func(t *testing.T) {
			s, sess, d, deps, count := rcControlFixture(t)
			if mode == "fsync" {
				deps.consume = func(path string, data []byte, perm os.FileMode) error {
					if err := writeFileOnce(path, data, perm); err != nil {
						return err
					}
					return errors.New("directory fsync unknown")
				}
			}
			deps.start = func(ctx context.Context, _ Settings, _ int, _ codexRCThreadSelection, consume func(context.Context) error) (codexRCThreadReceipt, error) {
				if err := consume(ctx); err != nil {
					return codexRCThreadReceipt{}, err
				}
				*count++
				if mode == "lost-turn-ack" {
					*count++
				}
				return codexRCThreadReceipt{}, errors.New("lost ACK")
			}
			rec, _ := codexRCControl(context.Background(), s, sess, "start", d, deps)
			if rec.State == "confirmed" {
				t.Fatal(rec)
			}
			before := *count
			rec, _ = codexRCControl(context.Background(), s, sess, "start", d, deps)
			if *count != before || rec.State == "confirmed" {
				t.Fatal("replayed", rec, *count)
			}
		})
	}
}
