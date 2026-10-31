package apiserver

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/broker"
)

// Exercise S2 against S5's real request/view/digest and broker materialization.
// Authentication and Pod materialization use the combined fixture, not live pods.
func TestGrantDecisionCombinedNamedSecrets(t *testing.T) {
	f, _, delegate, _, childGrant := combinedDelegateFixture(t)
	ctx := context.Background()
	key := types.NamespacedName{Namespace: "dev-env-system", Name: "combined-child"}
	var policy v1alpha1.GrantPolicy
	if err := f.c.Get(ctx, key, &policy); err != nil {
		t.Fatal(err)
	}
	policy.Spec.Kube = &v1alpha1.KubePolicy{
		Roles: []string{v1alpha1.RoleNamespaceOperator}, Namespaces: []string{"frontend"},
		DeniedSecrets: []string{"private-*"},
	}
	if err := f.c.Update(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	// The launch fixture's workloads grant is unrelated to these requests;
	// remove it rather than exceed the normal per-session Pending limit.
	if err := f.c.Delete(ctx, f.grant(childGrant.Name)); err != nil {
		t.Fatal(err)
	}
	child := "tok-launched-" + childGrant.Session
	request := apiv1.CreateGrantRequest{Type: "kube", Role: v1alpha1.RoleNamespaceOperator,
		Namespaces: []string{"frontend"}, Secrets: []string{"app-b", "app-a"}, TTL: "30m", Reason: "synthetic named Secret integration"}
	g := f.request(child, request, http.StatusCreated)
	want := []string{"app-a", "app-b"}
	if !slices.Equal(g.Secrets, want) || g.ScopeDigest == "" {
		t.Fatalf("request/view lost named Secrets or digest: %+v", g)
	}

	// The UID is right, but the reviewed Secret list is wrong: no record lands.
	f.now = f.now.Add(time.Second)
	request.Secrets = []string{"app-c"}
	changed := f.request(child, request, http.StatusCreated)
	bad := decisionFor(changed, true)
	bad.Digest = g.ScopeDigest
	wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(changed.Name), delegate, bad), http.StatusConflict, apiv1.CodeConflict)
	if f.grant(changed.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
		t.Fatal("a decision for different Secret names consumed the slot")
	}

	// A denied Secret is uncovered for both approvals and denials.
	f.now = f.now.Add(time.Second)
	request.Secrets = []string{"private-token"}
	denied := f.request(child, request, http.StatusCreated)
	for _, approve := range []bool{true, false} {
		wantError(t, f.do(http.MethodPost, apiv1.GrantDecisionPath(denied.Name), delegate, decisionFor(denied, approve)), http.StatusForbidden, apiv1.CodeForbidden)
		if f.grant(denied.Name).Annotations[broker.MaintenanceDecisionAnnotation] != "" {
			t.Fatal("an uncovered named-Secret decision consumed the slot")
		}
	}
	w := f.do(http.MethodPost, apiv1.GrantDecisionPath(g.Name), delegate, decisionFor(g, true))
	if w.Code != http.StatusAccepted || decode[apiv1.Grant](t, w).Phase != apiv1.GrantPending {
		t.Fatalf("decision must record 202/Pending: %d %s", w.Code, w.Body.String())
	}
	b := combinedBroker(f)
	reconcileCombined(t, f, b, g.Name, 3)
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantActive || st.ApprovedBy != broker.DelegateRef(delegateSessionSA) {
		t.Fatalf("broker did not activate delegated named-Secret grant: %+v", st)
	}
	useKey := types.NamespacedName{Namespace: "frontend", Name: g.Name + "-secret-use"}
	var role rbacv1.Role
	if err := f.c.Get(ctx, useKey, &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || !slices.Equal(role.Rules[0].Verbs, []string{"use"}) || !slices.Equal(role.Rules[0].ResourceNames, want) {
		t.Fatalf("Secret authorization escaped the exact names: %+v", role.Rules)
	}
	// Policy narrowing revokes Active access and removes its Secret-use objects.
	policy.Spec.Kube.DeniedSecrets = []string{"app-a"}
	if err := f.c.Update(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	reconcileCombined(t, f, b, g.Name, 3)
	if st := f.grant(g.Name).Status; st.Phase != v1alpha1.GrantReleased {
		t.Fatalf("narrowed policy retained access: %+v", st)
	}
	if err := f.c.Get(ctx, useKey, &role); !apierrors.IsNotFound(err) {
		t.Fatalf("revoked Secret-use Role remains: %v", err)
	}
	if err := f.c.Get(ctx, useKey, &rbacv1.RoleBinding{}); !apierrors.IsNotFound(err) {
		t.Fatalf("revoked Secret-use binding remains: %v", err)
	}
}
