package broker

import (
	"context"
	"fmt"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TestDecisionGuardActorMutations runs the exact haynes-ops CEL under the
// envtest API server. system:masters isolates admission from RBAC for this test.
func TestDecisionGuardActorMutations(t *testing.T) {
	suite(t)
	ctx := context.Background()
	if err := apply(ctx, "testdata/decision-guard/guard.yaml"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = admin.Delete(ctx, &admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-grant-decision-handoff"}})
		_ = admin.Delete(ctx, &admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-grant-decision-handoff"}})
	})
	actor := func(user string) client.Client {
		cfg := rest.CopyConfig(adminCfg)
		cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:masters"}}
		c, err := client.New(cfg, client.Options{Scheme: scheme})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	operator, broker, other := actor(operatorUser), actor(brokerUser), actor("system:serviceaccount:dev-agents:dev-env-agent")
	session, _ := newSession(t)
	id := newGrant(t, session)
	patch := func(c client.Client, value string) error {
		return c.Patch(ctx, grant(t, id), client.RawPatch(types.MergePatchType, []byte(fmt.Sprintf(`{"metadata":{"annotations":{"dev-env.haynesops.com/grant-decision":%s}}}`, value))))
	}
	// Admission policies are loaded asynchronously by kube-apiserver.
	eventually(t, "decision guard enforced", func() (bool, string) {
		err := patch(other, `"synthetic"`)
		if apierrors.IsForbidden(err) {
			return true, ""
		}
		if err == nil {
			_ = patch(broker, "null")
		}
		return false, fmt.Sprint(err)
	})
	for label, test := range map[string]struct {
		actor   client.Client
		allowed bool
	}{
		"operator": {operator, true}, "broker": {broker, false}, "other": {other, false},
	} {
		g := grant(t, id).DeepCopy()
		g.Name, g.ResourceVersion, g.UID = name("grant"), "", ""
		g.CreationTimestamp = metav1.Time{}
		g.Generation, g.ManagedFields = 0, nil
		g.Annotations = map[string]string{"dev-env.haynesops.com/grant-decision": "synthetic"}
		err := test.actor.Create(ctx, g)
		if test.allowed && err != nil || !test.allowed && !apierrors.IsForbidden(err) {
			t.Fatalf("%s create: %v", label, err)
		}
		if err == nil {
			_ = admin.Delete(ctx, g)
		}
	}
	if err := patch(broker, `"synthetic"`); !apierrors.IsForbidden(err) {
		t.Fatalf("broker fabricated: %v", err)
	}
	if err := patch(operator, `"first"`); err != nil {
		t.Fatalf("operator add: %v", err)
	}
	if err := patch(operator, `"second"`); !apierrors.IsForbidden(err) {
		t.Fatalf("operator overwrote: %v", err)
	}
	if err := patch(broker, `"second"`); !apierrors.IsForbidden(err) {
		t.Fatalf("broker overwrote: %v", err)
	}
	if err := patch(other, "null"); !apierrors.IsForbidden(err) {
		t.Fatalf("other removed: %v", err)
	}
	if err := patch(other, `"first"`); err != nil {
		t.Fatalf("unchanged handoff: %v", err)
	}
	if err := patch(broker, "null"); err != nil {
		t.Fatalf("broker removal: %v", err)
	}
}

func TestBrokerPodUIDGuard(t *testing.T) {
	suite(t)
	ctx := context.Background()
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: brokerUser, Groups: []string{"system:masters"}}
	bc, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := rest.CopyConfig(adminCfg)
	cfg2.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:dev-agents:dev-env-agent", Groups: []string{"system:masters"}}
	other, err := client.New(cfg2, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	_, pod := newSession(t)
	patch := func(c client.Client, body string) error {
		return c.Patch(ctx, pod.DeepCopy(), client.RawPatch(types.MergePatchType, []byte(body)))
	}
	wrong := `{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":"wrong"}}}`
	eventually(t, "pod UID guard enforced", func() (bool, string) {
		err := patch(bc, wrong)
		return apierrors.IsForbidden(err), fmt.Sprint(err)
	})
	if err := patch(other, fmt.Sprintf(`{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":%q}}}`, pod.UID)); !apierrors.IsForbidden(err) {
		t.Fatalf("other actor forged UID label: %v", err)
	}
	bad := fmt.Sprintf(`{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":%q,"other":"changed"}}}`, pod.UID)
	if err := patch(bc, bad); !apierrors.IsForbidden(err) {
		t.Fatalf("broker changed another label: %v", err)
	}
	bad = fmt.Sprintf(`{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":%q},"generateName":"changed-"}}`, pod.UID)
	if err := patch(bc, bad); !apierrors.IsForbidden(err) {
		t.Fatalf("broker changed generateName: %v", err)
	}
	bad = fmt.Sprintf(`{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":%q},"annotations":{"other":"changed"}}}`, pod.UID)
	if err := patch(bc, bad); !apierrors.IsForbidden(err) {
		t.Fatalf("broker changed an annotation: %v", err)
	}
	good := fmt.Sprintf(`{"metadata":{"labels":{"dev-env.haynesops.com/grant-pod-uid":%q}}}`, pod.UID)
	if err := patch(bc, good); err != nil {
		t.Fatalf("broker UID label: %v", err)
	}
}
