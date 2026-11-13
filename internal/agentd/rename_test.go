package agentd

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

// Rename tests use a fake tmux runner and files under the test home. They never
// start tmux, never read a real Claude sessions directory, and never launch
// agentd. Rule 8.11 names each case so a reader can find it.

type renameClock struct {
	mu      sync.Mutex
	at      time.Time
	onSleep func()
}

func (c *renameClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *renameClock) Sleep(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	hook := c.onSleep
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func (c *renameClock) Advance(d time.Duration) { c.Sleep(d) }

type recSpec struct {
	status, name, source, bridge, proc string
	age                                time.Duration
	future                             bool
	omitName, omitSource               bool
	statusNum                          bool
	updated                            int64
	updatedNull, updatedString         bool
}

type renameWorld struct {
	t               *testing.T
	s               Settings
	sess            protocol.Session
	run             *fakeRunner
	clock           *renameClock
	app             *TitleApplier
	pid             int
	start           string
	spec            recSpec
	panes           string
	clients         string
	clientsExact    string
	clientsExactSet bool
	version         string
	screens         []string
	lastScreen      string
	capN            int
	paneN           int
	cursorX         int
	cursorY         int
	cursorSet       bool

	versionErr bool
	clientErr  bool
	captureErr bool
	cursorErr  bool
	paneErr    bool
	sendErr    bool
	enterErr   bool
	confirmTo  string
	afterText  func()
	onRecheck  func()
	log        *slog.Logger
}

func newRenameWorld(t *testing.T) *renameWorld {
	t.Helper()
	home := t.TempDir()
	s := testSettings(t, home)
	pid := os.Getpid()
	start := procStartTime(pid)
	if start == "" {
		t.Fatal("this test process has no /proc start time")
	}
	w := &renameWorld{
		t: t, s: s, pid: pid, start: start,
		clock:   &renameClock{at: time.Date(2026, 11, 13, 14, 0, 0, 0, time.UTC)},
		panes:   sessiontitle.RecordedPaneLine(),
		version: "2.1.321 (Claude Code)\n",
		screens: []string{sessiontitle.IdleEmptyScreen},
		spec: recSpec{
			status: "idle", name: "derived-name", source: "api",
			bridge: "session_01AbCdEf", age: 30 * time.Second,
		},
	}
	w.sess = protocol.Session{
		Name: "dev-env-1019-120000", SessionUID: "uid-1",
		Agent: protocol.AgentClaude, Mode: protocol.ModeRemote,
		Model: "claude-haiku-4-5", Repo: "haynes-ops", Title: "Café",
	}
	w.app = &TitleApplier{Clock: w.clock}
	w.run = &fakeRunner{handle: w.answer}
	prev := readProcIDs
	readProcIDs = func(int) (procIDs, error) {
		return procIDs{ppid: 2028, pgrp: 7, tpgid: 7}, nil
	}
	t.Cleanup(func() { readProcIDs = prev })
	w.boot(false)
	w.writeRecord()
	return w
}

func (w *renameWorld) boot(first bool) {
	w.t.Helper()
	_ = os.Remove(w.s.statePath(resumeFile))
	_ = os.Remove(w.s.statePath(launchFile))
	const id = "boot-1"
	writeJSON(w.t, w.s.statePath(bootFile), bootRecord{BootID: id, Boot: protocol.BootReady})
	proj := filepath.Join(w.s.Home, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		w.t.Fatal(err)
	}
	l := Launch{
		Session: w.sess.Name, SessionUID: w.sess.SessionUID, BootID: id,
		TUI: true, Resume: !first, ConversationID: "conv-1", Dir: proj,
		Argv: []string{"claude", "--remote-control", "Haiku | Café"},
	}
	file := resumeFile
	if first {
		file = launchFile
	}
	writeJSON(w.t, w.s.statePath(file), l)
	writeJSON(w.t, w.s.statePath(pidFile), agentPid{Pid: w.pid, Start: w.start})
}

func (w *renameWorld) recordPath() string {
	return filepath.Join(w.s.ClaudeConfigDir, "sessions", strconv.Itoa(w.pid)+".json")
}

func (w *renameWorld) writeRecord() {
	w.t.Helper()
	if w.spec.updated == 0 {
		if w.spec.future {
			w.spec.updated = w.clock.Now().Add(time.Minute).UnixMilli()
		} else {
			w.spec.updated = w.clock.Now().Add(-w.spec.age).UnixMilli()
		}
	}
	m := map[string]any{
		"bridgeSessionId": w.spec.bridge,
		"procStart":       w.start,
		"status":          w.spec.status,
		"updatedAt":       w.spec.updated,
		"name":            w.spec.name,
		"nameSource":      w.spec.source,
	}
	if w.spec.proc != "" {
		m["procStart"] = w.spec.proc
	}
	if w.spec.omitName {
		delete(m, "name")
	}
	if w.spec.omitSource {
		delete(m, "nameSource")
	}
	if w.spec.statusNum {
		m["status"] = 1
	}
	if w.spec.updatedNull {
		m["updatedAt"] = nil
	}
	if w.spec.updatedString {
		m["updatedAt"] = "1"
	}
	writeJSON(w.t, w.recordPath(), m)
}

func (w *renameWorld) composed(text string) string {
	return sessiontitle.Compose(w.sess.Model, text)
}

func (w *renameWorld) arm(target string, confirm bool) {
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding("/rename " + target)}
	w.confirmTo = ""
	if confirm {
		w.confirmTo = target
	}
	w.afterText = nil
	w.onRecheck = nil
}

