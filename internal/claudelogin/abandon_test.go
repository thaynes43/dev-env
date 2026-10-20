package claudelogin

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fakeProc writes one /proc-style entry.
func fakeProc(t *testing.T, dir string, pid int, state string, argv ...string) {
	t.Helper()
	d := filepath.Join(dir, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := ""
	for _, a := range argv {
		cmd += a + "\x00"
	}
	if err := os.WriteFile(filepath.Join(d, "cmdline"), []byte(cmd), 0o644); err != nil {
		t.Fatal(err)
	}
	stat := strconv.Itoa(pid) + " (agentd) " + state + " 1 1 1 0 -1"
	if err := os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatal(err)
	}
}

// No helper running is a success and signals nothing, whatever else runs.
func TestAbandonWithNoHelperIsSuccess(t *testing.T) {
	dir := t.TempDir()
	fakeProc(t, dir, 7, "S", "/usr/local/bin/agentd", "claude-login-idle") // the entrypoint
	fakeProc(t, dir, 9, "S", "agentd", "claude-login-status")
	fakeProc(t, dir, 11, "S", "agentd", "claude-login-abandon")
	fakeProc(t, dir, 12, "S", "agentd", "claude-login", "--login-dir", "/tmp/x") // not the helper's exact argv
	fakeProc(t, dir, 13, "S", "/bin/sh", "claude-login")
	fakeProc(t, dir, 14, "S", "claude", "auth", "login", "--claudeai")
	signalled := 0
	got := Abandon(context.Background(), AbandonOptions{ProcDir: dir, Self: 11, Signal: func(int) error { signalled++; return nil }})
	if got != AbandonNone || signalled != 0 {
		t.Fatalf("got %q, signalled %d", got, signalled)
	}
	// An empty or missing /proc is the same.
	if got := Abandon(context.Background(), AbandonOptions{ProcDir: filepath.Join(dir, "missing")}); got != AbandonNone {
		t.Fatalf("missing proc: %q", got)
	}
}

// A running helper (by full path or bare name) is signalled once, and the command
// waits for it to leave; one that never leaves is reported Stuck.
func TestAbandonSignalsTheHelperAndWaits(t *testing.T) {
	dir := t.TempDir()
	fakeProc(t, dir, 21, "S", "/usr/local/bin/agentd", "claude-login")
	fakeProc(t, dir, 22, "S", "agentd", "claude-login")
	fakeProc(t, dir, 23, "S", "agentd", "claude-login-idle")
	var sent []int
	polls := 0
	got := Abandon(context.Background(), AbandonOptions{
		ProcDir: dir, Self: 99,
		Signal: func(pid int) error { sent = append(sent, pid); return nil },
		Sleep: func(time.Duration) {
			polls++
			if polls == 2 { // the helpers end after two polls: one gone, one a zombie
				_ = os.Remove(filepath.Join(dir, "21", "stat"))
				fakeProc(t, dir, 22, "Z", "agentd", "claude-login")
			}
		},
	})
	if got != AbandonDone || len(sent) != 2 || polls < 2 {
		t.Fatalf("got %q, sent %v, polls %d", got, sent, polls)
	}
	for _, pid := range sent {
		if pid == 23 || pid == 99 {
			t.Fatalf("signalled %d", pid)
		}
	}
	// Never leaves.
	fakeProc(t, dir, 24, "S", "agentd", "claude-login")
	now := time.Now()
	if got := Abandon(context.Background(), AbandonOptions{
		ProcDir: dir, Self: 99, Wait: time.Second,
		Signal: func(int) error { return nil },
		Sleep:  func(time.Duration) { now = now.Add(400 * time.Millisecond) },
		Now:    func() time.Time { return now },
	}); got != AbandonStuck {
		t.Fatalf("a helper that never leaves: %q", got)
	}
}

// With a real process and the real signal: a helper stand-in whose argv is exactly
// `agentd claude-login` ends on SIGHUP, and a second abandon finds nothing.
func TestAbandonEndsARealHelperStandIn(t *testing.T) {
	if _, err := os.Stat("/proc/self/cmdline"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	if err := os.Symlink("/bin/sh", filepath.Join(dir, "agentd")); err != nil {
		t.Fatal(err)
	}
	// `agentd claude-login` runs the shell on this script: it exits on SIGHUP, as
	// the real helper's ctx cancel ends it.
	script := "trap 'exit 0' HUP\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(filepath.Join(dir, "claude-login"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(dir, "agentd"), "claude-login")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }() // reaps it, so it is not a zombie
	defer func() { _ = cmd.Process.Kill() }()
	// The shell must have exec'd the script before its argv is the helper's.
	deadline := time.Now().Add(5 * time.Second)
	for len(helperPIDs("/proc", os.Getpid())) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in never showed up as a helper")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := Abandon(context.Background(), AbandonOptions{Wait: 5 * time.Second}); got != AbandonDone {
		t.Fatalf("abandon = %q", got)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the stand-in is still running")
	}
	if got := Abandon(context.Background(), AbandonOptions{}); got != AbandonNone {
		t.Fatalf("second abandon = %q", got)
	}
}
