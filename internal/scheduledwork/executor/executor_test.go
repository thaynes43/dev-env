package executor

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/reminders"
	"github.com/thaynes43/dev-env/internal/scheduledwork"
)

const grace = DefaultHeadlessGrace

var errTransient = errors.New("injected: the Kubernetes API server could not be reached")

// A due occurrence is claimed, its session created once through the API's
// fixed-name entry with the finalizer, label and annotations of the atomic
// Create, and recorded Dispatched. Later passes, and a restarted executor,
// create nothing more.
func TestDispatchesADueOccurrenceOnce(t *testing.T) {
	f := newFixture(t)
	e := f.executor()
	key := f.item("audit", spec())

	f.at(e, day0.Add(-time.Minute))
	w := f.get(key)
	if w.Status.NextDue == nil || !w.Status.NextDue.Equal(&metav1.Time{Time: day0}) || len(w.Status.Occurrences) != 0 {
		t.Fatalf("before the due time: nextDue %v, occurrences %v", w.Status.NextDue, w.Status.Occurrences)
	}
	if len(f.sessionsOf()) != 0 {
		t.Fatal("a session was created before the due time")
	}

	f.at(e, day0.Add(10*time.Second))
	name := f.derived(key, day0)
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	w = f.get(key)
	if o.Session != name || o.ClaimedAt == nil || o.SpecHash != scheduledwork.SpecHash(w.Spec) || o.Attempt != 0 {
		t.Errorf("dispatched occurrence: %+v", o)
	}
	if w.Status.NextDue != nil {
		t.Errorf("a one-shot has no next occurrence, got %v", w.Status.NextDue)
	}
	s := f.session(name)
	if !controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) ||
		s.Labels[v1alpha1.LabelScheduledWork] != string(w.UID) ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkName] != key.Name ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkOccurrence] != "1906725600" ||
		s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] != "2030-06-03T14:15:00Z" {
		t.Errorf("session metadata: finalizers %v labels %v annotations %v", s.Finalizers, s.Labels, s.Annotations)
	}
	if s.Spec.Parent != apiserver.ScheduledWorkParent || s.Labels[v1alpha1.LabelDepth] != "0" ||
		s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Prompt != spec().Work.Prompt || s.Spec.Profile != "dev" ||
		s.Spec.Model != "claude-opus-5-5" || s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil || s.Spec.Limits.Timeout.Duration != 40*time.Minute {
		t.Errorf("session spec: %+v labels %v", s.Spec, s.Labels)
	}

	f.at(e, day0.Add(time.Minute))
	f.at(f.executor(), day0.Add(2*time.Minute))
	if calls, created, _ := f.create.counts(); calls != 1 || created != 1 || len(f.sessionsOf()) != 1 {
		t.Errorf("creation entry called %d times, %d created, %d sessions; want one of each", calls, created, len(f.sessionsOf()))
	}
}

