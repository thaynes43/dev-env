package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
)

func TestBrokerEgressLostCreateResponse(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakePassiveClock(now)
	g := egressGrant(v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)})
	g.Namespace = sessionNS
	g.Finalizers = []string{Finalizer}
	approve(g, "authentik/tom", time.Hour, metav1.NewTime(now))
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: g.Spec.Requester.Session}}
	sc := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(sc); err != nil {
		t.Fatal(err)
	}
	base := fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(g, s).Build()
	creates := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetObjectKind().GroupVersionKind() != egress.GVK {
			t.Fatalf("egress tried to create %T", obj)
		}
		creates++
		if err := c.Create(ctx, obj, opts...); err != nil {
			return err
		}
		return errors.New("API response lost after create")
	}})
	b := &Broker{Client: c, APIReader: base, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: clk}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
	if _, err := b.Reconcile(ctx, req); err == nil {
		t.Fatal("lost create response did not remain retryable")
	}
	u := egress.Object(sessionNS, g.Name)
	if err := base.Get(ctx, client.ObjectKeyFromObject(u), u); err != nil {
		t.Fatalf("the accepted policy is missing: %v", err)
	}
	// A restarted broker observes the accepted object, without making another
	// policy or needing a separately persisted materialization annotation.
	restarted := &Broker{Client: c, APIReader: base, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: clk}
	if _, err := restarted.Reconcile(ctx, req); err != nil || creates != 1 {
		t.Fatalf("restart did not recover the accepted create: %v, %d creates", err, creates)
	}
	clk.SetTime(now.Add(time.Hour))
	if _, err := restarted.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := base.Get(ctx, client.ObjectKeyFromObject(u), u); !apierrors.IsNotFound(err) {
		t.Fatalf("partial materialization was not revoked: %v", err)
	}
	var ended v1alpha1.AccessGrant
	if err := base.Get(ctx, req.NamespacedName, &ended); err != nil || ended.Status.Phase != v1alpha1.GrantExpired {
		t.Fatalf("expiry record = %+v, %v", ended.Status, err)
	}
}

// TestBrokerEgressRevokeUsesUIDAndRechecksReplacement is the broker half of
// D-64: both deletion paths name the policy UID they observed, and a
// replacement is deleted only after a new ownership check. The operator half
// is grantexpiry.TestExpiryUsesUIDPreconditionAndRechecksReplacement.
func TestBrokerEgressRevokeUsesUIDAndRechecksReplacement(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*unstructured.Unstructured)
		gone bool
	}{
		{
			name: "foreign source selector",
			edit: func(u *unstructured.Unstructured) {
				u.Object["spec"].(map[string]any)["endpointSelector"] = map[string]any{}
			},
			gone: false,
		},
		{
			name: "owned replacement",
			edit: func(*unstructured.Unstructured) {},
			gone: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
			clk := clocktesting.NewFakePassiveClock(now)
			g := egressGrant(v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)})
			g.Namespace = sessionNS
			g.Finalizers = []string{Finalizer}
			approve(g, "authentik/tom", time.Hour, metav1.NewTime(now))
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: g.Spec.Requester.Session}}
			u := egress.Policy(sessionNS, g)
			u.SetUID("owned-uid")
			sc := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(sc); err != nil {
				t.Fatal(err)
			}
			base := fake.NewClientBuilder().WithScheme(sc).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(g, s, u).Build()
			observed := egress.Object(sessionNS, g.Name)
			if err := base.Get(ctx, client.ObjectKeyFromObject(observed), observed); err != nil || observed.GetUID() == "" {
				t.Fatalf("owned policy was not stored: %v", err)
			}
			originalUID := observed.GetUID()
			var replacementUID string
			deletes := 0
			c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if obj.GetObjectKind().GroupVersionKind() != egress.GVK {
					t.Fatalf("revoke deleted %T", obj)
				}
				do := &client.DeleteOptions{}
				do.ApplyOptions(opts)
				if do.Preconditions == nil || do.Preconditions.UID == nil || *do.Preconditions.UID == "" {
					t.Fatal("the delete did not name the observed policy UID")
				}
				deletes++
				uid := string(*do.Preconditions.UID)
				if deletes == 1 {
					if uid != string(originalUID) {
						t.Fatalf("first delete named UID %s, want %s", uid, originalUID)
					}
					if err := c.Delete(ctx, obj); err != nil {
						return err
					}
					replacement := egress.Policy(sessionNS, g)
					replacement.SetUID("replaced-uid")
					tc.edit(replacement)
					if err := c.Create(ctx, replacement); err != nil {
						return err
					}
					stored := egress.Object(sessionNS, g.Name)
					if err := c.Get(ctx, client.ObjectKeyFromObject(stored), stored); err != nil {
						return err
					}
					replacementUID = string(stored.GetUID())
					if replacementUID == "" || replacementUID == string(originalUID) {
						t.Fatalf("replacement UID %q did not replace %s", replacementUID, originalUID)
					}
					return apierrors.NewConflict(schema.GroupResource{Group: "cilium.io", Resource: "ciliumnetworkpolicies"}, obj.GetName(), errors.New("UID changed"))
				}
				if !tc.gone {
					t.Fatal("foreign replacement was deleted")
				}
				if uid != replacementUID {
					t.Fatalf("fresh check named UID %s, want replacement %s", uid, replacementUID)
				}
				return c.Delete(ctx, obj, opts...)
			}})
			b := &Broker{Client: c, APIReader: base, SessionNamespace: sessionNS, PolicyNamespace: systemNS, Clock: clk}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)}
			clk.SetTime(g.Status.ExpiresAt.Time)
			if _, err := b.Reconcile(ctx, req); !apierrors.IsConflict(err) || deletes != 1 {
				t.Fatalf("UID conflict did not stay on the observed policy: %v, deletes=%d", err, deletes)
			}
			var pending v1alpha1.AccessGrant
			if err := base.Get(ctx, req.NamespacedName, &pending); err != nil || pending.Status.Phase != v1alpha1.GrantActive || pending.Status.EndedAt != nil {
				t.Fatalf("conflict claimed revocation: %v, %+v", err, pending.Status)
			}
			if _, err := b.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			err := base.Get(ctx, client.ObjectKeyFromObject(observed), egress.Object(sessionNS, g.Name))
			if apierrors.IsNotFound(err) != tc.gone {
				t.Fatalf("policy absent = %t, want %t (%v)", apierrors.IsNotFound(err), tc.gone, err)
			}
			var ended v1alpha1.AccessGrant
			if err := base.Get(ctx, req.NamespacedName, &ended); err != nil || ended.Status.Phase != v1alpha1.GrantExpired {
				t.Fatalf("expiry record = %+v, %v", ended.Status, err)
			}
			if tc.gone && deletes != 2 {
				t.Fatalf("owned replacement was not deleted after a fresh check: %d deletes", deletes)
			}
		})
	}
}
