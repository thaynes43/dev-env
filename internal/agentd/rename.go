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
	"unicode/utf8"

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
	renameCallFor    = 2 * time.Second
	renameBudget     = 5 * time.Second
	titleStateFile   = "title.json"
	// renameInterruptCap is the most interrupted cycles one target may record.
	// The next one is Failed.
	renameInterruptCap = 3
)

// renameTailFor bounds the second check, the clear and Enter once the text
// has been sent. It comes from the caller's context, so the five-second
// budget ending does not skip the clear. Tests shorten it.
var renameTailFor = 4 * time.Second

// HeartbeatTitle is the title object in a heartbeat answer. Present is false
// for a 204, an empty body, or a body with no title object: agentd then types
// nothing and keeps the state it had. Unknown is a title object whose text is
// missing or not a string: nothing is typed and nothing is cleared.
type HeartbeatTitle struct {
	Present bool
	Unknown bool
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
	pace     paceFloor
}

// paceFloor keeps the last attempt in memory when title.json cannot be
// written, so a failed write does not let the next heartbeat type immediately.
type paceFloor struct {
	mu       sync.Mutex
	target   string
	at       time.Time
	attempts int
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
	Interrupted   int       `json:"interrupted,omitempty"`
	// PaneID, CLIPid and ProcStart are the pane and the CLI process that
	// received the unsent text. They are set with the left-in-input mark and
	// cleared when that mark clears. An older file has none of them.
	PaneID    string `json:"paneId,omitempty"`
	CLIPid    int    `json:"cliPid,omitempty"`
	ProcStart string `json:"procStart,omitempty"`
	found     bool   `json:"-"`
}

func (d *titleDisk) set(state, reason, target string, now time.Time) {
	if state == "Typed" {
		reason = ""
	}
	keep := d.State == state && d.Reason == reason && d.Target == target && !d.Since.IsZero()
	d.State = state
	d.Reason = reason
	d.Target = target
	if !keep {
		d.Since = now.UTC()
	}
	if state != "Failed" || reason != "left-in-input" {
		d.PaneID = ""
		d.CLIPid = 0
		d.ProcStart = ""
	}
}

// markLeft records that text was left in the input of this pane and CLI.
func (d *titleDisk) markLeft(target string, now time.Time, p agentPid, pane paneSnap) {
	d.Stopped = true
	d.set("Failed", "left-in-input", target, now)
	d.PaneID = pane.id
	d.CLIPid = p.Pid
	d.ProcStart = p.Start
}

// clearLeft drops the left-in-input mark and the process it was bound to.
func (d *titleDisk) clearLeft() {
	d.Stopped = false
	d.State = ""
	d.Reason = ""
	d.Since = time.Time{}
	d.PaneID = ""
	d.CLIPid = 0
	d.ProcStart = ""
}

