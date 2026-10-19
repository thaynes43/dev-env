//go:build linux

package hostexecutor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	binary, err := os.Executable()
	if err != nil {
		return Observation{}, err
	}
	rawBinding, _ := json.Marshal(g.c.Binding)
	cmd := exec.CommandContext(ctx, binary, "fixture-evidence-probe", filepath.Join(filepath.Dir(g.c.ReceiptPath), EvidenceSocketName), strconv.Itoa(os.Getpid()), string(rawBinding), g.c.Evidence.HomeUID, g.c.Evidence.Baseline)
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	raw, err := cmd.Output()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || !exited.Exited() || !ValidEvidenceExit(exited.ExitCode()) {
		return Observation{}, fmt.Errorf("separate probe refused (%s): %w", diagnostic.String(), err)
	}
	var proof Observation
	err = json.Unmarshal(raw, &proof)
	if err == nil && EvidenceExit(proof, g.c.Binding, g.c.Evidence.HomeUID, g.c.Evidence.Baseline) != exited.ExitCode() {
		return proof, errors.New("test output differs from explicit process status")
	}
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
	// Match the production serialized binding shape, including nanosecond UTC
	// deadline and original logical identifiers; no API or provider is involved.
	c.Binding = Binding{TaskUID: "owned-luna-1019-0004", Epoch: 1, HostID: "owned-luna-1019-4", PodUID: "5dc5b644-277f-457a-a30f-96410e3c6c16", Deadline: time.Now().UTC().Add(600 * time.Second)}
	c.ReadyVersion = PinnedNativeVersion
	if err := os.WriteFile(filepath.Join(c.Home, "fixture-readiness"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	gate := &evidenceGate{c: c}
	refusals := make(chan string, 4)
	afterStopped := false
	c.Evidence = &EvidenceConfig{HomeUID: "independent-home-uid", Baseline: baseline, RequirePID1: pid1, ReportRefusal: func(stage string) { refusals <- stage }, ReportStopped: func(ctx context.Context, r Receipt) error {
		proof, err := gate.observe("Stopped")
		if err != nil {
			return err
		}
		if !proof.RootWaitObserved || !proof.TreeReaped || proof.Root != r.Root {
			return errors.New("owned wait/reap not independently observed")
		}
		gate.stopped = true
		return nil
	}, CheckContinuation: func() bool { return true }, AfterStopped: func(r Receipt) error {
		if !gate.stopped {
			return errors.New("hook preceded stop acknowledgement")
		}
		proof, err := gate.observe("Stopped")
		if err != nil {
			return err
		}
		if !proof.ContinuationReady || EvidenceExit(proof, c.Binding, "independent-home-uid", baseline) != EvidenceContinuable {
			return errors.New("original guard not serving continuable stop")
		}
		afterStopped = true
		return nil
	}}
	gate.c = c
	r, err := Run(context.Background(), c, gate)
	if err != nil || !gate.ready || !gate.stopped || !afterStopped || r.State != "Stopped" {
		select {
		case stage := <-refusals:
			t.Logf("actual guard refusal stage=%s", stage)
		case <-time.After(100 * time.Millisecond):
			t.Log("no guard refusal stage received")
		}
		t.Fatalf("actual lifecycle: ready=%v stopped=%v receipt=%+v err=%v", gate.ready, gate.stopped, r, err)
	}
	if _, err = Run(context.Background(), c, gate); !errors.Is(err, ErrNeedsReview) {
		t.Fatal("one-use root rearmed", err)
	}

	// The original guard may run one separately receipted epoch while retaining
	// the original policy baseline and opaque native HOME. This inert test grants
	// no budget authority; API/service tests separately verify the owner transition.
	oldReceipt, err := os.ReadFile(c.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := c.ReceiptPath
	if st, e := os.Lstat(c.SocketPath); e == nil && st.Mode()&os.ModeSocket != 0 {
		if e = os.Remove(c.SocketPath); e != nil {
			t.Fatal(e)
		}
	}
	actual, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	c.Binding.Epoch = 2
	c.ReceiptPath = filepath.Join(filepath.Dir(oldPath), "owned-host-epoch-2.json")
	gate = &evidenceGate{c: c}
	c.Evidence = &EvidenceConfig{HomeUID: "independent-home-uid", Baseline: baseline, ResumeBaseline: actual, RequirePID1: pid1, ReportStopped: func(ctx context.Context, r Receipt) error {
		proof, e := gate.observe("Stopped")
		if e != nil {
			return e
		}
		if proof.Baseline != baseline || proof.StartupBaseline != actual || proof.Guard.PID != os.Getpid() || !proof.RootWaitObserved || !proof.TreeReaped {
			return errors.New("continuation lost original guard/policy or wait/reap")
		}
		gate.stopped = true
		return nil
	}}
	gate.c = c
	second, err := Run(context.Background(), c, gate)
	if err != nil || !gate.ready || !gate.stopped || second.Binding.Epoch != 2 || second.Root == r.Root {
		t.Fatal("separate native-process continuation", err)
	}
	retained, err := os.ReadFile(oldPath)
	if err != nil || string(retained) != string(oldReceipt) {
		t.Fatal("old receipt changed", err)
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

func TestOwnedRootEvidenceOneIdlePeerDoesNotStarveProbe(t *testing.T) {
	c := config(t)
	baseline, err := NativeHomeBaseline(c.Home)
	if err != nil {
		t.Fatal(err)
	}
	e := &EvidenceConfig{HomeUID: "home-original", Baseline: baseline}
	if err := e.start(c); err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	path := filepath.Join(filepath.Dir(c.ReceiptPath), EvidenceSocketName)
	idle, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idle.Close() }()
	// The idle peer occupies its entire500ms request allowance. This independent
	// trusted observation must complete within250ms, before that allowance expires.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	proof, err := ProbeEvidence(ctx, path, os.Getpid(), c.Binding, strings.Repeat("cd", 32))
	if err != nil || proof.Stage != "Prepared" {
		t.Fatal("idle peer starved actual probe", err)
	}
}

func TestContinuationHookRequiresExplicitStopAcknowledgement(t *testing.T) {
	called := false
	e := &EvidenceConfig{ReportStopped: func(context.Context, Receipt) error { return errors.New("lost stop acknowledgement") }, AfterStopped: func(Receipt) error { called = true; return nil }}
	if err := e.reportStopped(Config{Binding: Binding{Deadline: time.Now().Add(time.Minute)}}, Receipt{State: "Stopped", RootWaitObserved: true, TreeReaped: true}); err == nil || called {
		t.Fatal("lost stop status authorized continuation")
	}
}
