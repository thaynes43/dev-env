// Package summonledger is the create ledger's shape and time rules (DESIGN-064
// 5.9, "The create ledger", N1, and its corrections of 2026-11-12): one
// caller's entry, its charges, how an entry is read and written, when a charge
// counts toward createsPerHour and when it may be pruned. The API's write
// paths (internal/apiserver) and the operator leader's admission
// (internal/controller) share it, so both read one format. It reads and writes
// no object itself.
//
// One operator-owned ConfigMap, Namespace/Name, holds one key per summoning
// caller: its CallerPolicy's name, which is also the spec.caller of its
// sessions. Each key is one JSON document, the caller's entry:
//
//	schema             Schema
//	clock              the latest "now" any write of this entry used
//	charges            each {id, at, attempt, hash, name}
//	urgentRefusals     the count of refused urgent creates, stopping at its maximum
//	lastUrgentRefusal  when the last was refused, or null
//
// A charge binds one dispatch id to its request's hash (mode, lane and the
// requested name) and to the one session name it creates. at is when it was
// counted toward createsPerHour; attempt is the last time a create was about
// to be made for it.
//
// Time. For one entry, "now" is the later of the replica's clock and the
// entry's clock (Entry.Now), and every write stores it back as the clock. A
// charge counts toward createsPerHour while it is younger than ChargeWindow by
// that clock. The assumption, stated whole: replica clocks agree within 5
// minutes, and none steps forward against real time by more than that.
package summonledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	// Namespace and Name are the ledger ConfigMap's coordinates. haynes-ops
	// precreates it and grants the operator get and update on it by name.
	Namespace = "dev-env-system"
	Name      = "dev-env-summon-creates"
	// Schema is the entry's schema member.
	Schema = "dev-env.summon-creates.v1"
	// ChargeWindow is how long a charge counts toward createsPerHour, from its
	// at: 60 minutes and a 5 minute allowance for clocks that differ between
	// replicas.
	ChargeWindow = 65 * time.Minute
	// AttemptHold is how old a charge's last attempt mark must be before that
	// charge may be pruned. It covers the 2 minute create deadline, 5 minutes
	// for clocks that differ and 5 minutes for a forward step. That sum is 12
	// minutes; the hold is 15.
	AttemptHold = 15 * time.Minute
	// MaxEntry bounds one caller's document.
	MaxEntry = 64 << 10
	// maxDepth bounds the nesting the member check walks.
	maxDepth = 32
)

