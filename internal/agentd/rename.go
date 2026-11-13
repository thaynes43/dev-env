package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/sessiontitle"
)

// Title timing from DESIGN-065 rules 8.5 to 8.8.
const (
	renameQuietFor   = 30 * time.Second
	renamePaceFor    = 10 * time.Minute
	renameMaxTries   = 2
	renameSettle     = 300 * time.Millisecond
	renameEnterFor   = time.Second
	renameConfirmFor = 10 * time.Second
	renameConfirmGap = 500 * time.Millisecond
	renameTypedFor   = 2 * time.Minute
	titleStateFile   = "title.json"
)

// HeartbeatTitle is the title object in a heartbeat answer. Present is false
// for a 204, an empty body, or a body with no title object: agentd then types
// nothing and keeps the state it had.
type HeartbeatTitle struct {
	Present bool
	Text    string
}

// TitleClock is the clock a typed rename sleeps on. Tests advance it; a nil
// clock is the wall clock.
type TitleClock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// TitleApplier types one session's recorded title. One value per process so
// claude --version runs once per boot.
type TitleApplier struct {
	Clock    TitleClock
	versions cliVersionCache
}

type cliVersionCache struct {
	mu    sync.Mutex
	minor string
	ok    bool
	done  bool
}

type titleDisk struct {
	UID           string    `json:"uid"`
	Applied       string    `json:"applied"`
	LastTypedAt   time.Time `json:"lastTypedAt,omitempty"`
	Target        string    `json:"target,omitempty"`
	Attempts      int       `json:"attempts"`
	State         string    `json:"state,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Since         time.Time `json:"since,omitempty"`
	Stopped       bool      `json:"stopped,omitempty"`
	ProviderTitle string    `json:"providerTitle,omitempty"`
	found         bool      `json:"-"`
}

func (d *titleDisk) set(state, reason, target string, now time.Time) {
	keep := state == "Held" && d.State == "Held" && d.Target == target && !d.Since.IsZero()
	d.State = state
	d.Reason = reason
	d.Target = target
	if state == "Typed" {
		d.Reason = ""
	}
	if !keep {
		d.Since = now.UTC()
	}
}

// Apply types the recorded title when the guard allows it (DESIGN-065 rules
// 8.3 to 8.8). It types nothing when the answer has no title object.
func (a *TitleApplier) Apply(ctx context.Context, r Runner, s Settings, sess protocol.Session, log *slog.Logger, ans HeartbeatTitle) {
	if a == nil || !ans.Present {
		return
	}
	log = titleLogger(log)
	if !titleKind(ctx, r, s, sess, a.now()) {
		return
	}
	disk := loadTitle(s, sess)
	prev := disk
	if value, ok := firstLaunchValue(s, sess); ok {
		disk.UID = sess.SessionUID
		disk.Applied = value
		disk.Stopped = false
		disk.set("Applied", "launch", value, a.now())
		disk.save(s, log, prev)
		return
	}
	target, ok := renameTarget(sess, ans.Text)
	if !ok {
		disk.UID = sess.SessionUID
		disk.Stopped = true
		disk.set("Failed", "invalid-title", "", a.now())
		disk.save(s, log, prev)
		return
	}
	if !disk.found && ans.Text == "" {
		disk.UID = sess.SessionUID
		disk.Applied = sess.Name
		disk.Stopped = false
		disk.set("Applied", "already", sess.Name, a.now())
		disk.save(s, log, prev)
		return
	}
	disk.UID = sess.SessionUID
	if disk.Target != target {
		disk.Attempts = 0
		disk.Stopped = false
		disk.Since = time.Time{}
		disk.State = ""
		disk.Reason = ""
		disk.Target = target
	}
	if !a.willType(disk, target) {
		a.reconcile(&disk, s, target, prev)
		disk.save(s, log, prev)
		return
	}
	unlock, locked := tryDeliverLock(s)
	if !locked {
		disk.set("Held", "delivering", target, a.now())
		disk.save(s, log, prev)
		return
	}
	held := true
	defer func() {
		if held {
			unlock()
		}
	}()
	if a.guardAndType(ctx, r, s, sess, log, &disk, target) {
		held = false
		unlock()
		a.confirm(ctx, s, &disk, target)
	}
	disk.save(s, log, prev)
}

// willType is true only on the path that may send keys. The delivery lock is
// taken before any later read on that path.
func (a *TitleApplier) willType(disk titleDisk, target string) bool {
	if disk.Stopped || disk.Applied == target {
		return false
	}
	if disk.State == "Typed" && disk.Target == target {
		if a.now().Sub(disk.Since) < renameTypedFor || disk.Attempts >= renameMaxTries {
			return false
		}
	}
	return true
}

func (a *TitleApplier) reconcile(disk *titleDisk, s Settings, target string, prev titleDisk) {
	rec, fail := readTitleRecord(s, liveCLI(s))
	if fail == "" {
		disk.ProviderTitle = providerTitle(rec)
		if rec.name == target {
			reason := "already"
			if disk.Attempts > 0 || prev.State == "Typed" || prev.State == "Failed" {
				reason = "confirmed"
			}
			disk.Applied = target
			disk.Stopped = false
			disk.set("Applied", reason, target, a.now())
			return
		}
	}
	if disk.State == "Typed" && disk.Target == target && a.now().Sub(disk.Since) >= renameTypedFor && disk.Attempts >= renameMaxTries {
		disk.Stopped = true
		disk.set("Failed", "not-confirmed", target, a.now())
		return
	}
	if disk.Applied == target && disk.State == "" {
		disk.set("Applied", "already", target, a.now())
	}
}

// guardAndType runs conditions 2 to 10 and the keystrokes. It returns true
// when Enter was sent and the lock may be released for the confirmation poll.
func (a *TitleApplier) guardAndType(ctx context.Context, r Runner, s Settings, sess protocol.Session, log *slog.Logger, disk *titleDisk, target string) bool {
	now := a.now()
	p := liveCLI(s)
	if !cliOfThisBoot(s, sess) || !pidAlive(p) {
		disk.set("Held", "process", target, now)
		return false
	}
	rec, fail := readTitleRecord(s, p)
	if fail == "process" {
		disk.set("Held", "process", target, now)
		return false
	}
	if fail == "record-shape" {
		disk.set("Unknown", "record-shape", target, now)
		return false
	}
	disk.ProviderTitle = providerTitle(rec)
	if rec.name == target {
		reason := "already"
		if disk.Attempts > 0 {
			reason = "confirmed"
		}
		disk.Applied = target
		disk.Stopped = false
		disk.set("Applied", reason, target, now)
		return false
	}
	if !bridgeSessionID.MatchString(rec.bridge) {
		disk.set("Held", "not-registered", target, now)
		return false
	}
	if reason := idleReason(rec, now); reason != "" {
		disk.set("Held", reason, target, now)
		return false
	}
	n, err := strictClientCount(ctx, r, s)
	if err != nil || n != 0 {
		disk.set("Held", "attached", target, now)
		return false
	}
	pane, err := readAgentPane(ctx, r, s)
	if err != nil || !paneForeground(p.Pid, pane) {
		disk.set("Held", "pane", target, now)
		return false
	}
	if _, ok := a.cliMinor(ctx, r, s); !ok {
		disk.set("Held", "cli-version", target, now)
		return false
	}
	screen, err := capturePane(ctx, r, s, pane.id)
	if err != nil {
		disk.set("Held", "screen-unknown", target, now)
		return false
	}
	switch sessiontitle.ClassifyInput(screen, "") {
	case sessiontitle.InputEmpty:
	case sessiontitle.InputOther:
		disk.set("Held", "input", target, now)
		return false
	default:
		disk.set("Held", "screen-unknown", target, now)
		return false
	}
	if (!disk.LastTypedAt.IsZero() && now.Sub(disk.LastTypedAt) < renamePaceFor) || disk.Attempts >= renameMaxTries {
		disk.set("Held", "paced", target, now)
		return false
	}
	typed := "/rename " + target
	log.Info("session title keystroke", "kind", "text", "titleHash", sessiontitle.HashPrefix(target))
	if _, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", pane.id, "-l", "--", typed}}); err != nil {
		disk.Attempts++
		disk.LastTypedAt = a.now()
		disk.Stopped = true
		disk.set("Failed", "left-in-input", target, a.now())
		return false
	}
	disk.Attempts++
	disk.LastTypedAt = a.now()
	a.sleep(renameSettle)
	began := a.now()
	if !secondCheck(ctx, r, s, p, pane, rec, typed) || a.now().Sub(began) >= renameEnterFor {
		disk.Stopped = true
		disk.set("Failed", "left-in-input", target, a.now())
		return false
	}
	log.Info("session title keystroke", "kind", "enter", "titleHash", sessiontitle.HashPrefix(target))
	_, _ = r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", pane.id, "Enter"}})
	disk.set("Typed", "", target, a.now())
	return true
}

func (a *TitleApplier) confirm(ctx context.Context, s Settings, disk *titleDisk, target string) {
	p := liveCLI(s)
	deadline := a.now().Add(renameConfirmFor)
	for {
		rec, fail := readTitleRecord(s, p)
		if fail == "" && rec.name == target {
			disk.ProviderTitle = providerTitle(rec)
			disk.Applied = target
			disk.Stopped = false
			disk.set("Applied", "confirmed", target, a.now())
			return
		}
		if fail == "record-shape" {
			disk.set("Unknown", "record-shape", target, a.now())
			return
		}
		if ctx.Err() != nil || !a.now().Before(deadline) {
			break
		}
		a.sleep(renameConfirmGap)
	}
	disk.set("Typed", "", target, a.now())
}

func (a *TitleApplier) now() time.Time {
	if a == nil || a.Clock == nil {
		return time.Now()
	}
	return a.Clock.Now()
}

func (a *TitleApplier) sleep(d time.Duration) {
	if a == nil || a.Clock == nil {
		time.Sleep(d)
		return
	}
	a.Clock.Sleep(d)
}

func (a *TitleApplier) cliMinor(ctx context.Context, r Runner, s Settings) (string, bool) {
	a.versions.mu.Lock()
	defer a.versions.mu.Unlock()
	if !a.versions.done {
		a.versions.done = true
		res, err := r.Run(ctx, Cmd{Name: s.ClaudeBin, Args: []string{"--version"}})
		if err == nil {
			minor, ok := sessiontitle.CLIMinor(string(res.Stdout))
			a.versions.minor = minor
			a.versions.ok = ok && sessiontitle.MinorRecorded(minor)
		}
	}
	return a.versions.minor, a.versions.ok
}

func titleLogger(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return log
}

func titleKind(ctx context.Context, r Runner, s Settings, sess protocol.Session, now time.Time) bool {
	if sess.Agent != protocol.AgentClaude || sess.Mode != protocol.ModeRemote || sess.Caller != "" {
		return false
	}
	_, _, explicit := explicitClaudeRCStatus(ctx, r, s, sess, now)
	return !explicit
}

func renameTarget(sess protocol.Session, text string) (string, bool) {
	if text == "" {
		return sess.Name, sess.Name != "" && sessiontitle.StartsWithLetterOrDigit(sess.Name)
	}
	if sessiontitle.Validate(text) != "" {
		return "", false
	}
	target := sessiontitle.Compose(sess.Model, text)
	return target, sessiontitle.StartsWithLetterOrDigit(target)
}

func firstLaunchValue(s Settings, sess protocol.Session) (string, bool) {
	l, ok := currentLaunch(s)
	if !ok || !l.TUI || l.Resume || l.Session != sess.Name || l.SessionUID != sess.SessionUID {
		return "", false
	}
	if v, ok := argvFlagValue(l.Argv, "--remote-control"); ok && v != "" {
		return v, true
	}
	return sess.Name, true
}

func cliOfThisBoot(s Settings, sess protocol.Session) bool {
	l, ok := currentLaunch(s)
	return ok && l.TUI && l.Session == sess.Name && l.SessionUID == sess.SessionUID
}

func liveCLI(s Settings) agentPid {
	var p agentPid
	_ = readJSONFile(s.statePath(pidFile), &p)
	return p
}

func loadTitle(s Settings, sess protocol.Session) titleDisk {
	var disk titleDisk
	if readJSONFile(s.statePath(titleStateFile), &disk) != nil || disk.UID != sess.SessionUID {
		return titleDisk{}
	}
	disk.found = true
	return disk
}

func (d *titleDisk) save(s Settings, log *slog.Logger, prev titleDisk) {
	if err := writeJSONFile(s.statePath(titleStateFile), d); err != nil {
		log.Warn("session title state", "err", err.Error())
		return
	}
	if d.State == prev.State && d.Reason == prev.Reason && d.Attempts == prev.Attempts && d.Target == prev.Target {
		return
	}
	log.Info("session title", "state", d.State, "reason", d.Reason, "attempts", d.Attempts, "titleHash", sessiontitle.HashPrefix(d.Target))
}

func tryDeliverLock(s Settings) (func(), bool) {
	path := s.statePath(deliverLockFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}

type titleRecord struct {
	bridge  string
	status  string
	updated int64
	name    string
	source  string
}

func readTitleRecord(s Settings, p agentPid) (titleRecord, string) {
	var rec titleRecord
	path := filepath.Join(s.ClaudeConfigDir, "sessions", strconv.Itoa(p.Pid)+".json")
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxSessionRecordLen {
		return rec, "process"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return rec, "process"
	}
	var raw struct {
		BridgeSessionID string          `json:"bridgeSessionId"`
		ProcStart       json.RawMessage `json:"procStart"`
		Status          json.RawMessage `json:"status"`
		UpdatedAt       json.RawMessage `json:"updatedAt"`
		Name            json.RawMessage `json:"name"`
		NameSource      json.RawMessage `json:"nameSource"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return rec, "process"
	}
	if p.Start == "" || strings.Trim(string(raw.ProcStart), `"`) != p.Start {
		return rec, "process"
	}
	var updated int64
	if !jsonString(raw.Status, &rec.status) || !jsonNumber(raw.UpdatedAt, &updated) || !jsonString(raw.Name, &rec.name) || !jsonString(raw.NameSource, &rec.source) {
		return titleRecord{}, "record-shape"
	}
	rec.bridge = raw.BridgeSessionID
	rec.updated = updated
	return rec, ""
}

