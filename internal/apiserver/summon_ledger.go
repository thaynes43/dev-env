package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The create ledger (DESIGN-064 5.9, "The create ledger", N1). One
// operator-owned ConfigMap, summonLedgerNamespace/summonLedgerName, holds one
// key per summoning caller: its policy's name. Each key is one JSON document
// with the caller's charges, each a dispatch id and a time, and its count of
// urgent refusals with the time of the last. GitOps precreates the object
// with no data; the operator reads it uncached and writes it only by Update
// with the resourceVersion it read. It never creates or deletes it.
//
// The charge comes first: a summoning create commits the charge for its
// dispatch id before it creates the session, so no session exists whose id
// was not charged when it was created. A charge with no session is a spent
// slot of its hour, never a free session.
const (
	summonLedgerSchema       = "dev-env.summon-creates.v1"
	summonLedgerFieldManager = "dev-env-summon-creates"
	// summonChargeWindow is how long a charge counts toward createsPerHour,
	// from its time. After it the charge leaves the ledger at the next write
	// (summonChargeSettled says when).
	summonChargeWindow = time.Hour
	// summonLedgerAttempts bounds the optimistic-lock retries of one write.
	summonLedgerAttempts = 5
	// summonLedgerMaxEntry bounds one caller's document.
	summonLedgerMaxEntry = 64 << 10
)

