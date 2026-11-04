package executor

// The page cases of DESIGN-054 S4b on the envtest API server: the executor
// pages through one keyed, one-shot Reminder per (item UID, occurrence,
// event, attempt), records it on the occurrence with AppendPage, and never
// lets a page hold a state change. The Reminder CRD is installed, so each
// Reminder the pager builds passes the real schema.

import (
	"context"
	"errors"
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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
	"github.com/thaynes43/dev-env/internal/scheduledwork/pages"
)

// paging is a fresh executor on the fixture with the operator's pager:
// Reminders are created through the executor's writing client and read
// uncached.
func (f *fixture) paging() *Executor {
	e := f.executor()
	e.Pager = NewPager(e.Client, e.Reader)
	return e
}

// pagingWith is paging whose Reminder creates go through create instead.
func (f *fixture) pagingWith(create func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error) *Executor {
	e := f.executor()
	e.Pager = NewPager(interceptor.NewClient(e.Client.(client.WithWatch), interceptor.Funcs{Create: create}), e.Reader)
	return e
}

// reminders lists the Reminders in the item namespace, from the API server.
func (f *fixture) reminders() []v1alpha1.Reminder {
	f.t.Helper()
	var list v1alpha1.ReminderList
	if err := cluster(f.t).List(context.Background(), &list, client.InNamespace(f.items)); err != nil {
		f.t.Fatal(err)
	}
	return list.Items
}

// pageOf is the occurrence's page entry for (event, attempt), or nil.
func pageOf(o v1alpha1.ScheduledWorkOccurrence, event string, attempt int32) *v1alpha1.ScheduledWorkPage {
	for i := range o.Pages {
		if o.Pages[i].Event == event && o.Pages[i].Attempt == attempt {
			return &o.Pages[i]
		}
	}
	return nil
}

// wantPage fails unless the occurrence at at records the page (event,
// attempt) under its deterministic Reminder name, and that Reminder exists:
// a one-shot owned by the scheduled-work identity, in the item's namespace,
// labelled with the item, occurrence, event and attempt, targeting the
// item's notify references. It returns the Reminder.
func (f *fixture) wantPage(key types.NamespacedName, at time.Time, event string, attempt int32) *v1alpha1.Reminder {
	f.t.Helper()
	w := f.get(key)
	o := f.occ(key, at)
	p := pageOf(o, event, attempt)
	name := pages.Key(w, at, event, attempt)
	if p == nil || p.Reminder != name || p.State == "skipped" {
		f.t.Fatalf("occurrence %s pages %+v: want %s attempt %d recorded as Reminder %s", at.Format(time.RFC3339), o.Pages, event, attempt, name)
	}
	var r v1alpha1.Reminder
	if err := cluster(f.t).Get(context.Background(), types.NamespacedName{Namespace: key.Namespace, Name: name}, &r); err != nil {
		f.t.Fatalf("the page Reminder %s: %v", name, err)
	}
	if r.Spec.Owner != pages.Owner || r.Spec.Recurrence != nil || !slices.Equal(r.Spec.Targets, w.Spec.Notify) ||
		r.Labels[v1alpha1.LabelScheduledWork] != string(w.UID) || r.Labels[pages.LabelEvent] != event ||
		r.Labels[v1alpha1.AnnotationScheduledWorkOccurrence] != scheduledwork.OccurrenceAnnotation(at) {
		f.t.Errorf("page Reminder %s: spec %+v labels %v", name, r.Spec, r.Labels)
	}
	if !strings.Contains(r.Spec.Title, w.Spec.Title) || !strings.Contains(r.Spec.Body, key.Name) {
		f.t.Errorf("page Reminder %s: title %q body %q", name, r.Spec.Title, r.Spec.Body)
	}
	return &r
}

// wantReminders fails unless the item namespace holds exactly n Reminders.
func (f *fixture) wantReminders(n int) {
	f.t.Helper()
	if got := f.reminders(); len(got) != n {
		names := make([]string, 0, len(got))
		for _, r := range got {
			names = append(names, r.Name+"("+r.Labels[pages.LabelEvent]+")")
		}
		f.t.Fatalf("%d page Reminders %v, want %d", len(got), names, n)
	}
}

