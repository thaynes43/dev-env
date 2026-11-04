package controller

// The releaser's page cases of DESIGN-054 S4b on envtest: a session's outcome
// pages through one keyed, one-shot Reminder in the item's namespace, recorded
// on the occurrence before the release; a page that cannot be created is
// recorded skipped and holds nothing; a restart or a lost record write never
// makes a second Reminder.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/pages"
)

// pageReminders lists the fixture item's page Reminders, from the API server.
func (f swFixture) pageReminders(t *testing.T) []v1alpha1.Reminder {
	t.Helper()
	var list v1alpha1.ReminderList
	if err := k8s.List(context.Background(), &list, client.InNamespace(f.key.Namespace), client.MatchingLabels{v1alpha1.LabelScheduledWork: string(f.uid)}); err != nil {
		t.Fatal(err)
	}
	return list.Items
}

// releasePage is the occurrence's page entry for (event, attempt), or nil.
func releasePage(o v1alpha1.ScheduledWorkOccurrence, event string, attempt int32) *v1alpha1.ScheduledWorkPage {
	for i := range o.Pages {
		if o.Pages[i].Event == event && o.Pages[i].Attempt == attempt {
			return &o.Pages[i]
		}
	}
	return nil
}

// wantReleasePage fails unless the stored occurrence records the page (event,
// attempt) under its keyed Reminder, and that Reminder is the only one of
// the item: a one-shot owned by the scheduled-work identity that targets the
// item's notify references. It returns the Reminder.
func (f swFixture) wantReleasePage(t *testing.T, event string, attempt int32) *v1alpha1.Reminder {
	t.Helper()
	w := f.item(t)
	o := f.occurrence(t)
	name := pages.Key(w, f.at, event, attempt)
	if p := releasePage(o, event, attempt); p == nil || p.Reminder != name {
		t.Fatalf("occurrence pages %+v: want %s attempt %d recorded as %s", o.Pages, event, attempt, name)
	}
	rs := f.pageReminders(t)
	if len(rs) != 1 || rs[0].Name != name {
		t.Fatalf("%d page Reminders, want the one %s", len(rs), name)
	}
	r := &rs[0]
	if r.Spec.Owner != pages.Owner || r.Spec.Recurrence != nil || !slices.Equal(r.Spec.Targets, w.Spec.Notify) ||
		!strings.HasSuffix(r.Spec.Title, ": "+w.Spec.Title) || !strings.Contains(r.Spec.Body, f.key.Name) || !strings.Contains(r.Spec.Body, f.name) {
		t.Errorf("page Reminder %s: spec %+v", name, r.Spec)
	}
	return r
}

// A suspended task with no task result is Failed, "ended without a task
// result": the failed page is recorded before the release, with the reason.
func TestScheduledWorkReleasePagesAFailure(t *testing.T) {
	startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := releaseFixtureSession(t, f, nil)
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	if n := len(f.pageReminders(t)); n != 0 {
		t.Fatalf("%d pages while the session runs", n)
	}
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	waitReleased(t, s.Name)
	r := f.wantReleasePage(t, v1alpha1.PageEventFailed, 0)
	if !strings.HasPrefix(r.Spec.Title, "Scheduled work failed") || !strings.Contains(r.Spec.Body, scheduledwork.ReasonNoTaskResult) {
		t.Errorf("failed page title %q body %q", r.Spec.Title, r.Spec.Body)
	}
}

// A never-launched agent that agentd refused past its start deadline is
// Failed, "not started before grace ended", and pages that.
func TestScheduledWorkReleasePagesNotStartedBeforeGrace(t *testing.T) {
	var ahead atomic.Int64
	startOperator(t, aheadClock(&ahead))
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDispatched)
	s := releaseFixtureSession(t, f, func(s *v1alpha1.AgentSession) {
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	})
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	ahead.Store(int64(10 * time.Minute))
	eventually(t, "the heartbeat", func() error {
		live := session(t, s.Name)
		hb := metav1.Now()
		live.Status.Agent = &v1alpha1.AgentStatus{Boot: protocol.BootReady, Status: protocol.AgentFailed, Message: protocol.StartDeadlinePassed, LastHeartbeat: &hb}
		return k8s.Status().Update(context.Background(), live)
	})
	f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
	waitReleased(t, s.Name)
	r := f.wantReleasePage(t, v1alpha1.PageEventFailed, 0)
	if !strings.Contains(r.Spec.Body, scheduledwork.ReasonNotStarted) {
		t.Errorf("failed page body %q", r.Spec.Body)
	}
}

