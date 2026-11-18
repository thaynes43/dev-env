package controller

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonledger"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

func seedAbandonCharge(t *testing.T, w *summonWorld, attempt time.Time) {
	t.Helper()
	s := w.get(filingRem)
	at := metav1.NewTime(summonT0)
	s.Status.Summon.Escalation.ChargedAt = &at
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	w.ledger[callerA] = &summonledger.Entry{Clock: attempt, Charges: []summonledger.Charge{{ID: summonledger.FilingID(s.UID), Hash: summonFilingHash(s), Name: summonoutcome.EscalationName(s), At: summonT0, Attempt: attempt}}}
	w.writeLedger()
}

func abandonChild(t *testing.T, w *summonWorld) *v1alpha1.AgentSession {
	t.Helper()
	s := w.get(filingRem)
	child, err := summonFilingChild(s, s.Status.Summon.Escalation.Name, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	child.UID = types.UID("00000000-0000-0000-0000-000000000803")
	child.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeDone}
	child.Status.Summon = &v1alpha1.SummonStatus{Closed: &v1alpha1.SummonClosedStatus{At: metav1.NewTime(w.now), Released: true}}
	w.add(child)
	return child
}

func wantAbandoned(t *testing.T, w *summonWorld, reason v1alpha1.SummonFilingReason, at time.Time) {
	t.Helper()
	s := w.get(filingRem)
	st := s.Status.Summon
	if st.Escalation.State != v1alpha1.SummonEscalationAbandoned || st.Escalation.Reason != reason || st.Escalation.UID != "" {
		t.Fatalf("want Abandoned/%s, got %+v", reason, st.Escalation)
	}
	events := 0
	for _, p := range st.Pages {
		if p.Event == v1alpha1.PageEscalationAbandoned {
			events++
			if !p.At.Time.Equal(at.UTC().Truncate(time.Second)) {
				t.Fatalf("event at=%s, want %s", p.At, at)
			}
		}
	}
	c := meta.FindStatusCondition(s.Status.Conditions, summonoutcome.ConditionEscalationPending)
	if events != 1 || c == nil || c.Status != metav1.ConditionFalse || c.Reason != "Abandoned" {
		t.Fatalf("event/condition not settled together: %+v", s.Status)
	}
}

func TestSummonFilingAbandonBound(t *testing.T) {
	for _, restored := range []time.Duration{24*time.Hour - 11*time.Minute, 24 * time.Hour, 25 * time.Hour} {
		t.Run(restored.String(), func(t *testing.T) {
			w := newFilingWorld(t)
			if err := w.base.Delete(context.Background(), filingPolicy()); err != nil {
				t.Fatal(err)
			}
			w.pass()
			filingBlocked(t, w, summonoutcome.FilingPolicyGone)
			w.now = summonT0.Add(restored)
			w.add(filingPolicy())
			writes, creates, ledgers := 0, 0, 0
			w.r = filingLeader(w, interceptor.Funcs{
				Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					creates++
					return c.Create(ctx, o, opts...)
				},
				Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
					ledgers++
					return c.Update(ctx, o, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					s := o.(*v1alpha1.AgentSession)
					if s.Name == filingRem && s.Status.Summon != nil && s.Status.Summon.Escalation != nil && s.Status.Summon.Escalation.State == v1alpha1.SummonEscalationAbandoned {
						if len(s.Status.Summon.Pages) != 2 || s.Status.Summon.Pages[1].Event != v1alpha1.PageEscalationAbandoned {
							t.Fatal("Abandoned and event were not in one write")
						}
						writes++
					}
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
			})
			w.pass()
			if restored < 24*time.Hour {
				assertOneFiling(t, w, w.now)
				w.pass()
				if creates != 1 {
					t.Fatalf("restored before bound: creates=%d", creates)
				}
				return
			}
			wantAbandoned(t, w, summonoutcome.FilingPolicyGone, w.now)
			if creates != 0 || ledgers != 0 || writes != 1 {
				t.Fatalf("abandon charged/marked/created: status=%d ledger=%d creates=%d", writes, ledgers, creates)
			}
			before := w.get(filingRem).ResourceVersion
			w.now = w.now.Add(48 * time.Hour)
			for range 3 {
				w.pass()
			}
			if w.get(filingRem).ResourceVersion != before || writes != 1 || creates != 0 || ledgers != 0 {
				t.Fatal("Abandoned filing was retried")
			}
		})
	}
}

