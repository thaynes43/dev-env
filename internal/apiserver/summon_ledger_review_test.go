package apiserver

// The schedules of the review of PR 1085 and the coordinator's rulings on it
// (story 758, fix round 1; DESIGN-064 5.9, "Correction, coordinator,
// 2026-11-12"). Each schedule is driven by client hooks and the fake clocks,
// never by goroutine timing or sleeps. The fixture's create hook
// (summonFixture) also checks every summoned create against the ledger.

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// summonTestAccount is the summoning caller's account, spec.parent.
const summonTestAccount = "upgrade-agent/alert-responder"

// replicaAt is a second API replica whose wall clock is offset from the
// fixture's.
func replicaAt(f *fixture, c client.Client, offset time.Duration) *Server {
	s := replica(f, c)
	s.Now = func() time.Time { return f.now.Add(offset) }
	return s
}

// holdSessionCreate is a client whose first AgentSession create first runs
// hold, synchronously: replica A is paused immediately before its create.
func holdSessionCreate(f *fixture, hold func()) client.Client {
	held := false
	return interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok && !held {
			held = true
			hold()
		}
		return c.Create(ctx, obj, opts...)
	}})
}

// wantOneSessionPerID fails when two summoned sessions carry one dispatch id.
func wantOneSessionPerID(t *testing.T, f *fixture) {
	t.Helper()
	seen := map[string]string{}
	for _, s := range f.summonedSessions() {
		id := s.Spec.Summon.DispatchID
		if other, ok := seen[id]; ok {
			t.Fatalf("sessions %s and %s carry one dispatch id %s", other, s.Name, id)
		}
		seen[id] = s.Name
	}
}

func generationOf(t *testing.T, s *v1alpha1.AgentSession) int {
	t.Helper()
	g, err := strconv.Atoi(s.Annotations[v1alpha1.AnnotationSummonGeneration])
	if err != nil {
		t.Fatalf("session %s generation %q", s.Name, s.Annotations[v1alpha1.AnnotationSummonGeneration])
	}
	return g
}

// Ruling 1 (review finding 1, HIGH): a charge another replica's write would
// prune is kept while a create is in flight on it. A crashed after charging
// X; A retries and is held immediately before its session create; B writes
// the ledger (a charge, or an urgent refusal); then A creates. The fixture's
// hook fails the test if X is not in the ledger at A's create.
func TestSummonLedgerPruneKeepsAnInflightCharge(t *testing.T) {
	cases := []struct {
		name    string
		retryAt time.Duration // A's retry, from X's charge
		writeAt time.Duration // B's write, from X's charge
		refusal bool          // B writes an urgent refusal, not a charge
	}{
		{"B charges at 60:01", 59*time.Minute + 59*time.Second, 60*time.Minute + time.Second, false},
		{"B charges at 65:01", 64*time.Minute + 59*time.Second, 65*time.Minute + time.Second, false},
		{"B refuses at 60:01", 59*time.Minute + 59*time.Second, 60*time.Minute + time.Second, true},
		{"B refuses at 65:01", 64*time.Minute + 59*time.Second, 65*time.Minute + time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := summonFixture(t, ledgerPolicy(4, 6))
			t0 := f.now
			if tc.refusal {
				// The escalations' pool is full, so B's escalation is refused
				// by its pool, and its refusal is a ledger write.
				for _, name := range []string{"esc-1", "esc-2"} {
					wantStatus(t, summonPost(t, f, map[string]any{"name": name, "mode": "remote", "lane": "escalation"}), http.StatusCreated)
				}
			}
			failSessionCreate(f, false, func(n int) bool { return n == 1 })
			body := keyedRemediation("rem-responder-1", "alert-1")
			if w := summonPost(t, f, body); w.Code < 500 {
				t.Fatalf("crash: %d %s", w.Code, w.Body.String())
			}
			x := summonDispatchID(summonTestAccount, "alert-1", nil)
			if _, ok := f.ledger().charge(x); !ok {
				t.Fatalf("X is not charged after the crash: %+v", f.ledger().Charges)
			}

			b := replica(f, f.c)
			var bw *httptest.ResponseRecorder
			a := replica(f, holdSessionCreate(f, func() {
				f.now = t0.Add(tc.writeAt)
				if tc.refusal {
					bw = post(t, b.Handler(), tokStranger, map[string]any{"name": "esc-3", "mode": "remote", "lane": "escalation"})
				} else {
					bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
				}
			}))
			f.now = t0.Add(tc.retryAt)
			aw := post(t, a.Handler(), tokStranger, body)
			wantStatus(t, aw, http.StatusCreated)
			if tc.refusal {
				wantError(t, bw, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
				if e := f.ledger(); e.UrgentRefusals != 1 {
					t.Fatalf("urgent refusals %d", e.UrgentRefusals)
				}
			} else {
				wantStatus(t, bw, http.StatusCreated)
			}
			if got := f.session("rem-responder-1").Spec.Summon.DispatchID; got != x {
				t.Fatalf("A created id %s, want X %s", got, x)
			}
			if _, ok := f.ledger().charge(x); !ok {
				t.Fatalf("X left the ledger: %+v", f.ledger().Charges)
			}
			wantOneSessionPerID(t, f)
		})
	}
}