// A headless success pages its result once the agent's reported outcome is
// in, and not before: during the wait for it the intent is stored and no
// Reminder exists. The page says what was reported, not that it is done.
func TestScheduledWorkReleasePagesTheReportedResult(t *testing.T) {
	op := startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := releaseFixtureSession(t, f, nil)
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.Now()}, nil)
	f.waitOccurrence(t, v1alpha1.OccurrenceSucceeded)
	holdsFinalizer(t, s.Name, time.Second, op.passes.held(s.Name, func(p releasePass) bool {
		return p.agent == protocol.AgentExited && p.requeue > 0 && p.requeue <= scheduledwork.ReportWait+scheduledwork.ReportClockSkew+time.Second
	}))
	if p := releasePage(f.occurrence(t), v1alpha1.PageEventResult, 0); p == nil || p.Reminder != "" || p.State != "" {
		t.Fatalf("during the wait the result page should be a stored intent: %+v", f.occurrence(t).Pages)
	}
	if n := len(f.pageReminders(t)); n != 0 {
		t.Fatalf("%d page Reminders before the reported outcome", n)
	}
	eventually(t, "the retained outcome", func() error {
		live := session(t, s.Name)
		live.Status.TaskOutcome = &v1alpha1.TaskOutcomeStatus{SessionUID: string(live.UID), PodUID: string(pod.UID),
			Record: v1alpha1.TaskOutcomeRecordStatus{Summary: "opened the audit PR", PRURLs: []string{"https://github.com/thaynes43/haynes-ops/pull/4242"}}}
		return k8s.Status().Update(context.Background(), live)
	})
	waitReleased(t, s.Name)
	r := f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
	for _, want := range []string{"opened the audit PR", "not verified", "https://github.com/thaynes43/haynes-ops/pull/4242", "Task result Succeeded"} {
		if !strings.Contains(r.Spec.Body, want) {
			t.Errorf("result page body %q lacks %q", r.Spec.Body, want)
		}
	}
	if !strings.HasPrefix(r.Spec.Title, "Scheduled work reported success") {
		t.Errorf("result page title %q", r.Spec.Title)
	}
}

// An interactive end is Ended, "participation not observed", and pages its
// result as ended.
func TestScheduledWorkReleasePagesAnInteractiveEnd(t *testing.T) {
	startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	s := releaseFixtureSession(t, f, suspendedAtCreate)
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	waitReleased(t, s.Name)
	r := f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
	if !strings.HasPrefix(r.Spec.Title, "Scheduled work ended") || !strings.Contains(r.Spec.Body, scheduledwork.ReasonParticipationNotObserved) {
		t.Errorf("result page title %q body %q", r.Spec.Title, r.Spec.Body)
	}
}

// reminderCreateFails fails every Reminder create with err; every other call
// goes through.
type reminderCreateFails struct {
	client.Client
	err   error
	calls atomic.Int32
}

func (c *reminderCreateFails) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if _, ok := o.(*v1alpha1.Reminder); ok {
		c.calls.Add(1)
		return c.err
	}
	return c.Client.Create(ctx, o, opts...)
}

