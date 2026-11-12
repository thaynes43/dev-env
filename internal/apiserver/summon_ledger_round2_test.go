package apiserver

// The schedules of the second review of PR 1085 and the coordinator's
// rulings on it (story 758, fix round 2; PR comments 6812929855 and
// 6812930183; DESIGN-064 5.9, "Correction, coordinator, 2026-11-12", amended
// 2026-11-12). Each schedule is driven by client hooks and fake clocks, never
// by goroutine timing, sleeps or loops.
//
// Two schedules here pass by design and assert a stated limit, not a
// guarantee: a create the API server has already received when its charge is
// pruned, and a stale destination created after a rebind. Admission by the
// leader closes both in story 741; until then this slice states them.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// fakeSummonClock is the monotonic clock of a summoning create's deadline in
// a test, and the contexts that carry the deadline. The test moves it; a
// context is done once the clock reaches its deadline, at the move, never by
// a timer.
type fakeSummonClock struct {
	now  time.Time
	ctxs []*fakeDeadlineCtx
	// pauseAfterCheck, when set, runs once, inside the first look at a
	// deadline context that finds it live, after that look: the process is
	// paused between its deadline check and the call that follows.
	pauseAfterCheck func()
}

func newFakeSummonClock() *fakeSummonClock {
	return &fakeSummonClock{now: time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeSummonClock) Now() time.Time { return c.now }

func (c *fakeSummonClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
	for _, x := range c.ctxs {
		if !c.now.Before(x.deadline) {
			x.end(context.DeadlineExceeded)
		}
	}
}

func (c *fakeSummonClock) WithDeadline(parent context.Context, deadline time.Time) (context.Context, context.CancelFunc) {
	x := &fakeDeadlineCtx{Context: parent, clock: c, deadline: deadline, done: make(chan struct{})}
	if !c.now.Before(deadline) {
		x.end(context.DeadlineExceeded)
	}
	c.ctxs = append(c.ctxs, x)
	return x, func() { x.end(context.Canceled) }
}

type fakeDeadlineCtx struct {
	context.Context
	clock    *fakeSummonClock
	deadline time.Time
	done     chan struct{}
	err      error
}

func (x *fakeDeadlineCtx) end(err error) {
	if x.err == nil {
		x.err = err
		close(x.done)
	}
}

func (x *fakeDeadlineCtx) Deadline() (time.Time, bool) { return x.deadline, true }
func (x *fakeDeadlineCtx) Done() <-chan struct{}       { return x.done }

func (x *fakeDeadlineCtx) Err() error {
	if x.err != nil {
		return x.err
	}
	if pause := x.clock.pauseAfterCheck; pause != nil {
		x.clock.pauseAfterCheck = nil
		pause()
	}
	return nil
}

// otherSession is a session of no summoning caller, holding name.
func otherSession(name string) *v1alpha1.AgentSession {
	return &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name},
		Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
	}
}

// sessionsOfID are the stored summoned sessions that carry id, by name.
func sessionsOfID(f *fixture, id string) []string {
	var out []string
	for _, s := range f.summonedSessions() {
		if s.Spec.Summon.DispatchID == id {
			out = append(out, s.Name)
		}
	}
	return out
}

// crashFirstCreate charges X for body and fails its session create: the
// state a retry starts from.
func crashFirstCreate(t *testing.T, f *fixture, body map[string]any) string {
	t.Helper()
	saved := f.srv.Client
	failSessionCreate(f, false, func(n int) bool { return n == 1 })
	if w := summonPost(t, f, body); w.Code < 500 {
		t.Fatalf("crash: %d %s", w.Code, w.Body.String())
	}
	f.srv.Client = saved
	x := summonDispatchID(summonTestAccount, body["idempotencyKey"].(string), nil)
	if _, ok := f.ledger().charge(x); !ok {
		t.Fatalf("X is not charged after the crash: %+v", f.ledger().Charges)
	}
	return x
}