// Two leaders that overlap (a deposed leader still running its pass): the
// one whose create comes second adopts the first one's session, so exactly
// one session is created and the occurrence is recorded Dispatched once.
func TestOverlappingLeadersCreateOneSession(t *testing.T) {
	t.Run("both reach the create", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		e1, e2 := f.executor(), f.executor()
		f.at(e1, day0.Add(-time.Minute))
		f.clock.Set(day0)
		// e1 claims and, at its create, e2 runs a whole pass first: it finds
		// the claim without a session and creates. e1's create then adopts.
		var interleaved atomic.Bool
		f.create.hooks(func() error {
			if interleaved.CompareAndSwap(false, true) {
				e2.Tick(context.Background())
			}
			return nil
		}, nil)
		e1.Tick(context.Background())
		calls, created, adopted := f.create.counts()
		if calls != 2 || created != 1 || adopted != 1 || len(f.sessionsOf()) != 1 {
			t.Fatalf("calls %d created %d adopted %d sessions %d; want 2 calls, one created and one adopted", calls, created, adopted, len(f.sessionsOf()))
		}
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, ""); o.Session != f.derived(key, day0) {
			t.Errorf("occurrence session %q", o.Session)
		}
	})
	t.Run("the second sees the claim", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		e1 := f.executor()
		f.at(e1, day0.Add(-time.Minute))
		f.clock.Set(day0)
		var stale v1alpha1.ScheduledWorkList
		if err := cluster(t).List(context.Background(), &stale, client.InNamespace(f.items)); err != nil {
			t.Fatal(err)
		}
		e1.Tick(context.Background())
		// e2 works from the listing it took before e1's claim: its claim's
		// compare-and-set fails, it reads the item again and finds it
		// Dispatched.
		claimed := f.occ(key, day0).ClaimedAt
		e2 := f.executor()
		reads := &atomic.Int32{}
		e2.Reader = staleList{Reader: cluster(t), list: &stale, reads: reads}
		e2.Tick(context.Background())
		if calls, created, _ := f.create.counts(); calls != 1 || created != 1 {
			t.Fatalf("calls %d created %d; the second leader must not create", calls, created)
		}
		if reads.Load() != 1 {
			t.Errorf("the second leader read the item %d times after its claim; its compare-and-set must fail and send it back to the API server once", reads.Load())
		}
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, ""); !o.ClaimedAt.Equal(claimed) {
			t.Errorf("the claim was overwritten: claimedAt %v, want %v", o.ClaimedAt, claimed)
		}
	})
}

// staleList answers List with a listing taken earlier, as a leader that read
// before another one wrote; Get goes to the API server.
type staleList struct {
	client.Reader
	list  *v1alpha1.ScheduledWorkList
	reads *atomic.Int32
}

func (s staleList) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*v1alpha1.ScheduledWork); ok {
		s.reads.Add(1)
	}
	return s.Reader.Get(ctx, key, obj, opts...)
}

func (s staleList) List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error {
	if l, ok := obj.(*v1alpha1.ScheduledWorkList); ok {
		s.list.DeepCopyInto(l)
		return nil
	}
	return s.Reader.List(ctx, obj, opts...)
}

// A create that succeeded but whose answer, or whose status write, was lost
// leaves the occurrence Claimed with no session recorded; the next pass, of a
// restarted executor too, finds the derived-name session and adopts it.
func TestLostCreateResponseIsAdopted(t *testing.T) {
	t.Run("the answer is lost", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		var lost atomic.Bool
		f.create.hooks(nil, func(name string, adopted bool, err error) (string, bool, error) {
			if err == nil && lost.CompareAndSwap(false, true) {
				return "", false, errTransient
			}
			return name, adopted, err
		})
		f.at(f.executor(), day0)
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, ""); o.Session != "" || len(f.sessionsOf()) != 1 {
			t.Fatalf("after the lost answer: %+v, %d sessions", o, len(f.sessionsOf()))
		}
		f.at(f.executor(), day0.Add(30*time.Second))
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, ""); o.Session != f.derived(key, day0) {
			t.Errorf("adopted occurrence %+v", o)
		}
		if calls, created, _ := f.create.counts(); calls != 1 || created != 1 || len(f.sessionsOf()) != 1 {
			t.Errorf("calls %d created %d sessions %d; the adoption must not create", calls, created, len(f.sessionsOf()))
		}
	})
	t.Run("the status write fails", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.create.hooks(nil, func(name string, adopted bool, err error) (string, bool, error) {
			f.failStatus.Store(1)
			return name, adopted, err
		})
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		f.create.hooks(nil, nil)
		f.at(f.executor(), day0.Add(30*time.Second))
		f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
		if calls, created, _ := f.create.counts(); calls != 1 || created != 1 || len(f.sessionsOf()) != 1 {
			t.Errorf("calls %d created %d sessions %d", calls, created, len(f.sessionsOf()))
		}
	})
}

