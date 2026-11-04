package agentd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/codexauth"
)

// The rejected-login marker (consumer side of the keeper's reliable refresh).
//
// A provider can revoke the keeper's login while the applied access token has
// not expired. Until the keeper's next generation is visible, the pod would
// read "logged in". The one pod-side signal is the session transcript ending in
// a 401 turn, which the continue logic already classifies. This file records
// that observation, and only that, as ~/.agentd/claude-rejected.json:
//
//	{"version":1,"generation":N,"observedAt":"<RFC3339 UTC>"}
//
// N is the keeper generation applied when the rejection was seen; observedAt is
// the time of the rejected turn in the pod's own transcript. The marker holds no
// token, no hash, no error text and no transcript text. agentd's poll is its
// only writer, under a non-blocking file lock; a poll that cannot take the lock
// writes nothing and keeps the rejection in memory for the heartbeat (lost on
// an agentd restart). It never sends, never touches the
// continue receipt and never changes a state word: the text rides in the
// existing status message.
//
// DESIGN CHOICES (not proved prevention):
//
//   - Generation-only binding. A marker is about generation N and says nothing
//     about N+1. When N+1 is applied the marker stops applying; N+1 reads as it
//     did before this feature (no rejection observed) until agentd sees a 401
//     dated after N+1 was applied. "No rejection observed" is not "proved
//     accepted".
//   - Age never clears. An unresolved same-generation marker stays Rejected for
//     as long as that generation is the current one and the access token is
//     unexpired; only the sentence changes (it asks to check the keeper after
//     rejectionPendingWindow). When the access token itself has expired, the
//     existing expired-token display takes over.
//   - Cannot tell is not fine. A marker that exists but cannot be read, parsed,
//     or believed (oversize, unknown field, future-dated, non-regular, for a
//     generation above the applied one) reads as "cannot tell", never as Ready.
//     The one exception is a marker file last modified before the applied
//     generation arrived: it cannot describe that generation. The writer never
//     deletes a marker it cannot parse; it deletes only a parsed marker for an
//     older generation than the one applied.
//   - Clear only on explicit proof of acceptance: see classifyTranscript's
//     Accepted. Nothing else clears a marker for the current generation.
const (
	rejectionFile = "claude-rejected.json"
	rejectionLock = "claude-rejected.lock"
	// rejectionPendingWindow changes the sentence only, never the state.
	rejectionPendingWindow = 10 * time.Minute
	// rejectionSkew tolerates a time stamped slightly ahead of this clock.
	rejectionSkew     = time.Minute
	rejectionMaxBytes = 4 << 10
)

// rejectionAfterRead is a test seam: called inside the critical section right
// after the marker is read. Nil in production.
var rejectionAfterRead func()

// rejectionAfterLookup is a test seam: called after each reader's lookup of the
// marker. Nil in production.
var rejectionAfterLookup func()

// rejectionFlock is syscall.Flock; a test seam for lock errors.
var rejectionFlock = func(fd, how int) error { return syscall.Flock(fd, how) }

type claudeRejection struct {
	Version    int       `json:"version"`
	Generation uint64    `json:"generation"`
	ObservedAt time.Time `json:"observedAt"`
}

type rejectionFileState int

const (
	rejectionAbsent  rejectionFileState = iota
	rejectionValid                      // parsed, believable
	rejectionInvalid                    // exists, but cannot be read or believed
)

// readRejectionFile classifies the marker file. Only a missing file is Absent.
func readRejectionFile(s Settings, now time.Time) (claudeRejection, rejectionFileState, time.Time) {
	path := s.statePath(rejectionFile)
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return claudeRejection{}, rejectionAbsent, time.Time{}
	case err != nil:
		return claudeRejection{}, rejectionInvalid, time.Time{}
	}
	mtime := fi.ModTime()
	if !fi.Mode().IsRegular() || fi.Size() > rejectionMaxBytes {
		return claudeRejection{}, rejectionInvalid, mtime
	}
	raw, err := readBounded(path, rejectionMaxBytes)
	if err != nil {
		return claudeRejection{}, rejectionInvalid, mtime
	}
	var m claudeRejection
	if codexauth.DecodeStrict(raw, &m) != nil || m.Version != 1 || m.Generation == 0 || m.ObservedAt.IsZero() || m.ObservedAt.After(now.Add(rejectionSkew)) {
		return claudeRejection{}, rejectionInvalid, mtime
	}
	return m, rejectionValid, mtime
}

