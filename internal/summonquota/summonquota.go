// Package summonquota is the operator leader's quota gate for summoned
// sessions (DESIGN-064 6.9, story 783, slice a of item 743). One plan serves
// Tom's own sessions and the summoned ones: while the plan's usage window is
// past a line set by an operator flag, the leader holds a normal session (an
// upgrade) or a bulk one (curation), and never an urgent one (an escalation
// or a remediation).
//
// Decide is the one decision, pure: a priority, the newest reading, the lines
// and the clock in; admit or hold, with what it read, out. Gate (gate.go) is
// the admission's hook around it: one read per 30 seconds at most, a bounded
// read, and one log line per decision.
//
// The gate fails open (ruling Q13 on item 743 comment 7458, and C8 in
// DESIGN-064 section 10): an unset line is off; a reading that is stale,
// missing or unreadable admits for every line; and a fresh reading that lacks
// a window, or whose window has reset since it was read, admits for that
// window's line. The readings come from running interactive Claude sessions
// (DESIGN-059), so they go stale exactly when nobody is working.
package summonquota

import (
	"math/big"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The two windows, picked by their length, never by their position.
const (
	FiveHourMinutes = 5 * 60
	SevenDayMinutes = protocol.WeeklyAllowanceMinutes
)

// Why is a decision's reason, or why a line had no usable window. Each is a
// fixed code, safe to log.
type Why string

const (
	// WhyUrgent: an urgent session is never gated.
	WhyUrgent Why = "urgent"
	// WhyUnknownPriority: a priority the gate does not know is admitted.
	WhyUnknownPriority Why = "unknown-priority"
	// WhyNoLine: no line is set for the session's priority.
	WhyNoLine Why = "no-line"
	// WhyWithinLines: every line set had a usable window, or none of them
	// was passed.
	WhyWithinLines Why = "within-lines"
	// WhyFiveHourPast and WhySevenDayPast hold the session.
	WhyFiveHourPast Why = "five-hour-past-line"
	WhySevenDayPast Why = "seven-day-past-line"

	// Why there is no reading at all.
	WhyNoReading   Why = "no-reading"
	WhyReadError   Why = "read-error"
	WhyReadTimeout Why = "read-timeout"
	// WhyStale: the reading is older than Lines.MaxAge.
	WhyStale Why = "stale"
	// Why one window of a fresh reading is not used.
	WhyWindowMissing Why = "window-missing"
	WhyWindowReset   Why = "window-reset"
)

// Lines are the operator's flags. A zero line is off (ruling Q12: no code
// defaults); a set line is 1 to 100, checked when the operator starts.
type Lines struct {
	// NormalFiveHour is --summon-normal-max-five-hour.
	NormalFiveHour int
	// BulkFiveHour is --summon-bulk-max-five-hour.
	BulkFiveHour int
	// BulkSevenDay is --summon-bulk-max-seven-day.
	BulkSevenDay int
	// MaxAge is --summon-usage-max-age: a reading older than this is stale.
	MaxAge time.Duration
}

// Set reports whether any line is set. With none the gate reads nothing.
func (l Lines) Set() bool { return l.NormalFiveHour > 0 || l.BulkFiveHour > 0 || l.BulkSevenDay > 0 }

// For reports whether a line is set for priority p, so that a reading is
// needed to decide.
func (l Lines) For(p v1alpha1.SummonPriority) bool {
	five, seven := l.of(p)
	return five > 0 || seven > 0
}

// of is the 5-hour and the 7-day line of priority p.
func (l Lines) of(p v1alpha1.SummonPriority) (fiveHour, sevenDay int) {
	switch p {
	case v1alpha1.SummonPriorityNormal:
		return l.NormalFiveHour, 0
	case v1alpha1.SummonPriorityBulk:
		return l.BulkFiveHour, l.BulkSevenDay
	}
	return 0, 0
}

// Reading is the newest reading of the session's provider, or why there is
// none. A Reading with Missing empty and a zero ReadAt is no reading.
type Reading struct {
	// Missing is why there is no reading: WhyNoReading, WhyReadError or
	// WhyReadTimeout. Empty when ReadAt and Windows hold one.
	Missing Why
	// ReadAt is when the numbers last changed (DESIGN-059 4.4): a reading
	// can look older than it is, never newer.
	ReadAt  time.Time
	Windows []protocol.AllowanceWindow
}

// Window is what one window showed, for the log: its used percent as the
// reading has it ("" when there is none), its line (0 off), whether the
// line was passed, and why a set line had no usable window.
type Window struct {
	Used    string
	Line    int
	Past    bool
	Missing Why
}

// Decision is Decide's answer.
type Decision struct {
	Admit    bool
	Priority v1alpha1.SummonPriority
	// Reason is the hold's window when held. When admitted it is the first
	// of: urgent, unknown-priority, no-line, the reading's Missing or stale,
	// the 5-hour line's Missing, the 7-day line's Missing, within-lines.
	Reason             Why
	FiveHour, SevenDay Window
	// HaveReading says Age is the reading's age at now (never negative).
	HaveReading bool
	Age         time.Duration
}

// Decide admits or holds a summoned session of priority p by the reading r,
// the lines l and the clock now. "Past" a line is strictly greater than it.
func Decide(p v1alpha1.SummonPriority, r Reading, l Lines, now time.Time) Decision {
	d := Decision{Admit: true, Priority: p}
	switch p {
	case v1alpha1.SummonPriorityUrgent:
		d.Reason = WhyUrgent
		return d
	case v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk:
	default:
		d.Reason = WhyUnknownPriority
		return d
	}
	d.FiveHour.Line, d.SevenDay.Line = l.of(p)
	if d.FiveHour.Line <= 0 && d.SevenDay.Line <= 0 {
		d.Reason = WhyNoLine
		return d
	}
	missing := r.Missing
	if missing == "" && r.ReadAt.IsZero() {
		missing = WhyNoReading
	}
	if missing == "" {
		d.HaveReading = true
		d.Age = max(now.Sub(r.ReadAt), 0)
		d.FiveHour.Used, d.FiveHour.Missing = window(r.Windows, FiveHourMinutes, now)
		d.SevenDay.Used, d.SevenDay.Missing = window(r.Windows, SevenDayMinutes, now)
		if d.Age > l.MaxAge {
			missing = WhyStale
		}
	}
	for _, w := range []*Window{&d.FiveHour, &d.SevenDay} {
		switch {
		case w.Line <= 0:
			// An unset line decides nothing; its window is only reported.
			w.Missing = ""
		case missing != "":
			w.Missing = missing
		case w.Missing == "":
			w.Past = past(w.Used, w.Line)
		}
	}
	switch {
	case d.FiveHour.Past:
		d.Admit, d.Reason = false, WhyFiveHourPast
	case d.SevenDay.Past:
		d.Admit, d.Reason = false, WhySevenDayPast
	case missing != "":
		d.Reason = missing
	case d.FiveHour.Missing != "":
		d.Reason = d.FiveHour.Missing
	case d.SevenDay.Missing != "":
		d.Reason = d.SevenDay.Missing
	default:
		d.Reason = WhyWithinLines
	}
	return d
}

// window finds the window of the given length and its used percent. A
// window whose reset is at or before now has reset since it was read: its
// numbers describe an old window, so it has no percent and counts as missing (the coordinator's answer to op783-q-1,
// gap B; agentd leaves such windows out when it builds a reading).
func window(ws []protocol.AllowanceWindow, minutes int64, now time.Time) (string, Why) {
	for _, w := range ws {
		if w.Minutes != minutes {
			continue
		}
		if !w.ResetsAt.After(now) {
			return "", WhyWindowReset
		}
		if !protocol.ValidAllowancePercent(w.UsedPercent) {
			// The store keeps only canonical percents; anything else is no
			// window.
			return "", WhyWindowMissing
		}
		return w.UsedPercent, ""
	}
	return "", WhyWindowMissing
}

// past reports whether the canonical percent used is strictly greater than
// line, exactly: "80.000001" is past 80, "80" is not.
func past(used string, line int) bool {
	u, ok := new(big.Rat).SetString(used)
	return ok && u.Cmp(big.NewRat(int64(line), 1)) > 0
}
