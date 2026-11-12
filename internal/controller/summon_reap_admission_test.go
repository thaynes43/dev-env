package controller

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// Seed an eligible, never-admitted snapshot so the admission pass can run
// before any Reconcile in these tests, including the free-lane case.
func queuedReapFixture(name string, lane v1alpha1.Lane) *v1alpha1.AgentSession {
	s := summonedFor(name, callerA, lane, 0)
	s.Finalizers = []string{Finalizer}
	if s.Spec.Mode == v1alpha1.ModeTask {
		s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{
			Version: 1, SessionUID: string(s.UID), HomeClaim: HomeClaimName(s.Name), BindingGeneration: s.Generation,
			Admission: sharedNeverStarted, Provider: string(s.Spec.Agent), StopState: "Unknown",
		}
	}
	return s
}

// N1: the marker's presence removes a session from the admission queue.
// The first pass must not write anything for it; only the following
// reconciles decide its stop outcome and then begin the recorded reap.
func TestSummonRecordedReapLeavesAdmissionQueue(t *testing.T) {
	for _, reaper := range []struct{ name, marker string }{
		{"person", "human/dev-env-system/dev-env-human"},
		{"session", "session/worker"},
		{"empty", ""},
	} {
		for _, scenario := range []string{"at-maxHold", "after-maxHold", "free-lane", "not-charged", "held-escalation"} {
			t.Run(reaper.name+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				lane := v1alpha1.LaneRemediation
				if scenario == "held-escalation" {
					lane = v1alpha1.LaneEscalation
				}
				s := queuedReapFixture("recorded-reap", lane)
				s.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: reaper.marker}
				w := newSummonWorld(t, s, summonPolicyObject(callerA, 2, ptr.To[int32](1)))
				if scenario != "not-charged" {
					w.charge(s, s.Name)
				}
				switch scenario {
				case "at-maxHold", "after-maxHold":
					busy := queuedReapFixture("busy", lane)
					at := metav1.NewTime(summonT0)
					busy.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at}
					busy.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning, At: &at}
					w.add(busy)
					w.charge(busy, busy.Name)
					w.now = summonT0.Add(20 * time.Minute)
					if scenario == "after-maxHold" {
						w.now = w.now.Add(time.Minute)
					}
				case "held-escalation":
					w.now = summonT0.Add(16 * time.Minute)
				}
				before := w.get(s.Name)
				grantsBefore := readTestGrants(t, w.base)
				var writes []string
				observe := func(kind string, obj client.Object) {
					if obj.GetName() == s.Name {
						writes = append(writes, kind)
					}
				}
				w.r = w.leader(&interceptor.Funcs{
					Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
						observe("patch", obj)
						return c.Patch(ctx, obj, p, opts...)
					},
					Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
						observe("update", obj)
						return c.Update(ctx, obj, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, sub string, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
						observe(sub+" patch", obj)
						return c.SubResource(sub).Patch(ctx, obj, p, opts...)
					},
				})
				w.pass() // no Reconcile has run, before or after recording the reap
				if len(writes) != 0 || !apiequality.Semantic.DeepEqual(before, w.get(s.Name)) ||
					!slices.Equal(grantsBefore, readTestGrants(t, w.base)) {
					t.Fatalf("pass changed a recorded reap: writes=%v admitted=%v closed=%+v hold=%s pages=%v grants=%v",
						writes, w.admitted(s.Name), w.closed(s.Name), w.hold(s.Name), w.pages(s.Name), w.grantNames())
				}
				w.wantNoPodOrVolume(s.Name)
				reason, state := summonoutcome.ReasonSuspendedUnreported, v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported)
				if reaper.name == "person" {
					reason, state = summonoutcome.ReasonStopped, v1alpha1.OutcomeFailed
				}
				deletes := 0
				w.r = w.leader(&interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
					if _, ok := obj.(*v1alpha1.AgentSession); !ok || obj.GetName() != s.Name {
						t.Fatalf("unexpected delete: %T %s", obj, obj.GetName())
					}
					w.wantClosed(s.Name, reason, state)
					deletes++
					return c.Delete(ctx, obj, opts...)
				}})
				req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}
				if _, err := w.r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
				w.wantClosed(s.Name, reason, state)
				closed := w.get(s.Name).Status.Summon
				if deletes != 0 || !closed.Closed.Released || closed.AdmittedAt != nil {
					t.Fatalf("close did not precede deletion and release without admission: deletes=%d status=%+v", deletes, closed)
				}
				if state == v1alpha1.OutcomeEscalated {
					if closed.Escalation == nil || closed.Escalation.State != "Pending" {
						t.Fatalf("missing pending filing: %+v", closed.Escalation)
					}
				} else if closed.Escalation != nil {
					t.Fatalf("failed stop got a filing: %+v", closed.Escalation)
				}
				if _, err := w.r.Reconcile(ctx, req); err != nil {
					t.Fatal(err)
				}
				if deletes != 1 || w.get(s.Name).DeletionTimestamp.IsZero() {
					t.Fatalf("leader did not begin the recorded reap after closing: deletes=%d", deletes)
				}
			})
		}
	}
}

