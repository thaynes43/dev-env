package protocol

import (
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Allowance reading sources (DESIGN-059 4.1, 4.2). Each source belongs to one
// agent: a Codex rollout to codex, the Claude statusline to claude.
const (
	AllowanceSourceCodexRollout     = "codex-rollout"
	AllowanceSourceClaudeStatusline = "claude-statusline"
)

// Bounds on one allowance reading (DESIGN-059 4.4). agentd builds readings
// inside them, and the operator checks them again: a reading comes from a
// session pod and is not trusted.
const (
	// MaxAllowanceWindows is the most windows one reading carries: Claude
	// reports a five-hour and a seven-day window, Codex a primary and a
	// secondary.
	MaxAllowanceWindows = 2
	// MaxAllowanceWindowMinutes is the longest window accepted: 31 days.
	MaxAllowanceWindowMinutes = 31 * 24 * 60
	// AllowanceResetSlack is how far past one window length a reset may lie.
	// A provider may round its reset up to the next hour, so a reading taken
	// early in a window can show a reset slightly more than one window away.
	AllowanceResetSlack = time.Hour
	// WeeklyAllowanceMinutes is the length of the weekly window. The weekly
	// window is picked by this length, never by its position.
	WeeklyAllowanceMinutes = 7 * 24 * 60
)

// AllowanceReading is one passive allowance reading: the numbers a running
// session's own CLI received with a model response (DESIGN-059). It carries no
// account identifier, plan or credit: those are never read or forwarded. An
// operator that does not know the field ignores it.
type AllowanceReading struct {
	// Provider is the session's agent: claude or codex.
	Provider string `json:"provider"`
	// Source is where agentd read it: codex-rollout or claude-statusline.
	Source string `json:"source"`
	// ReadAt is when the CLI received the numbers: the rollout record's time
	// for Codex, the callback time for Claude, kept when no value changed.
	ReadAt time.Time `json:"readAt"`
	// Windows are one or two windows of different lengths.
	Windows []AllowanceWindow `json:"windows"`
}

// AllowanceWindow is one allowance window of a reading.
type AllowanceWindow struct {
	// Minutes is the window's length.
	Minutes int64 `json:"minutes"`
	// UsedPercent is the percent used, 0 to 100, as a canonical decimal with
	// at most six fraction digits and no trailing zero ("34", "12.5").
	UsedPercent string `json:"usedPercent"`
	// ResetsAt is when the window resets, in whole seconds.
	ResetsAt time.Time `json:"resetsAt"`
}

// allowancePercent is 0 to 99.999999, or exactly 100, with no leading zero,
// no trailing fraction zero and at most six fraction digits.
var allowancePercent = regexp.MustCompile(`^(100|[1-9]?[0-9](\.[0-9]{0,5}[1-9])?)$`)

// ValidAllowancePercent reports whether s is a canonical used percent.
func ValidAllowancePercent(s string) bool { return allowancePercent.MatchString(s) }

// allowanceNumber is the JSON number grammar, with a short exponent so the
// parse stays small.
var allowanceNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]{1,3})?$`)

// CanonicalAllowancePercent rewrites a JSON number's text as a canonical used
// percent: rounded to six fraction digits, trailing zeros removed. It refuses
// a value below 0 or above 100, and anything that is not a JSON number.
func CanonicalAllowancePercent(text string) (string, bool) {
	if len(text) > 64 || !allowanceNumber.MatchString(text) {
		return "", false
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok || r.Sign() < 0 || r.Cmp(big.NewRat(100, 1)) > 0 {
		return "", false
	}
	s := r.FloatString(6)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s, ValidAllowancePercent(s)
}

// AllowanceSourceOf is the one source an agent's readings may name.
func AllowanceSourceOf(agent string) string {
	switch agent {
	case AgentClaude:
		return AllowanceSourceClaudeStatusline
	case AgentCodex:
		return AllowanceSourceCodexRollout
	default:
		return ""
	}
}

// Valid checks the reading's shape and the bounds that need no clock: a known
// provider and its own source, one or two windows of different lengths, each
// with a canonical percent and a whole-second reset that lies after ReadAt and
// at most one window length (plus AllowanceResetSlack) after it. The caller
// checks ReadAt against its own clock and the pod's start.
func (a AllowanceReading) Valid() bool {
	if a.Source == "" || AllowanceSourceOf(a.Provider) != a.Source || a.ReadAt.IsZero() || a.ReadAt.Unix() <= 0 {
		return false
	}
	if len(a.Windows) == 0 || len(a.Windows) > MaxAllowanceWindows {
		return false
	}
	seen := map[int64]bool{}
	for _, w := range a.Windows {
		if w.Minutes <= 0 || w.Minutes > MaxAllowanceWindowMinutes || seen[w.Minutes] {
			return false
		}
		seen[w.Minutes] = true
		if !ValidAllowancePercent(w.UsedPercent) || w.ResetsAt.IsZero() || w.ResetsAt.Nanosecond() != 0 {
			return false
		}
		limit := a.ReadAt.Add(time.Duration(w.Minutes)*time.Minute + AllowanceResetSlack)
		if !w.ResetsAt.After(a.ReadAt) || w.ResetsAt.After(limit) {
			return false
		}
	}
	return true
}

// SameValues reports whether two readings carry the same windows, in any
// order. Their times and sources are not compared.
func (a AllowanceReading) SameValues(b AllowanceReading) bool {
	if len(a.Windows) != len(b.Windows) {
		return false
	}
	for _, w := range a.Windows {
		found := false
		for _, v := range b.Windows {
			if w.Minutes == v.Minutes && w.UsedPercent == v.UsedPercent && w.ResetsAt.Equal(v.ResetsAt) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// AllowanceResetsAt reads a reset given as whole Unix seconds. It refuses a
// fraction, an exponent, a sign, zero and anything past the year 9999.
func AllowanceResetsAt(text string) (time.Time, bool) {
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 || n > 253402300799 || strconv.FormatInt(n, 10) != text {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}
