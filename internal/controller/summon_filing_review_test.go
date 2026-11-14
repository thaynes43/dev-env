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

func TestSummonFilingRefusalWindowSurvivesStatusConflict(t *testing.T) {
	w := newFilingWorld(t)
	writes, creates, conflicts := 0, 0, 0
	hooks := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.ConfigMap); ok {
				writes++
			}
			return c.Update(ctx, o, opts...)
		},
		Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
			creates++
			return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			s := o.(*v1alpha1.AgentSession)
			if s.Status.Summon.Escalation.Reason == summonoutcome.FilingCreateRefused && conflicts == 0 {
				conflicts++
				return apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, s.Name, errors.New("fixture status conflict"))
			}
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	}
	w.r = filingLeader(w, hooks)
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); !apierrors.IsInvalid(err) {
		t.Fatalf("lost refusal: %v", err)
	}
	w.now = summonT0.Add(time.Minute)
	w.r = filingLeader(w, hooks)
	_ = w.r.summonFile(context.Background(), w.get(filingRem))
	if writes != 2 || creates != 1 || conflicts != 1 {
		t.Fatalf("status conflict lost attempt window: writes=%d creates=%d conflicts=%d", writes, creates, conflicts)
	}
	filingBlocked(t, w, summonoutcome.FilingCreateRefused)
}

func TestSummonFilingRefusalWindowSurvivesReadFailure(t *testing.T) {
	for _, failure := range []string{"label-list", "ledger-read"} {
		t.Run(failure, func(t *testing.T) {
			w := newFilingWorld(t)
			writes, creates := 0, 0
			hooks := interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					if _, ok := o.(*corev1.ConfigMap); ok {
						writes++
					}
					return c.Update(ctx, o, opts...)
				},
				Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
					creates++
					return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
				},
			}
			w.r = filingLeader(w, hooks)
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			before := w.get(filingRem).ResourceVersion
			cause := errors.New("fixture transient read")
			w.now = summonT0.Add(time.Minute)
			w.r = filingLeader(w, hooks)
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
					if _, ok := o.(*corev1.ConfigMap); ok && failure == "ledger-read" {
						return cause
					}
					return c.Get(ctx, key, o, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, o client.ObjectList, opts ...client.ListOption) error {
					options := (&client.ListOptions{}).ApplyOptions(opts)
					if options.LabelSelector != nil && failure == "label-list" {
						return cause
					}
					return c.List(ctx, o, opts...)
				},
			})
			if err := w.r.summonFile(context.Background(), w.get(filingRem)); !errors.Is(err, cause) {
				t.Fatalf("lost read error: %v", err)
			}
			if s := w.get(filingRem); s.ResourceVersion != before || s.Status.Summon.Escalation.Reason != summonoutcome.FilingCreateRefused {
				t.Error("read failure erased the persisted refusal")
			}
			w.now = summonT0.Add(2 * time.Minute)
			w.r = filingLeader(w, hooks)
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			if writes != 2 || creates != 1 {
				t.Fatalf("read failure lost attempt window: writes=%d creates=%d", writes, creates)
			}
		})
	}
}

func TestSummonFilingHeldWindowRecoversChild(t *testing.T) {
	w := newFilingWorld(t)
	w.r = filingLeader(w, interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
		return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	}})
	_ = w.r.summonFile(context.Background(), w.get(filingRem))
	child, err := summonFilingChild(w.get(filingRem), summonoutcome.EscalationName(w.get(filingRem)), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	child.UID = "00000000-0000-0000-0000-000000000802"
	w.add(child) // another in-flight leader's create has now landed
	_, before := filingLedger(t, w)
	w.now = summonT0.Add(time.Minute)
	w.r = filingLeader(w, interceptor.Funcs{})
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	assertOneFiling(t, w, summonT0)
	_, after := filingLedger(t, w)
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatal("recovery rewrote the mark")
	}
}

func TestSummonFilingUnrecordedRefusalReplay(t *testing.T) {
	w := newFilingWorld(t)
	writes, creates := 0, 0
	var conflicts []error
	refusal := apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, "fixture", field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	hooks := interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if _, ok := o.(*corev1.ConfigMap); ok {
				writes++
			}
			return c.Update(ctx, o, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
			creates++
			if creates == 1 {
				return refusal
			}
			return c.Create(ctx, o, opts...)
		},
		SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
			s := o.(*v1alpha1.AgentSession)
			if s.Status.Summon.Escalation.Reason == summonoutcome.FilingCreateRefused {
				err := apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, s.Name, errors.New("fixture conflict"))
				conflicts = append(conflicts, err)
				return err
			}
			return c.SubResource(sub).Patch(ctx, o, p, opts...)
		},
	}
	w.r = filingLeader(w, hooks)
	err := w.r.summonFile(context.Background(), w.get(filingRem))
	if len(conflicts) != 5 || !errors.Is(err, refusal) {
		t.Fatalf("unrecorded refusal: conflicts=%d error=%v", len(conflicts), err)
	}
	for _, conflict := range conflicts {
		if !errors.Is(err, conflict) {
			t.Fatal("exhausted retry dropped a patch error")
		}
	}
	if s := w.get(filingRem); s.Status.Summon.Escalation.Reason != "" || len(s.Status.Summon.Pages) != 0 {
		t.Fatal("claimed an unrecorded refusal was durable")
	}
	// The accepted exception: a fresh leader can mark/create again before
	// 15m, but it reuses the charge and later passes recover the same child.
	w.now = summonT0.Add(time.Minute)
	w.r = filingLeader(w, hooks)
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	assertOneFiling(t, w, summonT0)
	w.now = summonT0.Add(2 * time.Minute)
	w.r = filingLeader(w, hooks)
	if errs := w.r.summonFilings(context.Background(), []v1alpha1.AgentSession{*w.get(filingRem)}); len(errs) != 0 {
		t.Fatal(errs)
	}
	assertOneFiling(t, w, summonT0)
	if writes != 3 || creates != 2 {
		t.Fatalf("unrecorded refusal replay: writes=%d creates=%d", writes, creates)
	}
}

