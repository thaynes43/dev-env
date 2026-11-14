package controller

import (
	"context"
	"errors"
	"strings"
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

func TestSummonFilingRefusalAttemptWindow(t *testing.T) {
	for _, kind := range []string{"invalid", "forbidden", "no-free-name"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			reason := v1alpha1.SummonFilingReason("create-refused")
			if kind == "no-free-name" {
				reason = summonoutcome.FilingNoFreeName
				for _, name := range summonledger.NameSequence(summonoutcome.EscalationName(w.get(filingRem))) {
					w.add(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents"}})
				}
			}
			writes, creates := 0, 0
			recoverCreate := false
			hooks := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					if _, ok := o.(*corev1.ConfigMap); ok {
						writes++
					}
					return c.Update(ctx, o, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					creates++
					if recoverCreate {
						return c.Create(ctx, o, opts...)
					}
					if kind == "forbidden" {
						return apierrors.NewForbidden(schema.GroupResource{Resource: "agentsessions"}, o.GetName(), errors.New("admission webhook denied fixture"))
					}
					return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Invalid(field.NewPath("spec", "profile"), "fixture", "refused fixture")})
				},
			}
			for i, elapsed := range []time.Duration{0, time.Minute, summonledger.AttemptHold - time.Nanosecond, summonledger.AttemptHold, summonledger.AttemptHold + time.Minute} {
				w.now = summonT0.Add(elapsed)
				w.r = filingLeader(w, hooks) // no process-local retry state
				before := w.get(filingRem).ResourceVersion
				_ = w.r.summonFile(context.Background(), w.get(filingRem))
				filingBlocked(t, w, reason)
				attempts := 1
				if elapsed >= summonledger.AttemptHold {
					attempts++
				}
				if writes != 1+attempts { // one charge, then one mark per window
					t.Fatalf("at %s ledger writes=%d, want %d", elapsed, writes, 1+attempts)
				}
				wantCreates := attempts
				if kind == "no-free-name" {
					wantCreates = 0
				}
				if creates != wantCreates {
					t.Fatalf("at %s creates=%d, want %d", elapsed, creates, wantCreates)
				}
				if i > 0 && before != w.get(filingRem).ResourceVersion {
					t.Fatal("same blocked reason rewrote status/event")
				}
				e, _ := filingLedger(t, w)
				if len(e.Charges) != 1 || !e.Charges[0].At.Equal(summonT0) {
					t.Fatal("retry recharged")
				}
				wantAttempt := summonT0.Add(time.Duration(attempts-1) * summonledger.AttemptHold)
				if !e.Charges[0].Attempt.Equal(wantAttempt) {
					t.Fatalf("attempt=%s, want %s", e.Charges[0].Attempt, wantAttempt)
				}
				if i == 2 {
					q := summonledger.Queue{Urgent: true, Escalations: true}
					atBoundary := summonT0.Add(summonledger.AttemptHold)
					if summonledger.QueueCount(callerA, filingPolicy(), e.Charges, nil, nil, summonledger.Grants{}, q, "", atBoundary) != 0 {
						t.Fatal("old attempt did not leave queue at boundary")
					}
				}
			}
			// Recover after the next window, including an earlier name that
			// became free. The first event remains; Filed clears the reason.
			recoverCreate = true
			if kind == "no-free-name" {
				name := summonoutcome.EscalationName(w.get(filingRem))
				if err := w.base.Delete(context.Background(), &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents"}}); err != nil {
					t.Fatal(err)
				}
			}
			w.now = summonT0.Add(2 * summonledger.AttemptHold)
			w.r = filingLeader(w, hooks)
			if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			assertOneFiling(t, w, summonT0)
			if st := w.get(filingRem).Status.Summon; st.Escalation.Reason != "" || len(st.Pages) != 1 || writes != 4 {
				t.Fatalf("recovery changed event/charge: %+v writes=%d", st, writes)
			}
		})
	}
}