// Ruling 1, the deadline: the session create is issued within two minutes of
// the ledger write before it, its mark, by the replica's monotonic clock, or
// the request answers 503, creates nothing and leaves the charge. Since fix
// round 2 the deadline is a context, done at the mark's monotonic reading
// plus 2 minutes (second review's rulings, 1).
func TestSummonCreateDeadline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after time.Duration
		want  int
	}{
		{"past two minutes", 2*time.Minute + time.Second, http.StatusServiceUnavailable},
		{"inside two minutes", 2*time.Minute - time.Second, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := summonFixture(t)
			clock := newFakeSummonClock()
			f.srv.SummonClock = clock
			advance := tc.after
			f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				err := c.Update(ctx, obj, opts...)
				if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == ledgerTestName && err == nil {
					clock.Advance(advance)
				}
				return err
			}})
			body := keyedRemediation("rem-responder-1", "alert-1")
			w := summonPost(t, f, body)
			wantStatus(t, w, tc.want)
			x := summonDispatchID(summonTestAccount, "alert-1", nil)
			ch, ok := f.ledger().charge(x)
			if !ok {
				t.Fatalf("no charge for X: %+v", f.ledger().Charges)
			}
			if tc.want != http.StatusServiceUnavailable {
				return
			}
			if n := len(f.summonedSessions()); n != 0 {
				t.Fatalf("%d sessions past the deadline", n)
			}
			if !strings.Contains(w.Body.String(), "no refund") || !strings.Contains(w.Body.String(), "normal pruning") || strings.Contains(w.Body.String(), "stays") {
				t.Fatalf("the refusal does not say there is no refund and that normal pruning applies: %s", w.Body.String())
			}

			// The retry, inside the deadline, marks the same charge and creates.
			advance = 0
			f.now = f.now.Add(time.Minute)
			wantStatus(t, summonPost(t, f, body), http.StatusCreated)
			e := f.ledger()
			got, _ := e.charge(x)
			if len(e.Charges) != 1 || !got.At.Equal(ch.At) || !got.Attempt.Equal(f.now) {
				t.Fatalf("retry: charge %+v, before %+v", e.Charges, ch)
			}
		})
	}
}

// The second review's rulings, 1 (replacing fix round 1's "what is left of
// the deadline"): the create, and the read after its AlreadyExists, run under
// one context whose deadline is absolute, the monotonic reading of the mark
// before that create plus 2 minutes, not a timeout started later. Each
// ledger write here is followed by 90 seconds on the fake monotonic clock; a
// clash makes the create, the read and, after the rebind and its own mark,
// the second create.
func TestSummonCreateRunsUnderTheDeadline(t *testing.T) {
	f := summonFixture(t)
	clock := newFakeSummonClock()
	t0 := clock.Now()
	f.srv.SummonClock = clock
	var calls []string
	record := func(what string, ctx context.Context) {
		dl, ok := ctx.Deadline()
		if !ok {
			calls = append(calls, what+" without a deadline")
			return
		}
		calls = append(calls, what+" until "+dl.Sub(t0).String())
	}
	taken := false
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, obj, opts...)
			if cm, ok := obj.(*corev1.ConfigMap); ok && cm.Name == ledgerTestName && err == nil {
				clock.Advance(90 * time.Second)
			}
			return err
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			s, ok := obj.(*v1alpha1.AgentSession)
			if !ok {
				return c.Create(ctx, obj, opts...)
			}
			record("create "+s.Name, ctx)
			if s.Name == "rem-responder-1" && !taken {
				taken = true
				if err := c.Create(context.Background(), otherSession(s.Name)); err != nil {
					return err
				}
			}
			return c.Create(ctx, obj, opts...)
		},
	})
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			record("read "+key.Name, ctx)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	w := summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1"))
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1-2" {
		t.Fatalf("name %s", got)
	}
	// The charge's write reads 0 and moves the clock to 1m30s; the mark reads
	// 1m30s, so its deadline is 3m30s. The rebind reads 3m0s; its mark reads
	// 4m30s, so the second create's deadline is 6m30s.
	want := "create rem-responder-1 until 3m30s,read rem-responder-1 until 3m30s,create rem-responder-1-2 until 6m30s"
	if got := strings.Join(calls, ","); got != want {
		t.Fatalf("calls %s, want %s", got, want)
	}
}

