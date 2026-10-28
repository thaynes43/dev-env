package reminders

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

const testNS = "dev-env-system"

// fakeSender stands in for Pushover: it counts sends and answers from a script.
type fakeSender struct {
	mu    sync.Mutex
	calls []Message
	// errs answers call i with errs[i]; calls past the script succeed.
	errs []error
	// during runs inside Send, before it answers: a concurrent edit or cancel.
	during func()
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	i := len(f.calls)
	f.calls = append(f.calls, m)
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}
	if i < len(f.errs) {
		return f.errs[i]
	}
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type env struct {
	t   *testing.T
	c   client.Client
	now time.Time
	s   *Scheduler
	snd *fakeSender
	cal *FakeCalendar
}

func newEnv(t *testing.T) *env {
	t.Helper()
	sch := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(sch); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(sch).WithStatusSubresource(&v1alpha1.Reminder{}).Build()
	e := &env{t: t, c: c, now: utc("2026-10-28T12:00:00Z"), snd: &fakeSender{}, cal: &FakeCalendar{}}
	e.s = &Scheduler{Client: c, Reader: c, Namespace: testNS, Sender: e.snd, Calendar: e.cal, Log: logr.Discard(),
		Now: func() time.Time { return e.now }}
	return e
}

func (e *env) create(name string, edit func(*v1alpha1.ReminderSpec)) {
	e.t.Helper()
	r := &v1alpha1.Reminder{
		ObjectMeta: metav1.ObjectMeta{Namespace: testNS, Name: name},
		Spec: v1alpha1.ReminderSpec{Title: "Renew the VPN", Body: "pay it", Owner: "session/s", Due: "2026-10-28T13:00",
			TimeZone: "UTC", Targets: []string{"pushover:default"}},
	}
	if edit != nil {
		edit(&r.Spec)
	}
	if err := e.c.Create(context.Background(), r); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) get(name string) *v1alpha1.Reminder {
	e.t.Helper()
	var r v1alpha1.Reminder
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: testNS, Name: name}, &r); err != nil {
		e.t.Fatal(err)
	}
	return &r
}

func (e *env) tick() { e.s.Tick(context.Background()) }

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

// mutate edits the reminder as the API would: a spec update on a fresh copy.
func (e *env) mutate(name string, edit func(*v1alpha1.Reminder)) {
	e.t.Helper()
	r := e.get(name)
	edit(r)
	if err := e.c.Update(context.Background(), r); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) wantState(name, state string) *v1alpha1.Reminder {
	e.t.Helper()
	r := e.get(name)
	if r.Status.State != state {
		e.t.Fatalf("state %q (%s), want %q; status %+v", r.Status.State, r.Status.Message, state, r.Status)
	}
	return r
}

func (e *env) wantSends(n int) {
	e.t.Helper()
	if got := e.snd.count(); got != n {
		e.t.Fatalf("%d sends, want %d", got, n)
	}
}

func TestOneShotFiresOnceAndIsIdempotentOnReplay(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick() // plans; not yet due
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if r.Status.NextDue == nil || !r.Status.NextDue.Time.Equal(utc("2026-10-28T13:00:00Z")) || r.Status.Occurrence != 1 {
		t.Fatalf("plan: %+v", r.Status)
	}
	e.wantSends(0)
	e.advance(time.Hour)
	e.tick()
	e.wantSends(1)
	r = e.wantState("rem-a", v1alpha1.ReminderSent)
	if r.Status.Deliveries[0].State != v1alpha1.DeliverySent || r.Status.NextDue != nil {
		t.Errorf("status %+v", r.Status)
	}
	if m := e.snd.calls[0]; m.Title != "Renew the VPN" || m.Body != "pay it" {
		t.Errorf("message %+v", m)
	}
	// Replays, a restart (a new Scheduler on the same store) and a later pass send nothing.
	e.tick()
	e.advance(24 * time.Hour)
	e.tick()
	s2 := *e.s
	s2.Tick(context.Background())
	e.wantSends(1)
}

