package sessionraise

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	// MaxSessions is the most raised sessions the ledger holds.
	MaxSessions = 512
	// MaxWords is the most used-word rows the ledger holds.
	MaxWordUses = 2048
	// WordRetention is how long a word stays used.
	WordRetention = 90 * 24 * time.Hour
	// PerformerWindow is the hour in which three raises is the limit.
	PerformerWindow = time.Hour
	// PerformerLimit is the most raises one performer may make in that hour.
	PerformerLimit = 3
	// TargetGap is the minute between changes of one session's raise.
	TargetGap = time.Minute
	// EffectDelay is how long after a signed Sent a pending raise becomes active.
	EffectDelay = 2 * time.Minute
	// NoticeAttempts is the most completed attempts before a raise is unannounced.
	NoticeAttempts = 12
	// NoticeRetry is how long the keeper waits before another attempt.
	NoticeRetry = 5 * time.Minute
)

// SessionEntry is one raised session. The record counts only when its id and
// state equal this row and the row's UID is the session's UID. The other
// fields let a lost annotation be signed again without spending the word twice.
type SessionEntry struct {
	Name          string    `json:"name"`
	UID           string    `json:"uid"`
	ID            string    `json:"id"`
	State         string    `json:"state"`
	ChangedAt     time.Time `json:"changedAt"`
	Profile       string    `json:"profile,omitempty"`
	FromProfile   string    `json:"fromProfile,omitempty"`
	StartSessions bool      `json:"startSessions,omitempty"`
	Depth         string    `json:"depth,omitempty"`
	WordsHash     string    `json:"wordsHash,omitempty"`
	PerformedBy   string    `json:"performedBy,omitempty"`
	PerformedAt   string    `json:"performedAt,omitempty"`
	Where         string    `json:"where,omitempty"`
	SaidAt        string    `json:"saidAt,omitempty"`
	Ref           string    `json:"ref,omitempty"`
	OwnerSession  string    `json:"ownerSession,omitempty"`
	TakenBackBy   string    `json:"takenBackBy,omitempty"`
	TakenBackAt   string    `json:"takenBackAt,omitempty"`
	NoticeSentAt  string    `json:"noticeSentAt,omitempty"`
	ActiveFrom    string    `json:"activeFrom,omitempty"`
}

// WordUse is one spent word. The check is the performer and the hash, across
// every target.
type WordUse struct {
	Performer string    `json:"performer"`
	Hash      string    `json:"hash"`
	TargetUID string    `json:"targetUID"`
	At        time.Time `json:"at"`
}

// RaiseStamp is one raise a performer made, kept for the hourly limit.
type RaiseStamp struct {
	Performer string    `json:"performer"`
	At        time.Time `json:"at"`
}

// Ledger is the operator's raise ledger. Take-back and the operator's own
// writes do not call Admit, so they are exempt from every limit here.
type Ledger struct {
	Sessions []SessionEntry `json:"sessions"`
	Words    []WordUse      `json:"words"`
	Raises   []RaiseStamp   `json:"raises"`
}

// AdmitInput is one raise the route wants to record.
type AdmitInput struct {
	Name          string
	UID           string
	ID            string
	Profile       string
	FromProfile   string
	StartSessions bool
	Depth         string
	WordsHash     string
	PerformedBy   string
	PerformedAt   string
	Where         string
	SaidAt        string
	Ref           string
	OwnerSession  string
}

// AdmitResult is the row to sign. Same is true when this body is already
// pending or active: the caller writes nothing.
type AdmitResult struct {
	Entry SessionEntry
	Same  bool
}

// FullError is a ledger that cannot take another row. Resume is when the
// oldest row that is holding the cap expires.
type FullError struct {
	Resume time.Time
}

func (e *FullError) Error() string { return "raise ledger is full" }

// RateError is a limit that is still running. Until is when it ends.
// Target is set for the per-session minute; otherwise it is the hourly limit.
type RateError struct {
	Until  time.Time
	Target bool
	Wait   time.Duration
}

func (e *RateError) Error() string { return "raise rate limit" }

// ErrUsedWords is the same normalised word used again by this performer.
var ErrUsedWords = errors.New("words already used")

// ErrNoEntry is a take-back of a session the ledger does not hold.
var ErrNoEntry = errors.New("no raise entry")

// Find is the row for uid, or nil.
func (l *Ledger) Find(uid string) *SessionEntry {
	if l == nil {
		return nil
	}
	for i := range l.Sessions {
		if l.Sessions[i].UID == uid {
			return &l.Sessions[i]
		}
	}
	return nil
}

// Prune drops words older than 90 days and raise times older than an hour.
// It does not drop session rows: those stay while the session exists, and
// Admit drops a gone session only when the list is full.
func (l *Ledger) Prune(now time.Time) {
	if l == nil {
		return
	}
	words := l.Words[:0]
	for _, w := range l.Words {
		if now.Sub(w.At) <= WordRetention {
			words = append(words, w)
		}
	}
	l.Words = words
	raises := l.Raises[:0]
	for _, r := range l.Raises {
		if now.Sub(r.At) <= PerformerWindow {
			raises = append(raises, r)
		}
	}
	l.Raises = raises
}

