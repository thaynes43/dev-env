package reminders

import (
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// ProjectionPendingMessage is the status message while a projection reminder
// has no further occurrence and is still waiting for cancel or for until.
const ProjectionPendingMessage = "projection pending until cancelled or until passes"

// ProjectionUntilMessage is the status message once a projection's until has
// passed. The calendar event is left in place.
const ProjectionUntilMessage = "projection until passed"

// ValidateProjection refuses a projection reminder that is not calendar-only.
// A reminder that is not a projection is always accepted here; the ordinary
// target allowlist is CheckTargets.
func ValidateProjection(spec v1alpha1.ReminderSpec) error {
	if !spec.Projection {
		return nil
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

// ProjectionRecurrence returns the recurrence stored on a projection reminder.
// A count becomes the until of its last occurrence, because DESIGN-032 refuses
// count and until together. That until is then capped at notAfter when notAfter
// is non-zero: an open-ended series takes notAfter, and a later until is pulled
// back to it. A zero notAfter does not cap. A one-shot stays a one-shot.
// The scheduler does not call this; the caller stores the result.
func ProjectionRecurrence(due, timeZone string, rec *v1alpha1.ReminderRecurrence, notAfter metav1.Time) (*v1alpha1.ReminderRecurrence, error) {
	if rec == nil {
		if _, err := NewSchedule(due, timeZone, nil); err != nil {
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
	if _, err := NewSchedule(due, timeZone, out); err != nil {
		return nil, err
	}
	return out, nil
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
