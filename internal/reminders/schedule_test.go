package reminders

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func ny(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("no tzdata: %v", err)
	}
	return loc
}

func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// The explicit DST choices of DESIGN-032, in America/New_York, 2026: the clocks
// go forward at 02:00 on March 8 and back at 02:00 on November 1.
func TestResolveGapAndFold(t *testing.T) {
	loc := ny(t)
	for name, c := range map[string]struct {
		y         int
		mo        time.Month
		d, h, mi  int
		wantUTC   string
		wantLocal string
	}{
		"an ordinary winter time":     {2026, 1, 15, 9, 0, "2026-01-15T14:00:00Z", "09:00 EST"},
		"an ordinary summer time":     {2026, 7, 15, 9, 0, "2026-07-15T13:00:00Z", "09:00 EDT"},
		"the gap shifts forward":      {2026, 3, 8, 2, 30, "2026-03-08T07:30:00Z", "03:30 EDT"},
		"the first minute of the gap": {2026, 3, 8, 2, 0, "2026-03-08T07:00:00Z", "03:00 EDT"},
		"just before the gap":         {2026, 3, 8, 1, 59, "2026-03-08T06:59:00Z", "01:59 EST"},
		"just after the gap":          {2026, 3, 8, 3, 0, "2026-03-08T07:00:00Z", "03:00 EDT"},
		"the fold takes the first":    {2026, 11, 1, 1, 30, "2026-11-01T05:30:00Z", "01:30 EDT"},
		"the fold's first minute":     {2026, 11, 1, 1, 0, "2026-11-01T05:00:00Z", "01:00 EDT"},
		"just after the fold":         {2026, 11, 1, 2, 0, "2026-11-01T07:00:00Z", "02:00 EST"},
	} {
		t.Run(name, func(t *testing.T) {
			got := Resolve(loc, c.y, c.mo, c.d, c.h, c.mi)
			if !got.Equal(utc(c.wantUTC)) {
				t.Errorf("Resolve = %s, want %s", got.UTC().Format(time.RFC3339), c.wantUTC)
			}
			if l := got.Format("15:04 MST"); l != c.wantLocal {
				t.Errorf("local = %s, want %s", l, c.wantLocal)
			}
		})
	}
}

func spec(due, zone string, rec *v1alpha1.ReminderRecurrence) v1alpha1.ReminderSpec {
	return v1alpha1.ReminderSpec{Title: "t", Owner: "session/x", Due: due, TimeZone: zone, Recurrence: rec, Targets: []string{"pushover:default"}}
}

func series(t *testing.T, sp v1alpha1.ReminderSpec, n int) []string {
	t.Helper()
	sch, err := ScheduleOf(sp)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	from := time.Time{}
	for range n {
		o, ok := sch.Next(from)
		if !ok {
			break
		}
		out = append(out, o.At.UTC().Format("2006-01-02T15:04Z"))
		from = o.At.Add(time.Minute)
	}
	return out
}

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestDailyKeepsTheWallClockAcrossTheGapAndTheFold(t *testing.T) {
	ny(t)
	rec := &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}
	// 02:30 local: EST, then the gap day (03:30 EDT, the same instant), then EDT.
	eq(t, series(t, spec("2026-03-07T02:30", "America/New_York", rec), 3),
		[]string{"2026-03-07T07:30Z", "2026-03-08T07:30Z", "2026-03-09T06:30Z"})
	// 01:30 local across the fold: EDT every day, the first of the two on the fold day.
	eq(t, series(t, spec("2026-10-31T01:30", "America/New_York", rec), 3),
		[]string{"2026-10-31T05:30Z", "2026-11-01T05:30Z", "2026-11-02T06:30Z"})
}

