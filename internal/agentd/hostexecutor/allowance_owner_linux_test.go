//go:build linux

package hostexecutor

import (
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
	if err := serveAllowanceChild(conn, cmd, os.Args[0]); err != nil {
		t.Fatal("synthetic owned child", err)
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
