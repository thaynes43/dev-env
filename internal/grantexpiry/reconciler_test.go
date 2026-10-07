package grantexpiry

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
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
)

func expiryGrant(now time.Time) *v1alpha1.AccessGrant {
	expires := metav1.NewTime(now.Add(time.Minute))
	return &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "grant-expiry-test", UID: "grant-uid"},
		Spec: v1alpha1.AccessGrantSpec{
			Type: v1alpha1.GrantEgress, Requester: v1alpha1.GrantRequester{Session: "session-test"},
			Egress: &v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: []v1alpha1.GrantPort{{Port: 8443}}},
		},
		Status: v1alpha1.AccessGrantStatus{Phase: v1alpha1.GrantActive, ExpiresAt: &expires},
	}
}

func expiryClient(t *testing.T, objs ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AccessGrant{}).WithObjects(objs...).Build()
}

func reconcile(t *testing.T, r *Reconciler, g *v1alpha1.AccessGrant) ctrl.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestExpiryTimerAndLateCreate(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clk := clocktesting.NewFakePassiveClock(now)
	g := expiryGrant(now)
	u := egress.Policy(g.Namespace, g)
	u.SetUID("policy-uid")
	c := expiryClient(t, g, u)
	r := &Reconciler{Client: c, APIReader: c, SessionNamespace: g.Namespace, Clock: clk}
	if res := reconcile(t, r, g); res.RequeueAfter != time.Minute {
		t.Fatalf("next check %s, want exact expiry", res.RequeueAfter)
	}
	// A fresh reconciler must reconstruct the remaining timer from the CR.
	clk.SetTime(now.Add(30 * time.Second))
	restarted := &Reconciler{Client: c, APIReader: c, SessionNamespace: g.Namespace, Clock: clk}
	if res := reconcile(t, restarted, g); res.RequeueAfter != 30*time.Second {
		t.Fatalf("restart timer = %s", res.RequeueAfter)
	}
	clk.SetTime(g.Status.ExpiresAt.Time)
	if res := reconcile(t, restarted, g); res.RequeueAfter != RetryInterval {
		t.Errorf("expiry needs a late-create recheck: %+v", res)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(u), egress.Object(g.Namespace, g.Name)); !apierrors.IsNotFound(err) {
		t.Fatalf("expired policy remains: %v", err)
	}
	// Active absence still schedules a retry. A broker create whose response
	// crossed expiry is deleted on the next pass, without changing the grant.
	if res := reconcile(t, restarted, g); res.RequeueAfter != RetryInterval {
		t.Errorf("absent Active policy stopped rechecking: %+v", res)
	}
	late := egress.Policy(g.Namespace, g)
	late.SetUID("late-policy-uid")
	if err := c.Create(context.Background(), late); err != nil {
		t.Fatal(err)
	}
	reconcile(t, restarted, g)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(late), egress.Object(g.Namespace, g.Name)); !apierrors.IsNotFound(err) {
		t.Fatalf("late policy remains: %v", err)
	}
	var record v1alpha1.AccessGrant
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(g), &record); err != nil || record.Status.Phase != v1alpha1.GrantActive {
		t.Fatalf("expiry backstop wrote the audit record: %v, %+v", err, record.Status)
	}
}

func TestExpiryLeavesForeignPoliciesAlone(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, edit := range []struct {
		name string
		fn   func(*unstructured.Unstructured)
	}{
		{"grant label", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelGrant] = "grant-other"
			u.SetLabels(ls)
		}},
		{"session label", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelSession] = "other"
			u.SetLabels(ls)
		}},
		{"manager label", func(u *unstructured.Unstructured) {
			ls := u.GetLabels()
			ls[v1alpha1.LabelManagedBy] = "other"
			u.SetLabels(ls)
		}},
		{"source selector", func(u *unstructured.Unstructured) {
			u.Object["spec"].(map[string]any)["endpointSelector"] = map[string]any{}
		}},
	} {
		t.Run(edit.name, func(t *testing.T) {
			g := expiryGrant(now)
			u := egress.Policy(g.Namespace, g)
			edit.fn(u)
			c := expiryClient(t, g, u)
			r := &Reconciler{Client: c, APIReader: c, SessionNamespace: g.Namespace, Clock: clocktesting.NewFakePassiveClock(now.Add(2 * time.Minute))}
			reconcile(t, r, g)
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(u), egress.Object(g.Namespace, g.Name)); err != nil {
				t.Errorf("foreign policy was deleted: %v", err)
			}
		})
	}
}

