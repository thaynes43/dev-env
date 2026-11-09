package reminders

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestValidateProjectionRejectsPushover(t *testing.T) {
	ok := spec("2026-10-28T09:00", "UTC", nil)
	ok.Projection = true
	ok.Targets = []string{"calendar:haynesnetwork"}
	retain := metav1.NewTime(utc("2026-11-30T00:00:00Z"))
	ok.ProjectionRetainUntil = &retain
	if err := ValidateProjection(ok); err != nil {
		t.Fatalf("calendar only: %v", err)
	}
	none := ok
	none.ProjectionRetainUntil = nil
	if err := ValidateProjection(none); err == nil || !strings.Contains(err.Error(), "projectionRetainUntil") {
		t.Fatalf("no retention end: %v", err)
	}
	page := ok
	page.Targets = []string{"pushover:default"}
	if err := ValidateProjection(page); err == nil || !strings.Contains(err.Error(), "calendar targets only") {
		t.Fatalf("pushover: %v", err)
	}
	mixed := ok
	mixed.Targets = []string{"calendar:haynesnetwork", "pushover:default"}
	if err := ValidateProjection(mixed); err == nil || !strings.Contains(err.Error(), "pushover:default") {
		t.Fatalf("mixed: %v", err)
	}
	ordinary := spec("2026-10-28T09:00", "UTC", nil)
	ordinary.Targets = []string{"pushover:default"}
	if err := ValidateProjection(ordinary); err != nil {
		t.Fatalf("a normal reminder may page: %v", err)
	}
}

func TestProjectionRecurrenceConvertsCountAndCapsUntil(t *testing.T) {
	due := "2026-10-31T09:00"
	count := &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}
	got, err := ProjectionRecurrence(due, "UTC", count, metav1.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 || got.Until == nil || !got.Until.Time.Equal(utc("2026-11-01T09:00:00Z")) {
		t.Fatalf("count conversion: %+v", got)
	}
	if count.Count != 2 || count.Until != nil {
		t.Fatal("the caller's recurrence was modified")
	}
	sch, err := NewSchedule(due, "UTC", got)
	if err != nil {
		t.Fatal(err)
	}
	if rule := sch.RRule(); strings.Contains(rule, "COUNT=") || !strings.Contains(rule, "UNTIL=20261101T090000Z") {
		t.Fatalf("rrule %q", rule)
	}
	var n int
	sch.each(func(Occurrence) bool { n++; return true })
	if n != 2 {
		t.Fatalf("%d occurrences, want the original count", n)
	}

	notAfter := metav1.NewTime(utc("2026-10-31T09:00:00Z"))
	capped, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 10}, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if capped.Count != 0 || capped.Until == nil || !capped.Until.Time.Equal(notAfter.Time) {
		t.Fatalf("capped count: %+v", capped)
	}

	open, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderWeekly}, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if open.Count != 0 || open.Until == nil || !open.Until.Time.Equal(notAfter.Time) {
		t.Fatalf("open series: %+v", open)
	}

	later := metav1.NewTime(utc("2026-12-01T00:00:00Z"))
	early := metav1.NewTime(utc("2026-11-15T00:00:00Z"))
	kept, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Until: &early}, later)
	if err != nil {
		t.Fatal(err)
	}
	if !kept.Until.Time.Equal(early.Time) {
		t.Fatalf("an earlier until was moved: %s", kept.Until.Time)
	}
	pulled, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Until: &later}, notAfter)
	if err != nil {
		t.Fatal(err)
	}
	if !pulled.Until.Time.Equal(notAfter.Time) || pulled.Count != 0 {
		t.Fatalf("until was not capped: %+v", pulled)
	}

	if _, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2, Until: &early}, metav1.Time{}); err == nil || !strings.Contains(err.Error(), "exclusive") {
		t.Fatalf("count with until: %v", err)
	}
	one, err := ProjectionRecurrence(due, "UTC", nil, notAfter)
	if err != nil || one != nil {
		t.Fatalf("one-shot: %+v %v", one, err)
	}
	if _, err := ProjectionRecurrence("not-a-time", "UTC", nil, metav1.Time{}); err == nil {
		t.Fatal("a bad due was accepted")
	}
}

func TestProjectionRecurrenceUsesTheResolvedLastInstant(t *testing.T) {
	// 2026-11-01 01:30 America/New_York is the fold. Count 1 keeps that instant.
	due := "2026-11-01T01:30"
	got, err := ProjectionRecurrence(due, "America/New_York", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 1}, metav1.Time{})
	if err != nil {
		t.Fatal(err)
	}
	sch, err := NewSchedule(due, "America/New_York", nil)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := sch.Next(time.Time{})
	if !ok || got.Until == nil || !got.Until.Time.Equal(first.At.UTC()) || got.Count != 0 {
		t.Fatalf("until %v first %v ok %v", got.Until, first.At, ok)
	}
}

// F5: a notAfter before the first occurrence is refused, for a series and for a
// one-shot, rather than returning a series with no occurrence.
func TestProjectionRecurrenceRefusesANotAfterBeforeTheFirstOccurrence(t *testing.T) {
	notAfter := metav1.NewTime(utc("2026-10-30T00:00:00Z"))
	due := "2026-11-02T09:00"
	for name, rec := range map[string]*v1alpha1.ReminderRecurrence{
		"a count":  {Freq: v1alpha1.ReminderDaily, Count: 3},
		"open":     {Freq: v1alpha1.ReminderDaily},
		"one-shot": nil,
	} {
		if _, err := ProjectionRecurrence(due, "UTC", rec, notAfter); err == nil || !strings.Contains(err.Error(), "before the first occurrence") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// notAfter equal to the first occurrence is allowed: the first one runs.
	atFirst := metav1.NewTime(utc("2026-11-02T09:00:00Z"))
	got, err := ProjectionRecurrence(due, "UTC", &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 3}, atFirst)
	if err != nil || got.Until == nil || !got.Until.Time.Equal(atFirst.Time) {
		t.Fatalf("notAfter at the first occurrence: %+v %v", got, err)
	}
}

// G1-R1: every projection has a retention end, one-shots included.
func TestProjectionRetainUntil(t *testing.T) {
	due := "2026-11-02T09:00"
	notAfter := metav1.NewTime(utc("2026-12-01T00:00:00Z").In(time.FixedZone("x", 3600)))
	got, err := ProjectionRetainUntil(due, "UTC", notAfter)
	if err != nil || got == nil || !got.Time.Equal(notAfter.Time) || got.Location() != time.UTC {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ProjectionRetainUntil(due, "UTC", metav1.Time{}); err == nil {
		t.Error("a zero notAfter was accepted")
	}
	if _, err := ProjectionRetainUntil(due, "UTC", metav1.NewTime(utc("2026-11-01T00:00:00Z"))); err == nil || !strings.Contains(err.Error(), "before the first occurrence") {
		t.Errorf("notAfter before the first occurrence: %v", err)
	}
	if _, err := ProjectionRetainUntil("not-a-time", "UTC", notAfter); err == nil {
		t.Error("a bad due was accepted")
	}
}
