// Package coordinatorwait implements a parent-owned metadata wait. It never
// starts a provider turn or reads handoff contents.
package coordinatorwait

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"time"
)

// Options are explicit operating limits, not authorization for unattended work.
type Options struct {
	SessionUID string        `json:"sessionUID"`
	ThreadID   string        `json:"threadID"`
	WaitID     string        `json:"waitID"`
	Directory  string        `json:"directory"`
	Timer      time.Duration `json:"timer"`
	ScanEvery  time.Duration `json:"scanEvery"`
	MaxEntries int           `json:"maxEntries"`
	MaxPending int           `json:"maxPending"`
	BudgetEnd  time.Time     `json:"budgetEnd"`
}

var identity = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (o Options) Validate() error {
	if !identity.MatchString(o.SessionUID) || !identity.MatchString(o.ThreadID) || !identity.MatchString(o.WaitID) {
		return errors.New("session UID, explicit thread ID and wait ID are required (1..128 letters, digits, underscores or hyphens)")
	}
	if !filepath.IsAbs(o.Directory) || filepath.Clean(o.Directory) != o.Directory || len(o.Directory) > 4096 {
		return errors.New("directory must be an absolute clean path")
	}
	if o.Timer < time.Second || o.ScanEvery < time.Second || o.Timer%time.Second != 0 || o.ScanEvery%time.Second != 0 || o.MaxEntries < 1 || o.MaxEntries > 4096 || o.MaxPending < 1 || o.MaxPending > 1024 || o.BudgetEnd.IsZero() {
		return errors.New("require whole-second timer/scan cadence >=1s, entries 1..4096, pending 1..1024 and budget end")
	}
	return nil
}

// Result is the complete output allowlist. References are data, never commands.
type Result struct {
	Kind      string    `json:"kind"`
	Reason    string    `json:"reason,omitempty"`
	ID        string    `json:"id"`
	Note      string    `json:"note,omitempty"`
	Path      string    `json:"path,omitempty"`
	Cursor    string    `json:"cursor"`
	Watermark string    `json:"watermark"`
	Time      time.Time `json:"time"`
}

// Record is private to this Session and caller-named native thread. Outstanding
// is committed before output and replayed whole until its exact ID is acknowledged.
type Record struct {
	Started       time.Time          `json:"started"`
	DirectoryPin  *DirectoryIdentity `json:"directoryPin,omitempty"`
	EntryOverflow bool               `json:"entryOverflow"`
	Options       Options            `json:"options"`
	NextTimer     time.Time          `json:"nextTimer"`
	Cursor        string             `json:"cursor"`
	Watermark     string             `json:"watermark"`
	Files         map[string]string  `json:"files"`
	Refused       []string           `json:"refused,omitempty"`
	Pending       []Result           `json:"pending"`
	Seen          []string           `json:"seen"`
	Sequence      uint64             `json:"sequence"`
	Outstanding   *Result            `json:"outstanding,omitempty"`
	LastID        string             `json:"lastID,omitempty"`
	Stopped       bool               `json:"stopped"`
	Ended         bool               `json:"ended"`
	NeedsResync   bool               `json:"needsResync"`
}

func New(o Options, now time.Time, cursor string, files map[string]string) *Record {
	return &Record{Started: now, Options: o, NextTimer: now.Add(o.Timer), Cursor: cursor, Watermark: watermark(files), Files: files}
}

