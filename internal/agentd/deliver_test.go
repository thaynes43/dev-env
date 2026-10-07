package agentd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// deliverRig is a pod whose agent runs: a boot record, this boot's launch and
// a live process standing in for the CLI.
func deliverRig(t *testing.T, tui bool) (Settings, *fakeRunner, map[string]string) {
	t.Helper()
	s := testSettings(t, t.TempDir())
	if err := os.MkdirAll(s.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(bootFile), bootRecord{BootID: "b1"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONFile(s.statePath(launchFile), Launch{BootID: "b1", TUI: tui, ConversationID: "conv-1"}); err != nil {
		t.Fatal(err)
	}
	cli := exec.Command("sleep", "30")
	if err := cli.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Process.Kill(); _ = cli.Wait() })
	if err := writeJSONFile(s.statePath(pidFile), agentPid{Pid: cli.Process.Pid, Start: procStartTime(cli.Process.Pid)}); err != nil {
		t.Fatal(err)
	}
	stdin := map[string]string{}
	f := &fakeRunner{handle: func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			stdin[c.Name+" "+c.Args[0]] = string(b)
		}
		return Result{}, nil
	}}
	old := deliverSettle
	deliverSettle = 0
	t.Cleanup(func() { deliverSettle = old })
	return s, f, stdin
}

// A message reaches a Claude TUI as one bracketed paste and an Enter, with who
// sent it and how to answer (D-65).
func TestDeliverToAClaudeTUI(t *testing.T) {
	s, f, stdin := deliverRig(t, true)
	sess := protocol.Session{Name: "s-1", Agent: protocol.AgentClaude}
	if err := Deliver(context.Background(), f, s, sess, "session/haynes-ops-1007-1200", "  please rebase on main\nthen push  "); err != nil {
		t.Fatal(err)
	}
	l := f.lines()
	if len(l) != 3 || !strings.HasPrefix(l[0], "tmux load-buffer -b agentd-msg-") || !strings.HasSuffix(l[0], " -") ||
		l[1] != "tmux paste-buffer -b "+strings.Fields(l[0])[3]+" -d -p -t =agent:" || l[2] != "tmux send-keys -t =agent: Enter" {
		t.Errorf("commands:\n%s", strings.Join(l, "\n"))
	}
	msg := stdin["tmux load-buffer"]
	for _, part := range []string{"[Message from session/haynes-ops-1007-1200", "not from this session's user", "agent-run msg haynes-ops-1007-1200", "\n\nplease rebase on main\nthen push"} {
		if !strings.Contains(msg, part) {
			t.Errorf("message lacks %q:\n%s", part, msg)
		}
	}
	if strings.HasSuffix(msg, " ") {
		t.Errorf("the text was not trimmed: %q", msg)
	}
}

// Codex takes it with codex queue on the session's thread.
func TestDeliverToCodex(t *testing.T) {
	s, f, _ := deliverRig(t, true)
	if err := Deliver(context.Background(), f, s, protocol.Session{Name: "s-1", Agent: protocol.AgentCodex}, "human/dev-env-system/dev-env-human", "hi"); err != nil {
		t.Fatal(err)
	}
	if l := f.lines(); len(l) != 1 || !strings.HasPrefix(l[0], "codex queue --thread conv-1 --message [Message from human/dev-env-system/dev-env-human") || strings.Contains(l[0], "To answer") {
		t.Errorf("commands %q", l)
	}
}

// A headless task, an agent that is not running, or a bad message takes
// nothing.
func TestDeliverRefusals(t *testing.T) {
	sess := protocol.Session{Name: "s-1", Agent: protocol.AgentClaude}
	s, f, _ := deliverRig(t, false)
	if err := Deliver(context.Background(), f, s, sess, "session/x", "hi"); !errors.Is(err, ErrNotAddressable) || !strings.Contains(err.Error(), "headless") {
		t.Errorf("a task: %v", err)
	}
	s, f, _ = deliverRig(t, true)
	if err := os.Remove(s.statePath(pidFile)); err != nil {
		t.Fatal(err)
	}
	if err := Deliver(context.Background(), f, s, sess, "session/x", "hi"); !errors.Is(err, ErrNotAddressable) {
		t.Errorf("no agent: %v", err)
	}
	s, f, _ = deliverRig(t, true)
	for _, c := range []struct{ from, text string }{{"session/x", "  "}, {"", "hi"}, {"session/x", strings.Repeat("x", MaxMessageBytes+1)}} {
		if err := Deliver(context.Background(), f, s, sess, c.from, c.text); err == nil || errors.Is(err, ErrNotAddressable) {
			t.Errorf("from %q, %d bytes: %v", c.from, len(c.text), err)
		}
	}
	if len(f.lines()) != 0 {
		t.Errorf("ran %q", f.lines())
	}
	empty := testSettings(t, t.TempDir())
	if err := Deliver(context.Background(), f, empty, sess, "session/x", "hi"); !errors.Is(err, ErrNotAddressable) {
		t.Errorf("no boot: %v", err)
	}
}

