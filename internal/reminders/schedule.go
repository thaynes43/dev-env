// Package reminders is the toolkit's reminder engine (DESIGN-032, OpenProject
// #206): the schedule arithmetic, the target rules and a leader-only scheduler
// over the Reminder CRD. It depends on no provider and no process: Pushover and
// the calendar come in as interfaces, so the keeper wires the real sender and
// the tests wire fakes.
package reminders

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // the operator and keeper images may carry no zoneinfo; zones are the point

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// DueLayout is the layout of ReminderSpec.Due: a local wall time, minute
// resolution, no offset.
const DueLayout = "2006-01-02T15:04"

// maxScan bounds how many calendar days one search walks. A daily reminder
// that began 270 years ago is not a reminder.
const maxScan = 100000

var weekdays = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

var weekdayCodes = [...]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Occurrence is one firing: its 1-based index in the series and its instant.
type Occurrence struct {
	N  int
	At time.Time
}

// Schedule is a parsed, validated ReminderSpec schedule.
type Schedule struct {
	loc          *time.Location
	y            int
	mo           time.Month
	d, h, mi     int
	freq         string
	days         map[time.Weekday]bool
	monthDay     int
	until        *time.Time
	count        int
	zone, due    string
	byDaySorted  []string
	hasRecurrent bool
}

// ScheduleOf parses the schedule fields of a spec. It is the one validation the
// API and the scheduler share, so a spec the API accepted is one the scheduler
// can plan.
func ScheduleOf(spec v1alpha1.ReminderSpec) (*Schedule, error) {
	loc, err := time.LoadLocation(spec.TimeZone)
	if err != nil || spec.TimeZone == "" || spec.TimeZone == "Local" {
		return nil, fmt.Errorf("timeZone %q is not an IANA zone name such as America/New_York", spec.TimeZone)
	}
	t, err := time.Parse(DueLayout, spec.Due)
	if err != nil {
		return nil, fmt.Errorf("due %q is not a local time such as 2026-11-02T09:00", spec.Due)
	}
	s := &Schedule{loc: loc, y: t.Year(), mo: t.Month(), d: t.Day(), h: t.Hour(), mi: t.Minute(), zone: spec.TimeZone, due: spec.Due}
	rec := spec.Recurrence
	if rec == nil {
		return s, nil
	}
	s.hasRecurrent = true
	s.freq = rec.Freq
	switch rec.Freq {
	case v1alpha1.ReminderDaily:
		if len(rec.ByDay) > 0 || rec.ByMonthDay != 0 {
			return nil, errors.New("a daily recurrence takes neither byDay nor byMonthDay")
		}
	case v1alpha1.ReminderWeekly:
		if rec.ByMonthDay != 0 {
			return nil, errors.New("byMonthDay is for a monthly recurrence")
		}
		s.days = map[time.Weekday]bool{}
		for _, code := range rec.ByDay {
			wd, ok := weekdays[code]
			if !ok {
				return nil, fmt.Errorf("byDay %q is not MO, TU, WE, TH, FR, SA or SU", code)
			}
			s.days[wd] = true
		}
		if len(s.days) == 0 {
			s.days[time.Date(s.y, s.mo, s.d, 0, 0, 0, 0, time.UTC).Weekday()] = true
		}
		for wd := time.Sunday; wd <= time.Saturday; wd++ {
			if s.days[wd] {
				s.byDaySorted = append(s.byDaySorted, weekdayCodes[wd])
			}
		}
	case v1alpha1.ReminderMonthly:
		if len(rec.ByDay) > 0 {
			return nil, errors.New("byDay is for a weekly recurrence")
		}
		s.monthDay = int(rec.ByMonthDay)
		if s.monthDay == 0 {
			s.monthDay = s.d
		}
		if s.monthDay < 1 || s.monthDay > 31 {
			return nil, fmt.Errorf("byMonthDay %d is not 1 to 31", s.monthDay)
		}
	default:
		return nil, fmt.Errorf("freq %q is not daily, weekly or monthly", rec.Freq)
	}
	if rec.Until != nil && rec.Count != 0 {
		return nil, errors.New("until and count are exclusive")
	}
	if rec.Until != nil {
		u := rec.Until.UTC()
		s.until = &u
	}
	if rec.Count < 0 || rec.Count > 1000 {
		return nil, fmt.Errorf("count %d is not 1 to 1000", rec.Count)
	}
	s.count = int(rec.Count)
	return s, nil
}

// Recurring reports whether the schedule repeats.
func (s *Schedule) Recurring() bool { return s.hasRecurrent }

