package usagecollector

import (
	"context"
	"errors"
	"os"
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