func TestStaleSchedulerCannotSendAgain(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	// Two leaders: A listed the reminder, then B (or A's own earlier pass) finished it.
	stale := e.get("rem-a")
	e.tick()
	e.wantSends(1)
	e.s.tickOne(context.Background(), stale)
	e.wantSends(1)
}

func TestCancelWinsTheRaceAgainstTheFire(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	stale := e.get("rem-a") // the scheduler's copy, read just before the cancel
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.s.tickOne(context.Background(), stale) // its claim loses the compare-and-set
	e.wantSends(0)
	e.tick()
	e.wantState("rem-a", v1alpha1.ReminderCancelled)
	e.advance(time.Hour)
	e.tick()
	e.wantSends(0)
}

func TestEditWinsTheRaceAgainstTheFire(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour) // 13:00, due
	stale := e.get("rem-a")
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-10-28T15:00" })
	e.s.tickOne(context.Background(), stale)
	e.wantSends(0)
	e.tick() // replans for 15:00
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if !r.Status.NextDue.Time.Equal(utc("2026-10-28T15:00:00Z")) {
		t.Fatalf("next due %v", r.Status.NextDue)
	}
	e.wantSends(0)
	e.advance(2 * time.Hour)
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestFireWinsAgainstALateCancelAndTheResultIsKept(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	// The owner cancels and edits while the request is in flight.
	e.snd.during = func() {
		e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true; r.Spec.Title = "edited" })
	}
	e.tick()
	e.wantSends(1)
	r := e.get("rem-a")
	if r.Status.Deliveries[0].State != v1alpha1.DeliverySent {
		t.Fatalf("the result of a request already made was lost: %+v", r.Status)
	}
	e.tick() // the cancel is honoured afterwards, and nothing is sent again
	e.wantSends(1)
	// The reminder had already finished (sent), so the cancel leaves it sent.
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestAmbiguousAfterACrashIsNeverResent(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	// A previous process claimed the delivery and died before it recorded a result.
	r := e.get("rem-a")
	r.Status.Deliveries = []v1alpha1.ReminderDelivery{{Target: "pushover:default", Occurrence: 1, State: v1alpha1.DeliveryClaimed, Attempts: 1, UpdatedAt: metav1.NewTime(e.now)}}
	if err := e.c.Status().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	e.tick()
	r = e.wantState("rem-a", v1alpha1.ReminderFailed)
	if d := r.Status.Deliveries[0]; d.State != v1alpha1.DeliveryAmbiguous {
		t.Fatalf("delivery %+v", d)
	}
	for range 3 {
		e.advance(time.Hour)
		e.tick()
	}
	e.wantSends(0)
}

func TestAnAmbiguousSendResultIsRecordedAndNotRetried(t *testing.T) {
	e := newEnv(t)
	e.snd.errs = []error{errors.New("timeout after the request left")}
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick()
	r := e.wantState("rem-a", v1alpha1.ReminderFailed)
	if r.Status.Deliveries[0].State != v1alpha1.DeliveryAmbiguous {
		t.Fatalf("%+v", r.Status.Deliveries)
	}
	e.advance(time.Hour)
	e.tick()
	e.wantSends(1)
}

func TestRetriesAreBoundedThenFailed(t *testing.T) {
	e := newEnv(t)
	notSent := fmt.Errorf("%w: credentials", ErrNotSent)
	e.snd.errs = []error{notSent, notSent, notSent, notSent}
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick() // attempt 1
	e.wantSends(1)
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if d := r.Status.Deliveries[0]; d.State != v1alpha1.DeliveryRetry || d.NotBefore == nil {
		t.Fatalf("delivery %+v", d)
	}
	e.tick() // inside the backoff
	e.wantSends(1)
	e.advance(61 * time.Second)
	e.tick() // attempt 2
	e.wantSends(2)
	e.tick()
	e.wantSends(2)
	e.advance(5*time.Minute + time.Second)
	e.tick() // attempt 3, the last
	e.wantSends(3)
	r = e.wantState("rem-a", v1alpha1.ReminderFailed)
	if d := r.Status.Deliveries[0]; d.State != v1alpha1.DeliveryFailed || d.Attempts != 3 {
		t.Fatalf("delivery %+v", d)
	}
	e.advance(time.Hour)
	e.tick()
	e.wantSends(3)
}