// Ruling 2 (review finding 2, HIGH): one charge binds one dispatch to its
// request and its name. Two replicas, one key and two requested names: B's
// whole create runs inside A's first ledger write. A then finds B's charge
// under its id with another hash: 409, and no write.
func TestSummonConcurrentKeyDifferentNames(t *testing.T) {
	t.Run("no earlier session", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() {
			bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-b", "alert-1"))
		})
		aw := post(t, replica(f, ac).Handler(), tokStranger, keyedRemediation("rem-responder-a", "alert-1"))
		wantStatus(t, bw, http.StatusCreated)
		wantError(t, aw, http.StatusConflict, apiv1.CodeConflict)
		wantLostTheWrite(t, writes)
		if len(writes.writes) != 1 {
			t.Fatalf("A wrote after it lost: %v", writes.writes)
		}
		if s, e := f.summonedSessions(), f.ledger(); len(s) != 1 || s[0].Name != "rem-responder-b" || len(e.Charges) != 1 {
			t.Fatalf("sessions %d charges %+v", len(s), e.Charges)
		}
		wantOneSessionPerID(t, f)
	})

	t.Run("an ended earlier session", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-a", "alert-1")), http.StatusCreated)
		f.editStatus("rem-responder-a", v1alpha1.PhaseFailed)
		f.now = f.now.Add(time.Minute)
		b := replica(f, f.c)
		var bw *httptest.ResponseRecorder
		ac, writes := interleave(f, func() {
			bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-a", "alert-1"))
		})
		aw := post(t, replica(f, ac).Handler(), tokStranger, keyedRemediation("rem-responder-a-2", "alert-1"))
		wantStatus(t, bw, http.StatusCreated)
		if got := decode[apiv1.Session](t, bw).Name; got != "rem-responder-a-2" {
			t.Fatalf("B took %s", got)
		}
		wantError(t, aw, http.StatusConflict, apiv1.CodeConflict)
		wantLostTheWrite(t, writes)
		if s, e := f.summonedSessions(), f.ledger(); len(s) != 2 || len(e.Charges) != 2 {
			t.Fatalf("sessions %d charges %+v", len(s), e.Charges)
		}
		wantOneSessionPerID(t, f)
	})

	// The charge alone binds: B charged and crashed before its session, so
	// only the ledger knows B's request.
	t.Run("a charged dispatch with no session yet", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		failSessionCreate(f, false, func(n int) bool { return n == 1 })
		if w := summonPost(t, f, keyedRemediation("rem-responder-b", "alert-1")); w.Code < 500 {
			t.Fatalf("crash: %d %s", w.Code, w.Body.String())
		}
		version := f.ledgerVersion()
		f.now = f.now.Add(time.Minute)
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-a", "alert-1")), http.StatusConflict, apiv1.CodeConflict)
		if f.ledgerVersion() != version || len(f.summonedSessions()) != 0 {
			t.Fatal("the other request wrote the ledger or created a session")
		}
		w := summonPost(t, f, keyedRemediation("rem-responder-b", "alert-1"))
		wantStatus(t, w, http.StatusCreated)
		if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-b" {
			t.Fatalf("B's retry took %s", got)
		}
		wantOneSessionPerID(t, f)
	})
}

