package apiserver

import (
	"context"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/controller"
	"github.com/thaynes43/dev-env/internal/templates"
)

// launchedSessionToken stands in only for controller pod materialization and
// TokenReview's authentication. The session and admission record come from the
// actual HTTP launch; classification, grant requests and decisions use routes.
func launchedSessionToken(t *testing.T, f *fixture, name string) string {
	t.Helper()
	s := f.session(name)
	account := controller.ServiceAccountName
	if admitted := s.Labels[templates.CoordinatorIdentityLabel]; admitted != "" {
		account = admitted
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sessionNS, UID: uuid.NewUUID(),
		Labels:          map[string]string{v1alpha1.LabelSession: name, v1alpha1.LabelProfile: "full"},
		OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(s, v1alpha1.GroupVersion.WithKind("AgentSession"))}},
		Spec: corev1.PodSpec{ServiceAccountName: account}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	if account != controller.ServiceAccountName {
		pod.Labels[v1alpha1.LabelProfile] = "coordinator"
	}
	if err := f.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	setPhase(t, f, name, v1alpha1.PhaseRunning)
	tok := "tok-launched-" + name
	id := saIdentity(sessionNS + "/" + account)
	id.PodName, id.PodUID = name, string(pod.UID)
	f.auth[tok] = id
	return tok
}

func combinedDelegateFixture(t *testing.T) (*fixture, string, string, string, apiv1.Grant) {
	t.Helper()
	f := newFixtureWith(t, interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
		if obj.GetUID() == "" {
			obj.SetUID(uuid.NewUUID())
		}
		return c.Create(ctx, obj, opts...)
	}})
	withCoordinatorProfile(f)
	wb := workbenchSessionToken(t, f, "workbench-root")
	f.srv.GrantDelegates = []string{delegateSessionSA}
	f.h = f.srv.Handler()
	plain := f.sessionPod("ordinary-root", "full", 0)
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, plain, coordinatorProfileTask()), http.StatusForbidden, apiv1.CodeForbidden)
	w := f.do(http.MethodPost, apiv1.SessionsPath, wb, coordinatorProfileTask())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	lead := decode[apiv1.Session](t, w).Name
	if s := f.session(lead); s.Spec.Parent != "workbench-root" || s.Labels[v1alpha1.LabelDepth] != "1" || !f.srv.Policy.NamedCoordinatorSessionEligible(s) {
		t.Fatalf("launch: %+v", s)
	}
	leadTok := launchedSessionToken(t, f, lead)
	c, err := f.srv.resolveCaller(context.Background(), f.auth[leadTok])
	if err != nil || c.kind != kindSession || c.session.Name != lead {
		t.Fatalf("token resolution: %+v %v", c, err)
	}
	wantError(t, f.do(http.MethodPost, apiv1.SessionsPath, wb, coordinatorProfileTask()), http.StatusConflict, apiv1.CodeConflict)
	w = f.do(http.MethodPost, apiv1.SessionsPath, leadTok, task())
	if w.Code != http.StatusCreated {
		t.Fatal(w.Code, w.Body.String())
	}
	child := decode[apiv1.Session](t, w).Name
	if s := f.session(child); s.Spec.Parent != lead || s.Labels[templates.CoordinatorIdentityLabel] != "" {
		t.Fatalf("child inherited identity: %+v", s)
	}
	childTok := launchedSessionToken(t, f, child)
	g := f.request(childTok, apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, TTL: "1h", Reason: "synthetic child grant"}, http.StatusCreated)
	p := &v1alpha1.GrantPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-env-system", Name: "combined-child"}, Spec: v1alpha1.GrantPolicySpec{Description: "synthetic", Type: v1alpha1.GrantKube, Delegated: &v1alpha1.DelegatedRequesters{Profiles: []string{"full"}}, Kube: &v1alpha1.KubePolicy{Roles: []string{v1alpha1.RoleWorkloads}, Namespaces: []string{"frontend"}}, MaxTTL: metav1.Duration{Duration: time.Hour}}}
	if err := f.c.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	return f, wb, leadTok, lead, g
}

func combinedBroker(f *fixture) *broker.Broker {
	return &broker.Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: f.srv.GrantDelegates, Clock: clocktesting.NewFakeClock(f.now)}
}