// A page Reminder that cannot be created (the kind not served, the create
// refused, or any other create error) is recorded skipped, with no Reminder,
// and holds nothing: the record is stored and the session is released.
func TestScheduledWorkReleasePageFailuresHoldNothing(t *testing.T) {
	for name, fail := range map[string]error{
		"kind not served": &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "Reminder"}, SearchedVersions: []string{"v1alpha1"}},
		"create refused":  apierrors.NewForbidden(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "reminders"}, "", errors.New("injected: no create grant")),
		"create failed":   errors.New("injected: the create failed"),
	} {
		t.Run(name, func(t *testing.T) {
			var failing *reminderCreateFails
			startOperator(t, func(r *Reconciler) {
				failing = &reminderCreateFails{Client: r.Client, err: fail}
				r.Client = failing
			})
			f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
			s := releaseFixtureSession(t, f, suspendedAtCreate)
			f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
			waitReleased(t, s.Name)
			o := f.occurrence(t)
			if p := releasePage(o, v1alpha1.PageEventResult, 0); p == nil || p.State != "skipped" || p.Reminder != "" {
				t.Fatalf("pages %+v", o.Pages)
			}
			if failing.calls.Load() == 0 || len(f.pageReminders(t)) != 0 {
				t.Errorf("creates %d, reminders %d", failing.calls.Load(), len(f.pageReminders(t)))
			}
		})
	}
}

// pageRecordFails fails the status write that records a created page (an
// occurrence page that names a Reminder) and the patch that lifts the
// scheduled-work finalizer, as an operator that stops between the two would
// leave things.
type pageRecordFails struct {
	*failingClient
	failed atomic.Int32
}

func (c *pageRecordFails) Status() client.SubResourceWriter {
	return pageRecordStatus{SubResourceWriter: c.failingClient.Status(), c: c}
}

type pageRecordStatus struct {
	client.SubResourceWriter
	c *pageRecordFails
}

func (s pageRecordStatus) Update(ctx context.Context, o client.Object, opts ...client.SubResourceUpdateOption) error {
	if w, ok := o.(*v1alpha1.ScheduledWork); ok {
		for _, occ := range w.Status.Occurrences {
			if slices.ContainsFunc(occ.Pages, func(p v1alpha1.ScheduledWorkPage) bool { return p.Reminder != "" }) {
				s.c.failed.Add(1)
				return apierrors.NewServiceUnavailable("injected: the page record write failed")
			}
		}
	}
	return s.SubResourceWriter.Update(ctx, o, opts...)
}

// The page Reminder was created but its record was lost, and the operator
// stopped before the release. The next operator records the same Reminder
// and releases: one Reminder, recorded once.
func TestScheduledWorkReleasePagesOnceAcrossARestart(t *testing.T) {
	blocked := NewScheduledWorkReleaseHoldsCounter()
	var failing *pageRecordFails
	first := startOperator(t, withBlockedCounter(blocked), func(r *Reconciler) {
		failing = &pageRecordFails{failingClient: &failingClient{Client: r.Client}}
		failing.failRelease.Store(true)
		r.Client = failing
	})
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	s := releaseFixtureSession(t, f, suspendedAtCreate)
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	holdsFinalizer(t, s.Name, time.Second, counted(blocked, releaseBlockedPatch))
	if failing.failed.Load() == 0 {
		t.Fatal("no page record write was attempted")
	}
	if p := releasePage(f.occurrence(t), v1alpha1.PageEventResult, 0); p == nil || p.Reminder != "" || p.State != "" {
		t.Fatalf("the lost record should leave the intent stored: %+v", f.occurrence(t).Pages)
	}
	if n := len(f.pageReminders(t)); n != 1 {
		t.Fatalf("%d page Reminders after the lost record, want 1", n)
	}
	first.stop()

	startOperator(t)
	waitReleased(t, s.Name)
	f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
	if c := f.item(t).Status.Counts; c != (v1alpha1.ScheduledWorkCounts{Ended: 1}) {
		t.Errorf("counts %+v", c)
	}
}

