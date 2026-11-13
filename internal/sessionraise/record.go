package sessionraise

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

const (
	// Version is the record's v field.
	Version = 1
	// MaxEncoded is the most the annotation may hold, measured after encoding.
	MaxEncoded = 4096
	// PrefixRecord is the domain the operator signs, then a newline, then the
	// canonical record with the signature left out.
	PrefixRecord = "dev-env/session-raise/v1/record"
	// PrefixNotice is the domain the keeper signs for one notice outcome.
	PrefixNotice = "dev-env/session-raise/v1/notice"

	StatePending     = "pending"
	StateActive      = "active"
	StateUnannounced = "unannounced"
	StateTakenBack   = "taken-back"

	WherePhone = "phone-prompt"
	WhereChat  = "chat"
	WhereOwn   = "own-call"

	KindRaise    = "raise"
	KindTakeBack = "take-back"

	// Ignore reasons are the phrases a view shows. Empty means the record counts.
	IgnoreUID       = "UID mismatch"
	IgnoreSignature = "signature mismatch"
	IgnoreWords     = "words hash mismatch"
	IgnoreLedger    = "ledger mismatch"
)

// ErrTooBig is a record whose encoding is over the encoded limit. Spend nothing.
var ErrTooBig = errors.New("raise record is over 4 KiB")

// encodedLimit is MaxEncoded except in the route test that injects a lower bound.
var encodedLimit = MaxEncoded

// SetEncodedLimitForTest lowers the annotation size check. The returned
// function puts the limit back. Tests do not run it in parallel.
func SetEncodedLimitForTest(n int) func() {
	prev := encodedLimit
	encodedLimit = n
	return func() { encodedLimit = prev }
}

// Owner is Tom's words as the performing agent stated them.
type Owner struct {
	Words     string `json:"words"`
	WordsHash string `json:"wordsHash"`
	Where     string `json:"where"`
	Session   string `json:"session"`
	SaidAt    string `json:"saidAt"`
	Ref       string `json:"ref"`
}

// Record is one raise annotation. The signature covers every field but Sig.
type Record struct {
	V             int    `json:"v"`
	ID            string `json:"id"`
	SessionUID    string `json:"sessionUID"`
	State         string `json:"state"`
	Profile       string `json:"profile"`
	FromProfile   string `json:"fromProfile"`
	StartSessions bool   `json:"startSessions"`
	Depth         string `json:"depth"`
	Owner         Owner  `json:"owner"`
	PerformedBy   string `json:"performedBy"`
	PerformedAt   string `json:"performedAt"`
	NoticeSentAt  string `json:"noticeSentAt"`
	ActiveFrom    string `json:"activeFrom"`
	TakenBackBy   string `json:"takenBackBy"`
	TakenBackAt   string `json:"takenBackAt"`
	KeyID         string `json:"keyId"`
	Sig           string `json:"sig"`
}

// RecordID is the first 16 hex digits of SHA-256 over the session UID,
// performedAt and the words hash, each on its own line. Those three are the
// only inputs, so two raises of the same words at the same second collide
// and the second is the idempotent one.
func RecordID(sessionUID, performedAt, wordsHash string) string {
	sum := sha256.Sum256([]byte(sessionUID + "\n" + performedAt + "\n" + wordsHash))
	return hex.EncodeToString(sum[:])[:16]
}

// KeyID is the first 16 hex digits of SHA-256 of the raw public key.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}

type field struct {
	key string
	val any
}

func (r Record) signedFields() []field {
	return []field{
		{"activeFrom", r.ActiveFrom},
		{"depth", r.Depth},
		{"fromProfile", r.FromProfile},
		{"id", r.ID},
		{"keyId", r.KeyID},
		{"noticeSentAt", r.NoticeSentAt},
		{"owner", []field{
			{"ref", r.Owner.Ref},
			{"saidAt", r.Owner.SaidAt},
			{"session", r.Owner.Session},
			{"where", r.Owner.Where},
			{"words", r.Owner.Words},
			{"wordsHash", r.Owner.WordsHash},
		}},
		{"performedAt", r.PerformedAt},
		{"performedBy", r.PerformedBy},
		{"profile", r.Profile},
		{"sessionUID", r.SessionUID},
		{"startSessions", r.StartSessions},
		{"state", r.State},
		{"takenBackAt", r.TakenBackAt},
		{"takenBackBy", r.TakenBackBy},
		{"v", r.V},
	}
}

