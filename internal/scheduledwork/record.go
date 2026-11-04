package scheduledwork

import (
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// RecordResult is what Record did with an occurrence.
type RecordResult string

const (
	// Recorded: the terminal record is now in the item's status, and its
	// count was added. The caller writes the status with a compare-and-set.
	Recorded RecordResult = "recorded"
	// AlreadyTerminal: the occurrence already has a terminal state. Nothing
	// changed; a terminal state is final.
	AlreadyTerminal RecordResult = "already terminal"
	// NoOccurrence: the item records no occurrence at that instant. Nothing
	// changed. The executor claims an occurrence before it creates the
	// session, and prunes only terminal ones, so the session is not one the
	// item tracks (or its record was pruned long after it ended).
	NoOccurrence RecordResult = "no occurrence"
	// OtherSession: the occurrence names another session. Nothing changed.
	OtherSession RecordResult = "other session"
)

// sessionNamePattern is the schema's pattern for an occurrence's session.
var sessionNamePattern = regexp.MustCompile(`^sw-[0-9a-f]{12}-[0-9]{12}$`)

// Record writes terminal record t into w's occurrence at instant at, for the
// session named session, and reports what it did. It changes w only when it
// returns Recorded. It sets the occurrence's state and reason, its session
// when unset and the name fits the schema, its execution when t has one, and
// its reported outcome when unset (the schema sets it once). It adds one to the
// lifetime count of t's state: the writer of a terminal transition counts it,
// and the compare-and-set write lets only one writer make the transition.
func Record(w *v1alpha1.ScheduledWork, at time.Time, session string, t Terminal) RecordResult {
	for i := range w.Status.Occurrences {
		o := &w.Status.Occurrences[i]
		if !o.At.Time.Equal(at) {
			continue
		}
		if v1alpha1.OccurrenceTerminal(o.State) {
			return AlreadyTerminal
		}
		if o.Session != "" && o.Session != session {
			return OtherSession
		}
		o.State, o.Reason = t.State, clip(t.Reason, maxReason)
		if o.Session == "" && sessionNamePattern.MatchString(session) {
			o.Session = session
		}
		if t.Execution != nil {
			o.Execution = t.Execution.DeepCopy()
		}
		if o.Reported == nil && t.Reported != nil {
			o.Reported = t.Reported.DeepCopy()
		}
		count(&w.Status.Counts, t.State)
		return Recorded
	}
	return NoOccurrence
}

// Find returns w's occurrence at instant at, or nil.
func Find(w *v1alpha1.ScheduledWork, at time.Time) *v1alpha1.ScheduledWorkOccurrence {
	for i := range w.Status.Occurrences {
		if w.Status.Occurrences[i].At.Time.Equal(at) {
			return &w.Status.Occurrences[i]
		}
	}
	return nil
}

func count(c *v1alpha1.ScheduledWorkCounts, state string) {
	switch state {
	case v1alpha1.OccurrenceSucceeded:
		c.Succeeded++
	case v1alpha1.OccurrenceFailed:
		c.Failed++
	case v1alpha1.OccurrenceEnded:
		c.Ended++
	}
}
