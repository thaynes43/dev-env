//go:build linux

package hostexecutor

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAllowanceSyntheticNative(t *testing.T) {
	switch os.Getenv("OWNED_FIXTURE_NATIVE") {
	case "live":
		<-time.After(20 * time.Second)
	case "exit":
		os.Exit(0)
	}
}

type allowanceDeadlineFixture struct {
	net.Conn
	operation, ack bool
	timeout        bool
}

func (c *allowanceDeadlineFixture) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if bytes.Contains(p, []byte(`"kind":"started"`)) {
		// Simulate a fully consumed handshake clock immediately after its reply.
		_ = c.Conn.SetDeadline(time.Now().Add(-time.Second))
	}
	return n, err
}
func (c *allowanceDeadlineFixture) SetDeadline(deadline time.Time) error {
	c.operation = true
	if c.timeout {
		return c.Conn.SetDeadline(time.Now().Add(-time.Second))
	}
	return c.Conn.SetDeadline(deadline)
}
func (c *allowanceDeadlineFixture) SetWriteDeadline(deadline time.Time) error {
	c.ack = true
	_ = c.Conn.SetWriteDeadline(time.Now().Add(-time.Second))
	return c.Conn.SetWriteDeadline(deadline)
}

func TestAllowanceSyntheticHelper(t *testing.T) {
	if os.Getenv("OWNED_FIXTURE_HELPER") != "1" {
		return
	}
	if unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0) != nil {
		t.Fatal("subreaper")
	}
	f := os.NewFile(3, "fixture-control")
	conn, err := net.FileConn(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if conn.SetDeadline(time.Now().Add(10*time.Second)) != nil {
		t.Fatal("deadline")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAllowanceSyntheticNative$")
	cmd.Env = append(os.Environ(), "OWNED_FIXTURE_NATIVE=live")
	wrapped := &allowanceDeadlineFixture{Conn: conn, timeout: os.Getenv("OWNED_FIXTURE_TIMEOUT") == "1"}
	err = serveAllowanceChild(wrapped, cmd, os.Args[0])
	failure := os.Getenv("OWNED_FIXTURE_FAIL") == "1"
	if (err != nil) != failure || !wrapped.operation || (!failure && !wrapped.ack) {
		t.Fatal("synthetic phase deadlines", err, wrapped.operation, wrapped.ack)
	}
}
func TestAllowanceDirectChildHeldUntilStop(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := os.NewFile(uintptr(pair[0]), "parent")
	c := os.NewFile(uintptr(pair[1]), "helper")
	conn, err := net.FileConn(p)
	_ = p.Close()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	helper := exec.Command(os.Args[0], "-test.run=^TestAllowanceSyntheticHelper$")
	helper.Env = append(os.Environ(), "OWNED_FIXTURE_HELPER=1")
	helper.ExtraFiles = []*os.File{c}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	defer func() {
		if helper.ProcessState == nil {
			_ = helper.Process.Kill()
			_ = helper.Wait()
		}
	}()
	if conn.SetDeadline(time.Now().Add(10*time.Second)) != nil {
		t.Fatal("deadline")
	}
	var started allowanceOwnerMessage
	if json.NewDecoder(conn).Decode(&started) != nil || started.Kind != "started" || !rootLive(started.Root) {
		t.Fatal("owned child not live")
	}
	wrong := started.Root
	wrong.StartTicks++
	if rootLive(wrong) || allowanceMatchesExecutable(started.Root.PID, "/dev/null") {
		t.Fatal("replaced identity or wrong executable accepted")
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(started.Root.PID, &status, unix.WNOHANG, nil); !errors.Is(err, unix.ECHILD) {
		t.Fatal("caller could reap helper's direct child")
	}
	if err := json.NewEncoder(conn).Encode(allowanceOwnerMessage{Kind: "stop"}); err != nil {
		t.Fatal(err)
	}
	var stopped allowanceOwnerMessage
	if json.NewDecoder(conn).Decode(&stopped) != nil || stopped.Kind != "stopped" {
		t.Fatal("cleanup unconfirmed")
	}
	if err := helper.Wait(); err != nil {
		t.Fatal("fixture helper", err)
	}
	if rootLive(started.Root) {
		t.Fatal("child survived cleanup")
	}
}
func TestAllowanceZombieIsNotLive(t *testing.T) {
	child := exec.Command(os.Args[0], "-test.run=^TestAllowanceSyntheticNative$")
	child.Env = append(os.Environ(), "OWNED_FIXTURE_NATIVE=exit")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	root, err := identity(child.Process.Pid)
	if err != nil {
		_ = child.Wait()
		t.Fatal(err)
	}
	var info unix.Siginfo
	if err := unix.Waitid(unix.P_PID, child.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil); err != nil {
		_ = child.Wait()
		t.Fatal(err)
	}
	if rootLive(root) {
		_ = child.Wait()
		t.Fatal("zombie treated as live")
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestAllowanceTimeoutAndDisconnectReapChild(t *testing.T) {
	for _, mode := range []string{"timeout", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			p, c := os.NewFile(uintptr(pair[0]), "parent"), os.NewFile(uintptr(pair[1]), "helper")
			conn, err := net.FileConn(p)
			_ = p.Close()
			if err != nil {
				t.Fatal(err)
			}
			helper := exec.Command(os.Args[0], "-test.run=^TestAllowanceSyntheticHelper$")
			helper.Env = append(os.Environ(), "OWNED_FIXTURE_HELPER=1", "OWNED_FIXTURE_FAIL=1")
			if mode == "timeout" {
				helper.Env = append(helper.Env, "OWNED_FIXTURE_TIMEOUT=1")
			}
			helper.ExtraFiles = []*os.File{c}
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			_ = c.Close()
			defer func() {
				_ = conn.Close()
				if helper.ProcessState == nil {
					_ = helper.Process.Kill()
					_ = helper.Wait()
				}
			}()
			_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
			var started allowanceOwnerMessage
			if json.NewDecoder(conn).Decode(&started) != nil || started.Kind != "started" {
				t.Fatal("missing start")
			}
			if mode == "disconnect" {
				_ = conn.Close()
			}
			if err := helper.Wait(); err != nil {
				t.Fatal("cleanup fixture", err)
			}
			if rootLive(started.Root) {
				t.Fatal("child survived cleanup")
			}
		})
	}
}
