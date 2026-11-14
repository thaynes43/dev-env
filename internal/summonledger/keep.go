package summonledger

import "github.com/thaynes43/dev-env/api/v1alpha1"

// Keep is the keep rule of the prune (Prune's keep): from the sessions one
// ledger write listed, the dispatch ids whose charges are kept however old
// they are. It holds:
//
//   - the dispatch id of every session that carries one and is neither
//     admitted nor closed (DESIGN-064 5.9 item 4, item 741 comment 7233): the
//     leader may still admit that session by its charge;
//   - FilingID(UID) of every session whose status.summon.escalation.state is
//     Pending (story 800, coordinator rulings on item 768 comment 7702): the
//     platform's filing charges that id before its child exists, so while it
//     waits no session carries the id. Once the record is no longer Pending,
//     or the session is gone, the first rule decides: the child, once it
//     exists, carries the same id.
//
// The result is never nil. Its users are every ledger write of the API
// (internal/apiserver) and the operator leader's filing write (story 801),
// so both prune by one rule.
func Keep(sessions []v1alpha1.AgentSession) map[string]bool {
	keep := map[string]bool{}
	for i := range sessions {
		it := &sessions[i]
		if it.Spec.Summon != nil && it.Spec.Summon.DispatchID != "" && unsettled(it) {
			keep[it.Spec.Summon.DispatchID] = true
		}
		if pendingFiling(it) {
			keep[FilingID(it.UID)] = true
		}
	}
	return keep
}

// unsettled is a session the leader may still admit by its charge: not
// admitted and not closed (DESIGN-064 5.9 item 4).
func unsettled(sess *v1alpha1.AgentSession) bool {
	st := sess.Status.Summon
	return st == nil || st.AdmittedAt == nil && st.Closed == nil
}

// pendingFiling is a session whose filing waits: the one terminal write
// recorded status.summon.escalation with state Pending (summonoutcome.Write)
// and nothing has moved it on.
func pendingFiling(sess *v1alpha1.AgentSession) bool {
	st := sess.Status.Summon
	return st != nil && st.Escalation != nil && st.Escalation.State == "Pending"
}