// Dispatched pages the due event once, with the session's name. Later
// passes, a restart and a second executor create no second Reminder and
// record the page once.
func TestPagesTheDueEventOnce(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	f.wantReminders(0)
	f.at(f.paging(), day0.Add(10*time.Second))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	r := f.wantPage(key, day0, v1alpha1.PageEventDue, 0)
	if !strings.Contains(r.Spec.Body, o.Session) || !strings.HasPrefix(r.Spec.Title, "Scheduled work started") {
		t.Errorf("due page title %q body %q", r.Spec.Title, r.Spec.Body)
	}
	before, writes := f.get(key).ResourceVersion, f.writes.Load()
	f.at(f.paging(), day0.Add(time.Minute))
	f.at(f.paging(), day0.Add(2*time.Minute))
	f.wantReminders(1)
	if w := f.get(key); w.ResourceVersion != before || f.writes.Load() != writes || len(scheduledwork.Find(w, day0).Pages) != 1 {
		t.Errorf("later passes wrote the item again (%d writes): pages %+v", f.writes.Load()-writes, scheduledwork.Find(w, day0).Pages)
	}
}

// The executor sends only the pages it records. A session outcome's result
// or failed page is the releaser's, sent once the agent's reported outcome
// is in, so a stored intent of one is left alone; so is a failed intent on
// any state but Ambiguous.
func TestTheExecutorLeavesTheReleasersPages(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", daily(spec()))
	f.at(f.paging(), day0.Add(-time.Minute))
	w := f.get(key)
	claimed := metav1.NewTime(day0)
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{
		{At: metav1.NewTime(day0.Add(-48 * time.Hour)), State: v1alpha1.OccurrenceSucceeded, ClaimedAt: &claimed,
			Pages: []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventResult}}},
		{At: metav1.NewTime(day0.Add(-24 * time.Hour)), State: v1alpha1.OccurrenceFailed, ClaimedAt: &claimed,
			Pages: []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventFailed}}},
	}
	if err := cluster(t).Status().Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	writes := f.writes.Load()
	f.at(f.paging(), day0.Add(-30*time.Second))
	f.wantReminders(0)
	if f.writes.Load() != writes {
		t.Errorf("the executor wrote the item %d times", f.writes.Load()-writes)
	}
	for _, o := range f.get(key).Status.Occurrences {
		if len(o.Pages) != 1 || !pendingPage(o.Pages[0]) {
			t.Errorf("occurrence %s pages %+v: the releaser's intent was touched", o.At.Format(time.RFC3339), o.Pages)
		}
	}
}

// A park pages the parked event with its reason; when the decision window
// ends unanswered, the Skipped record pages the skipped event. Each is one
// Reminder.
func TestPagesAParkAndItsExpiry(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	ex := func() *Executor { e := f.paging(); e.Templates = NoTemplates{}; return e }
	f.at(ex(), day0.Add(-time.Minute))
	f.at(ex(), day0)
	f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "template ops-audit: "+NoTemplatesReason)
	r := f.wantPage(key, day0, v1alpha1.PageEventParked, 0)
	if !strings.Contains(r.Spec.Body, NoTemplatesReason) || !strings.HasPrefix(r.Spec.Title, "Scheduled work parked") {
		t.Errorf("parked page title %q body %q", r.Spec.Title, r.Spec.Body)
	}
	f.at(ex(), day0.Add(time.Hour))
	f.wantReminders(1)
	f.at(ex(), day0.Add(DefaultDecisionWindow))
	f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonParkedExpired)
	r = f.wantPage(key, day0, v1alpha1.PageEventSkipped, 0)
	if !strings.Contains(r.Spec.Body, ReasonParkedExpired) {
		t.Errorf("skipped page body %q", r.Spec.Body)
	}
	f.wantReminders(2)
}

// An owner that stays unproven parks the occurrence when its grace ends,
// before any claim; that park is paged too, and the Pending wait is not.
func TestPagesAnUnprovenOwnerPark(t *testing.T) {
	f := newFixture(t)
	sp := spec()
	sp.Owner, sp.Editor = "session/owner/0f0e0d0c-0b0a-0908-0706-050403020100", "session/owner/0f0e0d0c-0b0a-0908-0706-050403020100"
	key := f.item("owned", sp)
	f.auth.set(func(*v1alpha1.ScheduledWork) (Authority, error) { return Authority{Unproven: true}, nil })
	f.at(f.paging(), day0.Add(-time.Minute))
	f.at(f.paging(), day0)
	f.wantOcc(key, day0, v1alpha1.OccurrencePending, ReasonOwnerUnproven)
	f.wantReminders(0)
	f.at(f.paging(), day0.Add(grace+time.Second))
	f.wantOcc(key, day0, v1alpha1.OccurrenceParked, ReasonOwnerUnproven)
	f.wantPage(key, day0, v1alpha1.PageEventParked, 0)
	f.wantReminders(1)
}