func jsonString(raw json.RawMessage, dest *string) bool {
	if len(raw) == 0 || raw[0] != '"' {
		return false
	}
	return json.Unmarshal(raw, dest) == nil
}

func jsonNumber(raw json.RawMessage, dest *int64) bool {
	if len(raw) == 0 || raw[0] == '"' {
		return false
	}
	return json.Unmarshal(raw, dest) == nil
}

func providerTitle(rec titleRecord) string {
	if rec.source == "user" {
		return rec.name
	}
	return ""
}

func idleReason(rec titleRecord, now time.Time) string {
	switch rec.status {
	case "busy":
		return "busy"
	case "waiting":
		return "waiting"
	case "idle":
		at := time.UnixMilli(rec.updated)
		if at.After(now) {
			return "clock"
		}
		if now.Sub(at) < renameQuietFor {
			return "quiet"
		}
		return ""
	default:
		return "status-unknown"
	}
}

func strictClientCount(ctx context.Context, r Runner, s Settings) (int, error) {
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"list-clients", "-t", "=" + TmuxSession, "-F", "#{client_tty}"}})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

type paneSnap struct {
	id         string
	pid        int
	dead, mode string
	sync       string
}

func readAgentPane(ctx context.Context, r Runner, s Settings) (paneSnap, error) {
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{
		"list-panes", "-s", "-t", "=" + TmuxSession, "-F",
		"#{window_index} #{pane_index} #{pane_id} #{pane_dead} #{pane_in_mode} #{window_synchronized} #{pane_pid} #{pane_current_command}",
	}})
	if err != nil {
		return paneSnap{}, err
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(string(res.Stdout)), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return paneSnap{}, errors.New("pane")
	}
	fields := strings.SplitN(lines[0], " ", 8)
	if len(fields) < 7 || !strings.HasPrefix(fields[2], "%") {
		return paneSnap{}, errors.New("pane")
	}
	pid, err := strconv.Atoi(fields[6])
	if err != nil {
		return paneSnap{}, err
	}
	return paneSnap{id: fields[2], pid: pid, dead: fields[3], mode: fields[4], sync: fields[5]}, nil
}