// Ruling 2: a NotFound right after AlreadyExists retries the same name once,
// and the charge keeps its name.
func TestSummonNotFoundAfterAlreadyExistsRetriesTheName(t *testing.T) {
	f := summonFixture(t)
	first := true
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && first {
			first = false
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "agentsessions"}, s.Name)
		}
		return c.Create(ctx, obj, opts...)
	}})
	w := summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1"))
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1" {
		t.Fatalf("name %s", got)
	}
	e := f.ledger()
	if len(e.Charges) != 1 || e.Charges[0].Name != "rem-responder-1" {
		t.Fatalf("charges %+v", e.Charges)
	}
}

// Ruling 2 and 7: a name another request took after the check is a clash.
// The dispatch rebinds its charge to the next free suffix in one more ledger
// write, then creates, and the brief names the session it created.
func TestSummonNameClashRebindsTheCharge(t *testing.T) {
	f := summonFixture(t)
	taken := false
	var versionAtClash string
	f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == "rem-responder-1" && !taken {
			taken = true
			versionAtClash = f.ledgerVersion()
			other := &v1alpha1.AgentSession{
				ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: "rem-responder-1"},
				Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
			}
			if err := c.Create(ctx, other); err != nil {
				return err
			}
		}
		return c.Create(ctx, obj, opts...)
	}})
	w := summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1"))
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1-2" {
		t.Fatalf("name %s", got)
	}
	e := f.ledger()
	if len(e.Charges) != 1 || e.Charges[0].Name != "rem-responder-1-2" {
		t.Fatalf("the charge was not rebound: %+v", e.Charges)
	}
	if f.ledgerVersion() == versionAtClash {
		t.Fatal("the rebind wrote nothing")
	}
	wantBriefNames(t, f.session("rem-responder-1-2"), "rem-responder-1")
}

// wantBriefNames checks a summoned session's brief names the session as it
// is stored, its request hash stays bound to the requested name, and the
// transport check passes on it.
func wantBriefNames(t *testing.T, s *v1alpha1.AgentSession, requested string) {
	t.Helper()
	line := summonPlatformLine(s.Name, s.Spec.Caller, string(s.Spec.Lane))
	if !strings.Contains(s.Spec.Prompt, "\n"+line+"\n") {
		t.Fatalf("brief of %s does not carry %q:\n%s", s.Name, line, s.Spec.Prompt)
	}
	if requested != s.Name && strings.Contains(s.Spec.Prompt, "Session "+requested+",") {
		t.Fatalf("brief of %s names the requested %s", s.Name, requested)
	}
	if got, want := s.Annotations[v1alpha1.AnnotationRequestHash], summonRequestHash(summonWire{Name: requested, Mode: string(s.Spec.Mode), Lane: string(s.Spec.Lane)}, s.Spec.Summon.Priority); got != want {
		t.Fatalf("request hash %s, want the requested name's %s", got, want)
	}
	if err := checkAgentd(s); err != nil {
		t.Fatalf("transport check: %v", err)
	}
}

// Ruling 7 (review, additional MEDIUM): a re-fire stored as -2 names -2.
func TestSummonBriefNamesTheFinalSession(t *testing.T) {
	f := summonFixture(t)
	body := keyedRemediation("rem-responder-1", "alert-1")
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	wantBriefNames(t, f.session("rem-responder-1"), "rem-responder-1")
	f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
	f.now = f.now.Add(time.Minute)
	w := summonPost(t, f, body)
	wantStatus(t, w, http.StatusCreated)
	if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1-2" {
		t.Fatalf("re-fire name %s", got)
	}
	wantBriefNames(t, f.session("rem-responder-1-2"), "rem-responder-1")
}

