package apiserver

// The queues (DESIGN-064 5.9 "The queues", 6.4 "Limits", 6.7 "Holds"; OP778,
// slice b of item 741). With --enable-summoned-lanes a summoning create past
// its pool is created Queued and the operator leader admits it; what bounds
// the waiting is a caller's two queue limits:
//
//   - limits.queued counts the caller's Queued sessions of priority normal
//     and bulk (upgrades and curation);
//   - limits.queuedUrgent counts its Queued sessions of priority urgent, per
//     pool: Queued remediations apart from Queued escalations, so a full
//     escalations queue never refuses a remediation, nor the reverse.
//
// The count is taken inside the create ledger's compare-and-set (ruling D2 on
// item 741): in the same Update that charges, from the uncached session list
// that Update already reads, so of two API replicas racing for a queue's last
// place one write wins and the loser decides again on the winner's charge. It
// is the caller's Queued sessions in that queue (summonoutcome.Queued, the
// leader's own definition; a session the ledger's reserved key grants is
// admitted, not Queued), plus the caller's charges that have no Session and
// whose last attempt mark is younger than summonledger.AttemptHold: a create
// under way, or one that failed within that hold. A charge records no lane or
// priority, so it is put in the queue of the policy entry its request hash
// names (summonChargeQueues); one that no current entry names counts in every
// queue, so a bound can refuse early (coordinator answer op778-q-1).
// Priority is bound in the hash, so a policy edit cannot move a charge to
// another queue. The bounds assume Session creates land within the attempt
// hold: each late landing can add one Queued Session, while the leader still
// bounds admission by lane and pool (OP778 fix round 1, F1/F2).

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// summonQueueDefault is limits.queued's and limits.queuedUrgent's CRD
// default. A policy the API server stored always has both; an absent one
// reads as the default.
const summonQueueDefault = 4

// summonQueue is one of a caller's queues: limits.queued (urgent false), or
// limits.queuedUrgent for one pool (urgent true; escalations picks the
// escalations pool, else the concurrent pool).
type summonQueue struct {
	urgent      bool
	escalations bool
}

// summonQueues is every queue, the ones a charge no entry names counts in.
var summonQueues = []summonQueue{{}, {urgent: true}, {urgent: true, escalations: true}}

// summonQueueOf is the queue of a session or entry of priority p in lane l.
// Normal and bulk share one queue whatever the lane; urgent is per pool.
func summonQueueOf(p v1alpha1.SummonPriority, l v1alpha1.Lane) summonQueue {
	if p != v1alpha1.SummonPriorityUrgent {
		return summonQueue{}
	}
	return summonQueue{urgent: true, escalations: l == v1alpha1.LaneEscalation}
}

// summonQueueBound is the caller's limit for q.
func summonQueueBound(l v1alpha1.CallerPolicySummonLimits, q summonQueue) int {
	v := l.Queued
	if q.urgent {
		v = l.QueuedUrgent
	}
	if v == nil {
		return summonQueueDefault
	}
	return int(*v)
}

// summonRequestedNames are the requested names a charge bound to name can
// come from: name itself, and, when name ends in a suffix -2 to -99, the name
// before it (summonNameSequence).
func summonRequestedNames(name string) []string {
	out := []string{name}
	i := strings.LastIndexByte(name, '-')
	if i <= 0 {
		return out
	}
	n, err := strconv.Atoi(name[i+1:])
	if err == nil && n >= 2 && n <= summonMaxSuffix && strconv.Itoa(n) == name[i+1:] {
		out = append(out, name[:i])
	}
	return out
}

// summonChargeQueues are the queues a charge with request hash hash, bound to
// name, counts in: the queue of the policy entry whose mode, lane, priority and one of
// summonRequestedNames(name) hash to hash (summonRequestHash). A hash no
// current entry gives, a policy edited since the charge for example, counts
// in every queue.
func summonChargeQueues(p *v1alpha1.CallerPolicy, hash, name string) []summonQueue {
	if p != nil && p.Spec.Summon != nil {
		for _, requested := range summonRequestedNames(name) {
			for i := range p.Spec.Summon.Sessions {
				e := &p.Spec.Summon.Sessions[i]
				if summonRequestHash(summonWire{Mode: string(e.Kind), Lane: string(e.Lane), Name: requested}, e.Priority) == hash {
					return []summonQueue{summonQueueOf(e.Priority, e.Lane)}
				}
			}
		}
	}
	return summonQueues
}