func TestSummonFilingChildInvalidBeforeCharge(t *testing.T) {
	for _, kind := range []string{"build", "preview"} {
		t.Run(kind, func(t *testing.T) {
			w := newFilingWorld(t)
			s := w.get(filingRem)
			if kind == "build" {
				s.Spec.Summon.Escalation = nil
			} else {
				s.Spec.Summon.Escalation.Model = "fixture-invalid-model"
			}
			if err := w.base.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			_, before := filingLedger(t, w)
			s = w.get(filingRem)
			var cause error
			if kind == "preview" {
				child, err := summonFilingChild(s, summonledger.NameSequence(summonoutcome.EscalationName(s))[98], "0123456789abcdef")
				if err != nil {
					t.Fatal(err)
				}
				cause = CheckAgentdSession(child)
				if cause == nil {
					t.Fatal("preview fixture is valid")
				}
			}
			err := w.r.summonFile(context.Background(), s)
			if err == nil {
				t.Fatal("lost child validation error")
			}
			if cause != nil && !strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("lost preview cause %q: %v", cause, err)
			}
			filingBlocked(t, w, "child-invalid")
			first := w.get(filingRem).ResourceVersion
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			_, after := filingLedger(t, w)
			if before.ResourceVersion != after.ResourceVersion || first != w.get(filingRem).ResourceVersion {
				t.Fatal("invalid child wrote ledger or repeated status")
			}
		})
	}
}

func TestSummonFilingTwoLeadersOverlap(t *testing.T) {
	for _, gap := range []string{"mark", "create"} {
		t.Run(gap, func(t *testing.T) {
			w := newFilingWorld(t)
			bCreates, aCreates, aFiled, writes := 0, 0, 0, 0
			alreadyExists, fired := false, false
			b := filingLeader(w, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				bCreates++
				return c.Create(ctx, o, opts...)
			}})
			runB := func() {
				fired = true
				if err := b.SummonAdmissionPass(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			a := filingLeader(w, interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					if _, ok := o.(*corev1.ConfigMap); ok {
						writes++
						if gap == "mark" && writes == 2 {
							runB()
						}
					}
					return c.Update(ctx, o, opts...)
				},
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					aCreates++
					if gap == "create" && !fired {
						runB()
					}
					err := c.Create(ctx, o, opts...)
					alreadyExists = apierrors.IsAlreadyExists(err)
					return err
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					if s, ok := o.(*v1alpha1.AgentSession); ok && s.Name == filingRem && s.Status.Summon.Escalation.State == v1alpha1.SummonEscalationFiled {
						aFiled++
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
			})
			if err := a.SummonAdmissionPass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !fired || bCreates != 1 {
				t.Fatalf("B whole pass: fired=%v creates=%d", fired, bCreates)
			}
			assertOneFiling(t, w, summonT0)
			if gap == "mark" && aCreates != 0 {
				t.Fatal("A created after B won the mark")
			}
			if gap == "create" && (!alreadyExists || aCreates != 1 || aFiled != 1) {
				t.Fatalf("AlreadyExists recovery not executed: exists=%v creates=%d Filed patches=%d", alreadyExists, aCreates, aFiled)
			}
			before := aCreates
			if err := a.SummonAdmissionPass(context.Background()); err != nil {
				t.Fatal(err)
			}
			if aCreates != before {
				t.Fatal("A attempted a second create")
			}
			assertOneFiling(t, w, summonT0)
		})
	}
}

func TestSummonFilingBlockedLeadersShareAttemptWindow(t *testing.T) {
	w := newFilingWorld(t)
	creates, committed := 0, 0
	refuse := func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
		creates++
		return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	}
	countWrite := func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
		err := c.Update(ctx, o, opts...)
		if _, ok := o.(*corev1.ConfigMap); ok && err == nil {
			committed++
		}
		return err
	}
	b := filingLeader(w, interceptor.Funcs{Create: refuse, Update: countWrite})
	if err := b.SummonAdmissionPass(context.Background()); !apierrors.IsInvalid(err) {
		t.Fatalf("initial refusal: %v", err)
	}
	filingBlocked(t, w, "create-refused")
	w.now = summonT0.Add(summonledger.AttemptHold)
	fired := false
	a := filingLeader(w, interceptor.Funcs{Create: refuse, Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
		if _, ok := o.(*corev1.ConfigMap); ok && !fired {
			fired = true
			if err := b.SummonAdmissionPass(context.Background()); !apierrors.IsInvalid(err) {
				t.Fatalf("B refusal: %v", err)
			}
		}
		return countWrite(ctx, c, o, opts...)
	}})
	if err := a.SummonAdmissionPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fired || creates != 2 || committed != 3 {
		t.Fatalf("overlapping blocked attempts: fired=%v creates=%d committed=%d", fired, creates, committed)
	}
	filingBlocked(t, w, "create-refused")
}