// DispatchIDPattern is spec.summon.dispatchID's schema pattern. A charge whose
// id does not match it is not a charge the API wrote.
var DispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]{0,127}$`)

// requestHashPattern is the request hash's form: 64 lowercase hex.
var requestHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ErrUnreadable is an entry that is present and cannot be read: the API
// refuses a create with 503 and does not overwrite the key, and the leader
// admits nothing from it.
var ErrUnreadable = errors.New("summon create ledger entry is unreadable")

// Charge is one charge. Fields this version does not know are kept as they
// were.
type Charge struct {
	ID      string
	At      time.Time
	Attempt time.Time
	Hash    string
	Name    string
	extra   map[string]json.RawMessage
}

// Entry is one caller's key.
type Entry struct {
	Clock             time.Time
	Charges           []Charge
	UrgentRefusals    int64
	LastUrgentRefusal *time.Time
	extra             map[string]json.RawMessage
}

// Now is "now" for this entry: the later of the replica's clock and the
// entry's clock.
func (e *Entry) Now(replica time.Time) time.Time {
	replica = replica.UTC()
	if e.Clock.After(replica) {
		return e.Clock
	}
	return replica
}

// InWindow counts the charges that still count toward createsPerHour at now.
func (e *Entry) InWindow(now time.Time) int {
	n := 0
	for _, ch := range e.Charges {
		if ChargeCounts(ch, now) {
			n++
		}
	}
	return n
}

// Index is the index of id's charge, or -1.
func (e *Entry) Index(id string) int {
	for i := range e.Charges {
		if e.Charges[i].ID == id {
			return i
		}
	}
	return -1
}

// Binds reports whether the entry holds a charge for id bound to name: the
// leader's admission test (DESIGN-064 5.9, item 741 comment 7228). A charge
// for id bound to another name binds nothing here.
func (e *Entry) Binds(id, name string) bool {
	i := e.Index(id)
	return i >= 0 && e.Charges[i].Name == name
}

// CountUrgentRefusal adds one urgent refusal at now. The count stops at its
// maximum instead of wrapping.
func (e *Entry) CountUrgentRefusal(now time.Time) {
	if e.UrgentRefusals < math.MaxInt64 {
		e.UrgentRefusals++
	}
	at := now
	e.LastUrgentRefusal = &at
}

// ChargeCounts reports whether ch counts toward createsPerHour at now.
func ChargeCounts(ch Charge, now time.Time) bool {
	return now.Sub(ch.At) < ChargeWindow
}

// Prune drops a charge only when it is outside its window, its last attempt
// mark is at least AttemptHold old, and keep does not hold its id. A create
// the client may still send has a mark at most two minutes old by its
// replica's monotonic clock, so the charge is kept while the clock assumption
// holds. A retry after a drop maps to the same id and is charged again: a
// double charge across two hours is acceptable.
//
// keep is the dispatch ids of the sessions that exist and are neither
// admitted nor closed (DESIGN-064 5.9 item 4, item 741 comment 7233): a
// Queued session's charge is what the leader admits it by, so it is kept
// however old it is. nil keeps nothing extra.
func Prune(charges []Charge, now time.Time, keep map[string]bool) []Charge {
	kept := make([]Charge, 0, len(charges))
	for _, ch := range charges {
		if ChargeCounts(ch, now) || now.Sub(ch.Attempt) < AttemptHold || keep[ch.ID] {
			kept = append(kept, ch)
		}
	}
	return kept
}

// Decode reads one caller's key. An absent key is an empty entry. A present
// value that is empty, is not a JSON object, repeats a member at any depth,
// lacks a required member, has a member of the wrong type or repeats a
// charge's id is ErrUnreadable. Unknown members, at the top and in a charge,
// are kept, so a newer replica's field survives an older replica's write.
func Decode(raw string, present bool) (Entry, error) {
	var e Entry
	if !present {
		return e, nil
	}
	if raw == "" || len(raw) > MaxEntry {
		return e, ErrUnreadable
	}
	if err := checkMembersUnique([]byte(raw)); err != nil {
		return e, err
	}
	top, err := decodeObject([]byte(raw))
	if err != nil {
		return e, err
	}
	var schema string
	if err := takeMember(top, "schema", &schema); err != nil || schema != Schema {
		return e, ErrUnreadable
	}
	if err := takeTime(top, "clock", &e.Clock); err != nil {
		return e, err
	}
	var items []json.RawMessage
	if err := takeMember(top, "charges", &items); err != nil {
		return e, err
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		ch, err := decodeCharge(item)
		if err != nil {
			return e, err
		}
		if seen[ch.ID] {
			return e, ErrUnreadable
		}
		seen[ch.ID] = true
		e.Charges = append(e.Charges, ch)
	}
	if err := takeMember(top, "urgentRefusals", &e.UrgentRefusals); err != nil || e.UrgentRefusals < 0 {
		return e, ErrUnreadable
	}
	last, ok := top["lastUrgentRefusal"]
	if !ok {
		return e, ErrUnreadable
	}
	if !bytes.Equal(bytes.TrimSpace(last), []byte("null")) {
		var at time.Time
		if err := takeTime(top, "lastUrgentRefusal", &at); err != nil {
			return e, err
		}
		e.LastUrgentRefusal = &at
	}
	delete(top, "lastUrgentRefusal")
	if len(top) > 0 {
		e.extra = top
	}
	return e, nil
}

func decodeCharge(raw json.RawMessage) (Charge, error) {
	var ch Charge
	fields, err := decodeObject(raw)
	if err != nil {
		return ch, err
	}
	if takeMember(fields, "id", &ch.ID) != nil || !DispatchIDPattern.MatchString(ch.ID) ||
		takeTime(fields, "at", &ch.At) != nil ||
		takeTime(fields, "attempt", &ch.Attempt) != nil ||
		takeMember(fields, "hash", &ch.Hash) != nil || !requestHashPattern.MatchString(ch.Hash) ||
		takeMember(fields, "name", &ch.Name) != nil || len(validation.IsDNS1123Label(ch.Name)) > 0 {
		return ch, ErrUnreadable
	}
	if len(fields) > 0 {
		ch.extra = fields
	}
	return ch, nil
}

// takeMember decodes a required member that is not null into v and removes it
// from obj.
func takeMember(obj map[string]json.RawMessage, name string, v any) error {
	raw, ok := obj[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, v) != nil {
		return ErrUnreadable
	}
	delete(obj, name)
	return nil
}

// takeTime is takeMember for a time that is not zero.
func takeTime(obj map[string]json.RawMessage, name string, at *time.Time) error {
	if err := takeMember(obj, name, at); err != nil || at.IsZero() {
		return ErrUnreadable
	}
	*at = at.UTC()
	return nil
}

func decodeObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, ErrUnreadable
	}
	return obj, nil
}

// checkMembersUnique refuses a document in which any object repeats a member.
// encoding/json keeps the last of two, so without this a second "charges": []
// would silently empty an entry.
func checkMembersUnique(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrUnreadable
	}
	return nil
}

func walkValue(dec *json.Decoder, depth int) error {
	if depth > maxDepth {
		return ErrUnreadable
	}
	tok, err := dec.Token()
	if err != nil {
		return ErrUnreadable
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return ErrUnreadable
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrUnreadable
			}
			seen[name] = true
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrUnreadable
	}
	if _, err := dec.Token(); err != nil {
		return ErrUnreadable
	}
	return nil
}

// Encode writes one caller's key. A document over MaxEntry is ErrUnreadable:
// the write is refused, never truncated.
func Encode(e Entry) (string, error) {
	top := map[string]any{}
	for k, v := range e.extra {
		top[k] = v
	}
	charges := make([]map[string]any, 0, len(e.Charges))
	for _, ch := range e.Charges {
		m := map[string]any{}
		for k, v := range ch.extra {
			m[k] = v
		}
		m["id"] = ch.ID
		m["at"] = ch.At.UTC()
		m["attempt"] = ch.Attempt.UTC()
		m["hash"] = ch.Hash
		m["name"] = ch.Name
		charges = append(charges, m)
	}
	top["schema"] = Schema
	top["clock"] = e.Clock.UTC()
	top["charges"] = charges
	top["urgentRefusals"] = e.UrgentRefusals
	top["lastUrgentRefusal"] = nil
	if e.LastUrgentRefusal != nil {
		top["lastUrgentRefusal"] = e.LastUrgentRefusal.UTC()
	}
	b, err := json.Marshal(top)
	if err != nil {
		return "", err
	}
	if len(b) > MaxEntry {
		return "", ErrUnreadable
	}
	return string(b), nil
}