// Ruling 1 (second review F1, HIGH): one absolute deadline, the monotonic
// reading of the ledger write before the create plus 2 minutes, carried as
// one context through the create. The review's schedule: X was charged at
// t=0 and its create failed; A retries at 64:59 and marks X; A is paused for
// 11 minutes, after its deadline check or inside its client's create before
// the request is sent; B charges Y at 75:59, finds no session of X and
// prunes X. A's create then runs under a context that is done, and the
// client refuses it: 503, and no session of X persisted by A.
//
// What this does not cover, and cannot: a request the API server has already
// received is outside what the client can stop. The third case is that
// schedule; it persists X after its charge was pruned, the first stated
// limit, which admission by the leader closes in story 741.
func TestSummonDeadlineFencesAPausedCreate(t *testing.T) {
	for _, pause := range []string{"after the deadline check", "inside the client's create", "the API server already received it"} {
		t.Run(pause, func(t *testing.T) {
			f := summonFixture(t, ledgerPolicy(4, 6))
			t0 := f.now
			body := keyedRemediation("rem-responder-1", "alert-1")
			x := crashFirstCreate(t, f, body)

			clock := newFakeSummonClock()
			b := replica(f, f.c)
			var bw *httptest.ResponseRecorder
			paused := false
			hold := func() {
				paused = true
				clock.Advance(11 * time.Minute)
				f.now = t0.Add(75*time.Minute + 59*time.Second)
				bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
			}
			var ac client.Client = f.c
			switch pause {
			case "after the deadline check":
				clock.pauseAfterCheck = hold
			case "inside the client's create":
				ac = holdSessionCreate(f, hold)
			default:
				// The request was sent before the pause: the API server
				// persists it whatever the client's context says.
				f.summonLimit = true
				sent := false
				ac = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if _, ok := obj.(*v1alpha1.AgentSession); !ok || sent {
						return c.Create(ctx, obj, opts...)
					}
					sent = true
					if err := ctx.Err(); err != nil {
						return err
					}
					hold()
					return c.Create(context.Background(), obj, opts...)
				}})
			}
			a := replica(f, ac)
			a.SummonClock = clock
			f.now = t0.Add(64*time.Minute + 59*time.Second)
			aw := post(t, a.Handler(), tokStranger, body)
			if !paused {
				t.Fatal("the schedule did not run: A was never paused")
			}
			wantStatus(t, bw, http.StatusCreated)
			if _, ok := f.ledger().charge(x); ok {
				t.Fatalf("B did not prune X, so this is not the review's schedule: %+v", f.ledger().Charges)
			}
			if pause == "the API server already received it" {
				// The stated limit, as it is today.
				wantStatus(t, aw, http.StatusCreated)
				if got := sessionsOfID(f, x); len(got) != 1 || len(f.summonUncharged) != 1 || f.summonUncharged[0] != "rem-responder-1" {
					t.Fatalf("sessions of X %v, uncharged creates %v", got, f.summonUncharged)
				}
				return
			}
			e := wantError(t, aw, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if !strings.Contains(e.Message, "2m0s") || !strings.Contains(e.Message, "charge") {
				t.Fatalf("message %q", e.Message)
			}
			if got := sessionsOfID(f, x); len(got) != 0 {
				t.Fatalf("A persisted %v after X was pruned", got)
			}
		})
	}
}

// rebindSchedule is the second review's F2 schedule. X binds to N; B, a
// retry of the same request on another replica, meets Q, an unrelated
// session that took N after B's checks, reads it, rebinds X to N-2 and
// creates N-2; Q is deleted, so N is free again. hold returns the function
// that runs B and the deletion; crashN2 makes B's create of N-2 fail, so the
// ledger binds X to N-2 with no session yet.
func rebindSchedule(t *testing.T, f *fixture, crashN2 bool) (run func(), bw **httptest.ResponseRecorder) {
	t.Helper()
	const n, n2 = "rem-responder-1", "rem-responder-1-2"
	var out *httptest.ResponseRecorder
	taken := false
	b := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		s, ok := obj.(*v1alpha1.AgentSession)
		if ok && s.Name == n && !taken {
			taken = true
			if err := c.Create(ctx, otherSession(n)); err != nil {
				return err
			}
		}
		if ok && s.Name == n2 && crashN2 {
			return errors.New("connection reset by peer")
		}
		return c.Create(ctx, obj, opts...)
	}}))
	return func() {
		out = post(t, b.Handler(), tokStranger, keyedRemediation(n, "alert-1"))
		var q v1alpha1.AgentSession
		if err := f.c.Get(context.Background(), client.ObjectKey{Namespace: sessionNS, Name: n}, &q); err == nil && q.Spec.Summon == nil {
			if err := f.c.Delete(context.Background(), &q); err != nil {
				t.Fatal(err)
			}
		}
	}, &out
}

// afterFirstLedgerWrite is a client whose first successful ledger write is
// followed, synchronously, by hook.
func afterFirstLedgerWrite(f *fixture, hook func()) client.Client {
	done := false
	return interceptor.NewClient(f.c, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		err := c.Update(ctx, obj, opts...)
		if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == ledgerTestName && err == nil && !done {
			done = true
			hook()
		}
		return err
	}})
}

