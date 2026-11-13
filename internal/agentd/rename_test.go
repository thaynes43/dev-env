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
	clientN         int
	textSent        bool
	// derivedCursor lets a screen that was built in the test use ObservedCursor.
	// A recorded screen answers from RecordedCursor and does not need this.
	derivedCursor  bool
	cursorX        int
	cursorY        int
	cursorSet      bool
	onSecondClient func()
	// paneWidth and paneHeight are the four-number cursor reply. Zero means
	// the recorded 200 by 50.
	paneWidth  int
	paneHeight int
	cursorN    int
	// narrowFrom makes display-message reads at that count and after use
	// narrowWidth. Zero leaves every read at paneWidth.
	narrowFrom  int
	narrowWidth int
	// cursorRaw, when set, is the whole display-message reply.
	cursorRaw string
	// preCaps counts captures before the title text is sent. secondScreen
	// replaces the frame from the second of those captures.
	preCaps            int
	secondScreen       string
	keepAfterBackspace bool

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
	// The holding screen is derived from the idle fixture. It has no recorded
	// cursor, so the runner answers it with ObservedCursor. The idle screen
	// is recorded and answers from RecordedCursor.
	w.derivedCursor = true
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
	w.clientN = 0
	w.cursorN = 0
	w.preCaps = 0
	w.textSent = false
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
		w.clientN++
		if w.clientN >= 2 && w.onSecondClient != nil {
			w.onSecondClient()
		}
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
		// Before the text is sent, every capture is the first screen, so the
		// early read and the read after the staged write see the same frame
		// unless secondScreen replaces the second of those captures.
		// After the text, captures walk the frames that follow it.
		screen := sessiontitle.IdleEmptyScreen
		frames := w.screens
		if w.textSent && len(frames) > 1 {
			frames = frames[1:]
		}
		if len(frames) > 0 {
			if w.textSent {
				i := w.capN
				if i >= len(frames) {
					i = len(frames) - 1
				}
				screen = frames[i]
				w.capN++
			} else {
				w.preCaps++
				screen = frames[0]
				if w.preCaps >= 2 && w.secondScreen != "" {
					screen = w.secondScreen
				}
			}
		}
		w.lastScreen = screen
		return Result{Stdout: []byte(screen)}, nil
	case "display-message":
		if w.cursorErr {
			return Result{}, errTest("cursor")
		}
		if w.cursorRaw != "" {
			return Result{Stdout: []byte(w.cursorRaw + "\n")}, nil
		}
		if w.cursorSet {
			return Result{Stdout: []byte(w.cursorLine(w.cursorX, w.cursorY) + "\n")}, nil
		}
		if cur, ok := sessiontitle.RecordedCursor(w.lastScreen); ok {
			return Result{Stdout: []byte(w.cursorLine(cur.X, cur.Y) + "\n")}, nil
		}
		if w.derivedCursor {
			cur := sessiontitle.ObservedCursor(w.lastScreen)
			if cur.Known {
				return Result{Stdout: []byte(w.cursorLine(cur.X, cur.Y) + "\n")}, nil
			}
		}
		return Result{}, errTest("cursor")
	case "send-keys":
		literal := slices.Contains(c.Args, "-l")
		if literal {
			w.textSent = true
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
			if !w.keepAfterBackspace {
				w.screens = []string{sessiontitle.IdleEmptyScreen}
				w.capN = 0
				w.textSent = true
				w.lastScreen = sessiontitle.IdleEmptyScreen
				w.cursorSet = false
			}
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

func (w *renameWorld) cursorLine(x, y int) string {
	w.cursorN++
	width := w.paneWidth
	if width <= 0 {
		width = 200
	}
	height := w.paneHeight
	if height <= 0 {
		height = 50
	}
	if w.narrowFrom > 0 && w.cursorN >= w.narrowFrom {
		width = w.narrowWidth
	}
	return strconv.Itoa(x) + " " + strconv.Itoa(y) + " " + strconv.Itoa(width) + " " + strconv.Itoa(height)
}

func (w *renameWorld) leftDisk(target string) titleDisk {
	return titleDisk{
		State: "Failed", Reason: "left-in-input", Target: target, Stopped: true,
		PaneID: "%0", CLIPid: w.pid, ProcStart: w.start,
	}
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
	w.assertNoWriteSince(t, 0)
}

func (w *renameWorld) assertNoWriteSince(t *testing.T, mark int) {
	t.Helper()
	allowed := map[string]bool{
		"list-panes": true, "list-clients": true, "capture-pane": true, "display-message": true,
	}
	calls := w.run.snapshot()
	if mark > len(calls) {
		t.Fatalf("mark %d past %d calls", mark, len(calls))
	}
	for _, c := range calls[mark:] {
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

func (w *renameWorld) writesSince(mark int) [][]string {
	var out [][]string
	calls := w.run.snapshot()
	if mark > len(calls) {
		return nil
	}
	for _, c := range calls[mark:] {
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
	// screenHolding is derived from the idle fixture. It has no recorded
	// cursor, so this test uses ObservedCursor.
	w.derivedCursor = true
	w.screens = []string{screenHolding("hello")}
	w.hold(t, "Café", "input")
}

func TestRenameGuardScreenUnknownQuestion(t *testing.T) {
	// question-open.2.1.321.ansi is the recorded screen with a question open.
	// It has no recorded cursor. Column 2 on row 46 is the empty-input cursor,
	// so a parser that called this screen an empty box would type.
	w := newRenameWorld(t)
	w.screens = []string{sessiontitle.QuestionOpenScreen}
	w.cursorX, w.cursorY, w.cursorSet = 2, 46, true
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardScreenUnknownUnrecognised(t *testing.T) {
	// "$ " is not a recorded screen. Column 2 on row 46 is supplied so the
	// hold is the screen, not a missing cursor. ObservedCursor is not used.
	w := newRenameWorld(t)
	w.screens = []string{"$ "}
	w.cursorX, w.cursorY, w.cursorSet = 2, 46, true
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
	// IdlePlaceholderScreen has no recorded cursor, and the holding screen is
	// derived. Both are answered with ObservedCursor.
	w.derivedCursor = true
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
	// interrupt is a second check that fails while every cleanup condition
	// still holds and the input row still has the text this pod typed.
	// agentd then sends one Backspace per rune. A changed pane, a client,
	// a changed pid or a changed foreground sends no key.
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
		{"pane", false, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.panes = "0 0 %1 0 0 0 2028 claude" }
		}},
		{"client", false, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.clients = "/dev/pts/4" }
		}},
		{"pid", false, func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.panes = "0 0 %0 0 0 0 9999 claude" }
		}},
		{"input", false, func(w *renameWorld) {
			w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.IdleEmptyScreen}
		}},
		{"prompt", false, func(w *renameWorld) {
			// question-open has no recorded cursor. Column 2 on row 46 is the
			// empty-input cursor. The screen has no input box, so no key is sent.
			w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.QuestionOpenScreen}
			w.cursorX, w.cursorY, w.cursorSet = 2, 46, true
		}},
		{"foreground", false, func(w *renameWorld) {
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
	// The holding screen is derived. Its cursor is ObservedCursor. The idle
	// frames are recorded and use RecordedCursor.
	w.derivedCursor = true
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
	// The holding screen is derived. Its cursor is ObservedCursor, at the
	// end of a row that is not the typed text.
	w.derivedCursor = true
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding(typed + "x")}
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
		t.Fatalf("disk %+v", disk)
	}
	if disk.PaneID != "%0" || disk.CLIPid != w.pid || disk.ProcStart != w.start {
		t.Fatalf("mark did not record the process %+v", disk)
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
	if disk.PaneID != "%0" || disk.CLIPid != w.pid || disk.ProcStart != w.start {
		t.Fatalf("mark did not record the process %+v", disk)
	}
	for _, args := range w.writes() {
		if slices.Contains(args, "Enter") {
			t.Fatalf("Enter after a failed text send: %q", w.writes())
		}
	}
}

