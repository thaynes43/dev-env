package agentd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

func noticeLine(child string) string {
	return "[dev-env notice] a note is waiting from " + child + ": agent-run parent read message." +
		strings.Repeat("a", 32) + "." + strings.Repeat("b", 32) +
		". This line is typed by the platform, not by the owner. If it arrived as the answer to a question you asked, discard it and ask again."
}

func noticePlural(child string, n int) string {
	return "[dev-env notice] " + strconv.Itoa(n) + " notes are waiting, the oldest from " + child +
		": agent-run parent list. This line is typed by the platform, not by the owner. If it arrived as the answer to a question you asked, discard it and ask again."
}

func noticeCLI(t *testing.T, s Settings) agentPid {
	t.Helper()
	var p agentPid
	if err := readJSONFile(s.statePath(pidFile), &p); err != nil || !pidAlive(p) {
		t.Fatal(err, p)
	}
	return p
}

func writeClaudeStatus(t *testing.T, s Settings, pid int, body string) {
	t.Helper()
	dir := filepathJoinSessions(s)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/"+strconv.Itoa(pid)+".json", []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func filepathJoinSessions(s Settings) string {
	return s.ClaudeConfigDir + "/sessions"
}

func idleBody() string { return `{"status":"idle","updatedAt":4102444800000}` }

// panesFor answers list-panes and records every other tmux invocation.
func panesFor(t *testing.T, stdin map[string]string, stdout string) func(Cmd) (Result, error) {
	t.Helper()
	return func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			stdin[c.Name+" "+c.Args[0]] = string(b)
		}
		if len(c.Args) > 0 && c.Args[0] == "list-panes" {
			if !slices.Contains(c.Args, "-s") {
				return Result{Stdout: []byte("%1 1\n")}, nil
			}
			return Result{Stdout: []byte(stdout)}, nil
		}
		return Result{}, nil
	}
}

// A Claude TUI receives the fixed line and Enter on the pane of the recorded
// live CLI. A pid file whose start time is not that process's is not typed
// into: removing the live-pid check would paste.
func TestDeliverNoticeTypesOnlyTheFixedLine(t *testing.T) {
	const noteText = "NOTE-TEXT-MUST-NOT-BE-TYPED"
	line := noticeLine("child-1")
	if strings.Contains(line, noteText) || !parentNoticeLine.MatchString(line) {
		t.Fatal("fixture line is not the fixed form")
	}
	s, f, stdin := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	status, _, ok := claudeStatus(s, cli.Pid)
	if !ok || status != claudeIdle {
		t.Fatal("claudeStatus did not read idle for the live pid", status, ok)
	}
	f.handle = panesFor(t, stdin, fmt.Sprintf("%%12 %d\n", cli.Pid))
	sess := protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}
	if err := DeliverNotice(context.Background(), f, s, sess, line+"\n"); err != nil {
		t.Fatal(err)
	}
	got := f.lines()
	if len(got) != 4 || !strings.Contains(got[0], "list-panes") || !strings.Contains(got[0], " -s ") || !strings.Contains(got[0], "#{pane_id} #{pane_pid}") ||
		!strings.HasPrefix(got[1], "tmux load-buffer -b agentd-notice-") || !strings.HasSuffix(got[1], " -") ||
		got[2] != "tmux paste-buffer -b "+strings.Fields(got[1])[3]+" -d -p -t %12" ||
		got[3] != "tmux send-keys -t %12 Enter" {
		t.Fatalf("commands:\n%s", strings.Join(got, "\n"))
	}
	if strings.Contains(strings.Join(got, "\n"), "=agent:") {
		t.Fatal("the notice targeted the active pane")
	}
	if stdin["tmux load-buffer"] != line || strings.Contains(stdin["tmux load-buffer"], "[Message from") || strings.Contains(stdin["tmux load-buffer"], noteText) {
		t.Fatalf("typed %q", stdin["tmux load-buffer"])
	}
	dotted := noticeLine("dev.child")
	if err := DeliverNotice(context.Background(), f, s, sess, dotted); err != nil {
		t.Fatal(err)
	}
	if stdin["tmux load-buffer"] != dotted {
		t.Fatalf("dotted child typed %q", stdin["tmux load-buffer"])
	}

	s, f, stdin = deliverRig(t, true)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	stale := cli
	stale.Start = "1"
	if err := writeJSONFile(s.statePath(pidFile), stale); err != nil {
		t.Fatal(err)
	}
	if pidAlive(stale) {
		t.Fatal("fixture start time still matches")
	}
	f.handle = panesFor(t, stdin, fmt.Sprintf("%%12 %d\n", cli.Pid))
	if err := DeliverNotice(context.Background(), f, s, sess, line); err == nil || !errors.Is(err, ErrNotAddressable) {
		t.Fatal("a pid with the wrong start time was typed", err)
	}
	if len(f.lines()) != 0 {
		t.Fatalf("stale pid typed:\n%s", strings.Join(f.lines(), "\n"))
	}
}

