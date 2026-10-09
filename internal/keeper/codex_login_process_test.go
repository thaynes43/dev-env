package keeper

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCodexNativeLoginUsesPrivateTmpAndNoInheritedCredential(t *testing.T) {
	home := t.TempDir()
	tmp := filepath.Join(home, "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "SECRET_SYNTHETIC_CANARY")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `test "${OPENAI_API_KEY+x}" = "" && test "$TMPDIR" = "$HOME/tmp" && f=$(mktemp) && test "${f%/*}" = "$TMPDIR" && rm "$f"`)
	cmd.Env = codexLoginEnvironment(home, tmp)
	if err := cmd.Run(); err != nil {
		t.Fatal("native temp creation escaped staging or inherited a credential")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("native temp fixture retained files")
	}
	fi, err := os.Stat(tmp)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatal("native temp directory is not private")
	}
}

func TestCodexNativeLoginFixedDiagnosticsAndChallengeBound(t *testing.T) {
	for _, test := range []struct {
		name, script, code string
		challenge, success bool
	}{
		{"network", `printf '%s\n' 'Error logging in with device code: error sending request for url (SECRET_SYNTHETIC_CANARY)'; exit 1`, "NativeNetworkUnavailable", false, false},
		{"configuration", `printf '%s\n' 'Error loading configuration: SECRET_SYNTHETIC_CANARY'; exit 1`, "NativeConfigurationUnavailable", false, false},
		{"timeout", `exec sleep 2`, "InitialChallengeTimeout", false, false},
		{"presented", `printf '%s\n' 'https://auth.openai.com/codex/device' '2. Enter this one-time code (expires in 15 minutes)' 'FAKE-CODE'; sleep 0.1; exit 0`, "", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var out bytes.Buffer
			prompt := &codexPromptWriter{Out: &out, Device: true, Challenge: make(chan struct{})}
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", test.script)
			cmd.Stdout, cmd.Stderr = prompt, prompt
			cmd.WaitDelay = 50 * time.Millisecond
			var report CodexLoginReport
			err := runCodexLoginProcess(cmd, prompt, cancel, &report, 50*time.Millisecond)
			if (err == nil) != test.success || report.FailureCode != test.code || report.ChallengePresented != test.challenge || !report.NativeStarted {
				t.Fatalf("wrong fixed flow metadata: %+v success=%v", report, err == nil)
			}
			raw, _ := json.Marshal(report)
			if strings.Contains(string(raw), "CANARY") || strings.Contains(out.String(), "CANARY") {
				t.Fatal("raw native diagnostics leaked")
			}
			if test.success && ctx.Err() != nil {
				t.Fatal("startup timer cancelled an already presented owner challenge")
			}
		})
	}
}

func TestCodexNativeLoginExitMetadataRequiresStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prompt := &codexPromptWriter{Out: &bytes.Buffer{}, Challenge: make(chan struct{})}
	cmd := exec.CommandContext(ctx, filepath.Join(t.TempDir(), "absent-native"))
	var report CodexLoginReport
	if runCodexLoginProcess(cmd, prompt, cancel, &report, time.Second) == nil || report.NativeStarted || report.NativeExitCode != nil {
		t.Fatal("unstarted native invocation acquired exit metadata")
	}
	raw, _ := json.Marshal(report)
	if bytes.Contains(raw, []byte("nativeExitCode")) {
		t.Fatal("unstarted native exit code was serialized")
	}
}

func TestCodexLoginCleanupMetadataDistinguishesLostAckAndLiveGroup(t *testing.T) {
	for _, test := range []struct {
		name                 string
		started, absent, ack bool
		state                string
		removed, cleared     bool
	}{
		{"staging-only", false, false, true, "Cleared", true, true},
		{"joined", true, true, true, "Cleared", true, true},
		{"lost-ack", true, true, false, "Uncertain", true, false},
		{"group-unconfirmed", true, false, true, "Reserved", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			attempt := "synthetic-attempt"
			home := filepath.Join(dir, attempt)
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			report := CodexLoginReport{NativeStarted: test.started, NativeGroupAbsent: test.absent, ReservationState: "Reserved"}
			cancelled := false
			finishCodexLoginAttempt(dir, attempt, false, &report, func() bool { cancelled = true; return test.ack })
			_, err := os.Lstat(home)
			if os.IsNotExist(err) != test.removed || report.CleanupComplete != test.removed || report.ReservationCleared != test.cleared || report.ReservationState != test.state {
				t.Fatal("attempt cleanup acknowledgement metadata is inaccurate")
			}
			if test.started && !test.absent && cancelled {
				t.Fatal("unconfirmed native group triggered staging-removing cancellation")
			}
		})
	}
}

func TestCodexNativeLoginOutputHoldingDescendantCannotClaimGroupExit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	prompt := &codexPromptWriter{Out: &bytes.Buffer{}, Device: true, Challenge: make(chan struct{})}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `sleep 3 & printf '%s\n' 'https://auth.openai.com/codex/device' '2. Enter this one-time code' 'FAKE-CODE'; exit 0`)
	cmd.Stdout, cmd.Stderr = prompt, prompt
	cmd.WaitDelay = 30 * time.Millisecond
	var report CodexLoginReport
	if runCodexLoginProcess(cmd, prompt, cancel, &report, time.Second) == nil {
		t.Fatal("output-holding descendant was accepted as successful native completion")
	}
	if report.NativeExitCode == nil || *report.NativeExitCode != 0 {
		t.Fatal("native leader was not reaped")
	}
	if !report.NativeGroupAbsent && report.FailureCode != "NativeGroupTerminationUnconfirmed" {
		t.Fatal("unconfirmed group termination acquired another result")
	}
}

func TestCodexNativeLoginBrowserTTY(t *testing.T) {
	if os.Getenv("CODEX_TTY_FIXTURE") == "worker" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		prompt := &codexPromptWriter{Out: &bytes.Buffer{}, Challenge: make(chan struct{})}
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `printf '%s\n' 'https://auth.openai.com/codex/device'; read line; test "$line" = "FIXTURE-INPUT"`)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, prompt, prompt
		cmd.Env = codexLoginEnvironment(t.TempDir(), t.TempDir())
		cmd.WaitDelay = 50 * time.Millisecond
		var report CodexLoginReport
		if runCodexLoginProcess(cmd, prompt, cancel, &report, time.Second) != nil || !report.NativeGroupAbsent {
			t.Fatal("browser native terminal input failed")
		}
		foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
		if err != nil || foreground != syscall.Getpgrp() {
			t.Fatal("helper terminal foreground was not restored")
		}
		return
	}
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "fixture-pty")
	defer func() { _ = master.Close() }()
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(number), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	worker := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCodexNativeLoginBrowserTTY$")
	worker.Env = append(os.Environ(), "CODEX_TTY_FIXTURE=worker")
	worker.Stdin, worker.Stdout, worker.Stderr = slave, slave, slave
	worker.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("FIXTURE-INPUT\n")); err != nil {
		t.Fatal(err)
	}
	if err := worker.Wait(); err != nil {
		t.Fatal("actual browser PTY fixture failed")
	}
}
