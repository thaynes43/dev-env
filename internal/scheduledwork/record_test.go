package scheduledwork

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
		{"already terminal is not overwritten", item(occ(at, v1alpha1.OccurrenceFailed, name)), name, Terminal{State: succeeded.State, Reason: succeeded.Reason, Execution: succeeded.Execution},
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

// A terminal occurrence of this session with no reported outcome takes one
// that lands after the record; nothing else changes, and no count is added.
func TestRecordAddsALateReportedOutcome(t *testing.T) {
	at := time.Date(2026, 12, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	late := Terminal{State: v1alpha1.OccurrenceSucceeded, Reason: "the task exited 0 (success)", Reported: &v1alpha1.TaskOutcomeRecordStatus{Summary: "late"}}
	for _, c := range []struct {
		name    string
		occ     v1alpha1.ScheduledWorkOccurrence
		term    Terminal
		want    RecordResult
		summary string
	}{
		{"late outcome", v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name, Reason: "ok"}, late, ReportedAdded, "late"},
		{"already reported", v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name, Reported: &v1alpha1.TaskOutcomeRecordStatus{Summary: "first"}}, late, AlreadyTerminal, "first"},
		{"no outcome yet", v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name}, Terminal{State: v1alpha1.OccurrenceSucceeded}, AlreadyTerminal, ""},
		{"another session's record", v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceFailed, Session: "sw-ffffffffffff-202612031400"}, late, AlreadyTerminal, ""},
		{"a record with no session", v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSkipped}, late, AlreadyTerminal, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.occ.At = metav1.NewTime(at)
			w := &v1alpha1.ScheduledWork{}
			w.Status.Counts.Succeeded = 1
			w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{c.occ}
			state, reason := c.occ.State, c.occ.Reason
			if got := Record(w, at, name, c.term); got != c.want {
				t.Fatalf("Record = %q, want %q", got, c.want)
			}
			o := w.Status.Occurrences[0]
			if o.State != state || o.Reason != reason || w.Status.Counts.Succeeded != 1 {
				t.Errorf("occurrence %+v, counts %+v", o, w.Status.Counts)
			}
			if got := ""; o.Reported != nil {
				got = o.Reported.Summary
				if got != c.summary {
					t.Errorf("reported %q, want %q", got, c.summary)
				}
			} else if c.summary != "" {
				t.Errorf("reported none, want %q", c.summary)
			}
		})
	}
}

func TestAwaitsReport(t *testing.T) {
	now := time.Date(2026, 12, 3, 14, 10, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	finished := func(ago time.Duration) func(*v1alpha1.AgentSession) {
		return func(s *v1alpha1.AgentSession) {
			s.Status.Agent = &v1alpha1.AgentStatus{Task: &v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(now.Add(-ago))}}
		}
	}
	for _, c := range []struct {
		name  string
		edit  func(*v1alpha1.AgentSession, *v1alpha1.ScheduledWorkOccurrence)
		wait  bool
		until time.Time
	}{
		{"just finished", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) { finished(10 * time.Second)(s) }, true, now.Add(-10*time.Second + ReportWait)},
		{"finished long ago", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) { finished(ReportWait)(s) }, false, time.Time{}},
		{"a finish far in the future is not trusted", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) { finished(-time.Hour)(s) }, false, time.Time{}},
		{"a finish just ahead, within the clock skew", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) { finished(-ReportClockSkew)(s) }, true, now.Add(ReportClockSkew + ReportWait)},
		{"a finish beyond the clock skew", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) {
			finished(-ReportClockSkew - time.Second)(s)
		}, false, time.Time{}},
		{"a private session's retained finish", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) {
			s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{TaskTerminal: &v1alpha1.PrivateTaskTerminalStatus{Task: v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(now.Add(-time.Minute))}}}
		}, true, now.Add(-time.Minute + ReportWait)},
		{"no recorded finish", func(*v1alpha1.AgentSession, *v1alpha1.ScheduledWorkOccurrence) {}, false, time.Time{}},
		{"outcome retained", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) {
			finished(time.Second)(s)
			s.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{}
		}, false, time.Time{}},
		{"already reported", func(s *v1alpha1.AgentSession, o *v1alpha1.ScheduledWorkOccurrence) {
			finished(time.Second)(s)
			o.Reported = &v1alpha1.TaskOutcomeRecordStatus{Summary: "x"}
		}, false, time.Time{}},
		{"being deleted", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) {
			finished(time.Second)(s)
			d := metav1.NewTime(now)
			s.DeletionTimestamp = &d
		}, false, time.Time{}},
		{"interactive", func(s *v1alpha1.AgentSession, _ *v1alpha1.ScheduledWorkOccurrence) {
			finished(time.Second)(s)
			s.Spec.Mode = v1alpha1.ModeRemote
		}, false, time.Time{}},
		{"another session's record", func(s *v1alpha1.AgentSession, o *v1alpha1.ScheduledWorkOccurrence) {
			finished(time.Second)(s)
			o.Session = "sw-ffffffffffff-202612031400"
		}, false, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask}}
			o := &v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name}
			c.edit(s, o)
			wait, until := AwaitsReport(s, o, now)
			if wait != c.wait || (wait && !until.Equal(c.until)) {
				t.Errorf("wait %v until %v, want %v until %v", wait, until, c.wait, c.until)
			}
		})
	}
	if wait, _ := AwaitsReport(&v1alpha1.AgentSession{Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask}}, nil, now); wait {
		t.Error("waits with no occurrence")
	}
}