// A session of the derived name that this occurrence did not create (a prefix
// collision) is never adopted: the occurrence is Ambiguous, whether the
// executor's read finds it or the creation entry does.
func TestCollisionIsAmbiguous(t *testing.T) {
	t.Run("found before the create", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.foreignSession(f.derived(key, day0), "00000000-0000-0000-0000-000000000000", key.Name, day0)
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceAmbiguous, ReasonCollision)
		if calls, _, _ := f.create.counts(); calls != 0 || f.get(key).Status.Counts.Ambiguous != 1 {
			t.Errorf("calls %d counts %+v", calls, f.get(key).Status.Counts)
		}
		if s := f.session(f.derived(key, day0)); controllerutil.ContainsFinalizer(s, v1alpha1.ScheduledWorkFinalizer) {
			t.Error("the foreign session was changed")
		}
	})
	t.Run("found by the creation entry", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		uid := string(f.get(key).UID)
		f.create.hooks(func() error {
			// Another occurrence's session, by its annotation, under this name.
			f.foreignSession(f.derived(key, day0), uid, key.Name, day0.Add(time.Hour))
			return nil
		}, nil)
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceAmbiguous, ReasonCollision)
		if calls, created, _ := f.create.counts(); calls != 1 || created != 0 {
			t.Errorf("calls %d created %d", calls, created)
		}
	})
}

// foreignSession creates a session under name that carries the given item
// label and annotations but no finalizer, as something else made it.
func (f *fixture) foreignSession(name, uid, item string, at time.Time) {
	f.t.Helper()
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.sessions, Name: name,
			Labels:      map[string]string{v1alpha1.LabelScheduledWork: uid},
			Annotations: map[string]string{v1alpha1.AnnotationScheduledWorkName: item, v1alpha1.AnnotationScheduledWorkOccurrence: scheduledwork.OccurrenceAnnotation(at)}},
		Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5", Prompt: "p", Parent: "dev-env-system/dev-env-human"},
	}
	if err := cluster(f.t).Create(context.Background(), s); err != nil {
		f.t.Fatal(err)
	}
}

// A Claimed occurrence with no session is created again while it is inside
// its grace (counted from the due time), and Skipped once grace has ended;
// nothing is unknown, so it is not Ambiguous.
func TestClaimedWithoutSessionRetriesInsideGrace(t *testing.T) {
	t.Run("inside grace", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		var failed atomic.Bool
		f.create.hooks(func() error {
			if failed.CompareAndSwap(false, true) {
				return errTransient
			}
			return nil
		}, nil)
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		f.at(f.executor(), day0.Add(grace-time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
		if calls, created, _ := f.create.counts(); calls != 2 || created != 1 {
			t.Errorf("calls %d created %d", calls, created)
		}
	})
	t.Run("after grace", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.create.hooks(func() error { return errTransient }, nil)
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		f.at(f.executor(), day0.Add(grace+time.Second))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonNotCreated)
		if calls, _, _ := f.create.counts(); calls != 1 || len(f.sessionsOf()) != 0 || f.get(key).Status.Counts.Skipped != 1 {
			t.Errorf("calls %d sessions %d counts %+v", calls, len(f.sessionsOf()), f.get(key).Status.Counts)
		}
	})
}

// A session that completed and was reaped inside the occurrence's grace is
// never created again: the releaser records the outcome before it lifts the
// finalizer, so the executor finds a terminal occurrence, even when its own
// Dispatched record was lost and the occurrence was still Claimed.
func TestReapedSessionIsNeverRecreated(t *testing.T) {
	t.Run("after Dispatched", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.at(f.executor(), day0)
		name := f.derived(key, day0)
		f.finish(name, v1alpha1.TaskResultSucceeded)
		f.release(name, true)
		f.at(f.executor(), day0.Add(time.Minute))
		f.at(f.executor(), day0.Add(2*time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSucceeded, "")
		if calls, _, _ := f.create.counts(); calls != 1 || len(f.sessionsOf()) != 0 {
			t.Errorf("calls %d sessions %d", calls, len(f.sessionsOf()))
		}
	})
	t.Run("while still Claimed", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		f.at(f.executor(), day0.Add(-time.Minute))
		f.create.hooks(nil, func(name string, adopted bool, err error) (string, bool, error) {
			f.failStatus.Store(1)
			return name, adopted, err
		})
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		name := f.derived(key, day0)
		f.finish(name, v1alpha1.TaskResultSucceeded)
		f.release(name, true)
		f.create.hooks(nil, nil)
		// Inside grace, with no session of the derived name: only the
		// recorded outcome stops a second create.
		f.at(f.executor(), day0.Add(2*time.Minute))
		if o := f.wantOcc(key, day0, v1alpha1.OccurrenceSucceeded, ""); o.Session != name {
			t.Errorf("recorded occurrence %+v", o)
		}
		if calls, _, _ := f.create.counts(); calls != 1 || len(f.sessionsOf()) != 0 {
			t.Errorf("calls %d sessions %d: a reaped session was created again", calls, len(f.sessionsOf()))
		}
	})
}