// Apply types the recorded title when the guard allows it (DESIGN-065 rules
// 8.3 to 8.8). It types nothing when the answer has no title object.
func (a *TitleApplier) Apply(ctx context.Context, r Runner, s Settings, sess protocol.Session, log *slog.Logger, ans HeartbeatTitle) {
	if a == nil || !ans.Present || ans.Unknown {
		return
	}
	log = titleLogger(log)
	if !titleKind(ctx, r, s, sess, a.now()) {
		return
	}
	disk := loadTitle(s, sess)
	prev := disk
	// While the mark is set, this pass only removes the recorded text or
	// clears the mark when the row is already empty. It does not seed a
	// launch title and it does not type, whatever text the answer carries.
	if disk.State == "Failed" && disk.Reason == "left-in-input" && disk.Target != "" {
		a.healLeft(ctx, r, s, &disk)
		disk.save(s, log, prev)
		return
	}
	// A first launch records the title the CLI was given. Only that value is
	// left untyped. A different target later in this pod's life is applied.
	// An untitled session, whose launch value is the session name and whose
	// text is empty, is not seeded and reports no rename object.
	if value, ok := firstLaunchValue(s, sess); ok && disk.Applied == "" {
		if ans.Text != "" || value != sess.Name {
			disk.UID = sess.SessionUID
			disk.Applied = value
			disk.Stopped = false
			if disk.State == "" {
				disk.set("Applied", "launch", value, a.now())
			}
		}
	}
	target, ok := renameTarget(sess, ans.Text)
	if !ok {
		// The character rule refuses to type this text. When the provider
		// already shows it, the state stays Applied and nothing is typed.
		if a.keepShownTitle(&disk, s, sess, ans.Text) {
			disk.save(s, log, prev)
			return
		}
		disk.UID = sess.SessionUID
		disk.Stopped = true
		disk.set("Failed", "invalid-title", "", a.now())
		disk.save(s, log, prev)
		return
	}
	if !disk.found && ans.Text == "" && disk.Applied == "" {
		return
	}
	disk.UID = sess.SessionUID
	// The attempt count is the one from before a target change. The reset
	// below zeroes it, and a clear must not treat a typed title as never typed.
	prevAttempts := disk.Attempts
	prevState := disk.State
	if disk.Target != target {
		disk.Attempts = 0
		disk.Interrupted = 0
		disk.Stopped = false
		disk.Since = time.Time{}
		disk.State = ""
		disk.Reason = ""
		disk.Target = target
	}
	// A clear of a title that was never typed sets the session name as
	// applied and does not type it. lastTypedAt stays for the life of this
	// file, so a title typed under an earlier target is not "never typed".
	if disk.found && disk.Applied == "" && disk.LastTypedAt.IsZero() && prevAttempts == 0 && prevState != "Typed" && target == sess.Name {
		disk.Applied = target
		disk.Stopped = false
		disk.set("Applied", "already", target, a.now())
		disk.save(s, log, prev)
		return
	}
	a.applyFloor(&disk, target)
	if !a.willType(disk, target) {
		a.reconcile(&disk, s, target, prev)
		disk.save(s, log, prev)
		return
	}
	budget, stop := context.WithTimeout(ctx, renameBudget)
	defer stop()
	if _, ok := a.cliMinor(budget, r, s); !ok {
		disk.set("Held", "cli-version", target, a.now())
		disk.save(s, log, prev)
		return
	}
	unlock, locked := tryDeliverLock(s)
	if !locked {
		disk.set("Held", "delivering", target, a.now())
		disk.save(s, log, prev)
		return
	}
	released := false
	release := func() {
		if !released && unlock != nil {
			released = true
			unlock()
		}
	}
	defer release()
	if a.guardAndType(ctx, budget, r, s, sess, log, &disk, target) {
		release()
		a.confirm(budget, s, &disk, target)
	}
	disk.save(s, log, prev)
}

func (a *TitleApplier) applyFloor(disk *titleDisk, target string) {
	if a == nil {
		return
	}
	a.pace.mu.Lock()
	defer a.pace.mu.Unlock()
	if a.pace.target != target || a.pace.attempts == 0 {
		return
	}
	if a.pace.attempts > disk.Attempts {
		disk.Attempts = a.pace.attempts
	}
	if !a.pace.at.IsZero() && disk.LastTypedAt.Before(a.pace.at) {
		disk.LastTypedAt = a.pace.at
	}
}