// The log's tail comes from the worktree's log, else its shared copy; the
// copy is whole and skipped when unchanged (D-65).
func TestTailLogAndSharedCopy(t *testing.T) {
	s := testSettings(t, t.TempDir())
	old := sharedIsMounted
	sharedIsMounted = func(string, string) error { return nil }
	t.Cleanup(func() { sharedIsMounted = old })
	var buf bytes.Buffer
	if err := TailLog(s, "s-1", 2, &buf); err == nil {
		t.Error("a missing log")
	}
	if err := copyLogToShared(s, "s-1"); err != nil {
		t.Errorf("nothing to copy: %v", err)
	}
	writeFile(t, s.LogPath("s-1"), "one\ntwo\nthree\n")
	if err := TailLog(s, "s-1", 2, &buf); err != nil || buf.String() != "two\nthree\n" {
		t.Errorf("tail %q %v", buf.String(), err)
	}
	if err := copyLogToShared(s, "s-1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(s.SharedLogPath("s-1")); string(got) != "one\ntwo\nthree\n" {
		t.Errorf("copy %q", got)
	}
	fi, _ := os.Stat(s.SharedLogPath("s-1"))
	time.Sleep(10 * time.Millisecond)
	if err := copyLogToShared(s, "s-1"); err != nil {
		t.Fatal(err)
	}
	// A copy renames a new file into place, so an unchanged log keeps the
	// same inode: the copy was skipped.
	if fi2, _ := os.Stat(s.SharedLogPath("s-1")); !os.SameFile(fi, fi2) {
		t.Error("an unchanged log was copied again")
	}
	// A changed log is copied again: a new inode with the new content.
	writeFile(t, s.LogPath("s-1"), "one\ntwo\nthree\nfour\n")
	if err := copyLogToShared(s, "s-1"); err != nil {
		t.Fatal(err)
	}
	if fi3, _ := os.Stat(s.SharedLogPath("s-1")); os.SameFile(fi, fi3) {
		t.Error("a changed log was not copied")
	}
	if err := os.Remove(s.SharedLogPath("s-1")); err != nil {
		t.Fatal(err)
	}
	if err := copyLogToShared(s, "s-1"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.LogPath("s-1")); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := TailLog(s, "s-1", 5, &buf); err != nil || buf.String() != "one\ntwo\nthree\nfour\n" {
		t.Errorf("from the shared copy: %q %v", buf.String(), err)
	}
}

// Two deliveries at once never interleave: each loads, pastes and submits its
// own buffer while it holds the pod's delivery lock.
func TestConcurrentDeliveries(t *testing.T) {
	s, f, _ := deliverRig(t, true)
	deliverSettle = 20 * time.Millisecond
	sess := protocol.Session{Name: "s-1", Agent: protocol.AgentClaude}
	done := make(chan error, 2)
	for _, text := range []string{"first", "second"} {
		go func() { done <- Deliver(context.Background(), f, s, sess, "session/x", text) }()
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	l := f.lines()
	if len(l) != 6 {
		t.Fatalf("commands %q", l)
	}
	for i := 0; i < 6; i += 3 {
		buf := strings.Fields(l[i])[3]
		if !strings.HasPrefix(l[i], "tmux load-buffer") || !strings.Contains(l[i+1], "paste-buffer -b "+buf+" ") || !strings.Contains(l[i+2], "send-keys") {
			t.Errorf("deliveries interleaved:\n%s", strings.Join(l, "\n"))
		}
	}
	if strings.Fields(l[0])[3] == strings.Fields(l[3])[3] {
		t.Error("two deliveries shared a buffer")
	}
}

// A message cannot end the bracketed paste early or send keys of its own:
// the control characters a terminal acts on are dropped (D-65).
func TestDeliverStripsControlCharacters(t *testing.T) {
	s, f, stdin := deliverRig(t, true)
	sess := protocol.Session{Name: "s-1", Agent: protocol.AgentClaude}
	if err := Deliver(context.Background(), f, s, sess, "session/x", "hi\x1b[201~\x03rm -rf\n\tok\u009b"); err != nil {
		t.Fatal(err)
	}
	msg := stdin["tmux load-buffer"]
	if strings.ContainsAny(msg, "\x1b\x03\u009b") || !strings.HasSuffix(msg, "hi[201~rm -rf\n\tok") {
		t.Errorf("message %q", msg)
	}
	if got := StripControl("a\x00b\x7fc\td\ne"); got != "abc\td\ne" {
		t.Errorf("StripControl %q", got)
	}
}

// A line too long to print whole is cut and marked, never a failed read; no
// log at all is ErrNoLog.
func TestTailLogLongLines(t *testing.T) {
	s := testSettings(t, t.TempDir())
	if err := TailLog(s, "s-1", 5, io.Discard); !errors.Is(err, ErrNoLog) {
		t.Errorf("no log: %v", err)
	}
	writeFile(t, s.LogPath("s-1"), "first\n"+strings.Repeat("x", 2<<20)+"\nlast")
	var buf bytes.Buffer
	if err := TailLog(s, "s-1", 5, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "first" || len(lines[1]) > maxLogLine+64 || !strings.HasSuffix(lines[1], "[agentd: line cut at 64 KiB]") || lines[2] != "last" {
		t.Errorf("%d lines; the long one %d bytes", len(lines), len(lines[1]))
	}
}