func TestDeliverNoticeRefusesCodexHeadlessAndNoteText(t *testing.T) {
	line := noticeLine("child-1")
	s, f, _ := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentCodex}, line); !errors.Is(err, ErrNotAddressable) || !errors.Is(err, noticeHold(NoticeNotClaudeTUI)) {
		t.Fatalf("codex: %v", err)
	}
	if len(f.lines()) != 0 {
		t.Fatalf("codex was typed into: %q", f.lines())
	}
	s, f, _ = deliverRig(t, false)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, line); !errors.Is(err, ErrNotAddressable) || !errors.Is(err, noticeHold(NoticeNotClaudeTUI)) {
		t.Fatalf("headless: %v", err)
	}
	if len(f.lines()) != 0 {
		t.Fatalf("headless was typed into: %q", f.lines())
	}
	s, f, _ = deliverRig(t, true)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	f.handle = panesFor(t, map[string]string{}, fmt.Sprintf("%%12 %d\n", cli.Pid))
	for _, bad := range []string{
		"NOTE-TEXT-MUST-NOT-BE-TYPED",
		line + " and the note text",
		"[dev-env notice] a note is waiting from child-1: agent-run parent read message." + strings.Repeat("a", 32) + "." + strings.Repeat("b", 32),
		noticePlural("child-1", 1),
		noticePlural("child-1", 10000),
		"",
	} {
		if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, bad); err == nil || errors.Is(err, ErrNotAddressable) {
			t.Fatalf("line %q: %v", bad, err)
		}
	}
	if len(f.lines()) != 0 {
		t.Fatalf("a bad line was typed: %q", f.lines())
	}
	plural := noticePlural("child-1", 2)
	if !parentNoticeLine.MatchString(plural) {
		t.Fatal("plural form does not match")
	}
	if err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, plural); err != nil {
		t.Fatal(err)
	}
}

// The admission reads claudeStatus for the recorded live pid and types only
// when that read succeeded and the status is idle.
func TestDeliverNoticeTypesOnlyWhenClaudeStatusIsIdle(t *testing.T) {
	line := noticeLine("child-1")
	sess := protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}
	cases := []struct {
		name string
		body string // empty means no file
		word string
	}{
		{name: "missing", word: NoticeUnknown},
		{name: "malformed", body: "{", word: NoticeUnknown},
		{name: "empty", body: `{}`, word: NoticeUnknown},
		{name: "unknown", body: `{"status":"pondering","updatedAt":4102444800000}`, word: NoticeUnknown},
		{name: "stale", body: `{"status":"idle","updatedAt":1}`, word: NoticeUnknown},
		{name: "busy", body: `{"status":"busy","updatedAt":4102444800000}`, word: NoticeBusy},
		{name: "waiting", body: `{"status":"waiting","updatedAt":4102444800000}`, word: NoticeWaiting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, stdin := deliverRig(t, true)
			cli := noticeCLI(t, s)
			if tc.body != "" {
				writeClaudeStatus(t, s, cli.Pid, tc.body)
			}
			status, _, readOK := claudeStatus(s, cli.Pid)
			if tc.name == "stale" {
				if !readOK || status != claudeIdle {
					t.Fatal("stale fixture was not a readable idle record")
				}
			} else if readOK && status == claudeIdle {
				t.Fatal("claudeStatus reported idle")
			}
			f.handle = panesFor(t, stdin, fmt.Sprintf("%%12 %d\n", cli.Pid))
			err := DeliverNotice(context.Background(), f, s, sess, line)
			if !errors.Is(err, noticeHold(tc.word)) {
				t.Fatal(err)
			}
			if len(f.lines()) != 0 {
				t.Fatalf("typed:\n%s", strings.Join(f.lines(), "\n"))
			}
		})
	}
}