// An item with no notify target records no page and creates no Reminder; nor
// does an occurrence that was already terminal before the releaser saw it
// (a page goes out only with the record that makes it).
func TestScheduledWorkReleaseNoPageWithoutItsRecordOrTarget(t *testing.T) {
	op := startOperator(t)
	t.Run("no notify", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		eventually(t, "drop the notify target", func() error {
			w := f.item(t)
			w.Spec.Notify = nil
			return k8s.Update(context.Background(), w)
		})
		s := releaseFixtureSession(t, f, suspendedAtCreate)
		f.waitOccurrence(t, v1alpha1.OccurrenceFailed)
		waitReleased(t, s.Name)
		if o := f.occurrence(t); len(o.Pages) != 0 || len(f.pageReminders(t)) != 0 {
			t.Errorf("pages %+v reminders %d", o.Pages, len(f.pageReminders(t)))
		}
	})
	t.Run("already terminal", func(t *testing.T) {
		f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
		f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
			st.Occurrences[0].State, st.Occurrences[0].Reason = v1alpha1.OccurrenceFailed, "recorded earlier"
			st.Counts.Failed = 1
		})
		s := releaseFixtureSession(t, f, suspendedAtCreate)
		waitReleased(t, s.Name)
		if o := f.occurrence(t); len(o.Pages) != 0 || len(f.pageReminders(t)) != 0 {
			t.Errorf("pages %+v reminders %d", o.Pages, len(f.pageReminders(t)))
		}
		if slices.Contains(op.writes.orderLog(), "update status *v1alpha1.ScheduledWork "+f.key.Name) {
			t.Errorf("the releaser wrote the item: %v", op.writes.orderLog())
		}
	})
}

// The pages list stays within ScheduledWorkMaxPages: the result of a last
// attempt, on an occurrence whose list is full, drops the oldest attempt's
// first entry and records the new page.
func TestScheduledWorkReleasePagesWithinTheCap(t *testing.T) {
	startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	retried := metav1.NewTime(f.at.Add(time.Hour))
	f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
		o := &st.Occurrences[0]
		o.Attempt, o.RetriedAt = 3, &retried
		o.Pages = []v1alpha1.ScheduledWorkPage{
			{Event: v1alpha1.PageEventDue, Attempt: 0, Reminder: "swp-seed-due-0", State: "sent"},
			{Event: v1alpha1.PageEventParked, Attempt: 0, Reminder: "swp-seed-parked-0", State: "sent"},
			{Event: v1alpha1.PageEventDue, Attempt: 1, Reminder: "swp-seed-due-1", State: "sent"},
			{Event: v1alpha1.PageEventParked, Attempt: 1, Reminder: "swp-seed-parked-1", State: "sent"},
			{Event: v1alpha1.PageEventDue, Attempt: 2, Reminder: "swp-seed-due-2", State: "sent"},
			{Event: v1alpha1.PageEventParked, Attempt: 2, Reminder: "swp-seed-parked-2", State: "sent"},
			{Event: v1alpha1.PageEventDue, Attempt: 3, Reminder: "swp-seed-due-3", State: "sent"},
			{Event: v1alpha1.PageEventLink, Attempt: 3, Reminder: "swp-seed-link-3", State: "sent"},
		}
	})
	s := releaseFixtureSession(t, f, suspendedAtCreate)
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	waitReleased(t, s.Name)
	o := f.occurrence(t)
	if len(o.Pages) != v1alpha1.ScheduledWorkMaxPages || releasePage(o, v1alpha1.PageEventDue, 0) != nil || releasePage(o, v1alpha1.PageEventParked, 0) == nil {
		t.Fatalf("pages %+v", o.Pages)
	}
	f.wantReleasePage(t, v1alpha1.PageEventResult, 3)
}

// pageRecordRaced writes the item once, as another writer would, just before
// the releaser's first page record write reaches the API server, so that
// write meets a conflict.
type pageRecordRaced struct {
	client.Client
	raced atomic.Bool
}

func (c *pageRecordRaced) Status() client.SubResourceWriter {
	return pageRecordRace{SubResourceWriter: c.Client.Status(), c: c}
}

type pageRecordRace struct {
	client.SubResourceWriter
	c *pageRecordRaced
}

