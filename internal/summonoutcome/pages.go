package summonoutcome

import (
	"slices"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// LabelSummoned marks the AgentSession a summoning create built (DESIGN-064
// 6.8; story 782, coordinator ruling Q2 on item 742 comment 7457). The API's
// summoning create sets it to LabelSummonedValue, and the keeper's pager lists
// sessions by it and still requires spec.caller. No other create path sets it.
// A session without it is never paged; only an operator-tier Kubernetes
// writer can remove it (a stated limit). It lives here, beside the table it
// selects for, and not under api/v1alpha1.
const (
	LabelSummoned      = v1alpha1.LabelPrefix + "summoned"
	LabelSummonedValue = "true"
)

// TerminalEvent is what a closed summoned session gets by DESIGN-064 6.8's
// terminal table: one terminal page, or the daily digest.
type TerminalEvent string

const (
	TerminalFailed    TerminalEvent = "failed"
	TerminalLost      TerminalEvent = "lost"
	TerminalEscalated TerminalEvent = "escalated"
	// TerminalDigest pages nothing: the close goes to the daily digest
	// (story 742b).
	TerminalDigest TerminalEvent = "digest"
)

var (
	everyLane = []v1alpha1.Lane(nil)
	cue       = []v1alpha1.Lane{v1alpha1.LaneCuration, v1alpha1.LaneUpgrade, v1alpha1.LaneEscalation}
	anyReason = []string(nil)
)

// terminalRow is one row of the table. A nil lane or reason list matches any
// value; an empty source matches either. Rows are exclusive: the test walks
// every lane and every reason in reasons.go and requires exactly one match.
type terminalRow struct {
	lanes   []v1alpha1.Lane
	state   v1alpha1.OutcomeState
	source  string
	reasons []string
	event   TerminalEvent
}

func (r terminalRow) matches(lane v1alpha1.Lane, state v1alpha1.OutcomeState, source, reason string) bool {
	return (r.lanes == nil || slices.Contains(r.lanes, lane)) && r.state == state &&
		(r.source == "" || r.source == source) && (r.reasons == nil || slices.Contains(r.reasons, reason))
}

// terminalRows is DESIGN-064 6.8's terminal table (N8) with its dated rows.
// The reason of a report is its verb (done, failed, escalate), which no row
// reads: 6.8 writes "—" for it. Every unreported reason is named in each lane
// that can close with it, so a new reason in reasons.go fails the table test
// until it gets a row.
var terminalRows = []terminalRow{
	// The 779 correction of 6.8 (ruling D1 on item 741): a person's stop,
	// before every other failed row, in every lane. It never pages.
	{everyLane, v1alpha1.OutcomeFailed, Unreported, []string{ReasonStopped}, TerminalDigest},

	{[]v1alpha1.Lane{v1alpha1.LaneRemediation}, v1alpha1.OutcomeDone, Report, anyReason, TerminalDigest},
	// A remediation that ends escalated, by its own report (a promoted failed
	// included) or unreported, does not page: the platform's filing pages for
	// it (escalation-started, or escalation-pending and escalation-abandoned).
	{[]v1alpha1.Lane{v1alpha1.LaneRemediation}, v1alpha1.OutcomeEscalated, "", anyReason, TerminalDigest},

	{cue, v1alpha1.OutcomeDone, Report, anyReason, TerminalDigest},
	{cue, v1alpha1.OutcomeFailed, Report, anyReason, TerminalFailed},
	{cue, v1alpha1.OutcomeEscalated, Report, anyReason, TerminalEscalated},
	// A quiet exit.
	{cue, v1alpha1.OutcomeFailed, Unreported, []string{ReasonAgentExited, ReasonExitUnknown, ReasonTurnCap}, TerminalDigest},
	// C8: the plan being busy is not a fault.
	{[]v1alpha1.Lane{v1alpha1.LaneCuration}, v1alpha1.OutcomeFailed, Unreported, []string{ReasonHoldExpired, ReasonQuotaHoldExpired}, TerminalDigest},
	{cue, v1alpha1.OutcomeFailed, Unreported, []string{ReasonSessionLost, ReasonHeartbeatLost, ReasonAgentInterrupted, ReasonPodTerminated}, TerminalLost},
	// 6.8's "every other reason", by name.
	{cue, v1alpha1.OutcomeFailed, Unreported, []string{ReasonTimedOut, ReasonDeadline, ReasonStale, ReasonEscalationBound, ReasonAgentFailed, ReasonNeverStarted, ReasonNoUsableModel, ReasonSuspendedUnreported}, TerminalFailed},
	// "Every other reason" for the lanes whose hold expiry 6.8 does not send
	// to the digest. The CRD gives upgrade and escalation no maxHold, so no
	// operator writes these today.
	{[]v1alpha1.Lane{v1alpha1.LaneUpgrade, v1alpha1.LaneEscalation}, v1alpha1.OutcomeFailed, Unreported, []string{ReasonHoldExpired, ReasonQuotaHoldExpired}, TerminalFailed},
	// Ruling Q4 on item 742 (story 782, a dated row in 6.8): not-charged
	// outside remediation is a platform fault and pages failed. In lane
	// remediation it closes escalated, the row above.
	{cue, v1alpha1.OutcomeFailed, Unreported, []string{ReasonNotCharged}, TerminalFailed},
}

func matchingRows(lane v1alpha1.Lane, state v1alpha1.OutcomeState, source, reason string) []terminalRow {
	var out []terminalRow
	for _, r := range terminalRows {
		if r.matches(lane, state, source, reason) {
			out = append(out, r)
		}
	}
	return out
}

// TerminalPage is the one pure function from a close (its lane, outcome
// state, source and reason) to its terminal page or the digest (DESIGN-064
// 6.8; story 782, ruling Q1 on item 742: derived, not stored). A close no row
// names pages failed (story 782, op782-q-1): no operator writes one today, so
// only a keeper older than its operator can meet one, and a close nobody
// expected is a fault Tom hears about.
func TerminalPage(lane v1alpha1.Lane, state v1alpha1.OutcomeState, source, reason string) TerminalEvent {
	e, _ := TerminalRow(lane, state, source, reason)
	return e
}

// TerminalRow is TerminalPage, and whether a row names the close. A page for
// a close no row names says so in its own fixed text (op782-q-1).
func TerminalRow(lane v1alpha1.Lane, state v1alpha1.OutcomeState, source, reason string) (TerminalEvent, bool) {
	if rows := matchingRows(lane, state, source, reason); len(rows) == 1 {
		return rows[0].event, true
	}
	return TerminalFailed, false
}

// ClosedTerminalPage reads a session's close record: its terminal page or the
// digest, and false while it has no terminal outcome and close record.
func ClosedTerminalPage(s *v1alpha1.AgentSession) (TerminalEvent, bool) {
	if s == nil || !Terminal(s.Status.Outcome) || s.Status.Summon == nil || s.Status.Summon.Closed == nil {
		return "", false
	}
	c := s.Status.Summon.Closed
	return TerminalPage(s.Spec.Lane, s.Status.Outcome.State, c.Source, c.Reason), true
}
