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
}

func (c *filingDeadlineHookClock) WithDeadline(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	out, cancel := c.filingTestClock.WithDeadline(ctx, at)
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
		t.Run(kind, func(t *testing.T) { filingForwardStepSchedule(t, kind, 11*time.Minute, true) })
	}
}

func TestSummonFilingForwardStepWithinRemainingMargin(t *testing.T) {
	for _, kind := range []string{"charge", "mark", "rebind", "create"} {
		t.Run(kind, func(t *testing.T) { filingForwardStepSchedule(t, kind, 9*time.Minute, false) })
	}
}

// Preserve the gate review's schedule: after A samples its final local time,
// both wall clocks step together (zero lag), without advancing the injected
// process clock or elapsed delay. B runs before A uses that old sample.
func filingForwardStepSchedule(t *testing.T, kind string, step time.Duration, outsidePromise bool) {
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
		sampled := w.now
		if ready && !stepped {
			stepped = true
			w.now = w.now.Add(step)
			if err := b.summonFile(ctx, w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			if outsidePromise {
				wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
			} else if w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
				t.Fatal("step below the remaining margin abandoned early")
			}
		}
		return sampled
	}
	if kind == "create" {
		w.r.SummonFilingClock = &filingDeadlineHookClock{filingTestClock: filingTestClock{now: w.now}, afterDeadline: func() { ready = true }}
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
	wantLate := 0
	if outsidePromise {
		wantLate = 1
	}
	if !stepped || requests != 1 || afterAbandoned != wantLate {
		t.Fatalf("step=%s: stepped=%v requests=%d after Abandoned=%d, want %d", step, stepped, requests, afterAbandoned, wantLate)
	}
	// Let B reach the bound after the in-bound request. A successful in-bound
	// create is recovered/Filed; ledger-only requests still end Abandoned.
	w.now = bound
	if err := b.summonFile(ctx, w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	stored := w.get(filingRem).Status.Summon
	if !outsidePromise && kind == "create" {
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
		wantChildren = 1
	}
	if got := len(filingChildren(t, w)); got != wantChildren {
		t.Fatalf("children=%d, want %d", got, wantChildren)
	}
}
