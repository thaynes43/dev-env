package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"golang.org/x/sys/unix"
)

func saveManagedDelivery(t *testing.T, s Settings) protocol.Session {
	t.Helper()
	sess := protocol.Session{Name: "task", SessionUID: "session-uid", Agent: protocol.AgentCodex}
	first := Launch{Provider: protocol.AgentCodex, Session: sess.Name, SessionUID: sess.SessionUID,
		ConversationID: "12345678-1234-1234-1234-123456789abc", NativeThreadConfirmed: true, Dir: s.WorktreePath(sess.Name), BootID: "first"}
	if err := writeWorkspaceJSON(s.statePath(launchFile), first); err != nil {
		t.Fatal(err)
	}
	first.Resume, first.TUI, first.BootID = true, true, "b1"
	if err := writeWorkspaceJSON(s.statePath(resumeFile), first); err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestManagedCodexDeliveryUsesOwnedTUIAndRefusesChangedIdentity(t *testing.T) {
	for _, kind := range []string{"valid", "uid", "thread", "unconfirmed", "first-uid", "not-resume"} {
		t.Run(kind, func(t *testing.T) {
			s, runner, input := deliverRig(t, true)
			sess := saveManagedDelivery(t, s)
			var resumed Launch
			if readJSONFile(s.statePath(resumeFile), &resumed) != nil {
				t.Fatal("resume fixture unavailable")
			}
			switch kind {
			case "uid":
				sess.SessionUID = "replacement-uid"
			case "thread":
				resumed.ConversationID = "87654321-1234-1234-1234-123456789abc"
			case "unconfirmed":
				resumed.NativeThreadConfirmed = false
			case "not-resume":
				resumed.Resume = false
			case "first-uid":
				var first Launch
				if readJSONFile(s.statePath(launchFile), &first) != nil {
					t.Fatal("first fixture unavailable")
				}
				first.SessionUID = "replacement-uid"
				if err := writeWorkspaceJSON(s.statePath(launchFile), first); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeWorkspaceJSON(s.statePath(resumeFile), resumed); err != nil {
				t.Fatal(err)
			}
			err := Deliver(context.Background(), runner, s, sess, "coordinator/host-a", "recorded synthetic answer")
			if kind != "valid" {
				if !errors.Is(err, ErrNotAddressable) || len(runner.lines()) != 0 {
					t.Fatal("changed native identity reached the TUI")
				}
				return
			}
			if err != nil || len(runner.lines()) != 3 || !strings.HasSuffix(input["tmux load-buffer"], "recorded synthetic answer") {
				t.Fatal("owned native TUI did not receive one bounded paste")
			}
			for _, call := range runner.lines() {
				if strings.Contains(call, "codex") || strings.Contains(call, "queue") {
					t.Fatal("managed native delivery created another provider/server invocation")
				}
			}
		})
	}
}

type isolatedTmuxRunner struct {
	socket string
	ExecRunner
}

func (r isolatedTmuxRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if c.Name != "/usr/bin/tmux" {
		return Result{}, errors.New("unexpected fixture executable")
	}
	c.Args = append([]string{"-S", r.socket}, c.Args...)
	return r.ExecRunner.Run(ctx, c)
}

// This finite fixture exercises the actual pane terminal and bracketed-paste
// framing. Its worker reads bytes only; it never starts a provider or model.
func TestManagedCodexDeliveryActualTTY(t *testing.T) {
	if os.Getenv("MANAGED_CODEX_TTY_FIXTURE") == "worker" {
		terminal, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
		if err != nil {
			t.Fatal("fixture has no terminal")
		}
		terminal.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
		terminal.Iflag &^= unix.ICRNL
		terminal.Cc[unix.VMIN], terminal.Cc[unix.VTIME] = 1, 0
		if unix.IoctlSetTermios(int(os.Stdin.Fd()), unix.TCSETS, terminal) != nil {
			t.Fatal("fixture cannot configure terminal")
		}
		_, _ = io.WriteString(os.Stdout, "\x1b[?2004h")
		path := os.Getenv("MANAGED_CODEX_TTY_RECEIPT")
		if err := os.WriteFile(path+".ready", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		var received strings.Builder
		var one [1]byte
		for received.Len() < 32<<10 {
			if _, err := os.Stdin.Read(one[:]); err != nil {
				t.Fatal("fixture input ended")
			}
			received.WriteByte(one[0])
			if strings.HasSuffix(received.String(), "\x1b[201~\r") || strings.HasSuffix(received.String(), "\x1b[201~\n") {
				if err := os.WriteFile(path, []byte(received.String()), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
		t.Fatal("fixture input exceeded its bound")
	}
	if _, err := os.Stat("/usr/bin/tmux"); err != nil {
		t.Skip("actual terminal fixture requires tmux")
	}
	home := t.TempDir()
	s := testSettings(t, home)
	s.TmuxBin = "/usr/bin/tmux"
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1"}); err != nil {
		t.Fatal(err)
	}
	sess := saveManagedDelivery(t, s)
	socket, receipt := filepath.Join(home, "tmux.sock"), filepath.Join(home, "input.receipt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := exec.CommandContext(ctx, s.TmuxBin, "-S", socket, "-f", "/dev/null", "new-session", "-d", "-s", TmuxSession,
		"/usr/bin/env", "MANAGED_CODEX_TTY_FIXTURE=worker", "MANAGED_CODEX_TTY_RECEIPT="+receipt, os.Args[0], "-test.run=^TestManagedCodexDeliveryActualTTY$")
	start.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GOMAXPROCS=2"}
	if start.Run() != nil {
		t.Fatal("isolated pane fixture cannot start")
	}
	t.Cleanup(func() { _ = exec.Command(s.TmuxBin, "-S", socket, "kill-server").Run() })
	waitFixtureFile(t, ctx, receipt+".ready")
	pidRaw, err := exec.CommandContext(ctx, s.TmuxBin, "-S", socket, "display-message", "-p", "-t", "="+TmuxSession+":", "#{pane_pid}").Output()
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidRaw)))
	if err != nil || parseErr != nil {
		t.Fatal("owned pane PID unavailable")
	}
	if err := writeJSONFile(s.statePath(pidFile), agentPid{Pid: pid, Start: procStartTime(pid)}); err != nil {
		t.Fatal(err)
	}
	runner := isolatedTmuxRunner{socket: socket, ExecRunner: ExecRunner{BaseEnv: []string{"HOME=" + home, "PATH=/usr/bin:/bin"}}}
	answer := "recorded synthetic answer\nsecond line"
	if err := Deliver(ctx, runner, s, sess, "coordinator/host-a", answer); err != nil {
		t.Fatal("owned native pane delivery failed")
	}
	waitFixtureFile(t, ctx, receipt)
	data, err := os.ReadFile(receipt)
	if err != nil || !strings.HasPrefix(string(data), "\x1b[200~") || !strings.HasSuffix(string(data), "\x1b[201~\r") || !strings.Contains(string(data), answer) {
		t.Fatal("actual native terminal did not receive the exact bounded bracketed paste and one Enter")
	}
}

func waitFixtureFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("finite terminal fixture did not finish"))
		case <-tick.C:
		}
	}
}
