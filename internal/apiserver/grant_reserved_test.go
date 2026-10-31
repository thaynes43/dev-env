package apiserver

import (
	"context"
	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
	"github.com/thaynes43/dev-env/internal/grantattest"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"net/http"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"strings"
	"testing"
)

func reservedAPICase(t *testing.T, class string, legacy bool) (*fixture, string, apiv1.Grant, apiv1.GrantDecisionRequest) {
	t.Helper()
	var f *fixture
	var token string
	var view apiv1.Grant
	if legacy {
		f, token, view = legacyReachFixture(t)
	} else {
		f, _, view = delegateFixture(t)
		token = tokCoordinator
		reachRunning(t, f, view.Session)
	}
	g := f.grant(view.Name)
	switch class {
	case grantattest.ClassRD2:
		g.Spec.Type = v1alpha1.GrantBreakglass
		g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleBreakglass, Reserved: true}
	case grantattest.ClassRD3a:
		g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, Namespaces: []string{"frontend"}, Reserved: true}
	case grantattest.ClassRD3b:
		g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleSecretsRead, AllNamespaces: true, Reserved: true}
	case grantattest.ClassRD4Storage:
		g.Spec.Kube = &v1alpha1.KubeGrant{Role: v1alpha1.RoleStorage, Namespaces: []string{"frontend"}, Reserved: true, StorageTargets: []v1alpha1.GrantStorageTarget{{Kind: "PersistentVolumeClaim", Name: "data", UID: "11111111-2222-3333-4444-555555555555"}}}
	}
	if err := f.c.Update(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frontend", "database", "kube-system", "dev-agents"} {
		if err := f.c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name)}}); err != nil {
			t.Fatal(err)
		}
	}
	f.srv.ReservedDecisionClasses = []string{class}
	f.h = f.srv.Handler()
	view = f.srv.grantView(f.grant(view.Name))
	req := decisionFor(view, true)
	req.Reserved = true
	req.TomRef = "msg-tom-fresh-1"
	return f, token, view, req
}

func TestReservedAPICasesAndLegacyTopLevelBrokerLifecycle(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, class := range []string{grantattest.ClassRD2, grantattest.ClassRD3a, grantattest.ClassRD3b, grantattest.ClassRD4Storage} {
			t.Run(class+map[bool]string{false: "/tree", true: "/legacy-top-level"}[legacy], func(t *testing.T) {
				f, tok, g, req := reservedAPICase(t, class, legacy)
				w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req)
				if w.Code != http.StatusAccepted {
					t.Fatal(w.Code, w.Body.String())
				}
				rec, ok := f.decisionAnnotation(g.Name)
				if !ok || rec.Reserved == nil || rec.Reach == nil || rec.Reach.AuthorityKind != "reserved" || rec.Reach.PolicyName != "" || rec.Reserved.TomRef != req.TomRef || rec.By == "human/" {
					t.Fatal("missing exact reserved actor binding", rec)
				}
				b := &broker.Broker{Client: f.c, APIReader: f.c, Clock: clocktesting.NewFakeClock(f.now), SessionNamespace: sessionNS, PolicyNamespace: f.srv.PolicyNamespace, GrantDelegates: f.srv.GrantDelegates, GrantPolicyDelegates: f.srv.GrantPolicyDelegates, LegacyGrantDelegates: f.srv.LegacyGrantDelegates, ReservedDecisionClasses: f.srv.ReservedDecisionClasses}
				for i := 0; i < 3; i++ {
					if _, err := b.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: sessionNS, Name: g.Name}}); err != nil {
						t.Fatal(err)
					}
				}
				active := f.grant(g.Name)
				if active.Status.Phase != v1alpha1.GrantActive || !strings.Contains(active.Status.Message, grantattest.ReservedWording) {
					t.Fatal("202 did not separately become audited Active", active.Status)
				}
				w = f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req)
				if w.Code != http.StatusConflict {
					t.Fatal("replayed reserved decision", w.Code, w.Body.String())
				}
			})
		}
	}
}