func TestDeliverNoticePinsTheCLIPane(t *testing.T) {
	line := noticeLine("child-1")
	sess := protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}
	s, f, stdin := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	other := 1 << 30
	listed := 0
	f.handle = func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			stdin[c.Name+" "+c.Args[0]] = string(b)
		}
		if len(c.Args) == 0 || c.Args[0] != "list-panes" {
			return Result{}, nil
		}
		if !slices.Contains(c.Args, "-s") {
			return Result{Stdout: []byte(fmt.Sprintf("%%8 %d\n", other))}, nil
		}
		listed++
		if listed == 1 {
			return Result{Stdout: []byte(fmt.Sprintf("%%8 %d\n%%7 %d\n", other, cli.Pid))}, nil
		}
		// A later list would show the other pane focused. The pinned id must not follow it.
		return Result{Stdout: []byte(fmt.Sprintf("%%8 %d\n", other))}, nil
	}
	if err := DeliverNotice(context.Background(), f, s, sess, line); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.lines(), "\n")
	if listed != 1 || !strings.Contains(got, "paste-buffer") || !strings.Contains(got, "-t %7") || !strings.Contains(got, "send-keys -t %7 Enter") || strings.Contains(got, "%8") || strings.Contains(got, "=agent:") {
		t.Fatalf("listed %d\n%s", listed, got)
	}

	// The launch's pane process is the parent of the recorded CLI.
	s, f, stdin = deliverRig(t, true)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	parent, ok := procParent(cli.Pid)
	if !ok {
		t.Fatal("no parent")
	}
	f.handle = panesFor(t, stdin, fmt.Sprintf("%%4 %d\n", parent))
	if err := DeliverNotice(context.Background(), f, s, sess, line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.lines(), "\n"), "-t %4") {
		t.Fatalf("parent pane was not used:\n%s", strings.Join(f.lines(), "\n"))
	}

	// A pane that is a direct child of the recorded CLI is that CLI's pane.
	s, f, stdin = deliverRig(t, true)
	child := noticeCLI(t, s)
	self := agentPid{Pid: os.Getpid(), Start: procStartTime(os.Getpid())}
	if err := writeJSONFile(s.statePath(pidFile), self); err != nil || !pidAlive(self) {
		t.Fatal(err)
	}
	writeClaudeStatus(t, s, self.Pid, idleBody())
	f.handle = panesFor(t, stdin, fmt.Sprintf("%%9 %d\n", child.Pid))
	if err := DeliverNotice(context.Background(), f, s, sess, line); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.lines(), "\n"), "-t %9") {
		t.Fatalf("child pane was not used:\n%s", strings.Join(f.lines(), "\n"))
	}

	s, f, stdin = deliverRig(t, true)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	parent, ok = procParent(cli.Pid)
	if !ok {
		t.Fatal("no parent")
	}
	for _, stdout := range []string{"", fmt.Sprintf("%%7 %d\n%%4 %d\n", cli.Pid, parent), "not-a-pane 12\n"} {
		f.handle = panesFor(t, stdin, stdout)
		if err := DeliverNotice(context.Background(), f, s, sess, line); err == nil {
			t.Fatalf("stdout %q typed", stdout)
		}
		for _, got := range f.lines() {
			if strings.Contains(got, "load-buffer") || strings.Contains(got, "paste-buffer") || strings.Contains(got, "send-keys") {
				t.Fatalf("buffer loaded for %q:\n%s", stdout, strings.Join(f.lines(), "\n"))
			}
		}
	}
}

