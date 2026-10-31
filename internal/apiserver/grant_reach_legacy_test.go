package apiserver

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/controller"

	"github.com/thaynes43/dev-env/internal/broker"
)

func TestPolicyReachAPIRequiresExplicitDesignation(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(map[bool]string{true: "approve", false: "deny"}[approve], func(t *testing.T) {
			f, _, g := delegateFixture(t)
			f.reparent("requester", humanSA)
			reachRunning(t, f, "requester")
			host, err := f.srv.resolveCaller(context.Background(), f.auth[tokCoordinator])
			if err != nil {
				t.Fatal(err)
			}
			if status, _ := f.decideAs(host, g.Name, decisionFor(g, approve)); status != http.StatusForbidden {
				t.Fatalf("ordinary tree delegate reached unrelated requester: %d", status)
			}
			f.srv.GrantPolicyDelegates = []string{coordinatorSA}
			if status, err := f.decideErr(host, g.Name, decisionFor(g, approve)); status != http.StatusAccepted {
				t.Fatalf("designated delegate: %d %+v", status, err)
			}
			d, ok := f.decisionAnnotation(g.Name)
			if !ok || d.Reach == nil || d.Reach.Mode != "policy" || d.Reach.PolicyUID == "" || d.Reach.PolicyDigest == "" || d.By != broker.DelegateRef(coordinatorSA) {
				t.Fatalf("unbound decision: %+v", d)
			}
		})
	}
}

func reachRunning(t *testing.T, f *fixture, name string) {
	t.Helper()
	ctx := context.Background()
	s := f.session(name)
	s.Status.Phase = v1alpha1.PhaseRunning
	if err := f.c.Status().Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	var p corev1.Pod
	if err := f.c.Get(ctx, types.NamespacedName{Namespace: sessionNS, Name: name}, &p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodRunning
	p.Labels[v1alpha1.LabelSession] = name
	if err := f.c.Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = corev1.PodRunning
	if err := f.c.Status().Update(ctx, &p); err != nil {
		t.Fatal(err)
	}
}

// These fixtures preserve the observed metadata shapes: the actor is a full
// Claude depth1 session under a legacy baseline-SA parent (not S4-admitted);
// the separate requester is full depth0, immediately created by the human SA.
// UUIDs/tokens are synthetic; no running session or provider credential is read.
func legacyReachFixture(t *testing.T) (*fixture, string, apiv1.Grant) {
	t.Helper()
	ctx := context.Background()
	f, _, _ := delegateFixture(t)
	f.sessionPod("legacy-parent", "full", 0)
	f.reparent("legacy-parent", "dev/dev-env")
	ancestor := f.session("legacy-parent")
	ancestor.CreationTimestamp = metav1.NewTime(f.now.Add(-time.Hour))
	if err := f.c.Update(ctx, ancestor); err != nil {
		t.Fatal(err)
	}
	tok := f.sessionPod("legacy-claude", "full", 1)
	f.reparent("legacy-claude", "legacy-parent")
	requester := f.sessionPod("top-level-gpu", "full", 0)
	reachRunning(t, f, "legacy-claude")
	reachRunning(t, f, "top-level-gpu")
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: sessionNS, Name: controller.ServiceAccountName, UID: uuid.NewUUID()}}
	if err := f.c.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	id := f.auth[tok]
	id.ServiceAccountUID = string(sa.UID)
	f.auth[tok] = id
	sess := f.session("legacy-claude")
	f.srv.GrantDelegates = []string{sessionNS + "/" + controller.ServiceAccountName}
	f.srv.GrantPolicyDelegates = append([]string(nil), f.srv.GrantDelegates...)
	f.srv.LegacyGrantDelegates = []broker.LegacyDelegateBinding{{ServiceAccount: f.srv.GrantDelegates[0], ServiceAccountUID: string(sa.UID), Session: sess.Name, SessionUID: string(sess.UID), PodName: id.PodName, PodUID: id.PodUID}}
	f.h = f.srv.Handler()
	g := f.request(requester, apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleWorkloads, Namespaces: []string{"frontend"}, TTL: "1h", Reason: "synthetic top-level GPU-shaped requester"}, http.StatusCreated)
	f.addDelegatedPolicy(f.grant(g.Name))
	return f, tok, g
}