// Ruling 3 (review finding 4, HIGH): the newest session of a key is the one
// with the highest generation; creation times and names do not order.
func TestSummonNewestIsTheHighestGeneration(t *testing.T) {
	t.Run("three in one second in adverse name order", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 30))
		names := []string{"rem-responder-z", "rem-responder-a", "rem-responder-m"}
		for i, name := range names {
			wantStatus(t, summonPost(t, f, keyedRemediation(name, "alert-1")), http.StatusCreated)
			if g := generationOf(t, f.session(name)); g != i+1 {
				t.Fatalf("%s generation %d, want %d", name, g, i+1)
			}
			if i < len(names)-1 {
				f.editStatus(name, v1alpha1.PhaseFailed)
			}
		}
		w := summonPost(t, f, keyedRemediation("rem-responder-m", "alert-1"))
		wantStatus(t, w, http.StatusOK)
		if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-m" {
			t.Fatalf("repeat returned %s", got)
		}
		if s, e := f.summonedSessions(), f.ledger(); len(s) != 3 || len(e.Charges) != 3 {
			t.Fatalf("sessions %d charges %d", len(s), len(e.Charges))
		}
		wantOneSessionPerID(t, f)
	})

	t.Run("a suffix step from -9 to -10", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 30))
		body := keyedRemediation("rem-responder-1", "alert-1")
		last := ""
		for i := 1; i <= 10; i++ {
			w := summonPost(t, f, body)
			wantStatus(t, w, http.StatusCreated)
			want := "rem-responder-1"
			if i > 1 {
				want += "-" + strconv.Itoa(i)
			}
			if last = decode[apiv1.Session](t, w).Name; last != want {
				t.Fatalf("create %d took %s, want %s", i, last, want)
			}
			if g := generationOf(t, f.session(last)); g != i {
				t.Fatalf("%s generation %d", last, g)
			}
			if i < 10 {
				f.editStatus(last, v1alpha1.PhaseFailed)
			}
		}
		w := summonPost(t, f, body)
		wantStatus(t, w, http.StatusOK)
		if got := decode[apiv1.Session](t, w).Name; got != "rem-responder-1-10" {
			t.Fatalf("repeat returned %s", got)
		}
		if s := f.summonedSessions(); len(s) != 10 {
			t.Fatalf("%d sessions", len(s))
		}
		wantOneSessionPerID(t, f)
	})
}

// plantKeySession is a session of the caller's key planted in the store, as
// an older replica or a hand edit could leave it: a request hash for body,
// and the generation annotation when gen is not empty.
func plantKeySession(name, uid, key, gen string, finished bool, body summonWire) *v1alpha1.AgentSession {
	s := &v1alpha1.AgentSession{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: sessionNS, Name: name, UID: types.UID(uid),
			Labels:      map[string]string{v1alpha1.LabelDepth: "0", v1alpha1.LabelIdempotencyKey: key},
			Annotations: map[string]string{v1alpha1.AnnotationRequestHash: summonRequestHash(body, v1alpha1.SummonPriorityUrgent)},
		},
		Spec: v1alpha1.AgentSessionSpec{
			Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p",
			Parent: summonTestAccount, Caller: ledgerTestKey, Lane: v1alpha1.LaneRemediation, IdempotencyKey: key,
			Summon: &v1alpha1.SessionSummon{DispatchID: "planted-" + uid, Priority: v1alpha1.SummonPriorityUrgent},
		},
	}
	if gen != "" {
		s.Annotations[v1alpha1.AnnotationSummonGeneration] = gen
	}
	if finished {
		s.Status.Phase = v1alpha1.PhaseFailed
	}
	return s
}

