package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Reproduce the review's barrier AFTER the last uncached Pending read, with
// B one second ahead. With the guard that read is unreachable; B then runs
// after A's refused pass. On unfixed code B runs inside A's read instead.
func TestSummonFilingGuardAfterFinalRead(t *testing.T) {
	for _, kind := range []string{"create", "charge", "mark", "rebind"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			if kind == "mark" || kind == "rebind" {
				seedAbandonCharge(t, w, summonT0)
			}
			w.now = summonT0.Add(24*time.Hour - time.Second)
			b := filingLeader(w, interceptor.Funcs{})
			b.Now = func() time.Time { return summonT0.Add(24 * time.Hour) }
			writes, creates, reads, barriers := 0, 0, 0, 0
			w.r = filingLeader(w, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					writes++
					return c.Update(ctx, o, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					creates++
					if ctx.Err() != nil {
						t.Fatal("create probe used a canceled context")
					}
					if w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationAbandoned {
						t.Fatal("create barrier did not settle first")
					}
					return c.Create(ctx, o, opts...)
				},
			})
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, o, opts...); err != nil {
					return err
				}
				if s, ok := o.(*v1alpha1.AgentSession); ok && s.Name == filingRem {
					reads++
					final := kind == "create" && writes == 2 || kind != "create" && reads == 2
					if final && barriers == 0 {
						barriers++
						if err := b.SummonAdmissionPass(ctx); err != nil {
							t.Fatal(err)
						}
					}
				}
				return nil
			}})
			var err error
			if kind == "create" {
				err = w.r.summonFile(context.Background(), w.get(filingRem))
			} else {
				action := filingCharge
				if kind == "mark" {
					action = filingMark
				}
				if kind == "rebind" {
					action = filingRebind
				}
				result, e := w.r.filingLedgerStep(context.Background(), w.get(filingRem), action, summonoutcome.EscalationName(w.get(filingRem)))
				err = e
				if !result.done {
					t.Errorf("%s continued after guard: done=false", kind)
				}
			}
			// A stale Filed status patch can conflict on the unfixed create path;
			// report the invariant itself, not that expected conflict, as the failure.
			if err != nil && !filingOnlyStaleErrors(err) {
				t.Fatal(err)
			}
			if barriers == 0 {
				if err := b.SummonAdmissionPass(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			wantAbandoned(t, w, summonoutcome.FilingNotFiled, summonT0.Add(24*time.Hour))
			if writes != 0 || creates != 0 || len(filingChildren(t, w)) != 0 {
				t.Fatalf("%s escaped guard: ledger writes=%d creates=%d children=%d final=%s", kind, writes, creates, len(filingChildren(t, w)), w.get(filingRem).Status.Summon.Escalation.State)
			}
		})
	}
}

func TestSummonFilingGuardPolicyReturn(t *testing.T) {
	for _, remaining := range []time.Duration{11 * time.Minute, 10 * time.Minute, 9 * time.Minute} {
		t.Run(remaining.String(), func(t *testing.T) {
			w := newFilingWorld(t)
			if err := w.base.Delete(context.Background(), filingPolicy()); err != nil {
				t.Fatal(err)
			}
			w.pass()
			filingBlocked(t, w, summonoutcome.FilingPolicyGone)
			before := w.get(filingRem).ResourceVersion
			w.now = summonT0.Add(24*time.Hour - remaining)
			w.add(filingPolicy())
			w.pass()
			if remaining > 10*time.Minute {
				assertOneFiling(t, w, w.now)
				return
			}
			if w.get(filingRem).ResourceVersion != before {
				t.Fatal("guard window changed the last stored blocked word or filing status")
			}
			filingBlocked(t, w, summonoutcome.FilingPolicyGone)
			e, _ := filingLedger(t, w)
			if len(e.Charges) != 0 {
				t.Fatal("policy restored inside guard charged")
			}
			w.now = summonT0.Add(24 * time.Hour)
			w.pass()
			wantAbandoned(t, w, summonoutcome.FilingPolicyGone, w.now)
		})
	}
}