func (w *renameWorld) apply(text string) titleDisk {
	w.capN = 0
	w.paneN = 0
	log := slog.New(slog.DiscardHandler)
	if w.log != nil {
		log = w.log
	}
	w.app.Apply(context.Background(), w.run, w.s, w.sess, log, HeartbeatTitle{Present: true, Text: text})
	return loadTitle(w.s, w.sess)
}

func (w *renameWorld) answer(c Cmd) (Result, error) {
	if c.Name == "claude" {
		if w.versionErr {
			return Result{}, errTest("version")
		}
		return Result{Stdout: []byte(w.version)}, nil
	}
	if len(c.Args) == 0 {
		return Result{}, nil
	}
	switch c.Args[0] {
	case "list-panes":
		w.paneN++
		if w.paneN >= 2 && w.onRecheck != nil {
			w.onRecheck()
		}
		if w.paneErr {
			return Result{}, errTest("panes")
		}
		return Result{Stdout: []byte(w.panes + "\n")}, nil
	case "list-clients":
		if w.clientErr {
			return Result{}, errTest("clients")
		}
		if w.clientsExactSet {
			return Result{Stdout: []byte(w.clientsExact)}, nil
		}
		if w.clients == "" {
			return Result{}, nil
		}
		return Result{Stdout: []byte(w.clients + "\n")}, nil
	case "capture-pane":
		if w.captureErr {
			return Result{}, errTest("capture")
		}
		screen := sessiontitle.IdleEmptyScreen
		if len(w.screens) > 0 {
			i := w.capN
			if i >= len(w.screens) {
				i = len(w.screens) - 1
			}
			screen = w.screens[i]
			w.capN++
		}
		w.lastScreen = screen
		return Result{Stdout: []byte(screen)}, nil
	case "display-message":
		if w.cursorErr {
			return Result{}, errTest("cursor")
		}
		if w.cursorSet {
			return Result{Stdout: []byte(strconv.Itoa(w.cursorX) + " " + strconv.Itoa(w.cursorY) + "\n")}, nil
		}
		cur := sessiontitle.ObservedCursor(w.lastScreen)
		if !cur.Known {
			return Result{}, errTest("cursor")
		}
		return Result{Stdout: []byte(strconv.Itoa(cur.X) + " " + strconv.Itoa(cur.Y) + "\n")}, nil
	case "send-keys":
		literal := slices.Contains(c.Args, "-l")
		if literal {
			if w.sendErr {
				return Result{}, errTest("send")
			}
			if w.afterText != nil {
				w.afterText()
			}
			return Result{}, nil
		}
		if w.confirmTo != "" {
			w.spec.name = w.confirmTo
			w.spec.source = "user"
			w.spec.updated = 0
			w.writeRecord()
		}
		if slices.Contains(c.Args, "Backspace") {
			w.screens = []string{sessiontitle.IdleEmptyScreen}
			w.capN = 0
			w.lastScreen = sessiontitle.IdleEmptyScreen
			w.cursorSet = false
			return Result{}, nil
		}
		if w.enterErr {
			return Result{}, errTest("enter")
		}
		return Result{}, nil
	default:
		return Result{}, nil
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func (f *fakeRunner) snapshot() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Cmd(nil), f.calls...)
}

func (w *renameWorld) writes() [][]string {
	var out [][]string
	for _, c := range w.run.snapshot() {
		if c.Name != "tmux" || len(c.Args) == 0 {
			continue
		}
		switch c.Args[0] {
		case "send-keys", "load-buffer", "paste-buffer", "set-buffer", "delete-buffer":
			out = append(out, append([]string(nil), c.Args...))
		}
	}
	return out
}

func (w *renameWorld) assertNoWrite(t *testing.T) {
	t.Helper()
	allowed := map[string]bool{
		"list-panes": true, "list-clients": true, "capture-pane": true, "display-message": true,
	}
	for _, c := range w.run.snapshot() {
		switch c.Name {
		case "claude":
			if !slices.Equal(c.Args, []string{"--version"}) {
				t.Fatalf("claude %q", c.Args)
			}
		case "tmux":
			if len(c.Args) == 0 || !allowed[c.Args[0]] {
				t.Fatalf("tmux subcommand %q is not read-only", c.Args)
			}
		default:
			t.Fatalf("command %s %q", c.Name, c.Args)
		}
	}
}

func (w *renameWorld) assertTyped(t *testing.T, target string) {
	t.Helper()
	want := [][]string{
		{"send-keys", "-t", "%0", "-l", "--", "/rename " + target},
		{"send-keys", "-t", "%0", "Enter"},
	}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
	}
}

func (w *renameWorld) hold(t *testing.T, text, reason string) titleDisk {
	t.Helper()
	disk := w.apply(text)
	if disk.State != "Held" || disk.Reason != reason || disk.Since.IsZero() {
		t.Fatalf("state %s reason %s since %v, want Held %s", disk.State, disk.Reason, disk.Since, reason)
	}
	w.assertNoWrite(t)
	return disk
}