// An occurrence made Ambiguous by a derived-name session it did not create
// pages the failed event.
func TestPagesAnAmbiguousOccurrenceAsFailed(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	f.foreignSession(f.derived(key, day0), "00000000-0000-0000-0000-000000000000", key.Name, day0)
	f.at(f.paging(), day0)
	f.wantOcc(key, day0, v1alpha1.OccurrenceAmbiguous, ReasonCollision)
	r := f.wantPage(key, day0, v1alpha1.PageEventFailed, 0)
	if !strings.Contains(r.Spec.Body, ReasonCollision) {
		t.Errorf("ambiguous page body %q", r.Spec.Body)
	}
	f.wantReminders(1)
}

// Every Skipped record pages the skipped event once: an outage's collapsed
// record with its count (one page for all of them), a later occurrence
// while the previous one runs, a claim whose create never landed before its
// grace ended, a notAfter passed at revalidation, and a due occurrence that
// an edit re-planned.
func TestPagesSkippedOccurrences(t *testing.T) {
	t.Run("missed beyond grace", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("daily", daily(spec()))
		day5 := day0.Add(5 * 24 * time.Hour)
		f.at(f.paging(), day5.Add(time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, "")
		r := f.wantPage(key, day0, v1alpha1.PageEventSkipped, 0)
		if !strings.Contains(r.Spec.Body, "5 occurrences") {
			t.Errorf("collapsed page body %q", r.Spec.Body)
		}
		f.wantPage(key, day5, v1alpha1.PageEventDue, 0)
		f.wantReminders(2)
	})
	t.Run("previous still running", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("daily", daily(spec()))
		f.at(f.paging(), day0.Add(-time.Minute))
		f.at(f.paging(), day0)
		day1 := day0.Add(24 * time.Hour)
		f.at(f.paging(), day1)
		f.wantOcc(key, day1, v1alpha1.OccurrenceSkipped, ReasonPreviousRunning)
		f.wantPage(key, day1, v1alpha1.PageEventSkipped, 0)
		f.wantReminders(2)
	})
	t.Run("not created before grace ended", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.paging(), day0.Add(-time.Minute))
		f.create.hooks(func() error { return errTransient }, nil)
		f.at(f.paging(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		f.wantReminders(0)
		f.at(f.paging(), day0.Add(grace+time.Second))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonNotCreated)
		f.wantPage(key, day0, v1alpha1.PageEventSkipped, 0)
		f.wantReminders(1)
	})
	t.Run("notAfter passed at revalidation", func(t *testing.T) {
		f := newFixture(t)
		sp := spec()
		sp.NotAfter = metav1.NewTime(day0.Add(5 * time.Minute))
		key := f.item("audit", sp)
		f.at(f.paging(), day0.Add(-time.Minute))
		var failed atomic.Bool
		f.create.hooks(func() error {
			if failed.CompareAndSwap(false, true) {
				return errTransient
			}
			return nil
		}, nil)
		f.at(f.paging(), day0)
		f.at(f.paging(), day0.Add(6*time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonNotAfter)
		f.wantPage(key, day0, v1alpha1.PageEventSkipped, 0)
		f.wantReminders(1)
	})
	t.Run("re-planned by an edit", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.paging(), day0.Add(-time.Minute))
		f.edit(key, func(s *v1alpha1.ScheduledWorkSpec) { s.Due = day0.Add(time.Hour).Format("2006-01-02T15:04") })
		f.at(f.paging(), day0.Add(time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonReplanned)
		f.wantPage(key, day0, v1alpha1.PageEventSkipped, 0)
		f.wantReminders(1)
	})
}

// A Cancelled occurrence is not paged: the schema has no cancelled page
// event, and a person or coordinator made the change.
func TestCancelledIsNotPaged(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	f.create.hooks(func() error { return errTransient }, nil)
	f.at(f.paging(), day0)
	f.edit(key, func(s *v1alpha1.ScheduledWorkSpec) { s.Cancelled = true })
	f.at(f.paging(), day0.Add(time.Minute))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceCancelled, ReasonCancelledAtRevalidation)
	if len(o.Pages) != 0 {
		t.Errorf("pages %+v", o.Pages)
	}
	f.wantReminders(0)
}