func TestSummonFilingAbandonWithoutRecordedWord(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24 * time.Hour)
	w.pass()
	wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
	if len(filingChildren(t, w)) != 0 {
		t.Fatal("created at abandon bound")
	}
}

func TestSummonFilingAbandonFindsChildAtAnyAge(t *testing.T) {
	for _, age := range []time.Duration{24 * time.Hour, 48 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			w := newFilingWorld(t)
			seedAbandonCharge(t, w, summonT0)
			child := abandonChild(t, w)
			w.now = summonT0.Add(age)
			// Recovery must work even when policy and ledger reads cannot authorize a create.
			w.r.SummonCreateKinds = nil
			lists := 0
			w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{List: func(ctx context.Context, c client.WithWatch, o client.ObjectList, opts ...client.ListOption) error {
				lo := (&client.ListOptions{}).ApplyOptions(opts)
				if lo.LabelSelector != nil {
					lists++
				}
				return c.List(ctx, o, opts...)
			}})
			if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			st := w.get(filingRem).Status.Summon
			if lists != 1 || st.Escalation.State != v1alpha1.SummonEscalationFiled || st.Escalation.UID != string(child.UID) || len(st.Pages) != 0 {
				t.Fatalf("child lost at bound: lists=%d status=%+v", lists, st)
			}
		})
	}
}

func TestSummonFilingAbandonReplay(t *testing.T) {
	for _, gap := range []string{"after-list-before-write", "lost-write-response", "leader-b-abandons", "leader-b-files"} {
		t.Run(gap, func(t *testing.T) {
			w := newFilingWorld(t)
			seedAbandonCharge(t, w, summonT0)
			w.now = summonT0.Add(24 * time.Hour)
			stopped := errors.New("fixture crash or lost response")
			landed, attempts := 0, 0
			patch := func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
				err := c.SubResource(sub).Patch(ctx, o, p, opts...)
				if err == nil && o.(*v1alpha1.AgentSession).Status.Summon.Escalation.State == v1alpha1.SummonEscalationAbandoned {
					landed++
				}
				return err
			}
			b := filingLeader(w, interceptor.Funcs{SubResourcePatch: patch})
			w.r = filingLeader(w, interceptor.Funcs{
				Create: func(context.Context, client.WithWatch, client.Object, ...client.CreateOption) error {
					t.Fatal("abandon pass created")
					return nil
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					if o.(*v1alpha1.AgentSession).Status.Summon.Escalation.State != v1alpha1.SummonEscalationAbandoned {
						return patch(ctx, c, sub, o, p, opts...)
					}
					attempts++
					switch gap {
					case "after-list-before-write":
						return stopped
					case "lost-write-response":
						if err := patch(ctx, c, sub, o, p, opts...); err != nil {
							return err
						}
						return stopped
					case "leader-b-files":
						abandonChild(t, w)
					}
					if err := b.SummonAdmissionPass(ctx); err != nil {
						t.Fatal(err)
					}
					return patch(ctx, c, sub, o, p, opts...)
				},
			})
			err := w.r.summonFile(context.Background(), w.get(filingRem))
			if attempts != 1 || err == nil {
				t.Fatalf("barrier not exercised: attempts=%d err=%v", attempts, err)
			}
			w.r = b
			w.pass()
			before := w.get(filingRem).ResourceVersion
			w.pass()
			if w.get(filingRem).ResourceVersion != before {
				t.Fatal("settled replay wrote again")
			}
			if gap == "leader-b-files" {
				assertOneFiling(t, w, summonT0)
				if landed != 0 {
					t.Fatal("both Filed and Abandoned landed")
				}
			} else {
				wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
				if landed != 1 || len(filingChildren(t, w)) != 0 {
					t.Fatalf("final abandons=%d children=%d", landed, len(filingChildren(t, w)))
				}
			}
		})
	}
}