func TestSummonFilingBlockedRetryStopsOnFreshState(t *testing.T) {
	for _, stop := range []string{"Filed", "Abandoned", "new-uid", "non-conflict"} {
		t.Run(stop, func(t *testing.T) {
			w := newFilingWorld(t)
			patches := 0
			stopErr := errors.New("fixture non-conflict")
			w.r = filingLeader(w, interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, _ string, o client.Object, _ client.Patch, _ ...client.SubResourcePatchOption) error {
				patches++
				if stop == "non-conflict" {
					return stopErr
				}
				var live v1alpha1.AgentSession
				if err := c.Get(ctx, client.ObjectKeyFromObject(o), &live); err != nil {
					t.Fatal(err)
				}
				if stop == "new-uid" {
					live.UID = "replacement-uid"
					if err := c.Update(ctx, &live); err != nil {
						t.Fatal(err)
					}
				} else {
					live.Status.Summon.Escalation.State = v1alpha1.SummonEscalationState(stop)
					if err := c.Status().Update(ctx, &live); err != nil {
						t.Fatal(err)
					}
				}
				return apierrors.NewConflict(schema.GroupResource{Resource: "agentsessions"}, o.GetName(), errors.New("fixture changed state"))
			}})
			err := w.r.blockSummonFiling(context.Background(), w.get(filingRem), summonoutcome.FilingCreateRefused)
			if patches != 1 || stop == "non-conflict" && !errors.Is(err, stopErr) || stop != "non-conflict" && err != nil {
				t.Fatalf("did not stop at fresh %s: patches=%d error=%v", stop, patches, err)
			}
		})
	}
}

func TestSummonFilingRenewedAttemptReplacesReason(t *testing.T) {
	for _, reason := range []v1alpha1.SummonFilingReason{summonoutcome.FilingNoFreeName, summonoutcome.FilingLedgerUnavailable} {
		t.Run(string(reason), func(t *testing.T) {
			w := newFilingWorld(t)
			creates := 0
			hooks := interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
				creates++
				if creates == 1 {
					return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
				}
				return errors.New("fixture unknown create outcome")
			}}
			w.r = filingLeader(w, hooks)
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			if reason == summonoutcome.FilingNoFreeName {
				for _, name := range summonledger.NameSequence(summonoutcome.EscalationName(w.get(filingRem))) {
					w.add(&v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "dev-agents"}})
				}
			}
			w.now = summonT0.Add(summonledger.AttemptHold)
			w.r = filingLeader(w, hooks)
			_ = w.r.summonFile(context.Background(), w.get(filingRem))
			filingBlocked(t, w, reason)
		})
	}
}

func TestSummonFilingOlderAttemptPreservesNewerRefusal(t *testing.T) {
	w := newFilingWorld(t)
	writes, aCreates, bCreates, cCreates := 0, 0, 0, 0
	countWrite := func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
		err := c.Update(ctx, o, opts...)
		if _, ok := o.(*corev1.ConfigMap); ok && err == nil {
			writes++
		}
		return err
	}
	refuse := func(o client.Object) error {
		return apierrors.NewInvalid(schema.GroupKind{Kind: "AgentSession"}, o.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), "fixture refusal")})
	}
	b := filingLeader(w, interceptor.Funcs{Update: countWrite, Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
		bCreates++
		return refuse(o)
	}})
	unknown := errors.New("fixture older create response is unknown")
	a := filingLeader(w, interceptor.Funcs{Update: countWrite, Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
		aCreates++
		if err := b.SummonAdmissionPass(context.Background()); !apierrors.IsInvalid(err) {
			t.Fatalf("B refusal: %v", err)
		}
		return unknown
	}})
	if err := a.SummonAdmissionPass(context.Background()); !errors.Is(err, unknown) {
		t.Fatalf("lost A result: %v", err)
	}
	before := writes
	w.now = summonT0.Add(time.Minute)
	c := filingLeader(w, interceptor.Funcs{Update: countWrite, Create: func(_ context.Context, _ client.WithWatch, o client.Object, _ ...client.CreateOption) error {
		cCreates++
		return refuse(o)
	}})
	_ = c.SummonAdmissionPass(context.Background())
	if aCreates != 1 || bCreates != 1 || cCreates != 0 || writes != before {
		t.Fatalf("older result erased newer refusal: A=%d B=%d C=%d writes=%d before=%d", aCreates, bCreates, cCreates, writes, before)
	}
	filingBlocked(t, w, summonoutcome.FilingCreateRefused)
	e, _ := filingLedger(t, w)
	if len(e.Charges) != 1 || !e.Charges[0].At.Equal(summonT0) {
		t.Fatal("overlap recharged")
	}
}
