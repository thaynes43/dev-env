package broker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Install the proposed haynes-ops manifests on the local envtest API server
// for this case. Keep the historical fixtures for tests of legacy grants.
func replacePodExecFixtureObject(t *testing.T, path string, original, proposed client.Object) {
	t.Helper()
	ctx := context.Background()
	key := client.ObjectKeyFromObject(original)
	if err := admin.Get(ctx, key, original); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "proposed-haynes-ops", "pod-exec", path))
	if err != nil {
		t.Fatal(err)
	}
	d := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	for proposed.GetName() == "" {
		if err := d.Decode(proposed); err != nil {
			t.Fatal(err)
		}
	}
	if client.ObjectKeyFromObject(proposed) != key {
		t.Fatal("proposal must update the installed object")
	}
	proposed.SetResourceVersion(original.GetResourceVersion())
	proposed.SetUID(original.GetUID())
	if err := admin.Update(ctx, proposed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Get(ctx, key, proposed); err != nil {
			t.Error(err)
			return
		}
		original.SetResourceVersion(proposed.GetResourceVersion())
		if err := admin.Update(ctx, original); err != nil {
			t.Error(err)
		}
	})
}

func TestPodExecBrokerRBACAndInstaller(t *testing.T) {
	suite(t)
	ctx := t.Context()
	replacePodExecFixtureObject(t, "broker-guard.yaml", &admissionv1.ValidatingAdmissionPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "dev-env-broker-guard"},
	}, &admissionv1.ValidatingAdmissionPolicy{})
	replacePodExecFixtureObject(t, "broker-clusterrole.yaml", &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: "dev-env-broker"},
	}, &rbacv1.ClusterRole{})
	bc, err := client.New(brokerCfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the pod-exec Role fence", func() (bool, string) {
		wide := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "grant-pod-exec-wide-fixture", Namespace: "frontend"},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create"}}},
		}
		err := bc.Create(ctx, wide, client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "dev-env-broker-guard:"), fmt.Sprint(err)
	})
	installer := &recorder{}
	r := startBroker(t, nil, installer)
	s, requesterPod := newSession(t)
	target := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("pod-exec-target")},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "synthetic"}}},
	}
	if err := admin.Create(ctx, target); err != nil {
		t.Fatal(err)
	}
	n := newGrant(t, s, func(g *v1alpha1.AccessGrant) {
		g.Name = name("grant-pod-exec")
		g.Spec.Type, g.Spec.Kube = v1alpha1.GrantPodExec, nil
		g.Spec.PodExec = &v1alpha1.PodExecGrant{Namespace: target.Namespace, Pod: target.Name, Container: "app", PodUID: string(target.UID)}
	})
	waitPhase(t, n, v1alpha1.GrantPending)
	// The local Ask unit test exercises the authenticated answer path. This
	// case starts from its resulting status to test the broker's actual RBAC.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		g := grant(t, n)
		g.Status.Ask = "ask-0123456789abcdef"
		approve(g, "authentik/fixture", g.Spec.TTL.Duration, r.b.now())
		return admin.Status().Update(ctx, g)
	}); err != nil {
		t.Fatal(err)
	}
	made := waitMade(t, n)
	if made.Status.InstalledPodUID != string(requesterPod.UID) {
		t.Fatal("credential went to a different requester pod")
	}
	installed, _ := installer.of(n)
	if len(installed) != 1 || installed[0].pod != requesterPod.UID || installed[0].token == "" {
		t.Fatal("expected one credential delivered to the requester")
	}
	grantee := "system:serviceaccount:" + sessionNS + ":" + n
	execAttr := authorizationv1.ResourceAttributes{Namespace: target.Namespace, Resource: "pods", Subresource: "exec", Verb: "create", Name: target.Name}
	eventually(t, "the named exec RBAC", func() (bool, string) {
		return allowed(t, grantee, execAttr), "exec permission has not propagated"
	})
	readAttr := authorizationv1.ResourceAttributes{Namespace: target.Namespace, Resource: "pods", Verb: "get", Name: target.Name}
	otherPod := execAttr
	otherPod.Name = "sibling-fixture"
	otherVerb := execAttr
	otherVerb.Subresource = "attach"
	if !allowed(t, grantee, readAttr) || allowed(t, grantee, otherPod) || allowed(t, grantee, otherVerb) {
		t.Fatal("expected get and exec for the one pod only")
	}
	release(t, n)
	waitPhase(t, n, v1alpha1.GrantReleased)
	waitGone(t, rb(target.Namespace, n), &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: target.Namespace, Name: n}}, sa(n))
	eventually(t, "revoked exec RBAC", func() (bool, string) {
		return !allowed(t, grantee, execAttr), "exec permission has not been revoked"
	})
}