func TestSummonReapWaitsForFiling(t *testing.T) {
	for _, state := range []string{"Pending", "Filed", "Abandoned", "person"} {
		t.Run(state, func(t *testing.T) {
			w := newFilingWorld(t)
			s := w.get(filingRem)
			s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: "session/worker"}
			if err := w.base.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			s = w.get(filingRem)
			switch state {
			case "Filed":
				s.Status.Summon.Escalation.State = v1alpha1.SummonEscalationFiled
			case "Abandoned":
				s.Status.Summon.Escalation.State = v1alpha1.SummonEscalationAbandoned
				s.Status.Summon.Escalation.Reason = summonoutcome.FilingNotFiled
				s.Status.Summon.Pages = []v1alpha1.SummonPage{{Event: v1alpha1.PageEscalationAbandoned, At: metav1.NewTime(summonT0.Add(24 * time.Hour))}}
			case "person":
				s.Status.Outcome.State = v1alpha1.OutcomeFailed
				s.Status.Summon.Escalation = nil
			}
			if err := w.base.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			if state == "person" {
				s = w.get(filingRem)
				s.Annotations[v1alpha1.AnnotationReapedBy] = "human/dev-env-system/dev-env-human"
				if err := w.base.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
			}
			w.now = summonT0.Add(24*time.Hour + 10*time.Minute - time.Nanosecond)
			if _, err := w.r.reconcileSummonReap(context.Background(), w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			held := state == "Pending" || state == "Abandoned"
			if w.exists(&v1alpha1.AgentSession{}, filingRem) != held {
				t.Fatalf("state=%s held=%v", state, held)
			}
			if state == "Abandoned" {
				w.now = summonT0.Add(24*time.Hour + 10*time.Minute)
				if _, err := w.r.reconcileSummonReap(context.Background(), w.get(filingRem)); err != nil {
					t.Fatal(err)
				}
				if w.exists(&v1alpha1.AgentSession{}, filingRem) {
					t.Fatal("held past the 24h10m deliberate maximum")
				}
			}
		})
	}
}

func TestSummonReapLongestFilingHold(t *testing.T) {
	w := newFilingWorld(t)
	// Creates off still abandons existing filings and holds their reap.
	w.r.SummonCreateKinds = nil
	s := w.get(filingRem)
	s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: "session/worker"}
	if err := w.base.Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// Full reconciliation must requeue a held reap even with no later event.
	var out ctrl.Result
	var err error
	for range reconcileRounds {
		out, err = w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err != nil || out.RequeueAfter <= 0 || out.RequeueAfter > time.Minute {
		t.Fatalf("no bounded reap recheck: %+v %v", out, err)
	}
	w.now = summonT0.Add(24*time.Hour - time.Nanosecond)
	if _, err := w.r.reconcileSummonReap(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	w.now = summonT0.Add(24 * time.Hour)
	w.pass()
	wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
	for _, elapsed := range []time.Duration{24 * time.Hour, 24*time.Hour + 10*time.Minute - time.Nanosecond, 24*time.Hour + 10*time.Minute} {
		w.now = summonT0.Add(elapsed)
		if _, err := w.r.reconcileSummonReap(context.Background(), w.get(filingRem)); err != nil {
			t.Fatal(err)
		}
		wantExists := elapsed < 24*time.Hour+10*time.Minute
		exists := w.exists(&v1alpha1.AgentSession{}, filingRem)
		retained := exists && w.get(filingRem).DeletionTimestamp.IsZero()
		if retained != wantExists {
			t.Fatalf("at %s: want retained=%v", elapsed, wantExists)
		}
	}
}

func TestSummonFilingAbandonReadFailureKeepsPending(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24 * time.Hour)
	unavailable := errors.New("fixture list failed")
	w.r.APIReader = interceptor.NewClient(w.base, interceptor.Funcs{List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
		return unavailable
	}})
	err := w.r.summonFile(context.Background(), w.get(filingRem))
	if !errors.Is(err, unavailable) || w.get(filingRem).Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending {
		t.Fatalf("unproven absence abandoned: %v", err)
	}
}

