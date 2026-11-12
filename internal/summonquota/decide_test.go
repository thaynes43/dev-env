package summonquota

// The decision proofs of story 783 (slice a of item 743; DESIGN-064 6.9,
// coordinator rulings Q12, Q13 on item 743 comment 7458): fake readings and a
// fixed clock, no store and no goroutine.

import (
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

var t0 = time.Date(2026, 11, 12, 22, 0, 0, 0, time.UTC)

// installLines are the lines haynes-ops is to set: normal 95, bulk 80, the
// weekly line unset, a 15-minute max age (C8).
var installLines = Lines{NormalFiveHour: 95, BulkFiveHour: 80, MaxAge: 15 * time.Minute}

// reading is a fresh Claude reading, two minutes old, with the windows given
// as minutes and used percent; each resets an hour from now.
func reading(windows ...any) Reading {
	r := Reading{ReadAt: t0.Add(-2 * time.Minute)}
	for i := 0; i+1 < len(windows); i += 2 {
		r.Windows = append(r.Windows, protocol.AllowanceWindow{Minutes: int64(windows[i].(int)), UsedPercent: windows[i+1].(string), ResetsAt: t0.Add(time.Hour)})
	}
	return r
}

func TestBulkHoldsPastItsFiveHourLineAndAdmitsAtIt(t *testing.T) {
	for _, tc := range []struct {
		used  string
		admit bool
	}{{"81", false}, {"80.000001", false}, {"80", true}, {"79.5", true}, {"0", true}} {
		d := Decide(v1alpha1.SummonPriorityBulk, reading(FiveHourMinutes, tc.used, SevenDayMinutes, "10"), installLines, t0)
		if d.Admit != tc.admit {
			t.Errorf("bulk at %s %% of the 5-hour window, line 80: admit %v, want %v", tc.used, d.Admit, tc.admit)
		}
		want := WhyWithinLines
		if !tc.admit {
			want = WhyFiveHourPast
		}
		if d.Reason != want || d.FiveHour.Used != tc.used || d.FiveHour.Line != 80 || d.FiveHour.Past == tc.admit || !d.HaveReading || d.Age != 2*time.Minute {
			t.Errorf("bulk at %s %%: %+v", tc.used, d)
		}
	}
}

func TestNormalHoldsPastItsFiveHourLineAndAdmitsAtIt(t *testing.T) {
	for _, tc := range []struct {
		used  string
		admit bool
	}{{"96", false}, {"95.5", false}, {"95", true}, {"81", true}} {
		d := Decide(v1alpha1.SummonPriorityNormal, reading(FiveHourMinutes, tc.used), installLines, t0)
		if d.Admit != tc.admit || d.FiveHour.Line != 95 || d.FiveHour.Used != tc.used {
			t.Errorf("normal at %s %% of the 5-hour window, line 95: %+v, want admit %v", tc.used, d, tc.admit)
		}
	}
	// The bulk lines are not the normal session's: a normal session at 96 %
	// with only the bulk line set is admitted, and no line is reported.
	d := Decide(v1alpha1.SummonPriorityNormal, reading(FiveHourMinutes, "96"), Lines{BulkFiveHour: 80, MaxAge: time.Minute}, t0)
	if !d.Admit || d.Reason != WhyNoLine {
		t.Errorf("normal with its line unset: %+v", d)
	}
}

func TestBulkWeeklyLineOnlyWhenItsFlagIsSet(t *testing.T) {
	r := reading(FiveHourMinutes, "10", SevenDayMinutes, "91")
	// Unset (the install's value): the weekly window decides nothing.
	d := Decide(v1alpha1.SummonPriorityBulk, r, installLines, t0)
	if !d.Admit || d.Reason != WhyWithinLines || d.SevenDay.Line != 0 || d.SevenDay.Past {
		t.Errorf("bulk at 91 %% weekly with the weekly line unset: %+v", d)
	}
	// Set to 90: held by the weekly window.
	weekly := installLines
	weekly.BulkSevenDay = 90
	d = Decide(v1alpha1.SummonPriorityBulk, r, weekly, t0)
	if d.Admit || d.Reason != WhySevenDayPast || d.SevenDay.Line != 90 || !d.SevenDay.Past || d.SevenDay.Used != "91" {
		t.Errorf("bulk at 91 %% weekly with the weekly line at 90: %+v", d)
	}
	// At the line: admitted.
	d = Decide(v1alpha1.SummonPriorityBulk, reading(FiveHourMinutes, "10", SevenDayMinutes, "90"), weekly, t0)
	if !d.Admit {
		t.Errorf("bulk at 90 %% weekly with the weekly line at 90: %+v", d)
	}
	// The weekly line is never a normal session's.
	d = Decide(v1alpha1.SummonPriorityNormal, r, weekly, t0)
	if !d.Admit || d.SevenDay.Line != 0 {
		t.Errorf("normal at 91 %% weekly: %+v", d)
	}
	// Both past: held, and the 5-hour window is named first.
	d = Decide(v1alpha1.SummonPriorityBulk, reading(FiveHourMinutes, "85", SevenDayMinutes, "91"), weekly, t0)
	if d.Admit || d.Reason != WhyFiveHourPast || !d.FiveHour.Past || !d.SevenDay.Past {
		t.Errorf("bulk past both lines: %+v", d)
	}
}

func TestUrgentAlwaysAdmits(t *testing.T) {
	all := Lines{NormalFiveHour: 1, BulkFiveHour: 1, BulkSevenDay: 1, MaxAge: time.Hour}
	for name, r := range map[string]Reading{
		"spent":      reading(FiveHourMinutes, "100", SevenDayMinutes, "100"),
		"no reading": {Missing: WhyNoReading},
		"read error": {Missing: WhyReadError},
	} {
		if d := Decide(v1alpha1.SummonPriorityUrgent, r, all, t0); !d.Admit || d.Reason != WhyUrgent {
			t.Errorf("urgent, %s: %+v", name, d)
		}
	}
}

// Ruling Q13 (item 743 comment 7458) and C8: a stale, missing or unreadable
// reading admits, for every priority, and says why. A fresh reading without
// a window, or with a window that has reset since it was read (the
// coordinator's answer to op783-q-1, gap B), counts as missing for that
// window's line only.
func TestNoUsableReadingAdmitsAndSaysWhy(t *testing.T) {
	weekly := installLines
	weekly.BulkSevenDay = 90
	stale := reading(FiveHourMinutes, "99", SevenDayMinutes, "99")
	stale.ReadAt = t0.Add(-15*time.Minute - time.Second)
	reset := reading(FiveHourMinutes, "99", SevenDayMinutes, "10")
	reset.Windows[0].ResetsAt = t0
	for _, tc := range []struct {
		name string
		r    Reading
		want Why
	}{
		{"no reading", Reading{Missing: WhyNoReading}, WhyNoReading},
		{"read error", Reading{Missing: WhyReadError}, WhyReadError},
		{"read timeout", Reading{Missing: WhyReadTimeout}, WhyReadTimeout},
		{"zero reading", Reading{}, WhyNoReading},
		{"stale", stale, WhyStale},
		{"no 5-hour window", reading(SevenDayMinutes, "10"), WhyWindowMissing},
		{"5-hour percent not canonical", reading(FiveHourMinutes, "99.0"), WhyWindowMissing},
		{"5-hour window reset", reset, WhyWindowReset},
	} {
		for _, p := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
			d := Decide(p, tc.r, weekly, t0)
			if !d.Admit || d.Reason != tc.want || d.FiveHour.Missing != tc.want || d.FiveHour.Past {
				t.Errorf("%s, %s: %+v, want admitted for %s", tc.name, p, d, tc.want)
			}
		}
	}
	// Missing for its own line only: a fresh reading with no 5-hour window
	// still holds bulk past the weekly line when that line is set.
	d := Decide(v1alpha1.SummonPriorityBulk, reading(SevenDayMinutes, "91"), weekly, t0)
	if d.Admit || d.Reason != WhySevenDayPast || d.FiveHour.Missing != WhyWindowMissing {
		t.Errorf("bulk, no 5-hour window, 91 %% weekly at line 90: %+v", d)
	}
	// And the weekly window that reset counts as missing for the weekly line.
	r := reading(FiveHourMinutes, "10", SevenDayMinutes, "99")
	r.Windows[1].ResetsAt = t0.Add(-time.Second)
	if d := Decide(v1alpha1.SummonPriorityBulk, r, weekly, t0); !d.Admit || d.SevenDay.Missing != WhyWindowReset || d.Reason != WhyWindowReset {
		t.Errorf("bulk, weekly window reset: %+v", d)
	}
}