// An item with no notify target pages nothing and records no page, through
// a dispatch and a park; nil Pager pages nothing either.
func TestNoPageWithoutNotify(t *testing.T) {
	t.Run("empty notify", func(t *testing.T) {
		f := newFixture(t)
		sp := spec()
		sp.Notify = nil
		key := f.item("audit", sp)
		f.at(f.paging(), day0.Add(-time.Minute))
		f.at(f.paging(), day0)
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, ""); len(o.Pages) != 0 {
			t.Errorf("pages %+v", o.Pages)
		}
		parked := f.item("parked", sp)
		ex := f.paging()
		ex.Templates = NoTemplates{}
		f.at(ex, day0.Add(30*time.Second))
		if o := f.occ(parked, day0); o.State != v1alpha1.OccurrenceParked || len(o.Pages) != 0 {
			t.Errorf("parked occurrence %+v", o)
		}
		f.wantReminders(0)
	})
	t.Run("no pager", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.at(f.executor(), day0)
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, ""); len(o.Pages) != 0 {
			t.Errorf("pages %+v", o.Pages)
		}
		f.wantReminders(0)
	})
}

// A page that cannot be created is recorded skipped, with no Reminder, and
// holds nothing: the occurrence is Dispatched with its session all the same.
// That covers a Reminder kind the API server does not serve and any other
// create error.
func TestAFailedPageIsSkippedAndHoldsNothing(t *testing.T) {
	for name, fail := range map[string]error{
		"kind not served": &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: v1alpha1.GroupVersion.Group, Kind: "Reminder"}, SearchedVersions: []string{"v1alpha1"}},
		"create refused":  apierrors.NewForbidden(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "reminders"}, "", errors.New("injected: no create grant")),
		"create failed":   errTransient,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			key := f.item("audit", spec())
			var calls atomic.Int32
			ex := func() *Executor {
				return f.pagingWith(func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*v1alpha1.Reminder); ok {
						calls.Add(1)
						return fail
					}
					return c.Create(ctx, obj, opts...)
				})
			}
			f.at(ex(), day0.Add(-time.Minute))
			f.at(ex(), day0)
			o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
			p := pageOf(o, v1alpha1.PageEventDue, 0)
			if p == nil || p.State != "skipped" || p.Reminder != "" || len(f.sessionsOf()) != 1 {
				t.Fatalf("pages %+v sessions %d", o.Pages, len(f.sessionsOf()))
			}
			// A skipped page is final: later passes try no second create.
			f.at(ex(), day0.Add(time.Minute))
			if n := calls.Load(); n != 1 {
				t.Errorf("%d Reminder creates, want 1", n)
			}
			f.wantReminders(0)
		})
	}
}