func TestRetryThatSucceeds(t *testing.T) {
	e := newEnv(t)
	e.snd.errs = []error{ErrNotSent}
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.advance(2 * time.Minute)
	e.tick()
	e.wantSends(2)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestARefusalFailsAtOnce(t *testing.T) {
	e := newEnv(t)
	e.snd.errs = []error{ErrRejected}
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderFailed)
}

func TestUnconfiguredCalendarIsAnHonestSkip(t *testing.T) {
	e := newEnv(t)
	e.s.Calendar = nil
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Targets = []string{"pushover:default", "calendar:haynesnetwork"} })
	e.tick()
	r := e.get("rem-a")
	if len(r.Status.Calendar) != 1 || r.Status.Calendar[0].State != v1alpha1.CalendarSkipped || r.Status.Calendar[0].Reason != "calendar: unconfigured" {
		t.Fatalf("calendar status %+v", r.Status.Calendar)
	}
	rv := r.ResourceVersion
	e.tick() // nothing changes, so nothing is written
	if got := e.get("rem-a").ResourceVersion; got != rv {
		t.Errorf("a repeat pass wrote the status: %s -> %s", rv, got)
	}
	e.advance(time.Hour)
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestUnconfiguredPushoverIsAnHonestSkip(t *testing.T) {
	e := newEnv(t)
	e.s.Sender = nil
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick()
	r := e.wantState("rem-a", v1alpha1.ReminderSent)
	if d := r.Status.Deliveries[0]; d.State != v1alpha1.DeliverySkipped || d.Reason != "pushover: unconfigured" {
		t.Errorf("delivery %+v", d)
	}
}

func TestCalendarEntryIsIdempotentEditedAndRemoved(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) {
		s.Targets = []string{"pushover:default", "calendar:haynesnetwork"}
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"MO"}}
		s.Due = "2026-11-02T09:00"
		s.TimeZone = "America/New_York"
	})
	e.tick()
	e.tick()
	e.tick()
	if e.cal.Count() != 1 || e.cal.Upserts != 1 {
		t.Fatalf("replays made %d entries in %d upserts", e.cal.Count(), e.cal.Upserts)
	}
	ev := e.cal.Events["haynesnetwork/"+EventID(testNS, "rem-a")]
	if ev.Title != "Renew the VPN" || ev.Start != "2026-11-02T09:00" || ev.TimeZone != "America/New_York" || ev.RRule != "FREQ=WEEKLY;BYDAY=MO" {
		t.Errorf("event %+v", ev)
	}
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Title = "Renew the VPN now" })
	e.tick()
	if e.cal.Upserts != 2 || e.cal.Count() != 1 {
		t.Errorf("an edit: %d upserts, %d entries", e.cal.Upserts, e.cal.Count())
	}
	// A removed target takes its entry down.
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"pushover:default"} })
	e.tick()
	if e.cal.Count() != 0 {
		t.Errorf("the removed target's entry is still there")
	}
	// Cancel removes the entry too.
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"pushover:default", "calendar:haynesnetwork"} })
	e.tick()
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.tick()
	e.wantState("rem-a", v1alpha1.ReminderCancelled)
	if e.cal.Count() != 0 {
		t.Error("cancel left the calendar entry")
	}
}

