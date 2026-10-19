package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// ADR-008: with retention on, only a task session is newly marked. A marked
// session of any mode stays protected, and so does a canary.
func TestPrivateHistoryIsNewlyMarkedOnlyForTaskSessions(t *testing.T) {
	for _, tc := range []struct {
		mode   v1alpha1.SessionMode
		marked bool
	}{{v1alpha1.ModeTask, true}, {v1alpha1.ModeRemote, false}, {v1alpha1.ModeLocal, false}} {
		t.Run(string(tc.mode), func(t *testing.T) {
			s := taskSession()
			s.Generation = 1
			s.Spec.Mode = tc.mode
			if tc.mode != v1alpha1.ModeTask {
				s.Spec.Prompt = ""
			}
			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: templatesKey.Name, Namespace: templatesKey.Namespace}, Data: map[string]string{templates.Key: exampleDoc}}
			c := privateHistoryClient(s, cm)
			r := &Reconciler{Client: c, APIReader: c, Templates: templatesKey, PrivateHistoryRetention: true}
			if got := r.privateHistoryEnabled(s); got != tc.marked {
				t.Fatalf("privateHistoryEnabled = %v, want %v", got, tc.marked)
			}
			for range 3 {
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
					t.Fatal(err)
				}
			}
			var got v1alpha1.AgentSession
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), &got); err != nil {
				t.Fatal(err)
			}
			if (got.Status.PrivateHistory != nil) != tc.marked {
				t.Fatalf("marked = %v, want %v", got.Status.PrivateHistory != nil, tc.marked)
			}
		})
	}
}

func TestPrivateHistoryAlreadyMarkedInteractiveSessionStaysProtected(t *testing.T) {
	for _, mode := range []v1alpha1.SessionMode{v1alpha1.ModeRemote, v1alpha1.ModeLocal} {
		s, _ := privateHistoryFixture()
		s.Spec.Mode = mode
		// Flag off, as after an operator rebuild: the latch alone protects it.
		r := &Reconciler{}
		if !privateHistoryProtected(s) || !r.privateHistoryEnabled(s) {
			t.Fatalf("a marked %s session lost its protection", mode)
		}
	}
}