func TestRenameEnterErrorDoesNotRetry(t *testing.T) {
	target := sessiontitle.Compose("claude-haiku-4-5", "Café")
	text := []string{"send-keys", "-t", "%0", "-l", "--", "/rename " + target}
	backs := make([]string, utf8.RuneCountInString("/rename "+target))
	for i := range backs {
		backs[i] = "Backspace"
	}
	t.Run("cleanup", func(t *testing.T) {
		w := newRenameWorld(t)
		w.arm(target, false)
		w.enterErr = true
		disk := w.apply("Café")
		if disk.State != "Held" || disk.Reason != "interrupted" || disk.Stopped || disk.Attempts != 0 {
			t.Fatalf("disk %+v", disk)
		}
		want := [][]string{text, {"send-keys", "-t", "%0", "Enter"}, append([]string{"send-keys", "-t", "%0"}, backs...)}
		if !reflect.DeepEqual(w.writes(), want) {
			t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
		}
	})
	t.Run("record already renamed", func(t *testing.T) {
		w := newRenameWorld(t)
		w.arm(target, true)
		w.enterErr = true
		disk := w.apply("Café")
		if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target {
			t.Fatalf("disk %+v", disk)
		}
		want := [][]string{text, {"send-keys", "-t", "%0", "Enter"}}
		if !reflect.DeepEqual(w.writes(), want) {
			t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
		}
	})
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
	// A clear of a title that was held and never typed sets the session name
	// as applied and does not type it, including while the record is busy.
	disk := w.apply("")
	if disk.Applied != w.sess.Name || disk.State != "Applied" || disk.Reason != "already" || disk.Attempts != 0 || disk.Target != w.sess.Name || !disk.Since.After(first.Since) {
		t.Fatalf("disk %+v first %v", disk, first.Since)
	}
	w.assertNoWrite(t)
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
	mark := len(w.run.snapshot())
	disk := w.apply("Café")
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != target {
		t.Fatalf("disk %+v", disk)
	}
	w.assertNoWriteSince(t, mark)
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
	// The row is already empty, so this pass clears the mark and types nothing.
	// The record already holds the name. The pass after that records Applied.
	w.screens = []string{sessiontitle.IdleEmptyScreen}
	w.spec.name = target
	w.spec.source = "user"
	w.spec.updated = 0
	w.writeRecord()
	mark := len(w.run.snapshot())
	cleared := w.apply("Café")
	if cleared.State != "" || cleared.Reason != "" || cleared.Stopped {
		t.Fatalf("cleared %+v", cleared)
	}
	w.assertNoWriteSince(t, mark)
	got := w.apply("Café")
	if got.State != "Applied" || got.Reason != "confirmed" || got.Applied != target {
		t.Fatalf("later %+v", got)
	}
	w.assertNoWriteSince(t, mark)
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

func TestRenameLaunchTitleOutsideTheCharacterSetStaysApplied(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	title := "Cost \u2014 review"
	launched := sessiontitle.Compose(w.sess.Model, title)
	var l Launch
	if err := readJSONFile(w.s.statePath(launchFile), &l); err != nil {
		t.Fatal(err)
	}
	l.Argv = []string{"claude", "--remote-control", launched}
	writeJSON(t, w.s.statePath(launchFile), l)
	disk := w.apply(title)
	if disk.State != "Applied" || disk.Reason != "launch" || disk.Applied != launched || disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
	again := w.apply(title)
	if again.State != "Applied" || again.Reason != "launch" || again.Applied != launched || again.Stopped {
		t.Fatalf("second %+v", again)
	}
	w.assertNoWrite(t)

	// A previous pass recorded the refusal. The next heartbeat reports the
	// launch again, because the provider already shows that title.
	w.saveDisk(titleDisk{Applied: launched, State: "Failed", Reason: "invalid-title", Stopped: true})
	restored := w.apply(title)
	if restored.State != "Applied" || restored.Reason != "launch" || restored.Applied != launched || restored.Stopped {
		t.Fatalf("restored %+v", restored)
	}
	w.assertNoWrite(t)
}

func TestRenameResumeKeepsATitleTheRecordAlreadyShows(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(false)
	title := "Cost \u2014 review"
	shown := sessiontitle.Compose(w.sess.Model, title)
	// A launch title is not in the record. nameSource "user" is a title
	// someone typed by hand.
	w.spec.name = shown
	w.spec.source = "user"
	w.writeRecord()
	disk := w.apply(title)
	if disk.State != "Applied" || disk.Reason != "already" || disk.Applied != shown || disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)

	// title.json from the previous boot already records the composed text.
	// The record's name is something else. The state stays Applied.
	w.saveDisk(titleDisk{Applied: shown, Target: shown, State: "Applied", Reason: "launch"})
	w.spec.name = "derived-name"
	w.writeRecord()
	kept := w.apply(title)
	if kept.State != "Applied" || kept.Reason != "launch" || kept.Applied != shown || kept.Stopped {
		t.Fatalf("kept %+v", kept)
	}
	w.assertNoWrite(t)
}

