package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// P2: a recorded refusal renews its queue place at each retry boundary.
// These are full leader passes one fake minute apart, with a fresh leader.
func TestSummonFilingRecordedRefusalQueueHold(t *testing.T) {
	w := newFilingWorld(t)
	writes, creates, countedBefore, countedAfter := 0, 0, 0, 0
	refusal := apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, "fixture", field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	hooks := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, o, opts...)
			if _, ok := o.(*corev1.ConfigMap); ok && err == nil {
				writes++
			}
			return err
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			creates++
			return refusal
		},
	}
	q := summonledger.Queue{Urgent: true, Escalations: true}
	count := func(p *v1alpha1.CallerPolicy, q summonledger.Queue) int {
		e, _ := filingLedger(t, w)
		var sessions v1alpha1.AgentSessionList
		if err := w.base.List(context.Background(), &sessions); err != nil {
			t.Fatal(err)
		}
		byID := map[string]*v1alpha1.AgentSession{}
		for i := range sessions.Items {
			s := &sessions.Items[i]
			if s.Spec.Summon != nil {
				byID[s.Spec.Summon.DispatchID] = s
			}
		}
		return summonledger.QueueCount(callerA, p, e.Charges, sessions.Items, byID, summonledger.Grants{}, q, "", w.now)
	}
	for minute := 0; minute < 46; minute++ {
		w.now = summonT0.Add(time.Duration(minute) * time.Minute)
		before := count(filingPolicy(), q)
		wantBefore := 1
		if minute%15 == 0 {
			wantBefore = 0
		}
		if before != wantBefore {
			t.Fatalf("minute %d before pass: queue count=%d, want %d", minute, before, wantBefore)
		}
		countedBefore += before
		w.r = filingLeader(w, hooks)
		err := w.r.SummonAdmissionPass(context.Background())
		filingBlocked(t, w, summonoutcome.FilingCreateRefused)
		after := count(filingPolicy(), q)
		if after != 1 {
			t.Fatalf("minute %d after pass: queue count=%d, want 1", minute, after)
		}
		countedAfter += after
		if minute%15 == 0 && !errors.Is(err, refusal) || minute%15 != 0 && err != nil {
			t.Fatalf("minute %d pass: %v", minute, err)
		}
	}
	if creates != 4 || writes != 5 || countedBefore != 42 || countedAfter != 46 {
		t.Fatalf("46 passes: creates=%d writes=%d countedBefore=%d countedAfter=%d", creates, writes, countedBefore, countedAfter)
	}
	e, _ := filingLedger(t, w)
	if len(e.Charges) != 1 || !e.Charges[0].At.Equal(summonT0) || !e.Charges[0].Attempt.Equal(summonT0.Add(45*time.Minute)) {
		t.Fatalf("renewal changed charge identity/time: %+v", e.Charges)
	}
	// Without a policy entry that names the charge hash, it counts in every queue.
	p := filingPolicy()
	p.Spec.Summon.Sessions = nil
	for _, q := range summonledger.AllQueues() {
		if n := count(p, q); n != 1 {
			t.Fatalf("unmatched hash in queue %+v: count=%d, want 1", q, n)
		}
	}
}

// P3: removal of the recorded refusal's charge names the lost evidence once;
// later passes stay blocked without charging or returning that error forever.
func TestSummonFilingRecordedRefusalMissingCharge(t *testing.T) {
	for _, reason := range []v1alpha1.SummonFilingReason{summonoutcome.FilingCreateRefused, summonoutcome.FilingNoFreeName} {
		t.Run(string(reason), func(t *testing.T) {
			w := newFilingWorld(t)
			creates := 0
			hooks := interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
				creates++
				return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
			}}
			w.r = filingLeader(w, hooks)
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			s := w.get(filingRem)
			s.Status.Summon.Escalation.Reason = reason
			if err := w.base.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			_, cm := filingLedger(t, w)
			delete(cm.Data, callerA) // externally removed evidence; chargedAt survives
			if err := w.base.Update(context.Background(), cm); err != nil {
				t.Fatal(err)
			}
			for i, minute := range []int{1, 16, 60} {
				w.now = summonT0.Add(time.Duration(minute) * time.Minute)
				w.r = filingLeader(w, hooks)
				err := w.r.summonFile(context.Background(), w.get(filingRem))
				filingBlocked(t, w, summonoutcome.FilingLedgerUnavailable)
				if i > 0 && err != nil {
					t.Fatalf("minute %d repeated lost-charge error: %v", minute, err)
				}
				e, after := filingLedger(t, w)
				if len(e.Charges) != 0 || creates != 1 || after.ResourceVersion != cm.ResourceVersion {
					t.Fatalf("minute %d recharged or retried: charges=%d creates=%d", minute, len(e.Charges), creates)
				}
			}
		})
	}
}