func TestSummonFilingBlockedConflictKeepsCause(t *testing.T) {
	w := newFilingWorld(t)
	cause := errors.New("fixture ledger read failed")
	w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
		if _, ok := o.(*corev1.ConfigMap); ok {
			return cause
		}
		return c.Get(ctx, key, o, opts...)
	}})
	w.r.Client = interceptor.NewClient(w.base, interceptor.Funcs{SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
		return apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, filingRem, errors.New("fixture conflict"))
	}})
	errs := w.r.summonFilings(context.Background(), []v1alpha1.AgentSession{*w.get(filingRem)})
	if !errors.Is(errors.Join(errs...), cause) {
		t.Fatalf("Blocked conflict hid real cause: %v", errs)
	}
}

func TestSummonFilingEmptyChargeNeverWritesZeroTime(t *testing.T) {
	w := newFilingWorld(t)
	stale := w.get(filingRem)
	live := stale.DeepCopy()
	at := metav1.NewTime(w.now)
	live.Status.Summon.Escalation.ChargedAt = &at
	if err := w.base.Status().Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	zeroPatches := 0
	w.r.Client = interceptor.NewClient(w.base, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
		if s, ok := o.(*v1alpha1.AgentSession); ok && s.Status.Summon.Escalation.ChargedAt != nil && s.Status.Summon.Escalation.ChargedAt.IsZero() {
			zeroPatches++
		}
		return c.SubResource(sub).Patch(ctx, o, p, opts...)
	}})
	_ = w.r.summonFile(context.Background(), stale)
	if zeroPatches != 0 {
		t.Fatal("empty charge attempted a zero chargedAt patch")
	}
	e, _ := filingLedger(t, w)
	if len(e.Charges) != 0 || len(filingChildren(t, w)) != 0 {
		t.Fatal("empty binding authorized a charge/create")
	}
}

func TestSummonFilingSettledListSkipsGET(t *testing.T) {
	w := newFilingWorld(t)
	for _, state := range []v1alpha1.SummonEscalationState{v1alpha1.SummonEscalationFiled, v1alpha1.SummonEscalationAbandoned} {
		s := filingRemediation()
		s.Status.Summon.Escalation.State = state
		gets := 0
		w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
			gets++
			return errors.New("unexpected GET")
		}})
		if errs := w.r.summonFilings(context.Background(), []v1alpha1.AgentSession{*s}); len(errs) != 0 || gets != 0 {
			t.Fatalf("listed %s did uncached GET: gets=%d errors=%v", state, gets, errs)
		}
	}
}

func TestSummonFilingOpenAndFiledQuiet(t *testing.T) {
	w := newFilingWorld(t)
	w.pass()
	child := filingChildren(t, w)[0]
	if err := w.base.Delete(context.Background(), &child); err != nil {
		t.Fatal(err)
	}
	open := summonedFor("rem-open", callerA, v1alpha1.LaneRemediation, 0)
	at := metav1.NewTime(w.now)
	open.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
	open.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning, At: &at}
	w.add(open)
	w.pass() // settle admission before measuring the next unchanged pass
	writes := 0
	w.r = filingLeader(w, interceptor.Funcs{
		Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
			writes++
			return errors.New("unexpected create")
		},
		Update: func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			writes++
			return errors.New("unexpected update")
		},
		SubResourcePatch: func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
			writes++
			return errors.New("unexpected patch")
		},
	})
	if err := w.r.SummonAdmissionPass(context.Background()); err != nil || writes != 0 {
		t.Fatalf("quiet writes=%d error=%v", writes, err)
	}
}