// The coordinator's answer to op758-fix1-q-1: a tie on the highest
// generation goes to the greater UID; a generation that is not a
// non-negative integer is 503 and nothing is charged or created.
func TestSummonGenerationTieAndUnreadable(t *testing.T) {
	req := summonWire{Name: "rem-responder-1", Mode: "task", Lane: "remediation"}
	// Two sessions of one key without the annotation, both generation 0. The
	// live one has the greater UID; its name sorts first in one layout and
	// last in the other, so neither list order nor name decides.
	for _, layout := range []struct{ live, ended string }{
		{"rem-responder-1", "rem-responder-1-2"},
		{"rem-responder-1-2", "rem-responder-1"},
	} {
		t.Run("tie, live "+layout.live, func(t *testing.T) {
			f := summonFixture(t, ledgerPolicy(4, 6),
				plantKeySession(layout.live, "uid-b", "alert-1", "", false, req),
				plantKeySession(layout.ended, "uid-a", "alert-1", "", true, req))
			version := f.ledgerVersion()
			w := summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1"))
			wantStatus(t, w, http.StatusOK)
			if got := decode[apiv1.Session](t, w).Name; got != layout.live {
				t.Fatalf("repeat returned %s, want %s", got, layout.live)
			}
			if f.ledgerVersion() != version {
				t.Fatal("the repeat wrote the ledger")
			}
		})
	}
	// The supported range is 0 to the largest int64 less one; the largest
	// value is refused, since the next generation would not fit (second
	// review, choice 1; rulings, 6).
	for _, gen := range []string{"x", "-1", "1.5", "", "9223372036854775807"} {
		t.Run("unreadable "+strconv.Quote(gen), func(t *testing.T) {
			s := plantKeySession("rem-responder-1", "uid-a", "alert-1", gen, true, req)
			if gen == "" {
				s.Annotations[v1alpha1.AnnotationSummonGeneration] = ""
			}
			f := summonFixture(t, ledgerPolicy(4, 6), s)
			version := f.ledgerVersion()
			wantError(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			if f.ledgerVersion() != version || len(f.summonedSessions()) != 1 {
				t.Fatal("an unreadable generation charged or created")
			}
		})
	}
}

// Ruling 4 (review finding 3, MEDIUM): a charge counts for 65 minutes by the
// later of the replica's clock and the entry's clock, and every write stores
// that time back, so replica clocks within 5 minutes of each other never let
// more than createsPerHour creates through in any 60 minutes.
func TestSummonLedgerClockSkew(t *testing.T) {
	t.Run("a replica 2 minutes ahead", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 1))
		t0 := f.now
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
		ahead := replicaAt(f, f.c, 2*time.Minute)
		f.now = t0.Add(59 * time.Minute)
		wantError(t, post(t, ahead.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if e := f.ledger(); !e.Clock.Equal(t0.Add(61 * time.Minute)) {
			t.Fatalf("clock %s", e.Clock)
		}
		// Past 65 minutes by its own clock, and 63 by the true one.
		f.now = t0.Add(63 * time.Minute)
		wantStatus(t, post(t, ahead.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
	})

	t.Run("a replica 2 minutes behind", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 1))
		t0 := f.now
		behind := replicaAt(f, f.c, -2*time.Minute)
		wantStatus(t, post(t, behind.Handler(), tokStranger, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
		// The behind replica's charge counts on a true clock 59 minutes on.
		f.now = t0.Add(59 * time.Minute)
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		// The behind replica writes the later time, never its own.
		f.now = t0.Add(60 * time.Minute)
		wantError(t, post(t, behind.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		e := f.ledger()
		if !e.Clock.Equal(t0.Add(59*time.Minute)) || e.LastUrgentRefusal == nil || !e.LastUrgentRefusal.Equal(t0.Add(59*time.Minute)) || e.UrgentRefusals != 2 {
			t.Fatalf("clock %s last refusal %v count %d", e.Clock, e.LastUrgentRefusal, e.UrgentRefusals)
		}
	})

	t.Run("a clock that jumps forward and back", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 1))
		t0 := f.now
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
		jump := time.Duration(0)
		f.srv.Now = func() time.Time { return f.now.Add(jump) }
		refused := func(at time.Duration, wantClock time.Duration) {
			t.Helper()
			f.now = t0.Add(at)
			wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
			if e := f.ledger(); !e.Clock.Equal(t0.Add(wantClock)) {
				t.Fatalf("at %s: clock %s, want %s", at, e.Clock, t0.Add(wantClock))
			}
		}
		jump = 3 * time.Minute
		refused(58*time.Minute, 61*time.Minute)
		jump = 0
		refused(59*time.Minute, 61*time.Minute)
		refused(64*time.Minute+30*time.Second, 64*time.Minute+30*time.Second)
		f.now = t0.Add(65*time.Minute + 30*time.Second)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusCreated)
		if e := f.ledger(); !e.Clock.Equal(f.now) {
			t.Fatalf("clock %s after the create at %s", e.Clock, f.now)
		}
	})
}

// validLedgerEntry is a well-formed entry with one charge for another key.
const validLedgerEntry = `{"schema":"dev-env.summon-creates.v1","clock":"2026-10-06T17:20:00Z",` +
	`"charges":[{"id":"0123456789abcdef0123456789abcdef","at":"2026-10-06T17:20:00Z","attempt":"2026-10-06T17:20:00Z",` +
	`"hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","name":"rem-responder-9"}],` +
	`"urgentRefusals":0,"lastUrgentRefusal":null}`