func (a *TitleApplier) rememberPace(target string, attempts int, at time.Time) {
	if a == nil {
		return
	}
	a.pace.mu.Lock()
	a.pace.target = target
	a.pace.attempts = attempts
	a.pace.at = at
	a.pace.mu.Unlock()
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
// parent is the caller's context. ctx is the five-second budget.
func (a *TitleApplier) guardAndType(parent, ctx context.Context, r Runner, s Settings, sess protocol.Session, log *slog.Logger, disk *titleDisk, target string) bool {
	now := a.now()
	hold := func(state, reason string) bool {
		if state == "Failed" {
			disk.Stopped = true
		}
		disk.set(state, reason, target, a.now())
		return false
	}
	if ctx.Err() != nil {
		return hold("Held", "process")
	}
	p := liveCLI(s)
	if !cliOfThisBoot(s, sess) || !pidAlive(p) {
		return hold("Held", "process")
	}
	rec, fail := readTitleRecord(s, p)
	if fail == "process" {
		return hold("Held", "process")
	}
	if fail == "record-shape" {
		return hold("Unknown", "record-shape")
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
		return hold("Held", "not-registered")
	}
	if reason := idleReason(rec, now); reason != "" {
		return hold("Held", reason)
	}
	n, err := strictClientCount(ctx, r, s)
	if err != nil || n != 0 {
		return hold("Held", "attached")
	}
	pane, err := readAgentPane(ctx, r, s)
	if err != nil || !paneForeground(p.Pid, pane) {
		return hold("Held", "pane")
	}
	screen, err := capturePane(ctx, r, s, pane.id)
	if err != nil {
		return hold("Held", "screen-unknown")
	}
	cur, err := readCursor(ctx, r, s, pane.id)
	if err != nil {
		return hold("Held", "screen-unknown")
	}
	typed := "/rename " + target
	switch sessiontitle.ClassifyInput(screen, "", cur) {
	case sessiontitle.InputEmpty:
	case sessiontitle.InputOther:
		return hold("Held", "input")
	default:
		return hold("Held", "screen-unknown")
	}
	if !typedFits(typed, cur.Width) {
		return hold("Held", "pane")
	}
	if (!disk.LastTypedAt.IsZero() && now.Sub(disk.LastTypedAt) < renamePaceFor) || disk.Attempts >= renameMaxTries {
		return hold("Held", "paced")
	}
	project := ""
	if launch, ok := currentLaunch(s); ok {
		project = launch.Dir
	}
	if ctx.Err() != nil || commandShadowed(ctx, project, s.ClaudeConfigDir) {
		return hold("Held", "command-shadowed")
	}
	if strings.HasSuffix(typed, ";") {
		return hold("Failed", "invalid-title")
	}
	staged := *disk
	prevAttempts := disk.Attempts
	prevTypedAt := disk.LastTypedAt
	staged.Attempts++
	staged.LastTypedAt = a.now().UTC()
	a.rememberPace(target, staged.Attempts, staged.LastTypedAt)
	if err := writeTitleFile(s, &staged); err != nil {
		disk.Attempts = staged.Attempts
		disk.LastTypedAt = staged.LastTypedAt
		return hold("Held", "state")
	}
	disk.Attempts = staged.Attempts
	disk.LastTypedAt = staged.LastTypedAt
	// The early reads stay above, so a hold there costs no write. These
	// reads are after the write and directly before the last record read.
	// A hold here refunds the attempt, because the text has not been sent.
	refund := func(state, reason string) bool {
		disk.Attempts = prevAttempts
		disk.LastTypedAt = prevTypedAt
		a.rememberPace(target, prevAttempts, prevTypedAt)
		if err := writeTitleFile(s, disk); err != nil {
			return hold("Held", "state")
		}
		return hold(state, reason)
	}
	if n, err = strictClientCount(ctx, r, s); err != nil || n != 0 {
		return refund("Held", "attached")
	}
	screen, err = capturePane(ctx, r, s, pane.id)
	if err != nil {
		return refund("Held", "screen-unknown")
	}
	cur, err = readCursor(ctx, r, s, pane.id)
	if err != nil {
		return refund("Held", "screen-unknown")
	}
	switch sessiontitle.ClassifyInput(screen, "", cur) {
	case sessiontitle.InputEmpty:
	case sessiontitle.InputOther:
		return refund("Held", "input")
	default:
		return refund("Held", "screen-unknown")
	}
	if !typedFits(typed, cur.Width) {
		return refund("Held", "pane")
	}
	log.Info("session title keystroke", "kind", "text", "titleHash", sessiontitle.HashPrefix(target))
	fresh, fail := readTitleRecord(s, p)
	if fail == "process" {
		return refund("Held", "process")
	}
	if fail == "record-shape" {
		return refund("Unknown", "record-shape")
	}
	if fresh.status != rec.status || fresh.updated != rec.updated {
		if reason := idleReason(fresh, a.now()); reason != "" {
			return refund("Held", reason)
		}
		return refund("Held", "quiet")
	}
	// Do not start the text unless one full call still fits. A budget that
	// is already nearly gone holds, and the attempt recorded above is undone.
	if d, ok := ctx.Deadline(); ok && time.Until(d) < renameCallFor {
		return refund("Held", "screen-unknown")
	}
	tail, stopTail := context.WithTimeout(parent, renameTailFor)
	defer stopTail()
	if _, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", pane.id, "-l", "--", typed}}); err != nil {
		disk.markLeft(target, a.now(), p, pane)
		return false
	}
	a.sleep(renameSettle)
	began := a.now()
	if !secondCheck(tail, r, s, p, pane, rec, typed) || a.now().Sub(began) >= renameEnterFor {
		return a.afterKeys(tail, r, s, disk, p, pane, typed, target)
	}
	log.Info("session title keystroke", "kind", "enter", "titleHash", sessiontitle.HashPrefix(target))
	if _, err := renameRun(tail, r, Cmd{Name: s.TmuxBin, Args: []string{"send-keys", "-t", pane.id, "Enter"}}); err != nil {
		confirmed, fail := readTitleRecord(s, p)
		if fail == "" && confirmed.name == target {
			disk.ProviderTitle = providerTitle(confirmed)
			disk.Applied = target
			disk.Stopped = false
			disk.set("Applied", "confirmed", target, a.now())
			return false
		}
		return a.afterKeys(tail, r, s, disk, p, pane, typed, target)
	}
	disk.set("Typed", "", target, a.now())
	return true
}