func screenHolding(text string) string {
	const mark = "❯\u00a0"
	base := sessiontitle.IdleEmptyScreen
	i := strings.LastIndex(base, mark)
	if i < 0 {
		panic("recorded idle screen has no input mark")
	}
	rest := base[i+len(mark):]
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return base[:i+len(mark)] + text
	}
	return base[:i+len(mark)] + text + rest[nl:]
}

func (w *renameWorld) saveDisk(d titleDisk) {
	w.t.Helper()
	d.UID = w.sess.SessionUID
	writeJSON(w.t, w.s.statePath(titleStateFile), d)
}

func TestRenameKindTaskTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Mode = protocol.ModeTask
	w.apply("Café")
	w.assertNoWrite(t)
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("a task wrote title state")
	}
}

func TestRenameKindLocalTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Mode = protocol.ModeLocal
	w.apply("Café")
	w.assertNoWrite(t)
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("a local session wrote title state")
	}
}

func TestRenameKindCodexTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Agent = protocol.AgentCodex
	w.apply("Café")
	w.assertNoWrite(t)
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("a Codex session wrote title state")
	}
}

func TestRenameKindSummonedTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Caller = "ops/summoner"
	w.apply("Café")
	w.assertNoWrite(t)
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("a summoned session wrote title state")
	}
}

func TestRenameKindExplicitContinuationTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	writeFile(t, w.s.statePath(explicitClaudeRCFile), "{")
	w.apply("Café")
	w.assertNoWrite(t)
	if len(w.run.snapshot()) != 0 {
		t.Fatalf("explicit continuation called %q", w.run.lines())
	}
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("an explicit continuation wrote title state")
	}
}

func TestRenameGuardProcessDeadPID(t *testing.T) {
	w := newRenameWorld(t)
	writeJSON(t, w.s.statePath(pidFile), agentPid{Pid: 1 << 30, Start: "1"})
	w.hold(t, "Café", "process")
}

func TestRenameGuardNotRegistered(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.bridge = ""
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "not-registered")
}

func TestRenameGuardRecordShapeMissingName(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.omitName = true
	w.spec.updated = 0
	w.writeRecord()
	disk := w.apply("Café")
	if disk.State != "Unknown" || disk.Reason != "record-shape" {
		t.Fatalf("state %s reason %s", disk.State, disk.Reason)
	}
	w.assertNoWrite(t)
}

func TestRenameGuardRecordShapeMissingNameSource(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.omitSource = true
	w.spec.updated = 0
	w.writeRecord()
	disk := w.apply("Café")
	if disk.State != "Unknown" || disk.Reason != "record-shape" {
		t.Fatalf("state %s reason %s", disk.State, disk.Reason)
	}
	w.assertNoWrite(t)
}

func TestRenameGuardBusy(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "busy")
}

func TestRenameGuardWaiting(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "waiting"
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "waiting")
}

func TestRenameGuardStatusUnknown(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "thinking"
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "status-unknown")
}

func TestRenameGuardShell(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "shell"
	target := w.composed("Café")
	w.arm(target, true)
	w.writeRecord()
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target {
		t.Fatalf("shell was not applied: %+v", disk)
	}
}

func TestRenameGuardShellRecentHoldsQuiet(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "shell"
	w.spec.updated = 0
	w.spec.age = 10 * time.Second
	w.writeRecord()
	w.hold(t, "Café", "quiet")
}

func TestRenameGuardQuietIdle10s(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.age = 10 * time.Second
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "quiet")
}

func TestRenameGuardQuietAgeZero(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.age = 0
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "quiet")
}

func TestRenameGuardClockFutureUpdatedAt(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.future = true
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "clock")
}

func TestRenameGuardProcessProcStart(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.proc = "not-this-process"
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "process")
}

func TestRenameGuardProcessOver64KiB(t *testing.T) {
	w := newRenameWorld(t)
	writeFile(t, w.recordPath(), strings.Repeat("x", 64<<10+1))
	w.hold(t, "Café", "process")
}

func TestRenameGuardProcessLink(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	other := filepath.Join(t.TempDir(), "not-the-record.json")
	writeJSON(t, other, map[string]any{
		"bridgeSessionId": "session_01AbCdEf", "procStart": w.start,
		"status": "idle", "updatedAt": w.clock.Now().Add(-time.Minute).UnixMilli(),
		"name": target, "nameSource": "user",
	})
	if err := os.Remove(w.recordPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, w.recordPath()); err != nil {
		t.Fatal(err)
	}
	w.hold(t, "Café", "process")
}

func TestRenameGuardProcessBadJSON(t *testing.T) {
	w := newRenameWorld(t)
	writeFile(t, w.recordPath(), "{")
	w.hold(t, "Café", "process")
}

func TestRenameGuardAttachedClient(t *testing.T) {
	w := newRenameWorld(t)
	w.clients = "/dev/pts/1"
	w.hold(t, "Café", "attached")
}

func TestRenameGuardAttachedListClientsError(t *testing.T) {
	w := newRenameWorld(t)
	w.clientErr = true
	w.hold(t, "Café", "attached")
}

