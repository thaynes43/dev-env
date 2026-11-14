package sessionraise

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
)

const (
	OutcomeSent      = "Sent"
	OutcomeFailed    = "Failed"
	OutcomeAmbiguous = "Ambiguous"
	OutcomeReserved  = "reserved"
)

// Notice is one keeper outcome for one record and one kind. The signature
// covers every field but Sig. SentAt stays once a Sent has been signed, so
// a later attempt cannot erase it without the keeper's key.
type Notice struct {
	ID         string `json:"id"`
	SessionUID string `json:"sessionUID"`
	Kind       string `json:"kind"`
	Attempt    int    `json:"attempt"`
	Outcome    string `json:"outcome"`
	At         string `json:"at"`
	SentAt     string `json:"sentAt"`
	KeyID      string `json:"keyId"`
	Sig        string `json:"sig"`
}

func (n Notice) signedFields() []field {
	return []field{
		{"at", n.At},
		{"attempt", n.Attempt},
		{"id", n.ID},
		{"keyId", n.KeyID},
		{"kind", n.Kind},
		{"outcome", n.Outcome},
		{"sentAt", n.SentAt},
		{"sessionUID", n.SessionUID},
	}
}

// Canonical is the notice JSON without its signature.
func (n Notice) Canonical() ([]byte, error) {
	var b bytes.Buffer
	if err := writeObject(&b, n.signedFields()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// SignNotice signs n with the keeper key just read back.
func SignNotice(n Notice, priv ed25519.PrivateKey) (Notice, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return Notice{}, errors.New("raise notice key has the wrong length")
	}
	pub := priv.Public().(ed25519.PublicKey)
	n.KeyID = KeyID(pub)
	n.Sig = ""
	body, err := n.Canonical()
	if err != nil {
		return Notice{}, err
	}
	sig := ed25519.Sign(priv, SigningMessage(PrefixNotice, body))
	n.Sig = base64.StdEncoding.EncodeToString(sig)
	return n, nil
}

// VerifyNotice reports whether the keeper signed this outcome.
func VerifyNotice(n Notice, pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize || n.KeyID != KeyID(pub) || n.ID == "" {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(n.Sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	body, err := n.Canonical()
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, SigningMessage(PrefixNotice, body), raw)
}

// Action is what the operator does with a pending row.
type Action int

const (
	// ActionNone leaves the row pending.
	ActionNone Action = iota
	// ActionActivate moves pending to active.
	ActionActivate
	// ActionUnannounce moves pending to unannounced.
	ActionUnannounce
)

// Activation decides one pending row. A Sent that does not verify, a Sent
// for a take-back, and a row that is not pending do nothing. A verified Sent
// activates only once EffectDelay has passed. Twelve finished attempts with
// no verified Sent unannounce. now is the operator clock.
func Activation(now time.Time, entry SessionEntry, notices []Notice, pub ed25519.PublicKey) Action {
	if entry.State != StatePending || entry.ID == "" {
		return ActionNone
	}
	sent, ok := raisedSent(entry, notices, pub)
	finished := finishedAttempts(entry, notices, pub)
	if aged := agedReservations(now, entry, notices, pub); aged > finished {
		finished = aged
	}
	if ok {
		at, err := time.Parse(time.RFC3339, sent.SentAt)
		if err != nil {
			return ActionNone
		}
		if !now.Before(at.Add(EffectDelay)) {
			return ActionActivate
		}
		return ActionNone
	}
	if finished >= NoticeAttempts {
		return ActionUnannounce
	}
	return ActionNone
}

// SentBeforePerformed reports a verified Sent whose sentAt is earlier than
// the raise. The operator leaves that row pending.
func SentBeforePerformed(sentAt, performedAt string) bool {
	return sentAt != "" && performedAt != "" && sentAt < performedAt
}

// SentTime is the signed Sent time for a pending raise, when one verifies.
func SentTime(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) (string, bool) {
	n, ok := raisedSent(entry, notices, pub)
	if !ok {
		return "", false
	}
	return n.SentAt, true
}

// AttemptCount is the larger of the finished attempts and the aged
// reservations, the same count Activation uses. now is the operator clock.
func AttemptCount(now time.Time, entry SessionEntry, notices []Notice, pub ed25519.PublicKey) int {
	finished := finishedAttempts(entry, notices, pub)
	if aged := agedReservations(now, entry, notices, pub); aged > finished {
		return aged
	}
	return finished
}
func raisedSent(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) (Notice, bool) {
	var sent *Notice
	for i := range notices {
		n := notices[i]
		if n.ID != entry.ID || n.SessionUID != entry.UID || n.Kind != KindRaise || n.Outcome != OutcomeSent || n.SentAt == "" || !VerifyNotice(n, pub) {
			continue
		}
		if sent == nil || n.SentAt < sent.SentAt {
			copy := n
			sent = &copy
		}
	}
	if sent == nil {
		return Notice{}, false
	}
	return *sent, true
}

func finishedAttempts(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) int {
	finished := 0
	for i := range notices {
		n := notices[i]
		if n.ID != entry.ID || n.SessionUID != entry.UID || n.Kind != KindRaise || !VerifyNotice(n, pub) {
			continue
		}
		if n.Outcome == OutcomeSent || n.Outcome == OutcomeFailed || n.Outcome == OutcomeAmbiguous {
			if n.Attempt > finished {
				finished = n.Attempt
			}
		}
	}
	return finished
}

// agedReservations is the attempt on a verified reservation whose time is
// at least NoticeRetry ago. That reservation had no outcome, so it counts
// as one Ambiguous attempt and the operator can unannounce at twelve.
func agedReservations(now time.Time, entry SessionEntry, notices []Notice, pub ed25519.PublicKey) int {
	finished := 0
	for i := range notices {
		n := notices[i]
		if n.ID != entry.ID || n.SessionUID != entry.UID || n.Kind != KindRaise || n.Outcome != OutcomeReserved || !VerifyNotice(n, pub) {
			continue
		}
		at, err := time.Parse(time.RFC3339, n.At)
		if err != nil || now.Before(at.Add(NoticeRetry)) {
			continue
		}
		if n.Attempt > finished {
			finished = n.Attempt
		}
	}
	return finished
}

// NoticeBodyMax is the most bytes a phone notice body may carry.
const NoticeBodyMax = 1024

// noticeFullTime is a full-length whole-second RFC 3339 time, with a numeric
// offset. The keeper's clock writes the shorter UTC form. Admission uses this
// longer one, so a raise that fits here still fits when it is sent.
const noticeFullTime = "2006-01-02T15:04:05-07:00"

// errProfileNotice is the refusal for a raise that names a profile. Slice (b)
// writes that notice's text. Until then Message sends nothing for it.
var errProfileNotice = errors.New("a profile raise has no notice text until slice (b) writes it")

// NoticeText is what the keeper says. Message redacts the title and the words.
type NoticeText struct {
	Session   string
	Title     string
	Performer string
	Profile   string
	Words     string
	Where     string
	SaidAt    string
	Effect    string
	TakeBack  bool
	TakenBy   string
	TakenAt   string
}

// Message is the Pushover title and body. redact runs on the session title
// and on Tom's words. The command in the body is not redacted. The session
// title is the performer's text, so it is quoted and follows the take-back
// command. An empty title leaves that sentence out. A record that names a
// profile is refused: slice (b) has to write that text before a notice can
// go out. A take-back names its caller and its time only when the caller has
// the caller form and the time is RFC 3339; otherwise the body is a fixed
// sentence and carries neither field.
func Message(in NoticeText, redact func(string) string) (string, string, error) {
	if in.Profile != "" {
		return "", "", errProfileNotice
	}
	if redact == nil {
		redact = func(s string) string { return s }
	}
	shownTitle := foldQuotes(redact(in.Title))
	if in.TakeBack {
		title := redact("[dev-env] raise taken back: " + in.Session)
		body := in.Session + " was taken back."
		if callerForm(in.TakenBy) && rfc3339Time(in.TakenAt) {
			body = in.Session + " was taken back by " + in.TakenBy + " at " + in.TakenAt + "."
		}
		if shownTitle != "" {
			body += ` Its title is "` + shownTitle + `".`
		}
		return title, body, nil
	}
	title := redact("[dev-env] raise: " + in.Session)
	body := in.Session + " was raised by " + in.Performer + ". It may start sessions one level deeper. It takes effect at " + in.Effect + ", two minutes after this notice is accepted, unless taken back. To stop it: agent-run raise " + in.Session + " --take-back."
	if shownTitle != "" {
		body += ` Its title is "` + shownTitle + `".`
	}
	shown := foldQuotes(redact(in.Words))
	body += " " + in.Performer + ` says you said: "` + shown + `" (` + in.Where + ", " + in.SaidAt + ")."
	return title, body, nil
}

// NoticeFits reports whether the raise notice for these request fields stays
// within NoticeBodyMax. The title is 48 two-byte runes, the longest a session
// title can be, and the effect time is a full-length RFC 3339 time. name,
// performer, where, saidAt and words are the request's own.
func NoticeFits(name, performer, where, saidAt, words string, redact func(string) string) (bool, error) {
	_, body, err := Message(NoticeText{
		Session: name, Title: strings.Repeat("\u00e9", 48), Performer: performer,
		Words: words, Where: where, SaidAt: saidAt, Effect: noticeFullTime,
	}, redact)
	if err != nil {
		return false, err
	}
	return len(body) <= NoticeBodyMax, nil
}

// callerForm is a kind, a slash and a name. Each part is a DNS label.
// A ServiceAccount name is itself namespace/name, so more than one slash is
// a name, not a second kind.
func callerForm(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if !dnsLabel(part) {
			return false
		}
	}
	return true
}

func dnsLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(s)-1:
		default:
			return false
		}
	}
	return true
}