func TestSummonFilingGuardRecoversLabelledChild(t *testing.T) {
	w := newFilingWorld(t)
	seedAbandonCharge(t, w, summonT0)
	child := abandonChild(t, w)
	w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
	// Even without a policy, the label is sufficient to recover Filed.
	w.r.SummonCreateKinds = nil
	w.r.Client = interceptor.NewClient(w.base, interceptor.Funcs{
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			t.Fatal("recovery wrote a ledger")
			return nil
		},
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			t.Fatal("recovery created a child")
			return nil
		},
	})
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	if st := w.get(filingRem).Status.Summon.Escalation; st.State != v1alpha1.SummonEscalationFiled || st.UID != string(child.UID) {
		t.Fatalf("guard lost labelled child: %+v", st)
	}
}

// Slow reads must also respect the guard when the pass started outside it.
func TestSummonFilingGuardAfterDependencyRead(t *testing.T) {
	for _, action := range []filingLedgerAction{filingCharge, filingMark, filingRebind} {
		t.Run(fmt.Sprint(action), func(t *testing.T) {
			w := newFilingWorld(t)
			if action != filingCharge {
				seedAbandonCharge(t, w, summonT0)
			}
			w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
			writes, barriers := 0, 0
			w.r = filingLeader(w, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
				writes++
				return c.Update(ctx, o, opts...)
			}})
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, o client.ObjectList, opts ...client.ListOption) error {
				if err := c.List(ctx, o, opts...); err != nil {
					return err
				}
				barriers++
				w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
				return nil
			}})
			result, err := w.r.filingLedgerStep(context.Background(), w.get(filingRem), action, summonoutcome.EscalationName(w.get(filingRem)))
			if err != nil || barriers != 1 || !result.done || writes != 0 {
				t.Fatalf("dependency read crossed guard: barriers=%d done=%v writes=%d err=%v", barriers, result.done, writes, err)
			}
		})
	}
}

// Entering the guard after the mark is caught by the Pending-read check.
// TestSummonFilingGuardAfterDeadlineConstruction isolates the later check.
func TestSummonFilingGuardAfterMark(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
	writes, creates := 0, 0
	w.r = filingLeader(w, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if err := c.Update(ctx, o, opts...); err != nil {
				return err
			}
			if _, ok := o.(*corev1.ConfigMap); ok {
				writes++
				if writes == 2 {
					w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
				}
			}
			return nil
		},
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			creates++
			return c.Create(ctx, o, opts...)
		},
	})
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	if writes != 2 || creates != 0 || len(filingChildren(t, w)) != 0 {
		t.Fatalf("mark crossed guard: writes=%d creates=%d", writes, creates)
	}
}

func TestSummonFilingGuardPreservesWordAfterSlowRetryRead(t *testing.T) {
	w := newFilingWorld(t)
	seedAbandonCharge(t, w, summonT0)
	if err := w.r.blockSummonFiling(context.Background(), w.get(filingRem), summonoutcome.FilingCreateRefused); err != nil {
		t.Fatal(err)
	}
	before := w.get(filingRem).ResourceVersion
	w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
	w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, o, opts...); err != nil {
			return err
		}
		if _, ok := o.(*corev1.ConfigMap); ok {
			w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
		}
		return nil
	}})
	if err := w.r.recordSummonFilingBlock(context.Background(), w.get(filingRem), summonoutcome.FilingPolicyGone, ""); err != nil {
		t.Fatal(err)
	}
	if live := w.get(filingRem); live.ResourceVersion != before || live.Status.Summon.Escalation.Reason != summonoutcome.FilingCreateRefused {
		t.Fatal("slow retry read replaced the last blocked word inside guard")
	}
}

func TestSummonFilingGuardAfterCharge(t *testing.T) {
	w := newFilingWorld(t)
	before := w.get(filingRem).ResourceVersion
	w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
	writes := 0
	w.r = filingLeader(w, interceptor.Funcs{Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
		if err := c.Update(ctx, o, opts...); err != nil {
			return err
		}
		writes++
		w.now = summonT0.Add(24*time.Hour - 9*time.Minute)
		return nil
	}})
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	if writes != 1 || w.get(filingRem).ResourceVersion != before || len(filingChildren(t, w)) != 0 {
		t.Fatal("charge response crossed guard but started another filing write")
	}
}