func TestLegacyDesignatedActorToTopLevelRequester(t *testing.T) {
	f, tok, g := legacyReachFixture(t)
	w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true))
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	d, ok := f.decisionAnnotation(g.Name)
	if !ok || d.Delegate.Form != 3 || d.Reach == nil || d.Reach.AuthorityKind != "delegatedPolicy" || d.Reach.Mode != "policy" || d.Delegate.ServiceAccountUID == "" {
		t.Fatalf("binding: %+v", d)
	}
	b := &broker.Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: f.srv.GrantDelegates, GrantPolicyDelegates: f.srv.GrantPolicyDelegates, LegacyGrantDelegates: f.srv.LegacyGrantDelegates, Clock: clocktesting.NewFakeClock(f.now)}
	key := types.NamespacedName{Namespace: sessionNS, Name: g.Name}
	for range 4 {
		if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantActive || st.DelegateReach == nil {
		t.Fatalf("not Active: %+v", st)
	}
	// Removing the exact tuple revokes without changing the shared SA or sessions.
	b.LegacyGrantDelegates = nil
	for range 2 {
		if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
	}
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantReleased {
		t.Fatalf("not revoked: %+v", st)
	}
	var sa corev1.ServiceAccount
	if err := f.c.Get(context.Background(), key, &sa); !apierrors.IsNotFound(err) {
		t.Fatalf("grant identity remains: %v", err)
	}
}

func TestLegacyExactActorRefusals(t *testing.T) {
	cases := map[string]func(*fixture, *string, apiv1.Grant){
		"no authority":   func(f *fixture, tok *string, g apiv1.Grant) { f.srv.LegacyGrantDelegates = nil },
		"no designation": func(f *fixture, tok *string, g apiv1.Grant) { f.srv.GrantPolicyDelegates = nil },
		"wrong session UID": func(f *fixture, tok *string, g apiv1.Grant) {
			f.srv.LegacyGrantDelegates[0].SessionUID = string(uuid.NewUUID())
		},
		"wrong pod UID": func(f *fixture, tok *string, g apiv1.Grant) {
			f.srv.LegacyGrantDelegates[0].PodUID = string(uuid.NewUUID())
		},
		"wrong SA UID": func(f *fixture, tok *string, g apiv1.Grant) {
			f.srv.LegacyGrantDelegates[0].ServiceAccountUID = string(uuid.NewUUID())
		},
		"wrong SA": func(f *fixture, tok *string, g apiv1.Grant) {
			f.srv.LegacyGrantDelegates[0].ServiceAccount = "dev-agents/another"
		},
		"other baseline session": func(f *fixture, tok *string, g apiv1.Grant) {
			*tok = f.sessionPod("other-baseline", "full", 1)
			reachRunning(f.t, f, "other-baseline")
			id := f.auth[*tok]
			id.ServiceAccountUID = f.srv.LegacyGrantDelegates[0].ServiceAccountUID
			f.auth[*tok] = id
		},
		"stale token pod UID": func(f *fixture, tok *string, g apiv1.Grant) {
			id := f.auth[*tok]
			id.PodUID = string(uuid.NewUUID())
			f.auth[*tok] = id
		},
		"requester descendant actor": func(f *fixture, tok *string, g apiv1.Grant) { f.reparent("legacy-claude", "top-level-gpu") },
		"ambiguous actor ancestry":   func(f *fixture, tok *string, g apiv1.Grant) { f.reparent("legacy-claude", "missing-parent") },
		"self": func(f *fixture, tok *string, g apiv1.Grant) {
			stored := f.grant(g.Name)
			stored.Spec.Requester.Session = "legacy-claude"
			stored.Spec.Requester.SessionUID = f.session("legacy-claude").UID
			if err := f.c.Update(context.Background(), stored); err != nil {
				f.t.Fatal(err)
			}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			f, tok, g := legacyReachFixture(t)
			edit(f, &tok, g)
			f.h = f.srv.Handler()
			w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true))
			if w.Code != http.StatusForbidden {
				t.Fatalf("expected refusal, got %d: %s", w.Code, w.Body.String())
			}
			if _, ok := f.decisionAnnotation(g.Name); ok {
				t.Fatal("refusal consumed decision slot")
			}
		})
	}
}