func (s pageRecordRace) Update(ctx context.Context, o client.Object, opts ...client.SubResourceUpdateOption) error {
	if w, ok := o.(*v1alpha1.ScheduledWork); ok && !s.c.raced.Load() {
		for _, occ := range w.Status.Occurrences {
			if slices.ContainsFunc(occ.Pages, func(p v1alpha1.ScheduledWorkPage) bool { return p.Reminder != "" || p.State != "" }) {
				s.c.raced.Store(true)
				var live v1alpha1.ScheduledWork
				if err := k8s.Get(ctx, client.ObjectKeyFromObject(w), &live); err != nil {
					return err
				}
				if live.Annotations == nil {
					live.Annotations = map[string]string{}
				}
				live.Annotations["test.dev-env.haynesops.com/raced"] = "yes"
				if err := k8s.Update(ctx, &live); err != nil {
					return err
				}
				break
			}
		}
	}
	return s.SubResourceWriter.Update(ctx, o, opts...)
}

// A page record write that meets a conflict reads the item again and writes
// once more: the page is recorded under the one Reminder.
func TestScheduledWorkReleasePageRecordRetriesAConflict(t *testing.T) {
	var racing *pageRecordRaced
	startOperator(t, func(r *Reconciler) {
		racing = &pageRecordRaced{Client: r.Client}
		r.Client = racing
	})
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	s := releaseFixtureSession(t, f, suspendedAtCreate)
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	waitReleased(t, s.Name)
	if !racing.raced.Load() {
		t.Fatal("the page record write never met the race")
	}
	f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
}

// A skipped page is final: a later pass of the releaser, while the finalizer
// still holds the derived name, creates no second Reminder for it.
func TestScheduledWorkReleaseASkippedPageIsFinal(t *testing.T) {
	var ahead atomic.Int64
	var failing *reminderCreateFails
	op := startOperator(t, aheadClock(&ahead), func(r *Reconciler) {
		failing = &reminderCreateFails{Client: r.Client, err: errors.New("injected: the create failed")}
		r.Client = failing
	})
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	deadline := time.Now().Add(30 * time.Second).Truncate(time.Second)
	s := releaseFixtureSession(t, f, func(s *v1alpha1.AgentSession) {
		suspendedAtCreate(s)
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = deadline.UTC().Format(time.RFC3339)
	})
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	holdsFinalizer(t, s.Name, time.Second, op.passes.held(s.Name, func(p releasePass) bool { return p.requeue > scheduledwork.ReleaseMargin }))
	if p := releasePage(f.occurrence(t), v1alpha1.PageEventResult, 0); p == nil || p.State != "skipped" {
		t.Fatalf("pages %+v", f.occurrence(t).Pages)
	}
	poke(t, s.Name)
	holdsFinalizer(t, s.Name, time.Second, op.passes.held(s.Name, func(p releasePass) bool { return p.requeue > scheduledwork.ReleaseMargin }))
	ahead.Store(int64(time.Until(deadline) + scheduledwork.ReleaseMargin + 10*time.Second))
	poke(t, s.Name)
	waitReleased(t, s.Name)
	if n := failing.calls.Load(); n != 1 {
		t.Errorf("%d Reminder creates, want 1: a skipped page was tried again", n)
	}
}

// reminderCreateHangs blocks every Reminder create until its context ends,
// and records the deadline it was given; every other call goes through.
type reminderCreateHangs struct {
	client.Client
	hang  bool
	left  atomic.Int64
	calls atomic.Int32
}