// A page whose Reminder was created but whose record was lost (the status
// write failed after the create) is recorded on the replay, under the same
// Reminder: the restarted executor creates no second one.
func TestAReplayRecordsTheSamePage(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	var lost atomic.Bool
	e := f.paging()
	e.Client = interceptor.NewClient(e.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if w, ok := obj.(*v1alpha1.ScheduledWork); ok {
				if o := scheduledwork.Find(w, day0); o != nil && slices.ContainsFunc(o.Pages, func(p v1alpha1.ScheduledWorkPage) bool { return p.Reminder != "" }) && lost.CompareAndSwap(false, true) {
					return apierrors.NewServiceUnavailable("injected: the page record write failed")
				}
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	f.at(e, day0)
	if !lost.Load() {
		t.Fatal("no page record write was attempted")
	}
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	if p := pageOf(o, v1alpha1.PageEventDue, 0); p == nil || p.Reminder != "" || p.State != "" {
		t.Fatalf("after the lost write the page should be pending: %+v", o.Pages)
	}
	f.wantReminders(1)
	f.at(f.paging(), day0.Add(time.Minute))
	f.wantPage(key, day0, v1alpha1.PageEventDue, 0)
	f.wantReminders(1)
}

// Two leaders that overlap page once: the second leader's pass runs inside
// the first one's page record write, and both create the same keyed
// Reminder. One Reminder exists and the occurrence records it once.
func TestOverlappingLeadersPageOnce(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	var creates, interleaved atomic.Int32
	counting := func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.Reminder); ok {
			creates.Add(1)
		}
		return c.Create(ctx, obj, opts...)
	}
	first := f.pagingWith(counting)
	first.Client = interceptor.NewClient(first.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if w, ok := obj.(*v1alpha1.ScheduledWork); ok {
				if o := scheduledwork.Find(w, day0); o != nil && slices.ContainsFunc(o.Pages, func(p v1alpha1.ScheduledWorkPage) bool { return p.Reminder != "" }) && interleaved.Add(1) == 1 {
					f.pagingWith(counting).Tick(ctx)
				}
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	f.clock.Set(day0)
	first.Tick(context.Background())
	if interleaved.Load() == 0 {
		t.Fatal("the second leader never ran inside the first one's page write")
	}
	f.wantPage(key, day0, v1alpha1.PageEventDue, 0)
	f.wantReminders(1)
	if n := creates.Load(); n != 2 {
		t.Errorf("%d Reminder creates, want 2 (one each, the second AlreadyExists)", n)
	}
	if o := f.occ(key, day0); len(o.Pages) != 1 {
		t.Errorf("pages %+v", o.Pages)
	}
}

// The pages list stays within ScheduledWorkMaxPages: a park at the last
// attempt, on an occurrence whose list is full, drops the oldest attempt's
// first entry (its Reminder is not touched) and records the new page.
func TestThePagesCapKeepsTheNewestAttempt(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	full := []v1alpha1.ScheduledWorkPage{
		{Event: v1alpha1.PageEventDue, Attempt: 0, Reminder: "swp-seed-due-0", State: "sent"},
		{Event: v1alpha1.PageEventParked, Attempt: 0, Reminder: "swp-seed-parked-0", State: "sent"},
		{Event: v1alpha1.PageEventDue, Attempt: 1, Reminder: "swp-seed-due-1", State: "sent"},
		{Event: v1alpha1.PageEventParked, Attempt: 1, Reminder: "swp-seed-parked-1", State: "sent"},
		{Event: v1alpha1.PageEventDue, Attempt: 2, Reminder: "swp-seed-due-2", State: "sent"},
		{Event: v1alpha1.PageEventParked, Attempt: 2, Reminder: "swp-seed-parked-2", State: "sent"},
		{Event: v1alpha1.PageEventSkipped, Attempt: 2, State: "skipped"},
		{Event: v1alpha1.PageEventDue, Attempt: 3, Reminder: "swp-seed-due-3", State: "sent"},
	}
	retried := metav1.NewTime(day0.Add(time.Hour))
	w := f.get(key)
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(day0), State: v1alpha1.OccurrenceClaimed,
		ClaimedAt: &retried, RetriedAt: &retried, Attempt: 3, SpecHash: scheduledwork.SpecHash(w.Spec), Pages: full}}
	if err := cluster(t).Status().Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	ex := f.paging()
	ex.Templates = NoTemplates{}
	f.at(ex, retried.Add(time.Minute))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "")
	if len(o.Pages) != v1alpha1.ScheduledWorkMaxPages {
		t.Fatalf("%d pages, want %d: %+v", len(o.Pages), v1alpha1.ScheduledWorkMaxPages, o.Pages)
	}
	if pageOf(o, v1alpha1.PageEventDue, 0) != nil || pageOf(o, v1alpha1.PageEventParked, 0) == nil {
		t.Errorf("the oldest attempt's first entry should go and the rest stay: %+v", o.Pages)
	}
	f.wantPage(key, day0, v1alpha1.PageEventParked, 3)
	f.wantReminders(1)
}

// A transition replayed onto an occurrence that already records its page
// (an approve or retry that reopens the same attempt, or any future path
// that repeats one) records no second intent and sends no second page.
func TestAReplayedTransitionSendsNoSecondPage(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	sent := []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventDue, Attempt: 0, Reminder: "swp-earlier-due-0", State: "sent"}}
	claimed := metav1.NewTime(day0)
	w := f.get(key)
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{{At: metav1.NewTime(day0), State: v1alpha1.OccurrenceClaimed,
		ClaimedAt: &claimed, SpecHash: scheduledwork.SpecHash(w.Spec), Pages: sent}}
	if err := cluster(t).Status().Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	f.at(f.paging(), day0.Add(time.Minute))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	if !slices.Equal(o.Pages, sent) {
		t.Errorf("pages %+v, want the recorded page unchanged", o.Pages)
	}
	f.wantReminders(0)
}

