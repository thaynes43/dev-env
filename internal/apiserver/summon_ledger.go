package apiserver

import (
	"context"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonledger"
)

// The create ledger (DESIGN-064 5.9, "The create ledger", N1, and its
// correction of 2026-11-12). One operator-owned ConfigMap,
// summonLedgerNamespace/summonLedgerName, holds one key per summoning caller:
// its policy's name. The leader also owns summonledger.GrantsKey, which no
// CallerPolicy can be named. Every API path below reads/changes only its
// caller's key and updates the original ConfigMap, carrying that reserved key
// byte-for-byte. Charge, mark, rebind, prune and urgent-refusal counts never
// iterate over the ConfigMap's keys or decode the grants as a caller entry.
// Each caller key is one JSON document, the caller's entry:
//
//	schema             summonledger.Schema
//	clock              the latest "now" any write of this entry used
//	charges            each {id, at, attempt, hash, name}
//	urgentRefusals     the count of refused urgent creates, stopping at its maximum
//	lastUrgentRefusal  when the last was refused, or null
//
// A charge binds one dispatch id to its request's hash (mode, lane, entry
// priority and the requested name) and to the one session name it creates. at is when it was
// counted toward createsPerHour; attempt is the last time a create was about
// to be made for it. GitOps precreates the object with no data; the operator
// reads it uncached and writes it only by Update with the resourceVersion it
// read. It never creates or deletes it.
//
// Time. For one entry, "now" is the later of the replica's clock and the
// entry's clock, and every write stores it back as the clock. A charge counts
// toward createsPerHour while it is younger than summonledger.ChargeWindow by that
// clock: 60 minutes and 5 for replica clocks that differ. The limit is on
// charges, not creates: at most createsPerHour charges in any
// summonledger.ChargeWindow by the entry's clock. A create may follow its charge at
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
// only when it is summonledger.ChargeWindow old and its last attempt mark is at
// least summonledger.AttemptHold old, and no session of its id exists that is
// neither admitted nor closed (summonledger.Prune; DESIGN-064 5.9 item 4,
// OP777): a Queued session keeps the charge the leader admits it by.
//
// Limits of this slice (the correction's amendment). A client-side deadline
// does not stop a request the API server has already received, nor a process
// frozen between its last check and the send; a mark does not stop a replica
// paused between it and its create. So two windows remain. (1) A create that
// lands after its charge was pruned. The charge is already summonledger.ChargeWindow
// old. A marks X and sends its create inside the deadline; the API server has
// not persisted it when another replica's write, summonledger.AttemptHold after the
// mark by the entry's clock, prunes X; the session is then persisted with no
// charge. A process frozen before it sends, or between the mark and the send
// for longer than the hold allows, is this same window. (2) A stale destination: A marks
// X for N and is paused before its create; B marks X for N, meets another
// session on N, rebinds X to N-2 and creates N-2; that other session is
// deleted, freeing N; A resumes inside its deadline and creates N, so X has
// two sessions. That no summoned session gets a pod unless the ledger holds
// its id bound to its name, and so that no id runs two sessions, is enforced
// at admission by the operator leader (internal/controller/summon_admission.go,
// OP777), not here: the uncharged or surplus session is closed not-charged.
const (
	summonLedgerFieldManager = "dev-env-summon-creates"
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
)

// The entry's types are the shared package's (internal/summonledger), which
// the operator leader's admission reads too. The API keeps its write paths here.
type (
	summonCharge      = summonledger.Charge
	summonLedgerEntry = summonledger.Entry
)

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
// unsettled holds the dispatch id of every session that exists and is neither
// admitted nor closed: the prune keeps its charge (summonledger.Prune, 7233).
// items are the listed sessions, which the queue counts read (OP778), and
// grantsRaw and grantsPresent the ledger's reserved key summonledger.GrantsKey
// as this write read it: the queue counts decode it, read only, to tell a
// granted session from a Queued one. Every write carries it byte-for-byte.
type summonSessions struct {
	byID          map[string]*v1alpha1.AgentSession
	names         map[string]bool
	unsettled     map[string]bool
	items         []v1alpha1.AgentSession
	grantsRaw     string
	grantsPresent bool
}

// summonUnsettled is a session the leader may still admit by its charge: not
// admitted and not closed (DESIGN-064 5.9 item 4).
func summonUnsettled(sess *v1alpha1.AgentSession) bool {
	st := sess.Status.Summon
	return st == nil || st.AdmittedAt == nil && st.Closed == nil
}

