package apiserver

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/homeguard"
	rootfixture "github.com/thaynes43/dev-env/internal/homeguard/testutil"
)

// The name-only lifecycle route must consult the configured Session UID before
// any patch, even when a replacement carries no sticky binding annotation.
func TestOriginalHomeLifecycleReplacementUID(t *testing.T) {
	root := rootfixture.Load(t)
	b := root.Binding(t)
	for _, configured := range []bool{true, false} {
		t.Run(map[bool]string{true: "startup binding refuses", false: "no binding is unguarded"}[configured], func(t *testing.T) {
			var replacement v1alpha1.AgentSession
			var pvc corev1.PersistentVolumeClaim
			var pv corev1.PersistentVolume
			rootfixture.Decode(t, root.Session, &replacement)
			rootfixture.Decode(t, root.PVC, &pvc)
			rootfixture.Decode(t, root.PV, &pv)
			replacement.UID = "replacement-session-uid"
			replacement.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
			if _, sticky := replacement.Annotations[homeguard.Annotation]; sticky {
				t.Fatal("replacement unexpectedly admitted")
			}
			patches, storageReads := 0, 0
			f := newFixtureWith(t, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					switch obj.(type) {
					case *corev1.PersistentVolumeClaim, *corev1.PersistentVolume:
						storageReads++
					}
					return c.Get(ctx, key, obj, opts...)
				},
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patches++
					return c.Patch(ctx, obj, patch, opts...)
				},
			}, &replacement, &pvc, &pv)
			if configured {
				f.srv.OriginalHome = &b
			}
			var before v1alpha1.AgentSession
			key := client.ObjectKeyFromObject(&replacement)
			if err := f.c.Get(context.Background(), key, &before); err != nil {
				t.Fatal(err)
			}
			w := f.do(http.MethodPost, apiv1.SessionResumePath(b.Session), tokHuman, nil)
			var after v1alpha1.AgentSession
			if err := f.c.Get(context.Background(), key, &after); err != nil {
				t.Fatal(err)
			}
			if configured {
				e := wantError(t, w, http.StatusConflict, apiv1.CodeConflict)
				if e.Message != homeguard.ErrUnsafe.Error() || patches != 0 || storageReads != 0 || !reflect.DeepEqual(before, after) {
					t.Fatalf("wrong UID refusal path: error=%q patches=%d storage GETs=%d", e.Message, patches, storageReads)
				}
			} else if w.Code != http.StatusAccepted || patches != 1 || after.Spec.OperatingMode != v1alpha1.OperatingModeRunning || after.Annotations[homeguard.Annotation] != "" {
				t.Fatalf("unguarded replacement: status=%d patches=%d session=%+v", w.Code, patches, after)
			}
		})
	}
}

func TestOriginalHomeLifecycle(t *testing.T) {
	for _, bad := range []string{"unchanged", "missing", "foreign", "replaced", "deleting", "changed PV", "changed claimRef"} {
		t.Run(bad, func(t *testing.T) {
			b := homeguard.Binding{Namespace: sessionNS, Session: "remote-home", SessionUID: "session-uid", PVCUID: "pvc-uid", PV: "original-pv", PVUID: "pv-uid"}
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.Session, UID: "session-uid"}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentCodex, Mode: v1alpha1.ModeRemote, OperatingMode: v1alpha1.OperatingModeSuspended}}
			c := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: b.Claim(), UID: "pvc-uid", Finalizers: []string{homeguard.RescueFinalizer}, OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession", Name: s.Name, UID: s.UID, Controller: ptr.To(true)}}}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: b.PV}, Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound}}
			v := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: b.PV, UID: "pv-uid", Finalizers: []string{"kubernetes.io/pv-protection"}}, Spec: corev1.PersistentVolumeSpec{ClaimRef: &corev1.ObjectReference{APIVersion: "v1", Kind: "PersistentVolumeClaim", Name: c.Name, Namespace: sessionNS, UID: c.UID}}, Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound}}
			switch bad {
			case "foreign":
				c.OwnerReferences[0].UID = "foreign"
			case "replaced":
				c.UID = "new"
			case "deleting":
				at := metav1.Now()
				c.DeletionTimestamp = &at
			case "changed PV":
				v.UID = "new"
			case "changed claimRef":
				v.Spec.ClaimRef.UID = "new"
			}
			objs := []client.Object{s, v}
			if bad != "missing" {
				objs = append(objs, c)
			}
			f := newFixture(t, objs...)
			f.srv.OriginalHome = &b
			w := f.do(http.MethodPost, apiv1.SessionResumePath(s.Name), tokHuman, nil)
			if bad == "unchanged" {
				if w.Code != http.StatusAccepted {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				var live v1alpha1.AgentSession
				_ = f.c.Get(context.Background(), client.ObjectKeyFromObject(s), &live)
				if live.Annotations[homeguard.Annotation] != b.String() {
					t.Fatal("binding not durable")
				}
				f.srv.OriginalHome = nil
				if w := f.do(http.MethodPost, apiv1.SessionResumePath(s.Name), tokHuman, nil); w.Code != http.StatusOK {
					t.Fatalf("durable no-op %d", w.Code)
				}
				// A storage failure must block resume without blocking the existing stop route.
				if err := f.c.Delete(context.Background(), c); err != nil {
					t.Fatal(err)
				}
				if w := f.do(http.MethodPost, apiv1.SessionResumePath(s.Name), tokHuman, nil); w.Code != http.StatusConflict {
					t.Fatal("deleting HOME resume accepted")
				}
				if w := f.do(http.MethodPost, apiv1.SessionSuspendPath(s.Name), tokHuman, nil); w.Code != http.StatusAccepted {
					t.Fatal("unsafe HOME prevented stop")
				}

			} else if w.Code != http.StatusConflict {
				t.Fatalf("unsafe resume %d %s", w.Code, w.Body.String())
			}
			var claims corev1.PersistentVolumeClaimList
			_ = f.c.List(context.Background(), &claims)
			if bad == "missing" && len(claims.Items) != 0 {
				t.Fatal("replacement HOME")
			}
		})
	}
}