type procIDs struct {
	ppid, pgrp, tpgid int
}

// readProcIDs reads the CLI's parent, process group and foreground group.
// Tests replace it; the test process is not a terminal's foreground group.
var readProcIDs = readProcStat

func readProcStat(pid int) (procIDs, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return procIDs{}, err
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return procIDs{}, errors.New("stat")
	}
	fields := strings.Fields(string(data)[i+1:])
	// fields[1] ppid, fields[2] pgrp, fields[5] tpgid.
	if len(fields) < 6 {
		return procIDs{}, errors.New("stat")
	}
	var ids procIDs
	ids.ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return procIDs{}, err
	}
	ids.pgrp, err = strconv.Atoi(fields[2])
	if err != nil {
		return procIDs{}, err
	}
	ids.tpgid, err = strconv.Atoi(fields[5])
	return ids, err
}

func paneForeground(cli int, pane paneSnap) bool {
	if pane.dead != "0" || pane.mode != "0" || pane.sync != "0" {
		return false
	}
	ids, err := readProcIDs(cli)
	if err != nil || ids.ppid != pane.pid || ids.tpgid <= 0 || ids.pgrp != ids.tpgid {
		return false
	}
	return true
}

func capturePane(ctx context.Context, r Runner, s Settings, paneID string) (string, error) {
	res, err := r.Run(ctx, Cmd{Name: s.TmuxBin, Args: []string{"capture-pane", "-p", "-e", "-t", paneID}})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

func secondCheck(ctx context.Context, r Runner, s Settings, p agentPid, pane paneSnap, rec titleRecord, typed string) bool {
	again, err := readAgentPane(ctx, r, s)
	if err != nil || again != pane {
		return false
	}
	fresh, fail := readTitleRecord(s, p)
	if fail != "" || fresh.status != "idle" || fresh.updated != rec.updated {
		return false
	}
	n, err := strictClientCount(ctx, r, s)
	if err != nil || n != 0 {
		return false
	}
	screen, err := capturePane(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	return sessiontitle.ClassifyInput(screen, typed) == sessiontitle.InputExact
}

// overlayRecordedTitle copies title.json onto a heartbeat when this session
// reports a rename. A task, a local session, Codex, a summoned session and an
// explicit continuation report nothing.
func overlayRecordedTitle(s Settings, sess protocol.Session, rc *protocol.RemoteControl) {
	if rc == nil || sess.Agent != protocol.AgentClaude || sess.Mode != protocol.ModeRemote || sess.Caller != "" {
		return
	}
	if _, err := os.Lstat(s.statePath(explicitClaudeRCFile)); err == nil || (err != nil && !errors.Is(err, os.ErrNotExist)) {
		return
	}
	disk := loadTitle(s, sess)
	if !disk.found || disk.State == "" {
		return
	}
	rc.ProviderTitle = disk.ProviderTitle
	rc.Rename = &protocol.RemoteRename{State: disk.State, Reason: disk.Reason, Since: disk.Since, Attempts: disk.Attempts}
}