// The wait for a reported outcome is one fixed window of the operator's clock,
// [finishedAt - ReportClockSkew, finishedAt + ReportWait), read from the
// session alone. However often the releaser judges the same session, and
// whatever finish its pod's clock reports, the wait's end never moves and the
// whole wait is at most ReportWait + ReportClockSkew. A fresh operator reads
// the same window, so a restart neither extends nor cuts it.
func TestAwaitsReportWindowNeverMoves(t *testing.T) {
	t0 := time.Date(2026, 12, 3, 14, 10, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	for _, ahead := range []time.Duration{-time.Minute, 0, time.Second, ReportClockSkew, ReportClockSkew + time.Second, time.Hour, 24 * time.Hour} {
		t.Run("finish "+ahead.String()+" after the first judgement", func(t *testing.T) {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask},
				Status: v1alpha1.AgentSessionStatus{Agent: &v1alpha1.AgentStatus{Task: &v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(t0.Add(ahead))}}}}
			o := &v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name}
			var first, last, end time.Time
			for step := time.Duration(0); step <= 48*time.Hour; step += 7 * time.Second {
				now := t0.Add(step)
				wait, until := AwaitsReport(s, o, now)
				if !wait {
					continue
				}
				if end.IsZero() {
					end, first = until, now
				}
				if !until.Equal(end) {
					t.Fatalf("at t0+%v the wait ends %v; it ended %v before", step, until, end)
				}
				last = now
			}
			if !end.IsZero() && last.Sub(first) > ReportWait+ReportClockSkew {
				t.Errorf("waited from %v to %v, longer than %v", first, last, ReportWait+ReportClockSkew)
			}
		})
	}
}

// A finish an hour after the operator's clock (pod clock skew, a bad
// heartbeat) holds nothing for long: the same fixture judged at t0 and again
// at t0 + ReportWait + 1s is not waited for at the second judgement.
func TestAwaitsReportStopsForAFarFutureFinish(t *testing.T) {
	t0 := time.Date(2026, 12, 3, 14, 10, 0, 0, time.UTC)
	const name = "sw-0123456789ab-202612031400"
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask},
		Status: v1alpha1.AgentSessionStatus{Agent: &v1alpha1.AgentStatus{Task: &v1alpha1.TaskStatus{FinishedAt: metav1.NewTime(t0.Add(time.Hour))}}}}
	o := &v1alpha1.ScheduledWorkOccurrence{State: v1alpha1.OccurrenceSucceeded, Session: name}
	AwaitsReport(s, o, t0)
	if wait, until := AwaitsReport(s, o, t0.Add(ReportWait+time.Second)); wait {
		t.Fatalf("still waits at t0+%v, until %v, for a finish an hour ahead", ReportWait+time.Second, until)
	}
}