func TestCalendarFailureBacksOffAndCancelRetries(t *testing.T) {
	e := newEnv(t)
	e.cal.FailUpsert = true
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Targets = []string{"calendar:haynesnetwork"} })
	e.tick()
	r := e.get("rem-a")
	if c := r.Status.Calendar[0]; c.State != v1alpha1.CalendarFailed || c.Reason != "upsert failed" {
		t.Fatalf("%+v", c)
	}
	e.cal.FailUpsert = false
	e.tick() // inside the backoff
	if e.cal.Count() != 0 {
		t.Error("retried inside the backoff")
	}
	e.advance(6 * time.Minute)
	e.tick()
	if e.cal.Count() != 1 {
		t.Fatal("did not retry after the backoff")
	}
	e.cal.FailDelete = true
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Cancelled = true })
	e.tick()
	e.wantState("rem-a", v1alpha1.ReminderPending) // not cancelled until the entry is gone
	e.cal.FailDelete = false
	e.tick()
	e.wantState("rem-a", v1alpha1.ReminderCancelled)
	if e.cal.Count() != 0 {
		t.Error("entry left")
	}
}

func TestRecurringFiresEachOccurrenceOnceAndEnds(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) {
		s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "daily", Count: 2}
	})
	e.tick()
	e.advance(time.Hour) // 13:00 day 1
	e.tick()
	e.tick()
	e.wantSends(1)
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if r.Status.Occurrence != 2 || !r.Status.NextDue.Time.Equal(utc("2026-10-29T13:00:00Z")) || len(r.Status.Deliveries) != 0 {
		t.Fatalf("after occurrence 1: %+v", r.Status)
	}
	e.advance(24 * time.Hour)
	e.tick()
	e.tick()
	e.wantSends(2)
	r = e.wantState("rem-a", v1alpha1.ReminderSent)
	if len(r.Status.Attempts) != 2 || r.Status.NextDue != nil {
		t.Errorf("status %+v", r.Status)
	}
}

func TestRecurringSkipsAMissedOccurrenceBeyondTheGrace(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "daily"} })
	e.tick()
	e.advance(72*time.Hour + time.Hour) // the scheduler was down for three days
	e.tick()
	e.wantSends(0)
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if a := r.Status.Attempts[len(r.Status.Attempts)-1]; a.Outcome != "missed" {
		t.Errorf("attempts %+v", r.Status.Attempts)
	}
	// It walks forward one missed occurrence a pass until it reaches one in grace.
	for range 5 {
		e.tick()
	}
	if got := e.snd.count(); got > 1 {
		t.Errorf("%d sends", got)
	}
}

func TestOneShotFiresLateHoweverLate(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(10 * 24 * time.Hour)
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestRecurringFailureKeepsTheSeriesPending(t *testing.T) {
	e := newEnv(t)
	e.snd.errs = []error{ErrRejected}
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "daily"} })
	e.tick()
	e.advance(time.Hour)
	e.tick()
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if r.Status.Occurrence != 2 {
		t.Errorf("did not move on: %+v", r.Status)
	}
	e.advance(24 * time.Hour)
	e.tick()
	e.wantSends(2)
}

func TestAnInvalidScheduleFailsHonestly(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.TimeZone = "Mars/Olympus" })
	e.tick()
	r := e.wantState("rem-a", v1alpha1.ReminderFailed)
	if r.Status.Message == "" {
		t.Error("no message")
	}
	e.wantSends(0)
}

func TestWritesAreNotMadeForOtherNamespacesOrFinalStates(t *testing.T) {
	e := newEnv(t)
	other := &v1alpha1.Reminder{ObjectMeta: metav1.ObjectMeta{Namespace: "elsewhere", Name: "rem-x"},
		Spec: v1alpha1.ReminderSpec{Title: "x", Owner: "o", Due: "2026-10-28T11:00", TimeZone: "UTC", Targets: []string{"pushover:default"}}}
	if err := e.c.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	e.tick()
	e.wantSends(0)
	var got v1alpha1.Reminder
	if err := e.c.Get(context.Background(), types.NamespacedName{Namespace: "elsewhere", Name: "rem-x"}, &got); err != nil || got.Status.State != "" {
		t.Errorf("touched %+v %v", got.Status, err)
	}
	_ = apierrors.IsNotFound
}