func rfc3339Time(s string) bool {
	_, err := time.Parse(time.RFC3339, s)
	return err == nil
}

// foldQuotes turns the runes listed in quoteRune into an apostrophe, in the
// words and in the title, so neither can close the quotation the notice
// puts around them.
func foldQuotes(s string) string {
	return strings.Map(func(r rune) rune {
		if quoteRune(r) {
			return '\''
		}
		return r
	}, s)
}

func quoteRune(r rune) bool {
	switch r {
	case '"', '\u201E', '\u201F', '\uFF02', '\u301D', '\u301E', '\u301F', '\u00AB', '\u00BB',
		'\u02BA', '\u02DD', '\u02EE', '\u2033', '\u2036', '\u3003', '\u05F4',
		'\uFF62', '\uFF63':
		return true
	}
	if r >= '\u275D' && r <= '\u2760' || r >= '\u300C' && r <= '\u300F' || r >= '\uFE41' && r <= '\uFE44' || r >= '\U0001F676' && r <= '\U0001F678' {
		return true
	}
	return unicode.Is(unicode.Pi, r) || unicode.Is(unicode.Pf, r)
}

// NoticeLedger is the keeper's map of the latest signed outcome per record
// and kind. One row, not a growing history, so the ConfigMap stays bounded.
// A Sent's sentAt stays inside the next signature.
type NoticeLedger struct {
	Items []Notice `json:"items"`
}

// FindNotice is the latest row for id and kind.
func (l *NoticeLedger) Find(id, kind string) *Notice {
	if l == nil {
		return nil
	}
	for i := range l.Items {
		if l.Items[i].ID == id && l.Items[i].Kind == kind {
			return &l.Items[i]
		}
	}
	return nil
}

// Put replaces the row for id and kind.
func (l *NoticeLedger) Put(n Notice) {
	if cur := l.Find(n.ID, n.Kind); cur != nil {
		*cur = n
		return
	}
	l.Items = append(l.Items, n)
}

// MarshalNotice encodes the ledger.
func (l *NoticeLedger) Marshal() ([]byte, error) {
	if l == nil {
		return []byte(`{"items":[]}`), nil
	}
	return json.Marshal(l)
}

// UnmarshalNotice reads a notice ledger. An empty body is empty.
func UnmarshalNotice(raw []byte) (*NoticeLedger, error) {
	l := &NoticeLedger{}
	if len(raw) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(raw, l); err != nil {
		return nil, err
	}
	return l, nil
}