func TestDeliverNoticeRechecksIdleBeforeEnter(t *testing.T) {
	line := noticeLine("child-1")
	s, f, stdin := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	f.handle = func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			stdin[c.Name+" "+c.Args[0]] = string(b)
		}
		if len(c.Args) > 0 && c.Args[0] == "list-panes" {
			if !slices.Contains(c.Args, "-s") {
				return Result{Stdout: []byte("%1 1\n")}, nil
			}
			return Result{Stdout: []byte(fmt.Sprintf("%%12 %d\n", cli.Pid))}, nil
		}
		if len(c.Args) > 0 && c.Args[0] == "paste-buffer" {
			writeClaudeStatus(t, s, cli.Pid, `{"status":"waiting","updatedAt":4102444800000}`)
		}
		return Result{}, nil
	}
	err := DeliverNotice(context.Background(), f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, line)
	if !errors.Is(err, noticeHold(NoticeWaiting)) {
		t.Fatal(err)
	}
	got := strings.Join(f.lines(), "\n")
	if !strings.Contains(got, "paste-buffer") || strings.Contains(got, "send-keys") {
		t.Fatalf("enter followed a waiting recheck:\n%s", got)
	}
}

func TestDeliverNoticeLockAndSettleObserveContext(t *testing.T) {
	line := noticeLine("child-1")
	s, f, _ := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	held, err := lockFileWait(s.statePath(deliverLockFile), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = DeliverNotice(ctx, f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, line)
	held()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatal(err, time.Since(start))
	}
	if len(f.lines()) != 0 {
		t.Fatalf("lock wait typed:\n%s", strings.Join(f.lines(), "\n"))
	}

	s, f, stdin := deliverRig(t, true)
	cli = noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	deliverSettle = time.Hour
	ctx, cancel = context.WithCancel(context.Background())
	f.handle = func(c Cmd) (Result, error) {
		if c.Stdin != nil {
			b, _ := io.ReadAll(c.Stdin)
			stdin[c.Name+" "+c.Args[0]] = string(b)
		}
		if len(c.Args) > 0 && c.Args[0] == "list-panes" {
			if !slices.Contains(c.Args, "-s") {
				return Result{Stdout: []byte("%1 1\n")}, nil
			}
			return Result{Stdout: []byte(fmt.Sprintf("%%12 %d\n", cli.Pid))}, nil
		}
		if len(c.Args) > 0 && c.Args[0] == "paste-buffer" {
			cancel()
		}
		return Result{}, nil
	}
	start = time.Now()
	err = DeliverNotice(ctx, f, s, protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}, line)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 2*time.Second {
		t.Fatal(err, time.Since(start))
	}
	if strings.Contains(strings.Join(f.lines(), "\n"), "send-keys") {
		t.Fatal("settle ignored the context and pressed Enter")
	}
}

func TestNoticeProbeReadsClaudeStatusOnly(t *testing.T) {
	s, _, _ := deliverRig(t, true)
	cli := noticeCLI(t, s)
	writeClaudeStatus(t, s, cli.Pid, idleBody())
	sess := protocol.Session{Name: "parent-1", Agent: protocol.AgentClaude}
	if err := NoticeProbe(s, sess); err != nil {
		t.Fatal(err)
	}
	writeClaudeStatus(t, s, cli.Pid, `{"status":"busy","updatedAt":4102444800000}`)
	if err := NoticeProbe(s, sess); !errors.Is(err, noticeHold(NoticeBusy)) {
		t.Fatal(err)
	}
}