// Existing delete-error/precondition proofs now start after the filing has
// settled. They still exercise the same delete and release behavior.
func settleReapFilingFixture(t *testing.T, w *summonWorld, name string) {
	t.Helper()
	s := w.get(name)
	at := metav1.NewTime(w.now)
	s.Status.Summon.Escalation.State = v1alpha1.SummonEscalationFiled
	s.Status.Summon.Escalation.UID = "00000000-0000-0000-0000-000000000804"
	s.Status.Summon.Escalation.ChargedAt = &at
	if err := w.base.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func TestSummonFilingAbandonReleasesChargeAndQueue(t *testing.T) {
	for _, freshMark := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh-mark=%v", freshMark), func(t *testing.T) {
			w := newFilingWorld(t)
			attempt := summonT0
			if freshMark {
				attempt = summonT0.Add(24*time.Hour - time.Minute)
			}
			seedAbandonCharge(t, w, attempt)
			w.now = summonT0.Add(24 * time.Hour)
			e, _ := filingLedger(t, w)
			q := summonledger.Queue{Urgent: true, Escalations: true}
			count := func() int {
				return summonledger.QueueCount(callerA, filingPolicy(), e.Charges, []v1alpha1.AgentSession{*w.get(filingRem)}, nil, summonledger.Grants{}, q, "", w.now)
			}
			if count() != 1 {
				t.Fatal("Pending lost its queue place before Abandoned")
			}
			beforeLedger := e.Charges[0]
			if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
				t.Fatal(err)
			}
			wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
			got, _ := filingLedger(t, w)
			if len(got.Charges) != 1 || got.Charges[0].At != beforeLedger.At || got.Charges[0].Attempt != beforeLedger.Attempt {
				t.Fatal("abandon changed charge or mark")
			}
			want := 0
			if freshMark {
				want = 1
			}
			kept := summonledger.Prune(e.Charges, w.now, summonledger.Keep([]v1alpha1.AgentSession{*w.get(filingRem)}))
			if count() != want || len(kept) != want {
				t.Fatalf("after Abandoned count=%d prune=%d want=%d", count(), len(kept), want)
			}
			if freshMark {
				w.now = attempt.Add(summonledger.AttemptHold - time.Nanosecond)
				if count() != 1 {
					t.Fatal("last mark freed before 15 minutes")
				}
				w.now = attempt.Add(summonledger.AttemptHold)
				kept = summonledger.Prune(e.Charges, w.now, summonledger.Keep([]v1alpha1.AgentSession{*w.get(filingRem)}))
				if count() != 0 || len(kept) != 0 {
					t.Fatal("last mark still holds the charge/queue at 15 minutes")
				}
			}
		})
	}
}

