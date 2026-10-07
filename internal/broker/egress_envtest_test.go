package broker

import (
	"context"
	"fmt"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/egress"
	"github.com/thaynes43/dev-env/internal/grantexpiry"
)

func egressRequest(g *v1alpha1.AccessGrant) {
	g.Spec.Type, g.Spec.Kube = v1alpha1.GrantEgress, nil
	g.Spec.Egress = &v1alpha1.EgressGrant{
		FQDNs: []string{"api.example.com", "*.example.net"}, CIDRs: []string{"192.0.2.8/32"},
		Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app.kubernetes.io/name": "grant-target"}}},
		Ports:     []v1alpha1.GrantPort{{Port: 8443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}},
	}
}

func newEgressPolicy(t *testing.T) {
	t.Helper()
	newPolicy(t, fmt.Sprintf("egress-%d", seq.Add(1)), func(p *v1alpha1.GrantPolicy) {
		p.Spec.Type, p.Spec.Kube = v1alpha1.GrantEgress, nil
		p.Spec.Egress = &v1alpha1.EgressPolicy{
			FQDNs: []string{"api.example.com", "*.example.net"}, CIDRs: []string{"192.0.2.8/32"},
			Endpoints: []v1alpha1.EgressEndpoint{{Namespace: "frontend", MatchLabels: map[string]v1alpha1.LabelValue{"app.kubernetes.io/name": "grant-target"}}},
			Ports:     []v1alpha1.GrantPort{{Port: 8443}, {Port: 53, Protocol: v1alpha1.ProtocolUDP}},
		}
	})
}

func waitEgressPolicy(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	u := egress.Object(sessionNS, name)
	eventually(t, "egress policy "+name, func() (bool, string) {
		err := admin.Get(context.Background(), client.ObjectKeyFromObject(u), u)
		return err == nil, fmt.Sprint(err)
	})
	return u
}

func TestBrokerEgressLifecycleAndRestart(t *testing.T) {
	suite(t)
	inst := &recorder{}
	r := startBroker(t, nil, inst)
	newEgressPolicy(t)
	s, pod := newSession(t)
	n := newGrant(t, s, egressRequest)
	u := waitEgressPolicy(t, n)
	g := waitPhase(t, n, v1alpha1.GrantActive)
	if !egress.Matches(u, sessionNS, g) || g.Status.ServiceAccount != "" || g.Status.InstalledPodUID != "" {
		t.Fatalf("egress policy or status is wrong: %+v", g.Status)
	}
	if installs, removes := inst.of(n); len(installs) != 0 || len(removes) != 0 || exists(t, sa(n)) {
		t.Fatal("egress used an installer or a ServiceAccount")
	}
	uid := u.GetUID()
	r.stop(t)
	restarted := startBroker(t, r.clock, inst)
	restarted.settle(t, n)
	if current := waitEgressPolicy(t, n); current.GetUID() != uid {
		t.Fatal("broker restart replaced the grant policy")
	}
	// A new session pod keeps the same session selector and policy UID. No
	// token reinstall is needed for an egress grant.
	if err := admin.Delete(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	newPod := runningPod(t, s)
	restarted.settle(t, n)
	if current := waitEgressPolicy(t, n); current.GetUID() != uid {
		t.Fatal("replacement pod replaced the grant policy")
	}
	// Release and a session ending revoke with the same policy path. These
	// requests are created before advancing the clock past PendingTimeout.
	released := newGrant(t, s, egressRequest)
	waitEgressPolicy(t, released)
	release(t, released)
	waitPhase(t, released, v1alpha1.GrantReleased)
	waitGone(t, egress.Object(sessionNS, released))
	s2, _ := newSession(t)
	ended := newGrant(t, s2, egressRequest)
	waitEgressPolicy(t, ended)
	if err := admin.Delete(context.Background(), s2); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, ended, v1alpha1.GrantReleased)
	waitGone(t, egress.Object(sessionNS, ended))
	restarted.clock.SetTime(g.Status.ExpiresAt.Add(time.Second))
	poke(t, n)
	waitPhase(t, n, v1alpha1.GrantExpired)
	waitGone(t, egress.Object(sessionNS, n))
	currentPod := newPod.DeepCopy()
	if err := admin.Get(context.Background(), client.ObjectKeyFromObject(newPod), currentPod); err != nil || currentPod.UID != newPod.UID {
		t.Fatalf("egress expiry touched the session pod: %v", err)
	}
	if installs, removes := inst.of(n); len(installs) != 0 || len(removes) != 0 {
		t.Fatal("egress installed or removed a token")
	}

}

