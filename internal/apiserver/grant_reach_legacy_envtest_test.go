package apiserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/controller"
)

// Real local TokenRequest/TokenReview proof with the observed legacy actor and
// top-level requester metadata shapes. No S4 named identity, live cluster or
// provider credential is involved. Only the kubelet is supplied by the fixture.
func TestEnvtestLegacyDesignatedActorToTopLevelRequester(t *testing.T) {
	ctx := context.Background()
	ref := sessionNS + "/" + controller.ServiceAccountName
	var srv *Server
	l := startLiveWith(t, func(s *Server) {
		srv = s
		s.GrantDelegates = []string{ref}
		s.GrantPolicyDelegates = []string{ref}
		s.PolicyNamespace = "dev-env-system"
	})
	if err := l.k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "reach-legacy-fixture"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	materialize := func(name, parent, depth string) (*v1alpha1.AgentSession, *corev1.Pod, string) {
		s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name, Labels: map[string]string{v1alpha1.LabelDepth: depth}}, Spec: v1alpha1.AgentSessionSpec{Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5", Profile: "full", Prompt: "synthetic metadata shape", Parent: parent}}
		if err := l.k8s.Create(ctx, s); err != nil {
			t.Fatal(err)
		}
		s.Status.Phase = v1alpha1.PhaseRunning
		if err := l.k8s.Status().Update(ctx, s); err != nil {
			t.Fatal(err)
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: name, Labels: map[string]string{v1alpha1.LabelProfile: "full", v1alpha1.LabelSession: name}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: controller.ServiceAccountName, Containers: []corev1.Container{{Name: "agent", Image: "registry.example/synthetic:fixture"}}}}
		if err := l.k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status.Phase = corev1.PodRunning
		if err := l.k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		return s, pod, l.token(ref, []string{apiv1.TokenAudience}, pod)
	}
	materialize("reach-legacy-parent", "dev/dev-env", "0")
	actor, pod, tok := materialize("reach-legacy-claude", "reach-legacy-parent", "1")
	_, _, requester := materialize("reach-top-level-gpu", humanSA, "0")
	_, _, ordinary := materialize("reach-other-baseline", humanSA, "0")
	var sa corev1.ServiceAccount
	if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: controller.ServiceAccountName}, &sa); err != nil {
		t.Fatal(err)
	}
	exact := broker.LegacyDelegateBinding{ServiceAccount: ref, ServiceAccountUID: string(sa.UID), Session: actor.Name, SessionUID: string(actor.UID), PodName: pod.Name, PodUID: string(pod.UID)}
	srv.LegacyGrantDelegates = []broker.LegacyDelegateBinding{exact}
	req := apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"reach-legacy-fixture"}, TTL: "10m", Reason: "synthetic separate actor proof"}
	g := unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantsPath, requester, req, http.StatusCreated))
	expired, err := env.ExpiredServiceAccountToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), expired, decisionFor(g, true), http.StatusUnauthorized)
	// An ordinary holder of the SAME baseline account gains no decision authority.
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), ordinary, decisionFor(g, true), http.StatusForbidden)
	// Neither approval nor denial may consume the slot without covering authority.
	for _, approve := range []bool{true, false} {
		l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, approve), http.StatusForbidden)
	}
	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "legacy-reach-real-token"}, Spec: v1alpha1.GrantPolicySpec{Description: "synthetic", Type: v1alpha1.GrantKube, Delegated: &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}, Kube: &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"reach-legacy-fixture"}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if err := l.k8s.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	// Exact tuple and policy designation are independently necessary.
	srv.LegacyGrantDelegates = nil
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true), http.StatusForbidden)
	srv.LegacyGrantDelegates = []broker.LegacyDelegateBinding{exact}
	srv.GrantPolicyDelegates = nil
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true), http.StatusForbidden)
	srv.GrantPolicyDelegates = []string{ref}
	wrong := exact
	wrong.PodUID = "00000000-0000-0000-0000-000000000000"
	srv.LegacyGrantDelegates = []broker.LegacyDelegateBinding{wrong}
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true), http.StatusForbidden)
	srv.LegacyGrantDelegates = []broker.LegacyDelegateBinding{exact}
	got := unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true), http.StatusAccepted))
	if got.Phase != apiv1.GrantPending {
		t.Fatal("202 claimed Active")
	}
	b := &broker.Broker{Client: l.k8s, APIReader: l.k8s, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: []string{ref}, GrantPolicyDelegates: []string{ref}, LegacyGrantDelegates: []broker.LegacyDelegateBinding{exact}, Clock: clock.RealClock{}}
	key := types.NamespacedName{Namespace: sessionNS, Name: g.Name}
	step := func(n int) {
		for range n {
			if _, err := b.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
		}
	}
	step(4)
	var stored v1alpha1.AccessGrant
	if err := l.k8s.Get(ctx, key, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != v1alpha1.GrantActive || stored.Status.DelegateReach == nil || stored.Status.DelegateReach.PolicyUID != string(p.UID) {
		t.Fatalf("Active binding: %+v", stored.Status)
	}
	// A replaced actor Pod invalidates its real bound token and revokes the grant.
	if err := l.k8s.Delete(ctx, pod); err != nil {
		t.Fatal(err)
	}
	step(2)
	if err := l.k8s.Get(ctx, key, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != v1alpha1.GrantReleased {
		t.Fatalf("actor removal retained lease: %+v", stored.Status)
	}
}