// The global cap is checked before the claim: an occurrence at the cap stays
// Pending (never Claimed, never Parked), is claimed when a slot frees inside
// its grace, and is Skipped "at capacity" when its grace ends first.
func TestCapIsCheckedBeforeTheClaim(t *testing.T) {
	setup := func(t *testing.T) (*fixture, types.NamespacedName, types.NamespacedName, func() *Executor) {
		f := newFixture(t)
		a, b := f.item("cap-a", spec()), f.item("cap-b", spec())
		ex := func() *Executor { e := f.executor(); e.MaxHeadless = 1; return e }
		f.at(ex(), day0.Add(-time.Minute))
		f.at(ex(), day0)
		f.wantOcc(a, day0, v1alpha1.OccurrenceDispatched, "")
		if o := f.wantOcc(b, day0, v1alpha1.OccurrencePending, ReasonAtCapacity); o.ClaimedAt != nil || o.SpecHash != "" {
			t.Fatalf("the occurrence at the cap was claimed: %+v", o)
		}
		if c := f.get(b).Status.Counts; c.Parked != 0 {
			t.Fatalf("counts %+v", c)
		}
		return f, a, b, ex
	}
	t.Run("a slot frees inside grace", func(t *testing.T) {
		f, a, b, ex := setup(t)
		f.finish(f.derived(a, day0), v1alpha1.TaskResultSucceeded)
		f.release(f.derived(a, day0), false)
		f.at(ex(), day0.Add(5*time.Minute))
		if o := f.wantOcc(b, day0, v1alpha1.OccurrenceDispatched, ""); o.Session != f.derived(b, day0) {
			t.Errorf("occurrence %+v", o)
		}
	})
	t.Run("grace ends at the cap", func(t *testing.T) {
		f, _, b, ex := setup(t)
		f.at(ex(), day0.Add(5*time.Minute))
		f.wantOcc(b, day0, v1alpha1.OccurrencePending, ReasonAtCapacity)
		f.at(ex(), day0.Add(grace+time.Second))
		f.wantOcc(b, day0, v1alpha1.OccurrenceSkipped, ReasonAtCapacity)
		if calls, _, _ := f.create.counts(); calls != 1 || f.get(b).Status.Counts.Skipped != 1 {
			t.Errorf("calls %d counts %+v", calls, f.get(b).Status.Counts)
		}
	})
}

// One unfinished occurrence per item: the next occurrence of a daily item
// that comes due while the previous one runs is Skipped, and the item moves
// on to the one after.
func TestPreviousStillRunningIsSkipped(t *testing.T) {
	f := newFixture(t)
	key := f.item("daily", daily(spec()))
	f.at(f.executor(), day0.Add(-time.Minute))
	f.at(f.executor(), day0)
	f.wantOcc(key, day0, v1alpha1.OccurrenceDispatched, "")
	day1 := day0.Add(24 * time.Hour)
	f.at(f.executor(), day1)
	f.wantOcc(key, day1, v1alpha1.OccurrenceSkipped, ReasonPreviousRunning)
	w := f.get(key)
	if w.Status.NextDue == nil || !w.Status.NextDue.Time.Equal(day1.Add(24*time.Hour)) || w.Status.Counts.Skipped != 1 {
		t.Errorf("nextDue %v counts %+v", w.Status.NextDue, w.Status.Counts)
	}
	if calls, _, _ := f.create.counts(); calls != 1 {
		t.Errorf("calls %d", calls)
	}
}