func TestBrokerEgressCollisionAndPolicyFinalizer(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, nil)
	s, _ := newSession(t)
	ctx := context.Background()

	for _, foreign := range []bool{true, false} {
		n := newGrant(t, s, egressRequest)
		g := waitPhase(t, n, v1alpha1.GrantPending)
		u := egress.Policy(sessionNS, g)
		if foreign {
			u.SetLabels(map[string]string{v1alpha1.LabelGrant: "grant-another"})
		} else {
			// Owned source, but the existing policy opens a larger range.
			rules, _, err := unstructured.NestedSlice(u.Object, "spec", "egress")
			if err != nil {
				t.Fatal(err)
			}
			rules[1].(map[string]any)["toCIDR"] = []any{"0.0.0.0/0"}
			if err := unstructured.SetNestedSlice(u.Object, rules, "spec", "egress"); err != nil {
				t.Fatal(err)
			}
		}
		if err := admin.Create(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := r.b.Decide(ctx, n, Decision{Approve: true, By: "authentik/tom"}); err != nil {
			t.Fatal(err)
		}
		waitPhase(t, n, v1alpha1.GrantFailed)
		if foreign {
			if !exists(t, egress.Object(sessionNS, n)) {
				t.Fatal("foreign collision was deleted")
			}
			if err := admin.Delete(ctx, u); err != nil {
				t.Fatal(err)
			}
		} else {
			waitGone(t, egress.Object(sessionNS, n))
		}
	}

	newEgressPolicy(t)
	n := newGrant(t, s, egressRequest)
	u := waitEgressPolicy(t, n)
	u.SetFinalizers([]string{"test/policy-finalizer"})
	if err := admin.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	// Grant deletion cannot lift its finalizer while the policy still exists.
	g := grant(t, n)
	if err := admin.Delete(ctx, g); err != nil {
		t.Fatal(err)
	}
	eventually(t, "policy waits on its finalizer", func() (bool, string) {
		err := admin.Get(ctx, client.ObjectKeyFromObject(u), u)
		return err == nil && !u.GetDeletionTimestamp().IsZero(), fmt.Sprint(err)
	})
	g = grant(t, n)
	if !controllerutil.ContainsFinalizer(g, Finalizer) {
		t.Fatal("grant finalizer lifted before policy deletion")
	}
	u.SetFinalizers(nil)
	if err := admin.Update(ctx, u); err != nil {
		t.Fatal(err)
	}
	poke(t, n)
	waitGone(t, egress.Object(sessionNS, n), &v1alpha1.AccessGrant{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: n}})
}

func TestEgressExpiryBackstopAsOperator(t *testing.T) {
	suite(t)
	r := startBroker(t, nil, &recorder{})
	newEgressPolicy(t)
	s, pod := newSession(t)
	n := newGrant(t, s, egressRequest)
	waitEgressPolicy(t, n)
	g := waitPhase(t, n, v1alpha1.GrantActive)
	r.stop(t)
	// A real manager's AccessGrant informer reconstructs the timer on
	// startup; the same controller runs as the operator's exact ServiceAccount.
	mgr, err := ctrl.NewManager(impersonate(operatorUser), ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{sessionNS: {}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	expiry := &grantexpiry.Reconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), SessionNamespace: sessionNS, Clock: r.clock}
	if err := expiry.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	// Already expired when this operator starts: no broker event or CNP watch
	// can help it, and it has no permission to list or watch policies.
	r.clock.SetTime(g.Status.ExpiresAt.Add(time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(wait):
			t.Error("expiry manager did not stop")
		}
	})
	waitGone(t, egress.Object(sessionNS, n))
	if record := grant(t, n); record.Status.Phase != v1alpha1.GrantActive || !controllerutil.ContainsFinalizer(record, Finalizer) {
		t.Fatalf("operator changed broker status/finalizer: %+v", record.Status)
	}
	current := pod.DeepCopy()
	if err := admin.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: s.Name}, current); err != nil || current.UID != pod.UID {
		t.Fatalf("expiry backstop touched a session pod: %v", err)
	}
}

func TestEgressPolicyRBAC(t *testing.T) {
	suite(t)
	for _, verb := range []string{"get", "delete", "list", "watch", "create", "update", "patch", "deletecollection"} {
		ra := authorizationv1.ResourceAttributes{Verb: verb, Group: "cilium.io", Resource: "ciliumnetworkpolicies", Namespace: sessionNS, Name: "grant-egress-rbac"}
		want := verb == "get" || verb == "delete"
		if got := allowed(t, operatorUser, ra); got != want {
			t.Errorf("operator CNP %s allowed = %t, want %t", verb, got, want)
		}
	}
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	u := egress.Policy(sessionNS, &v1alpha1.AccessGrant{
		ObjectMeta: metav1.ObjectMeta{Name: "not-a-grant"},
		Spec:       v1alpha1.AccessGrantSpec{Requester: v1alpha1.GrantRequester{Session: "some-session"}, Egress: &v1alpha1.EgressGrant{CIDRs: []string{"192.0.2.8/32"}, Ports: tcp(8443)}},
	})
	if err := bc.Create(context.Background(), u); !apierrors.IsForbidden(err) {
		t.Errorf("broker guard admitted a policy not named grant-<id>: %v", err)
	}
}