func (c *reminderCreateHangs) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if _, ok := o.(*v1alpha1.Reminder); ok {
		c.calls.Add(1)
		if dl, ok := ctx.Deadline(); ok {
			c.left.Store(int64(time.Until(dl)))
		}
		if c.hang {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	return c.Client.Create(ctx, o, opts...)
}

// Each page's Ensure has its own deadline: by default within
// scheduledWorkPageTimeout (10s), and a create that hangs is cut off there,
// recorded skipped, and the release goes on.
func TestScheduledWorkReleasePageCallIsBounded(t *testing.T) {
	t.Run("default deadline", func(t *testing.T) {
		var c *reminderCreateHangs
		startOperator(t, func(r *Reconciler) { c = &reminderCreateHangs{Client: r.Client}; r.Client = c })
		f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
		s := releaseFixtureSession(t, f, suspendedAtCreate)
		waitReleased(t, s.Name)
		f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
		if d := time.Duration(c.left.Load()); d <= 0 || d > 10*time.Second {
			t.Errorf("the page create had %v left, want a deadline within 10s", d)
		}
	})
	t.Run("a hanging create", func(t *testing.T) {
		old := scheduledWorkPageTimeout
		scheduledWorkPageTimeout = 300 * time.Millisecond
		t.Cleanup(func() { scheduledWorkPageTimeout = old })
		var c *reminderCreateHangs
		startOperator(t, func(r *Reconciler) { c = &reminderCreateHangs{Client: r.Client, hang: true}; r.Client = c })
		f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
		s := releaseFixtureSession(t, f, suspendedAtCreate)
		f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
		start := time.Now()
		waitReleased(t, s.Name)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("the release took %v after the record with a hanging page create; the page should be cut off at %v", took, scheduledWorkPageTimeout)
		}
		o := f.occurrence(t)
		if p := releasePage(o, v1alpha1.PageEventResult, 0); p == nil || p.State != "skipped" || p.Reminder != "" || c.calls.Load() == 0 {
			t.Errorf("pages %+v, creates %d", o.Pages, c.calls.Load())
		}
	})
}

// A terminal record stored without a page intent (by an operator before S4b,
// or before notify was set) gets none from a later ReportedAdded write: the
// agent's outcome is copied, and no page is sent for the earlier record.
func TestScheduledWorkReleaseNoPageWhenOnlyTheReportIsAdded(t *testing.T) {
	op := startOperator(t)
	f := newScheduledWork(t, v1alpha1.ScheduledWorkHeadless, v1alpha1.OccurrenceRunning)
	s := releaseFixtureSession(t, f, nil)
	f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences[0].State, st.Occurrences[0].Reason = v1alpha1.OccurrenceSucceeded, "recorded before S4b"
		st.Counts.Succeeded = 1
	})
	pod := markRunning(t, waitPod(t, s.Name), "talosw02")
	setTask(t, s.Name, v1alpha1.TaskStatus{Subtype: "success", FinishedAt: metav1.NewTime(time.Now().Add(-2 * scheduledwork.ReportWait))},
		&v1alpha1.TaskOutcomeStatus{PodUID: string(pod.UID), Record: v1alpha1.TaskOutcomeRecordStatus{Summary: "opened the audit PR"}})
	waitReleased(t, s.Name)
	o := f.occurrence(t)
	if o.Reported == nil || o.Reported.Summary != "opened the audit PR" {
		t.Fatalf("the reported outcome was not added: %+v", o)
	}
	if len(o.Pages) != 0 || len(f.pageReminders(t)) != 0 {
		t.Errorf("pages %+v reminders %d", o.Pages, len(f.pageReminders(t)))
	}
	if !slices.Contains(op.writes.orderLog(), "update status *v1alpha1.ScheduledWork "+f.key.Name) {
		t.Errorf("the reported outcome write did not happen: %v", op.writes.orderLog())
	}
}

// reminderReadsFail fails every Reminder read (only the one named name, when
// set) while fail is set; every other read goes through.
type reminderReadsFail struct {
	client.Reader
	fail *atomic.Bool
	name string
}

