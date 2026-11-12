package apiserver

// The schedules of the short re-check of PR 1085 and the coordinator's
// rulings on it (story 758, fix round 3; PR comments 6813721839 and
// 6813756080). Each schedule is driven by client hooks and the fake clocks,
// never by goroutine timing, sleeps or loops for time.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Ruling 1 (re-check R3-1). X is charged at t=0 and its create fails. A marks
// at 64:59. B's clock is five minutes ahead. Both clocks then take one common
// forward step of five minutes while monotonic time advances one second, so
// pairwise skew stays five minutes and neither forward step exceeds five
// minutes. B sees X's attempt as 10 minutes and 1 second old and, with the
// old 10 minute hold, prunes X while charging Y. A's deadline is still live.
// The 15 minute hold covers that sum: the charge is not pruned, and A creates
// the session with its charge in the ledger.
func TestSummonCombinedClocksDoNotPruneALiveAttempt(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 6))
	t0 := f.now
	const markAt = 64*time.Minute + 59*time.Second
	body := keyedRemediation("rem-responder-1", "alert-1")
	x := crashFirstCreate(t, f, body)

	clock := newFakeSummonClock()
	b := replicaAt(f, f.c, 5*time.Minute)
	var bw *httptest.ResponseRecorder
	stepped := false
	clock.pauseAfterCheck = func() {
		stepped = true
		clock.Advance(time.Second)
		// A's wall clock: the mark, plus the one second of real time, plus the
		// five minute forward step. B stays five minutes ahead of that.
		f.now = t0.Add(markAt + 5*time.Minute + time.Second)
		bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
	}
	a := replica(f, f.c)
	a.SummonClock = clock
	f.now = t0.Add(markAt)
	aw := post(t, a.Handler(), tokStranger, body)
	if !stepped {
		t.Fatal("the schedule did not run: A was never paused")
	}
	wantStatus(t, bw, http.StatusCreated)
	wantStatus(t, aw, http.StatusCreated)
	ch, ok := f.ledger().charge(x)
	if !ok {
		t.Fatalf("X was pruned: %+v", f.ledger().Charges)
	}
	if age := f.ledger().Clock.Sub(ch.Attempt); age != 10*time.Minute+time.Second {
		t.Fatalf("attempt age %s, want 10m1s (mark %s, clock %s)", age, ch.Attempt, f.ledger().Clock)
	}
	sess := f.session("rem-responder-1")
	if sess.Spec.Summon.DispatchID != x || ch.Name != sess.Name || ch.ID != x {
		t.Fatalf("session %s id %s, charge %+v", sess.Name, sess.Spec.Summon.DispatchID, ch)
	}
}

// Ruling 2 (re-check R3-2). X is charged at t=0 and its create fails. A marks
// at 64:59 and is held before its create. B creates X under the same name,
// then at 65:01 charges Y. With the old rule that write prunes X because the
// session exists, without looking at the attempt mark (2 seconds old). An
// authorized deleter removes the session. A resumes. The hold applies to
// every charge, so A's session is created with its charge in the ledger.
func TestSummonSettledChargeKeepsALiveAttempt(t *testing.T) {
	const n = "rem-responder-1"
	f := summonFixture(t, ledgerPolicy(4, 6))
	t0 := f.now
	body := keyedRemediation(n, "alert-1")
	x := crashFirstCreate(t, f, body)

	b := replica(f, f.c)
	var created, charged *httptest.ResponseRecorder
	ran := false
	a := replica(f, holdSessionCreate(f, func() {
		ran = true
		created = post(t, b.Handler(), tokStranger, body)
		f.now = t0.Add(65*time.Minute + time.Second)
		charged = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
		if err := f.c.Delete(context.Background(), f.session(n)); err != nil {
			t.Fatal(err)
		}
	}))
	f.now = t0.Add(64*time.Minute + 59*time.Second)
	aw := post(t, a.Handler(), tokStranger, body)
	if !ran {
		t.Fatal("the schedule did not run: A was never held")
	}
	wantStatus(t, created, http.StatusCreated)
	wantStatus(t, charged, http.StatusCreated)
	wantStatus(t, aw, http.StatusCreated)
	ch, ok := f.ledger().charge(x)
	if !ok {
		t.Fatalf("X was pruned while its attempt was live: %+v", f.ledger().Charges)
	}
	if want := t0.Add(64*time.Minute + 59*time.Second); !ch.Attempt.Equal(want) {
		t.Fatalf("attempt %s, want the mark at %s", ch.Attempt, want)
	}
	sess := f.session(n)
	if sess.Spec.Summon.DispatchID != x || ch.Name != sess.Name {
		t.Fatalf("session id %s, charge %+v", sess.Spec.Summon.DispatchID, ch)
	}
	if got := sessionsOfID(f, x); len(got) != 1 || got[0] != n {
		t.Fatalf("sessions of X %v", got)
	}
}