// Ruling 2 (second review F2, HIGH), the form the client can win: every
// create attempt is preceded by its own compare-and-set mark, which succeeds
// only while the ledger binds the id to the name about to be created, and
// otherwise returns the binding, which the dispatch follows. A's mark comes
// after B's rebind, so A does not create the old name N.
func TestSummonMarkBeforeEveryCreate(t *testing.T) {
	const n, n2 = "rem-responder-1", "rem-responder-1-2"
	body := keyedRemediation(n, "alert-1")
	x := summonDispatchID(summonTestAccount, "alert-1", nil)

	t.Run("the first attempt: A finds B's session", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		run, bw := rebindSchedule(t, f, false)
		a := replica(f, afterFirstLedgerWrite(f, run))
		aw := post(t, a.Handler(), tokStranger, body)
		wantStatus(t, *bw, http.StatusCreated)
		wantStatus(t, aw, http.StatusOK)
		if got := decode[apiv1.Session](t, aw).Name; got != n2 {
			t.Fatalf("A returned %s, want B's %s", got, n2)
		}
		if got := sessionsOfID(f, x); len(got) != 1 || got[0] != n2 {
			t.Fatalf("sessions of X %v", got)
		}
	})

	t.Run("the first attempt: the binding moved, no session yet", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		run, bw := rebindSchedule(t, f, true)
		a := replica(f, afterFirstLedgerWrite(f, run))
		aw := post(t, a.Handler(), tokStranger, body)
		if (*bw).Code < 500 {
			t.Fatalf("B: %d %s", (*bw).Code, (*bw).Body.String())
		}
		wantStatus(t, aw, http.StatusCreated)
		if got := decode[apiv1.Session](t, aw).Name; got != n2 {
			t.Fatalf("A created %s, want the binding's %s", got, n2)
		}
		if got := sessionsOfID(f, x); len(got) != 1 || got[0] != n2 {
			t.Fatalf("sessions of X %v", got)
		}
		if ch, _ := f.ledger().charge(x); ch.Name != n2 || len(f.ledger().Charges) != 1 {
			t.Fatalf("charges %+v", f.ledger().Charges)
		}
	})

	// A's create of N meets Q; before A reads N, B runs and Q is deleted, so
	// A's read is NotFound. A's retry of N is preceded by its own mark, which
	// finds B's session of X.
	t.Run("the retry after NotFound", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		takenByQ, readN := false, false
		a := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == n && !takenByQ {
				takenByQ = true
				if err := c.Create(ctx, otherSession(n)); err != nil {
					return err
				}
			}
			return c.Create(ctx, obj, opts...)
		}}))
		a.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == n && !readN {
				readN = true
				bw = post(t, b.Handler(), tokStranger, body)
				if err := c.Delete(ctx, otherSession(n)); err != nil {
					t.Fatal(err)
				}
			}
			return c.Get(ctx, key, obj, opts...)
		}})
		aw := post(t, a.Handler(), tokStranger, body)
		if !readN {
			t.Fatal("the schedule did not run: A never read N")
		}
		wantStatus(t, bw, http.StatusCreated)
		wantStatus(t, aw, http.StatusOK)
		if got := decode[apiv1.Session](t, aw).Name; got != n2 {
			t.Fatalf("A returned %s, want B's %s", got, n2)
		}
		if got := sessionsOfID(f, x); len(got) != 1 || got[0] != n2 {
			t.Fatalf("sessions of X %v", got)
		}
	})
}

// Ruling 2, the form the client cannot win, asserted as it is today: the
// second stated limit. A is paused after its mark, between that write and
// its create. B rebinds X to N-2 and creates it; Q, which held N, is deleted.
// A resumes inside its deadline and creates the stale destination N: two
// sessions carry X, and the ledger binds X to N-2 only. No client-side check
// closes this; admission by the leader does, in story 741, by refusing the
// session whose name the ledger does not bind.
func TestSummonStaleDestinationIsAStatedLimit(t *testing.T) {
	const n, n2 = "rem-responder-1", "rem-responder-1-2"
	f := summonFixture(t, ledgerPolicy(4, 6))
	f.summonLimit = true
	run, bw := rebindSchedule(t, f, false)
	a := replica(f, holdSessionCreate(f, run))
	aw := post(t, a.Handler(), tokStranger, keyedRemediation(n, "alert-1"))
	wantStatus(t, *bw, http.StatusCreated)
	wantStatus(t, aw, http.StatusCreated)
	x := summonDispatchID(summonTestAccount, "alert-1", nil)
	got := sessionsOfID(f, x)
	if len(got) != 2 {
		t.Fatalf("sessions of X %v, want the two the limit allows", got)
	}
	if ch, _ := f.ledger().charge(x); ch.Name != n2 || len(f.ledger().Charges) != 1 {
		t.Fatalf("charges %+v", f.ledger().Charges)
	}
	if len(f.summonUncharged) != 1 || f.summonUncharged[0] != n {
		t.Fatalf("creates the ledger did not bind %v, want [%s]", f.summonUncharged, n)
	}
}