// Ruling 6 (review finding 8, MEDIUM): an entry that is present but wrong
// fails closed: 503, no write, no session, the entry untouched.
func TestSummonMalformedLedgerFailsClosed(t *testing.T) {
	charge := `{"id":"0123456789abcdef0123456789abcdef","at":"2026-10-06T17:20:00Z","attempt":"2026-10-06T17:20:00Z","hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","name":"rem-responder-9"}`
	head := `{"schema":"dev-env.summon-creates.v1","clock":"2026-10-06T17:20:00Z",`
	tail := `"urgentRefusals":0,"lastUrgentRefusal":null}`
	for _, tc := range []struct{ name, raw string }{
		{"empty value", ``},
		{"not an object", `[]`},
		{"null", `null`},
		{"charges null", head + `"charges":null,` + tail},
		{"charges missing", head + tail},
		{"charges repeated", head + `"charges":[` + charge + `],` + `"urgentRefusals":0,"lastUrgentRefusal":null,"charges":[]}`},
		{"a member repeated in a charge", head + `"charges":[` + strings.Replace(charge, `"name":"rem-responder-9"`, `"name":"rem-responder-9","name":"rem-responder-8"`, 1) + `],` + tail},
		{"a charge without its hash", head + `"charges":[` + strings.Replace(charge, `"hash":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",`, ``, 1) + `],` + tail},
		{"a charge without its attempt", head + `"charges":[` + strings.Replace(charge, `"attempt":"2026-10-06T17:20:00Z",`, ``, 1) + `],` + tail},
		{"a charge not an object", head + `"charges":[1],` + tail},
		{"clock missing", `{"schema":"dev-env.summon-creates.v1","charges":[],` + tail},
		{"urgentRefusals a string", head + `"charges":[],"urgentRefusals":"0","lastUrgentRefusal":null}`},
		{"urgentRefusals null", head + `"charges":[],"urgentRefusals":null,"lastUrgentRefusal":null}`},
		{"lastUrgentRefusal missing", head + `"charges":[],"urgentRefusals":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := summonLedgerObject()
			cm.Data = map[string]string{ledgerTestKey: tc.raw}
			f := summonFixture(t, summonTestPolicy(), cm)
			// Read without parsing the planted entry.
			_, before, err := readLedgerConfigMap(context.Background(), f.c, "other")
			if err != nil {
				t.Fatal(err)
			}
			version := before.ResourceVersion
			wantError(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusServiceUnavailable, apiv1.CodeUnavailable)
			_, got, err := readLedgerConfigMap(context.Background(), f.c, "other")
			if err != nil {
				t.Fatal(err)
			}
			if got.ResourceVersion != version || got.Data[ledgerTestKey] != tc.raw {
				t.Fatalf("the entry changed: version %s to %s, %q", version, got.ResourceVersion, got.Data[ledgerTestKey])
			}
			if n := len(f.summonedSessions()); n != 0 {
				t.Fatalf("%d sessions", n)
			}
		})
	}
	t.Run("a well-formed entry", func(t *testing.T) {
		cm := summonLedgerObject()
		cm.Data = map[string]string{ledgerTestKey: validLedgerEntry}
		f := summonFixture(t, summonTestPolicy(), cm)
		wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
		if e := f.ledger(); len(e.Charges) != 2 {
			t.Fatalf("charges %+v", e.Charges)
		}
	})
}

// Ruling 5 (review finding 7, MEDIUM): a create refused after its charge, for
// no free name left at the create or the API server refusing the object, is
// a charged failure, and its text says so.
func TestSummonChargedFailures(t *testing.T) {
	t.Run("the last suffix taken during the create", func(t *testing.T) {
		f := summonFixture(t, ledgerPolicy(4, 6))
		body := keyedRemediation("rem-responder-1", "alert-1")
		wantStatus(t, summonPost(t, f, body), http.StatusCreated)
		f.editStatus("rem-responder-1", v1alpha1.PhaseFailed)
		other := func(name string) *v1alpha1.AgentSession {
			return &v1alpha1.AgentSession{
				ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name},
				Spec:       v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: "claude", Mode: "task", Model: "claude-opus-5-5", Prompt: "p", Parent: humanSA},
			}
		}
		for i := 2; i <= 98; i++ {
			if err := f.c.Create(context.Background(), other("rem-responder-1-"+itoa(i))); err != nil {
				t.Fatal(err)
			}
		}
		taken := false
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if s, ok := obj.(*v1alpha1.AgentSession); ok && s.Name == "rem-responder-1-99" && !taken {
				taken = true
				if err := c.Create(ctx, other(s.Name)); err != nil {
					return err
				}
			}
			return c.Create(ctx, obj, opts...)
		}})
		f.now = f.now.Add(time.Minute)
		e := wantError(t, summonPost(t, f, body), http.StatusConflict, apiv1.CodeConflict)
		if !strings.Contains(e.Message, "no refund") || !strings.Contains(e.Message, "normal pruning") || strings.Contains(e.Message, "stays") {
			t.Fatalf("message %q", e.Message)
		}
		if n := len(f.ledger().Charges); n != 2 {
			t.Fatalf("charges %d, want 2: the failure is charged", n)
		}
	})

	t.Run("the API server refuses the object", func(t *testing.T) {
		f := summonFixture(t)
		refused := false
		f.srv.Client = interceptor.NewClient(f.c, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if s, ok := obj.(*v1alpha1.AgentSession); ok && !refused {
				refused = true
				return apierrors.NewInvalid(v1alpha1.GroupVersion.WithKind("AgentSession").GroupKind(), s.Name,
					field.ErrorList{field.Invalid(field.NewPath("spec", "lane"), "remediation", "not in this schema")})
			}
			return c.Create(ctx, obj, opts...)
		}})
		body := keyedRemediation("rem-responder-1", "alert-1")
		e := wantError(t, summonPost(t, f, body), http.StatusUnprocessableEntity, apiv1.CodeInvalid)
		if !strings.Contains(e.Message, "no refund") || !strings.Contains(e.Message, "normal pruning") || strings.Contains(e.Message, "stays") {
			t.Fatalf("message %q", e.Message)
		}
		if n, s := len(f.ledger().Charges), len(f.summonedSessions()); n != 1 || s != 0 {
			t.Fatalf("charges %d sessions %d", n, s)
		}
		// The retry reuses the charge.
		wantStatus(t, summonPost(t, f, body), http.StatusCreated)
		if n := len(f.ledger().Charges); n != 1 {
			t.Fatalf("charges %d after the retry", n)
		}
	})
}

// Ruling 10 (review answer 6, LOW): the count of urgent refusals stops at its
// maximum instead of wrapping.
func TestSummonUrgentRefusalsStopAtTheMaximum(t *testing.T) {
	cm := summonLedgerObject()
	cm.Data = map[string]string{ledgerTestKey: `{"schema":"dev-env.summon-creates.v1","clock":"2026-10-06T17:20:00Z","charges":[],` +
		`"urgentRefusals":` + strconv.FormatInt(math.MaxInt64, 10) + `,"lastUrgentRefusal":"2026-10-06T17:20:00Z"}`}
	f := summonFixture(t, ledgerPolicy(1, 6), cm)
	wantStatus(t, summonPost(t, f, keyedRemediation("rem-responder-1", "alert-1")), http.StatusCreated)
	for i := 0; i < 2; i++ {
		f.now = f.now.Add(time.Minute)
		wantError(t, summonPost(t, f, keyedRemediation("rem-responder-2", "alert-2")), http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
		if e := f.ledger(); e.UrgentRefusals != math.MaxInt64 || e.LastUrgentRefusal == nil || !e.LastUrgentRefusal.Equal(f.now) {
			t.Fatalf("refusal %d: count %d last %v", i, e.UrgentRefusals, e.LastUrgentRefusal)
		}
	}
}

// Ruling 8 (review, administrative deletion): not built against. A session
// an authorized deleter removes inside its charge's hour leaves its charge,
// and the key's next create reuses it: the same id, no second charge.
func TestSummonDeletedSessionReusesItsCharge(t *testing.T) {
	f := summonFixture(t)
	body := keyedRemediation("rem-responder-1", "alert-1")
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	first := f.session("rem-responder-1")
	if err := f.c.Delete(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(10 * time.Minute)
	wantStatus(t, summonPost(t, f, body), http.StatusCreated)
	again := f.session("rem-responder-1")
	if again.UID == first.UID || again.Spec.Summon.DispatchID != first.Spec.Summon.DispatchID {
		t.Fatalf("uid %s id %s, first %s %s", again.UID, again.Spec.Summon.DispatchID, first.UID, first.Spec.Summon.DispatchID)
	}
	if n := len(f.ledger().Charges); n != 1 {
		t.Fatalf("charges %d", n)
	}
}
