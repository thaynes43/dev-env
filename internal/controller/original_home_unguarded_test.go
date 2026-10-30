package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

// Without startup configuration or a sticky annotation, a replacement is
// unguarded at homeguard.Select. Existing PVC ownership still prevents mounting
// the original Session's HOME, and the legacy NameTaken condition records why.
func TestOriginalHomeReplacementWithoutBindingKeepsForeignPVC(t *testing.T) {
	s, claim, _, _ := originalFixture()
	s.UID, s.Annotations = "replacement-session", nil
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, claim).Build()
	r := &Reconciler{Client: cl, APIReader: cl}
	if b, err := r.originalHome(context.Background(), s); err != nil || b != nil {
		t.Fatalf("replacement should be unguarded: binding=%v err=%v", b, err)
	}
	ownedClaim, err := getOwned(context.Background(), cl, s, claim.Name, &corev1.PersistentVolumeClaim{})
	if err != nil || ownedClaim.foreign == "" {
		t.Fatalf("old HOME not recognized as foreign: %+v %v", ownedClaim, err)
	}
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: ownedClaim}
	if err := r.ensure(context.Background(), s, exampleTemplates(t), obs); err != nil || obs.buildErr != nil || obs.removalBlocked != nil {
		t.Fatalf("unexpected refusal path: err=%v build=%v blocked=%v", err, obs.buildErr, obs.removalBlocked)
	}
	_, ready := podPhase(s, *obs)
	if ready.Status != metav1.ConditionFalse || ready.Reason != "NameTaken" || !strings.Contains(ready.Message, "a volume named "+claim.Name) {
		t.Fatalf("ownership refusal missing: %+v", ready)
	}
	var pods corev1.PodList
	if err := cl.List(context.Background(), &pods); err != nil || len(pods.Items) != 0 {
		t.Fatalf("replacement mounted old HOME: pods=%d err=%v", len(pods.Items), err)
	}
	var after corev1.PersistentVolumeClaim
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(claim), &after); err != nil {
		t.Fatal(err)
	}
	after.ResourceVersion = claim.ResourceVersion
	if !reflect.DeepEqual(*claim, after) {
		t.Fatal("original HOME changed")
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