// afterKeys removes the text this pod just typed when the second check did
// not pass, or when Enter failed and the record does not yet show the name.
// The row coming back empty refunds the attempt. Anything else leaves the
// line and stops.
func (a *TitleApplier) afterKeys(ctx context.Context, r Runner, s Settings, disk *titleDisk, p agentPid, pane paneSnap, typed, target string) bool {
	if clearTyped(ctx, r, s, p, pane, typed) {
		a.noteInterrupted(disk, target)
		if err := writeTitleFile(s, disk); err != nil {
			disk.set("Held", "state", target, a.now())
			return false
		}
		return false
	}
	disk.markLeft(target, a.now(), p, pane)
	return false
}

// typedFits reports whether the text about to be typed stays on one input
// row. The measured limit at width 200 is N <= width-4. Eight columns of
// margin are four more than that.
func typedFits(typed string, width int) bool {
	return utf8.RuneCountInString(typed)+8 <= width
}

// noteInterrupted records one cleanup of this target. The fourth is Failed.
// The attempt is refunded because the row came back empty.
func (a *TitleApplier) noteInterrupted(disk *titleDisk, target string) {
	if disk.Attempts > 0 {
		disk.Attempts--
	}
	a.rememberPace(target, disk.Attempts, disk.LastTypedAt)
	disk.Interrupted++
	if disk.Interrupted > renameInterruptCap {
		disk.Stopped = true
		disk.set("Failed", "interrupted", target, a.now())
		return
	}
	disk.Stopped = false
	disk.set("Held", "interrupted", target, a.now())
}