func TestRenameClearAfterATypedTitleTypesTheSessionName(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.saveDisk(titleDisk{
		Applied: "", Target: target, Attempts: 1,
		State: "Typed", Since: w.clock.Now(),
	})
	w.arm(w.sess.Name, true)
	disk := w.apply("")
	w.assertTyped(t, w.sess.Name)
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != w.sess.Name {
		t.Fatalf("%+v", disk)
	}
}

func TestRenameInvalidAnswerOnFirstLaunchStaysFailed(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	disk := w.apply("测")
	if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
		t.Fatalf("%+v", disk)
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
	if disk.Applied != target {
		t.Fatalf("disk %+v", disk)
	}
	w.assertNoWrite(t)
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
		// tmux trims a spaces-only row, so the recorded idle screen is used
		// and the cursor is not at column 2. ObservedCursor is not used.
		w.screens = []string{sessiontitle.IdleEmptyScreen}
		w.cursorX, w.cursorY, w.cursorSet = 5, 46, true
		w.hold(t, "Café", "screen-unknown")
	})
	t.Run("multiline", func(t *testing.T) {
		w := newRenameWorld(t)
		// Derived from the recorded idle fixture by a blank row inside the box.
		// That screen has no single input box, so ObservedCursor is not known.
		// Column 2 on row 46 is supplied: a parser that called it empty would type.
		base := sessiontitle.IdleEmptyScreen
		const mark = "❯\u00a0"
		i := strings.LastIndex(base, mark)
		nl := strings.IndexByte(base[i:], '\n')
		at := i + nl + 1
		w.screens = []string{base[:at] + "\n" + base[at:]}
		w.cursorX, w.cursorY, w.cursorSet = 2, 46, true
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
	var format string
	for _, line := range strings.Split(sessiontitle.CursorFactsText(), "\n") {
		if after, ok := strings.CutPrefix(line, "list-panes format: "); ok {
			format = after
		}
	}
	if format == "" || format != sessiontitle.ListPanesFormat() || strings.Contains(format, "window_synchronized") || !strings.Contains(format, "#{pane_synchronized}") {
		t.Fatalf("format %q code %q", format, sessiontitle.ListPanesFormat())
	}
	w := newRenameWorld(t)
	if len(strings.Fields(w.panes)) != 8 {
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
	saw = false
	for _, c := range w.run.snapshot() {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "display-message" {
			saw = true
			if !slices.Contains(c.Args, sessiontitle.DisplayMessageFormat()) {
				t.Fatalf("argv %q", c.Args)
			}
		}
	}
	if !saw {
		t.Fatal("no display-message")
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

// paceRunner sleeps on the first calls so the five-second budget is partly
// gone. blockSecond makes the second list-panes wait until its own context
// ends, which is the second check.
type paceRunner struct {
	inner       Runner
	sleep       time.Duration
	n           int
	calls       int
	panes       int
	blockSecond bool
	waited      bool
}

func (p *paceRunner) LookPath(name string) (string, error) { return p.inner.LookPath(name) }

func (p *paceRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	p.calls++
	if p.calls <= p.n && p.sleep > 0 {
		time.Sleep(p.sleep)
	}
	if len(c.Args) > 0 && c.Args[0] == "list-panes" {
		p.panes++
		if p.panes == 2 && p.blockSecond {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}
	}
	if p.blockSecond && p.panes >= 2 && !p.waited {
		p.waited = true
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Result{}, ctx.Err()
		case <-timer.C:
		}
	}
	return p.inner.Run(ctx, c)
}

func TestRenameShortBudgetTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	p := &paceRunner{inner: w.run, sleep: 850 * time.Millisecond, n: 4}
	w.app.Apply(context.Background(), p, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{Present: true, Text: "Café"})
	disk := loadTitle(w.s, w.sess)
	if disk.State != "Held" || disk.Reason != "screen-unknown" || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameSecondCheckOutlivesTheBudget(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	w.arm("Café", false)
	p := &paceRunner{inner: w.run, sleep: 650 * time.Millisecond, n: 4, blockSecond: true}
	w.app.Apply(context.Background(), p, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{Present: true, Text: "Café"})
	disk := loadTitle(w.s, w.sess)
	if disk.State != "Held" || disk.Reason != "interrupted" || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	sawBackspace := false
	for _, args := range w.writes() {
		if slices.Contains(args, "Backspace") {
			sawBackspace = true
		}
	}
	if !sawBackspace {
		t.Fatalf("writes %q", w.writes())
	}
}

func TestRenameConfirmStopsWhenTheContextEnds(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	n := 0
	w.clock.onSleep = func() { n++ }
	disk := titleDisk{UID: w.sess.SessionUID, Target: target, State: "Typed", Attempts: 1, Since: w.clock.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.app.confirm(ctx, w.s, &disk, target)
	if n != 0 || disk.State != "Typed" {
		t.Fatalf("sleeps %d disk %+v", n, disk)
	}
}

func TestRenameBusyStreamingHoldsOnTheRecord(t *testing.T) {
	w := newRenameWorld(t)
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	// The streaming fixture is a recorded empty input (cursor 2 46). The hold
	// is the record's busy status, and nothing is typed.
	w.screens = []string{sessiontitle.BusyStreamingScreen}
	w.hold(t, "Café", "busy")
}

func TestRenameBusySpinnerScreensHoldBeforeTyping(t *testing.T) {
	for name, screen := range sessiontitle.BusySpinnerScreens() {
		t.Run(name, func(t *testing.T) {
			w := newRenameWorld(t)
			// Each spinner frame is recorded. Its cursor is RecordedCursor,
			// column 2 on row 46. The spinner row makes the screen unknown
			// before typing.
			w.screens = []string{screen}
			w.hold(t, "Café", "screen-unknown")
		})
	}
}

func TestRenameBusyThinkingTypedCleansUp(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	// BusyThinkingTypedScreen is the recorded "/rename zz" row. Its cursor is
	// RecordedCursor, column 12 on row 46. ObservedCursor is not used.
	w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.BusyThinkingTypedScreen}
	w.afterText = func() {
		w.spec.status = "busy"
		w.spec.updated = 0
		w.writeRecord()
	}
	disk := w.apply("zz")
	if disk.State != "Held" || disk.Reason != "interrupted" || disk.Stopped || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	backs := make([]string, utf8.RuneCountInString("/rename zz"))
	for i := range backs {
		backs[i] = "Backspace"
	}
	want := [][]string{
		{"send-keys", "-t", "%0", "-l", "--", "/rename zz"},
		append([]string{"send-keys", "-t", "%0"}, backs...),
	}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes\n got %q\nwant %q", w.writes(), want)
	}
}

func TestRenameQuestionOpenSendsNoKeyDuringCleanup(t *testing.T) {
	w := newRenameWorld(t)
	w.saveDisk(titleDisk{State: "Failed", Reason: "left-in-input", Target: "zz", Stopped: true})
	// question-open has no recorded cursor. Column 2 on row 46 is the
	// empty-input cursor. The screen has no input box, so no key is sent.
	w.screens = []string{sessiontitle.QuestionOpenScreen}
	w.cursorX, w.cursorY, w.cursorSet = 2, 46, true
	disk := w.apply("zz")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameCleanupSkipsAnAttachedClientAndAChangedPID(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*renameWorld)
	}{
		{"client", func(w *renameWorld) { w.clients = "/dev/pts/4" }},
		{"pid", func(w *renameWorld) { w.panes = "0 0 %0 0 0 0 9999 claude" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newRenameWorld(t)
			w.sess.Model = "custom-model"
			w.saveDisk(titleDisk{State: "Failed", Reason: "left-in-input", Target: "zz", Stopped: true})
			w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
			tc.edit(w)
			disk := w.apply("zz")
			if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped {
				t.Fatalf("%+v", disk)
			}
			w.assertNoWrite(t)
		})
	}
}

func TestRenameLeftInInputCleansThenRetries(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	w.saveDisk(w.leftDisk("zz"))
	w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
	disk := w.apply("zz")
	if disk.State != "Held" || disk.Reason != "interrupted" || disk.Interrupted != 1 || disk.Attempts != 0 {
		t.Fatalf("cleanup %+v", disk)
	}
	if len(w.writes()) != 1 || !slices.Contains(w.writes()[0], "Backspace") {
		t.Fatalf("writes %q", w.writes())
	}
	w.clock.Advance(renamePaceFor)
	w.spec.status = "idle"
	w.spec.updated = 0
	w.writeRecord()
	w.screens = []string{sessiontitle.IdleEmptyScreen, screenHolding("/rename zz")}
	w.derivedCursor = true
	w.confirmTo = "zz"
	w.afterText = nil
	later := w.apply("zz")
	if later.State != "Applied" || later.Reason != "confirmed" || later.Applied != "zz" {
		t.Fatalf("retry %+v", later)
	}
}

func TestRenameLeftInInputClearsWhenTheRowIsEmpty(t *testing.T) {
	w := newRenameWorld(t)
	w.saveDisk(titleDisk{State: "Failed", Reason: "left-in-input", Target: "zz", Stopped: true})
	w.screens = []string{sessiontitle.IdleEmptyScreen}
	disk := w.apply("Café")
	if disk.State != "" || disk.Reason != "" || disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameFlagAbsentSendsNoKeyWhileLeftInInput(t *testing.T) {
	w := newRenameWorld(t)
	// Every cleanup condition holds and the screen has the exact text. The
	// answer has no title object. A pass that ignored that and entered the
	// cleanup would send Backspace.
	w.saveDisk(w.leftDisk("zz"))
	w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
	before, err := os.ReadFile(w.s.statePath(titleStateFile))
	if err != nil {
		t.Fatal(err)
	}
	w.app.Apply(context.Background(), w.run, w.s, w.sess, slog.New(slog.DiscardHandler), HeartbeatTitle{})
	w.assertNoWrite(t)
	after, err := os.ReadFile(w.s.statePath(titleStateFile))
	if err != nil || string(after) != string(before) {
		t.Fatalf("state changed %s", after)
	}
}

func TestRenameInterruptCapResetsOnANewTarget(t *testing.T) {
	w := newRenameWorld(t)
	w.sess.Model = "custom-model"
	ready := func(screen string) {
		w.clock.Advance(renamePaceFor)
		w.spec.status = "idle"
		w.spec.updated = 0
		w.writeRecord()
		w.screens = []string{sessiontitle.IdleEmptyScreen, screen}
		w.afterText = func() {
			w.spec.status = "busy"
			w.spec.updated = 0
			w.writeRecord()
		}
	}
	for i := 1; i <= 3; i++ {
		ready(sessiontitle.BusyThinkingTypedScreen)
		disk := w.apply("zz")
		if disk.State != "Held" || disk.Reason != "interrupted" || disk.Interrupted != i || disk.Stopped {
			t.Fatalf("cycle %d %+v", i, disk)
		}
	}
	ready(sessiontitle.BusyThinkingTypedScreen)
	fourth := w.apply("zz")
	if fourth.State != "Failed" || fourth.Reason != "interrupted" || !fourth.Stopped || fourth.Interrupted != 4 {
		t.Fatalf("fourth %+v", fourth)
	}
	ready(screenHolding("/rename yy"))
	w.derivedCursor = true
	next := w.apply("yy")
	if next.State != "Held" || next.Reason != "interrupted" || next.Interrupted != 1 || next.Stopped {
		t.Fatalf("new target %+v", next)
	}
}

func TestRenameHoldAfterTheStagedWriteRefundsTheAttempt(t *testing.T) {
	w := newRenameWorld(t)
	w.onSecondClient = func() {
		w.spec.status = "busy"
		w.spec.updated = 0
		w.writeRecord()
	}
	disk := w.apply("Café")
	if disk.State != "Held" || disk.Reason != "busy" || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameTailRunsOutBetweenTextAndEnter(t *testing.T) {
	w := newRenameWorld(t)
	old := renameTailFor
	renameTailFor = 30 * time.Millisecond
	t.Cleanup(func() { renameTailFor = old })
	target := w.composed("Café")
	w.arm(target, true)
	w.afterText = func() { time.Sleep(80 * time.Millisecond) }
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
		t.Fatalf("%+v", disk)
	}
	want := [][]string{{"send-keys", "-t", "%0", "-l", "--", "/rename " + target}}
	if !reflect.DeepEqual(w.writes(), want) {
		t.Fatalf("writes %q", w.writes())
	}
}

func TestRenameWideAndCombiningTitlesTypeNothing(t *testing.T) {
	for _, title := range []string{"测", "e\u0301", "a\u200bb", "a\u00adb"} {
		t.Run(title, func(t *testing.T) {
			w := newRenameWorld(t)
			disk := w.apply(title)
			if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
				t.Fatalf("%+v", disk)
			}
			w.assertNoWrite(t)
		})
	}
}

func TestRenameUntitledFirstLaunchWritesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	var l Launch
	if err := readJSONFile(w.s.statePath(launchFile), &l); err != nil {
		t.Fatal(err)
	}
	l.Argv = []string{"claude", "--remote-control", w.sess.Name}
	writeJSON(t, w.s.statePath(launchFile), l)
	disk := w.apply("")
	if disk.found || disk.State != "" || disk.Applied != "" {
		t.Fatalf("%+v", disk)
	}
	if _, err := os.Stat(w.s.statePath(titleStateFile)); !os.IsNotExist(err) {
		t.Fatal("an untitled first launch wrote title.json")
	}
	w.assertNoWrite(t)
}