func TestSummonFilingUnknownCreateRetriesNextPass(t *testing.T) {
	for _, kind := range []string{"timeout", "server-error"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			creates, writes := 0, 0
			var cause error
			if kind == "timeout" {
				cause = apierrors.NewTimeoutError("fixture timeout", 0)
			} else {
				cause = &apierrors.StatusError{ErrStatus: metav1.Status{Code: 500}}
			}
			hooks := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					err := c.Update(ctx, o, opts...)
					if _, ok := o.(*corev1.ConfigMap); ok && err == nil {
						writes++
					}
					return err
				},
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					creates++
					return cause
				},
			}
			for minute := 0; minute < 2; minute++ {
				w.now = summonT0.Add(time.Duration(minute) * time.Minute)
				w.r = filingLeader(w, hooks)
				if err := w.r.summonFile(context.Background(), w.get(filingRem)); !errors.Is(err, cause) {
					t.Fatalf("minute %d lost create cause: %v", minute, err)
				}
				filingBlocked(t, w, summonoutcome.FilingLedgerUnavailable)
			}
			e, _ := filingLedger(t, w)
			if creates != 2 || writes != 3 || len(e.Charges) != 1 || !e.Charges[0].At.Equal(summonT0) || !e.Charges[0].Attempt.Equal(w.now) {
				t.Fatalf("unknown result not retried next pass: creates=%d writes=%d charges=%+v", creates, writes, e.Charges)
			}
		})
	}
}

// L2's allowed text-only choice: a late collision after a recorded refusal
// consumes this retry window; the next window recovers the rebound name.
func TestSummonFilingLateCollisionRetryWaitsNextWindow(t *testing.T) {
	w := newFilingWorld(t)
	writes, creates := 0, 0
	refuse, collide := true, true
	refusal := apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, "fixture", field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	hooks := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			err := c.Update(ctx, o, opts...)
			if _, ok := o.(*corev1.ConfigMap); ok && err == nil {
				writes++
			}
			return err
		},
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			creates++
			if refuse {
				return refusal
			}
			if collide {
				collide = false
				w.add(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: o.GetName(), Namespace: o.GetNamespace(), UID: "late-occupant"}})
			}
			return c.Create(ctx, o, opts...)
		},
	}
	w.r = filingLeader(w, hooks)
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); !errors.Is(err, refusal) {
		t.Fatal(err)
	}
	filingBlocked(t, w, summonoutcome.FilingCreateRefused)
	version := w.get(filingRem).ResourceVersion
	refuse = false
	for _, minute := range []int{15, 16} {
		w.now = summonT0.Add(time.Duration(minute) * time.Minute)
		w.r = filingLeader(w, hooks)
		if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
			t.Fatal(err)
		}
		filingBlocked(t, w, summonoutcome.FilingCreateRefused)
		e, _ := filingLedger(t, w)
		if creates != 2 || writes != 4 || len(filingChildren(t, w)) != 0 || w.get(filingRem).ResourceVersion != version ||
			len(e.Charges) != 1 || e.Charges[0].Name == summonoutcome.EscalationName(w.get(filingRem)) || !e.Charges[0].Attempt.Equal(summonT0.Add(summonledger.AttemptHold)) {
			t.Fatalf("minute %d collision delay: creates=%d writes=%d charges=%+v", minute, creates, writes, e.Charges)
		}
	}
	w.now = summonT0.Add(2 * summonledger.AttemptHold)
	w.r = filingLeader(w, hooks)
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	assertOneFiling(t, w, summonT0)
	if creates != 3 || writes != 5 {
		t.Fatalf("next-window recovery: creates=%d writes=%d", creates, writes)
	}
}