// After an outage, every occurrence of an item missed beyond grace collapses
// into one Skipped record whose missed field holds the count, and the one
// still inside its grace is dispatched.
func TestOutageCollapsesMissedOccurrences(t *testing.T) {
	f := newFixture(t)
	key := f.item("daily", daily(spec()))
	day5 := day0.Add(5 * 24 * time.Hour)
	f.at(f.executor(), day5.Add(time.Minute))
	o := f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, "")
	if o.Missed != 5 || !strings.Contains(o.Reason, "5 occurrences, the last at 2030-06-07T14:00:00Z") {
		t.Errorf("collapsed record %+v", o)
	}
	f.wantOcc(key, day5, v1alpha1.OccurrenceDispatched, "")
	w := f.get(key)
	if len(w.Status.Occurrences) != 2 || w.Status.Counts.Skipped != 5 {
		t.Errorf("occurrences %d counts %+v", len(w.Status.Occurrences), w.Status.Counts)
	}
}

// Revalidation check 1 ends the occurrence: a cancelled item makes it
// Cancelled and a passed notAfter Skipped. Neither parks, because no approval
// can change them.
func TestRevalidationCancelAndNotAfterEndTheOccurrence(t *testing.T) {
	claim := func(t *testing.T, sp v1alpha1.ScheduledWorkSpec) (*fixture, types.NamespacedName) {
		f := newFixture(t)
		key := f.item("audit", sp)
		f.at(f.executor(), day0.Add(-time.Minute))
		var failed atomic.Bool
		f.create.hooks(func() error {
			if failed.CompareAndSwap(false, true) {
				return errTransient
			}
			return nil
		}, nil)
		f.at(f.executor(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceClaimed, "")
		return f, key
	}
	t.Run("cancelled", func(t *testing.T) {
		f, key := claim(t, spec())
		f.edit(key, func(s *v1alpha1.ScheduledWorkSpec) { s.Cancelled = true })
		f.at(f.executor(), day0.Add(time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceCancelled, ReasonCancelledAtRevalidation)
		if c := f.get(key).Status.Counts; c.Cancelled != 1 || c.Parked != 0 || len(f.sessionsOf()) != 0 {
			t.Errorf("counts %+v sessions %d", c, len(f.sessionsOf()))
		}
	})
	t.Run("notAfter passed", func(t *testing.T) {
		sp := spec()
		sp.NotAfter = metav1.NewTime(day0.Add(5 * time.Minute))
		f, key := claim(t, sp)
		f.at(f.executor(), day0.Add(6*time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonNotAfter)
		if c := f.get(key).Status.Counts; c.Skipped != 1 || c.Parked != 0 || len(f.sessionsOf()) != 0 {
			t.Errorf("counts %+v sessions %d", c, len(f.sessionsOf()))
		}
	})
	t.Run("cancelled before the due time", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", daily(spec()))
		f.at(f.executor(), day0.Add(-time.Minute))
		f.edit(key, func(s *v1alpha1.ScheduledWorkSpec) { s.Cancelled = true })
		f.at(f.executor(), day0.Add(-30*time.Second))
		f.at(f.executor(), day0)
		if w := f.get(key); w.Status.NextDue != nil || len(w.Status.Occurrences) != 0 {
			t.Errorf("a cancelled item still plans: nextDue %v occurrences %v", w.Status.NextDue, w.Status.Occurrences)
		}
	})
}

// The production wiring until S3b2, DenyAll and NoTemplates, parks every
// occurrence with its reason and dispatches nothing; a Parked occurrence that
// nobody approves expires to Skipped at the end of its decision window.
func TestProductionDefaultsParkAndDispatchNothing(t *testing.T) {
	t.Run("deny all", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		ex := func() *Executor { e := f.executor(); e.Authorizer, e.Templates = DenyAll{}, NoTemplates{}; return e }
		f.at(ex(), day0.Add(-time.Minute))
		f.at(ex(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceParked, DenyAllReason)
		if calls, _, _ := f.create.counts(); calls != 0 || len(f.sessionsOf()) != 0 || f.get(key).Status.Counts.Parked != 1 {
			t.Fatalf("calls %d sessions %d counts %+v", calls, len(f.sessionsOf()), f.get(key).Status.Counts)
		}
		f.at(ex(), day0.Add(DefaultDecisionWindow-time.Minute))
		f.wantOcc(key, day0, v1alpha1.OccurrenceParked, DenyAllReason)
		f.at(ex(), day0.Add(DefaultDecisionWindow))
		f.wantOcc(key, day0, v1alpha1.OccurrenceSkipped, ReasonParkedExpired)
	})
	t.Run("no templates", func(t *testing.T) {
		f := newFixture(t)
		key := f.item("audit", spec())
		ex := func() *Executor { e := f.executor(); e.Templates = NoTemplates{}; return e }
		f.at(ex(), day0.Add(-time.Minute))
		f.at(ex(), day0)
		f.wantOcc(key, day0, v1alpha1.OccurrenceParked, "template ops-audit: "+NoTemplatesReason)
		if calls, _, _ := f.create.counts(); calls != 0 {
			t.Errorf("calls %d", calls)
		}
	})
}

// A recurring item moves to its next occurrence once one is decided, and
// each occurrence gets its own derived session.
func TestRecurringItemMovesToItsNextOccurrence(t *testing.T) {
	f := newFixture(t)
	key := f.item("daily", daily(spec()))
	f.at(f.executor(), day0.Add(-time.Minute))
	f.at(f.executor(), day0)
	first := f.derived(key, day0)
	f.finish(first, v1alpha1.TaskResultSucceeded)
	f.release(first, true)
	day1 := day0.Add(24 * time.Hour)
	if w := f.get(key); w.Status.NextDue == nil || !w.Status.NextDue.Time.Equal(day1) {
		t.Fatalf("nextDue %v, want %s", w.Status.NextDue, day1)
	}
	f.at(f.executor(), day1)
	o := f.wantOcc(key, day1, v1alpha1.OccurrenceDispatched, "")
	if o.Session != f.derived(key, day1) || o.Session == first || !strings.HasSuffix(o.Session, "-203006041400") {
		t.Errorf("second session %q", o.Session)
	}
	f.wantOcc(key, day0, v1alpha1.OccurrenceSucceeded, "")
	if w := f.get(key); w.Status.NextDue == nil || !w.Status.NextDue.Time.Equal(day1.Add(24*time.Hour)) || w.Status.Counts.Succeeded != 1 {
		t.Errorf("nextDue %v counts %+v", w.Status.NextDue, w.Status.Counts)
	}
}

// An occurrence in a spring-forward gap is planned with the shared schedule's
// rule (internal/reminders.Resolve): 02:30 on the gap day in New York is
// 03:30 EDT, and the next day is 02:30 EDT again.
func TestDSTGapUsesTheSharedSchedule(t *testing.T) {
	f := newFixture(t)
	sp := daily(spec())
	sp.Due, sp.TimeZone = "2030-03-10T02:30", "America/New_York"
	sp.NotAfter = metav1.NewTime(time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC))
	key := f.item("gap", sp)
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	gap := reminders.Resolve(ny, 2030, time.March, 10, 2, 30)
	if want := time.Date(2030, 3, 10, 7, 30, 0, 0, time.UTC); !gap.Equal(want) {
		t.Fatalf("the shared schedule resolves the gap to %s, want %s", gap.UTC(), want)
	}
	f.at(f.executor(), gap.Add(-time.Hour))
	if w := f.get(key); w.Status.NextDue == nil || !w.Status.NextDue.Time.Equal(gap) {
		t.Fatalf("nextDue %v, want %s", w.Status.NextDue, gap.UTC())
	}
	f.at(f.executor(), gap)
	if o := f.wantOcc(key, gap, v1alpha1.OccurrenceDispatched, ""); !strings.HasSuffix(o.Session, "-203003100730") {
		t.Errorf("session %q", o.Session)
	}
	if s := f.session(f.derived(key, gap)); s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline] != "2030-03-10T07:45:00Z" {
		t.Errorf("start deadline %q", s.Annotations[v1alpha1.AnnotationScheduledWorkStartDeadline])
	}
	if w := f.get(key); w.Status.NextDue == nil || !w.Status.NextDue.Time.Equal(time.Date(2030, 3, 11, 6, 30, 0, 0, time.UTC)) {
		t.Errorf("the day after the gap: nextDue %v, want 06:30Z (02:30 EDT)", w.Status.NextDue)
	}
}