func TestExpiryUsesUIDPreconditionAndRechecksReplacement(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	g := expiryGrant(now)
	u := egress.Policy(g.Namespace, g)
	u.SetUID("owned-uid")
	base := expiryClient(t, g, u)
	deletes := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
		deletes++
		do := &client.DeleteOptions{}
		do.ApplyOptions(opts)
		if do.Preconditions == nil || do.Preconditions.UID == nil || *do.Preconditions.UID != "owned-uid" {
			t.Fatal("the delete did not name the observed policy UID")
		}
		// Simulate the API server replacing the object between Get and
		// Delete. A fake client does not enforce UID preconditions itself.
		if err := c.Delete(ctx, obj); err != nil {
			return err
		}
		replacement := egress.Policy(g.Namespace, g)
		replacement.SetUID("foreign-uid")
		replacement.SetLabels(map[string]string{v1alpha1.LabelGrant: "grant-other"})
		if err := c.Create(ctx, replacement); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: "cilium.io", Resource: "ciliumnetworkpolicies"}, obj.GetName(), errors.New("UID changed"))
	}})
	r := &Reconciler{Client: c, APIReader: base, SessionNamespace: g.Namespace, Clock: clocktesting.NewFakePassiveClock(now.Add(2 * time.Minute))}
	if res := reconcile(t, r, g); res.RequeueAfter != time.Second {
		t.Fatalf("replacement not rechecked promptly: %+v", res)
	}
	reconcile(t, r, g)
	if deletes != 1 {
		t.Fatalf("replacement ownership was not checked: %d deletes", deletes)
	}
}

func TestExpiryKeepsEndedAuditAndWaitsForPolicyFinalizer(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	g := expiryGrant(now)
	g.Status.Phase = v1alpha1.GrantExpired
	g.Finalizers = []string{v1alpha1.LabelPrefix + "grant-revoke"}
	u := egress.Policy(g.Namespace, g)
	u.SetUID(types.UID("policy-uid"))
	u.SetFinalizers([]string{"test/policy-finalizer"})
	c := expiryClient(t, g, u)
	r := &Reconciler{Client: c, APIReader: c, SessionNamespace: g.Namespace, Clock: clocktesting.NewFakePassiveClock(now.Add(2 * time.Minute))}
	if res := reconcile(t, r, g); res.RequeueAfter != RetryInterval {
		t.Fatalf("a finalizing policy stopped rechecking: %+v", res)
	}
	remaining := egress.Object(g.Namespace, g.Name)
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(u), remaining); err != nil || remaining.GetDeletionTimestamp().IsZero() {
		t.Fatalf("policy did not enter deletion: %v", err)
	}
	remaining.SetFinalizers(nil)
	if err := c.Update(context.Background(), remaining); err != nil {
		t.Fatal(err)
	}
	if res := reconcile(t, r, g); res.RequeueAfter != 0 {
		t.Errorf("ended, absent policy kept a timer: %+v", res)
	}
	var retained v1alpha1.AccessGrant
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(g), &retained); err != nil || len(retained.Finalizers) != 1 {
		t.Fatalf("operator changed grant retention/finalizer: %v, %v", err, retained.Finalizers)
	}
}

func TestExpiryScopeAndMissingDeadline(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		edit func(*v1alpha1.AccessGrant)
		gone bool
	}{
		{"kube grant", func(g *v1alpha1.AccessGrant) { g.Spec.Type = v1alpha1.GrantKube }, false},
		{"pending without deadline", func(g *v1alpha1.AccessGrant) { g.Status.Phase = v1alpha1.GrantPending; g.Status.ExpiresAt = nil }, false},
		{"active without deadline", func(g *v1alpha1.AccessGrant) { g.Status.ExpiresAt = nil }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := expiryGrant(now)
			c.edit(g)
			u := egress.Policy(g.Namespace, g)
			base := expiryClient(t, g, u)
			r := &Reconciler{Client: base, APIReader: base, SessionNamespace: g.Namespace, Clock: clocktesting.NewFakePassiveClock(now.Add(2 * time.Minute))}
			reconcile(t, r, g)
			err := base.Get(context.Background(), client.ObjectKeyFromObject(u), egress.Object(g.Namespace, g.Name))
			if apierrors.IsNotFound(err) != c.gone {
				t.Errorf("policy absent = %t, want %t (error %v)", apierrors.IsNotFound(err), c.gone, err)
			}
		})
	}
}