func TestStartStopsWithItsContextAndNeedsTheLeader(t *testing.T) {
	e := newEnv(t)
	if !e.s.NeedLeaderElection() {
		t.Error("the scheduler must be leader-only")
	}
	e.s.Every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.s.Start(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not stop")
	}
}

// An edit during a claim: the old occurrence resolves only for itself (a crash
// leaves it ambiguous, never resent) and the new due time still fires, once.
func TestEditDuringTheClaimStillYieldsTheNewSchedule(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour) // 13:00, due
	r := e.get("rem-a")
	r.Status.Deliveries = []v1alpha1.ReminderDelivery{{Target: "pushover:default", Occurrence: 1, State: v1alpha1.DeliveryClaimed, Attempts: 1, UpdatedAt: metav1.NewTime(e.now)}}
	if err := e.c.Status().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-10-28T16:00" })
	e.tick() // the stale claim becomes ambiguous; the reminder is replanned, not closed
	r = e.wantState("rem-a", v1alpha1.ReminderPending)
	if r.Status.NextDue == nil || !r.Status.NextDue.Time.Equal(utc("2026-10-28T16:00:00Z")) || len(r.Status.Deliveries) != 0 {
		t.Fatalf("not replanned: %+v", r.Status)
	}
	e.wantSends(0)
	e.advance(3 * time.Hour)
	e.tick()
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

// The same edit lands while the request is in flight: its result is kept, the
// old time is never sent again, and the new time fires.
func TestEditWhileTheRequestIsInFlightKeepsTheResultAndPlansTheNewDue(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.snd.during = func() {
		e.snd.during = nil
		e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-10-28T16:00" })
	}
	e.tick()
	e.wantSends(1)
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if !r.Status.NextDue.Time.Equal(utc("2026-10-28T16:00:00Z")) {
		t.Fatalf("next due %v; status %+v", r.Status.NextDue, r.Status)
	}
	e.tick()
	e.wantSends(1)
	e.advance(3 * time.Hour)
	e.tick()
	e.wantSends(2)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

// An edit during the retry backoff drops the retry (nothing was sent) and the
// new time fires once.
func TestEditDuringBackoffDropsTheRetryAndFiresTheNewDue(t *testing.T) {
	e := newEnv(t)
	e.snd.errs = []error{ErrNotSent}
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.tick() // attempt 1: not sent, backing off
	e.wantSends(1)
	e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-10-28T16:00" })
	e.tick()
	r := e.wantState("rem-a", v1alpha1.ReminderPending)
	if !r.Status.NextDue.Time.Equal(utc("2026-10-28T16:00:00Z")) {
		t.Fatalf("next due %v", r.Status.NextDue)
	}
	e.advance(10 * time.Minute) // past the old backoff
	e.tick()
	e.wantSends(1) // the old retry is not sent late
	var superseded bool
	for _, a := range r.Status.Attempts {
		superseded = superseded || a.Detail == supersededReason
	}
	if !superseded {
		t.Errorf("no record of the dropped retry: %+v", r.Status.Attempts)
	}
	e.advance(3 * time.Hour)
	e.tick()
	e.wantSends(2)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

// An edit after the send but before the occurrence closed (the close write
// failed): the sent delivery is not sent again, and the new schedule is planned.
func TestEditAfterTheSendDoesNotResendAndPlansTheNewSchedule(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "daily"} })
	e.tick()
	e.advance(time.Hour)
	r := e.get("rem-a")
	r.Status.Deliveries = []v1alpha1.ReminderDelivery{{Target: "pushover:default", Occurrence: 1, State: v1alpha1.DeliverySent, Attempts: 1, UpdatedAt: metav1.NewTime(e.now)}}
	if err := e.c.Status().Update(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	e.mutate("rem-a", func(r *v1alpha1.Reminder) {
		r.Spec.Recurrence = &v1alpha1.ReminderRecurrence{Freq: "weekly", ByDay: []string{"FR"}}
		r.Spec.Due = "2026-10-30T09:00"
	})
	e.tick()
	e.wantSends(0)
	r = e.wantState("rem-a", v1alpha1.ReminderPending)
	if !r.Status.NextDue.Time.Equal(utc("2026-10-30T09:00:00Z")) {
		t.Fatalf("next due %v", r.Status.NextDue)
	}
	e.advance(48 * time.Hour)
	e.tick()
	e.wantSends(1)
}