// N1: a kept grant reserves both capacity dimensions, but its marked
// session must never get admittedAt. The reconciler's close releases it.
func TestSummonRecordedReapKeepsGrantUntilClose(t *testing.T) {
	for _, collision := range []string{"lane", "pool"} {
		t.Run(collision, func(t *testing.T) {
			stopped := queuedReapFixture("stopped", v1alpha1.LaneRemediation)
			stopped.Annotations = map[string]string{v1alpha1.AnnotationReapedBy: ""}
			waiting := queuedReapFixture("waiting", v1alpha1.LaneRemediation)
			cap, reason := int32(2), SummonReasonLaneBusy
			if collision == "pool" {
				waiting = queuedReapFixture("waiting", v1alpha1.LaneCuration)
				cap, reason = 1, SummonReasonPoolBusy
			}
			w := newSummonWorld(t, stopped, waiting, summonPolicyObject(callerA, cap, ptr.To[int32](1)))
			w.charge(stopped, stopped.Name)
			w.charge(waiting, waiting.Name)
			putTestGrants(t, w.base, grantFor(stopped, summonT0))
			before := w.get(stopped.Name)
			grantsBefore := readTestGrants(t, w.base)
			w.pass()
			if !apiequality.Semantic.DeepEqual(before, w.get(stopped.Name)) ||
				!slices.Equal(grantsBefore, readTestGrants(t, w.base)) {
				t.Fatalf("kept grant completed or changed before the close: admitted=%v grants=%v", w.admitted(stopped.Name), w.grantNames())
			}
			w.wantAdmitted("grant still reserves capacity", map[string]bool{stopped.Name: false, waiting.Name: false})
			if got := w.hold(waiting.Name); got != "False/"+reason {
				t.Fatalf("contender's hold=%s, want %s", got, reason)
			}
			if _, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stopped)}); err != nil {
				t.Fatal(err)
			}
			w.wantClosed(stopped.Name, summonoutcome.ReasonSuspendedUnreported, v1alpha1.OutcomeEscalated)
			if !w.released(stopped.Name) || !w.get(stopped.Name).DeletionTimestamp.IsZero() {
				t.Fatal("never-admitted close did not release before deletion")
			}
			w.pass()
			w.wantAdmitted("close frees capacity", map[string]bool{stopped.Name: false, waiting.Name: true})
			if names := w.grantNames(); len(names) != 1 || names[0] != waiting.Name {
				t.Fatalf("closed grant was not pruned: %v", names)
			}
		})
	}
}

// N2: repeated Session delete errors cannot skip suspension or a release
// supported by the uncached pod read. The error remains visible on every call.
func TestSummonRecordedReapDeleteFailureStillSuspendsAndReleases(t *testing.T) {
	w, s, waiting := launchSummoned(t)
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
	refused := errors.New("fixture: Session delete always fails")
	deletes := 0
	w.r = w.leader(&interceptor.Funcs{Delete: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.DeleteOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); !ok || obj.GetName() != s.Name {
			t.Fatalf("unexpected delete: %T %s", obj, obj.GetName())
		}
		deletes++
		return refused
	}})
	reconcile := func() {
		t.Helper()
		if _, err := w.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); !errors.Is(err, refused) {
			t.Fatalf("lost the delete error: %v", err)
		}
	}
	for attempt := range 2 {
		reconcile()
		live = w.get(s.Name)
		if live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != SummonCloseSuspender {
			t.Fatalf("delete failure blocked the operator's suspend on attempt %d", attempt+1)
		}
		if w.released(s.Name) {
			t.Fatal("released while the recorded pod can still run")
		}
		w.pass()
		w.wantAdmitted(fmt.Sprintf("failed delete %d holds until safe release", attempt+1), map[string]bool{waiting.Name: false})
	}
	// Supply safe release evidence independently: the recorded executor is
	// gone. This test does not claim the failed Session delete removed it.
	var pod corev1.Pod
	if !w.exists(&pod, s.Name) {
		t.Fatal("the recorded pod disappeared before release evidence")
	}
	if err := w.base.Delete(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if deletes != 3 || !w.released(s.Name) || !w.get(s.Name).DeletionTimestamp.IsZero() {
		t.Fatalf("repeated failed delete blocked release: deletes=%d released=%v", deletes, w.released(s.Name))
	}
	w.pass()
	w.wantAdmitted("release despite delete failure", map[string]bool{waiting.Name: true})
	if names := w.grantNames(); len(names) != 1 || names[0] != waiting.Name {
		t.Fatalf("released grant was not pruned: %v", names)
	}
	if !w.exists(&corev1.PersistentVolumeClaim{}, HomeClaimName(s.Name)) {
		t.Fatal("the retained volume disappeared")
	}
}
