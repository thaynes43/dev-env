// Package pages is the bounded retained record of Pushover pages that dev-env's
// own senders send to Tom (DESIGN-031). It is pure data and rules: standard
// library only, no Kubernetes client, no network, no clock of its own. The
// keeper writes a Document before it sends (write-ahead) and updates it with the
// outcome. agent-run reads the same JSON.
//
// A record says what dev-env did, never what the phone showed. The states are
// exact:
//
//	recorded   written before the send; the outcome was never stored (a crash, or the send is in flight)
//	sent       Pushover answered HTTP 200 with status 1 (it queued the page; this is not proof of delivery)
//	failed     a definite 4xx, or no request was made: Pushover did not accept it
//	ambiguous  a timeout, a transport error, a 5xx, or a 2xx that did not confirm: the page may have reached Tom
//
// The word "delivered" is never used. Anything not recorded is Unknown.
package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// SchemaVersion is the version of the Document.
	SchemaVersion = 1
	// ConfigMapKey is the key in the keeper's notice ConfigMap that holds the Document.
	ConfigMapKey = "pages.json"

	// MaxRecords and MaxBytes bound a Document. The oldest records go first and
	// DroppedCount says how many have gone.
	MaxRecords = 100
	MaxBytes   = 256 << 10
	// MaxDocumentBytes is the most any reader accepts: a ConfigMap holds 1 MiB.
	MaxDocumentBytes = 1 << 20

	// Pushover's own limits (https://pushover.net/api), applied to what is stored.
	MaxTitle    = 250
	MaxBody     = 1024
	MaxURL      = 512
	MaxURLTitle = 100
	// MaxField bounds the short identifier fields.
	MaxField = 200
	// MaxAttempts bounds the counters, so a stuck page cannot overflow them.
	MaxAttempts = 1000
)

// State is the honest outcome of a page.
type State string

const (
	StateRecorded  State = "recorded"
	StateSent      State = "sent"
	StateFailed    State = "failed"
	StateAmbiguous State = "ambiguous"
)

// Valid reports whether s is one of the four states.
func (s State) Valid() bool {
	switch s {
	case StateRecorded, StateSent, StateFailed, StateAmbiguous:
		return true
	}
	return false
}

// Outcome is what one send attempt came to.
type Outcome int

const (
	// OutcomeSent: HTTP 200 with status 1.
	OutcomeSent Outcome = iota
	// OutcomeFailed: a definite refusal, or no request made.
	OutcomeFailed
	// OutcomeAmbiguous: the page may or may not have been accepted.
	OutcomeAmbiguous
)

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	keyPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,198}[A-Za-z0-9]$|^[A-Za-z0-9]$`)

	// ErrInvalid is wrapped by every validation failure.
	ErrInvalid = errors.New("pages: invalid")
)

// Record is one page. There is no field that could carry a credential: the
// Pushover token and user key never reach this package.
type Record struct {
	// Key is the idempotency key: one record per message, however many attempts.
	Key string `json:"key"`
	// Correlation ties the page to the incident or day it is about.
	Correlation string `json:"correlation,omitempty"`
	// Sender is the dev-env component that sent it; Source is the path inside it.
	Sender string `json:"sender"`
	Source string `json:"source"`
	// Provenance names the code that sent it (a build identity).
	Provenance string `json:"provenance,omitempty"`

	Title     string `json:"title"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated,omitempty"`
	Priority  int    `json:"priority"`
	URL       string `json:"url,omitempty"`
	URLTitle  string `json:"urlTitle,omitempty"`

	// RecordedAt is when the record was first written, always UTC.
	RecordedAt time.Time `json:"recordedAt"`
	// LastAttemptAt is when the latest attempt began, UTC.
	LastAttemptAt time.Time `json:"lastAttemptAt"`
	// State is the best evidence over every attempt (see Finish).
	State State `json:"state"`
	// Attempts counts attempts begun. SentCount and AmbiguousCount count the
	// attempts that ended that way. An attempt with no outcome stored is the gap.
	Attempts       int `json:"attempts"`
	SentCount      int `json:"sentCount,omitempty"`
	AmbiguousCount int `json:"ambiguousCount,omitempty"`
	// RequestID is Pushover's `request` value from the latest accepted attempt.
	RequestID string `json:"requestId,omitempty"`
	// HTTPStatus is the latest status Pushover answered, 0 when it never did.
	HTTPStatus int `json:"httpStatus,omitempty"`
}