func TestLegacyIdentityChangesRefuseApplyAndRevoke(t *testing.T) {
	changes := map[string]func(*fixture, *broker.Broker){
		"tuple removed":     func(f *fixture, b *broker.Broker) { b.LegacyGrantDelegates = nil },
		"tuple session UID": func(f *fixture, b *broker.Broker) { b.LegacyGrantDelegates[0].SessionUID = string(uuid.NewUUID()) },
		"tuple pod UID":     func(f *fixture, b *broker.Broker) { b.LegacyGrantDelegates[0].PodUID = string(uuid.NewUUID()) },
		"SA replaced": func(f *fixture, b *broker.Broker) {
			var sa corev1.ServiceAccount
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: controller.ServiceAccountName}, &sa); err != nil {
				f.t.Fatal(err)
			}
			sa.UID = uuid.NewUUID()
			if err := f.c.Update(context.Background(), &sa); err != nil {
				f.t.Fatal(err)
			}
		},
		"actor session replaced": func(f *fixture, b *broker.Broker) {
			s := f.session("legacy-claude")
			s.UID = uuid.NewUUID()
			if err := f.c.Update(context.Background(), s); err != nil {
				f.t.Fatal(err)
			}
		},
		"actor pod SA changed": func(f *fixture, b *broker.Broker) {
			var p corev1.Pod
			if err := f.c.Get(context.Background(), types.NamespacedName{Namespace: sessionNS, Name: "legacy-claude"}, &p); err != nil {
				f.t.Fatal(err)
			}
			p.Spec.ServiceAccountName = "another"
			if err := f.c.Update(context.Background(), &p); err != nil {
				f.t.Fatal(err)
			}
		},
	}
	for name, edit := range changes {
		for _, active := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/apply", true: "/Active"}[active], func(t *testing.T) {
				f, tok, g := legacyReachFixture(t)
				if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true)); w.Code != http.StatusAccepted {
					t.Fatal(w.Code, w.Body.String())
				}
				b := &broker.Broker{Client: f.c, APIReader: f.c, SessionNamespace: sessionNS, PolicyNamespace: "dev-env-system", GrantDelegates: f.srv.GrantDelegates, GrantPolicyDelegates: f.srv.GrantPolicyDelegates, LegacyGrantDelegates: append([]broker.LegacyDelegateBinding(nil), f.srv.LegacyGrantDelegates...), Clock: clocktesting.NewFakeClock(f.now)}
				key := types.NamespacedName{Namespace: sessionNS, Name: g.Name}
				step := func(n int) {
					for range n {
						if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if active {
					step(4)
					if f.grant(g.Name).Status.Phase != v1alpha1.GrantActive {
						t.Fatal("not Active before change")
					}
				}
				edit(f, b)
				step(4)
				if st := f.grant(g.Name).Status; st.Phase == v1alpha1.GrantActive || (!active && st.ApprovedBy != "") {
					t.Fatalf("retained authority: %+v", st)
				}
				if active {
					var sa corev1.ServiceAccount
					if err := f.c.Get(context.Background(), key, &sa); !apierrors.IsNotFound(err) {
						t.Fatalf("owned identity remains: %v", err)
					}
				}
			})
		}
	}
}

func TestLegacyTransientReadDoesNotConsumeDecision(t *testing.T) {
	f, tok, g := legacyReachFixture(t)
	outage := errors.New("synthetic legacy identity read outage")
	f.srv.Live = interceptor.NewClient(f.c, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if _, ok := obj.(*corev1.ServiceAccount); ok {
			return outage
		}
		return c.Get(ctx, key, obj, opts...)
	}})
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, decisionFor(g, true)); w.Code != http.StatusServiceUnavailable {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, ok := f.decisionAnnotation(g.Name); ok {
		t.Fatal("transient identity failure consumed slot")
	}
}

func TestLegacySharedAccountCannotUseHostForm(t *testing.T) {
	f, tok, _ := legacyReachFixture(t)
	c, err := f.srv.resolveCaller(context.Background(), f.auth[tok])
	if err != nil {
		t.Fatal(err)
	}
	c.kind = kindCoordinator
	if _, err := f.srv.delegateOf(context.Background(), c); err == nil {
		t.Fatal("baseline actor bypassed exact legacy form")
	}
}