// healLeft tries to remove the recorded unsent text, or clears the mark when
// the input row is already empty. It sends no new title text.
func (a *TitleApplier) healLeft(ctx context.Context, r Runner, s Settings, disk *titleDisk) {
	typed := "/rename " + disk.Target
	budget, stop := context.WithTimeout(ctx, renameBudget)
	defer stop()
	unlock, locked := tryDeliverLock(s)
	if !locked {
		return
	}
	defer unlock()
	p := liveCLI(s)
	if !pidAlive(p) {
		return
	}
	pane, err := readAgentPane(budget, r, s)
	if err != nil || !paneForeground(p.Pid, pane) {
		return
	}
	rec, fail := readTitleRecord(s, p)
	if fail != "" || rec.status == "waiting" {
		return
	}
	n, err := strictClientCount(budget, r, s)
	if err != nil || n != 0 {
		return
	}
	screen, err := capturePane(budget, r, s, pane.id)
	if err != nil {
		return
	}
	cur, err := readCursor(budget, r, s, pane.id)
	if err != nil {
		return
	}
	switch sessiontitle.ClassifyBox(screen, typed, cur) {
	case sessiontitle.InputEmpty:
		disk.clearLeft()
	case sessiontitle.InputExact:
		// Backspace only for the pane and CLI that received the text, and
		// only on a recorded CLI minor. The empty-row clear above is not
		// bound to them. An older file has no fields, so it gets no key.
		if pane.id != disk.PaneID || p.Pid != disk.CLIPid || p.Start != disk.ProcStart || disk.PaneID == "" || disk.ProcStart == "" || disk.CLIPid == 0 {
			return
		}
		if _, ok := a.cliMinor(budget, r, s); !ok {
			return
		}
		if clearTyped(budget, r, s, p, pane, typed) {
			a.noteInterrupted(disk, disk.Target)
		}
	}
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
	if a.versions.done {
		return a.versions.minor, a.versions.ok
	}
	res, err := renameRun(ctx, r, Cmd{Name: s.ClaudeBin, Args: []string{"--version"}})
	if err != nil {
		return "", false
	}
	minor, ok := sessiontitle.CLIMinor(string(res.Stdout))
	if !ok {
		return "", false
	}
	a.versions.done = true
	a.versions.minor = minor
	a.versions.ok = sessiontitle.MinorRecorded(minor)
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

// keepShownTitle reports Applied when the provider already shows text, and
// types nothing. A first launch matches the --remote-control value. A resume
// matches title.json's applied value, or the CLI record's name. text is the
// role title. The compared string is the composed display title.
func (a *TitleApplier) keepShownTitle(disk *titleDisk, s Settings, sess protocol.Session, text string) bool {
	if text == "" {
		return false
	}
	shown := sessiontitle.Compose(sess.Model, text)
	if shown == "" {
		return false
	}
	if value, launched := firstLaunchValue(s, sess); launched && value == shown && (disk.Applied == "" || disk.Applied == value) {
		disk.UID = sess.SessionUID
		disk.Applied = value
		disk.Stopped = false
		disk.set("Applied", "launch", value, a.now())
		return true
	}
	if disk.found && disk.Applied == shown {
		disk.UID = sess.SessionUID
		disk.Stopped = false
		if disk.State != "Applied" {
			disk.set("Applied", "already", shown, a.now())
		}
		return true
	}
	rec, fail := readTitleRecord(s, liveCLI(s))
	if fail == "" && rec.name == shown && disk.Applied == "" {
		disk.UID = sess.SessionUID
		disk.Applied = shown
		disk.Stopped = false
		disk.set("Applied", "already", shown, a.now())
		return true
	}
	return false
}

func renameTarget(sess protocol.Session, text string) (string, bool) {
	if text == "" {
		if sess.Name == "" || !sessiontitle.StartsWithLetterOrDigit(sess.Name) || !sessiontitle.Typeable(sess.Name) || sessiontitle.ContainsSelectionPhrase(sess.Name) {
			return "", false
		}
		return sess.Name, true
	}
	if sessiontitle.Validate(text) != "" {
		return "", false
	}
	target := sessiontitle.Compose(sess.Model, text)
	if !sessiontitle.StartsWithLetterOrDigit(target) || !sessiontitle.Typeable(target) || sessiontitle.ContainsSelectionPhrase(target) {
		return "", false
	}
	return target, true
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
	if prev.found && titleSame(d, &prev) {
		return
	}
	if err := writeTitleFile(s, d); err != nil {
		log.Warn("session title state", "err", err.Error())
		return
	}
	if d.State == prev.State && d.Reason == prev.Reason && d.Attempts == prev.Attempts && d.Target == prev.Target {
		return
	}
	log.Info("session title", "state", d.State, "reason", d.Reason, "attempts", d.Attempts, "titleHash", sessiontitle.HashPrefix(d.Target))
}

func titleSame(a, b *titleDisk) bool {
	left, errA := json.Marshal(titleCopy(a))
	right, errB := json.Marshal(titleCopy(b))
	return errA == nil && errB == nil && string(left) == string(right)
}

func titleCopy(d *titleDisk) titleDisk {
	c := *d
	c.found = false
	return c
}

// writeTitleFile is the durable title.json write. Tests replace it.
var writeTitleFile = func(s Settings, d *titleDisk) error {
	data, err := json.MarshalIndent(titleCopy(d), "", "  ")
	if err != nil {
		return err
	}
	return writeFileDurable(s.statePath(titleStateFile), append(data, '\n'), 0o600)
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
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" || text[0] == '"' {
		return false
	}
	if json.Unmarshal(raw, dest) != nil || *dest <= 0 {
		return false
	}
	return true
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
	case "idle", "shell":
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
	res, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: []string{"list-clients", "-t", "=" + TmuxSession, "-F", "#{client_tty}"}})
	if err != nil {
		return 0, err
	}
	out := string(res.Stdout)
	if out == "" {
		return 0, nil
	}
	n := strings.Count(out, "\n")
	if !strings.HasSuffix(out, "\n") {
		n++
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
	res, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: []string{
		"list-panes", "-s", "-t", "=" + TmuxSession, "-F", sessiontitle.ListPanesFormat(),
	}})
	if err != nil {
		return paneSnap{}, err
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(string(res.Stdout), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return paneSnap{}, errors.New("pane")
	}
	fields := strings.SplitN(lines[0], " ", 8)
	if len(fields) != 8 || fields[7] == "" || !strings.HasPrefix(fields[2], "%") {
		return paneSnap{}, errors.New("pane")
	}
	for _, flag := range []string{fields[3], fields[4], fields[5]} {
		if flag != "0" && flag != "1" {
			return paneSnap{}, errors.New("pane")
		}
	}
	pid, err := strconv.Atoi(fields[6])
	if err != nil || pid <= 0 {
		return paneSnap{}, errors.New("pane")
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
	res, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: []string{"capture-pane", "-p", "-e", "-t", paneID}})
	if err != nil {
		return "", err
	}
	return string(res.Stdout), nil
}

