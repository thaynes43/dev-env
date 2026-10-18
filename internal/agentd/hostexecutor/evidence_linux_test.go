//go:build linux

package hostexecutor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeHomeBaselineStableAndRejectsAliases(t *testing.T) {
	c := config(t)
	path := filepath.Join(c.Home, ".codex", "snapshot.json")
	// Force relatime to advance on the first content read. Atime is not content.
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(path, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	same, err := NativeHomeBaseline(c.Home)
	if err != nil || same != baseline {
		t.Fatal("unchanged baseline refused", err)
	}
	auth := filepath.Join(c.Home, ".codex", "auth.json")
	if err := os.WriteFile(auth, []byte("rotating fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	same, err = NativeHomeBaseline(c.Home)
	if err != nil || same != baseline {
		t.Fatal("rotating access changed baseline", err)
	}
	if err := os.WriteFile(path, []byte("new recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := NativeHomeBaseline(c.Home)
	if err != nil || changed == baseline {
		t.Fatal("changed recovery accepted", err)
	}
	for _, kind := range []string{"auth-directory", "auth-hardlink", "auth-symlink", "recovery-symlink", "directory-symlink"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".codex")
			if os.Mkdir(root, 0700) != nil {
				t.Fatal("directory")
			}
			file := filepath.Join(home, "real")
			if os.WriteFile(file, []byte("fixture"), 0600) != nil {
				t.Fatal("file")
			}
			var err error
			switch kind {
			case "auth-directory":
				err = os.Mkdir(filepath.Join(root, "auth.json"), 0700)
			case "auth-hardlink":
				err = os.Link(file, filepath.Join(root, "auth.json"))
			case "auth-symlink":
				err = os.Symlink(file, filepath.Join(root, "auth.json"))
			case "recovery-symlink":
				err = os.Symlink(file, filepath.Join(root, "resume.json"))
			case "directory-symlink":
				err = os.Symlink(home, filepath.Join(root, "nested"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NativeHomeBaseline(home); err == nil {
				t.Fatal("alias/special auth accepted")
			}
		})
	}
}
func TestProbeIsolationScopeIsFailClosed(t *testing.T) {
	for _, raw := range []string{"", "0\n", "4", "-1", "1 garbage"} {
		if validPtraceScope(raw) {
			t.Fatalf("scope %q accepted", raw)
		}
	}
	for _, raw := range []string{"1\n", "2", "3"} {
		if !validPtraceScope(raw) {
			t.Fatalf("scope %q refused", raw)
		}
	}
}

type evidenceGate struct {
	c       Config
	stopped bool
	ready   bool
}

func (g *evidenceGate) observe(stage string) (Observation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	proof, err := ProbeEvidence(ctx, filepath.Join(filepath.Dir(g.c.ReceiptPath), EvidenceSocketName), os.Getpid(), g.c.Binding, strings.Repeat("ab", 32))
	if err == nil && proof.Stage != stage {
		return proof, errors.New("wrong actual guard phase: " + proof.Stage)
	}
	return proof, err
}
func (g *evidenceGate) Admit(context.Context, Binding) error {
	proof, err := g.observe("Prepared")
	if err == nil && proof.Root.PID != 0 {
		return errors.New("root exists before admission")
	}
	return err
}
func (g *evidenceGate) Observe(context.Context, Binding) (bool, error) {
	proof, err := g.observe("Ready")
	if err != nil {
		return true, err
	}
	if proof.Root.PID < 2 || proof.NativeVersion != PinnedNativeVersion {
		return true, errors.New("native proof")
	}
	g.ready = true
	return true, nil
}
func runEvidenceLifecycle(t *testing.T, pid1 bool) {
	t.Helper()
	c := config(t)
	c.ReadyVersion = PinnedNativeVersion
	if err := os.WriteFile(filepath.Join(c.Home, "fixture-readiness"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	gate := &evidenceGate{c: c}
	c.Evidence = &EvidenceConfig{HomeUID: "independent-home-uid", Baseline: baseline, RequirePID1: pid1, ReportStopped: func(ctx context.Context, r Receipt) error {
		proof, err := gate.observe("Stopped")
		if err != nil {
			return err
		}
		if !proof.RootWaitObserved || !proof.TreeReaped || proof.Root != r.Root {
			return errors.New("owned wait/reap not independently observed")
		}
		gate.stopped = true
		return nil
	}}
	r, err := Run(context.Background(), c, gate)
	if err != nil || !gate.ready || !gate.stopped || r.State != "Stopped" {
		t.Fatalf("actual lifecycle: ready=%v stopped=%v receipt=%+v err=%v", gate.ready, gate.stopped, r, err)
	}
	if _, err = Run(context.Background(), c, gate); !errors.Is(err, ErrNeedsReview) {
		t.Fatal("one-use root rearmed", err)
	}
}
func TestOwnedRootEvidenceActualLifecycle(t *testing.T) { runEvidenceLifecycle(t, false) }

// This is invoked in CI in a new PID namespace as nonroot UID1000. Ordinary
// local suites explicitly do not claim PID1/isolation acceptance by running it.
func TestOwnedRootPID1Evidence(t *testing.T) {
	if os.Getpid() != 1 {
		if os.Getenv("AGENTD_TEST_REQUIRE_PID1") == "true" {
			t.Fatal("PID1 fixture was not isolated")
		}
		t.Skip("requires bounded CI PID namespace")
	}
	if os.Geteuid() != 1000 {
		t.Fatal("PID1 fixture must run as nonroot1000")
	}
	runEvidenceLifecycle(t, true)
}
