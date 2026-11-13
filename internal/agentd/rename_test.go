package agentd

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
}

type renameWorld struct {
	t       *testing.T
	s       Settings
	sess    protocol.Session
	run     *fakeRunner
	clock   *renameClock
	app     *TitleApplier
	pid     int
	start   string
	spec    recSpec
	panes   string
	clients string
	version string
	screens []string
	capN    int
	paneN   int

	versionErr bool
	clientErr  bool
	captureErr bool
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
		panes:   "0 0 %0 0 0 0 2028 claude",
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
	l := Launch{
		Session: w.sess.Name, SessionUID: w.sess.SessionUID, BootID: id,
		TUI: true, Resume: !first, ConversationID: "conv-1",
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
		return Result{Stdout: []byte(screen)}, nil
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
	if got := w.writes(); len(got) != 0 {
		t.Fatalf("tmux write %q", got)
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
	w.spec.status = "shell"
	w.spec.updated = 0
	w.writeRecord()
	w.hold(t, "Café", "status-unknown")
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
	w := newRenameWorld(t)
	w.screens = []string{"Allow this tool?\n1. Yes\n2. No\n"}
	w.hold(t, "Café", "screen-unknown")
}

func TestRenameGuardScreenUnknownPermission(t *testing.T) {
	w := newRenameWorld(t)
	w.screens = []string{"Permission required to run a command\n"}
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
	if calls := w.run.snapshot(); len(calls) != 0 {
		t.Fatalf("a held delivery read the session: %q", w.run.lines())
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
	cases := []struct {
		name string
		edit func(*renameWorld)
	}{
		{"status", func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() {
				w.spec.status = "busy"
				w.writeRecord()
			}
		}},
		{"updatedAt", func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() {
				w.spec.updated = w.clock.Now().UnixMilli()
				w.writeRecord()
			}
		}},
		{"pane", func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.panes = "0 0 %1 0 0 0 2028 claude" }
		}},
		{"client", func(w *renameWorld) {
			w.arm(target, false)
			w.afterText = func() { w.clients = "/dev/pts/4" }
		}},
		{"input", func(w *renameWorld) {
			w.screens = []string{sessiontitle.IdleEmptyScreen, sessiontitle.IdleEmptyScreen}
		}},
		{"prompt", func(w *renameWorld) {
			w.screens = []string{sessiontitle.IdleEmptyScreen, "Allow this tool?\n1. Yes\n"}
		}},
		{"slow", func(w *renameWorld) {
			w.arm(target, false)
			w.onRecheck = func() { w.clock.Advance(time.Second) }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRenameWorld(t)
			tc.edit(w)
			disk := w.apply("Café")
			if disk.State != "Failed" || disk.Reason != "left-in-input" || !disk.Stopped || disk.Attempts != 1 {
				t.Fatalf("disk %+v", disk)
			}
			got := w.writes()
			want := [][]string{{"send-keys", "-t", "%0", "-l", "--", "/rename " + target}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("writes %q", got)
			}
		})
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

func TestRenameFirstLaunchTypesNothing(t *testing.T) {
	w := newRenameWorld(t)
	w.boot(true)
	disk := w.apply("Other")
	if disk.State != "Applied" || disk.Reason != "launch" || disk.Applied != "Haiku | Café" {
		t.Fatalf("disk %+v", disk)
	}
	w.assertNoWrite(t)
	again := w.apply("Café")
	if again.Reason != "launch" || len(w.writes()) != 0 {
		t.Fatalf("second boot heartbeat %+v writes %q", again, w.writes())
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
	if disk.State != "Applied" || disk.Reason != "already" || disk.Applied != w.sess.Name {
		t.Fatalf("disk %+v", disk)
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