func readCursor(ctx context.Context, r Runner, s Settings, paneID string) (sessiontitle.Cursor, error) {
	res, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: []string{
		"display-message", "-p", "-t", paneID, sessiontitle.DisplayMessageFormat(),
	}})
	if err != nil {
		return sessiontitle.Cursor{}, err
	}
	fields := strings.Fields(string(res.Stdout))
	if len(fields) != 4 {
		return sessiontitle.Cursor{}, errors.New("cursor")
	}
	x, errX := strconv.Atoi(fields[0])
	y, errY := strconv.Atoi(fields[1])
	width, errW := strconv.Atoi(fields[2])
	height, errH := strconv.Atoi(fields[3])
	if errX != nil || errY != nil || errW != nil || errH != nil || x < 0 || y < 0 || width <= 0 || height <= 0 {
		return sessiontitle.Cursor{}, errors.New("cursor")
	}
	return sessiontitle.Cursor{X: x, Y: y, Width: width, Height: height, Known: true}, nil
}

// renameRun gives one subprocess its own two-second limit and does not wait
// past that limit. The caller's context is the five-second budget.
func renameRun(ctx context.Context, r Runner, c Cmd) (Result, error) {
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	call, cancel := context.WithTimeout(ctx, renameCallFor)
	defer cancel()
	type outcome struct {
		res Result
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.Run(call, c)
		done <- outcome{res, err}
	}()
	select {
	case <-call.Done():
		return Result{}, call.Err()
	case got := <-done:
		return got.res, got.err
	}
}

func quietStatus(status string) bool {
	return status == "idle" || status == "shell"
}

// clearTyped removes exactly the text this pod just typed, one Backspace per
// rune. It does not depend on busy or idle, and a spinner row does not block
// it. Every condition is read again immediately before the keys. It reports
// whether the row is empty again with the cursor at column 2.
func clearTyped(ctx context.Context, r Runner, s Settings, p agentPid, pane paneSnap, typed string) bool {
	n, err := strictClientCount(ctx, r, s)
	if err != nil || n != 0 {
		return false
	}
	again, err := readAgentPane(ctx, r, s)
	if err != nil || again.id != pane.id || again.pid != pane.pid || !paneForeground(p.Pid, again) {
		return false
	}
	rec, fail := readTitleRecord(s, p)
	if fail != "" || rec.status == "waiting" {
		return false
	}
	screen, err := capturePane(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	cur, err := readCursor(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	if sessiontitle.ClassifyBox(screen, typed, cur) != sessiontitle.InputExact {
		return false
	}
	args := []string{"send-keys", "-t", pane.id}
	for range utf8.RuneCountInString(typed) {
		args = append(args, "Backspace")
	}
	if _, err := renameRun(ctx, r, Cmd{Name: s.TmuxBin, Args: args}); err != nil {
		return false
	}
	empty, err := capturePane(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	cur, err = readCursor(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	return sessiontitle.ClassifyBox(empty, "", cur) == sessiontitle.InputEmpty
}

func secondCheck(ctx context.Context, r Runner, s Settings, p agentPid, pane paneSnap, rec titleRecord, typed string) bool {
	again, err := readAgentPane(ctx, r, s)
	if err != nil || again != pane || !paneForeground(p.Pid, again) {
		return false
	}
	fresh, fail := readTitleRecord(s, p)
	if fail != "" || fresh.updated != rec.updated || fresh.status != rec.status || !quietStatus(fresh.status) {
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
	cur, err := readCursor(ctx, r, s, pane.id)
	if err != nil {
		return false
	}
	return sessiontitle.ClassifyInput(screen, typed, cur) == sessiontitle.InputExact
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