func TestSummonFilingBoundFencesPausedPass(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
	b := filingLeader(w, interceptor.Funcs{})
	b.Now = func() time.Time { return summonT0.Add(24 * time.Hour) }
	updates, creates := 0, 0
	w.r = filingLeader(w, interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.UpdateOption) error {
			if err := c.Update(ctx, o, opts...); err != nil {
				return err
			}
			updates++
			if updates == 2 {
				// A's mark landed outside the guard. B settles the filing
				// before A's final read; the fresh terminal state must win.
				if err := b.SummonAdmissionPass(ctx); err != nil {
					t.Fatal(err)
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
	wantAbandoned(t, w, summonoutcome.FilingNotFiled, summonT0.Add(24*time.Hour))
	if updates != 2 || creates != 0 || len(filingChildren(t, w)) != 0 {
		t.Fatalf("paused pass escaped final state: writes=%d creates=%d", updates, creates)
	}
}

func TestSummonReapPendingStillSuspendsAndReleases(t *testing.T) {
	w, s, _ := launchSummoned(t)
	w.schedule(s.Name, nil)
	w.close(s.Name, summonoutcome.Unreported)
	live := w.get(s.Name)
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[v1alpha1.AnnotationReapedBy] = "session/worker"
	if err := w.base.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	w.reconcile(s.Name)
	live = w.get(s.Name)
	if !live.DeletionTimestamp.IsZero() || live.Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending || live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("Pending hold did not preserve stop work")
	}
	// Once its executor is gone, the existing release path must still run.
	var pod corev1.Pod
	if err := w.base.Get(context.Background(), client.ObjectKeyFromObject(live), &pod); err != nil {
		t.Fatal(err)
	}
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	w.reconcile(s.Name)
	if !w.released(s.Name) || !w.get(s.Name).DeletionTimestamp.IsZero() {
		t.Fatal("Pending delete hold prevented release")
	}
}

type abandonDeadlineClock struct {
	filingTestClock
	deadline time.Time
}

func (c *abandonDeadlineClock) WithDeadline(ctx context.Context, at time.Time) (context.Context, context.CancelFunc) {
	c.deadline = at
	return c.filingTestClock.WithDeadline(ctx, at)
}

func TestSummonFilingCreateDeadlineBeforeGuard(t *testing.T) {
	w := newFilingWorld(t)
	w.now = summonT0.Add(24*time.Hour - 10*time.Minute - time.Second)
	w.r = filingLeader(w, interceptor.Funcs{})
	// The two-minute deadline stays anchored before the mark, on the process clock.
	clock := &abandonDeadlineClock{filingTestClock: filingTestClock{now: summonT0.Add(5 * time.Minute)}}
	w.r.SummonFilingClock = clock
	if err := w.r.summonFile(context.Background(), w.get(filingRem)); err != nil {
		t.Fatal(err)
	}
	if want := clock.now.Add(2 * time.Minute); !clock.deadline.Equal(want) {
		t.Fatalf("create can outlast bound: deadline=%s want=%s", clock.deadline, want)
	}
	assertOneFiling(t, w, w.now)
}

func TestSummonReapReportedFilingStillStopsExecutor(t *testing.T) {
	for _, heldByOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner-hold=%v", heldByOwner), func(t *testing.T) {
			w, s, _ := launchSummoned(t)
			w.schedule(s.Name, nil)
			if changed, err := summonoutcome.Write(context.Background(), w.base, w.get(s.Name), summonoutcome.Decision{State: v1alpha1.OutcomeEscalated, Source: summonoutcome.Report, Reason: "escalate", Released: true}, w.now); err != nil || !changed {
				t.Fatalf("report close: %v %v", changed, err)
			}
			live := w.get(s.Name)
			if live.Annotations == nil {
				live.Annotations = map[string]string{}
			}
			live.Annotations[v1alpha1.AnnotationReapedBy] = "session/worker"
			if heldByOwner {
				live.Annotations[v1alpha1.AnnotationHomeExpiry] = "hold"
			}
			if err := w.base.Update(context.Background(), live); err != nil {
				t.Fatal(err)
			}
			w.reconcile(s.Name)
			live = w.get(s.Name)
			if !live.DeletionTimestamp.IsZero() || live.Status.Summon.Escalation.State != v1alpha1.SummonEscalationPending || !live.Status.Summon.Closed.Released {
				t.Fatal("reported reap did not keep its filing and release")
			}
			if got := w.stopRequested(s.Name); got == heldByOwner {
				t.Fatalf("reported held reap stop=%v owner-hold=%v", got, heldByOwner)
			}
			if heldByOwner {
				delete(live.Annotations, v1alpha1.AnnotationHomeExpiry)
				if err := w.base.Update(context.Background(), live); err != nil {
					t.Fatal(err)
				}
				w.reconcile(s.Name)
				if !w.stopRequested(s.Name) {
					t.Fatal("released owner hold did not permit executor stop")
				}
			}
		})
	}
}

func TestSummonFilingBoundAfterDependencyReads(t *testing.T) {
	for _, action := range []filingLedgerAction{filingCharge, filingMark, filingRebind} {
		t.Run(fmt.Sprint(action), func(t *testing.T) {
			w := newFilingWorld(t)
			if action != filingCharge {
				seedAbandonCharge(t, w, summonT0)
			}
			w.now = summonT0.Add(24*time.Hour - 11*time.Minute)
			b := filingLeader(w, interceptor.Funcs{})
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
				if barriers == 1 {
					w.now = summonT0.Add(24 * time.Hour)
					if err := b.SummonAdmissionPass(ctx); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}})
			result, err := w.r.filingLedgerStep(context.Background(), w.get(filingRem), action, summonoutcome.EscalationName(w.get(filingRem)))
			if err != nil || barriers != 1 {
				t.Fatalf("dependency barrier: count=%d err=%v", barriers, err)
			}
			if writes != 0 || !result.done {
				t.Fatalf("settled filing wrote ledger after dependency reads: writes=%d done=%v", writes, result.done)
			}
			wantAbandoned(t, w, summonoutcome.FilingNotFiled, w.now)
		})
	}
}