// Resolve turns a local wall time into an instant, choosing explicitly what the
// zone leaves ambiguous (DESIGN-032):
//
//   - a time the zone skips (a spring-forward gap) is read with the offset in
//     force before the gap, so it lands one gap-length later: 02:30 in a gap
//     from 02:00 to 03:00 fires at 03:30. This is RFC 5545's rule and Google
//     Calendar's;
//   - a time the zone repeats (a fall-back fold) is the first of the two, the
//     one before the clocks go back.
func Resolve(loc *time.Location, y int, mo time.Month, d, h, mi int) time.Time {
	naive := time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
	offset := func(at time.Time) int { _, o := at.In(loc).Zone(); return o }
	// Transitions are months apart and offsets differ by under a day, so the
	// offsets a day either side are the two a wall time near a transition can have.
	before, after := offset(naive.Add(-24*time.Hour)), offset(naive.Add(24*time.Hour))
	var valid []time.Time
	for _, off := range slices.Compact([]int{before, after}) {
		t := naive.Add(-time.Duration(off) * time.Second)
		l := t.In(loc)
		if l.Year() == y && l.Month() == mo && l.Day() == d && l.Hour() == h && l.Minute() == mi {
			valid = append(valid, t)
		}
	}
	switch len(valid) {
	case 0:
		return naive.Add(-time.Duration(before) * time.Second).In(loc)
	case 1:
		return valid[0].In(loc)
	}
	if valid[1].Before(valid[0]) {
		valid[0] = valid[1]
	}
	return valid[0].In(loc)
}

func daysIn(y int, mo time.Month) int { return time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// each calls fn with every occurrence in order until fn returns false or the
// series ends. Every occurrence is derived from the start, so an index never
// depends on when the search began.
func (s *Schedule) each(fn func(Occurrence) bool) {
	emit := func(n int, y int, mo time.Month, d int) bool {
		at := Resolve(s.loc, y, mo, d, s.h, s.mi)
		if s.until != nil && at.After(*s.until) {
			return false
		}
		return fn(Occurrence{N: n, At: at})
	}
	if !s.hasRecurrent {
		emit(1, s.y, s.mo, s.d)
		return
	}
	start := time.Date(s.y, s.mo, s.d, 0, 0, 0, 0, time.UTC)
	n := 0
	switch s.freq {
	case v1alpha1.ReminderDaily, v1alpha1.ReminderWeekly:
		for k := range maxScan {
			day := start.AddDate(0, 0, k)
			if s.freq == v1alpha1.ReminderWeekly && !s.days[day.Weekday()] {
				continue
			}
			n++
			if s.count > 0 && n > s.count {
				return
			}
			if !emit(n, day.Year(), day.Month(), day.Day()) {
				return
			}
		}
	case v1alpha1.ReminderMonthly:
		// A month lacking the day uses its last day (clamping), and the first
		// month whose day is before the due date is skipped.
		for k := range maxScan / 28 {
			first := time.Date(s.y, s.mo+time.Month(k), 1, 0, 0, 0, 0, time.UTC)
			d := min(s.monthDay, daysIn(first.Year(), first.Month()))
			if time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC).Before(start) {
				continue
			}
			n++
			if s.count > 0 && n > s.count {
				return
			}
			if !emit(n, first.Year(), first.Month(), d) {
				return
			}
		}
	}
}

// Next returns the first occurrence at or after notBefore, or false when the
// series has none left. The zero time finds the first occurrence.
func (s *Schedule) Next(notBefore time.Time) (Occurrence, bool) {
	var found Occurrence
	var ok bool
	s.each(func(o Occurrence) bool {
		if o.At.Before(notBefore) {
			return true
		}
		found, ok = o, true
		return false
	})
	return found, ok
}

// RRule renders the recurrence as an RFC 5545 RRULE for a calendar entry, or ""
// for a one-shot. A monthly day above 28 differs from the scheduler's clamping:
// RFC 5545 skips a month that lacks the day, so the calendar shows fewer
// entries than the scheduler fires (DESIGN-032).
func (s *Schedule) RRule() string {
	if !s.hasRecurrent {
		return ""
	}
	parts := []string{"FREQ=" + strings.ToUpper(s.freq)}
	switch s.freq {
	case v1alpha1.ReminderWeekly:
		parts = append(parts, "BYDAY="+strings.Join(s.byDaySorted, ","))
	case v1alpha1.ReminderMonthly:
		parts = append(parts, fmt.Sprintf("BYMONTHDAY=%d", s.monthDay))
	}
	if s.until != nil {
		parts = append(parts, "UNTIL="+s.until.Format("20060102T150405Z"))
	}
	if s.count > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", s.count))
	}
	return strings.Join(parts, ";")
}

// PlanHash identifies the schedule fields: when it changes, NextDue is stale.
func PlanHash(spec v1alpha1.ReminderSpec) string {
	r := spec.Recurrence
	var parts []string
	parts = append(parts, spec.Due, spec.TimeZone)
	if r != nil {
		by := slices.Clone(r.ByDay)
		slices.Sort(by)
		until := ""
		if r.Until != nil {
			until = r.Until.UTC().Format(time.RFC3339)
		}
		parts = append(parts, r.Freq, strings.Join(by, ","), fmt.Sprint(r.ByMonthDay), until, fmt.Sprint(r.Count))
	}
	return hash(parts...)
}

// CalendarHash identifies the fields a calendar entry is written from.
func CalendarHash(spec v1alpha1.ReminderSpec) string {
	return hash(PlanHash(spec), spec.Title, spec.Body)
}

func hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:%s|", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// EventID is the calendar entry's id, derived from the reminder's identity so a
// replayed create finds the entry it already made. Hex digits are inside the
// base32hex alphabet Google Calendar allows for event ids.
func EventID(namespace, name string) string {
	return hash("reminder", namespace, name)
}