// raceCancel makes e's status write that first carries an occurrence at at
// in state, as the transition writes it, lose to a cancel of the item stored
// just before it: the write then meets a conflict and is never stored.
func (f *fixture) raceCancel(e *Executor, key types.NamespacedName, at time.Time, state string) *atomic.Bool {
	var raced atomic.Bool
	e.Client = interceptor.NewClient(e.Client.(client.WithWatch), interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if w, ok := obj.(*v1alpha1.ScheduledWork); ok && !raced.Load() {
				if o := scheduledwork.Find(w, at); o != nil && o.State == state {
					raced.Store(true)
					f.edit(key, func(s *v1alpha1.ScheduledWorkSpec) { s.Cancelled = true })
				}
			}
			return c.SubResource(sub).Update(ctx, obj, opts...)
		},
	})
	return &raced
}

// No page goes out for a transition that was never stored: its status write
// loses to a cancel, so the occurrence is Cancelled instead, and no Reminder
// is created for the park or skip that never happened.
func TestANeverStoredTransitionPagesNothing(t *testing.T) {
	counting := func(n *atomic.Int32) func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		return func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.Reminder); ok {
				n.Add(1)
			}
			return c.Create(ctx, obj, opts...)
		}
	}
	t.Run("a park", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.paging(), day0.Add(-time.Minute))
		var creates atomic.Int32
		e := f.pagingWith(counting(&creates))
		e.Templates = NoTemplates{}
		raced := f.raceCancel(e, key, day0, v1alpha1.OccurrenceParked)
		f.at(e, day0)
		if !raced.Load() {
			t.Fatal("the park's write never met the cancel")
		}
		o := f.wantOcc(key, day0, v1alpha1.OccurrenceCancelled, ReasonCancelledAtRevalidation)
		if len(o.Pages) != 0 || creates.Load() != 0 {
			t.Errorf("pages %+v, %d Reminder creates", o.Pages, creates.Load())
		}
		f.wantReminders(0)
	})
	t.Run("a skip", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("daily", daily(spec()))
		f.at(f.paging(), day0.Add(-time.Minute))
		f.at(f.paging(), day0)
		f.wantReminders(1)
		day1 := day0.Add(24 * time.Hour)
		var creates atomic.Int32
		e := f.pagingWith(counting(&creates))
		raced := f.raceCancel(e, key, day1, v1alpha1.OccurrenceSkipped)
		f.at(e, day1)
		if !raced.Load() {
			t.Fatal("the skip's write never met the cancel")
		}
		o := f.wantOcc(key, day1, v1alpha1.OccurrenceCancelled, ReasonCancelledBeforeClaim)
		if len(o.Pages) != 0 || creates.Load() != 0 {
			t.Errorf("pages %+v, %d Reminder creates", o.Pages, creates.Load())
		}
		f.wantReminders(1)
	})
}

// Each page's Ensure has its own deadline: by default within
// DefaultPageTimeout, and a create that hangs is cut off at
// Executor.PageTimeout, recorded skipped, and holds the pass no longer.
func TestAPageCallIsBounded(t *testing.T) {
	t.Run("default deadline", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.paging(), day0.Add(-time.Minute))
		var left atomic.Int64
		e := f.pagingWith(func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.Reminder); ok {
				if dl, ok := ctx.Deadline(); ok {
					left.Store(int64(time.Until(dl)))
				}
			}
			return c.Create(ctx, obj, opts...)
		})
		f.at(e, day0)
		f.wantPage(key, day0, v1alpha1.PageEventDue, 0)
		if d := time.Duration(left.Load()); d <= 0 || d > 10*time.Second {
			t.Errorf("the page create had %v left, want a deadline within 10s", d)
		}
	})
	t.Run("a hanging create", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.paging(), day0.Add(-time.Minute))
		e := f.pagingWith(func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.Reminder); ok {
				<-ctx.Done()
				return ctx.Err()
			}
			return c.Create(ctx, obj, opts...)
		})
		e.PageTimeout = 300 * time.Millisecond
		start := time.Now()
		f.at(e, day0)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("the pass took %v with a hanging page create; the page should be cut off at %v", took, e.PageTimeout)
		}
		o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
		if p := pageOf(o, v1alpha1.PageEventDue, 0); p == nil || p.State != "skipped" || p.Reminder != "" {
			t.Errorf("pages %+v", o.Pages)
		}
		f.wantReminders(0)
	})
}

