package controller

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestReconcileOfAnUnchangedSessionWritesNoStatus is haynes-ops#4663: a
// protected private Session that wants its pod gone, with no pod and no
// removal verdict of its own this reconcile (suspended inside its archive
// window), had RemovalBlocked removed and set again on every reconcile. The
// condition got a new lastTransitionTime each time, so every reconcile wrote
// the status, and each write's watch event queued the next reconcile. Once
// the queue took more than a second to come round, every write changed the
// stored object and the loop fed itself until the operator restarted
// (155 status PUTs a second for 13 hours on 2026-11-04).
func TestReconcileOfAnUnchangedSessionWritesNoStatus(t *testing.T) {
	transitioned := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	for _, tc := range []struct {
		name string
		edit func(*v1alpha1.AgentSession)
	}{
		// The storm's sessions: suspended this week, the archive not due, so
		// the reconcile sets no removal verdict and private history alone
		// sets RemovalBlocked.
		{"suspended inside the archive window", func(s *v1alpha1.AgentSession) {
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 168 * time.Hour}}
			s.Status.SuspendedAt = &metav1.Time{Time: time.Now().Add(-24 * time.Hour).Truncate(time.Second)}
		}},
		// The archive is due: archive() sets a removal verdict as well.
		{"suspended with the archive due", func(s *v1alpha1.AgentSession) {
			s.Spec.Lifecycle = &v1alpha1.Lifecycle{ArchiveAfter: &metav1.Duration{Duration: 168 * time.Hour}}
		}},
		{"reaped", func(s *v1alpha1.AgentSession) {
			at := metav1.NewTime(time.Now().Add(-time.Minute).Truncate(time.Second))
			s.DeletionTimestamp = &at
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, home := privateHistoryFixture()
			tc.edit(s)
			// The status the previous operator left: RemovalBlocked has been
			// True for an hour.
			s.Status.Conditions = []metav1.Condition{{Type: ConditionRemovalBlocked, Status: metav1.ConditionTrue, Reason: "RetentionPending",
				Message: "an earlier message", ObservedGeneration: s.Generation, LastTransitionTime: transitioned}}
			base := privateHistoryClient(s, home)
			writes := 0
			c := interceptor.NewClient(base, interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
					writes++
					return c.SubResource(sub).Update(ctx, o, opts...)
				},
				SubResourcePatch: func(ctx context.Context, c client.Client, sub string, o client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
					writes++
					return c.SubResource(sub).Patch(ctx, o, p, opts...)
				},
			})
			r := &Reconciler{Client: c, APIReader: base}
			reconcile := func() {
				t.Helper()
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
			}
			// The first reconcile may record what the fixture lacks.
			reconcile()
			var first v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &first); err != nil {
				t.Fatal(err)
			}
			c1 := meta.FindStatusCondition(first.Status.Conditions, ConditionRemovalBlocked)
			if c1 == nil || c1.Status != metav1.ConditionTrue {
				t.Fatalf("RemovalBlocked is not True after the first reconcile: %+v", first.Status.Conditions)
			}
			if !c1.LastTransitionTime.Equal(&transitioned) {
				t.Fatalf("RemovalBlocked stayed True but its lastTransitionTime moved from %s to %s", transitioned, c1.LastTransitionTime)
			}

			// Nothing changed since: no further reconcile writes the status,
			// a second later included.
			writes = 0
			reconcile()
			time.Sleep(1100 * time.Millisecond)
			reconcile()
			if writes != 0 {
				t.Fatalf("reconciles of an unchanged session wrote its status %d times", writes)
			}
			var last v1alpha1.AgentSession
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), &last); err != nil {
				t.Fatal(err)
			}
			if last.ResourceVersion != first.ResourceVersion {
				t.Fatalf("the session changed from resourceVersion %s to %s", first.ResourceVersion, last.ResourceVersion)
			}
		})
	}
}

// TestObserveKeepsTransitionTimesOfUnchangedConditions pins the rule observe
// follows whatever path sets a condition: lastTransitionTime moves with the
// status and only with it.
func TestObserveKeepsTransitionTimesOfUnchangedConditions(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))
	before := []metav1.Condition{
		{Type: "Same", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: old},
		{Type: "Flipped", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: old},
	}
	after := []metav1.Condition{
		{Type: "Same", Status: metav1.ConditionTrue, Reason: "B", LastTransitionTime: metav1.Now()},
		{Type: "Flipped", Status: metav1.ConditionFalse, Reason: "A", LastTransitionTime: metav1.Now()},
		{Type: "New", Status: metav1.ConditionTrue, Reason: "A", LastTransitionTime: metav1.Now()},
	}
	keepTransitionTimes(before, after)
	if !after[0].LastTransitionTime.Equal(&old) || after[0].Reason != "B" {
		t.Fatalf("an unchanged status lost its transition time or kept an old reason: %+v", after[0])
	}
	if after[1].LastTransitionTime.Equal(&old) || after[2].LastTransitionTime.Equal(&old) {
		t.Fatal("a changed or new condition kept an old transition time")
	}
}
