package sessionraise

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
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

// SentTime is the signed Sent time for a pending raise, when one verifies.
func SentTime(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) (string, bool) {
	n, ok := raisedSent(entry, notices, pub)
	if !ok {
		return "", false
	}
	return n.SentAt, true
}

// AttemptCount is the highest finished attempt on a verified raise notice.
func AttemptCount(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) int {
	return finishedAttempts(entry, notices, pub)
}
func raisedSent(entry SessionEntry, notices []Notice, pub ed25519.PublicKey) (Notice, bool) {
	var sent *Notice
	for i := range notices {
		n := notices[i]
		if n.ID != entry.ID || n.SessionUID != entry.UID || n.Kind != KindRaise || n.SentAt == "" || !VerifyNotice(n, pub) {
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

// NoticeText is what the keeper says. Title and Words are redacted by the caller.
type NoticeText struct {
	Session       string
	Title         string
	Performer     string
	From          string
	Profile       string
	Superuser     bool
	StartSessions bool
	Words         string
	Where         string
	SaidAt        string
	Effect        string
	TakeBack      bool
	TakenBy       string
	TakenAt       string
}

// Message is the Pushover title and body. redact runs on the title and on
// Tom's words. The command in the body is not redacted.
func Message(in NoticeText, redact func(string) string) (string, string) {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	if in.TakeBack {
		title := redact("[dev-env] raise taken back: " + in.Session)
		body := in.Session + " (" + redact(in.Title) + ") was taken back by " + in.TakenBy + " at " + in.TakenAt + "."
		return title, body
	}
	title := redact("[dev-env] raise: " + in.Session)
	body := in.Session + " (" + redact(in.Title) + ") was raised by " + in.Performer
	if in.Profile != "" {
		body += " from " + in.From + " to " + in.Profile
	}
	if in.Superuser {
		body += ". It will run as the superuser account"
	}
	if in.StartSessions {
		body += ". It may start sessions one level deeper"
	}
	body += ". Tom said, " + in.Where + ", at " + in.SaidAt + ": " + redact(in.Words) + "."
	body += " It takes effect at " + in.Effect + ", two minutes after this notice is accepted, unless taken back."
	body += " To stop it: agent-run raise " + in.Session + " --take-back."
	return title, body
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