// A restarted executor (a fresh one at every step) reads the state back and
// repeats nothing: a pass with nothing to do writes nothing, and each
// occurrence's session is created once.
func TestRestartAtEveryStateIsIdempotent(t *testing.T) {
	f := newFixture(t)
	// c is interactive, so the headless cap of 1 that holds b back does not
	// hold it: it is claimed and parks for want of authority.
	interactive := spec()
	interactive.Mode = v1alpha1.ScheduledWorkInteractive
	a, b, c := f.item("restart-a", spec()), f.item("restart-b", spec()), f.item("restart-c", interactive)
	f.auth.set(func(w *v1alpha1.ScheduledWork) (Authority, error) {
		if w.Name == c.Name {
			return Authority{Reason: ReasonNoAuthority}, nil
		}
		return Authority{Basis: BasisHuman}, nil
	})
	ex := func() *Executor { e := f.executor(); e.MaxHeadless = 1; return e }
	versions := func() [3]string {
		return [3]string{f.get(a).ResourceVersion, f.get(b).ResourceVersion, f.get(c).ResourceVersion}
	}
	steady := func(when time.Time, what string) {
		t.Helper()
		before := versions()
		f.at(ex(), when)
		if after := versions(); after != before {
			t.Fatalf("%s: a restarted executor wrote again: %v then %v", what, before, after)
		}
	}

	f.at(ex(), day0.Add(-time.Minute))
	steady(day0.Add(-30*time.Second), "planned")
	f.at(ex(), day0)
	f.wantOcc(a, day0, v1alpha1.OccurrenceDispatched, "")
	f.wantOcc(b, day0, v1alpha1.OccurrencePending, ReasonAtCapacity)
	f.wantOcc(c, day0, v1alpha1.OccurrenceParked, ReasonNoAuthority)
	steady(day0.Add(time.Minute), "Dispatched, Pending at the cap and Parked")

	f.finish(f.derived(a, day0), v1alpha1.TaskResultSucceeded)
	f.release(f.derived(a, day0), true)
	f.create.hooks(func() error { return errTransient }, nil)
	f.at(ex(), day0.Add(2*time.Minute))
	f.wantOcc(b, day0, v1alpha1.OccurrenceClaimed, "")
	f.create.hooks(nil, nil)
	f.at(ex(), day0.Add(3*time.Minute))
	f.wantOcc(b, day0, v1alpha1.OccurrenceDispatched, "")
	steady(day0.Add(4*time.Minute), "Succeeded, Dispatched and Parked")

	if calls, created, _ := f.create.counts(); calls != 3 || created != 2 || len(f.sessionsOf()) != 1 {
		t.Errorf("calls %d created %d sessions %d; want each occurrence created once", calls, created, len(f.sessionsOf()))
	}
}

