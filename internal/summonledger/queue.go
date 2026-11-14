package summonledger

// The queues' pure part (DESIGN-064 5.9 "The queues" and the story 778
// correction, item 2): which queue a session or a charge is in, and the count
// of one queue from what one ledger write read. The API counts inside its
// ledger compare-and-set (internal/apiserver, summon_queue.go); the operator
// leader's filing (story 768) is to count the same way.

import (
	"slices"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Queue is one of a caller's queues: limits.queued (Urgent false), or
// limits.queuedUrgent for one pool (Urgent true; Escalations picks the
// escalations pool, else the concurrent pool).
type Queue struct {
	Urgent      bool
	Escalations bool
}

// AllQueues is every queue, in the order a charge that no entry names counts
// in them.
func AllQueues() []Queue {
	return []Queue{{}, {Urgent: true}, {Urgent: true, Escalations: true}}
}

// QueueOf is the queue of a session or entry of priority p in lane l.
// Normal and bulk share one queue whatever the lane; urgent is per pool.
func QueueOf(p v1alpha1.SummonPriority, l v1alpha1.Lane) Queue {
	if p != v1alpha1.SummonPriorityUrgent {
		return Queue{}
	}
	return Queue{Urgent: true, Escalations: l == v1alpha1.LaneEscalation}
}

// ChargeQueues are the queues a charge with request hash hash, bound to
// name, counts in: the queue of the policy entry whose mode, lane, priority
// and one of the requested names name can come from hash to hash
// (RequestHash). A hash no current entry gives, a policy edited since the
// charge for example, counts in every queue (AllQueues).
func ChargeQueues(p *v1alpha1.CallerPolicy, hash, name string) []Queue {
	if p != nil && p.Spec.Summon != nil {
		for _, requested := range requestedNames(name) {
			for i := range p.Spec.Summon.Sessions {
				e := &p.Spec.Summon.Sessions[i]
				if RequestHash(string(e.Kind), string(e.Lane), requested, e.Priority) == hash {
					return []Queue{QueueOf(e.Priority, e.Lane)}
				}
			}
		}
	}
	return AllQueues()
}

// Granted reports whether grants hold an open grant for this session, by name
// and UID: the leader has admitted it, though admittedAt may not be written
// yet.
func Granted(g Grants, s *v1alpha1.AgentSession) bool {
	for _, it := range g.Items {
		if it.Name == s.Name && it.UID == string(s.UID) {
			return true
		}
	}
	return false
}

// QueueCount counts queue q for the caller named caller at now, from what
// one ledger write read: the caller's entry's charges, the listed sessions,
// those sessions by dispatch id, and the ledger's reserved key decoded read
// only. caller is the spec.caller of the caller's sessions, which is also its
// policy's name and its ledger key. It is the caller's Queued sessions in q
// (summonoutcome.Queued; a session grants holds is admitted, not Queued), each
// in the queue of its own priority and lane, and its charges in q that have no
// Session and whose attempt mark is younger than AttemptHold. skip, the
// dispatch being decided, is not counted.
//
// p, the caller's policy, only places a charge in its queues (ChargeQueues)
// and is never dereferenced here. A nil p, a policy that is gone, counts each
// such charge in every queue; the sessions are counted as with a policy.
func QueueCount(caller string, p *v1alpha1.CallerPolicy, charges []Charge, sessions []v1alpha1.AgentSession, byID map[string]*v1alpha1.AgentSession, grants Grants, q Queue, skip string, now time.Time) int {
	n := 0
	for i := range sessions {
		it := &sessions[i]
		if it.Spec.Caller != caller || it.Spec.Summon == nil || it.Spec.Summon.DispatchID == skip {
			continue
		}
		if summonoutcome.Queued(it, Granted(grants, it)) && QueueOf(it.Spec.Summon.Priority, it.Spec.Lane) == q {
			n++
		}
	}
	for _, ch := range charges {
		if ch.ID == skip || byID[ch.ID] != nil || now.Sub(ch.Attempt) >= AttemptHold {
			continue
		}
		if slices.Contains(ChargeQueues(p, ch.Hash, ch.Name), q) {
			n++
		}
	}
	return n
}
