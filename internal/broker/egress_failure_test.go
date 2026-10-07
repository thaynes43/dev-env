package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
