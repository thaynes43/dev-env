package controller

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

func TestOriginalHomeUnguardedSessionGETs(t *testing.T) {
	s := taskSession()
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s).Build()
	sessionGets, podGets := 0, 0
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		switch obj.(type) {
		case *v1alpha1.AgentSession:
			sessionGets++
		case *corev1.Pod:
			podGets++
		default:
			t.Fatalf("unexpected uncached read: %T", obj)
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	r := &Reconciler{Client: base, APIReader: reader}
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := r.ensure(context.Background(), s, exampleTemplates(t), obs); err != nil || obs.pod.obj == nil {
		t.Fatalf("unguarded creation failed: err=%v build=%v blocked=%v", err, obs.buildErr, obs.removalBlocked)
	}
	if sessionGets != 1 || podGets != 0 {
		t.Fatalf("creation GETs: Session=%d Pod=%d", sessionGets, podGets)
	}
	if _, _, err := r.archive(context.Background(), s, obs, false); err != nil {
		t.Fatal(err)
	}
	if sessionGets != 2 || podGets != 1 || obs.removalBlocked == nil {
		t.Fatalf("archive GETs: Session=%d Pod=%d blocked=%v", sessionGets, podGets, obs.removalBlocked)
	}
}

// Immutable Agent/Mode apply only to one UID. A stale unguarded Session and
// claim can remain cached after cleanup and same-name guarded replacement.
// Skipping the live Session GET based on that old spec would mount the new
// guarded HOME by name without its init verifier.
func TestOriginalHomeUnguardedCacheCannotBypassGuardedReplacement(t *testing.T) {
	live, claim, pv, _ := originalFixture()
	stale := taskSession()
	stale.UID = "old-unguarded-session"
	staleClaim := claim.DeepCopy()
	staleClaim.UID = "old-unguarded-home"
	staleClaim.OwnerReferences = []metav1.OwnerReference{ownerRef(stale)}
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live, claim, pv).Build()
	sessionGets := 0
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*v1alpha1.AgentSession); ok {
			sessionGets++
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	writes := 0
	writer := interceptor.NewClient(base, interceptor.Funcs{Create: func(_ context.Context, _ client.WithWatch, _ client.Object, _ ...client.CreateOption) error {
		writes++
		return errors.New("unexpected Create for replaced Session")
	}})
	r := &Reconciler{Client: writer, APIReader: reader} // flag removed; old cache has no binding
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: staleClaim}}
	if err := r.ensure(context.Background(), stale, exampleTemplates(t), obs); err != nil || !errors.Is(obs.removalBlocked, homeguard.ErrUnsafe) {
		t.Fatalf("stale creation not refused: err=%v blocked=%v", err, obs.removalBlocked)
	}
	if _, _, err := r.archive(context.Background(), stale, obs, true); !errors.Is(err, homeguard.ErrUnsafe) {
		t.Fatalf("stale archive not refused: %v", err)
	}
	if writes != 0 || sessionGets != 2 {
		t.Fatalf("replaced Session: writes=%d Session GETs=%d", writes, sessionGets)
	}
}