// Bind requires explicit same-thread rearming. Neither a new budget nor changed
// limits can extend/rebind an existing wait. Resume is deliberate, never automatic.
func (r *Record) Bind(o Options, ack string, resume, reconciled bool) error {
	if r.Options != o {
		return errors.New("wait binding or limits differ from the saved record")
	}
	if r.Outstanding != nil && ack == "" {
		if reconciled {
			return errors.New("--reconciled is only for an unreadable stored result")
		}
		return nil // replay the exact stored result, including stopped/budget_end
	}
	if r.LastID != "" && ack != r.LastID {
		return errors.New("acknowledgement must name the stored pending result")
	}
	if r.LastID == "" && ack != "" {
		return errors.New("no result to acknowledge")
	}
	lost := r.LastID != "" && r.Outstanding == nil
	if lost && !reconciled {
		return errors.New("stored result is unreadable: reconcile all sources, then --ack lastID --reconciled")
	}
	if !lost && reconciled {
		return errors.New("--reconciled is only for an unreadable stored result")
	}
	if r.Ended {
		return errors.New("wait budget has ended")
	}
	if r.Stopped && !resume {
		return errors.New("stopped wait requires explicit --resume with the same thread ID")
	}
	if r.NeedsResync {
		r.Seen = nil
	}
	r.LastID = ""
	r.Outstanding = nil
	r.NeedsResync = false
	r.Stopped = false
	if lost {
		r.resync()
	}

	return nil
}

// Note accepts only metadata returned by the existing addressed parent wait API.
// Cursor comparison is opaque; event identity is stable across note replay.
func (r *Record) Note(name, cursor string, resync bool) {
	r.Cursor = cursor
	if resync {
		r.resync()
		return
	}
	if name != "" {
		r.add(Result{Kind: "note", ID: digest("note:" + name), Note: name})
	}
}

func (r *Record) add(e Result) {
	if r.NeedsResync || slices.Contains(r.Seen, e.ID) || e.ID == r.LastID || slices.ContainsFunc(r.Pending, func(p Result) bool { return p.ID == e.ID }) {
		return
	}
	if len(r.Pending) >= r.Options.MaxPending {
		r.resync()
		return
	}
	r.Pending = append(r.Pending, e)
}
func (r *Record) resync() {
	r.NeedsResync = true
	r.Pending = nil
}

// Refuse reports each changed refused entry once without retaining its name or
// identity. Queue only available slots; later scans report the rest without
// displacing notes or file events. Sorting makes bounded selection deterministic.
func (r *Record) Refuse(refused map[string]string) {
	kept := make([]string, 0, len(r.Refused))
	for _, hash := range r.Refused {
		if _, ok := refused[hash]; ok {
			kept = append(kept, hash)
		}
	}
	r.Refused = kept
	keys := make([]string, 0, len(refused))
	for hash := range refused {
		keys = append(keys, hash)
	}
	slices.Sort(keys)
	for _, hash := range keys {
		if slices.Contains(r.Refused, hash) || slices.ContainsFunc(r.Pending, func(e Result) bool { return e.ID == hash }) || len(r.Pending) >= r.Options.MaxPending || r.NeedsResync {
			continue
		}
		r.Pending = append(r.Pending, Result{Kind: "resync", ID: hash, Reason: refused[hash]})
	}
}

// Scan coalesces one metadata burst to one directory reference. No entry text is
// opened. An incomplete bounded scan requires explicit source reconciliation.
func (r *Record) Scan(files map[string]string, overflow bool) {
	if overflow {
		if !r.EntryOverflow {
			r.EntryOverflow = true
			r.resync()
		}
		return
	}
	r.EntryOverflow = false
	changes := 0
	changed := len(files) != len(r.Files)
	for name, stamp := range files {
		if r.Files[name] != stamp {
			changed = true
			changes++
		}
	}
	for name := range r.Files {
		if _, ok := files[name]; !ok {
			changes++
		}
	}
	r.Files = files
	r.Watermark = watermark(files)
	if changes > r.Options.MaxPending {
		r.resync()
		return
	}
	if changed {
		r.add(Result{Kind: "file", ID: digest("file:" + r.Watermark), Path: r.Options.Directory})
	}
}