// Ruling 3 (re-check answer 2). A name clash rebinds N to N-2. Two advances of
// 66 minutes, each followed by another replica's write, prune that charge and
// a new charge of the same dispatch is created at N again: the binding goes
// N -> N-2 -> N -> N-2 -> N. The same cycle can repeat for as long as the
// request runs. One request stops at 200 create attempts and answers 503, a
// charged failure.
func TestSummonCreateAttemptsStopAtTheCap(t *testing.T) {
	const n, n2, yName = "rem-responder-1", "rem-responder-1-2", "rem-responder-9"
	const cap = 200
	f := summonFixture(t, ledgerPolicy(4, 6))
	body := keyedRemediation(n, "alert-1")
	yBody := keyedRemediation(yName, "alert-y")
	x := crashFirstCreate(t, f, body)
	b := replica(f, f.c)

	creates := 0
	cycling := false
	seq := []string{n}
	a := replica(f, interceptor.NewClient(f.c, interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			s, ok := obj.(*v1alpha1.AgentSession)
			if !ok || s.Spec.Summon == nil {
				return c.Create(ctx, obj, opts...)
			}
			creates++
			// Past the cap the old code has not stopped. Let this one create
			// succeed so the request ends and the missing cap is visible.
			if creates > cap {
				return c.Create(ctx, obj, opts...)
			}
			if err := c.Create(ctx, otherSession(s.Name)); err != nil {
				return err
			}
			return c.Create(ctx, obj, opts...)
		},
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, obj, opts...)
			if err != nil || cycling {
				return err
			}
			cm, ok := obj.(*corev1.ConfigMap)
			if !ok || cm.Name != ledgerTestName {
				return err
			}
			ch, found := f.ledger().charge(x)
			if !found || ch.Name != n2 {
				return err
			}
			cycling = true
			seq = append(seq, n2)
			f.now = f.now.Add(66 * time.Minute)
			if del := f.c.Delete(context.Background(), otherSession(n)); del != nil {
				t.Fatal(del)
			}
			yw := post(t, b.Handler(), tokStranger, yBody)
			if yw.Code != http.StatusCreated {
				t.Fatalf("Y: %d %s", yw.Code, yw.Body.String())
			}
			if del := f.c.Delete(context.Background(), f.session(yName)); del != nil {
				t.Fatal(del)
			}
			xw := post(t, b.Handler(), tokStranger, body)
			if xw.Code != http.StatusCreated {
				t.Fatalf("recreate X: %d %s; ledger %+v", xw.Code, xw.Body.String(), f.ledger().Charges)
			}
			if del := f.c.Delete(context.Background(), f.session(n)); del != nil {
				t.Fatal(del)
			}
			if got, _ := f.ledger().charge(x); got.Name != n {
				t.Fatalf("after the recreate the binding is %q, want %s", got.Name, n)
			}
			seq = append(seq, n)
			cycling = false
			return err
		},
	}))
	aw := post(t, a.Handler(), tokStranger, body)
	want := []string{n, n2, n, n2, n}
	if len(seq) < len(want) {
		t.Fatalf("binding sequence %v, want a prefix %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("binding sequence %v, want a prefix %v", seq[:len(want)], want)
		}
	}
	if aw.Code != http.StatusServiceUnavailable || creates != cap {
		t.Fatalf("status %d creates %d, want 503 and %d; body %s", aw.Code, creates, cap, aw.Body.String())
	}
	e := wantError(t, aw, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if strings.Contains(e.Message, "stays") || !strings.Contains(e.Message, "no refund") || !strings.Contains(e.Message, "normal pruning") {
		t.Fatalf("message %q", e.Message)
	}
	if _, ok := f.ledger().charge(x); !ok {
		t.Fatalf("the charged failure dropped X: %+v", f.ledger().Charges)
	}
	if got := sessionsOfID(f, x); len(got) != 0 {
		t.Fatalf("sessions of X %v", got)
	}
}