func TestRenameClearOfAnUntypedTitleAppliesTheSessionName(t *testing.T) {
	w := newRenameWorld(t)
	w.saveDisk(titleDisk{Applied: "", Attempts: 0})
	disk := w.apply("")
	if disk.Applied != w.sess.Name || disk.State != "Applied" || disk.Reason != "already" {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameVersionTimeoutIsNotCached(t *testing.T) {
	w := newRenameWorld(t)
	w.versionErr = true
	first := w.hold(t, "Café", "cli-version")
	if first.Reason != "cli-version" {
		t.Fatal(first.Reason)
	}
	w.versionErr = false
	target := w.composed("Café")
	w.arm(target, true)
	disk := w.apply("Café")
	w.assertTyped(t, target)
	if disk.State != "Applied" || disk.Reason != "confirmed" {
		t.Fatalf("%+v", disk)
	}
}

func TestRenameUnrecordedMinorIsCached(t *testing.T) {
	w := newRenameWorld(t)
	w.version = "2.2.0 (Claude Code)\n"
	w.hold(t, "Café", "cli-version")
	w.version = "2.1.321 (Claude Code)\n"
	w.hold(t, "Café", "cli-version")
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

func TestRenameCommandShadowFIFO(t *testing.T) {
	w := newRenameWorld(t)
	fifo := filepath.Join(w.s.Home, "wait.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(w.s.Home, "proj", ".claude", "commands")
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	done := make(chan bool, 1)
	go func() {
		done <- commandShadowed(ctx, filepath.Join(w.s.Home, "proj"), w.s.ClaudeConfigDir)
	}()
	select {
	case got := <-done:
		if !got {
			t.Fatal("a link to a FIFO did not hold")
		}
		if time.Since(start) > 500*time.Millisecond {
			t.Fatalf("the scan took %s", time.Since(start))
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a link to a FIFO blocked the scan")
	}
}

func TestRenameCommandShadowNestedAndFrontmatter(t *testing.T) {
	t.Run("nested", func(t *testing.T) {
		w := newRenameWorld(t)
		writeFile(t, filepath.Join(w.s.Home, "proj", ".claude", "commands", "group", "sub", "rename.md"), "name\n")
		w.hold(t, "Café", "command-shadowed")
	})
	t.Run("past depth", func(t *testing.T) {
		w := newRenameWorld(t)
		writeFile(t, filepath.Join(w.s.Home, "proj", ".claude", "commands", "a", "b", "c", "rename.md"), "name\n")
		target := w.composed("Café")
		w.arm(target, true)
		disk := w.apply("Café")
		if disk.Reason == "command-shadowed" {
			t.Fatalf("a file past depth 3 shadowed: %+v", disk)
		}
		if disk.State != "Applied" {
			t.Fatalf("%+v", disk)
		}
	})
	t.Run("frontmatter", func(t *testing.T) {
		w := newRenameWorld(t)
		writeFile(t, filepath.Join(w.s.Home, "proj", ".claude", "skills", "other", "SKILL.md"), "---\nname: rename\n---\nbody\n")
		w.hold(t, "Café", "command-shadowed")
	})
	t.Run("other name", func(t *testing.T) {
		w := newRenameWorld(t)
		writeFile(t, filepath.Join(w.s.Home, "proj", ".claude", "skills", "other", "SKILL.md"), "---\nname: other\n---\nbody\n")
		target := w.composed("Café")
		w.arm(target, true)
		disk := w.apply("Café")
		if disk.Reason == "command-shadowed" || disk.State != "Applied" {
			t.Fatalf("%+v", disk)
		}
	})
}

func TestRenameRefusedTextDoesNotWipeLeftInInput(t *testing.T) {
	refused := func(t *testing.T, applied string) *renameWorld {
		t.Helper()
		w := newRenameWorld(t)
		disk := titleDisk{State: "Failed", Reason: "left-in-input", Target: "zz", Stopped: true, Applied: applied}
		w.saveDisk(disk)
		w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
		return w
	}
	deliverRefuses := func(t *testing.T, w *renameWorld) {
		t.Helper()
		f, calls := deliverScreenRunner(t, sessiontitle.BusyThinkingTypedScreen, "12 46")
		err := Deliver(context.Background(), f, w.s, w.sess, "session/x", "hello")
		if err == nil || !strings.Contains(err.Error(), "not delivered: the session's input line holds unsent rename text") {
			t.Fatalf("%v", err)
		}
		joined := strings.Join(*calls, "\n")
		if strings.Contains(joined, "load-buffer") || strings.Contains(joined, "paste-buffer") || strings.Contains(joined, "Backspace") {
			t.Fatalf("sent keys:\n%s", joined)
		}
	}
	t.Run("refused", func(t *testing.T) {
		w := refused(t, "")
		before, err := os.ReadFile(w.s.statePath(titleStateFile))
		if err != nil {
			t.Fatal(err)
		}
		disk := w.apply("测")
		if disk.State != "Failed" || disk.Reason != "left-in-input" || disk.Target != "zz" || !disk.Stopped {
			t.Fatalf("%+v", disk)
		}
		after, err := os.ReadFile(w.s.statePath(titleStateFile))
		if err != nil || string(after) != string(before) {
			t.Fatalf("state rewritten %s", after)
		}
		w.assertNoWrite(t)
		deliverRefuses(t, w)
	})
	t.Run("shown", func(t *testing.T) {
		shown := sessiontitle.Compose(newRenameWorld(t).sess.Model, "测")
		w := refused(t, shown)
		disk := w.apply("测")
		if disk.State != "Failed" || disk.Reason != "left-in-input" || disk.Target != "zz" || disk.Applied != shown {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
		deliverRefuses(t, w)
	})
	t.Run("cleanup", func(t *testing.T) {
		w := newRenameWorld(t)
		w.saveDisk(w.leftDisk("zz"))
		w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
		disk := w.apply("测")
		if disk.State != "Held" || disk.Reason != "interrupted" || disk.Reason == "invalid-title" {
			t.Fatalf("%+v", disk)
		}
		if len(w.writes()) != 1 || !slices.Contains(w.writes()[0], "Backspace") {
			t.Fatalf("writes %q", w.writes())
		}
	})
}

func TestRenameClearAfterAHeldReplacementTypesTheSessionName(t *testing.T) {
	w := newRenameWorld(t)
	bee := w.composed("Bee")
	w.arm(bee, false)
	typed := w.apply("Bee")
	if typed.State != "Typed" || typed.Attempts != 1 || typed.Applied != "" || typed.LastTypedAt.IsZero() {
		t.Fatalf("typed %+v", typed)
	}
	w.spec.status = "busy"
	w.spec.updated = 0
	w.writeRecord()
	held := w.apply("Cee")
	if held.State != "Held" || held.Reason != "busy" || held.Attempts != 0 || held.Applied != "" || held.LastTypedAt.IsZero() {
		t.Fatalf("held %+v", held)
	}
	w.clock.Advance(renamePaceFor)
	w.spec.status = "idle"
	w.spec.age = 30 * time.Second
	w.spec.updated = 0
	w.writeRecord()
	w.arm(w.sess.Name, true)
	mark := len(w.run.snapshot())
	disk := w.apply("")
	want := [][]string{
		{"send-keys", "-t", "%0", "-l", "--", "/rename " + w.sess.Name},
		{"send-keys", "-t", "%0", "Enter"},
	}
	if !reflect.DeepEqual(w.writesSince(mark), want) {
		t.Fatalf("writes\n got %q\nwant %q", w.writesSince(mark), want)
	}
	if disk.State != "Applied" || disk.Reason != "confirmed" || disk.Applied != w.sess.Name {
		t.Fatalf("%+v", disk)
	}
}

func TestRenameSelectionPhraseTypesNothing(t *testing.T) {
	for _, title := range []string{"Enter to select", "Esc to Cancel", "eSC to INTERRUPT"} {
		t.Run(title, func(t *testing.T) {
			w := newRenameWorld(t)
			disk := w.apply(title)
			if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
				t.Fatalf("%+v", disk)
			}
			w.assertNoWrite(t)
		})
	}
	t.Run("session name", func(t *testing.T) {
		w := newRenameWorld(t)
		w.sess.Name = "Esc to cancel"
		w.saveDisk(titleDisk{Applied: "Haiku | Café", Target: "Haiku | Café", State: "Applied", Reason: "confirmed"})
		disk := w.apply("")
		if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
}

func TestRenamePaneWidthHoldsALongTitle(t *testing.T) {
	title := strings.Repeat("a", 48)
	t.Run("narrow", func(t *testing.T) {
		w := newRenameWorld(t)
		w.paneWidth = 60
		disk := w.hold(t, title, "pane")
		if disk.Attempts != 0 {
			t.Fatalf("attempt spent %+v", disk)
		}
	})
	t.Run("wide", func(t *testing.T) {
		w := newRenameWorld(t)
		w.paneWidth = 200
		target := w.composed(title)
		w.arm(target, true)
		disk := w.apply(title)
		w.assertTyped(t, target)
		if disk.State != "Applied" || disk.Reason != "confirmed" {
			t.Fatalf("%+v", disk)
		}
	})
}

func TestRenamePaneWidthBoundary(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	n := utf8.RuneCountInString("/rename " + target)
	t.Run("fits", func(t *testing.T) {
		w := newRenameWorld(t)
		w.paneWidth = n + 8
		w.arm(target, true)
		disk := w.apply("Café")
		w.assertTyped(t, target)
		if disk.State != "Applied" {
			t.Fatalf("%+v", disk)
		}
	})
	t.Run("one narrower", func(t *testing.T) {
		w := newRenameWorld(t)
		w.paneWidth = n + 7
		disk := w.hold(t, "Café", "pane")
		if disk.Attempts != 0 {
			t.Fatalf("attempt spent %+v", disk)
		}
	})
	t.Run("narrows after the write", func(t *testing.T) {
		w := newRenameWorld(t)
		w.narrowFrom = 2
		w.narrowWidth = 10
		disk := w.apply("Café")
		if disk.State != "Held" || disk.Reason != "pane" || disk.Attempts != 0 {
			t.Fatalf("%+v", disk)
		}
		w.assertNoWrite(t)
	})
}

func TestRenameCursorWidthMustBePositive(t *testing.T) {
	for _, raw := range []string{"2 46", "2 46 0 50", "2 46 -1 50"} {
		t.Run(raw, func(t *testing.T) {
			w := newRenameWorld(t)
			w.cursorRaw = raw
			w.hold(t, "Café", "screen-unknown")
		})
	}
}

func TestRenameLeftInInputBackspaceFollowsTheProcess(t *testing.T) {
	type edit func(*renameWorld, *titleDisk)
	cases := []struct {
		name  string
		minor string
		edit  edit
	}{
		{"pid", "", func(w *renameWorld, d *titleDisk) { d.CLIPid = w.pid + 1 }},
		{"procStart", "", func(_ *renameWorld, d *titleDisk) { d.ProcStart = "other-start" }},
		{"pane", "", func(_ *renameWorld, d *titleDisk) { d.PaneID = "%9" }},
		{"minor", "9.9.0 (Claude Code)\n", func(*renameWorld, *titleDisk) {}},
		{"old file", "", func(_ *renameWorld, d *titleDisk) {
			d.PaneID = ""
			d.CLIPid = 0
			d.ProcStart = ""
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRenameWorld(t)
			if tc.minor != "" {
				w.version = tc.minor
			}
			disk := w.leftDisk("zz")
			tc.edit(w, &disk)
			w.saveDisk(disk)
			w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
			got := w.apply("zz")
			if got.State != "Failed" || got.Reason != "left-in-input" || !got.Stopped || got.Target != "zz" {
				t.Fatalf("%+v", got)
			}
			w.assertNoWrite(t)
		})
		t.Run(tc.name+" empty", func(t *testing.T) {
			w := newRenameWorld(t)
			if tc.minor != "" {
				w.version = tc.minor
			}
			disk := w.leftDisk("zz")
			tc.edit(w, &disk)
			w.saveDisk(disk)
			w.screens = []string{sessiontitle.IdleEmptyScreen}
			got := w.apply("zz")
			if got.State != "" || got.Reason != "" || got.Stopped || got.PaneID != "" || got.CLIPid != 0 || got.ProcStart != "" {
				t.Fatalf("empty row %+v", got)
			}
			w.assertNoWrite(t)
		})
	}
	t.Run("match", func(t *testing.T) {
		w := newRenameWorld(t)
		w.saveDisk(w.leftDisk("zz"))
		w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
		got := w.apply("zz")
		if got.State != "Held" || got.Reason != "interrupted" || got.PaneID != "" || got.CLIPid != 0 || got.ProcStart != "" {
			t.Fatalf("%+v", got)
		}
		if len(w.writes()) != 1 || !slices.Contains(w.writes()[0], "Backspace") {
			t.Fatalf("writes %q", w.writes())
		}
	})
}

func TestRenameCommandShadowStopsAtTheCap(t *testing.T) {
	w := newRenameWorld(t)
	dir := filepath.Join(w.s.Home, "proj", ".claude", "commands")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const extra = 20
	files := shadowEntryCap + extra
	for i := 0; i < files; i++ {
		if err := os.WriteFile(filepath.Join(dir, "e"+strconv.Itoa(i)+".txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orig := readShadowDir
	got := 0
	readShadowDir = func(path string, limit int) ([]os.DirEntry, error) {
		if limit < 1 {
			t.Errorf("ReadDir limit %d", limit)
		}
		entries, err := orig(path, limit)
		got += len(entries)
		return entries, err
	}
	t.Cleanup(func() { readShadowDir = orig })
	w.hold(t, "Café", "command-shadowed")
	if got != shadowEntryCap+1 || got >= files {
		t.Fatalf("read %d entries from a directory of %d", got, files)
	}
}

func TestRenameResumeOutsideTheSetReportsInvalidTitle(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(false)
	title := "Cost \u2014 review"
	w.sess.Title = title
	w.spec.name = "derived-name"
	w.spec.source = "derived"
	w.writeRecord()
	disk := w.apply(title)
	if disk.State != "Failed" || disk.Reason != "invalid-title" || !disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameWaitingBlocksBackspaceAfterTheText(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	w.afterText = func() {
		w.spec.status = "waiting"
		w.writeRecord()
	}
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || disk.Attempts != 1 {
		t.Fatalf("%+v", disk)
	}
	for _, args := range w.writes() {
		if slices.Contains(args, "Backspace") {
			t.Fatalf("Backspace while waiting: %q", w.writes())
		}
	}
}

func TestRenameWaitingBlocksHealLeftBeforeTheScreen(t *testing.T) {
	w := newRenameWorld(t)
	w.saveDisk(w.leftDisk("zz"))
	w.spec.status = "waiting"
	w.spec.updated = 0
	w.writeRecord()
	w.screens = []string{sessiontitle.BusyThinkingTypedScreen}
	disk := w.apply("zz")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
	for _, c := range w.run.snapshot() {
		if c.Name == "tmux" && len(c.Args) > 0 && c.Args[0] == "capture-pane" {
			t.Fatal("healLeft read the screen while the record was waiting")
		}
	}
}

func TestRenameSecondCaptureBeforeTheTextHolds(t *testing.T) {
	w := newRenameWorld(t)
	w.derivedCursor = true
	w.secondScreen = screenHolding("hello")
	disk := w.apply("Café")
	if disk.State != "Held" || disk.Reason != "input" || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
}

func TestRenameBackspaceThatLeavesTextIsLeftInInput(t *testing.T) {
	w := newRenameWorld(t)
	target := w.composed("Café")
	w.arm(target, false)
	w.keepAfterBackspace = true
	w.afterText = func() {
		w.spec.status = "busy"
		w.writeRecord()
	}
	disk := w.apply("Café")
	if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
		t.Fatalf("%+v", disk)
	}
	saw := false
	for _, args := range w.writes() {
		if slices.Contains(args, "Backspace") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("no Backspace")
	}
}

func TestRenameClientAfterTheWriteRefunds(t *testing.T) {
	w := newRenameWorld(t)
	w.onSecondClient = func() { w.clients = "/dev/pts/4" }
	disk := w.apply("Café")
	if disk.State != "Held" || disk.Reason != "attached" || disk.Attempts != 0 {
		t.Fatalf("%+v", disk)
	}
	w.assertNoWrite(t)
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