// Take coalesces missed timer periods; stop and budget take precedence over work.
// Callers must persist this transition successfully BEFORE printing a result.
func (r *Record) Take(now time.Time, stop bool) *Result {
	if r.Outstanding != nil {
		e := *r.Outstanding
		return &e
	}
	var e Result
	switch {
	case stop:
		r.Stopped = true
		e.Kind = "stopped"
	case !now.Before(r.Options.BudgetEnd):
		r.Ended = true
		e.Kind = "budget_end"
	case r.NeedsResync:
		e.Kind = "resync"
	case len(r.Pending) > 0 && len(r.Seen) >= r.Options.MaxPending:
		r.resync()
		e.Kind = "resync"
	case len(r.Pending) > 0:
		e = r.Pending[0]
		r.Pending = r.Pending[1:]
	case !now.Before(r.NextTimer):
		e.Kind = "timer"
		r.NextTimer = now.Add(r.Options.Timer)
	default:
		return nil
	}
	r.Sequence++
	if e.ID == "" {
		e.ID = digest(fmt.Sprintf("%s:%s:%d:%s", r.Options.SessionUID, r.Options.WaitID, r.Sequence, e.Kind))
	}
	e.Cursor = r.Cursor
	e.Watermark = r.Watermark
	e.Time = now.UTC()
	if e.Kind == "note" || e.Kind == "file" {
		r.Seen = append(r.Seen, e.ID)
	}
	if e.Kind == "resync" && e.Reason != "" {
		r.Refused = append(r.Refused, e.ID)
	}
	r.LastID = e.ID
	saved := e
	r.Outstanding = &saved
	return &e
}

func digest(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func watermark(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "%d:%s:%s\n", len(k), k, files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validate refuses malformed saved metadata before it can become output. The
// private record is a checkpoint, not a source of instructions or arbitrary paths.
func (r *Record) validate() error {
	if r.Started.IsZero() || r.Options.BudgetEnd.After(r.Started.Add(24*time.Hour)) {
		return errors.New("budget end exceeds 24-hour wait bound")
	}
	if r.DirectoryPin != nil && (r.DirectoryPin.Path != r.Options.Directory || r.DirectoryPin.Inode == 0) {
		return errors.New("invalid directory pin")
	}
	if r.Watermark != watermark(r.Files) || r.Sequence > 1<<63 {
		return errors.New("invalid checkpoint")
	}
	if len(r.Refused) > r.Options.MaxEntries {
		return errors.New("invalid refusal bound")
	}
	for _, hash := range r.Refused {
		if !validID(hash) {
			return errors.New("invalid refusal hash")
		}
	}
	for name, stamp := range r.Files {
		if !safeEntry(name) || len(stamp) > 128 {
			return errors.New("invalid file metadata")
		}
	}
	for _, id := range append(slices.Clone(r.Seen), r.LastID) {
		if id != "" && !validID(id) {
			return errors.New("invalid event ID")
		}
	}
	if r.Outstanding != nil {
		if r.Outstanding.ID != r.LastID || r.validateResult(*r.Outstanding, true) != nil {
			return errors.New("invalid outstanding result")
		}
	}
	for _, e := range r.Pending {
		if err := r.validateResult(e, false); err != nil {
			return err
		}
	}
	return nil
}
func (r *Record) validateResult(e Result, complete bool) error {
	if !validID(e.ID) {
		return errors.New("invalid pending ID")
	}
	if complete && (e.Time.IsZero() || e.Cursor != r.Cursor || e.Watermark != r.Watermark) {
		return errors.New("invalid outstanding checkpoint")
	}
	switch e.Kind {
	case "timer", "stopped", "budget_end":
		if !complete || e.Note != "" || e.Path != "" || e.Reason != "" {
			return errors.New("invalid terminal result")
		}
	case "resync":
		if !refusalReason(e.Reason) && (!complete || e.Reason != "") || e.Note != "" || e.Path != "" {
			return errors.New("invalid refusal result")
		}
	case "note":
		if e.Reason != "" || e.Note == "" || len(e.Note) > 253 || !safeEntry(e.Note) || e.Path != "" {
			return errors.New("invalid note reference")
		}
	case "file":
		if e.Reason != "" || e.Path != r.Options.Directory || e.Note != "" {
			return errors.New("pending path escapes handoff directory")
		}
	default:
		return errors.New("invalid pending kind")
	}
	return nil
}
func validID(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}