// A one-shot edited to the same instant in another zone is not sent twice.
func TestEditToTheSameInstantIsNotASecondSend(t *testing.T) {
	e := newEnv(t)
	e.create("rem-a", nil)
	e.tick()
	e.advance(time.Hour)
	e.snd.during = func() {
		e.snd.during = nil
		// 13:00 UTC is 09:00 in New York (EDT).
		e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Due = "2026-10-28T09:00"; r.Spec.TimeZone = "America/New_York" })
	}
	e.tick()
	e.tick()
	e.wantSends(1)
	e.wantState("rem-a", v1alpha1.ReminderSent)
}

func TestCalendarStatusStaysWithinItsBoundWhateverTheAdapter(t *testing.T) {
	check := func(t *testing.T, e *env) {
		t.Helper()
		if n := len(e.get("rem-a").Status.Calendar); n > maxCalendarEntries {
			t.Fatalf("%d calendar entries, the CRD allows %d", n, maxCalendarEntries)
		}
	}
	retarget := func(e *env, ref string) {
		e.mutate("rem-a", func(r *v1alpha1.Reminder) { r.Spec.Targets = []string{"pushover:default", "calendar:" + ref} })
		e.tick()
	}
	t.Run("an adapter that deletes", func(t *testing.T) {
		e := newEnv(t)
		e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Targets = []string{"calendar:c0"} })
		e.tick()
		for i := 1; i <= 14; i++ {
			retarget(e, fmt.Sprintf("c%d", i))
			check(t, e)
		}
		if e.cal.Count() != 1 || len(e.get("rem-a").Status.Calendar) != 1 {
			t.Errorf("%d entries standing, %d in the status", e.cal.Count(), len(e.get("rem-a").Status.Calendar))
		}
	})
	t.Run("an adapter whose deletes fail", func(t *testing.T) {
		e := newEnv(t)
		e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Targets = []string{"calendar:c0"} })
		e.tick()
		e.cal.FailDelete = true
		for i := 1; i <= 14; i++ {
			e.advance(time.Minute)
			retarget(e, fmt.Sprintf("c%d", i))
			check(t, e)
		}
		if got := len(e.get("rem-a").Status.Calendar); got != maxCalendarEntries {
			t.Errorf("%d entries, want the bound %d", got, maxCalendarEntries)
		}
	})
	t.Run("no adapter at all", func(t *testing.T) {
		e := newEnv(t)
		e.s.Calendar = nil
		e.create("rem-a", func(s *v1alpha1.ReminderSpec) { s.Targets = []string{"calendar:c0"} })
		e.tick()
		r := e.get("rem-a")
		for i := 0; i < 12; i++ { // stale entries that still read as synced
			r.Status.Calendar = append(r.Status.Calendar, v1alpha1.ReminderCalendarSync{Target: fmt.Sprintf("calendar:old%d", i),
				State: v1alpha1.CalendarSynced, UpdatedAt: metav1.NewTime(e.now.Add(time.Duration(i) * time.Minute))})
		}
		if err := e.c.Status().Update(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		e.tick()
		check(t, e)
		got := e.get("rem-a").Status.Calendar
		if len(got) != maxCalendarEntries {
			t.Fatalf("%d entries", len(got))
		}
		if syncEntry(e.get("rem-a"), "calendar:c0") == nil {
			t.Error("the wanted target was pruned")
		}
		for _, c := range got {
			if c.Target == "calendar:old0" {
				t.Error("the oldest stale entry survived")
			}
		}
	})
}
