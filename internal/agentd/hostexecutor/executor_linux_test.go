//go:build linux

package hostexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMain supplies inert foreground, intermediate and leaf processes. The
// intermediate and leaf each create a new session: a process-group-only stop
// would miss the orphan leaf. They perform no network, auth or model calls.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "owned-codex-host-helper":
			if Helper() != nil {
				os.Exit(1)
			}
			os.Exit(0)
		case "fixture-supervisor":
			data, err := os.ReadFile(os.Args[2])
			if err != nil {
				os.Exit(2)
			}
			var c Config
			if json.Unmarshal(data, &c) != nil {
				os.Exit(2)
			}
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
			defer stop()
			if _, err = Run(ctx, c, fixtureGate{home: c.Home, hold: true}); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		case "app-server":
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "wip"), []byte("uncommitted work\n"), 0o600); err != nil {
				os.Exit(2)
			}
			child := exec.Command(os.Args[0], "fixture-middle")
			child.SysProcAttr = &unix.SysProcAttr{Setsid: true}
			if err := child.Run(); err != nil {
				os.Exit(2)
			}
			time.Sleep(20 * time.Second)
			os.Exit(0)
		case "fixture-middle":
			child := exec.Command(os.Args[0], "fixture-leaf")
			child.SysProcAttr = &unix.SysProcAttr{Setsid: true}
			if err := child.Start(); err != nil {
				os.Exit(2)
			}
			os.Exit(0)
		case "fixture-leaf":
			id, err := identity(os.Getpid())
			if err != nil {
				os.Exit(2)
			}
			data, err := json.Marshal(id)
			if err != nil {
				os.Exit(2)
			}
			if err = os.WriteFile(filepath.Join(os.Getenv("HOME"), "leaf.json"), data, 0o600); err != nil {
				os.Exit(2)
			}
			time.Sleep(20 * time.Second)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

type fixtureGate struct {
	home        string
	unavailable bool
	hold        bool
}

func (g fixtureGate) Admit(context.Context, Binding) error { return nil }
func (g fixtureGate) Observe(context.Context, Binding) (bool, error) {
	if g.hold {
		return false, nil
	}
	if g.unavailable {
		return false, errors.New("authority unavailable")
	}
	_, err := os.Stat(filepath.Join(g.home, "leaf.json"))
	return err == nil, nil
}
func config(t *testing.T) Config {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"enrollment.json", "snapshot.json"} {
		if err = os.WriteFile(filepath.Join(home, ".codex", name), []byte("inert preserved fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Config{Binding: Binding{TaskUID: "task-test", Epoch: 1, Deadline: time.Now().Add(10 * time.Second), HostID: "host-test", PodUID: "pod-test"}, HelperBinary: binary, NativeBinary: binary, Home: home, SocketPath: filepath.Join(home, "rc.sock"), ReceiptPath: filepath.Join(home, "receipt.json"), StopTimeout: 3 * time.Second}
}
func TestOwnedTreeLatch(t *testing.T) {
	c := config(t)
	r, err := Run(context.Background(), c, fixtureGate{home: c.Home})
	if err != nil {
		t.Fatalf("owned stop: %v, receipt=%+v", err, r)
	}
	if r.State != "Stopped" || !r.RootWaitObserved || !r.TreeReaped {
		t.Fatalf("no exact stop proof: %+v", r)
	}
	data, err := os.ReadFile(filepath.Join(c.Home, "leaf.json"))
	if err != nil {
		t.Fatal(err)
	}
	var leaf Identity
	if err = json.Unmarshal(data, &leaf); err != nil {
		t.Fatal(err)
	}
	for _, id := range []Identity{r.Root, leaf} {
		current, e := identity(id.PID)
		if e == nil && same(current, id) {
			t.Fatalf("owned process still exists: %+v", id)
		}
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			t.Fatal(e)
		}
	}
	wip, err := os.ReadFile(filepath.Join(c.Home, "wip"))
	if err != nil || string(wip) != "uncommitted work\n" {
		t.Fatal("WIP was changed")
	}
	for _, name := range []string{"enrollment.json", "snapshot.json"} {
		data, e := os.ReadFile(filepath.Join(c.Home, ".codex", name))
		if e != nil || string(data) != "inert preserved fixture\n" {
			t.Fatal("private home artifact changed")
		}
	}
	info, err := os.Stat(c.ReceiptPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("receipt not private")
	}
	_, err = Run(context.Background(), c, fixtureGate{home: c.Home})
	if !errors.Is(err, ErrNeedsReview) {
		t.Fatal("automatic rearm allowed", err)
	}
}
func TestAuthorityErrorStopsOwnedTree(t *testing.T) {
	c := config(t)
	r, err := Run(context.Background(), c, fixtureGate{home: c.Home, unavailable: true})
	if err != nil || r.State != "Stopped" || r.Reason != "budget authority unavailable" {
		t.Fatalf("authority stop: %+v %v", r, err)
	}
}
func TestDisabledAndInterruptedReceipt(t *testing.T) {
	c := config(t)
	if _, err := Run(context.Background(), c, nil); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.ReceiptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled path wrote receipt")
	}
	original := []byte(`{"state":"Stopping","reason":"interrupted"}`)
	if err := os.WriteFile(c.ReceiptPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), c, fixtureGate{}); !errors.Is(err, ErrNeedsReview) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(c.ReceiptPath)
	if err != nil || string(after) != string(original) {
		t.Fatal("interrupted receipt changed")
	}
}
func TestUnknownAndImmutableIdentity(t *testing.T) {
	c := config(t)
	r := Receipt{Binding: c.Binding, State: "Stopping"}
	ch := make(chan error, 1)
	ch <- nil
	result, err := finish(c, r, message{Kind: "unknown"}, ch)
	if err == nil || result.State == "Stopped" {
		t.Fatal("unknown was promoted")
	}
	self, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	wrong := self
	wrong.StartTicks++
	if same(self, wrong) {
		t.Fatal("PID reuse identity accepted")
	}
	// The root mismatch is rejected before any signal can target this process.
	wrong.PID = os.Getpid()
	if err = stopTree(wrong, ch, time.Second); err == nil {
		t.Fatal("wrong root identity accepted")
	}
	if _, err = os.Stat("/proc/" + strconv.Itoa(os.Getpid())); err != nil {
		t.Fatal(fmt.Errorf("unrelated process signaled: %w", err))
	}
}

