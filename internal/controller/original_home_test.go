package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

func originalFixture() (*v1alpha1.AgentSession, *corev1.PersistentVolumeClaim, *corev1.PersistentVolume, homeguard.Binding) {
	s := taskSession()
	s.Spec.Agent = v1alpha1.AgentCodex
	s.Spec.Mode = v1alpha1.ModeRemote
	s.Spec.Model = "gpt-6.1-sol"
	s.Spec.Effort = "medium"
	s.Spec.Prompt = ""
	b := homeguard.Binding{Namespace: s.Namespace, Session: s.Name, SessionUID: string(s.UID), PVCUID: "home-uid", PV: "original-pv", PVUID: "pv-uid"}
	s.Annotations = map[string]string{homeguard.Annotation: b.String()}
	c := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: s.Namespace, Name: b.Claim(), UID: "home-uid", Finalizers: []string{Finalizer}, OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: b.PV}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
	v := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: b.PV, UID: "pv-uid", Finalizers: []string{"kubernetes.io/pv-protection"}}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: s.Namespace, Name: c.Name, UID: c.UID}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
	return s, c, v, b
}
func TestOriginalHomeRecreationAndRace(t *testing.T) {
	for _, race := range []string{"unchanged", "missing after acceptance", "deleting after acceptance", "changed before create"} {
		t.Run(race, func(t *testing.T) {
			s, c, v, b := originalFixture()
			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			writes := 0
			reads := 0
			gone := false
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, c, v).Build()
			cl := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
				writes++
				if _, ok := o.(*corev1.PersistentVolumeClaim); ok {
					t.Fatal("replacement HOME created")
				}
				return c.Create(ctx, o, opts...)
			}})
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if _, ok := o.(*corev1.PersistentVolumeClaim); ok && gone {
					return apierrors.NewNotFound(corev1.Resource("persistentvolumeclaims"), key.Name)
				}
				if _, ok := o.(*corev1.PersistentVolume); ok {
					reads++
					if race == "changed before create" && reads == 2 {
						if err := c.Get(ctx, key, o, opts...); err != nil {
							return err
						}
						o.(*corev1.PersistentVolume).UID = "replacement"
						return nil
					}
				}
				return c.Get(ctx, key, o, opts...)
			}})
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			if err := CheckOriginalHome(context.Background(), reader, s, &b, false); err != nil {
				t.Fatal("acceptance", err)
			}
			reads = 0
			if race == "missing after acceptance" {
				gone = true
			}
			if race == "deleting after acceptance" {
				if err := base.Delete(context.Background(), c); err != nil {
					t.Fatal(err)
				}
			} // existing finalizer preserves terminating claim
			r := &Reconciler{Client: cl, APIReader: reader, OriginalHome: &b}
			obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
			if err := r.ensure(context.Background(), s, exampleTemplates(t), obs); err != nil {
				t.Fatal(err)
			}
			if race == "unchanged" {
				if writes != 1 || obs.pod.obj == nil || len(obs.pod.obj.Spec.InitContainers) != 1 {
					t.Fatalf("successful original HOME: writes=%d blocked=%v build=%v", writes, obs.removalBlocked, obs.buildErr)
				}
				agent := obs.pod.obj.Spec.Containers[0]
				count := 0
				for _, e := range agent.Env {
					if e.Name == homeguard.Env {
						count++
						if e.Value != b.String() || e.ValueFrom != nil {
							t.Fatal("main guard binding differs from init")
						}
					}
				}
				if count != 1 {
					t.Fatal("ambiguous main guard environment")
				}
				init := obs.pod.obj.Spec.InitContainers[0]
				if !init.VolumeMounts[0].ReadOnly || len(init.EnvFrom) != 0 || init.SecurityContext.AllowPrivilegeEscalation == nil || *init.SecurityContext.AllowPrivilegeEscalation {
					t.Fatal("init authority changed")
				}
			} else if writes != 0 || obs.removalBlocked == nil {
				t.Fatalf("race admitted: writes=%d blocked=%v", writes, obs.removalBlocked)
			}
			var after corev1.PersistentVolumeClaim
			_ = base.Get(context.Background(), client.ObjectKeyFromObject(c), &after)
			if after.UID != c.UID || !reflect.DeepEqual(after.Spec, c.Spec) || !reflect.DeepEqual(after.Finalizers, c.Finalizers) {
				t.Fatal("HOME identity/retention changed")
			}
		})
	}
}
func TestOriginalHomeRetainsFinalizer(t *testing.T) {
	s, c, _, _ := originalFixture()
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if volumeRemovalAllowed(s, false, true) == nil {
		t.Fatal("guarded HOME can be archived")
	}
	if err := expiryVolumeRemovalAllowed(s, c); !errors.Is(err, homeguard.ErrUnsafe) {
		t.Fatal("private expiry did not honor original HOME preservation", err)
	}

}

