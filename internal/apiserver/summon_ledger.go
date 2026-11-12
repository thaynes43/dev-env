package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The create ledger (DESIGN-064 5.9, "The create ledger", N1, and its
// correction of 2026-11-12). One operator-owned ConfigMap,
// summonLedgerNamespace/summonLedgerName, holds one key per summoning caller:
// its policy's name. Each key is one JSON document, the caller's entry:
//
//	schema             summonLedgerSchema
//	clock              the latest "now" any write of this entry used
//	charges            each {id, at, attempt, hash, name}
//	urgentRefusals     the count of refused urgent creates, stopping at its maximum
//	lastUrgentRefusal  when the last was refused, or null
//
// A charge binds one dispatch id to its request's hash (mode, lane and the
// requested name) and to the one session name it creates. at is when it was
// counted toward createsPerHour; attempt is the last time a create was about
// to be made for it. GitOps precreates the object with no data; the operator
// reads it uncached and writes it only by Update with the resourceVersion it
// read. It never creates or deletes it.
//
// Time. For one entry, "now" is the later of the replica's clock and the
// entry's clock, and every write stores it back as the clock. A charge counts
// toward createsPerHour while it is younger than summonChargeWindow by that
// clock: 60 minutes and 5 for replica clocks that differ. The limit is on
// charges, not creates: at most createsPerHour charges in any
// summonChargeWindow by the entry's clock. A create may follow its charge at
// any time inside that charge's window, since a retry reuses it, so two
// creates can be closer together than their charges. The assumption, stated
// whole: replica clocks agree within 5 minutes, and none steps forward
// against real time by more than that. A larger forward step expires charges
// early by the excess and can make an attempt mark look old. No time
// authority is built here.
//
// Every session create attempt is preceded by its own ledger write, the mark
// (markSummonAttempt): a compare-and-set that succeeds only while the charge
// binds the id to the name about to be created, and otherwise returns the
// binding, which the dispatch follows. The mark, the first write
// (bindSummonCharge) and every rebind apply one window rule: an attempt mark
// on a charge inside its window, or a re-charge, counted again and subject to
// the hour, of one outside it. Nothing creates on a charge it only read. The
// create, and the read after its AlreadyExists, run under one context that is
// done summonCreateDeadline after the mark's monotonic reading. One request
// makes at most summonMaxCreateAttempts session creates. A charge is pruned
// only when it is summonChargeWindow old and its last attempt mark is at
// least summonAttemptHold old, whether or not a session of its id exists.
//
// Limits of this slice (the correction's amendment). A client-side deadline
// does not stop a request the API server has already received, nor a process
// frozen between its last check and the send; a mark does not stop a replica
// paused between it and its create. So two windows remain. (1) A create that
// lands after its charge was pruned. The charge is already summonChargeWindow
// old. A marks X and sends its create inside the deadline; the API server has
// not persisted it when another replica's write, summonAttemptHold after the
// mark by the entry's clock, prunes X; the session is then persisted with no
// charge. A process frozen before it sends, or between the mark and the send
// for longer than the hold allows, is this same window. (2) A stale destination: A marks
// X for N and is paused before its create; B marks X for N, meets another
// session on N, rebinds X to N-2 and creates N-2; that other session is
// deleted, freeing N; A resumes inside its deadline and creates N, so X has
// two sessions. That no summoned session gets a pod unless the ledger holds
// its id bound to its name, and so that no id runs two sessions, is enforced
// at admission by the leader in story 741, not here.
const (
	summonLedgerSchema       = "dev-env.summon-creates.v1"
	summonLedgerFieldManager = "dev-env-summon-creates"
	// summonChargeWindow is how long a charge counts toward createsPerHour,
	// from its at: 60 minutes and a 5 minute allowance for clocks that differ
	// between replicas.
	summonChargeWindow = 65 * time.Minute
	// summonAttemptHold is how old a charge's last attempt mark must be before
	// that charge may be pruned, whether or not a session of its id exists.
	// It covers the 2 minute create deadline, 5 minutes for clocks that differ
	// and 5 minutes for a forward step. That sum is 12 minutes; the hold is 15.
	summonAttemptHold = 15 * time.Minute
	// summonCreateDeadline is how long after the monotonic reading of the mark
	// before it a session create, and the read after its AlreadyExists, may
	// run: one absolute deadline, carried as one context.
	summonCreateDeadline = 2 * time.Minute
	// summonMaxCreateAttempts is how many session creates one request may
	// make: 99 names, two attempts each, and two to spare. Past this the
	// request answers 503, a charged failure, whatever the ledger does meanwhile.
	summonMaxCreateAttempts = 200
	// summonLedgerAttempts bounds the optimistic-lock retries of one write.
	summonLedgerAttempts = 5
	// summonLedgerMaxEntry bounds one caller's document.
	summonLedgerMaxEntry = 64 << 10
	// summonLedgerMaxDepth bounds the nesting the member check walks.
	summonLedgerMaxDepth = 32
)

// summonDispatchIDPattern is spec.summon.dispatchID's schema pattern. A
// charge whose id does not match it is not a charge this API wrote.
var summonDispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]{0,127}$`)

// summonRequestHashPattern is summonRequestHash's form: 64 lowercase hex.
var summonRequestHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var errSummonLedgerUnreadable = errors.New("summon create ledger entry is unreadable")

// summonCharge is one charge. Fields this version does not know are kept as
// they were.
type summonCharge struct {
	ID      string
	At      time.Time
	Attempt time.Time
	Hash    string
	Name    string
	extra   map[string]json.RawMessage
}

// summonLedgerEntry is one caller's key.
type summonLedgerEntry struct {
	Clock             time.Time
	Charges           []summonCharge
	UrgentRefusals    int64
	LastUrgentRefusal *time.Time
	extra             map[string]json.RawMessage
}

// now is "now" for this entry: the later of the replica's clock and the
// entry's clock.
func (e *summonLedgerEntry) now(replica time.Time) time.Time {
	replica = replica.UTC()
	if e.Clock.After(replica) {
		return e.Clock
	}
	return replica
}

// inWindow counts the charges that still count toward createsPerHour at now.
func (e *summonLedgerEntry) inWindow(now time.Time) int {
	n := 0
	for _, ch := range e.Charges {
		if summonChargeCounts(ch, now) {
			n++
		}
	}
	return n
}

// charge is the index of id's charge, or -1.
func (e *summonLedgerEntry) charge(id string) int {
	for i := range e.Charges {
		if e.Charges[i].ID == id {
			return i
		}
	}
	return -1
}

// countUrgentRefusal adds one urgent refusal at now. The count stops at its
// maximum instead of wrapping.
func (e *summonLedgerEntry) countUrgentRefusal(now time.Time) {
	if e.UrgentRefusals < math.MaxInt64 {
		e.UrgentRefusals++
	}
	at := now
	e.LastUrgentRefusal = &at
}

func summonChargeCounts(ch summonCharge, now time.Time) bool {
	return now.Sub(ch.At) < summonChargeWindow
}

// decodeSummonLedgerEntry reads one caller's key. An absent key is an empty
// entry. A present value that is empty, is not a JSON object, repeats a member
// at any depth, lacks a required member, has a member of the wrong type or
// repeats a charge's id is errSummonLedgerUnreadable: the create is then
// refused with 503, never admitted without a charge, and the key is not
// overwritten. Unknown members, at the top and in a charge, are kept, so a
// newer replica's field survives an older replica's write.
func decodeSummonLedgerEntry(raw string, present bool) (summonLedgerEntry, error) {
	var e summonLedgerEntry
	if !present {
		return e, nil
	}
	if raw == "" || len(raw) > summonLedgerMaxEntry {
		return e, errSummonLedgerUnreadable
	}
	if err := checkLedgerMembersUnique([]byte(raw)); err != nil {
		return e, err
	}
	top, err := decodeLedgerObject([]byte(raw))
	if err != nil {
		return e, err
	}
	var schema string
	if err := takeLedgerMember(top, "schema", &schema); err != nil || schema != summonLedgerSchema {
		return e, errSummonLedgerUnreadable
	}
	if err := takeLedgerTime(top, "clock", &e.Clock); err != nil {
		return e, err
	}
	var items []json.RawMessage
	if err := takeLedgerMember(top, "charges", &items); err != nil {
		return e, err
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		ch, err := decodeSummonCharge(item)
		if err != nil {
			return e, err
		}
		if seen[ch.ID] {
			return e, errSummonLedgerUnreadable
		}
		seen[ch.ID] = true
		e.Charges = append(e.Charges, ch)
	}
	if err := takeLedgerMember(top, "urgentRefusals", &e.UrgentRefusals); err != nil || e.UrgentRefusals < 0 {
		return e, errSummonLedgerUnreadable
	}
	last, ok := top["lastUrgentRefusal"]
	if !ok {
		return e, errSummonLedgerUnreadable
	}
	if !bytes.Equal(bytes.TrimSpace(last), []byte("null")) {
		var at time.Time
		if err := takeLedgerTime(top, "lastUrgentRefusal", &at); err != nil {
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

func decodeSummonCharge(raw json.RawMessage) (summonCharge, error) {
	var ch summonCharge
	fields, err := decodeLedgerObject(raw)
	if err != nil {
		return ch, err
	}
	if takeLedgerMember(fields, "id", &ch.ID) != nil || !summonDispatchIDPattern.MatchString(ch.ID) ||
		takeLedgerTime(fields, "at", &ch.At) != nil ||
		takeLedgerTime(fields, "attempt", &ch.Attempt) != nil ||
		takeLedgerMember(fields, "hash", &ch.Hash) != nil || !summonRequestHashPattern.MatchString(ch.Hash) ||
		takeLedgerMember(fields, "name", &ch.Name) != nil || len(validation.IsDNS1123Label(ch.Name)) > 0 {
		return ch, errSummonLedgerUnreadable
	}
	if len(fields) > 0 {
		ch.extra = fields
	}
	return ch, nil
}

// takeLedgerMember decodes a required member that is not null into v and
// removes it from obj.
func takeLedgerMember(obj map[string]json.RawMessage, name string, v any) error {
	raw, ok := obj[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, v) != nil {
		return errSummonLedgerUnreadable
	}
	delete(obj, name)
	return nil
}

// takeLedgerTime is takeLedgerMember for a time that is not zero.
func takeLedgerTime(obj map[string]json.RawMessage, name string, at *time.Time) error {
	if err := takeLedgerMember(obj, name, at); err != nil || at.IsZero() {
		return errSummonLedgerUnreadable
	}
	*at = at.UTC()
	return nil
}

func decodeLedgerObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errSummonLedgerUnreadable
	}
	return obj, nil
}

// checkLedgerMembersUnique refuses a document in which any object repeats a
// member. encoding/json keeps the last of two, so without this a second
// "charges": [] would silently empty an entry.
func checkLedgerMembersUnique(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := walkLedgerValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errSummonLedgerUnreadable
	}
	return nil
}

func walkLedgerValue(dec *json.Decoder, depth int) error {
	if depth > summonLedgerMaxDepth {
		return errSummonLedgerUnreadable
	}
	tok, err := dec.Token()
	if err != nil {
		return errSummonLedgerUnreadable
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
				return errSummonLedgerUnreadable
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errSummonLedgerUnreadable
			}
			seen[name] = true
			if err := walkLedgerValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walkLedgerValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errSummonLedgerUnreadable
	}
	if _, err := dec.Token(); err != nil {
		return errSummonLedgerUnreadable
	}
	return nil
}

func encodeSummonLedgerEntry(e summonLedgerEntry) (string, error) {
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
	top["schema"] = summonLedgerSchema
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
	if len(b) > summonLedgerMaxEntry {
		return "", errSummonLedgerUnreadable
	}
	return string(b), nil
}

// pruneSummonCharges drops a charge only when it is outside its window and its
// last attempt mark is at least summonAttemptHold old, whether or not a
// session of its id exists. A create the client may still send has a mark at
// most summonCreateDeadline old by its replica's monotonic clock, so the
// charge is kept while the clock assumption holds. A create the API server
// received and persists later than the hold allows is the first stated limit
// (see the top of this file). A retry after a drop maps to the same id and is
// charged again: a double charge across two hours is acceptable.
func pruneSummonCharges(charges []summonCharge, now time.Time) []summonCharge {
	kept := make([]summonCharge, 0, len(charges))
	for _, ch := range charges {
		if summonChargeCounts(ch, now) || now.Sub(ch.Attempt) < summonAttemptHold {
			kept = append(kept, ch)
		}
	}
	return kept
}

// summonLedgerUnavailable is the refusal when the ledger cannot be read or
// written. A summoning create then makes no create at all: every create
// attempt follows a ledger write naming its id and name.
func summonLedgerUnavailable(why string) error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
		"the summon create ledger %s/%s %s; no session was created (DESIGN-064 5.9)", summonLedgerNamespace, summonLedgerName, why)
}

// summonLedgerKey is the caller's key in the ledger: its policy's name.
func summonLedgerKey(c *caller) string {
	return c.policy.Name
}

// summonSessions is an uncached read of the sessions a ledger write decides
// on: those carrying a dispatch id, by that id, and every name held.
type summonSessions struct {
	byID  map[string]*v1alpha1.AgentSession
	names map[string]bool
}

func (s *Server) readSummonSessions(ctx context.Context) (summonSessions, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return summonSessions{}, err
	}
	out := summonSessions{
		byID:  make(map[string]*v1alpha1.AgentSession, len(list.Items)),
		names: make(map[string]bool, len(list.Items)),
	}
	for i := range list.Items {
		it := &list.Items[i]
		out.names[it.Name] = true
		if it.Spec.Summon != nil && it.Spec.Summon.DispatchID != "" {
			out.byID[it.Spec.Summon.DispatchID] = it
		}
	}
	return out, nil
}

// SummonClock is the clock of a summoning create's deadline.
type SummonClock interface {
	// Now is a reading of a monotonic clock.
	Now() time.Time
	// WithDeadline is parent, done at deadline by this clock.
	WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc)
}

// processClock is the process's own: time.Now keeps its monotonic reading
// through Add, and context.WithDeadline measures against it.
type processClock struct{}

func (processClock) Now() time.Time { return time.Now() }

func (processClock) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(parent, deadline)
}

// summonClock is the clock the create deadline is measured on.
func (s *Server) summonClock() SummonClock {
	if s.SummonClock != nil {
		return s.SummonClock
	}
	return processClock{}
}

// readSummonLedger reads the caller's entry uncached, with the ConfigMap it
// is in. Any fault is the 503 a create answers.
func (s *Server) readSummonLedger(ctx context.Context, c *caller) (summonLedgerEntry, *corev1.ConfigMap, error) {
	if s.Live == nil || s.Client == nil {
		return summonLedgerEntry{}, nil, summonLedgerUnavailable("cannot be read")
	}
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: summonLedgerNamespace, Name: summonLedgerName}, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			s.Log.Error(err, "summon create ledger read")
		}
		return summonLedgerEntry{}, nil, summonLedgerUnavailable("cannot be read")
	}
	if !cm.DeletionTimestamp.IsZero() || (cm.Immutable != nil && *cm.Immutable) || cm.ResourceVersion == "" {
		return summonLedgerEntry{}, nil, summonLedgerUnavailable("cannot be written")
	}
	raw, present := cm.Data[summonLedgerKey(c)]
	entry, err := decodeSummonLedgerEntry(raw, present)
	if err != nil {
		s.Log.Info("summon create ledger entry unreadable", "caller", c.String())
		return summonLedgerEntry{}, nil, summonLedgerUnavailable("has an entry for this caller that cannot be read")
	}
	return entry, &cm, nil
}

// updateSummonLedger reads the ledger and the sessions uncached, lets decide
// change the caller's entry at the entry's now, and writes it back with the
// resourceVersion it read. A conflict re-reads and decides again, so a
// decision always rests on the ledger it is written over: of two replicas
// racing for a slot, one update wins and the loser decides again on the
// ledger with the winner's charge. decide returns whether to write. Every
// write stores the entry's now as its clock and drops the charges the ageing
// rule lets go; nothing else ages the ledger. It returns the monotonic
// reading taken before the written decision was made, or the zero time when
// nothing was written. A mark's reading starts its create's deadline.
func (s *Server) updateSummonLedger(ctx context.Context, c *caller, decide func(e *summonLedgerEntry, now time.Time, live summonSessions) bool) (time.Time, error) {
	key := summonLedgerKey(c)
	for attempt := 0; attempt < summonLedgerAttempts; attempt++ {
		entry, cm, err := s.readSummonLedger(ctx, c)
		if err != nil {
			return time.Time{}, err
		}
		live, err := s.readSummonSessions(ctx)
		if err != nil {
			return time.Time{}, fromKubeError(err, "list sessions")
		}
		mono := s.summonClock().Now()
		now := entry.now(s.now())
		if !decide(&entry, now, live) {
			return time.Time{}, nil
		}
		entry.Clock = now
		entry.Charges = pruneSummonCharges(entry.Charges, now)
		raw, err := encodeSummonLedgerEntry(entry)
		if err != nil {
			return time.Time{}, summonLedgerUnavailable("entry for this caller is too large")
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = raw
		err = s.Client.Update(ctx, cm, client.FieldOwner(summonLedgerFieldManager))
		if err == nil {
			return mono, nil
		}
		if !apierrors.IsConflict(err) {
			// The write may or may not have landed. A retry of the same create
			// maps to the same id, so it is not charged twice either way.
			s.Log.Error(err, "summon create ledger write", "caller", c.String())
			return time.Time{}, summonLedgerUnavailable("could not be written")
		}
	}
	return time.Time{}, summonLedgerUnavailable("is busy")
}

// summonChargeResult is what the ledger decided for one dispatch id.
type summonChargeResult int

const (
	// summonChargedNow: this request committed a new charge.
	summonChargedNow summonChargeResult = iota + 1
	// summonMarked: the id's charge, of the same hash and inside its window,
	// got an attempt mark. It is the retry of a create that charged and then
	// failed, or the other half of a race for one key. It is not counted again.
	summonMarked
	// summonRecharged: the id's charge was outside its window and its session
	// does not exist: it was counted again, at now.
	summonRecharged
	// summonRebound: a name clash moved the charge to the next free suffix,
	// or found another replica had moved it; the window rule applied.
	summonRebound
	// summonHourFull: createsPerHour charges are inside the window, and the
	// id has no charge inside its own. Nothing was charged; an urgent create
	// was counted as a refusal.
	summonHourFull
	// summonMoved: a mark found the id's charge bound to another name, the
	// one returned. Nothing written; the dispatch follows it.
	summonMoved
	// summonOtherRequest: the id's charge has another hash. Nothing written.
	summonOtherRequest
	// summonSessionExists: the id's session exists. Nothing written.
	summonSessionExists
	// summonNameRefused: no charge for the id, and the requested name is
	// refused. Nothing written.
	summonNameRefused
	// summonNoFreeName: a rebind found no free suffix. Nothing written.
	summonNoFreeName
	// summonChargeGone: a rebind found the id's charge gone. Nothing written.
	summonChargeGone
)

// summonBinding is a ledger decision for one dispatch: the result, the name
// the ledger binds, for a mark the absolute deadline of the create it
// precedes, and the session when it already exists.
type summonBinding struct {
	result   summonChargeResult
	name     string
	deadline time.Time
	existing *v1alpha1.AgentSession
}

// chargeAttempt is the window rule for one more attempt on the charge at i
// (DESIGN-064 5.9, the correction's amendment, ruling 3): inside its window
// an attempt mark; outside it a re-charge, at and attempt now, counted again,
// if the hour has room; else summonHourFull, and an urgent create's refusal is
// counted in the same write. It returns the result and whether to write.
func chargeAttempt(e *summonLedgerEntry, i int, now time.Time, urgent bool, perHour int32) (summonChargeResult, bool) {
	ch := &e.Charges[i]
	if summonChargeCounts(*ch, now) {
		ch.Attempt = now
		return summonMarked, true
	}
	if e.inWindow(now) >= int(perHour) {
		if urgent {
			e.countUrgentRefusal(now)
		}
		return summonHourFull, urgent
	}
	ch.At, ch.Attempt = now, now
	return summonRecharged, true
}

// bindSummonCharge is a dispatch's first ledger write, which decides its
// binding; the mark before its first create follows it. A session with the
// id: nothing written. A charge with the id and another hash: nothing written
// (409). The same hash: the window rule (chargeAttempt), and the charge's
// name is the binding. No charge: a new one for name, subject to the hour,
// unless nameErr refuses it. An urgent create refused by the hour adds one
// to the caller's urgent refusals in the same write.
func (s *Server) bindSummonCharge(ctx context.Context, c *caller, id, hash, name string, nameErr error, urgent bool, perHour int32) (summonBinding, error) {
	var b summonBinding
	_, err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time, live summonSessions) bool {
		b = summonBinding{}
		if sess, ok := live.byID[id]; ok {
			b.result, b.existing = summonSessionExists, sess
			return false
		}
		hourFull := func() bool {
			b.result = summonHourFull
			if !urgent {
				return false
			}
			e.countUrgentRefusal(now)
			return true
		}
		if i := e.charge(id); i >= 0 {
			if e.Charges[i].Hash != hash {
				b.result = summonOtherRequest
				return false
			}
			var write bool
			b.result, write = chargeAttempt(e, i, now, urgent, perHour)
			b.name = e.Charges[i].Name
			return write
		}
		if nameErr != nil {
			b.result = summonNameRefused
			return false
		}
		if e.inWindow(now) >= int(perHour) {
			return hourFull()
		}
		e.Charges = append(e.Charges, summonCharge{ID: id, At: now, Attempt: now, Hash: hash, Name: name})
		b.result, b.name = summonChargedNow, name
		return true
	})
	return b, err
}

// rebindSummonCharge is the ledger write after a name clash on from: the
// charge moves to the first name after from in candidates that no session
// holds. If another replica already moved it, the charge's name is kept.
// Either way the window rule applies (chargeAttempt): inside its window an
// attempt mark; outside it a re-charge, counted again, or summonHourFull and
// no move. The mark before the next create follows it.
func (s *Server) rebindSummonCharge(ctx context.Context, c *caller, id, hash, from string, candidates []string, urgent bool, perHour int32) (summonBinding, error) {
	var b summonBinding
	_, err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time, live summonSessions) bool {
		b = summonBinding{}
		if sess, ok := live.byID[id]; ok {
			b.result, b.existing = summonSessionExists, sess
			return false
		}
		i := e.charge(id)
		if i < 0 {
			b.result = summonChargeGone
			return false
		}
		ch := &e.Charges[i]
		if ch.Hash != hash {
			b.result = summonOtherRequest
			return false
		}
		next := ch.Name
		if ch.Name == from {
			next = ""
			for j, cand := range candidates {
				if cand != from {
					continue
				}
				for _, later := range candidates[j+1:] {
					if !live.names[later] {
						next = later
						break
					}
				}
				break
			}
			if next == "" {
				b.result = summonNoFreeName
				return false
			}
		}
		r, write := chargeAttempt(e, i, now, urgent, perHour)
		if r == summonHourFull {
			b.result = summonHourFull
			return write
		}
		ch.Name = next
		b.result, b.name = summonRebound, next
		return true
	})
	return b, err
}

// markSummonAttempt is the ledger write before each create attempt (the
// correction's amendment, ruling 2): a compare-and-set on the binding. It
// succeeds only if the ledger still binds id, with hash, to name: then the
// window rule (chargeAttempt) marks or re-charges the charge, or refuses with
// summonHourFull, and a mark or re-charge returns the create's absolute
// deadline, this write's monotonic reading plus summonCreateDeadline.
// Otherwise it writes nothing and returns what the ledger holds: the
// session of the id (summonSessionExists), the name the charge is bound to
// now (summonMoved), another request's charge or none.
func (s *Server) markSummonAttempt(ctx context.Context, c *caller, id, hash, name string, urgent bool, perHour int32) (summonBinding, error) {
	var b summonBinding
	mono, err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time, live summonSessions) bool {
		b = summonBinding{}
		if sess, ok := live.byID[id]; ok {
			b.result, b.existing = summonSessionExists, sess
			return false
		}
		i := e.charge(id)
		if i < 0 {
			b.result = summonChargeGone
			return false
		}
		ch := &e.Charges[i]
		if ch.Hash != hash {
			b.result = summonOtherRequest
			return false
		}
		b.name = ch.Name
		if ch.Name != name {
			b.result = summonMoved
			return false
		}
		var write bool
		b.result, write = chargeAttempt(e, i, now, urgent, perHour)
		return write
	})
	if err == nil && (b.result == summonMarked || b.result == summonRecharged) {
		b.deadline = mono.Add(summonCreateDeadline)
	}
	return b, err
}

// summonChargeHeld reports whether the ledger holds a charge for id with
// hash. It is a read: a dispatch decides with it whether a name refusal
// applies, and creates nothing on it.
func (s *Server) summonChargeHeld(ctx context.Context, c *caller, id, hash string) (bool, error) {
	entry, _, err := s.readSummonLedger(ctx, c)
	if err != nil {
		return false, err
	}
	i := entry.charge(id)
	return i >= 0 && entry.Charges[i].Hash == hash, nil
}

// countSummonUrgentRefusal adds one urgent refusal to the caller's entry, in
// the write that refuses the create (R1). Story 742 pages on the count;
// nothing in this API reads it.
func (s *Server) countSummonUrgentRefusal(ctx context.Context, c *caller) error {
	_, err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time, _ summonSessions) bool {
		e.countUrgentRefusal(now)
		return true
	})
	return err
}