// "Older than --summon-usage-max-age" is strict: a reading exactly that old
// is fresh. A reading time ahead of the clock (another node's) is age zero.
func TestReadingAgeAtTheMaxAge(t *testing.T) {
	for _, tc := range []struct {
		age   time.Duration
		admit bool
		want  time.Duration
	}{{15 * time.Minute, false, 15 * time.Minute}, {15*time.Minute + time.Second, true, 15*time.Minute + time.Second}, {-time.Minute, false, 0}} {
		r := reading(FiveHourMinutes, "99")
		r.ReadAt = t0.Add(-tc.age)
		d := Decide(v1alpha1.SummonPriorityBulk, r, installLines, t0)
		if d.Admit != tc.admit || d.Age != tc.want || !d.HaveReading {
			t.Errorf("reading %s old: %+v, want admit %v, age %s", tc.age, d, tc.admit, tc.want)
		}
	}
}

func TestEveryLineUnsetAdmitsAsToday(t *testing.T) {
	for _, p := range []v1alpha1.SummonPriority{v1alpha1.SummonPriorityUrgent, v1alpha1.SummonPriorityNormal, v1alpha1.SummonPriorityBulk} {
		if d := Decide(p, reading(FiveHourMinutes, "100", SevenDayMinutes, "100"), Lines{}, t0); !d.Admit {
			t.Errorf("%s with no line set: %+v", p, d)
		}
	}
	if (Lines{}).Set() || !installLines.Set() || !(Lines{BulkSevenDay: 1}).Set() {
		t.Error("Lines.Set")
	}
	if !installLines.For(v1alpha1.SummonPriorityBulk) || !installLines.For(v1alpha1.SummonPriorityNormal) || installLines.For(v1alpha1.SummonPriorityUrgent) ||
		(Lines{BulkFiveHour: 80}).For(v1alpha1.SummonPriorityNormal) || !(Lines{BulkSevenDay: 80}).For(v1alpha1.SummonPriorityBulk) {
		t.Error("Lines.For")
	}
}

// A priority the gate does not know is admitted: the schema allows only the
// three, and the gate fails open.
func TestUnknownPriorityAdmits(t *testing.T) {
	if d := Decide("", reading(FiveHourMinutes, "100"), installLines, t0); !d.Admit || d.Reason != WhyUnknownPriority {
		t.Errorf("empty priority: %+v", d)
	}
}
