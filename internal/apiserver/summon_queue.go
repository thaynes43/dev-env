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
// names (summonledger.ChargeQueues); one that no current entry names counts in
// every queue, so a bound can refuse early (coordinator answer op778-q-1).
// Priority is bound in the hash, so a policy edit cannot move a charge to
// another queue. The bounds assume Session creates land within the attempt
// hold: each late landing can add one Queued Session, while the leader still
// bounds admission by lane and pool (OP778 fix round 1, F1/F2).
//
// Which queue a session or charge is in, and the count itself, live in
// internal/summonledger (story 800), a leaf package the operator leader's
// filing can import too; the bound and the refusal stay here.

import (
	"net/http"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/summonledger"
)

// summonQueueDefault is limits.queued's and limits.queuedUrgent's CRD
// default. A policy the API server stored always has both; an absent one
// reads as the default.
const summonQueueDefault = 4

// summonQueue is one of a caller's queues (summonledger.Queue):
// limits.queued (Urgent false), or limits.queuedUrgent for one pool (Urgent
// true; Escalations picks the escalations pool, else the concurrent pool).
type summonQueue = summonledger.Queue

// summonQueueBound is the caller's limit for q.
func summonQueueBound(l v1alpha1.CallerPolicySummonLimits, q summonQueue) int {
	v := l.Queued
	if q.Urgent {
		v = l.QueuedUrgent
	}
	if v == nil {
		return summonQueueDefault
	}
	return int(*v)
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
	for _, q := range summonledger.ChargeQueues(c.policy, hash, name) {
		n := summonledger.QueueCount(c.policy.Name, c.policy, e.Charges, live.items, live.byID, grants, q, id, now)
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
	if b.queue.Urgent {
		pool := "concurrent"
		if b.queue.Escalations {
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