// Canonical is the record JSON with every field except the signature, keys
// sorted, no spaces, and no HTML escaping.
func (r Record) Canonical() ([]byte, error) {
	var b bytes.Buffer
	if err := writeObject(&b, r.signedFields()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Encode is the annotation value: the canonical record plus its signature.
func (r Record) Encode() ([]byte, error) {
	fields := append(r.signedFields(), field{"sig", r.Sig})
	var b bytes.Buffer
	if err := writeObject(&b, fields); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// TooBig reports whether Encode is over MaxEncoded.
func (r Record) TooBig() (bool, error) {
	raw, err := r.Encode()
	if err != nil {
		return false, err
	}
	return len(raw) > encodedLimit, nil
}

// SigningMessage is the domain prefix, a newline, and the canonical body.
func SigningMessage(prefix string, body []byte) []byte {
	msg := make([]byte, 0, len(prefix)+1+len(body))
	msg = append(msg, prefix...)
	msg = append(msg, '\n')
	msg = append(msg, body...)
	return msg
}

// Sign fills KeyID and Sig from priv. priv must be the key just read back
// from the Secret; this function does not generate one.
func Sign(rec Record, priv ed25519.PrivateKey) (Record, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return Record{}, errors.New("raise signing key has the wrong length")
	}
	pub := priv.Public().(ed25519.PublicKey)
	rec.V = Version
	rec.KeyID = KeyID(pub)
	rec.Sig = ""
	body, err := rec.Canonical()
	if err != nil {
		return Record{}, err
	}
	sig := ed25519.Sign(priv, SigningMessage(PrefixRecord, body))
	rec.Sig = base64.StdEncoding.EncodeToString(sig)
	return rec, nil
}

// Verify reports whether Sig is the operator's signature over Canonical.
// A wrong key id fails, so a record cannot name a key it was not signed with.
func Verify(rec Record, pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize || rec.KeyID != KeyID(pub) {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(rec.Sig)
	if err != nil || len(raw) != ed25519.SignatureSize {
		return false
	}
	body, err := rec.Canonical()
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, SigningMessage(PrefixRecord, body), raw)
}

// Parse reads an annotation. Unknown fields and trailing data are refused,
// so a field that is not signed cannot ride along.
func Parse(raw string) (Record, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var rec Record
	if err := dec.Decode(&rec); err != nil {
		return Record{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return Record{}, errors.New("raise record has trailing data")
	}
	// The bytes must be exactly Encode of the record just read. A duplicate
	// key or a case-folded key decodes, then fails this comparison.
	canonical, err := rec.Encode()
	if err != nil || string(canonical) != raw {
		return Record{}, errors.New("raise record is not its canonical encoding")
	}
	return rec, nil
}

// Counts is the only place a record is accepted. It counts when the session
// UID matches, the words hash matches the words, the signature verifies, and
// entry is the ledger row for that id and state. A nil key or a nil entry
// does not count. The reason is empty only when it counts.
func Counts(rec Record, sessionUID string, pub ed25519.PublicKey, entry *SessionEntry) string {
	if sessionUID == "" || rec.SessionUID != sessionUID {
		return IgnoreUID
	}
	if rec.Owner.WordsHash == "" || rec.Owner.WordsHash != WordsHash(rec.Owner.Words) {
		return IgnoreWords
	}
	if !Verify(rec, pub) {
		return IgnoreSignature
	}
	if entry == nil || entry.ID != rec.ID || entry.State != rec.State || entry.UID != sessionUID {
		return IgnoreLedger
	}
	switch rec.State {
	case StatePending, StateActive, StateUnannounced, StateTakenBack:
	default:
		return IgnoreLedger
	}
	return ""
}

func writeObject(b *bytes.Buffer, fields []field) error {
	sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
	b.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeString(b, f.key); err != nil {
			return err
		}
		b.WriteByte(':')
		if err := writeValue(b, f.val); err != nil {
			return err
		}
	}
	b.WriteByte('}')
	return nil
}

func writeValue(b *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case string:
		return writeString(b, t)
	case bool:
		if t {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
		return nil
	case int:
		body, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(body)
		return nil
	case []field:
		return writeObject(b, t)
	default:
		return errors.New("raise record has a field that cannot be encoded")
	}
}

func writeString(b *bytes.Buffer, s string) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	b.Write(raw)
	return nil
}