func TestRenameGuardPaneTwoPanes(t *testing.T) {
	w := newRenameWorld(t)
	w.panes = "0 0 %0 0 0 0 2028 claude\n0 1 %1 0 0 0 2029 bash"
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPaneTwoWindows(t *testing.T) {
	w := newRenameWorld(t)
	w.panes = "0 0 %0 0 0 0 2028 claude\n1 0 %1 0 0 0 2028 claude"
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPaneCopyMode(t *testing.T) {
	w := newRenameWorld(t)
	w.panes = "0 0 %0 0 1 0 2028 claude"
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPaneSynchronize(t *testing.T) {
	w := newRenameWorld(t)
	w.panes = "0 0 %0 0 0 1 2028 claude"
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPaneDead(t *testing.T) {
	w := newRenameWorld(t)
	w.panes = "0 0 %0 1 0 0 2028 claude"
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPanePidNotParent(t *testing.T) {
	w := newRenameWorld(t)
	readProcIDs = func(int) (procIDs, error) { return procIDs{ppid: 1, pgrp: 7, tpgid: 7}, nil }
	w.hold(t, "Café", "pane")
}

func TestRenameGuardPaneNotForeground(t *testing.T) {
	w := newRenameWorld(t)
	readProcIDs = func(int) (procIDs, error) { return procIDs{ppid: 2028, pgrp: 7, tpgid: 8}, nil }
	w.hold(t, "Café", "pane")
}

func TestRenameGuardCLIVersionUnknown(t *testing.T) {
	for _, version := range []string{"2.2.0 (Claude Code)\n", "2.0.9 (Claude Code)\n", "Claude Code\n"} {
		t.Run(version, func(t *testing.T) {
			w := newRenameWorld(t)
			w.version = version
			w.hold(t, "Café", "cli-version")
		})
	}
}

func TestRenameGuardInputText(t *testing.T) {
	w := newRenameWorld(t)
	w.screens = []string{screenHolding("hello")}
	w.hold(t, "Café", "input")
}

func TestRenameGuardScreenUnknownQuestion(t *testing.T) {
	// question-open.2.1.321.ansi is the recorded screen with a question open.
	w := newRenameWorld(t)
	w.screens = []string{sessiontitle.QuestionOpenScreen}
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardScreenUnknownPermission(t *testing.T) {
	// Derived from question-open.2.1.321.ansi by renaming the question words.
	// The selection list and the cancel hint are the recorded ones.
	w := newRenameWorld(t)
	derived := strings.ReplaceAll(sessiontitle.QuestionOpenScreen, "Fixture: pick one", "Permission: pick one")
	if derived == sessiontitle.QuestionOpenScreen {
		t.Fatal("derived screen matches the recorded question")
	}
	w.screens = []string{derived}
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardScreenUnknownUnrecognised(t *testing.T) {
	w := newRenameWorld(t)
	w.screens = []string{"$ "}
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardScreenUnknownCaptureError(t *testing.T) {
	w := newRenameWorld(t)
	w.captureErr = true
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardPaced(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{
		Applied: "other", Target: target, State: "Held", Reason: "busy",
		Since: w.clock.Now().Add(-time.Minute), LastTypedAt: w.clock.Now().Add(-time.Minute),
	})
	w.hold(t, "Café", "paced")
}

func TestRenameGuardDelivering(t *testing.T) {
	w := newRenameWorld(t)
	path := w.s.statePath(deliverLockFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	})
	disk := w.hold(t, "Café", "delivering")
	if disk.Reason != "delivering" {
		t.Fatal(disk.Reason)
	}
	for _, c := range w.run.snapshot() {
		if c.Name != "claude" || !slices.Equal(c.Args, []string{"--version"}) {
			t.Fatalf("a held delivery read the session: %q", w.run.lines())
		}
	}
}

func TestRenameKeystrokesTitleWithE(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	if target != "Haiku | Café" {
		t.Fatalf("composed %q", target)
	}
	w.arm(target, true)
	var buf strings.Builder
	w.log = slog.New(&lineLog{b: &buf})
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	if disk.ProviderTitle != target {
		t.Fatalf("provider title %q", disk.ProviderTitle)
	}
	text := buf.String()
	hash := sessiontitle.HashPrefix(target)
	if !strings.Contains(text, hash) || !strings.Contains(text, "kind=text") || !strings.Contains(text, "kind=enter") {
		t.Fatalf("log %q", text)
	}
	if strings.Contains(text, "Café") || strings.Contains(text, "Haiku") || strings.Contains(text, "/rename") {
		t.Fatalf("log holds the title %q", text)
	}
}

func TestRenameKeystrokesClearSessionName(t *testing.T) {
	w := newRenameWorld(t)
	w.screens = []string{sessiontitle.IdlePlaceholderScreen, screenHolding("/rename " + w.sess.Name)}
	w.confirmTo = w.sess.Name
	w.saveDisk(titleDisk{Applied: "Haiku | Café", Target: "Haiku | Café", State: "Applied", Reason: "confirmed"})
	disk := w.apply("")
	w.assertTyped(t, w.sess.Name)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != w.sess.Name {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameCLIVersionPatchStillTypes(t *testing.T) {
	w := newRenameWorld(t)
	w.version = "2.1.999 (Claude Code)\n"
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameCLIVersionOncePerBoot(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{
		Applied: "other", Target: target, LastTypedAt: w.clock.Now().Add(-time.Minute),
		State: "Held", Reason: "busy", Since: w.clock.Now(),
	})
	w.hold(t, "Café", "paced")
	w.hold(t, "Café", "paced")
	n := 0
	for _, c := range w.run.snapshot() {
		if c.Name == "claude" && slices.Equal(c.Args, []string{"--version"}) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("claude --version ran %d times", n)
	}
}

func TestRenameSecondCheckWithholdsEnter(t *testing.T) {
	target := sessiontitle.Compose("claude-haiku-4-5", "Café")
	// interrupt is a second check that fails while the input row still holds
	// the text this pod typed. agentd then sends one Backspace per rune.
	// The other cases are screens that do not hold that text, so nothing
	// else is typed.
	cases := []struct {
		name      string
		interrupt bool
		edit      func(*renameWorld)
	}{
		{"status", true, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() {
				w.spec.status = "busy"
				w.writeRecord()
			}
		}},
		{"updatedAt", true, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() {
				w.spec.updated = w.clock.Now().UnixMilli()
				w.writeRecord()
			}
		}},
		{"pane", true, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.panes = "0 0 %1 0 0 0 2028 claude" }
		}},
		{"client", true, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.clients = "/dev/pts/4" }
		}},
		{"input", false, func(w *renameWorld) {
			w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.IdleEmptyScreen}
		}},
		{"prompt", false, func(w *renameWorld) {
			// The second capture is the recorded question-open screen.
			w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.QuestionOpenScreen}
		}},
		{"foreground", true, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() {
				readProcIDs = func(int) (procIDs, error) {
					return procIDs{ppid: 2028, pgrp: 7, tpgid: 9}, nil
				}
			}
		}},
		{"cursor", false, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.cursorX, w.cursorY, w.cursorSet = 2, 46, true }
		}},
		{"slow", true, func(w *renameWorld) {
			w.arm(target, false)
			w.onRecheck = func() { w.clock.Advance(time.Second) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRenameWorld(t)
			tc.edit(w)
			disk := w.apply("Café")
			text := []string{"send-keys", "-t", "%0", "-l", "--", "/rename " + target}
			if !tc.interrupt {
				if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
					t.Fatalf("disk %+v", disk)
				}
				if !reflect.DeepEqual(w.writes(), [][]string{text}) {
					t.Fatalf("writes %q", w.writes())
				}
				return
			}
			if disk.State != "Held" || disk.Reason != "interrupted" || disk.Stopped || disk.Attempts != 0 {
				t.Fatalf("disk %+v", disk)
			}
			backs := make([]string, utf8.RuneCountInString("/rename "+target))
			for i := range backs {
				backs[i] = "Backspace"
			}
			want := [][]string{text, append([]string{"send-keys", "-t", "%0"}, backs...)}
			if !reflect.DeepEqual(w.writes(), want) {
				t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
			}
		})
	}
}