type rejectionKind int

const (
	rejectionNone         rejectionKind = iota // no rejection applies to the current generation
	rejectionRejected                          // the provider rejected the current generation
	rejectionUnverifiable                      // a marker exists that cannot be believed: cannot tell
)

// rejectionView is one read of the marker and the applied receipt. A status line
// is built from one view, so a marker that changes while it is built cannot mix
// two answers (for example "rejected" checked, then "logged in" printed).
type rejectionView struct {
	m    claudeRejection // set when kind is rejectionRejected
	kind rejectionKind
	// applied is the applied receipt's generation; 0 when it cannot be read.
	applied uint64
	// extra is a rejection agentd saw but could not write (nil otherwise); only
	// the in-memory overlay sets it, and only for the applied generation.
	extra *claudeRejection
}

// lookupRejection is what every agentd reader applies. It only reads.
func lookupRejection(s Settings, now time.Time) rejectionView {
	if rejectionAfterLookup != nil {
		defer rejectionAfterLookup()
	}
	var v rejectionView
	var applied claudeReceipt
	receipt := s.statePath("claude-access-generation.json")
	if readJSONFile(receipt, &applied) == nil {
		v.applied = applied.Generation
	}
	m, st, mtime := readRejectionFile(s, now)
	switch {
	case st == rejectionAbsent:
		return v
	case v.applied == 0:
		// A marker exists. Without a readable applied generation nothing can say
		// what it describes: cannot tell, not "none".
		v.kind = rejectionUnverifiable
	case st == rejectionInvalid:
		// A file last written before the applied generation arrived cannot be
		// about it, whatever it holds. Anything else cannot be told.
		if fi, err := os.Lstat(receipt); err == nil && !mtime.IsZero() && mtime.Before(fi.ModTime()) {
			return v
		}
		v.kind = rejectionUnverifiable
	case m.Generation == v.applied:
		v.m, v.kind = m, rejectionRejected
	case m.Generation < v.applied:
		// About an older generation: none.
	default:
		v.kind = rejectionUnverifiable // above the applied generation: inconsistent
	}
	return v
}

// rejectionText is the status sentence for a rejected current generation. It
// names the fixed observation time, not an age, so the heartbeat text does not
// change on every beat; it changes once, when the pending window passes.
func rejectionText(m claudeRejection, now time.Time) string {
	at := m.ObservedAt.UTC().Format(time.RFC3339)
	if now.Sub(m.ObservedAt) >= rejectionPendingWindow {
		return fmt.Sprintf("login-rejected: the provider rejected keeper generation %d at %s and no newer generation has arrived; check the keeper; no login is run in this pod", m.Generation, at)
	}
	return fmt.Sprintf("login-rejected: the provider rejected keeper generation %d at %s while its access token was unexpired; a newer generation may or may not arrive; no login is run in this pod", m.Generation, at)
}

const rejectionUnverifiableText = "login-unverified: this pod's rejection record is unreadable or inconsistent, so it cannot tell whether the provider rejected the keeper's login; no login is run in this pod"

// rejectionHeartbeatText is the agent message for a running session when a
// rejection (or a cannot-tell marker) applies, at every boundary of the access
// token: login-rejected while it is unexpired, otherwise the expiry text
// (refreshing, login-required) with the rejection appended. "" when none applies.
func rejectionHeartbeatText(s Settings, now time.Time) string {
	if s.ClaudeLiveFile == "" {
		return ""
	}
	v := lookupRejection(s, now)
	if v.kind == rejectionNone {
		return ""
	}
	return projectionStatusWith(s, now, v)
}