// Document is the retained set, one JSON value under ConfigMapKey.
type Document struct {
	Version int `json:"version"`
	// RecordingSince is when the first record was written: the start of coverage.
	RecordingSince *time.Time `json:"recordingSince,omitempty"`
	// DroppedCount is how many records the bounds have removed, ever.
	DroppedCount int `json:"droppedCount,omitempty"`
	// OldestRetainedAt is the RecordedAt of the oldest record kept.
	OldestRetainedAt *time.Time `json:"oldestRetainedAt,omitempty"`
	Records          []Record   `json:"records"`
}

// New returns an empty Document.
func New() *Document { return &Document{Version: SchemaVersion, Records: []Record{}} }

// Find returns the record for key, or nil.
func (d *Document) Find(key string) *Record {
	for i := range d.Records {
		if d.Records[i].Key == key {
			return &d.Records[i]
		}
	}
	return nil
}

// Page is what a sender hands over before it sends.
type Page struct {
	Key, Correlation string
	Sender, Source   string
	Provenance       string
	Title, Body      string
	Priority         int
	URL, URLTitle    string
}

// Begin writes the record before the send and counts the attempt. A key seen
// before adds an attempt to its record and never a second record: a retry does
// not duplicate. It reports whether the record is new. After Begin the record
// is `recorded` unless earlier attempts already left better evidence (sent, or
// ambiguous), which an attempt in flight does not erase.
func (d *Document) Begin(p Page, now time.Time) (created bool, err error) {
	if !keyPattern.MatchString(p.Key) {
		return false, fmt.Errorf("%w: key", ErrInvalid)
	}
	now = now.UTC()
	if r := d.Find(p.Key); r != nil {
		r.Attempts = bump(r.Attempts)
		r.LastAttemptAt = now
		if r.State == StateFailed || !r.State.Valid() {
			r.State = StateRecorded
		}
		return false, nil
	}
	title, tt := clamp(RedactText(p.Title), MaxTitle)
	body, bt := clamp(RedactText(p.Body), MaxBody)
	r := Record{
		Key:         p.Key,
		Correlation: first(p.Correlation, MaxField),
		Sender:      first(p.Sender, MaxField),
		Source:      first(p.Source, MaxField),
		Provenance:  first(p.Provenance, MaxField),
		Title:       title,
		Body:        body,
		Truncated:   tt || bt,
		Priority:    p.Priority,
		RecordedAt:  now, LastAttemptAt: now,
		State: StateRecorded, Attempts: 1,
	}
	r.URL, r.URLTitle = safeLink(p.URL, p.URLTitle)
	r.Correlation, r.Source, r.Provenance = RedactText(r.Correlation), RedactText(r.Source), RedactText(r.Provenance)
	if d.RecordingSince == nil {
		t := now
		d.RecordingSince = &t
	}
	d.Records = append(d.Records, r)
	d.trim()
	return true, nil
}

// Finish stores one attempt's outcome. status is the HTTP status Pushover
// answered (0 when it never did); requestID is its `request` value, kept only
// for an accepted page and only if it looks like an id. The state is the best
// evidence over all attempts: sent beats ambiguous beats failed, because a
// later refusal does not undo an earlier acceptance or an attempt that may
// have landed. A key with no record is ignored (it was trimmed away).
func (d *Document) Finish(key string, o Outcome, status int, requestID string, now time.Time) {
	r := d.Find(key)
	if r == nil {
		return
	}
	r.LastAttemptAt = now.UTC()
	if status >= 100 && status <= 599 {
		r.HTTPStatus = status
	} else {
		r.HTTPStatus = 0
	}
	switch o {
	case OutcomeSent:
		r.SentCount = bump(r.SentCount)
		if requestIDPattern.MatchString(requestID) {
			r.RequestID = requestID
		}
	case OutcomeAmbiguous:
		r.AmbiguousCount = bump(r.AmbiguousCount)
	}
	switch {
	case r.SentCount > 0:
		r.State = StateSent
	case r.AmbiguousCount > 0:
		r.State = StateAmbiguous
	case o == OutcomeFailed:
		r.State = StateFailed
	default:
		r.State = StateRecorded
	}
}