func (s *Server) readSummonSessions(ctx context.Context) (summonSessions, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return summonSessions{}, err
	}
	out := summonSessions{
		byID:      make(map[string]*v1alpha1.AgentSession, len(list.Items)),
		names:     make(map[string]bool, len(list.Items)),
		unsettled: map[string]bool{},
		items:     list.Items,
	}
	for i := range list.Items {
		it := &list.Items[i]
		out.names[it.Name] = true
		if it.Spec.Summon != nil && it.Spec.Summon.DispatchID != "" {
			out.byID[it.Spec.Summon.DispatchID] = it
			if summonUnsettled(it) {
				out.unsettled[it.Spec.Summon.DispatchID] = true
			}
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
	entry, err := summonledger.Decode(raw, present)
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
		live.grantsRaw, live.grantsPresent = cm.Data[summonledger.GrantsKey]
		mono := s.summonClock().Now()
		now := entry.Now(s.now())
		if !decide(&entry, now, live) {
			return time.Time{}, nil
		}
		entry.Clock = now
		entry.Charges = summonledger.Prune(entry.Charges, now, live.unsettled)
		raw, err := summonledger.Encode(entry)
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
	// summonQueueFull: with --enable-summoned-lanes, the queue this charge
	// would join holds its bound (OP778, refuseSummonQueue). Nothing was charged
	// or marked; an urgent create was counted as a refusal in the same write.
	summonQueueFull
	// summonGrantsUnreadable: the queue had to be counted and the ledger's
	// reserved key cannot be read, so a granted session cannot be told from a
	// Queued one. Nothing written.
	summonGrantsUnreadable
)

// summonBinding is a ledger decision for one dispatch: the result, the name
// the ledger binds, for a mark the absolute deadline of the create it
// precedes, the session when it already exists, and for summonQueueFull the
// queue, its count and whether the refused charge was already in the ledger
// (kept: a refusal that keeps a charge says so).
type summonBinding struct {
	result   summonChargeResult
	name     string
	deadline time.Time
	existing *v1alpha1.AgentSession
	queue    summonQueue
	queued   int
	kept     bool
}

// chargeAttempt is the window rule for one more attempt on the charge at i
// (DESIGN-064 5.9, the correction's amendment, ruling 3): inside its window
// an attempt mark; outside it a re-charge, at and attempt now, counted again,
// if the hour has room; else summonHourFull, and an urgent create's refusal is
// counted in the same write. A charge whose last attempt mark is at least
// summonledger.AttemptHold old is in no queue count, so with
// --enable-summoned-lanes its mark or re-charge first needs room in its queue
// (refuseSummonQueue), after the hour: a late retry cannot pass a queue that
// filled meanwhile. It returns the result and whether to write; b carries a
// queue refusal.
func (s *Server) chargeAttempt(b *summonBinding, c *caller, e *summonLedgerEntry, i int, now time.Time, live summonSessions, urgent bool, perHour int32) (summonChargeResult, bool) {
	ch := &e.Charges[i]
	stale := now.Sub(ch.Attempt) >= summonledger.AttemptHold
	if summonledger.ChargeCounts(*ch, now) {
		if stale {
			if stop, write := s.refuseSummonQueue(b, c, e, now, live, ch.ID, ch.Hash, ch.Name, urgent); stop {
				b.kept = true
				return b.result, write
			}
		}
		ch.Attempt = now
		return summonMarked, true
	}
	if e.InWindow(now) >= int(perHour) {
		if urgent {
			e.CountUrgentRefusal(now)
		}
		return summonHourFull, urgent
	}
	if stale {
		if stop, write := s.refuseSummonQueue(b, c, e, now, live, ch.ID, ch.Hash, ch.Name, urgent); stop {
			b.kept = true
			return b.result, write
		}
	}
	ch.At, ch.Attempt = now, now
	return summonRecharged, true
}

// bindSummonCharge is a dispatch's first ledger write, which decides its
// binding; the mark before its first create follows it. A session with the
// id: nothing written. A charge with the id and another hash: nothing written
// (409). The same hash: the window rule (chargeAttempt), and the charge's
// name is the binding. No charge: a new one for name, subject to the hour
// and then, with --enable-summoned-lanes, to its queue (refuseSummonQueue),
// unless nameErr refuses it. An urgent create refused by the hour or by its
// queue adds one to the caller's urgent refusals in the same write, once.
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
			e.CountUrgentRefusal(now)
			return true
		}
		if i := e.Index(id); i >= 0 {
			if e.Charges[i].Hash != hash {
				b.result = summonOtherRequest
				return false
			}
			var write bool
			b.result, write = s.chargeAttempt(&b, c, e, i, now, live, urgent, perHour)
			b.name = e.Charges[i].Name
			return write
		}
		if nameErr != nil {
			b.result = summonNameRefused
			return false
		}
		if e.InWindow(now) >= int(perHour) {
			return hourFull()
		}
		if stop, write := s.refuseSummonQueue(&b, c, e, now, live, id, hash, name, urgent); stop {
			return write
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
		i := e.Index(id)
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
		r, write := s.chargeAttempt(&b, c, e, i, now, live, urgent, perHour)
		if r != summonMarked && r != summonRecharged {
			b.result = r
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
		i := e.Index(id)
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
		b.result, write = s.chargeAttempt(&b, c, e, i, now, live, urgent, perHour)
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
	i := entry.Index(id)
	return i >= 0 && entry.Charges[i].Hash == hash, nil
}

// countSummonUrgentRefusal adds one urgent refusal to the caller's entry, in
// the write that refuses the create (R1). Story 742 pages on the count;
// nothing in this API reads it.
func (s *Server) countSummonUrgentRefusal(ctx context.Context, c *caller) error {
	_, err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time, _ summonSessions) bool {
		e.CountUrgentRefusal(now)
		return true
	})
	return err
}