func TestRenameInterruptedClearsExactText(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	const title = "Café"
	typed := "/rename " + title
	if utf8.RuneCountInString(typed) == len(typed) || !strings.Contains(typed, "é") {
		t.Fatalf("fixture %q is not a non-ASCII title", typed)
	}
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding(typed), sessiontitle.IdleEmptyScreen}
	w.afterText = func() {
		w.spec.status = "busy"
		w.writeRecord()
	}
	disk := w.apply(title)
	if disk.State != "Held" || disk.Reason != "interrupted" || disk.Stopped || disk.Attempts != 0 || disk.LastTypedAt.IsZero() {
		t.Fatalf("disk %+v", disk)
	}
	backs := make([]string, utf8.RuneCountInString(typed))
	for i := range backs {
		backs[i] = "Backspace"
	}
	want := [][]string{
		{"send-keys", "-t", "%0", "-l", "--", typed},
		append([]string{"send-keys", "-t", "%0"}, backs...),
	}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
	}
}

func TestRenameNoBackspaceWhenTheRowDiffers(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	typed := "/rename Café"
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding(typed + "x")}
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	want := [][]string{{"send-keys", "-t", "%0", "-l", "--", typed}}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes %q", w.writes())
	}
}

func TestRenameNoBackspaceWhenTheCursorIsElsewhere(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	typed := "/rename Café"
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding(typed)}
	w.afterText = func() { w.cursorX, w.cursorY, w.cursorSet = 2, 46, true }
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	want := [][]string{{"send-keys", "-t", "%0", "-l", "--", typed}}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes %q", w.writes())
	}
}

func TestRenameNoBackspaceWhenCaptureFails(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	typed := "/rename Café"
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding(typed)}
	w.afterText = func() { w.captureErr = true }
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	want := [][]string{{"send-keys", "-t", "%0", "-l", "--", typed}}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes %q", w.writes())
	}
}

func TestRenameSendKeysErrorLeftInInput(t *testing.T) {
	w := newRenameWorld(t)
	w.sendErr = true
	w.arm(w.composed("Café"), true)
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped {
		t.Fatalf("disk %+v", disk)
	}
	for _, args := range w.writes() {
		if slices.Contains(args, "Enter") {
			t.Fatalf("Enter after a failed text send: %q", w.writes())
		}
	}
}

func TestRenameEnterErrorDoesNotRetry(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	w.enterErr = true
	disk := w.apply("Café")
	if disk.State != "Typed" || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	enters := 0
	for _, args := range w.writes() {
		if slices.Contains(args, "Enter") {
			enters++
		}
	}
	if enters != 1 {
		t.Fatalf("Enter count %d in %q", enters, w.writes())
	}
}

func TestRenameHeldThenApplied(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	first := w.hold(t, "Café", "busy")
	w.spec.status = "idle"
	w.spec.updated = 0
	w.writeRecord()
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" || !disk.Since.After(first.Since) {
		t.Fatalf("disk %+v first %v", disk, first.Since)
	}
}

