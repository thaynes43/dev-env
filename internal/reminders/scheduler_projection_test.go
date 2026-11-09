package reminders

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// retainTo gives a projection its retention end (DESIGN-054 G1-R1).
func retainTo(s *v1alpha1.ReminderSpec, at string) {
	t := metav1.NewTime(utc(at))
	s.ProjectionRetainUntil = &t
}

func TestProjectionSyncEditCancelDelete(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO"}}
		s.Due = "2026-11-02T09:00"
		s.TimeZone = "America/New_York"
		s.Title = "Plan the week"
		s.Body = "work order"
		retainTo(s, "2026-12-01T00:00:00Z")
	})
	e.tick()
	e.tick()
	if e.cal.Count() != 1 || e.cal.Upserts != 1 {
		t.Fatalf("replays made %d entries in %d upserts", e.cal.Count(), e.cal.Upserts)
	}
	id := EventID(testNS, "rem-proj")
	ev := e.cal.Events["haynesnetwork/"+id]
	if ev.ID != id || ev.Title != "Plan the week" || ev.RRule != "FREQ=WEEKLY;BYDAY=MO" || ev.Start != "2026-11-02T09:00" {
		t.Fatalf("event %+v", ev)
	}
	r := e.wantState("rem-proj", v1alpha1.ReminderPending)
	if len(r.Status.Calendar) != 1 {
		t.Fatalf("calendar status %+v", r.Status.Calendar)
	}
	sync := r.Status.Calendar[0]
	if sync.EventID != id || sync.SpecHash != CalendarHash(r.Spec) || sync.State != v1alpha1.CalendarSynced {
		t.Fatalf("sync %+v", sync)
	}
	e.mutate("rem-proj", func(r *v1alpha1.Reminder) { r.Spec.Title = "Plan the week now" })
	e.tick()
	if e.cal.Upserts != 2 || e.cal.Count() != 1 || e.cal.Events["haynesnetwork/"+id].Title != "Plan the week now" {
		t.Fatalf("edit upserts %d entries %d event %+v", e.cal.Upserts, e.cal.Count(), e.cal.Events["haynesnetwork/"+id])
	}
	if got := e.get("rem-proj").Status.Calendar[0].SpecHash; got != CalendarHash(e.get("rem-proj").Spec) {
		t.Fatalf("spec hash %s", got)
	}
	e.mutate("rem-proj", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderCancelled)
	if e.cal.Count() != 0 || e.cal.Deletes != 1 {
		t.Fatalf("after cancel entries %d deletes %d", e.cal.Count(), e.cal.Deletes)
	}
	if st := e.get("rem-proj").Status.Calendar[0].State; st != v1alpha1.CalendarDeleted {
		t.Fatalf("delete record %s", st)
	}
}

func TestProjectionStaysPendingPastItsLastOccurrence(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 1}
		retainTo(s, "2026-11-30T00:00:00Z")
	})
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.tick()
	r := e.wantState("rem-proj", v1alpha1.ReminderPending)
	if r.Status.NextDue != nil || r.Status.Message != ProjectionPendingMessage {
		t.Fatalf("after the only occurrence: %+v", r.Status)
	}
	if e.cal.Count() != 1 {
		t.Fatal("the series event was dropped at the last occurrence")
	}
	e.advance(72 * time.Hour)
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	e.wantSends(0)
	if e.cal.Count() != 1 || e.cal.Upserts != 1 {
		t.Fatalf("entries %d upserts %d", e.cal.Count(), e.cal.Upserts)
	}
}

// G1-R2: the series event ends at the last occurrence (recurrence.until), but
// the reminder lives to its retention end, so an edit or a cancel made after
// the last occurrence still reaches the event.
func TestProjectionEditAfterTheLastOccurrenceStillReachesTheEvent(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		retainTo(s, "2026-11-30T00:00:00Z") // a one-shot: due 2026-10-28T13:00Z
	})
	e.tick()
	e.advance(time.Hour) // the only occurrence has passed
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	e.mutate("rem-proj", func(r *v1alpha1.Reminder) { r.Spec.Title = "Renamed after the run" })
	e.tick()
	if e.cal.Upserts != 2 || e.cal.Events["haynesnetwork/"+EventID(testNS, "rem-proj")].Title != "Renamed after the run" {
		t.Fatalf("an edit after the last occurrence: upserts %d events %+v", e.cal.Upserts, e.cal.Events)
	}
	e.wantState("rem-proj", v1alpha1.ReminderPending)
}

func TestProjectionCancelAfterTheLastOccurrenceDeletesTheEvent(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		retainTo(s, "2026-11-30T00:00:00Z")
	})
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.mutate("rem-proj", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderCancelled)
	if e.cal.Count() != 0 || e.cal.Deletes != 1 {
		t.Fatalf("after cancel: entries %d deletes %d", e.cal.Count(), e.cal.Deletes)
	}
}