func reconcileCombined(t *testing.T, f *fixture, b *broker.Broker, name string, n int) {
	t.Helper()
	for range n {
		if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: sessionNS, Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGrantDecisionCombinedNamedCoordinator(t *testing.T) {
	f, _, tok, lead, g := combinedDelegateFixture(t)
	for _, other := range []apiv1.Grant{
		f.putGrant(lead, nil),
		f.putGrant("workbench-root", nil),
		f.childGrant("lead-sibling", "workbench-root", 1),
		f.childGrant("unrelated", "dev/dev-env", 0),
	} {
		wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(other.Name), tok, decisionFor(other, true)), http.StatusForbidden, apiv1.CodeForbidden)
	}
	bad := decisionFor(g, true)
	bad.Digest = "stale"
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, bad), http.StatusConflict, apiv1.CodeConflict)
	w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true))
	if w.Code != http.StatusAccepted || decode[apiv1.Grant](t, w).Phase != apiv1.GrantPending {
		t.Fatalf("202 records Pending: %d %s", w.Code, w.Body.String())
	}
	b := combinedBroker(f)
	reconcileCombined(t, f, b, g.Name, 3)
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantActive || st.ApprovedBy != broker.DelegateRef(delegateSessionSA) {
		t.Fatalf("broker: %+v", st)
	}
	b.GrantDelegates = nil
	reconcileCombined(t, f, b, g.Name, 2)
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantReleased {
		t.Fatalf("revocation: %+v", st)
	}
	var sa corev1.ServiceAccount
	if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: g.Name}, &sa); err == nil {
		t.Fatal("revoked grant identity remains")
	}
}

// A race after the API's 202 cannot turn a stale or duplicated delegate into
// an issued grant. The active path applies the same checks and removes its SA.
func TestGrantDecisionCombinedRechecksDelegate(t *testing.T) {
	changes := map[string]func(*fixture, string){
		"pod gone": func(f *fixture, lead string) {
			var p corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: lead}, &p); err != nil {
				f.t.Fatal(err)
			}
			if err := f.c.Delete(context.Background(), &p); err != nil {
				f.t.Fatal(err)
			}
		},
		"suspended": func(f *fixture, lead string) { setPhase(f.t, f, lead, v1alpha1.PhaseSuspended) },
		"requester leaves tree": func(f *fixture, lead string) {
			var sessions v1alpha1.AgentSessionList
			if err := f.c.List(context.Background(), &sessions, client.InNamespace(sessionNS)); err != nil {
				f.f.t.Fatal(err)
			}
			for i := range sessions.Items {
				if sessions.Items[i].Spec.Parent == lead {
					sessions.Items[i].Spec.Parent = "dev/dev-env"
					f.mustUpdate(&sessions.Items[i])
				}
			}
		},
		"another holder": func(f *fixture, _ string) {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "racing-holder", Namespace: sessionNS, Labels: map[string]string{templates.CoordinatorIdentityLabel: leadSA}}}
			if err := f.c.Create(context.Background(), s); err != nil {
				f.t.Fatal(err)
			}
		},
	}
	for name, change := range changes {
		for _, active := range []bool{false, true} {
			suffix := " before apply"
			if active {
				suffix = " while active"
			}
			t.Run(name+suffix, func(t *testing.T) {
				f, _, tok, lead, g := combinedDelegateFixture(t)
				w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true))
				if w.Code != http.StatusAccepted {
					t.Fatal(w.Code, w.Body.String())
				}
				b := combinedBroker(f)
				if active {
					reconcileCombined(t, f, b, g.Name, 3)
				}
				change(f, lead)
				reconcileCombined(t, f, b, g.Name, 3)
				st := f.grant(g.Name).Status
				if active {
					if st.Phase != v1alpha1.GrantReleased {
						t.Fatalf("not revoked: %+v", st)
					}
				} else if st.Phase != v1alpha1.GrantPending || st.ApprovedBy != "" {
					t.Fatalf("stale delegate issued: %+v", st)
				}
				var sa corev1.ServiceAccount
				if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: g.Name}, &sa); err == nil {
					t.Fatal("grant identity exists after refusal/revoke")
				}
			})
		}
	}
}