// summonGranted reports whether grants hold an open grant for this session,
// by name and UID: the leader has admitted it, though admittedAt may not be
// written yet.
func summonGranted(g summonledger.Grants, s *v1alpha1.AgentSession) bool {
	for _, it := range g.Items {
		if it.Name == s.Name && it.UID == string(s.UID) {
			return true
		}
	}
	return false
}

// summonQueueCount counts queue q for the caller at now, from what one ledger
// write read: its Queued sessions in q, and its charges in q that have no
// Session and whose attempt mark is younger than summonledger.AttemptHold.
// skip, the dispatch being decided, is not counted.
func summonQueueCount(c *caller, e *summonLedgerEntry, now time.Time, live summonSessions, grants summonledger.Grants, q summonQueue, skip string) int {
	key := summonLedgerKey(c)
	n := 0
	for i := range live.items {
		it := &live.items[i]
		if it.Spec.Caller != key || it.Spec.Summon == nil || it.Spec.Summon.DispatchID == skip {
			continue
		}
		if summonoutcome.Queued(it, summonGranted(grants, it)) && summonQueueOf(it.Spec.Summon.Priority, it.Spec.Lane) == q {
			n++
		}
	}
	for _, ch := range e.Charges {
		if ch.ID == skip || live.byID[ch.ID] != nil || now.Sub(ch.Attempt) >= summonledger.AttemptHold {
			continue
		}
		if slices.Contains(summonChargeQueues(c.policy, ch.Hash, ch.Name), q) {
			n++
		}
	}
	return n
}

// refuseSummonQueue applies the queue bound to a charge about to join the
// count, inside a ledger write's decision: a new charge, or a mark or
// re-charge of a charge whose attempt mark has passed the hold. With
// --enable-summoned-lanes off there are no queues and it never stops the
// decision. Otherwise, for each queue the charge counts in, a queue already
// holding its bound refuses: b.result is summonQueueFull with that queue and
// its count, and an urgent create's refusal is counted in this same write. A
// present reserved key that cannot be read stops with summonGrantsUnreadable
// and no write. It reports whether the decision stops and whether to write.
func (s *Server) refuseSummonQueue(b *summonBinding, c *caller, e *summonLedgerEntry, now time.Time, live summonSessions, id, hash, name string, urgent bool) (stop, write bool) {
	if !s.SummonedLanes {
		return false, false
	}
	grants, err := summonledger.DecodeGrants(live.grantsRaw, live.grantsPresent)
	if err != nil {
		s.Log.Info("summon create ledger admission grants unreadable", "caller", c.String())
		b.result = summonGrantsUnreadable
		return true, false
	}
	limits := c.policy.Spec.Summon.Limits
	for _, q := range summonChargeQueues(c.policy, hash, name) {
		n := summonQueueCount(c, e, now, live, grants, q, id)
		if n < summonQueueBound(limits, q) {
			continue
		}
		b.result, b.queue, b.queued = summonQueueFull, q, n
		if urgent {
			e.CountUrgentRefusal(now)
		}
		return true, urgent
	}
	return false, false
}

// summonQueueRefusal is the 429 for a full queue. It names the queue: "summon
// queue full" for limits.queued, "summon urgent queue full" for
// limits.queuedUrgent and its pool. charged is a refusal of a charge already
// in the ledger, which keeps it, so it carries the retention sentence; the
// refusal of a new charge spends nothing and does not.
func summonQueueRefusal(c *caller, b summonBinding, charged bool) error {
	var err *apiError
	if b.queue.urgent {
		pool := "concurrent"
		if b.queue.escalations {
			pool = "escalations"
		}
		err = tooManyRequests("summon urgent queue full: caller %s has %d urgent sessions Queued or being created in its %s pool, its limits.queuedUrgent; no session was created (DESIGN-064 5.9)",
			c.policy.Name, b.queued, pool)
	} else {
		err = tooManyRequests("summon queue full: caller %s has %d sessions of priority normal or bulk Queued or being created, its limits.queued; no session was created (DESIGN-064 5.9)",
			c.policy.Name, b.queued)
	}
	if charged {
		return summonChargedFailure(err)
	}
	return err
}

// summonGrantsFault is the 503 when the queue must be counted and the
// ledger's reserved key cannot be read (coordinator answer op778-q-1).
func summonGrantsFault() error {
	return newError(http.StatusServiceUnavailable, apiv1.CodeUnavailable,
		"the summon create ledger %s/%s has admission grants that cannot be read, so the queue cannot be counted; no session was created (DESIGN-064 5.9)", summonLedgerNamespace, summonLedgerName)
}
