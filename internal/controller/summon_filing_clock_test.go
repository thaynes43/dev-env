package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

type filingDeadlineHookClock struct {
	filingTestClock
	afterDeadline func()
	deadlineCtx   context.Context
}

func (c *filingDeadlineHookClock) WithDeadline(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	out, cancel := c.filingTestClock.WithDeadline(ctx, at)
	c.deadlineCtx = out
	c.afterDeadline()
	return out, cancel
}

// The earlier Pending-read check sees 23h49m. Only the last check, after
// deadline construction, sees 23h51m. Removing that last check must fail here.
func TestSummonFilingGuardAfterDeadlineConstruction(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
	creates, deadlines := 0, 0
	w.r = filingLeader(w, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		creates++
		return c.Create(ctx, o, opts...)
	}})
	w.r.SummonFilingClock = &filingDeadlineHookClock{filingTestClock: filingTestClock{now: w.now}, afterDeadline: func() {
		deadlines++
		w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
	}}
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	if deadlines != 1 || creates != 0 || len(filingChildren(t, w)) != 0 {
		t.Fatalf("last local check after deadline construction: deadlines=%d creates=%d children=%d", deadlines, creates, len(filingChildren(t, w)))
	}
	if w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
		t.Fatal("refused create changed Pending")
	}
}

func TestSummonFilingForwardStepBeyondMarginIsOutsidePromise(t *testing.T) {
	for _, kind := range []string{"charge", "mark", "rebind", "create"} {
		t.Run(kind, func(t *testing.T) {
			filingForwardStepSchedule(t, kind, filingStepSchedule{lastRemaining: 11 * time.Minute, step: 11 * time.Minute, requests: 1, afterAbandoned: 1})
		})
	}
}

func TestSummonFilingForwardStepWithinRemainingMargin(t *testing.T) {
	for _, kind := range []string{"charge", "mark", "rebind", "create"} {
		t.Run(kind, func(t *testing.T) {
			filingForwardStepSchedule(t, kind, filingStepSchedule{lastRemaining: 11 * time.Minute, step: 9 * time.Minute, requests: 1})
		})
	}
}

// Earlier checks see 23h49m. Only the final check sees the guard boundary or
// its first thirty seconds; a nine-minute final guard must fail both cases.
func TestSummonFilingLastCheckTenMinuteMargin(t *testing.T) {
	for _, kind := range []string{"charge", "mark", "rebind", "create"} {
		t.Run(kind, func(t *testing.T) {
			for _, remaining := range []time.Duration{10 * time.Minute, 9*time.Minute + 30*time.Second} {
				t.Run(remaining.String(), func(t *testing.T) {
					filingForwardStepSchedule(t, kind, filingStepSchedule{lastRemaining: remaining})
				})
			}
		})
	}
}

// The last sample is 23h50m30s, then both wall clocks step 9m30s with zero
// replica lag or process-clock advancement. B stores Abandoned before A
// resumes. The original final guard refuses; a nine-minute guard permits A.
func TestSummonFilingLastCheckForwardStepWithinMargin(t *testing.T) {
	for _, kind := range []string{"charge", "mark", "rebind", "create"} {
		t.Run(kind, func(t *testing.T) {
			filingForwardStepSchedule(t, kind, filingStepSchedule{
				lastRemaining: 9*time.Minute + 30*time.Second,
				step:          9*time.Minute + 30*time.Second,
			})
		})
	}
}

type filingStepSchedule struct {
	lastRemaining  time.Duration
	step           time.Duration
	requests       int
	afterAbandoned int
}

