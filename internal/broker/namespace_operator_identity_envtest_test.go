package broker

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The installed identity guard is copied verbatim from haynes-ops a2d2a961,
// kubernetes/main/apps/dev-env-system/rbac/app/identity-guard.yaml. Unlike the
// isolated Secret-reference test, this checks its actual rule 3 too. Nothing
// here installs or changes a live policy, or relaxes rule 4 for named Secrets.
func TestNamespaceOperatorIdentityGuardDefaultServiceAccount(t *testing.T) {
	proposed(t)
	ctx := context.Background()
	if err := apply(ctx, filepath.Join("testdata", "identity-guard", "identity-guard.yaml")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, obj := range []client.Object{
			&admissionv1.ValidatingAdmissionPolicyBinding{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}},
			&admissionv1.ValidatingAdmissionPolicy{ObjectMeta: metav1.ObjectMeta{Name: "dev-env-identity-guard"}},
		} {
			if err := admin.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("remove envtest identity guard: %v", err)
			}
		}
	})
	_, actor := grantIdentity(t, v1alpha1.KubeGrant{Role: v1alpha1.RoleNamespaceOperator,
		Namespaces: []string{"frontend"}, Secrets: []string{"synthetic-secret"}})
	job := func(account string) func() client.Object {
		return func() client.Object {
			return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "frontend", Name: name("s5-sa")}, Spec: batchv1.JobSpec{
				ActiveDeadlineSeconds: ptr.To[int64](600),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{ServiceAccountName: account,
					RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "synthetic", Image: "synthetic"}}}},
			}}
		}
	}
	// Wait for the policy, and require its rule-3 message rather than a generic
	// RBAC denial. An arbitrary SA is refused even without a Secret reference.
	eventually(t, "identity guard rule 3", func() (bool, string) {
		err := actor.Create(ctx, job("synthetic-other-sa")(), client.DryRunAll)
		return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "default ServiceAccount"), fmt.Sprint(err)
	})
	wantAdmitted(t, actor, job("default"))
	wantAdmitted(t, actor, job(""))
}
