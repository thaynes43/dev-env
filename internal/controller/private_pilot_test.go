package controller

import (
	"context"
	"testing"
	"time"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
	"github.com/thaynes43/dev-env/internal/sessiondirectory"
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
				if pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 1200 || pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != 5 {
					t.Fatal("pilot executor is not finite")
				}
				for _, mode := range []string{"restart", "deadline"} {
					bad := pod.DeepCopy()
					if mode == "restart" {
						bad.Spec.RestartPolicy = corev1.RestartPolicyAlways
					} else {
						bad.Spec.ActiveDeadlineSeconds = nil
					}
					if validateProjectCanaryExecutor(s, bad) == nil {
						t.Fatal("unbounded executor accepted", mode)
					}
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

func TestPrivatePilotExpiryAfterDirectoryBeforeHome(t *testing.T) {
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	b := testutil.NewPilot(t)
	s := b.Session(t, 0)
	s.Finalizers = append(s.Finalizers, Finalizer)
	s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{Version: 1, SessionUID: string(s.UID), HomeClaim: HomeClaimName(s.Name), BindingGeneration: 1, Admission: sharedStarted, Provider: "codex", StopState: "Unknown"}
	now := b.Now
	creates := 0
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(b.Objects(), s)...).WithStatusSubresource(&v1alpha1.AgentSession{}).WithInterceptorFuncs(interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.CreateOption) error {
		if d, ok := o.(*coord.SessionDirectory); ok {
			d.UID = "directory-original"
			d.CreationTimestamp = metav1.NewTime(b.Now)
			now = b.Policy.ExpiresAt
		} else {
			creates++
		}
		return c.Create(ctx, o, opts...)
	}}).Build()
	r := &Reconciler{Client: c, APIReader: c, Templates: client.ObjectKeyFromObject(b.Template), APIURL: "https://operator.example", PrivatePilot: b.Policy, ManagedCodexTasks: true, PrivateHistoryRetention: true, RetainedCommonRules: true, PrivateProjectCloneOwner: "thaynes43", Now: func() time.Time { return now }, SessionDirectory: &sessiondirectory.KubeStore{Client: c, Live: c, Namespace: "dev-env-system"}}
	obs := observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{missing: true}}
	if err := r.ensure(context.Background(), s, b.Templates, &obs); err != nil {
		t.Fatal(err)
	}
	if s.Status.SessionDirectory == nil || s.Status.SessionDirectory.State != "Confirmed" || obs.removalBlocked == nil || obs.buildErr != nil || creates != 0 {
		t.Fatalf("expiry fence not reached safely: directory=%+v blocked=%v build=%v resources=%d", s.Status.SessionDirectory, obs.removalBlocked, obs.buildErr, creates)
	}
}
