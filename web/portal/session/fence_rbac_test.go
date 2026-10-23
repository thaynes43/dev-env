package session

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// The ordinary required CI make-test job supplies its pinned envtest binaries.
// This is a disposable API server, never the live cluster or a granted witness.
func TestAdmissionRuntimeRBAC(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("disposable API assets not configured; RBAC proof runs in required make-test CI")
	}
	environment := &envtest.Environment{BinaryAssetsDirectory: os.Getenv("KUBEBUILDER_ASSETS")}
	environment.ControlPlane.GetAPIServer().Configure().Set("authorization-mode", "RBAC")
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal("disposable admission API failed")
	}
	t.Cleanup(func() {
		if environment.Stop() != nil {
			t.Error("disposable admission API cleanup failed")
		}
	})
	admin, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal("fixture admin failed")
	}
	ctx := context.Background()
	ns := "dev-agents"
	name := "dev-env-v2-portal-admission"
	if _, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}, metav1.CreateOptions{}); err != nil {
		t.Fatal("fixture namespace failed")
	}
	for _, n := range []string{name, "another-object"} {
		if _, err := admin.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: n}}, metav1.CreateOptions{}); err != nil {
			t.Fatal("fixture object failed")
		}
	}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "portal-admission", Namespace: ns}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"configmaps"}, ResourceNames: []string{name}, Verbs: []string{"get"}}}}
	if _, err := admin.RbacV1().Roles(ns).Create(ctx, role, metav1.CreateOptions{}); err != nil {
		t.Fatal("fixture role failed")
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: ns}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: ns, Name: "dev-env-v2-portal"}}}
	if _, err := admin.RbacV1().RoleBindings(ns).Create(ctx, binding, metav1.CreateOptions{}); err != nil {
		t.Fatal("fixture binding failed")
	}
	runtimeConfig := rest.CopyConfig(cfg)
	runtimeConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:dev-agents:dev-env-v2-portal", Groups: []string{"system:serviceaccounts", "system:serviceaccounts:dev-agents", "system:authenticated"}}
	runtime, err := kubernetes.NewForConfig(runtimeConfig)
	if err != nil {
		t.Fatal("fixture runtime failed")
	}
	// RBAC caches propagate asynchronously. Only positive named GET is polled at
	// low frequency; no denied action retries or broad API scans.
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err = runtime.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("fixture role propagation failed")
		}
	}
	checks := map[string]func() error{
		"get-other": func() error {
			_, e := runtime.CoreV1().ConfigMaps(ns).Get(ctx, "another-object", metav1.GetOptions{})
			return e
		},
		"list": func() error { _, e := runtime.CoreV1().ConfigMaps(ns).List(ctx, metav1.ListOptions{}); return e },
		"watch": func() error {
			w, e := runtime.CoreV1().ConfigMaps(ns).Watch(ctx, metav1.ListOptions{})
			if w != nil {
				w.Stop()
			}
			return e
		},
		"create": func() error {
			_, e := runtime.CoreV1().ConfigMaps(ns).Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "forbidden"}}, metav1.CreateOptions{})
			return e
		},
		"update": func() error {
			_, e := runtime.CoreV1().ConfigMaps(ns).Update(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.UpdateOptions{})
			return e
		},
		"patch": func() error {
			_, e := runtime.CoreV1().ConfigMaps(ns).Patch(ctx, name, types.MergePatchType, []byte(`{"data":{"state":"open"}}`), metav1.PatchOptions{})
			return e
		},
		"delete": func() error { return runtime.CoreV1().ConfigMaps(ns).Delete(ctx, name, metav1.DeleteOptions{}) },
		"secret": func() error { _, e := runtime.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{}); return e },
	}
	for label, check := range checks {
		t.Run(label, func(t *testing.T) {
			if !apierrors.IsForbidden(check()) {
				t.Fatal("runtime RBAC exceeded named GET-only witness authority")
			}
		})
	}
}