// G1-R3: when the retention end is strictly in the past, the scheduler deletes
// the event itself and ends the reminder. A failed delete keeps it pending for
// the next pass.
func TestProjectionRetentionEndDeletesTheEventAndEnds(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}
		retainTo(s, "2026-10-30T12:00:00Z")
	})
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.advance(24 * time.Hour) // second occurrence
	e.tick()
	e.now = utc("2026-10-30T12:00:00Z") // exactly the retention end: not yet past
	e.tick()
	r := e.wantState("rem-proj", v1alpha1.ReminderPending)
	if r.Status.Message != ProjectionPendingMessage || e.cal.Count() != 1 || e.cal.Deletes != 0 {
		t.Fatalf("at the retention end (not yet past): %+v entries %d deletes %d", r.Status, e.cal.Count(), e.cal.Deletes)
	}
	e.advance(time.Minute)
	e.cal.FailDelete = true
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	if e.cal.Count() != 1 {
		t.Fatal("a failed delete removed the entry")
	}
	e.cal.FailDelete = false
	e.tick()
	r = e.wantState("rem-proj", v1alpha1.ReminderSkipped)
	if r.Status.Message != ProjectionUntilMessage || r.Status.NextDue != nil {
		t.Fatalf("after the retention end: %+v", r.Status)
	}
	if e.cal.Count() != 0 || e.cal.Deletes != 1 || r.Status.Calendar[0].State != v1alpha1.CalendarDeleted {
		t.Fatalf("entries %d deletes %d calendar %+v", e.cal.Count(), e.cal.Deletes, r.Status.Calendar)
	}
	e.tick()
	e.wantSends(0)
	if e.cal.Deletes != 1 {
		t.Fatal("a final reminder deleted again")
	}
}

// A one-shot projection leaves pending too: it has a retention end like every
// other (F1).
func TestOneShotProjectionEndsAtItsRetentionEnd(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		retainTo(s, "2026-11-04T00:00:00Z")
	})
	e.tick()
	e.advance(time.Hour) // the only occurrence has passed
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	e.advance(400 * 24 * time.Hour)
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderSkipped)
	if e.cal.Count() != 0 {
		t.Fatal("the event outlived the projection")
	}
}

// plan's projection branch: a projection whose whole series is already past
// stays pending with its own message, not failed as an ordinary reminder would.
func TestProjectionPlanKeepsAPastSeriesPending(t *testing.T) {
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		s.Due = "2026-10-20T09:00"
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}
		retainTo(s, "2026-11-30T00:00:00Z")
	})
	e.tick()
	r := e.wantState("rem-proj", v1alpha1.ReminderPending)
	if r.Status.Message != ProjectionPendingMessage || r.Status.NextDue != nil {
		t.Fatalf("%+v", r.Status)
	}
	// The plan hash is recorded, so the next pass does not plan again.
	e.tick()
	if again := e.get("rem-proj"); again.ResourceVersion != r.ResourceVersion {
		t.Fatalf("the next pass rewrote a settled projection: %s then %s", r.ResourceVersion, again.ResourceVersion)
	}
	// The same series on an ordinary reminder fails.
	e.create("rem-plain", func(s *v1alpha1.ReminderSpec) {
		s.Due = "2026-10-20T09:00"
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}
	})
	e.tick()
	e.wantState("rem-plain", v1alpha1.ReminderFailed)
}

// The projection gate: an ordinary reminder never ends on a retention end,
// even if one is stored on it (the CRD refuses it; the scheduler must not
// depend on that). It still sends and ends sent.
func TestOrdinaryReminderIgnoresARetentionEnd(t *testing.T) {
	e := newEnv(t)
	e.create("rem-plain", func(s *v1alpha1.ReminderSpec) {
		retainTo(s, "2026-10-28T00:00:00Z") // already in the past
	})
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.wantSends(1)
	e.wantState("rem-plain", v1alpha1.ReminderSent)
}

func TestProjectionDoesNotSendPushover(t *testing.T) {
	spec := v1alpha1.ReminderSpec{Projection: true, Targets: []string{"pushover:default", "calendar:haynesnetwork"}}
	retainTo(&spec, "2026-11-30T00:00:00Z")
	if err := ValidateProjection(spec); err == nil {
		t.Fatal("validation accepted Pushover")
	}
	e := newEnv(t)
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"pushover:default", "calendar:haynesnetwork"}
		retainTo(s, "2026-11-30T00:00:00Z")
	})
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.tick()
	e.wantSends(0)
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	if e.cal.Count() != 1 {
		t.Fatal("the calendar target was not synced")
	}
}

func TestProjectionCalendarFailureBacksOff(t *testing.T) {
	e := newEnv(t)
	e.cal.FailUpsert = true
	e.create("rem-proj", func(s *v1alpha1.ReminderSpec) {
		s.Projection = true
		s.Targets = []string{"calendar:haynesnetwork"}
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: v1alpha1.ReminderDaily, Count: 2}
		retainTo(s, "2026-11-30T00:00:00Z")
	})
	e.tick()
	r := e.get("rem-proj")
	if len(r.Status.Calendar) != 1 || r.Status.Calendar[0].State != v1alpha1.CalendarFailed || r.Status.Calendar[0].EventID != EventID(testNS, "rem-proj") {
		t.Fatalf("%+v", r.Status.Calendar)
	}
	e.cal.FailUpsert = false
	e.tick()
	if e.cal.Count() != 0 {
		t.Fatal("retried inside the backoff")
	}
	e.advance(6 * time.Minute)
	e.tick()
	if e.cal.Count() != 1 || e.get("rem-proj").Status.Calendar[0].State != v1alpha1.CalendarSynced {
		t.Fatal("did not retry after the backoff")
	}
	if e.get("rem-proj").Status.Calendar[0].SpecHash != CalendarHash(e.get("rem-proj").Spec) {
		t.Fatal("synced without the spec hash")
	}
	e.cal.FailDelete = true
	e.mutate("rem-proj", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderPending)
	if e.cal.Count() != 1 {
		t.Fatal("a failed delete removed the entry")
	}
	e.cal.FailDelete = false
	e.tick()
	e.wantState("rem-proj", v1alpha1.ReminderCancelled)
	if e.cal.Count() != 0 || e.get("rem-proj").Status.Calendar[0].State != v1alpha1.CalendarDeleted {
		t.Fatal("cancel did not delete the entry")
	}
}