// rejectionUsableText is the text for an unexpired token, or "".
func rejectionUsableText(v rejectionView, now time.Time) string {
	switch {
	case v.kind == rejectionRejected:
		return rejectionText(v.m, now)
	case v.kind == rejectionUnverifiable:
		return rejectionUnverifiableText
	case v.extra != nil:
		return rejectionText(*v.extra, now)
	}
	return ""
}

// rejectionExpiredNote is appended to the expired-token text.
func rejectionExpiredNote(s Settings, v rejectionView) string {
	if s.ClaudeLiveFile == "" {
		return ""
	}
	note := func(m claudeRejection) string {
		return fmt.Sprintf("; the provider had rejected keeper generation %d at %s", m.Generation, m.ObservedAt.UTC().Format(time.RFC3339))
	}
	switch {
	case v.kind == rejectionRejected:
		return note(v.m)
	case v.kind == rejectionUnverifiable:
		return "; this pod's rejection record is unreadable or inconsistent, so it cannot tell whether the provider rejected the login"
	case v.extra != nil:
		return note(*v.extra)
	}
	return ""
}

// lockRejection takes the marker's lock without blocking. busy means another
// writer is in its critical section (normal: this poll does nothing); err is a
// failure to open or lock it at all.
func lockRejection(s Settings) (unlock func(), busy bool, err error) {
	path := s.statePath(rejectionLock)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		// flock needs only an open descriptor: an existing regular lock file that
		// cannot be opened for writing (a read-only file or file system) still
		// locks. O_NONBLOCK keeps a FIFO in its place from blocking the open.
		ro, rerr := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if rerr != nil {
			return nil, false, err
		}
		if fi, serr := ro.Stat(); serr != nil || !fi.Mode().IsRegular() {
			_ = ro.Close()
			return nil, false, err
		}
		f = ro
	}
	if err := rejectionFlock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, true, nil // another writer holds it: normal
		}
		return nil, false, err // any other errno is a lock failure, logged by class
	}
	return func() { _ = rejectionFlock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, false, nil
}

// rejectionFault logs one fixed line when a failure class begins and again
// only after it has cleared; it never carries a path or an error text.
func (d *Daemon) rejectionFault(class, line string, failed bool) {
	if !failed {
		delete(d.rejectionFaults, class)
		return
	}
	if d.rejectionFaults[class] {
		return
	}
	if d.rejectionFaults == nil {
		d.rejectionFaults = map[string]bool{}
	}
	d.rejectionFaults[class] = true
	d.Log.Warn(line)
}

// removeRejection deletes our marker file and reports a failure other than "already gone".
func (d *Daemon) removeRejection(path string) {
	err := os.Remove(path)
	d.rejectionFault("remove", "Claude rejection record could not be removed", err != nil && !errors.Is(err, os.ErrNotExist))
}

// overlayRejection puts a rejection that could not be written (or was seen by a
// poll that could not lock the marker) into the heartbeat, at every boundary of
// the access token, so a full volume does not turn it into "logged in". It is in
// memory only: an agentd restart loses it. Like observe, it applies only to the
// applied receipt generation: a newer (or inconsistent) receipt retires it at once.
func (d *Daemon) overlayRejection(st *protocol.Status) {
	if d.unpersisted == nil || !d.projectsClaude() || st.Agent.Error != "" {
		return
	}
	switch st.Agent.State {
	case protocol.AgentBusy, protocol.AgentIdle, protocol.AgentWaiting:
	default:
		return
	}
	now := d.now()
	v := lookupRejection(d.S, now)
	if v.kind != rejectionNone || (v.applied != 0 && d.unpersisted.Generation != v.applied) {
		return
	}
	v.extra = d.unpersisted
	st.Agent.Error = projectionStatusWith(d.S, now, v)
}