// Preserve the gate review's schedule: after A samples its final local time,
// both wall clocks step together (zero lag), without advancing the injected
// process clock or elapsed delay. B runs before A uses that old sample.
func filingForwardStepSchedule(t *testing.T, kind string, schedule filingStepSchedule) {
	t.Helper()
	ctx := context.Background()
	w := newFilingWorld(t)
	if kind == "mark" || kind == "rebind" {
		seedAbandonCharge(t, w, summonT0)
	}
	bound := summonT0.Add(24 * time.Hour)
	w.now = bound.Add(-11 * time.Minute)
	b := filingLeader(w, interceptor.Funcs{})
	// B may recover/abandon existing records, but must not start its own attempt.
	b.SummonCreateKinds = nil
	ready, stepped := false, false
	var deadlineClock *filingDeadlineHookClock
	requests, afterAbandoned := 0, 0
	requestStart := func() {
		t.Helper()
		if !stepped {
			t.Fatal("request ran before the final-sample clock step")
		}
		requests++
		state := w.get(filingRem).Status.Summon.Escalation.State
		if state == v1alpha1.SummonEscalationAbandoned {
			afterAbandoned++
		} else if state != v1alpha1.SummonEscalationPending || !w.now.Before(bound) {
			t.Fatalf("in-bound request did not precede abandonment: state=%s time=%s", state, w.now)
		}
	}
	w.r = filingLeader(w, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.ConfigMap); ok && stepped {
				requestStart()
			}
			return c.Update(ctx, o, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			requestStart()
			if ctx.Err() != nil {
				t.Fatal("clock step canceled the process-clock deadline")
			}
			return c.Create(ctx, o, opts...)
		},
	})
	w.r.Now = func() time.Time {
		if ready && !stepped {
			w.now = bound.Add(-schedule.lastRemaining)
			sampled := w.now
			stepped = true
			w.now = w.now.Add(schedule.step)
			if deadlineClock != nil && deadlineClock.deadlineCtx.Err() != nil {
				t.Fatal("final check did not have a live process-clock deadline")
			}
			if err := b.summonFile(ctx, w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			if !w.now.Before(bound) {
				wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
			} else if w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
				t.Fatal("step below the remaining margin abandoned early")
			}
			return sampled
		}
		return w.now
	}
	if kind == "create" {
		deadlineClock = &filingDeadlineHookClock{filingTestClock: filingTestClock{now: w.now}, afterDeadline: func() { ready = true }}
		w.r.SummonFilingClock = deadlineClock
	} else {
		reads := 0
		w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
			if err := c.Get(ctx, key, o, opts...); err != nil {
				return err
			}
			if s, ok := o.(*v1alpha1.AgentSession); ok && s.Name == filingRem {
				reads++
				if reads == 2 {
					ready = true
				}
			}
			return nil
		}})
	}
	var err error
	if kind == "create" {
		err = w.r.summonFile(ctx, w.get(filingRem))
	} else {
		action := filingCharge
		switch kind {
		case "mark":
			action = filingMark
		case "rebind":
			action = filingRebind
		}
		_, err = w.r.filingLedgerStep(ctx, w.get(filingRem), action, summonoutcome.EscalationName(w.get(filingRem)))
	}
	if err != nil && !filingOnlyStaleErrors(err) {
		t.Fatal(err)
	}
	if !stepped || requests != schedule.requests || afterAbandoned != schedule.afterAbandoned {
		t.Fatalf("remaining=%s step=%s: stepped=%v requests=%d after Abandoned=%d, want requests=%d after Abandoned=%d", schedule.lastRemaining, schedule.step, stepped, requests, afterAbandoned, schedule.requests, schedule.afterAbandoned)
	}
	// Let B reach the bound after the in-bound request. A successful in-bound
	// create is recovered/Filed; ledger-only requests still end Abandoned.
	w.now = bound
	if err := b.summonFile(ctx, w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	stored := w.get(filingRem).Status.Summon
	if schedule.requests == 1 && schedule.afterAbandoned == 0 && kind == "create" {
		if stored.Escalation.State != v1alpha1.SummonEscalationFiled {
			t.Fatal("in-bound child was not Filed")
		}
		for _, p := range stored.Pages {
			if p.Event == v1alpha1.PageEscalationAbandoned {
				t.Fatal("Filed also has an Abandoned event")
			}
		}
	} else {
		wantAbandoned(t, w, summonoutcome.FilingNotFiled, bound)
		if stored.Escalation.UID != "" {
			t.Fatal("Abandoned also has a Filed UID")
		}
	}
	wantChildren := 0
	if kind == "create" {
		wantChildren = schedule.requests
	}
	if got := len(filingChildren(t, w)); got != wantChildren {
		t.Fatalf("children=%d, want %d", got, wantChildren)
	}
}
