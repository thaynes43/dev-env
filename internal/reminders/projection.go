package reminders

import (
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ProjectionPendingMessage is the status message while a projection reminder
// has no further occurrence and is still waiting for cancel or for its
// retention end (spec.projectionRetainUntil).
const ProjectionPendingMessage = "projection pending until cancelled or its retention end passes"

// ProjectionUntilMessage is the status message once a projection's
// projectionRetainUntil has passed and its calendar event has been deleted.
const ProjectionUntilMessage = "projection retention ended"

// ValidateProjection refuses a projection reminder that is not calendar-only
// or has no retention end. A reminder that is not a projection is always
// accepted here; the ordinary target allowlist is CheckTargets. It has no
// production caller yet: the CRD rules enforce the same refusals, and the
// ScheduledWork executor (DESIGN-054 S4/S5 wiring) is its intended caller,
// before it writes the companion Reminder.
func ValidateProjection(spec v1alpha1.ReminderSpec) error {
	if !spec.Projection {
		return nil
	}
	if spec.ProjectionRetainUntil == nil {
		return errors.New("a projection reminder needs projectionRetainUntil")
	}
	if len(spec.Targets) == 0 {
		return errors.New("a projection reminder has calendar targets only")
	}
	for _, raw := range spec.Targets {
		t, err := ParseTarget(raw)
		if err != nil {
			return fmt.Errorf("a projection reminder has calendar targets only: %w", err)
		}
		if t.Kind != v1alpha1.ReminderTargetCalendar {
			return fmt.Errorf("a projection reminder has calendar targets only: %q", raw)
		}
	}
	return nil
}

// ProjectionRecurrence returns the recurrence stored on a projection reminder:
// the calendar event's own series, which ends at its last occurrence. A count
// becomes the until of its last occurrence, because DESIGN-032 refuses count
// and until together. That until is then capped at notAfter when notAfter is
// non-zero: an open-ended series takes notAfter, and a later until is pulled
// back to it. A zero notAfter does not cap. A one-shot stays a one-shot. A
// notAfter before the first occurrence is refused, so the event is never an
// empty series. The reminder's own end is separate: see ProjectionRetainUntil.
// The scheduler does not call this; the caller (the ScheduledWork executor,
// DESIGN-054 S4/S5) stores the result.
func ProjectionRecurrence(due, timeZone string, rec *v1alpha1.ReminderRecurrence, notAfter metav1.Time) (*v1alpha1.ReminderRecurrence, error) {
	if rec == nil {
		sch, err := NewSchedule(due, timeZone, nil)
		if err != nil {
			return nil, err
		}
		if err := checkNotAfterReachesFirst(sch, notAfter); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if rec.Until != nil && rec.Count != 0 {
		return nil, errors.New("until and count are exclusive")
	}
	out := rec.DeepCopy()
	if out.Count > 0 {
		sch, err := NewSchedule(due, timeZone, out)
		if err != nil {
			return nil, err
		}
		last, ok := sch.lastOccurrence()
		if !ok {
			return nil, errors.New("count has no occurrence")
		}
		until := metav1.NewTime(last.At.UTC())
		out.Until = &until
		out.Count = 0
	}
	if !notAfter.IsZero() {
		capAt := metav1.NewTime(notAfter.UTC())
		if out.Until == nil || out.Until.After(capAt.Time) {
			out.Until = &capAt
		}
	}
	sch, err := NewSchedule(due, timeZone, out)
	if err != nil {
		return nil, err
	}
	if err := checkNotAfterReachesFirst(sch, notAfter); err != nil {
		return nil, err
	}
	return out, nil
}

// ProjectionRetainUntil returns the value of spec.projectionRetainUntil for an
// item's notAfter (DESIGN-054 G1-R1): every projection has one, one-shots
// included, so no projection stays pending forever or counts against its
// owner's live reminders for good. It is refused when notAfter is zero or falls
// before the first occurrence. The scheduler keeps the reminder pending until
// it is strictly in the past, then deletes the event and ends the reminder.
func ProjectionRetainUntil(due, timeZone string, notAfter metav1.Time) (*metav1.Time, error) {
	if notAfter.IsZero() {
		return nil, errors.New("a projection needs a notAfter")
	}
	sch, err := NewSchedule(due, timeZone, nil)
	if err != nil {
		return nil, err
	}
	if err := checkNotAfterReachesFirst(sch, notAfter); err != nil {
		return nil, err
	}
	t := metav1.NewTime(notAfter.UTC())
	return &t, nil
}

// checkNotAfterReachesFirst refuses a non-zero notAfter before the schedule's
// first occurrence. RFC 5545 counts DTSTART as the first instance, so such a
// series would show an entry the executor never runs.
func checkNotAfterReachesFirst(sch *Schedule, notAfter metav1.Time) error {
	if notAfter.IsZero() {
		return nil
	}
	first, ok := sch.Next(time.Time{})
	if !ok {
		return errors.New("notAfter is before the first occurrence")
	}
	if notAfter.Time.Before(first.At) {
		return errors.New("notAfter is before the first occurrence")
	}
	return nil
}

// lastOccurrence is the last occurrence a count or an until allows. An
// open-ended schedule walks maxScan, so callers use it only for a count.
func (s *Schedule) lastOccurrence() (Occurrence, bool) {
	var last Occurrence
	ok := false
	s.each(func(o Occurrence) bool {
		last = o
		ok = true
		return true
	})
	return last, ok
}