// The live-owner basis is a pre-claim condition: while the owner session is
// unproven the occurrence stays Pending, and when its grace ends it parks.
func TestUnprovenOwnerWaitsThenParks(t *testing.T) {
	f := newFixture(t)
	sp := spec()
	sp.Owner, sp.Editor = "session/owner/0f0e0d0c-0b0a-0908-0706-050403020100", "session/owner/0f0e0d0c-0b0a-0908-0706-050403020100"
	key := f.item("owned", sp)
	f.auth.set(func(*v1alpha1.ScheduledWork) (Authority, error) { return Authority{Unproven: true}, nil })
	f.at(f.executor(), day0.Add(-time.Minute))
	f.at(f.executor(), day0)
	if o := f.wantOcc(key, day0, v1alpha1.OccurrencePending, ReasonOwnerUnproven); o.ClaimedAt != nil {
		t.Fatalf("claimed while the owner was unproven: %+v", o)
	}
	f.at(f.executor(), day0.Add(grace-time.Minute))
	f.wantOcc(key, day0, v1alpha1.OccurrencePending, ReasonOwnerUnproven)
	f.at(f.executor(), day0.Add(grace+time.Second))
	f.wantOcc(key, day0, v1alpha1.OccurrenceParked, ReasonOwnerUnproven)
	if calls, _, _ := f.create.counts(); calls != 0 || f.get(key).Status.Counts.Parked != 1 {
		t.Errorf("calls %d counts %+v", calls, f.get(key).Status.Counts)
	}
}