func bump(n int) int {
	if n >= MaxAttempts {
		return MaxAttempts
	}
	return n + 1
}

// trim drops the oldest records until the bounds hold, and refreshes the markers.
func (d *Document) trim() {
	for len(d.Records) > MaxRecords {
		d.drop()
	}
	for len(d.Records) > 1 {
		raw, err := json.Marshal(d)
		if err != nil || len(raw) <= MaxBytes {
			break
		}
		d.drop()
	}
	d.mark()
}

func (d *Document) drop() {
	d.Records = append(d.Records[:0], d.Records[1:]...)
	d.DroppedCount++
}

func (d *Document) mark() {
	if len(d.Records) == 0 {
		d.OldestRetainedAt = nil
		return
	}
	t := d.Records[0].RecordedAt
	d.OldestRetainedAt = &t
}

// Marshal encodes the Document, trimmed to its bounds.
func (d *Document) Marshal() ([]byte, error) {
	d.Version = SchemaVersion
	if d.Records == nil {
		d.Records = []Record{}
	}
	d.trim()
	return json.Marshal(d)
}

// Parse decodes and validates a Document. A reader treats an error as
// "Unknown", never as an empty history.
func Parse(raw []byte) (*Document, error) {
	if len(raw) > MaxDocumentBytes {
		return nil, fmt.Errorf("%w: document too large", ErrInvalid)
	}
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("%w: not JSON", ErrInvalid)
	}
	if d.Version != SchemaVersion {
		return nil, fmt.Errorf("%w: version %d", ErrInvalid, d.Version)
	}
	if len(d.Records) > MaxRecords {
		return nil, fmt.Errorf("%w: too many records", ErrInvalid)
	}
	seen := map[string]bool{}
	for i := range d.Records {
		r := &d.Records[i]
		if !keyPattern.MatchString(r.Key) || seen[r.Key] {
			return nil, fmt.Errorf("%w: record %d key", ErrInvalid, i)
		}
		seen[r.Key] = true
		if !r.State.Valid() {
			return nil, fmt.Errorf("%w: record %d state", ErrInvalid, i)
		}
		if r.RecordedAt.IsZero() {
			return nil, fmt.Errorf("%w: record %d time", ErrInvalid, i)
		}
		r.sanitize()
	}
	if d.Records == nil {
		d.Records = []Record{}
	}
	return &d, nil
}

// clamp cuts s to max characters (not bytes, as Pushover counts) on a rune
// boundary and reports whether it cut. Invalid UTF-8 is replaced first.
func clamp(s string, max int) (string, bool) {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) <= max {
		return s, false
	}
	r := []rune(s)
	return string(r[:max]), true
}

func first(s string, max int) string {
	out, _ := clamp(s, max)
	return out
}

// safeLink keeps a plain https link and stores Redacted for any link that
// could carry a credential (see RedactURL).
func safeLink(raw, title string) (string, string) {
	u := RedactURL(raw)
	if u == "" {
		return "", ""
	}
	t, _ := clamp(RedactText(title), MaxURLTitle)
	return u, t
}

// sanitize re-applies the guards to a record read back from storage, so a
// record written by anything else, or edited by hand, is shown redacted.
func (r *Record) sanitize() {
	r.URL = RedactURL(r.URL)
	r.URLTitle = RedactText(r.URLTitle)
	r.Title = RedactText(r.Title)
	r.Body = RedactText(r.Body)
	r.Correlation, r.Source, r.Provenance = RedactText(r.Correlation), RedactText(r.Source), RedactText(r.Provenance)
	r.RequestID = onlyRequestID(r.RequestID)
}

func onlyRequestID(s string) string {
	if requestIDPattern.MatchString(s) {
		return s
	}
	return ""
}