func TestMonthlyClampsToTheLastDay(t *testing.T) {
	ny(t)
	rec := &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 31}
	eq(t, series(t, spec("2026-01-31T09:00", "UTC", rec), 5),
		[]string{"2026-01-31T09:00Z", "2026-02-28T09:00Z", "2026-03-31T09:00Z", "2026-04-30T09:00Z", "2026-05-31T09:00Z"})
	// Leap year: February 29.
	eq(t, series(t, spec("2028-01-30T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 30}), 2),
		[]string{"2028-01-30T09:00Z", "2028-02-29T09:00Z"})
	// The day of the due time when ByMonthDay is zero.
	eq(t, series(t, spec("2026-10-29T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly}), 3),
		[]string{"2026-10-29T09:00Z", "2026-11-29T09:00Z", "2026-12-29T09:00Z"})
	// A due date after this month's day starts next month.
	eq(t, series(t, spec("2026-10-29T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 5}), 2),
		[]string{"2026-11-05T09:00Z", "2026-12-05T09:00Z"})
}

func TestWeeklyByDay(t *testing.T) {
	rec := &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"TH", "MO"}}
	// 2026-10-28 is a Wednesday.
	eq(t, series(t, spec("2026-10-28T09:00", "UTC", rec), 4),
		[]string{"2026-10-29T09:00Z", "2026-11-02T09:00Z", "2026-11-05T09:00Z", "2026-11-09T09:00Z"})
	// No ByDay: the weekday of the due time.
	eq(t, series(t, spec("2026-10-28T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly}), 2),
		[]string{"2026-10-28T09:00Z", "2026-11-04T09:00Z"})
}

func TestUntilIsInclusiveAndCountEnds(t *testing.T) {
	until := metav1.NewTime(utc("2026-11-02T09:00:00Z"))
	eq(t, series(t, spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Until: &until}), 9),
		[]string{"2026-10-31T09:00Z", "2026-11-01T09:00Z", "2026-11-02T09:00Z"})
	eq(t, series(t, spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}), 9),
		[]string{"2026-10-31T09:00Z", "2026-11-01T09:00Z"})
}

func TestOneShotAndNext(t *testing.T) {
	sp := spec("2026-10-31T09:00", "UTC", nil)
	eq(t, series(t, sp, 3), []string{"2026-10-31T09:00Z"})
	sch, _ := ScheduleOf(sp)
	if _, ok := sch.Next(utc("2026-10-31T09:01:00Z")); ok {
		t.Error("a one-shot past notBefore still has an occurrence")
	}
	rec, _ := ScheduleOf(spec("2020-01-01T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 3}))
	if _, ok := rec.Next(utc("2026-01-01T00:00:00Z")); ok {
		t.Error("a finished count still has an occurrence")
	}
	// The index is the series index, wherever the search began.
	d, _ := ScheduleOf(spec("2026-10-01T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}))
	if o, _ := d.Next(utc("2026-10-10T00:00:00Z")); o.N != 10 {
		t.Errorf("occurrence index %d, want 10", o.N)
	}
}

func TestScheduleOfRefuses(t *testing.T) {
	for name, sp := range map[string]v1alpha1.ReminderSpec{
		"a bad zone":      spec("2026-10-31T09:00", "Mars/Olympus", nil),
		"Local":           spec("2026-10-31T09:00", "Local", nil),
		"a bad date":      spec("2026-13-31T09:00", "UTC", nil),
		"a bad freq":      spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "yearly"}),
		"a daily byDay":   spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "daily", ByDay: []string{"MO"}}),
		"a bad weekday":   spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"XX"}}),
		"a weekly day":    spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "weekly", ByMonthDay: 3}),
		"a monthly byDay": spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "monthly", ByDay: []string{"MO"}}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ScheduleOf(sp); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestRRuleAndHashes(t *testing.T) {
	until := metav1.NewTime(utc("2026-12-31T23:59:59Z"))
	sch, _ := ScheduleOf(spec("2026-10-28T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"TH", "MO"}, Until: &until}))
	if got := sch.RRule(); got != "FREQ=WEEKLY;BYDAY=MO,TH;UNTIL=20261231T235959Z" {
		t.Errorf("rrule %q", got)
	}
	one, _ := ScheduleOf(spec("2026-10-28T09:00", "UTC", nil))
	if one.RRule() != "" {
		t.Error("a one-shot has an rrule")
	}
	a, b := spec("2026-10-28T09:00", "UTC", nil), spec("2026-10-28T09:00", "UTC", nil)
	b.Title = "other"
	if PlanHash(a) != PlanHash(b) || CalendarHash(a) == CalendarHash(b) {
		t.Error("the title is a calendar field, not a plan field")
	}
	b.Due = "2026-10-29T09:00"
	if PlanHash(a) == PlanHash(b) {
		t.Error("the due time is a plan field")
	}
	if id := EventID("ns", "rem-1"); len(id) != 32 || id != EventID("ns", "rem-1") || id == EventID("ns", "rem-2") {
		t.Errorf("event id %q", id)
	}
}

func TestTargets(t *testing.T) {
	if err := CheckTargets([]string{"pushover:default", "calendar:haynesnetwork"}, []string{"haynesnetwork"}); err != nil {
		t.Errorf("allowed: %v", err)
	}
	for name, c := range map[string]struct {
		targets, refs []string
	}{
		"none":                  {nil, nil},
		"an address":            {[]string{"calendar:tom@haynesnetwork.com"}, []string{"haynesnetwork"}},
		"a URL":                 {[]string{"https://example.com/hook"}, nil},
		"another pushover":      {[]string{"pushover:other"}, nil},
		"a calendar not listed": {[]string{"calendar:other"}, []string{"haynesnetwork"}},
		"no calendar at all":    {[]string{"calendar:haynesnetwork"}, nil},
		"a duplicate":           {[]string{"pushover:default", "pushover:default"}, nil},
		"a raw user key":        {[]string{"uQiRzpo4DXghDmr9QzzfQu27cmVRsG"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			if err := CheckTargets(c.targets, c.refs); err == nil {
				t.Error("accepted")
			}
		})
	}
}