func TestStopDeadlineIsUnknown(t *testing.T) {
	c := config(t)
	root, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	done <- nil
	if err = stopTree(root, done, -time.Second); err == nil {
		t.Fatal("expired stop budget accepted")
	}
	r := Receipt{Binding: c.Binding, State: "Stopping", Root: root}
	if err = save(c.ReceiptPath, r); err != nil {
		t.Fatal(err)
	}
	result, err := finish(c, r, message{Kind: "unknown", Root: root}, done)
	if err == nil || result.State != "Stopping" {
		t.Fatal("deadline uncertainty was discarded")
	}
	bytes, err := os.ReadFile(c.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved Receipt
	if err = json.Unmarshal(bytes, &saved); err != nil || saved.State != "Stopping" {
		t.Fatal("unknown stop receipt lost")
	}
}
func TestStopProofMustMatchRoot(t *testing.T) {
	c := config(t)
	r := Receipt{Binding: c.Binding, State: "Stopping", Root: Identity{PID: 1, BootID: "boot", StartTicks: 1}}
	done := make(chan error, 1)
	done <- nil
	_, err := finish(c, r, message{Kind: "stopped", Root: Identity{PID: 1, BootID: "boot", StartTicks: 2}, RootWaitObserved: true, TreeReaped: true}, done)
	if err == nil {
		t.Fatal("different process stop proof accepted")
	}
}

func TestSignalActiveSupervisorStopsDetachedTree(t *testing.T) {
	c := config(t)
	path := filepath.Join(c.Home, "config.json")
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(c.HelperBinary, "fixture-supervisor", path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	until := time.Now().Add(3 * time.Second)
	var leaf Identity
	for {
		data, e := os.ReadFile(filepath.Join(c.Home, "leaf.json"))
		if e == nil {
			if json.Unmarshal(data, &leaf) == nil {
				break
			}
		}
		if time.Now().After(until) {
			t.Fatal("finite fixture did not become active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signaled supervisor did not exit")
	}
	data, err = os.ReadFile(c.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var r Receipt
	if err = json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.State != "Stopped" || !r.TreeReaped || r.Reason != "supervisor canceled" {
		t.Fatalf("signal stop not proved: %+v", r)
	}
	if current, e := identity(leaf.PID); e == nil && same(current, leaf) {
		t.Fatal("setsid/double-fork descendant survived")
	}
}
func TestHelperDeadlineStopsTreeWithoutLatch(t *testing.T) {
	c := config(t)
	c.Binding.Deadline = time.Now().Add(1500 * time.Millisecond)
	r, err := Run(context.Background(), c, fixtureGate{home: c.Home, hold: true})
	if err != nil || r.State != "Stopped" || !r.TreeReaped {
		t.Fatalf("independent deadline: %+v %v", r, err)
	}
}
func TestWrongStartIdentityCannotSignalLiveChild(t *testing.T) {
	c := config(t)
	cmd := exec.Command(c.NativeBinary, "fixture-leaf")
	cmd.Env = append(os.Environ(), "HOME="+c.Home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	id, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	wrong := id
	wrong.StartTicks++
	done := make(chan error, 1)
	if err = stopTree(wrong, done, time.Second); err == nil {
		t.Fatal("PID with changed start identity accepted")
	}
	current, err := identity(id.PID)
	if err != nil || !same(current, id) {
		t.Fatal("unmatched child was signaled")
	}
}

func TestReapedChildAfterPidfdOpenIsGone(t *testing.T) {
	c := config(t)
	cmd := exec.Command(c.NativeBinary, "fixture-leaf")
	cmd.Env = append(os.Environ(), "HOME="+c.Home)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	fd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("fixture was not killed")
	}
	owner, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	gone, err := checkParent(cmd.Process.Pid, owner, owner)
	if err != nil || !gone {
		t.Fatalf("reaped pidfd child became uncertain ancestry: gone=%v err=%v", gone, err)
	}
}

type noAdmissionGate struct{ called bool }

func (g *noAdmissionGate) Admit(context.Context, Binding) error {
	g.called = true
	return errors.New("must not admit")
}
func (*noAdmissionGate) Observe(context.Context, Binding) (bool, error) { return true, nil }
func TestInterruptedReceiptRefusesBeforeAuthorityAdmission(t *testing.T) {
	c := config(t)
	if err := os.WriteFile(c.ReceiptPath, []byte(`{"state":"Attempting"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := &noAdmissionGate{}
	if _, err := Run(context.Background(), c, gate); !errors.Is(err, ErrNeedsReview) {
		t.Fatal(err)
	}
	if gate.called {
		t.Fatal("authority mutated before interrupted receipt refusal")
	}
}