// summonDispatchIDPattern is spec.summon.dispatchID's schema pattern. A
// charge whose id does not match it is not a charge this API wrote.
var summonDispatchIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]{0,127}$`)

var errSummonLedgerUnreadable = errors.New("summon create ledger entry is unreadable")

// summonCharge is one charge: a dispatch id and when it was charged. Fields
// this version does not know are kept as they were.
type summonCharge struct {
	ID    string
	At    time.Time
	extra map[string]json.RawMessage
}

// summonLedgerEntry is one caller's key.
type summonLedgerEntry struct {
	Charges             []summonCharge
	UrgentRefusals      int64
	LastUrgentRefusalAt *time.Time
	extra               map[string]json.RawMessage
}

// inHour counts the charges that still count toward createsPerHour at now.
func (e *summonLedgerEntry) inHour(now time.Time) int {
	n := 0
	for _, ch := range e.Charges {
		if now.Sub(ch.At) < summonChargeWindow {
			n++
		}
	}
	return n
}

// decodeSummonLedgerEntry reads one caller's key. An absent key is an empty
// entry. Anything this version cannot read is errSummonLedgerUnreadable: the
// create is then refused, never admitted without a charge, and the key is not
// overwritten. Unknown members, at the top and in a charge, are kept, so a
// newer replica's field survives an older replica's write.
func decodeSummonLedgerEntry(raw string) (summonLedgerEntry, error) {
	var e summonLedgerEntry
	if raw == "" {
		return e, nil
	}
	if len(raw) > summonLedgerMaxEntry {
		return e, errSummonLedgerUnreadable
	}
	top, err := decodeLedgerObject([]byte(raw))
	if err != nil {
		return e, err
	}
	var schema string
	if json.Unmarshal(top["schema"], &schema) != nil || schema != summonLedgerSchema {
		return e, errSummonLedgerUnreadable
	}
	delete(top, "schema")
	if raw, ok := top["charges"]; ok {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return e, errSummonLedgerUnreadable
		}
		for _, item := range items {
			fields, err := decodeLedgerObject(item)
			if err != nil {
				return e, err
			}
			var ch summonCharge
			if json.Unmarshal(fields["id"], &ch.ID) != nil || !summonDispatchIDPattern.MatchString(ch.ID) ||
				json.Unmarshal(fields["at"], &ch.At) != nil || ch.At.IsZero() {
				return e, errSummonLedgerUnreadable
			}
			delete(fields, "id")
			delete(fields, "at")
			if len(fields) > 0 {
				ch.extra = fields
			}
			e.Charges = append(e.Charges, ch)
		}
		delete(top, "charges")
	}
	if raw, ok := top["urgentRefusals"]; ok {
		if json.Unmarshal(raw, &e.UrgentRefusals) != nil || e.UrgentRefusals < 0 {
			return e, errSummonLedgerUnreadable
		}
		delete(top, "urgentRefusals")
	}
	if raw, ok := top["lastUrgentRefusalAt"]; ok {
		if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			var at time.Time
			if json.Unmarshal(raw, &at) != nil || at.IsZero() {
				return e, errSummonLedgerUnreadable
			}
			e.LastUrgentRefusalAt = &at
		}
		delete(top, "lastUrgentRefusalAt")
	}
	if len(top) > 0 {
		e.extra = top
	}
	return e, nil
}

func decodeLedgerObject(raw []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errSummonLedgerUnreadable
	}
	return obj, nil
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
		charges = append(charges, m)
	}
	top["schema"] = summonLedgerSchema
	top["charges"] = charges
	top["urgentRefusals"] = e.UrgentRefusals
	if e.LastUrgentRefusalAt != nil {
		top["lastUrgentRefusalAt"] = e.LastUrgentRefusalAt.UTC()
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

// summonChargeSettled is the "session exists" test of the ledger's ageing
// rule (DESIGN-064 5.9): a charge an hour old leaves the ledger once its
// session exists, or once an uncached read by its id finds no session. Today
// every session that exists settles its charge. Story 741 narrows this to
// "admitted or closed": a Queued session keeps its charge, because the
// leader admits only a session whose id the ledger holds.
func summonChargeSettled(sess *v1alpha1.AgentSession) bool {
	return sess != nil
}

// pruneSummonCharges drops each charge at least an hour old whose session
// is settled or that no session carries. byID is an uncached read of the
// sessions, by dispatch id. A retry after a drop maps to the same id and is
// charged again: a double charge across two hours is acceptable, an
// uncharged session is not.
func pruneSummonCharges(charges []summonCharge, now time.Time, byID map[string]*v1alpha1.AgentSession) []summonCharge {
	kept := make([]summonCharge, 0, len(charges))
	for _, ch := range charges {
		if now.Sub(ch.At) < summonChargeWindow {
			kept = append(kept, ch)
			continue
		}
		if sess, found := byID[ch.ID]; found && !summonChargeSettled(sess) {
			kept = append(kept, ch)
		}
	}
	return kept
}

// summonLedgerUnavailable is the refusal when the ledger cannot be read or
// written. A summoning create is never admitted without its charge.
func summonLedgerUnavailable(why string) error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
		"the summon create ledger %s/%s %s; no session was created (DESIGN-064 5.9)", summonLedgerNamespace, summonLedgerName, why)
}

// summonLedgerKey is the caller's key in the ledger: its policy's name.
func summonLedgerKey(c *caller) string {
	return c.policy.Name
}

// summonSessionsByID is an uncached read of every session that carries a
// dispatch id, by that id.
func (s *Server) summonSessionsByID(ctx context.Context) (map[string]*v1alpha1.AgentSession, error) {
	var list v1alpha1.AgentSessionList
	if err := s.Live.List(ctx, &list, client.InNamespace(s.Policy.SessionNamespace)); err != nil {
		return nil, err
	}
	byID := make(map[string]*v1alpha1.AgentSession, len(list.Items))
	for i := range list.Items {
		it := &list.Items[i]
		if it.Spec.Summon != nil && it.Spec.Summon.DispatchID != "" {
			byID[it.Spec.Summon.DispatchID] = it
		}
	}
	return byID, nil
}

// updateSummonLedger reads the ledger uncached, lets decide change the
// caller's entry, and writes it back with the resourceVersion it read. A
// conflict re-reads and decides again, so a decision always rests on the
// ledger it is written over: of two replicas racing for a slot, one update
// wins and the loser decides again on the ledger with the winner's charge.
// decide returns whether to write. Every write also drops the charges the
// ageing rule lets go, read with an uncached list of the sessions; nothing
// else ages the ledger.
func (s *Server) updateSummonLedger(ctx context.Context, c *caller, decide func(e *summonLedgerEntry, now time.Time) bool) error {
	if s.Live == nil || s.Client == nil {
		return summonLedgerUnavailable("cannot be read")
	}
	key := summonLedgerKey(c)
	for attempt := 0; attempt < summonLedgerAttempts; attempt++ {
		var cm corev1.ConfigMap
		if err := s.Live.Get(ctx, client.ObjectKey{Namespace: summonLedgerNamespace, Name: summonLedgerName}, &cm); err != nil {
			if !apierrors.IsNotFound(err) {
				s.Log.Error(err, "summon create ledger read")
			}
			return summonLedgerUnavailable("cannot be read")
		}
		if !cm.DeletionTimestamp.IsZero() || (cm.Immutable != nil && *cm.Immutable) || cm.ResourceVersion == "" {
			return summonLedgerUnavailable("cannot be written")
		}
		entry, err := decodeSummonLedgerEntry(cm.Data[key])
		if err != nil {
			s.Log.Info("summon create ledger entry unreadable", "caller", c.String())
			return summonLedgerUnavailable("has an entry for this caller that cannot be read")
		}
		now := s.now()
		if !decide(&entry, now) {
			return nil
		}
		byID, err := s.summonSessionsByID(ctx)
		if err != nil {
			return fromKubeError(err, "list sessions")
		}
		entry.Charges = pruneSummonCharges(entry.Charges, now, byID)
		raw, err := encodeSummonLedgerEntry(entry)
		if err != nil {
			return summonLedgerUnavailable("entry for this caller is too large")
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[key] = raw
		err = s.Client.Update(ctx, &cm, client.FieldOwner(summonLedgerFieldManager))
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			// The write may or may not have landed. A retry of the same create
			// maps to the same id, so it is not charged twice either way.
			s.Log.Error(err, "summon create ledger write", "caller", c.String())
			return summonLedgerUnavailable("could not be written")
		}
	}
	return summonLedgerUnavailable("is busy")
}

// summonChargeResult is what the ledger decided for one dispatch id.
type summonChargeResult int

const (
	// summonChargedNow: this request committed the charge.
	summonChargedNow summonChargeResult = iota + 1
	// summonChargedBefore: the id was already in the ledger, so it is not
	// charged again. It is the retry of a create that charged and then failed,
	// or the other half of a race for one key.
	summonChargedBefore
	// summonHourFull: createsPerHour charges are inside the hour. Nothing was
	// charged; an urgent create was counted as a refusal.
	summonHourFull
)

// chargeSummon commits the charge for id, or finds it already committed, or
// refuses it because the caller's hour is full. An urgent refusal adds one to
// the caller's urgent refusals in the same write, once. An hour-old charge
// for id that the ageing rule drops is charged again, at now, and counts in
// the new hour.
func (s *Server) chargeSummon(ctx context.Context, c *caller, id string, urgent bool, perHour int32) (summonChargeResult, error) {
	var result summonChargeResult
	err := s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time) bool {
		result = 0
		for _, ch := range e.Charges {
			// A charge for this id that still counts is the record of this
			// dispatch. One past its hour is dropped by the write below
			// unless it is still needed, so its retry is charged again.
			if ch.ID == id && now.Sub(ch.At) < summonChargeWindow {
				result = summonChargedBefore
				return false
			}
		}
		if e.inHour(now) >= int(perHour) {
			result = summonHourFull
			if !urgent {
				return false
			}
			e.UrgentRefusals++
			at := now.UTC()
			e.LastUrgentRefusalAt = &at
			return true
		}
		kept := e.Charges[:0:0]
		for _, ch := range e.Charges {
			if ch.ID != id {
				kept = append(kept, ch)
			}
		}
		e.Charges = append(kept, summonCharge{ID: id, At: now.UTC()})
		result = summonChargedNow
		return true
	})
	return result, err
}

// countSummonUrgentRefusal adds one urgent refusal to the caller's entry, in
// the write that refuses the create (R1). Story 742 pages on the count;
// nothing in this API reads it.
func (s *Server) countSummonUrgentRefusal(ctx context.Context, c *caller) error {
	return s.updateSummonLedger(ctx, c, func(e *summonLedgerEntry, now time.Time) bool {
		e.UrgentRefusals++
		at := now.UTC()
		e.LastUrgentRefusalAt = &at
		return true
	})
}