// On the live-owner basis the session is the owner's child, made through the
// validation the owner's own create gets: its parent, its depth, its profile,
// and the depth limit, which parks the occurrence.
func TestSessionOwnerBasisCreatesTheOwnersChild(t *testing.T) {
	for _, c := range []struct {
		name  string
		depth string
		want  string
	}{
		{"a child at depth 2", "1", v1alpha1.OccurrenceDispatched},
		{"past the depth limit", "2", v1alpha1.OccurrenceParked},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			owner := f.ownerSession("coordinator-1", c.depth)
			sp := spec()
			ref := "session/" + owner.Name + "/" + string(owner.UID)
			sp.Owner, sp.Editor = ref, ref
			key := f.item("owned", sp)
			f.auth.set(func(*v1alpha1.ScheduledWork) (Authority, error) {
				return Authority{Basis: BasisSession, Owner: &scheduledwork.SessionRef{Name: owner.Name, UID: owner.UID}}, nil
			})
			tmpl := opsAudit
			tmpl.Profile = ""
			f.tmpl["ops-audit"] = tmpl
			f.at(f.executor(), day0.Add(-time.Minute))
			f.at(f.executor(), day0)
			o := f.wantOcc(key, day0, c.want, "")
			if c.want == v1alpha1.OccurrenceParked {
				if !strings.Contains(o.Reason, "two levels is the limit") || len(f.sessionsOf()) != 1 {
					t.Errorf("parked %+v with %d sessions", o, len(f.sessionsOf()))
				}
				return
			}
			s := f.session(o.Session)
			if s.Spec.Parent != owner.Name || s.Labels[v1alpha1.LabelDepth] != "2" || s.Spec.Profile != "ops" {
				t.Errorf("child: parent %q depth %q profile %q", s.Spec.Parent, s.Labels[v1alpha1.LabelDepth], s.Spec.Profile)
			}
		})
	}
}

// ownerSession creates a running session at depth with its pod, as the
// reconciler builds them, on profile ops.
func (f *fixture) ownerSession(name, depth string) *v1alpha1.AgentSession {
	f.t.Helper()
	ctx := context.Background()
	c := cluster(f.t)
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: f.sessions, Name: controller.ServiceAccountName}}
	if err := c.Create(ctx, sa); err != nil {
		f.t.Fatal(err)
	}
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.sessions, Name: name, Labels: map[string]string{v1alpha1.LabelDepth: depth}},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5", Profile: "ops", Parent: "dev-env-system/dev-env-human"},
	}
	if err := c.Create(ctx, s); err != nil {
		f.t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: f.sessions, Name: name, Labels: map[string]string{v1alpha1.LabelProfile: "ops"},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "agent"}}},
	}
	if err := c.Create(ctx, pod); err != nil {
		f.t.Fatal(err)
	}
	return s
}
