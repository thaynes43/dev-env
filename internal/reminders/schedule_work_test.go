package reminders

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// workSpec is a ScheduledWork spec with the schedule fields of a reminder spec.
func workSpec(r v1alpha1.ReminderSpec) v1alpha1.ScheduledWorkSpec {
	return v1alpha1.ScheduledWorkSpec{
		Title: r.Title, Owner: r.Owner, Editor: r.Owner, Due: r.Due, TimeZone: r.TimeZone, Recurrence: r.Recurrence,
		NotAfter: metav1.NewTime(utc("2027-01-01T00:00:00Z")), Mode: v1alpha1.ScheduledWorkHeadless,
		Work: v1alpha1.ScheduledWorkOrder{Template: "ops-audit", Repo: "haynes-ops", Prompt: "p"},
	}
}

func seriesOf(t *testing.T, sch *Schedule, n int) []string {
	t.Helper()
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

// A ScheduledWork and a Reminder with the same schedule fields have the same
// series, RRULE and plan hash: the generalisation changes no reminder.
func TestScheduleOfWorkMatchesScheduleOf(t *testing.T) {
	ny(t)
	until := metav1.NewTime(utc("2026-12-31T23:59:59Z"))
	for name, sp := range map[string]v1alpha1.ReminderSpec{
		"a one-shot":                 spec("2026-10-31T09:00", "UTC", nil),
		"daily across the gap":       spec("2026-03-07T02:30", "America/New_York", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}),
		"daily across the fold":      spec("2026-10-31T01:30", "America/New_York", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 5}),
		"weekly by day with until":   spec("2026-10-28T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly, ByDay: []string{"TH", "MO"}, Until: &until}),
		"monthly clamped to the end": spec("2026-01-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderMonthly, ByMonthDay: 31}),
	} {
		t.Run(name, func(t *testing.T) {
			rem, err := ScheduleOf(sp)
			if err != nil {
				t.Fatal(err)
			}
			work, err := ScheduleOfWork(workSpec(sp))
			if err != nil {
				t.Fatal(err)
			}
			shared, err := NewSchedule(sp.Due, sp.TimeZone, sp.Recurrence)
			if err != nil {
				t.Fatal(err)
			}
			want := seriesOf(t, rem, 6)
			eq(t, seriesOf(t, work, 6), want)
			eq(t, seriesOf(t, shared, 6), want)
			if work.RRule() != rem.RRule() || work.Recurring() != rem.Recurring() {
				t.Errorf("rrule %q recurring %v, want %q %v", work.RRule(), work.Recurring(), rem.RRule(), rem.Recurring())
			}
			if PlanHashOf(sp.Due, sp.TimeZone, sp.Recurrence) != PlanHash(sp) {
				t.Error("PlanHashOf differs from a reminder's PlanHash")
			}
		})
	}
}

// DESIGN-032's DST rules hold for scheduled work: the reminder case of a daily
// 02:30 across the spring-forward gap, and 01:30 across the fold.
func TestScheduleOfWorkKeepsTheDSTRules(t *testing.T) {
	ny(t)
	rec := &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily}
	gap, err := ScheduleOfWork(workSpec(spec("2026-03-07T02:30", "America/New_York", rec)))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, seriesOf(t, gap, 3), []string{"2026-03-07T07:30Z", "2026-03-08T07:30Z", "2026-03-09T06:30Z"})
	fold, err := ScheduleOfWork(workSpec(spec("2026-10-31T01:30", "America/New_York", rec)))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, seriesOf(t, fold, 3), []string{"2026-10-31T05:30Z", "2026-11-01T05:30Z", "2026-11-02T06:30Z"})
}

// NotAfter bounds dispatch in the executor, not the schedule: the series and
// its RRULE stay what the owner wrote, so a count stays a count.
func TestScheduleOfWorkLeavesNotAfterToTheExecutor(t *testing.T) {
	ws := workSpec(spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 3}))
	ws.NotAfter = metav1.NewTime(utc("2026-11-01T00:00:00Z"))
	sch, err := ScheduleOfWork(ws)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, seriesOf(t, sch, 9), []string{"2026-10-31T09:00Z", "2026-11-01T09:00Z", "2026-11-02T09:00Z"})
	if got := sch.RRule(); got != "FREQ=DAILY;COUNT=3" {
		t.Errorf("rrule %q", got)
	}
}

func TestScheduleOfWorkRefuses(t *testing.T) {
	for name, sp := range map[string]v1alpha1.ReminderSpec{
		"a bad zone":      spec("2026-10-31T09:00", "Mars/Olympus", nil),
		"Local":           spec("2026-10-31T09:00", "Local", nil),
		"no zone":         spec("2026-10-31T09:00", "", nil),
		"a bad date":      spec("2026-13-31T09:00", "UTC", nil),
		"a bad freq":      spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "yearly"}),
		"a monthly byDay": spec("2026-10-31T09:00", "UTC", &v1alpha1.ReminderRecurrence{Freq: "monthly", ByDay: []string{"MO"}}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ScheduleOfWork(workSpec(sp)); err == nil {
				t.Error("accepted")
			}
		})
	}
}
