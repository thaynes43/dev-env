package usagecollector

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCodexCancellationClosesInheritedOutputAndStopsOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	executable := filepath.Join(dir, "official-cli-fixture")
	body := "#!/bin/sh\n/bin/sleep 60 &\nprintf '%s' \"$!\" > '" + strings.ReplaceAll(pidFile, "'", "'\\''") + "'\nwait\n"
	if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := CollectCodex(ctx, executable, time.Now)
	if err == nil || time.Since(started) > 2*time.Second {
		t.Fatal("inherited stdout defeated cancellation", err)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal("fixture never started descendant", err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid < 1 {
		t.Fatal("invalid fixture pid")
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	// A killed orphan may briefly be a zombie while the test container's init
	// reaps it. Linux proc metadata distinguishes that from a leaked process.
	deadline := time.Now().Add(time.Second)
	for {
		killErr := syscall.Kill(pid, 0)
		if errors.Is(killErr, syscall.ESRCH) {
			break
		}
		stat, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("collector leaked its running descendant")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOwnedGroupCleanupAfterDirectChildExit(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	body := "/bin/sleep 60 &\nprintf '%s' \"$!\" > '" + strings.ReplaceAll(pidFile, "'", "'\\''") + "'\nexit 0\n"
	cmd := exec.Command("/bin/sh", "-c", body)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleaned := false
	t.Cleanup(func() {
		if !cleaned {
			_ = stopOwnedGroup(cmd)
		}
	})
	deadline := time.Now().Add(time.Second)
	for {
		stat, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "stat"))
		if strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture direct child did not exit")
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid < 1 || syscall.Kill(pid, 0) != nil {
		t.Fatal("fixture descendant did not remain alive")
	}
	if !stopOwnedGroup(cmd) {
		t.Fatal("group kill/direct reap unavailable")
	}
	cleaned = true
	for {
		stat, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) || strings.Contains(string(stat), ") Z ") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned descendant survived direct-child exit cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