// Ruling 3 (second review F3, MEDIUM): a rebind, and the branch that finds
// another replica already moved the binding, applies the window rule of a
// retry. X was charged at t=0 and its create failed. A retries at 64:59 and
// marks X; its create of N meets Q. While A reads Q, at 65:01, B charges and
// creates Y. A's rebind finds X outside its window: with room in the hour it
// re-charges X at 65:01 and creates N-2; without room it answers 429 and
// creates nothing. In the moved branch, C, a retry of the same request, ran
// first, at 64:59: it met Q too, rebound X to N-2 and crashed before its
// create, so A's rebind finds X already bound to N-2.
func TestSummonRebindAppliesTheWindow(t *testing.T) {
	const n, n2 = "rem-responder-1", "rem-responder-1-2"
	for _, moved := range []bool{false, true} {
		for _, room := range []bool{true, false} {
			name := "rebind"
			if moved {
				name = "moved by another replica"
			}
			if room {
				name += ", room in the hour"
			} else {
				name += ", the hour full"
			}
			t.Run(name, func(t *testing.T) {
				limit := int32(1)
				if room {
					limit = 2
				}
				f := summonFixture(t, ledgerPolicy(4, limit))
				t0 := f.now
				body := keyedRemediation(n, "alert-1")
				x := crashFirstCreate(t, f, body)

				b := replica(f, f.c)
				c := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == n2 {
						return errors.New("connection reset by peer")
					}
					return cl.Create(ctx, obj, opts...)
				}}))
				var bw, cw *httptest.ResponseRecorder
				taken, read := false, false
				a := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
					if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == n && !taken {
						taken = true
						if err := cl.Create(ctx, otherSession(n)); err != nil {
							return err
						}
					}
					return cl.Create(ctx, obj, opts...)
				}}))
				a.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*v1alpha1.AgentSession); ok && key.Name == n && !read {
						read = true
						if moved {
							cw = post(t, c.Handler(), tokStranger, body)
							if ch, _ := f.ledger().charge(x); ch.Name != n2 {
								t.Fatalf("C did not rebind X: %+v", f.ledger().Charges)
							}
						}
						f.now = t0.Add(65*time.Minute + time.Second)
						bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
					}
					return cl.Get(ctx, key, obj, opts...)
				}})
				f.now = t0.Add(64*time.Minute + 59*time.Second)
				aw := post(t, a.Handler(), tokStranger, body)
				wantStatus(t, bw, http.StatusCreated)
				if moved && cw.Code < 500 {
					t.Fatalf("C: %d %s", cw.Code, cw.Body.String())
				}
				e := f.ledger()
				ch, ok := e.charge(x)
				if !ok {
					t.Fatalf("X left the ledger: %+v", e.Charges)
				}
				if !room {
					wantError(t, aw, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
					if got := sessionsOfID(f, x); len(got) != 0 {
						t.Fatalf("sessions of X %v", got)
					}
					if !ch.At.Equal(t0) || e.UrgentRefusals != 1 {
						t.Fatalf("X %+v, urgent refusals %d", ch, e.UrgentRefusals)
					}
					return
				}
				wantStatus(t, aw, http.StatusCreated)
				if got := decode[apiv1.Session](t, aw).Name; got != n2 {
					t.Fatalf("A created %s", got)
				}
				if !ch.At.Equal(f.now) || ch.Name != n2 || e.inWindowAt(f.now) != 2 {
					t.Fatalf("X %+v, want re-charged at %s; charges %+v", ch, f.now, e.Charges)
				}
			})
		}
	}
}

// inWindowAt counts the charges that count toward createsPerHour at now.
func (e ledgerTestEntry) inWindowAt(now time.Time) int {
	n := 0
	for _, c := range e.Charges {
		if now.Sub(c.At) < 65*time.Minute {
			n++
		}
	}
	return n
}

// Ruling 4 (second review F4, MEDIUM), documented: the limit is on charges,
// not creates. A create may follow its charge at any time inside that
// charge's window, since a retry reuses it. With createsPerHour 1: X is
// charged at t=0 and its create fails; its retry creates X at 64:00; Y is
// charged and created at 65:00. Two creates one minute apart, and never two
// charges in one window.
func TestSummonLimitCountsChargesNotCreates(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 1))
	t0 := f.now
	body := keyedRemediation("rem-responder-1", "alert-1")
	crashFirstCreate(t, f, body)
	f.now = t0.Add(64 * time.Minute)
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	f.now = t0.Add(65 * time.Minute)
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
	first, second := f.session("rem-responder-1"), f.session("rem-responder-2")
	if gap := second.CreationTimestamp.Sub(first.CreationTimestamp.Time); gap != time.Minute {
		t.Fatalf("creates %s apart", gap)
	}
	if e := f.ledger(); e.inWindowAt(f.now) != 1 {
		t.Fatalf("charges in the window %+v", e.Charges)
	}
}