// The record names the session object by its UID: it is set with the record,
// and a session object of the same name with another UID (a second create
// after the first was deleted) records nothing and copies no outcome.
func TestRecordKeepsTheSessionObject(t *testing.T) {
	at := time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-203006031400"
	first := Terminal{State: v1alpha1.OccurrenceSucceeded, Reason: "done", SessionUID: "uid-first"}
	w := &v1alpha1.ScheduledWork{}
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceClaimed}}
	if r := Record(w, at, name, first); r != Recorded || w.Status.Occurrences[0].SessionUID != "uid-first" || w.Status.Occurrences[0].Session != name {
		t.Fatalf("%s: %+v", r, w.Status.Occurrences[0])
	}
	second := Terminal{State: v1alpha1.OccurrenceFailed, Reason: "refused", SessionUID: "uid-second", Reported: &v1alpha1.TaskOutcomeRecordStatus{Summary: "not mine"}}
	if r := Record(w, at, name, second); r != AlreadyTerminal || w.Status.Occurrences[0].Reported != nil {
		t.Errorf("a second session object: %s, %+v", r, w.Status.Occurrences[0])
	}
	open := &v1alpha1.ScheduledWork{}
	open.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceDispatched, Session: name, SessionUID: "uid-first"}}
	if r := Record(open, at, name, second); r != OtherSession || open.Status.Occurrences[0].State != v1alpha1.OccurrenceDispatched {
		t.Errorf("a second session object on an open occurrence: %s, %+v", r, open.Status.Occurrences[0])
	}
	if o := Outcome(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{UID: "uid-x"}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeRemote}}, at); o.SessionUID != "uid-x" {
		t.Errorf("Outcome's session UID %q", o.SessionUID)
	}
}

// A session that never launched, recorded into an occurrence the executor
// was ending, gets the mark's record: its reason, and Skipped or Cancelled.
func TestRecordIntoAnEndingOccurrence(t *testing.T) {
	at := time.Date(2030, 6, 3, 14, 0, 0, 0, time.UTC)
	const name = "sw-0123456789ab-203006031400"
	for reason, want := range map[string]string{ReasonCancelledAtRevalidation: v1alpha1.OccurrenceCancelled, "notAfter passed": v1alpha1.OccurrenceSkipped} {
		w := &v1alpha1.ScheduledWork{}
		w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: v1alpha1.OccurrenceClaimed, Ending: true, Reason: reason}}
		refused := refusedSession(name, "uid-late")
		if r := Record(w, at, name, Outcome(refused, at)); r != Recorded {
			t.Fatalf("%s", r)
		}
		o := w.Status.Occurrences[0]
		if o.State != want || o.Reason != reason || o.Session != name || o.SessionUID != "uid-late" {
			t.Errorf("%q: %+v", reason, o)
		}
		if c := w.Status.Counts; c.Failed != 0 || c.Cancelled+c.Skipped != 1 {
			t.Errorf("counts %+v", c)
		}
	}
	// Without positive evidence, or once the occurrence was Dispatched, the
	// outcome is the normal one.
	noTelemetry := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, UID: "uid-quiet"}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask}}
	noTelemetry.Status.Phase = v1alpha1.PhaseFailed
	hadPod := refusedSession(name, "uid-pod")
	hadPod.Status.PodName = name
	for what, c := range map[string]struct {
		s     *v1alpha1.AgentSession
		state string
	}{
		"failed with no agent report, no refusal condition": {noTelemetry, v1alpha1.OccurrenceClaimed},
		"refused, but a pod is recorded":                    {hadPod, v1alpha1.OccurrenceClaimed},
		"refused, but the occurrence was Dispatched":        {refusedSession(name, "uid-late"), v1alpha1.OccurrenceDispatched},
	} {
		w := &v1alpha1.ScheduledWork{}
		w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(at), State: c.state, Ending: true, Reason: ReasonCancelledAtRevalidation}}
		Record(w, at, name, Outcome(c.s, at))
		if o := w.Status.Occurrences[0]; o.State != v1alpha1.OccurrenceFailed {
			t.Errorf("%s: %+v", what, o)
		}
	}
}

// refusedSession is a session the reconciler refused before its first pod.
func refusedSession(name string, uid types.UID) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, UID: uid}, Spec: v1alpha1.AgentSessionSpec{Mode: v1alpha1.ModeTask}}
	s.Status.Phase = v1alpha1.PhaseFailed
	s.Status.Conditions = []metav1.Condition{{Type: PodReadyCondition, Status: metav1.ConditionFalse, Reason: SessionInvalidReason, Message: "refused"}}
	return s
}