func TestRenameNewerTitleReplacesHeld(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	first := w.hold(t, "Café", "busy")
	w.clock.Advance(time.Minute)
	w.spec.status = "waiting"
	w.spec.updated = 0
	w.writeRecord()
	disk := w.hold(t, "Ops", "waiting")
	if disk.Target != "Haiku | Ops" || disk.Attempts != 0 || !disk.Since.After(first.Since) {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameClearReplacesHeld(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	first := w.hold(t, "Café", "busy")
	w.clock.Advance(time.Minute)
	disk := w.hold(t, "", "busy")
	if disk.Target != w.sess.Name || disk.Attempts != 0 || !disk.Since.After(first.Since) {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameConfirmedWithin10s(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, true)
	start := w.clock.Now()
	disk := w.apply("Café")
	if disk.State != "Applied" || disk.Reason != "confirmed" {
		t.Fatalf("disk %+v", disk)
	}
	if w.clock.Now().Sub(start) >= 10*time.Second {
		t.Fatalf("confirmation took %s", w.clock.Now().Sub(start))
	}
	w.assertTyped(t, target)
}

func TestRenameNotConfirmedThenConfirmedLater(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	first := w.apply("Café")
	if first.State != "Typed" || first.Attempts != 1 {
		t.Fatalf("first %+v", first)
	}
	w.clock.Advance(30 * time.Second)
	w.spec.name = target
	w.spec.source = "user"
	w.spec.updated = 0
	w.writeRecord()
	before := len(w.writes())
	disk := w.apply("Café")
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target {
		t.Fatalf("disk %+v", disk)
	}
	if len(w.writes()) != before {
		t.Fatalf("typed again %q", w.writes())
	}
}

func TestRenameNotConfirmedTwiceThenFailed(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	first := w.apply("Café")
	if first.State != "Typed" || first.Attempts != 1 {
		t.Fatalf("first %+v", first)
	}
	w.clock.Advance(renameTypedFor)
	paced := w.apply("Café")
	if paced.State != "Held" || paced.Reason != "paced" {
		t.Fatalf("paced %+v", paced)
	}
	w.clock.Advance(renamePaceFor)
	w.arm(target, false)
	second := w.apply("Café")
	if second.State != "Typed" || second.Attempts != 2 {
		t.Fatalf("second %+v", second)
	}
	w.clock.Advance(renameTypedFor)
	failed := w.apply("Café")
	if failed.State != "Failed" || failed.Reason != "not-confirmed" || !failed.Stopped {
		t.Fatalf("failed %+v", failed)
	}
	w.clock.Advance(renamePaceFor)
	again := w.apply("Café")
	if again.State != "Failed" || again.Reason != "not-confirmed" {
		t.Fatalf("again %+v", again)
	}
	text := 0
	for _, args := range w.writes() {
		if slices.Contains(args, "-l") {
			text++
		}
	}
	if text != 2 {
		t.Fatalf("typed %d times: %q", text, w.writes())
	}
}

func TestRenamePacingAcrossRestart(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{
		Applied: "other", Target: target, LastTypedAt: w.clock.Now().Add(-time.Minute),
		State: "Held", Reason: "busy", Since: w.clock.Now().Add(-time.Minute),
	})
	w.app = &TitleApplier{Clock: w.clock}
	w.hold(t, "Café", "paced")
}

func TestRenameLeftInInputLaterSentBecomesApplied(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.IdleEmptyScreen}
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped {
		t.Fatalf("disk %+v", disk)
	}
	if len(w.writes()) != 1 {
		t.Fatalf("writes %q", w.writes())
	}
	w.spec.name = target
	w.spec.source = "user"
	w.spec.updated = 0
	w.writeRecord()
	got := w.apply("Café")
	if got.State != "Applied" || got.Reason != "confirmed" || got.Applied != target {
		t.Fatalf("later %+v", got)
	}
	if len(w.writes()) != 1 {
		t.Fatalf("typed again %q", w.writes())
	}
}

func TestRenameFirstLaunchTitleTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	disk := w.apply("Café")
	if disk.State != "Applied" || disk.Reason != "launch" || disk.Applied != "Haiku | Café" {
		t.Fatalf("disk %+v", disk)
	}
	w.assertNoWrite(t)
	again := w.apply("Café")
	if again.Reason != "launch" || again.Applied != "Haiku | Café" {
		t.Fatalf("second heartbeat %+v", again)
	}
	w.assertNoWrite(t)
}

func TestRenameFirstLaunchLaterTitleTypes(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	first := w.apply("Café")
	if first.Reason != "launch" {
		t.Fatalf("launch %+v", first)
	}
	w.assertNoWrite(t)
	target := w.composed("Other")
	w.arm(target, true)
	disk := w.apply("Other")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target {
		t.Fatalf("later %+v", disk)
	}
}

func TestRenameResumeAfterChangeTypesOnce(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(false)
	w.saveDisk(titleDisk{Applied: "Haiku | Old", Target: "Haiku | Old", State: "Applied", Reason: "launch"})
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameResumeWithNoChangeTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{Applied: target, Target: target, State: "Applied", Reason: "confirmed"})
	disk := w.apply("Café")
	if disk.Applied != target || len(w.writes()) != 0 {
		t.Fatalf("disk %+v writes %q", disk, w.writes())
	}
}

func TestRenameNoTitleFileAndNoTitleTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	disk := w.apply("")
	if disk.found || disk.State != "" || disk.Applied != "" {
		t.Fatalf("disk %+v", disk)
	}
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("a session that never had a title wrote title.json")
	}
	w.assertNoWrite(t)
}

func TestRenameNoTitleFileAppliesOnce(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.Attempts != 1 || disk.State != "Applied" {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameOtherUIDCountsAsMissing(t *testing.T) {
	w := newRenameWorld(t)
	writeJSON(t, w.s.statePath(titleStateFile), titleDisk{UID: "someone-else", Applied: "Haiku | Café", State: "Applied", Reason: "confirmed"})
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.UID != w.sess.SessionUID || disk.State != "Applied" {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameUserNameNotTargetTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{Applied: target, Target: target, State: "Applied", Reason: "confirmed"})
	w.spec.name = "Someone Else"
	w.spec.source = "user"
	w.spec.updated = 0
	w.writeRecord()
	disk := w.apply("Café")
	w.assertNoWrite(t)
	if disk.Applied != target || disk.ProviderTitle != "Someone Else" {
		t.Fatalf("disk %+v", disk)
	}
}

func TestRenameAnswerWithoutTitleObjectTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.app.Apply(context.Background(), w.run, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{})
	w.assertNoWrite(t)
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("an answer without a title object wrote state")
	}
}

func TestRenameInvalidTitleTypesNothing(t *testing.T) {
	for _, text := range []string{"bad|title", " Café"} {
		t.Run(text, func(t *testing.T) {
			w := newRenameWorld(t)
			disk := w.apply(text)
			if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
				t.Fatalf("disk %+v", disk)
			}
			w.assertNoWrite(t)
		})
	}
}

func TestRenameLockReleasedBeforeConfirm(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	n := 0
	acquired := false
	w.clock.onSleep = func() {
		n++
		if n < 2 {
			return
		}
		f, err := os.OpenFile(w.s.statePath(deliverLockFile), os.O_RDWR, 0)
		if err != nil {
			t.Errorf("lock file: %v", err)
			return
		}
		defer func() { _ = f.Close() }()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Errorf("lock still held during confirmation: %v", err)
			return
		}
		acquired = true
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	}
	disk := w.apply("Café")
	if !acquired || disk.State != "Typed" {
		t.Fatalf("acquired %v disk %+v sleeps %d", acquired, disk, n)
	}
}

func TestRenameForeignLaunchTypesNothing(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Launch)
	}{
		{"other session", func(l *Launch) { l.Session = "other-session" }},
		{"other uid", func(l *Launch) { l.SessionUID = "other-uid" }},
		{"not a tui", func(l *Launch) { l.TUI = false }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRenameWorld(t)
			w.boot(true)
			var l Launch
			if readJSONFile(w.s.statePath(launchFile), &l) != nil {
				t.Fatal("launch")
			}
			tc.edit(&l)
			writeJSON(t, w.s.statePath(launchFile), l)
			disk := w.hold(t, "Café", "process")
			if disk.Applied != "" {
				t.Fatalf("seeded %q", disk.Applied)
			}
		})
	}
}

func TestRenameTitleWriteFailureTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, true)
	orig := writeTitleFile
	n := 0
	writeTitleFile = func(s Settings, d *titleDisk) error {
		n++
		if n == 1 {
			return errors.New("full")
		}
		return orig(s, d)
	}
	t.Cleanup(func() { writeTitleFile = orig })
	disk := w.apply("Café")
	if disk.State != "Held" || disk.Reason != "state" {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameUpdatedAtNotAPositiveNumber(t *testing.T) {
	t.Run("null", func(t *testing.T) {
		w := newRenameWorld(t)
		w.spec.updatedNull = true
		w.spec.updated = 0
		w.writeRecord()
		disk := w.apply("Café")
		if disk.State != "Unknown" || disk.Reason != "record-shape" {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
	t.Run("string", func(t *testing.T) {
		w := newRenameWorld(t)
		w.spec.updatedString = true
		w.spec.updated = 0
		w.writeRecord()
		disk := w.apply("Café")
		if disk.State != "Unknown" || disk.Reason != "record-shape" {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
	t.Run("zero", func(t *testing.T) {
		w := newRenameWorld(t)
		writeJSON(t, w.recordPath(), map[string]any{
			"bridgeSessionId": "session_01AbCdEf", "procStart": w.start,
			"status": "idle", "updatedAt": 0, "name": "derived-name", "nameSource": "api",
		})
		disk := w.apply("Café")
		if disk.State != "Unknown" || disk.Reason != "record-shape" {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
	t.Run("missing", func(t *testing.T) {
		w := newRenameWorld(t)
		writeJSON(t, w.recordPath(), map[string]any{
			"bridgeSessionId": "session_01AbCdEf", "procStart": w.start,
			"status": "idle", "name": "derived-name", "nameSource": "api",
		})
		disk := w.apply("Café")
		if disk.State != "Unknown" || disk.Reason != "record-shape" {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
}

type hangRunner struct {
	inner Runner
	hang  atomic.Bool
}

func (h *hangRunner) LookPath(name string) (string, error) { return h.inner.LookPath(name) }

func (h *hangRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if h.hang.Load() && c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "list-clients" {
		h.hang.Store(false)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	return h.inner.Run(ctx, c)
}

func TestRenameHungCallDoesNotStopLaterHeartbeats(t *testing.T) {
	w := newRenameWorld(t)
	h := &hangRunner{inner: w.run}
	h.hang.Store(true)
	start := time.Now()
	w.app.Apply(context.Background(), h, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{Present: true, Text: "Café"})
	if time.Since(start) > 4*time.Second {
		t.Fatalf("heartbeat waited %s", time.Since(start))
	}
	disk := loadTitle(w.s, w.sess)
	if disk.Reason != "attached" {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
	target := w.composed("Café")
	w.arm(target, true)
	start = time.Now()
	disk = w.apply("Café")
	if time.Since(start) > time.Second {
		t.Fatalf("later heartbeat waited %s", time.Since(start))
	}
	w.assertTyped(t, target)
}

func TestRenameWhitespaceAndMultilineInputTypesNothing(t *testing.T) {
	t.Run("spaces", func(t *testing.T) {
		w := newRenameWorld(t)
		// Derived from the recorded idle fixture: spaces in its input row.
		w.screens = []string{screenHolding("   ")}
		w.hold(t, "Café", "screen-unknown")
	})
	t.Run("multiline", func(t *testing.T) {
		w := newRenameWorld(t)
		// Derived from the recorded idle fixture by a blank row inside the box.
		base := sessiontitle.IdleEmptyScreen
		const mark = "❯\u00a0"
		i := strings.LastIndex(base, mark)
		nl := strings.IndexByte(base[i:], '\n')
		at := i + nl + 1
		w.screens = []string{base[:at] + "\n" + base[at:]}
		w.hold(t, "Café", "screen-unknown")
	})
}

func TestRenameTrailingSemicolonTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	disk := w.apply("ab;")
	if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameListPanesFormatIsRecorded(t *testing.T) {
	raw, err := os.ReadFile("../sessiontitle/testdata/tmux-facts.txt")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("facts %q", lines)
	}
	format := lines[3]
	if format != sessiontitle.ListPanesFormat() || strings.Contains(format, "window_synchronized") || !strings.Contains(format, "#{pane_synchronized}") {
		t.Fatal(format)
	}
	w := newRenameWorld(t)
	if w.panes != lines[1] {
		t.Fatalf("fake pane line %q", w.panes)
	}
	target := w.composed("Café")
	w.arm(target, true)
	w.apply("Café")
	saw := false
	for _, c := range w.run.snapshot() {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "list-panes" {
			saw = true
			if !slices.Contains(c.Args, format) {
				t.Fatalf("argv %q", c.Args)
			}
		}
	}
	if !saw {
		t.Fatal("no list-panes")
	}
}

func TestRenameBlankClientCounts(t *testing.T) {
	w := newRenameWorld(t)
	w.clientsExact = "\n"
	w.clientsExactSet = true
	w.hold(t, "Café", "attached")
}

func TestRenameCommandShadowed(t *testing.T) {
	w := newRenameWorld(t)
	dir := filepath.Join(w.s.Home, "proj", ".claude", "commands")
	writeFile(t, filepath.Join(dir, "rename.md"), "name\n")
	w.hold(t, "Café", "command-shadowed")
}

func TestRenameCommandShadowUnreadable(t *testing.T) {
	w := newRenameWorld(t)
	if err := os.MkdirAll(filepath.Join(w.s.ClaudeConfigDir, "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	w.hold(t, "Café", "command-shadowed")
}

func TestRenameSinceSetOnceAndFileNotRewritten(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	first := w.hold(t, "Café", "busy")
	path := w.s.statePath(titleStateFile)
	old := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	w.clock.Advance(time.Minute)
	second := w.hold(t, "Café", "busy")
	if !second.Since.Equal(first.Since) {
		t.Fatalf("since %s -> %s", first.Since, second.Since)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().UTC().Equal(old) {
		t.Fatalf("rewrote title.json at %s", info.ModTime())
	}
}

func TestRenameClearWhenAlreadyTheSessionName(t *testing.T) {
	w := newRenameWorld(t)
	w.saveDisk(titleDisk{Applied: w.sess.Name, Target: w.sess.Name, State: "Applied", Reason: "already"})
	disk := w.apply("")
	if disk.State != "Applied" || disk.Reason != "already" || disk.Applied != w.sess.Name {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameUnknownTitleTextDoesNotClear(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{Applied: target, Target: target, State: "Applied", Reason: "confirmed"})
	before, err := os.ReadFile(w.s.statePath(titleStateFile))
	if err != nil {
		t.Fatal(err)
	}
	w.app.Apply(context.Background(), w.run, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{Unknown: true})
	w.assertNoWrite(t)
	after, err := os.ReadFile(w.s.statePath(titleStateFile))
	if err != nil || string(after) != string(before) {
		t.Fatalf("state changed %s", after)
	}
}

type lineLog struct{ b *strings.Builder }

func (l *lineLog) Enabled(context.Context, slog.Level) bool { return true }
func (l *lineLog) Handle(_ context.Context, r slog.Record) error {
	l.b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		l.b.WriteByte(' ')
		l.b.WriteString(a.Key)
		l.b.WriteByte('=')
		l.b.WriteString(a.Value.String())
		return true
	})
	l.b.WriteByte('\n')
	return nil
}
func (l *lineLog) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *lineLog) WithGroup(string) slog.Handler      { return l }
