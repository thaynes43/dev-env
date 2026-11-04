package scheduledwork

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func TestRecord(t *testing.T) {
	at := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	succeeded := Terminal{State: v1alpha1.OccurrenceSucceeded, Reason: "the task exited 0 (success)",
		Execution: &v1alpha1.ScheduledWorkExecution{State: v1alpha1.TaskResultSucceeded, Reason: "TaskSucceeded"},
		Reported:  &v1alpha1.TaskOutcomeRecordStatus{Summary: "opened the PR"}}
	item := func(occ ...v1alpha1.ScheduledWorkOccurrence) *v1alpha1.ScheduledWork {
		w := &v1alpha1.ScheduledWork{}
		w.Status.Counts = v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 1}
		w.Status.Occurrences = occ
		return w
	}
	occ := func(when time.Time, state, session string) v1alpha1.ScheduledWorkOccurrence {
		return v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(when), State: state, Session: session}
	}
	earlier := occ(at.Add(-24*time.Hour), v1alpha1.OccurrenceFailed, "sw-0123456789ab-202612021400")
	for _, c := range []struct {
		name     string
		w        *v1alpha1.ScheduledWork
		session  string
		term     Terminal
		want     RecordResult
		state    string
		recorded string // the occurrence's session afterwards
		counts   v1alpha1.ScheduledWorkCounts
	}{
		{"dispatched becomes succeeded", item(earlier, occ(at, v1alpha1.OccurrenceDispatched, name)), name, succeeded,
			Recorded, v1alpha1.OccurrenceSucceeded, name, v1alpha1.ScheduledWorkCounts{Succeeded: 5, Failed: 2, Ended: 1}},
		{"running becomes failed", item(occ(at, v1alpha1.OccurrenceRunning, name)), name, Terminal{State: v1alpha1.OccurrenceFailed, Reason: ReasonReaped},
			Recorded, v1alpha1.OccurrenceFailed, name, v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 3, Ended: 1}},
		{"delivered becomes ended", item(occ(at, v1alpha1.OccurrenceDelivered, name)), name, Terminal{State: v1alpha1.OccurrenceEnded, Reason: ReasonParticipationNotObserved},
			Recorded, v1alpha1.OccurrenceEnded, name, v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 2}},
		{"claimed without a session gets the session", item(occ(at, v1alpha1.OccurrenceClaimed, "")), name, succeeded,
			Recorded, v1alpha1.OccurrenceSucceeded, name, v1alpha1.ScheduledWorkCounts{Succeeded: 5, Failed: 2, Ended: 1}},
		{"a name off the schema's pattern is not written", item(occ(at, v1alpha1.OccurrenceClaimed, "")), "s-hand-made", succeeded,
			Recorded, v1alpha1.OccurrenceSucceeded, "", v1alpha1.ScheduledWorkCounts{Succeeded: 5, Failed: 2, Ended: 1}},
		{"parked with a session", item(occ(at, v1alpha1.OccurrenceParked, name)), name, succeeded,
			Recorded, v1alpha1.OccurrenceSucceeded, name, v1alpha1.ScheduledWorkCounts{Succeeded: 5, Failed: 2, Ended: 1}},
		{"already terminal is not overwritten", item(occ(at, v1alpha1.OccurrenceFailed, name)), name, succeeded,
			AlreadyTerminal, v1alpha1.OccurrenceFailed, name, v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 1}},
		{"already skipped", item(occ(at, v1alpha1.OccurrenceSkipped, "")), name, succeeded,
			AlreadyTerminal, v1alpha1.OccurrenceSkipped, "", v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 1}},
		{"another session's occurrence", item(occ(at, v1alpha1.OccurrenceRunning, "sw-ffffffffffff-202612031400")), name, succeeded,
			OtherSession, v1alpha1.OccurrenceRunning, "sw-ffffffffffff-202612031400", v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 1}},
		{"no occurrence at that instant", item(earlier), name, succeeded,
			NoOccurrence, "", "", v1alpha1.ScheduledWorkCounts{Succeeded: 4, Failed: 2, Ended: 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			before := c.w.DeepCopy()
			got := Record(c.w, at, c.session, c.term)
			if got != c.want {
				t.Fatalf("Record = %q, want %q", got, c.want)
			}
			if got != Recorded && !equalWork(before, c.w) {
				t.Fatalf("Record %q changed the item", got)
			}
			if c.w.Status.Counts != c.counts {
				t.Errorf("counts %+v, want %+v", c.w.Status.Counts, c.counts)
			}
			o := Find(c.w, at)
			if c.state == "" {
				if o != nil {
					t.Errorf("an occurrence appeared: %+v", o)
				}
				return
			}
			if o == nil || o.State != c.state || o.Session != c.recorded {
				t.Fatalf("occurrence %+v, want %s on %q", o, c.state, c.recorded)
			}
			if got == Recorded {
				if o.Reason != c.term.Reason {
					t.Errorf("reason %q", o.Reason)
				}
				if (c.term.Execution == nil) != (o.Execution == nil) || (o.Execution != nil && (*o.Execution != *c.term.Execution || o.Execution == c.term.Execution)) {
					t.Errorf("execution %+v, want a copy of %+v", o.Execution, c.term.Execution)
				}
			}
			if e := Find(c.w, earlier.At.Time); e != nil && e.State != v1alpha1.OccurrenceFailed {
				t.Errorf("another occurrence changed: %+v", e)
			}
		})
	}
}

// The schema sets reported once: a reported outcome already on the
// occurrence stays.
func TestRecordKeepsAReportedOutcome(t *testing.T) {
	at := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	w := &v1alpha1.ScheduledWork{}
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceRunning,
		Reported: &v1alpha1.TaskOutcomeRecordStatus{Summary: "first"}}}
	term := Terminal{State: v1alpha1.OccurrenceSucceeded, Reason: "ok", Reported: &v1alpha1.TaskOutcomeRecordStatus{Summary: "second"}}
	if got := Record(w, at, "sw-0123456789ab-202612031400", term); got != Recorded {
		t.Fatal(got)
	}
	if r := w.Status.Occurrences[0].Reported; r == nil || r.Summary != "first" {
		t.Errorf("reported %+v", r)
	}
	w.Status.Occurrences[0] = v1alpha1.ScheduledWorkOccurrence{At: metav1.NewTime(at), State: v1alpha1.OccurrenceRunning}
	if got := Record(w, at, "sw-0123456789ab-202612031400", term); got != Recorded {
		t.Fatal(got)
	}
	if r := w.Status.Occurrences[0].Reported; r == nil || r.Summary != "second" || r == term.Reported {
		t.Errorf("reported %+v, want a copy of the outcome", r)
	}
}

func equalWork(a, b *v1alpha1.ScheduledWork) bool {
	if a.Status.Counts != b.Status.Counts || len(a.Status.Occurrences) != len(b.Status.Occurrences) {
		return false
	}
	for i := range a.Status.Occurrences {
		x, y := a.Status.Occurrences[i], b.Status.Occurrences[i]
		if x.State != y.State || x.Session != y.Session || x.Reason != y.Reason || !x.At.Equal(&y.At) {
			return false
		}
	}
	return true
}