// reminderReadFails fails every Reminder read while fail is set; every other
// read goes through.
type reminderReadFails struct {
	client.Reader
	fail *atomic.Bool
}

func (r reminderReadFails) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.Reminder); ok && r.fail.Load() {
		return apierrors.NewServiceUnavailable("injected: the Reminder read failed")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

// A page whose Reminder already exists (a create whose record was lost) but
// cannot be read back is left pending, not settled skipped: it is tried once
// in the pass, and a later pass whose read works records the Reminder. No
// second Reminder is created.
func TestAnUnconfirmedPageStaysPending(t *testing.T) {
	f := newFixture(t)
	key := f.item("audit", spec())
	f.at(f.paging(), day0.Add(-time.Minute))
	w := f.get(key)
	earlier, err := pages.Build(w, day0, v1alpha1.PageEventDue, 0, "Scheduled work started: "+w.Spec.Title, key.Name+": an earlier create", day0)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster(t).Create(context.Background(), earlier); err != nil {
		t.Fatal(err)
	}
	var readFails atomic.Bool
	readFails.Store(true)
	var creates atomic.Int32
	ex := func() *Executor {
		e := f.executor()
		writer := interceptor.NewClient(e.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if _, ok := obj.(*v1alpha1.Reminder); ok {
				creates.Add(1)
			}
			return c.Create(ctx, obj, opts...)
		}})
		e.Pager = NewPager(writer, reminderReadFails{Reader: e.Reader, fail: &readFails})
		return e
	}
	f.at(ex(), day0)
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	if p := pageOf(o, v1alpha1.PageEventDue, 0); p == nil || !pendingPage(*p) {
		t.Fatalf("an unconfirmed page was settled: %+v", o.Pages)
	}
	if n := creates.Load(); n != 1 {
		t.Errorf("%d Reminder creates in one pass, want 1", n)
	}
	readFails.Store(false)
	f.at(ex(), day0.Add(time.Minute))
	f.wantPage(key, day0, v1alpha1.PageEventDue, 0)
	f.wantReminders(1)
}

// An unconfirmed page is tried once per pass, however many steps the pass
// takes: here the other occurrence's page is recorded after it, a write that
// starts another step, and the unconfirmed one is not tried again.
func TestAnUnconfirmedPageIsTriedOncePerPass(t *testing.T) {
	f := newFixture(t)
	key := f.item("daily", daily(spec()))
	w := f.get(key)
	a, b := day0.Add(-48*time.Hour), day0.Add(-24*time.Hour)
	w.Status.Occurrences = []v1alpha1.ScheduledWorkOccurrence{
		{At: metav1.NewTime(a), State: v1alpha1.OccurrenceSkipped, Reason: ReasonPreviousRunning, Pages: []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventSkipped}}},
		{At: metav1.NewTime(b), State: v1alpha1.OccurrenceSkipped, Reason: ReasonPreviousRunning, Pages: []v1alpha1.ScheduledWorkPage{{Event: v1alpha1.PageEventSkipped}}},
	}
	if err := cluster(t).Status().Update(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	w = f.get(key)
	earlier, err := pages.Build(w, a, v1alpha1.PageEventSkipped, 0, "Scheduled work skipped: "+w.Spec.Title, key.Name, a)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster(t).Create(context.Background(), earlier); err != nil {
		t.Fatal(err)
	}
	var readFails atomic.Bool
	readFails.Store(true)
	var mu sync.Mutex
	creates := map[string]int{}
	e := f.executor()
	writer := interceptor.NewClient(e.Client.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if r, ok := obj.(*v1alpha1.Reminder); ok {
			mu.Lock()
			creates[r.Name]++
			mu.Unlock()
		}
		return c.Create(ctx, obj, opts...)
	}})
	e.Pager = NewPager(writer, reminderReadFails{Reader: e.Reader, fail: &readFails})
	f.at(e, day0.Add(-time.Hour))
	if p := pageOf(f.occ(key, a), v1alpha1.PageEventSkipped, 0); p == nil || !pendingPage(*p) {
		t.Errorf("the unconfirmed page was settled: %+v", f.occ(key, a).Pages)
	}
	f.wantPage(key, b, v1alpha1.PageEventSkipped, 0)
	mu.Lock()
	defer mu.Unlock()
	if n := creates[earlier.Name]; n != 1 {
		t.Errorf("the unconfirmed page was tried %d times in one pass, want 1", n)
	}
}