// Admit records one raise, or reports Same when that raise is already pending
// or active. now is the operator clock. live is the set of session UIDs that
// still exist; a nil live drops no session row.
func (l *Ledger) Admit(now time.Time, in AdmitInput, live map[string]bool) (AdmitResult, error) {
	if l == nil {
		return AdmitResult{}, errors.New("no ledger")
	}
	l.Prune(now)
	if cur := l.Find(in.UID); cur != nil && sameRaise(cur, in) {
		return AdmitResult{Entry: *cur, Same: true}, nil
	}
	if l.used(in.PerformedBy, in.WordsHash) {
		return AdmitResult{}, ErrUsedWords
	}
	if cur := l.Find(in.UID); cur != nil {
		if wait := TargetGap - now.Sub(cur.ChangedAt); wait > 0 {
			return AdmitResult{}, &RateError{Until: cur.ChangedAt.Add(TargetGap), Target: true, Wait: wait}
		}
	}
	if err := l.performerLimit(in.PerformedBy, now); err != nil {
		return AdmitResult{}, err
	}
	if err := l.makeRoom(now, in.UID, live); err != nil {
		return AdmitResult{}, err
	}
	entry := SessionEntry{
		Name: in.Name, UID: in.UID, ID: in.ID, State: StatePending, ChangedAt: now,
		Profile: in.Profile, FromProfile: in.FromProfile, StartSessions: in.StartSessions,
		Depth: in.Depth, WordsHash: in.WordsHash, PerformedBy: in.PerformedBy,
		PerformedAt: in.PerformedAt, Where: in.Where, SaidAt: in.SaidAt,
		Ref: in.Ref, OwnerSession: in.OwnerSession,
	}
	replaced := false
	for i := range l.Sessions {
		if l.Sessions[i].UID == in.UID {
			l.Sessions[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		l.Sessions = append(l.Sessions, entry)
	}
	l.Words = append(l.Words, WordUse{Performer: in.PerformedBy, Hash: in.WordsHash, TargetUID: in.UID, At: now})
	l.Raises = append(l.Raises, RaiseStamp{Performer: in.PerformedBy, At: now})
	return AdmitResult{Entry: entry}, nil
}

func sameRaise(cur *SessionEntry, in AdmitInput) bool {
	if cur.State != StatePending && cur.State != StateActive {
		return false
	}
	return cur.WordsHash == in.WordsHash && cur.Profile == in.Profile && cur.StartSessions == in.StartSessions && cur.ID != ""
}

func (l *Ledger) used(performer, hash string) bool {
	for _, w := range l.Words {
		if w.Performer == performer && w.Hash == hash {
			return true
		}
	}
	return false
}

func (l *Ledger) performerLimit(performer string, now time.Time) error {
	var oldest time.Time
	n := 0
	for _, r := range l.Raises {
		if r.Performer != performer || now.Sub(r.At) > PerformerWindow {
			continue
		}
		n++
		if oldest.IsZero() || r.At.Before(oldest) {
			oldest = r.At
		}
	}
	if n >= PerformerLimit {
		return &RateError{Until: oldest.Add(PerformerWindow)}
	}
	return nil
}

func (l *Ledger) makeRoom(now time.Time, uid string, live map[string]bool) error {
	if l.Find(uid) != nil {
		return l.roomForWord(now)
	}
	if len(l.Sessions) >= MaxSessions {
		if live == nil {
			return &FullError{Resume: l.resume(now)}
		}
		kept := l.Sessions[:0]
		for _, s := range l.Sessions {
			if live[s.UID] {
				kept = append(kept, s)
			}
		}
		l.Sessions = kept
	}
	if len(l.Sessions) >= MaxSessions {
		return &FullError{Resume: l.resume(now)}
	}
	return l.roomForWord(now)
}

func (l *Ledger) roomForWord(now time.Time) error {
	if len(l.Words) < MaxWordUses {
		return nil
	}
	return &FullError{Resume: l.resume(now)}
}

func (l *Ledger) resume(now time.Time) time.Time {
	oldest := now
	for _, w := range l.Words {
		if oldest.Equal(now) || w.At.Before(oldest) {
			oldest = w.At
		}
	}
	if len(l.Words) == 0 {
		return now.Add(WordRetention)
	}
	return oldest.Add(WordRetention)
}

// TakeBack moves the row to taken-back. It spends no limit. A missing row is
// ErrNoEntry. The route writes this before the annotation, so the record
// stops counting at this write.
func (l *Ledger) TakeBack(uid, by, at string, now time.Time) (SessionEntry, error) {
	cur := l.Find(uid)
	if cur == nil {
		return SessionEntry{}, ErrNoEntry
	}
	cur.State = StateTakenBack
	cur.ChangedAt = now
	cur.TakenBackBy = by
	cur.TakenBackAt = at
	return *cur, nil
}

// Finish moves a pending row to active or unannounced when id still matches.
// A row that is no longer pending is left as it is and ok is false: a
// take-back is not overwritten.
func (l *Ledger) Finish(uid, id, state, noticeSentAt, activeFrom string, now time.Time) (SessionEntry, bool) {
	cur := l.Find(uid)
	if cur == nil || cur.ID != id || cur.State != StatePending {
		if cur == nil {
			return SessionEntry{}, false
		}
		return *cur, false
	}
	cur.State = state
	cur.ChangedAt = now
	cur.NoticeSentAt = noticeSentAt
	cur.ActiveFrom = activeFrom
	return *cur, true
}

// Marshal is the ConfigMap value.
func (l *Ledger) Marshal() ([]byte, error) {
	if l == nil {
		return []byte(`{"sessions":[],"words":[],"raises":[]}`), nil
	}
	return json.Marshal(l)
}

// Unmarshal reads a ledger. An empty body is an empty ledger.
func Unmarshal(raw []byte) (*Ledger, error) {
	l := &Ledger{}
	if len(raw) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(raw, l); err != nil {
		return nil, err
	}
	return l, nil
}