func (r reminderReadsFail) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.Reminder); ok && r.fail.Load() && (r.name == "" || key.Name == r.name) {
		return apierrors.NewServiceUnavailable("injected: the Reminder read failed")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// A page whose Reminder already exists but cannot be read back is left
// pending, not settled skipped; a later pass, while the finalizer still holds
// the derived name, records the Reminder. No second Reminder is created.
func TestScheduledWorkReleaseKeepsAnUnconfirmedPagePending(t *testing.T) {
	var ahead atomic.Int64
	var readFails atomic.Bool
	readFails.Store(true)
	op := startOperator(t, aheadClock(&ahead), func(r *Reconciler) { r.APIReader = reminderReadsFail{Reader: r.APIReader, fail: &readFails} })
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	w := f.item(t)
	earlier, err := pages.Build(w, f.at, v1alpha1.PageEventResult, 0, "Scheduled work ended: "+w.Spec.Title, f.key.Name+" "+f.name+": an earlier create", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(context.Background(), earlier); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second).Truncate(time.Second)
	s := releaseFixtureSession(t, f, func(s *v1alpha1.AgentSession) {
		suspendedAtCreate(s)
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] = deadline.UTC().Format(time.RFC3339)
	})
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	holdsFinalizer(t, s.Name, time.Second, op.passes.held(s.Name, func(p releasePass) bool { return p.requeue > scheduledwork.ReleaseMargin }))
	if p := releasePage(f.occurrence(t), v1alpha1.PageEventResult, 0); p == nil || p.Reminder != "" || p.State != "" {
		t.Fatalf("an unconfirmed page was settled: %+v", f.occurrence(t).Pages)
	}
	readFails.Store(false)
	poke(t, s.Name)
	eventually(t, "the page recorded once the read works", func() error {
		if p := releasePage(f.occurrence(t), v1alpha1.PageEventResult, 0); p == nil || p.Reminder != earlier.Name {
			return fmt.Errorf("pages %+v", f.occurrence(t).Pages)
		}
		return nil
	})
	ahead.Store(int64(time.Until(deadline) + scheduledwork.ReleaseMargin + 10*time.Second))
	poke(t, s.Name)
	waitReleased(t, s.Name)
	f.wantReleasePage(t, v1alpha1.PageEventResult, 0)
}

// Within one pass, an unconfirmed page is tried once even when the pass
// writes again: here a second pending page is recorded, its write meets a
// conflict and the pass reads the item again, and the unconfirmed page (its
// Reminder exists, the read back fails) is not created a second time.
func TestScheduledWorkReleaseTriesAnUnconfirmedPageOncePerPass(t *testing.T) {
	var readFails atomic.Bool
	readFails.Store(true)
	var racing *pageRecordRaced
	var mu sync.Mutex
	creates := map[string]int{}
	f := newScheduledWork(t, v1alpha1.ScheduledWorkInteractive, v1alpha1.OccurrenceDelivered)
	w := f.item(t)
	unread := pages.Key(w, f.at, v1alpha1.PageEventResult, 0)
	startOperator(t, func(r *Reconciler) {
		r.APIReader = reminderReadsFail{Reader: r.APIReader, fail: &readFails, name: unread}
		racing = &pageRecordRaced{Client: &reminderCreateCount{Client: r.Client, mu: &mu, creates: creates}}
		r.Client = racing
	})
	earlier, err := pages.Build(w, f.at, v1alpha1.PageEventResult, 0, "Scheduled work ended: "+w.Spec.Title, f.key.Name+" "+f.name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.Create(context.Background(), earlier); err != nil {
		t.Fatal(err)
	}
	// A second pending releaser page on the occurrence, which can be created.
	f.setStatus(t, func(st *v1alpha1.ScheduledWorkStatus) {
		st.Occurrences[0].Pages = []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventFailed}}
	})
	s := releaseFixtureSession(t, f, suspendedAtCreate)
	f.waitOccurrence(t, v1alpha1.OccurrenceEnded)
	waitReleased(t, s.Name)
	if !racing.raced.Load() {
		t.Fatal("the page record write never met the race")
	}
	o := f.occurrence(t)
	if p := releasePage(o, v1alpha1.PageEventFailed, 0); p == nil || p.Reminder == "" {
		t.Errorf("the second page was not recorded: %+v", o.Pages)
	}
	mu.Lock()
	defer mu.Unlock()
	if n := creates[earlier.Name]; n != 1 {
		t.Errorf("the unconfirmed page was created %d times in one pass, want 1", n)
	}
}

// reminderCreateCount counts the Reminder creates by name.
type reminderCreateCount struct {
	client.Client
	mu      *sync.Mutex
	creates map[string]int
}

func (c *reminderCreateCount) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	if r, ok := o.(*v1alpha1.Reminder); ok {
		c.mu.Lock()
		c.creates[r.Name]++
		c.mu.Unlock()
	}
	return c.Client.Create(ctx, o, opts...)
}