func TestReservedAPIDenialsAndRefusalsLeaveLegitimateSlot(t *testing.T) {
	for _, approve := range []bool{false, true} {
		for _, mode := range []string{"disabled", "unlisted", "no-tom-ref", "wrong-digest", "wrong-uid", "long-ttl", "ordinary-marker", "wrong-actor", "self", "excluded-scope", "maintenance"} {
			t.Run(mode+map[bool]string{false: "/deny", true: "/approve"}[approve], func(t *testing.T) {
				f, tok, g, req := reservedAPICase(t, grantattest.ClassRD3a, false)
				req.Approve = approve
				switch mode {
				case "disabled":
					f.srv.ReservedDecisionClasses = nil
				case "unlisted":
					f.srv.ReservedDecisionClasses = []string{grantattest.ClassRD2}
				case "no-tom-ref":
					req.TomRef = ""
				case "wrong-digest":
					req.Digest = strings.Repeat("X", 43)
				case "wrong-uid":
					req.GrantUID = "other"
				case "long-ttl":
					req.TTL = "2h"
				case "ordinary-marker":
					req.Reserved = false
				case "wrong-actor":
					tok = f.sessionPod("other", "full", 0)
				case "self":
					tok = "tok-session-requester"
				case "excluded-scope":
					live := f.grant(g.Name)
					live.Spec.Kube.Namespaces = []string{"kube-system"}
					if err := f.c.Update(context.Background(), live); err != nil {
						t.Fatal(err)
					}
					req.Digest = grantattest.ScopeDigest(live)
				case "maintenance":
					live := f.grant(g.Name)
					live.Spec.Type = v1alpha1.GrantMaintenance
					live.Spec.Kube = nil
					if err := f.c.Update(context.Background(), live); err != nil {
						t.Fatal(err)
					}
					req.Digest = grantattest.ScopeDigest(live)
				}
				f.h = f.srv.Handler()
				w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req)
				if w.Code/100 == 2 {
					t.Fatal("invalid reserved decision accepted", w.Code, w.Body.String())
				}
				if f.grant(g.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
					t.Fatal("refusal consumed decision slot")
				}
			})
		}
	}
	for _, approve := range []bool{false, true} {
		f, tok, g, req := reservedAPICase(t, grantattest.ClassRD3a, false)
		req.Approve = approve
		w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req)
		if w.Code != http.StatusAccepted {
			t.Fatal("legitimate reserved decision refused", w.Code, w.Body.String())
		}
	}
}

func TestReservedConfiguredNamespaceExceptionAndNoAttestation(t *testing.T) {
	f, tok, g, req := reservedAPICase(t, grantattest.ClassRD3a, false)
	live := f.grant(g.Name)
	live.Spec.Kube.Namespaces = []string{"kube-public"}
	if err := f.c.Update(context.Background(), live); err != nil {
		t.Fatal(err)
	}
	req.Digest = grantattest.ScopeDigest(live)
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req); w.Code != http.StatusForbidden {
		t.Fatal("default exclusion accepted", w.Code)
	}
	f.srv.ReservedExcludedNamespaces = []string{"dev-agents"}
	f.srv.GrantAttestation = true
	f.h = f.srv.Handler()
	if w := f.do(http.MethodPost, apiv1.GrantAttestationPath(g.Name), "tok-session-requester", apiv1.AttestGrantRequest{ApprovedBy: "user", Ref: "msg-tom-1", ScopeDigest: req.Digest}); w.Code != http.StatusForbidden {
		t.Fatal("reserved attested path accepted", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req); w.Code != http.StatusAccepted {
		t.Fatal("explicit bounded exception required broad root", w.Code, w.Body.String())
	}
}

func TestReservedDesignatedHostCanReviewExactScopeBeforeDecision(t *testing.T) {
	f, tok, g, req := reservedAPICase(t, grantattest.ClassRD3a, false)
	f.reparent("requester", humanSA)
	if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tok, nil); w.Code != http.StatusForbidden {
		t.Fatal("default tree read widened", w.Code)
	}
	f.srv.GrantPolicyDelegates = []string{coordinatorSA}
	f.h = f.srv.Handler()
	if w := f.do(http.MethodGet, apiv1.GrantPath(g.Name), tok, nil); w.Code != http.StatusOK {
		t.Fatal("designated host cannot review exact reserved scope", w.Code, w.Body.String())
	}
	if f.grant(g.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		t.Fatal("metadata read wrote approval")
	}
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req); w.Code != http.StatusAccepted {
		t.Fatal("reviewed scope decision failed", w.Code, w.Body.String())
	}
}

func TestReservedLegacyRequesterDescendantCannotDecide(t *testing.T) {
	f, tok, g, req := reservedAPICase(t, grantattest.ClassRD3a, true)
	f.reparent("legacy-claude", "top-level-gpu")
	if w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), tok, req); w.Code != http.StatusForbidden {
		t.Fatal("requester descendant acquired reserved authority", w.Code, w.Body.String())
	}
	if f.grant(g.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		t.Fatal("descendant consumed slot")
	}
}
