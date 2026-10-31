package apiserver

import (
	"context"
	"net/http"
	"os"
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
	"github.com/thaynes43/dev-env/internal/templates"
)

// This combined test uses the real local API server's TokenRequest and
// TokenReview, not fakeAuth or a manually constructed caller. Only kubelet/pod
// materialization is supplied by the test; no installed cluster is contacted.
func TestEnvtestGrantDecisionNamedCoordinatorLaunch(t *testing.T) {
	ctx := context.Background()
	doc, err := os.ReadFile("../templates/testdata/templates.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := templates.Parse(map[string]string{templates.Key: string(doc)})
	if err != nil {
		t.Fatal(err)
	}
	tmpl.Profiles["coordinator"] = templates.Profile{ServiceAccount: leadSA}
	tmpl.Profiles["workbench"] = templates.Profile{ServiceAccount: templates.WorkbenchServiceAccount}
	l := startLiveWith(t, func(s *Server) {
		s.Policy.NamedCoordinatorIdentities = true
		s.Policy.WorkbenchServiceAccount = templates.WorkbenchServiceAccount
		s.Policy.Clients = append(s.Policy.Clients, sessionNS+"/"+templates.WorkbenchServiceAccount)
		s.GrantDelegates = []string{delegateSessionSA}
		s.Templates = func(context.Context) (*templates.Templates, error) { return tmpl, nil }
	})
	for _, account := range []string{templates.WorkbenchServiceAccount, leadSA} {
		if err := l.k8s.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: account}}); err != nil && !apierrors.IsAlreadyExists(err) {
			t.Fatal(err)
		}
	}
	if err := l.k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "frontend"}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	launch := func(tok string, req apiv1.CreateSessionRequest) string {
		return unmarshal[apiv1.Session](t, l.want(http.MethodPost, apiv1.SessionsPath, tok, req, http.StatusCreated)).Name
	}
	materialize := func(name, account, profile string) string {
		var sess v1alpha1.AgentSession
		if err := l.k8s.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &sess); err != nil {
			t.Fatal(err)
		}
		if account == leadSA && sess.Labels[templates.CoordinatorIdentityLabel] != leadSA {
			t.Fatal("launch did not admit named identity")
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, Labels: map[string]string{v1alpha1.LabelSession: name, v1alpha1.LabelProfile: profile}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(&sess, v1alpha1.GroupVersion.WithKind("AgentSession"))}}, Spec: corev1.PodSpec{ServiceAccountName: account, Containers: []corev1.Container{{Name: "agent", Image: "registry.example/synthetic:fixture"}}}}
		if err := l.k8s.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		pod.Status.Phase = corev1.PodRunning
		if err := l.k8s.Status().Update(ctx, pod); err != nil {
			t.Fatal(err)
		}
		sess.Status.Phase = v1alpha1.PhaseRunning
		if err := l.k8s.Status().Update(ctx, &sess); err != nil {
			t.Fatal(err)
		}
		return l.token(sessionNS+"/"+account, []string{apiv1.TokenAudience}, pod)
	}
	human := l.token(humanSA, []string{apiv1.TokenAudience}, nil)
	wbReq := task()
	wbReq.Profile = "workbench"
	wbName := launch(human, wbReq)
	wb := materialize(wbName, templates.WorkbenchServiceAccount, "workbench")
	lead := launch(wb, coordinatorProfileTask())
	leadTok := materialize(lead, leadSA, "coordinator")
	l.want(http.MethodPost, apiv1.SessionsPath, wb, coordinatorProfileTask(), http.StatusConflict)
	child := launch(leadTok, task())
	childTok := materialize(child, controller.ServiceAccountName, "full")
	kube := apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, TTL: "10m", Reason: "offline combined token proof"}
	request := func(tok string) apiv1.Grant {
		return unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantsPath, tok, kube, http.StatusCreated))
	}
	g := request(childTok)
	ancestor := request(wb)
	l.want(http.MethodPost, apiv1.GrantDecisionPath(ancestor.Name), leadTok, decisionFor(ancestor, true), http.StatusForbidden)
	sibling := launch(wb, task())
	siblingGrant := request(materialize(sibling, controller.ServiceAccountName, "full"))
	l.want(http.MethodPost, apiv1.GrantDecisionPath(siblingGrant.Name), leadTok, decisionFor(siblingGrant, true), http.StatusForbidden)
	plain := launch(human, task())
	plainTok := materialize(plain, controller.ServiceAccountName, "full")
	l.want(http.MethodPost, apiv1.SessionsPath, plainTok, coordinatorProfileTask(), http.StatusForbidden)
	unrelated := request(plainTok)
	l.want(http.MethodPost, apiv1.GrantDecisionPath(unrelated.Name), leadTok, decisionFor(unrelated, true), http.StatusForbidden)
	bad := decisionFor(g, true)
	bad.Digest = "stale"
	l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), leadTok, bad, http.StatusConflict)
	got := unmarshal[apiv1.Grant](t, l.want(http.MethodPost, apiv1.GrantDecisionPath(g.Name), leadTok, decisionFor(g, true), http.StatusAccepted))
	if got.Phase != apiv1.GrantPending {
		t.Fatalf("202 must not claim Active: %+v", got)
	}
	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "combined-real-token"}, Spec: v1alpha1.GrantPolicySpec{Description: "offline fixture", Type: v1alpha1.GrantKube, Delegated: &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}, Kube: &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if err := l.k8s.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	b := &broker.Broker{Client: l.k8s, APIReader: l.k8s, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: []string{delegateSessionSA}, Clock: clock.RealClock{}}
	key := types.NamespacedName{Namespace: sessionNS, Name: g.Name}
	step := func(n int) {
		for range n {
			if _, err := b.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
		}
	}
	step(3)
	var stored v1alpha1.AccessGrant
	if err := l.k8s.Get(ctx, key, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != v1alpha1.GrantActive || stored.Status.ApprovedBy != broker.DelegateRef(delegateSessionSA) {
		t.Fatalf("broker did not apply real-token decision: %+v", stored.Status)
	}
	b.GrantDelegates = nil
	step(2)
	if err := l.k8s.Get(ctx, key, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != v1alpha1.GrantReleased {
		t.Fatalf("revocation: %+v", stored.Status)
	}
	var sa corev1.ServiceAccount
	if err := l.k8s.Get(ctx, key, &sa); !apierrors.IsNotFound(err) {
		t.Fatalf("revoked identity remains: %v", err)
	}
}