// Ruling 4 (re-check R3-3). X is charged at t=0 and its create fails. A binds
// again at 64:59. B charges Y at 65:01, filling the hour. A's mark runs at
// 80:00, when the attempt is 15 minutes and 1 second old, so the refusal's
// own write prunes X. The 429 must not say the charge stays: there is no
// refund, normal pruning applies, and the hour is charges in the 65 minute
// window by the entry's clock.
//
// The re-check's mark was at 75:00, 10 minutes and 1 second after the bind,
// which matched the old 10 minute hold. 80:00 is that same gap past the 15
// minute hold, so this write still prunes.
func TestSummonRefusalDoesNotClaimTheChargeStays(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 1))
	t0 := f.now
	body := keyedRemediation("rem-responder-1", "alert-1")
	x := crashFirstCreate(t, f, body)

	b := replica(f, f.c)
	var bw *httptest.ResponseRecorder
	a := replica(f, afterFirstLedgerWrite(f, func() {
		f.now = t0.Add(65*time.Minute + time.Second)
		bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
		f.now = t0.Add(80 * time.Minute)
	}))
	f.now = t0.Add(64*time.Minute + 59*time.Second)
	aw := post(t, a.Handler(), tokStranger, body)
	wantStatus(t, bw, http.StatusCreated)
	e := wantError(t, aw, http.StatusTooManyRequests, apiv1.CodeLimitExceeded)
	if strings.Contains(e.Message, "stays") || strings.Contains(e.Message, "creates in the last hour") {
		t.Fatalf("the refusal claims something the ledger does not: %q", e.Message)
	}
	if !strings.Contains(e.Message, "no refund") || !strings.Contains(e.Message, "normal pruning") || !strings.Contains(e.Message, "charges") || !strings.Contains(e.Message, "65 minute") {
		t.Fatalf("message %q", e.Message)
	}
	if _, ok := f.ledger().charge(x); ok {
		t.Fatalf("X is still charged, so this write did not prune it: %+v", f.ledger().Charges)
	}
	if e := f.ledger(); e.UrgentRefusals != 1 {
		t.Fatalf("urgent refusals %d", e.UrgentRefusals)
	}
	if got := sessionsOfID(f, x); len(got) != 0 {
		t.Fatalf("sessions of X %v", got)
	}
}

// Ruling 4, the charge-gone 503. The refusal's own later write is not this
// case: the charge has already left, and the 503 carries no retention sentence.
func TestSummonChargeGoneCarriesNoRetentionSentence(t *testing.T) {
	f := summonFixture(t, ledgerPolicy(4, 6))
	body := keyedRemediation("rem-responder-1", "alert-1")
	x := summonDispatchID(summonTestAccount, "alert-1", nil)
	b := replica(f, f.c)
	var bw *httptest.ResponseRecorder
	a := replica(f, afterFirstLedgerWrite(f, func() {
		f.now = f.now.Add(66 * time.Minute)
		bw = post(t, b.Handler(), tokStranger, keyedRemediation("rem-responder-2", "alert-2"))
	}))
	aw := post(t, a.Handler(), tokStranger, body)
	wantStatus(t, bw, http.StatusCreated)
	e := wantError(t, aw, http.StatusServiceUnavailable, apiv1.CodeUnavailable)
	if strings.Contains(e.Message, "stays") || strings.Contains(e.Message, "refund") || strings.Contains(e.Message, "pruning") {
		t.Fatalf("retention sentence on a charge that has left: %q", e.Message)
	}
	if !strings.Contains(e.Message, "left the ledger") {
		t.Fatalf("message %q", e.Message)
	}
	if _, ok := f.ledger().charge(x); ok {
		t.Fatalf("X is still charged: %+v", f.ledger().Charges)
	}
	if got := sessionsOfID(f, x); len(got) != 0 {
		t.Fatalf("sessions of X %v", got)
	}
}
