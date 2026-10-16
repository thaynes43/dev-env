package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestPrivatePilotControllerExpiryDriftAndPreservedPeer(t *testing.T) {
	for _, kind := range []string{"valid", "expired", "disabled", "missing-marker", "expiry-during-read"} {
		t.Run(kind, func(t *testing.T) {
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			b := testutil.NewPilot(t)
			s := b.Session(t, 0)
			now := b.Now
			objects := append(b.Objects(), s)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.AgentSession{}).WithInterceptorFuncs(interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
				if kind == "expiry-during-read" && key.Name == projectcanary.PilotSealName {
					now = b.Policy.ExpiresAt
				}
				return c.Get(ctx, key, o, opts...)
			}}).Build()
			r := &Reconciler{Client: c, APIReader: c, Templates: client.ObjectKeyFromObject(b.Template), APIURL: "https://operator.example", PrivatePilot: b.Policy, ManagedCodexTasks: true, PrivateHistoryRetention: true, RetainedCommonRules: true, PrivateProjectCloneOwner: "thaynes43", Now: func() time.Time { return now }}
			switch kind {
			case "expired":
				now = b.Policy.ExpiresAt
			case "disabled":
				r.PrivatePilot = nil
			case "missing-marker":
				delete(s.Annotations, projectcanary.PilotAnnotation)
			}
			err := r.validateProjectCanary(context.Background(), s, b.Templates)
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				pod, e := buildManagedPod(s, b.Templates, r.APIURL, true, false, nil)
				if e != nil {
					t.Fatal("pilot cannot render", e)
				}
				for _, v := range pod.Spec.Volumes {
					if v.Name == "shared" {
						t.Fatal("pilot exposes shared scratch")
					}
				}

			} else if err == nil {
				t.Fatal("pending pilot escaped fence")
			}
			if kind == "expired" || kind == "disabled" {
				obs := observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
				if err := r.ensure(context.Background(), s, b.Templates, &obs); err != nil || obs.removalBlocked == nil {
					t.Fatal("expired ensure not blocked", err)
				}
				peer := &corev1.Pod{}
				peer.Name = s.Name
				peer.UID = "unchanged-peer"
				kept := observation{pod: owned[*corev1.Pod]{obj: peer}}
				if err := r.ensure(context.Background(), s, b.Templates, &kept); err != nil || kept.pod.obj.UID != "unchanged-peer" {
					t.Fatal("present peer changed", err)
				}
				var pods corev1.PodList
				var claims corev1.PersistentVolumeClaimList
				if err := c.List(context.Background(), &pods); err != nil {
					t.Fatal(err)
				}
				if err := c.List(context.Background(), &claims); err != nil {
					t.Fatal(err)
				}
				if len(pods.Items) != 0 || len(claims.Items) != 0 {
					t.Fatal("expired resources created")
				}
			}
			s.Status.PrivateHistory = nil
			if !privateHistoryProtected(s) {
				t.Fatal("early HOME guard lost")
			}
		})
	}
}
