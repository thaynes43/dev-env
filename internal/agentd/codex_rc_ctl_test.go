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
	root := filepath.Join(s.Home, "codex", "sample", "repo")
	for _, dir := range []string{s.StateDir, filepath.Join(s.StateDir, "project-roots"), root, filepath.Join(s.ConfigDir, "codex")} {
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
	info, _ := os.Stat(root)
	st := info.Sys().(*syscall.Stat_t)
	now := time.Now().UTC()
	target := apiv1.CodexRCTarget{SessionName: "host", SessionUID: "session", PodUID: "pod", Project: "sample", Repository: "repo", SnapshotSHA256: apiv1.CodexRCHash(snapshot), Root: root, RootDevice: st.Dev, RootInode: st.Ino, PreparationSHA256: strings.Repeat("a", 64), PID: 222, BootID: "boot", StartTicks: 55, InstallationID: "installation", EnvironmentID: "environment", PairingSource: "supported-client", PairingSHA256: strings.Repeat("b", 64), VerifiedAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute)}

	head := strings.Repeat("a", 40)
	operation := PrivateProjectRootReport{Project: "sample", State: "Ready", CatalogRevision: snapshot.CatalogRevision(), RulesRevision: snapshot.RulesRevision(), Head: head, ObservedAt: now}
	owned := privateRootReceipt{Version: 1, Root: filepath.Dir(root), Head: head, Snapshot: data, Repositories: []PrivateProjectSource{{Repository: snapshot.Selected(), State: "Ready"}}}
	receipt, _ := json.Marshal(owned)
	if err := os.WriteFile(filepath.Join(s.StateDir, "project-roots", "sample.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	operationBytes, _ := json.Marshal(operation)
	if err := os.WriteFile(filepath.Join(s.StateDir, "project-roots", "sample.operation.json"), operationBytes, 0600); err != nil {
		t.Fatal(err)
	}
	preparation, _ := json.Marshal(PrivateProjectReport{Version: 1, State: "Ready", FinishedAt: now, CatalogRevision: snapshot.CatalogRevision(), Projects: []PrivateProjectRootReport{operation}})
	if err := os.WriteFile(s.statePath(privateProjectBootstrapFile), preparation, 0600); err != nil {
		t.Fatal(err)
	}
	target.PreparationSHA256 = projectcatalog.Digest(receipt)

	data, _ = json.Marshal(target)
	if err := os.WriteFile(filepath.Join(s.ConfigDir, CodexRCTargetFile), data, 0600); err != nil {
		t.Fatal(err)
	}
	d := apiv1.CodexRCDispatch{Caller: "system:serviceaccount:test:human", Target: target, ClientSHA256: strings.Repeat("c", 64), Request: apiv1.CodexRCRequest{RequestID: "request", SessionUID: "session", PodUID: "pod", Project: "sample", Repository: "repo", Model: "gpt-6.1-sol", Effort: "medium", Handoff: "initial bounded handoff", Deadline: now.Add(25 * time.Second)}}
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
	for _, mode := range []string{"missing-config", "session", "pod", "mode", "repo", "root", "inode", "snapshot", "preparation", "operation", "pending", "anchor", "process", "boot", "ticks", "pairing", "expires", "symlink", "state-mode"} {
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
				_ = os.WriteFile(filepath.Join(s.StateDir, "project-roots", "sample.json"), []byte(`{}`), 0600)
			case "preparation":
				_ = os.WriteFile(s.statePath(privateProjectBootstrapFile), []byte(`{}`), 0600)
			case "operation":
				_ = os.WriteFile(filepath.Join(s.StateDir, "project-roots", "sample.operation.json"), []byte(`{}`), 0600)
			case "pending":
				_ = os.WriteFile(filepath.Join(s.StateDir, "project-roots", "sample.pending.json"), []byte(`{}`), 0600)
			case "anchor":
				d.Target.Root = filepath.Dir(d.Target.Root)
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

func TestCodexRCRemotePreparationProducerNeedsNoTaskSnapshot(t *testing.T) {
	for _, selection := range []struct{ declaration, repo string }{{projectTestCatalog, "demo"}, {twoRepositoryRootDeclaration(), "extra"}} {
		t.Run(selection.repo, func(t *testing.T) {
			_, prepared, runner, source := privateRootsFixture(t, selection.declaration)
			owned := requirePrivateRootReady(t, prepared, runner, source)
			if exists(prepared.statePath("project-snapshot.json")) {
				t.Fatal("remote producer unexpectedly wrote task snapshot")
			}
			configured, _, d, deps, _ := rcControlFixture(t)
			prepared.ConfigDir = configured.ConfigDir
			prepared.PodUID = d.Target.PodUID
			catalog, err := projectcatalog.Parse([]byte(selection.declaration))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := catalog.Snapshot("sample", selection.repo)
			if err != nil {
				t.Fatal(err)
			}
			d.Request.Repository = snapshot.Selected().Name
			d.Target.Repository = d.Request.Repository
			d.Target.SnapshotSHA256 = apiv1.CodexRCHash(snapshot)
			d.Target.Root = filepath.Join(owned.Root, d.Target.Repository)
			info, err := os.Stat(d.Target.Root)
			if err != nil {
				t.Fatal(err)
			}
			st := info.Sys().(*syscall.Stat_t)
			d.Target.RootDevice, d.Target.RootInode = st.Dev, st.Ino
			d.Target.PreparationSHA256 = projectcatalog.Digest(mustRead(t, privateRootReceiptPath(prepared)))
			data, _ := json.Marshal(d.Target)
			if err := os.WriteFile(filepath.Join(prepared.ConfigDir, CodexRCTargetFile), data, 0600); err != nil {
				t.Fatal(err)
			}
			session := protocol.Session{Name: d.Target.SessionName, SessionUID: d.Target.SessionUID, Agent: "codex", Mode: "remote"}
			rec, err := codexRCControl(context.Background(), prepared, session, "preflight", d, deps)
			if err != nil || rec.State != "qualified" {
				t.Fatal("actual remote preparation refused", rec, err)
			}
		})
	}
}