// observeClaudeRejection runs on the poll after the projection sync and before
// the continue. It records a 401 turn that happened after the current
// generation was applied, and removes a marker that a newer generation or an
// explicit proof of acceptance has resolved. It sends nothing and writes
// nothing but the marker and its lock. When the lock cannot be had (busy is
// different: another writer is in its critical section), it still observes but
// writes and removes nothing, and keeps a new rejection in memory for the
// heartbeat, so a lock that cannot be created does not read as logged in.
func (d *Daemon) observeClaudeRejection() {
	if !d.projectsClaude() || d.claudeSyncErr != nil || d.Session.Mode == protocol.ModeTask {
		return
	}
	unlock, busy, err := lockRejection(d.S)
	d.rejectionFault("lock", "Claude rejection record could not be locked", err != nil)
	if busy {
		return
	}
	locked := err == nil
	if locked {
		defer unlock()
	}
	// With the lock, everything below is read under it, so a decision is never
	// made on a view another writer has since changed.
	receipt := d.S.statePath("claude-access-generation.json")
	var applied claudeReceipt
	if readJSONFile(receipt, &applied) != nil || applied.Generation == 0 {
		return
	}
	if d.unpersisted != nil && d.unpersisted.Generation != applied.Generation {
		d.unpersisted = nil
	}
	now := d.now()
	path := d.S.statePath(rejectionFile)
	m, st, _ := readRejectionFile(d.S, now)
	if rejectionAfterRead != nil {
		rejectionAfterRead()
	}
	have := false
	if st == rejectionValid {
		switch {
		case m.Generation < applied.Generation:
			// Parsed, and about an older generation: resolved by the newer one.
			if locked {
				d.removeRejection(path)
			}
		case m.Generation == applied.Generation:
			have = true
		default:
			return // inconsistent: leave it for the readers to report
		}
	}
	// An invalid marker is never deleted here. A fresh observation below may
	// replace it, which only turns "cannot tell" into a stated rejection.
	l, ok := currentLaunch(d.S)
	var p agentPid
	if !ok || !l.TUI || readJSONFile(d.S.statePath(pidFile), &p) != nil || !pidAlive(p) {
		return
	}
	tail, ok := readTranscriptTail(d.S, l.ConversationID)
	if !ok {
		return
	}
	// Explicit proof only: the most recent assistant turn, looking past trailing
	// user and tool_result entries, carries a real model response (Accepted) and
	// is dated after the rejected turn. User entries alone prove nothing.
	proof := func(since time.Time) bool {
		a := classifyLastAssistant(tail)
		return a.Accepted && !a.At.IsZero() && a.At.After(since)
	}
	if d.unpersisted != nil && proof(d.unpersisted.ObservedAt) {
		d.unpersisted = nil
	}
	if have {
		if locked && proof(m.ObservedAt) {
			d.removeRejection(path)
		}
		return
	}
	// The most recent assistant turn, past trailing user and tool_result entries
	// (mirrors the clear rule): a user entry inside the poll window does not lose
	// the refusal, and a later accepted turn supersedes it.
	turn := classifyLastAssistant(tail)
	if !turn.Revoked || turn.At.IsZero() || turn.At.After(now.Add(rejectionSkew)) {
		return
	}
	// Attributable to this generation only if the request began after it was
	// applied (the receipt is rewritten only when a new generation or fingerprint
	// lands). The request is the nearest earlier user entry; when the tail holds
	// none, the failure's own time is used, so an in-flight old-token request
	// that fails just after the apply can then be charged to the new generation.
	fi, err := os.Lstat(receipt)
	if err != nil || !turn.At.After(fi.ModTime()) || (!turn.RequestAt.IsZero() && !turn.RequestAt.After(fi.ModTime())) {
		return
	}
	obs := claudeRejection{Version: 1, Generation: applied.Generation, ObservedAt: turn.At.UTC()}
	if !locked {
		d.unpersisted = &obs // memory only until a poll holds the lock
		return
	}
	if err := writeJSONFile(path, obs); err != nil {
		d.rejectionFault("write", "Claude rejection record could not be written", true)
		d.unpersisted = &obs
		return
	}
	d.rejectionFault("write", "", false)
	d.unpersisted = nil
}