func TestOriginalHomeStickyBindingWithStaleCache(t *testing.T) {
	s, c, v, _ := originalFixture()
	scheme := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, c, v).Build()
	stale := s.DeepCopy()
	stale.Annotations = nil
	r := &Reconciler{Client: cl, APIReader: cl} // startup config removed; cached Session predates admission
	obs := &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := r.ensure(context.Background(), stale, exampleTemplates(t), obs); err != nil {
		t.Fatal(err)
	}
	if obs.removalBlocked == nil {
		t.Fatal("stale Session bypassed sticky binding")
	}
	var pods corev1.PodList
	_ = cl.List(context.Background(), &pods)
	if len(pods.Items) != 0 {
		t.Fatal("unguarded pod created")
	}
}

// A real API server proves the part fake clients cannot: DELETE with the
// existing finalizer cannot free the PVC name, and a Bound claim cannot be
// rebound by changing volumeName. envtest has no kubelet or CSI driver.
func TestOriginalHomeNameReuseRaceEnvtest(t *testing.T) {
	s := newSession(t, nil)
	c, err := buildHomeClaim(s, exampleTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	c.Spec.VolumeName = "original-home-pv"
	if err := k8s.Create(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.Status.Phase = corev1.ClaimBound
	if err := k8s.Status().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	changed := c.DeepCopy()
	changed.Spec.VolumeName = "replacement-pv"
	if err := k8s.Update(context.Background(), changed); !apierrors.IsInvalid(err) {
		t.Fatalf("bound volumeName mutation: %v", err)
	}
	if err := k8s.Delete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	replacement := c.DeepCopy()
	replacement.ResourceVersion = ""
	replacement.UID = ""
	replacement.DeletionTimestamp = nil
	if err := k8s.Create(context.Background(), replacement); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("name reuse after DELETE: %v", err)
	}
	var retained corev1.PersistentVolumeClaim
	if err := k8s.Get(context.Background(), client.ObjectKeyFromObject(c), &retained); err != nil || retained.UID != c.UID || retained.DeletionTimestamp.IsZero() {
		t.Fatalf("original identity not anchored: %v", err)
	}
}

func TestOriginalHomeUnavailableReadsKeepControllerBackoff(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(fmt.Sprint(guarded), func(t *testing.T) {
			s, c, v, b := originalFixture()
			if !guarded {
				s.Annotations = nil
			}
			scheme := runtime.NewScheme()
			_ = v1alpha1.AddToScheme(scheme)
			_ = corev1.AddToScheme(scheme)
			base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(s, c, v).Build()
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			transient := errors.New("synthetic API observation unavailable")
			failures := 2
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				// The guarded path must retry failed PV reads as well as initial Session reads.
				_, isSession := o.(*v1alpha1.AgentSession)
				_, isPV := o.(*corev1.PersistentVolume)
				if failures > 0 && (!guarded && isSession || guarded && isPV) {
					failures--
					return transient
				}
				return c.Get(ctx, key, o, opts...)
			}})
			r := &Reconciler{Client: base, APIReader: reader}
			if guarded {
				r.OriginalHome = &b
			}
			observe := func() *observation {
				return &observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: c}}
			}
			for range 2 {
				if err := r.ensure(context.Background(), s, exampleTemplates(t), observe()); !errors.Is(err, transient) {
					t.Fatalf("read failure lost controller backoff: %v", err)
				}
			}
			obs := observe()
			if err := r.ensure(context.Background(), s, exampleTemplates(t), obs); err != nil || obs.pod.obj == nil {
				t.Fatalf("backoff retry did not recover: %v blocked=%v", err, obs.removalBlocked)
			}
			if got := len(obs.pod.obj.Spec.InitContainers); (got == 1) != guarded {
				t.Fatal("default-off guard shape changed", got)
			}
		})
	}
}
