package apiserver

// Disposable API server only. No cluster kubeconfig, production policy or live
// API access is used. Every write below targets internal/testenv's localhost API.
import (
	"context"
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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/yaml"

	"github.com/thaynes43/dev-env/internal/testenv"
)

func TestPortalBootstrapRealAdmissionAndRBAC(t *testing.T) {
	ctx := context.Background()
	logFile, err := os.Create(filepath.Join(t.TempDir(), "apiserver.log"))
	mustBootstrap(t, err)
	defer func() { _ = logFile.Close() }()
	isolated, err := testenv.StartWithAPIServer(func(api *envtest.APIServer) {
		api.Configure().Set("enable-admission-plugins", "ValidatingAdmissionPolicy")
		api.Out, api.Err = logFile, logFile
	})
	mustBootstrap(t, err)
	defer func() {
		if err := isolated.Stop(); err != nil {
			t.Errorf("stop isolated API server: %v", err)
		}
	}()
	cfg := isolated.Config
	scheme := runtime.NewScheme()
	mustBootstrap(t, corev1.AddToScheme(scheme))
	mustBootstrap(t, rbacv1.AddToScheme(scheme))
	mustBootstrap(t, admissionv1.AddToScheme(scheme))
	mustBootstrap(t, authorizationv1.AddToScheme(scheme))
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	mustBootstrap(t, err)
	for _, ns := range []string{"dev-agents", "dev-env-system", "portal-bootstrap-negative"} {
		err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
		if !apierrors.IsAlreadyExists(err) {
			mustBootstrap(t, err)
		}
	}
	permit := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapPermitKey.Namespace, Name: bootstrapPermitKey.Name}, Data: map[string]string{"state": "active"}}
	mustBootstrap(t, admin.Create(ctx, permit))
	subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "dev-env-system", Name: "dev-env-operator"}
	role := func(ns, name string, rules []rbacv1.PolicyRule, sub rbacv1.Subject) {
		t.Helper()
		mustBootstrap(t, admin.Create(ctx, &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Rules: rules}))
		mustBootstrap(t, admin.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{sub}}))
	}
	cmRule := func(verbs []string, names ...string) rbacv1.PolicyRule {
		return rbacv1.PolicyRule{APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: verbs, ResourceNames: names}
	}
	role("dev-agents", "portal-bootstrap", []rbacv1.PolicyRule{cmRule([]string{"create"}), cmRule([]string{"get"}, bootstrapKey.Name)}, subject)
	role("dev-env-system", "portal-bootstrap-permit", []rbacv1.PolicyRule{cmRule([]string{"get", "update"}, bootstrapPermitKey.Name)}, subject)
	actor := func(user string) client.Client {
		c := rest.CopyConfig(cfg)
		c.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated"}}
		out, err := client.New(c, client.Options{Scheme: scheme})
		mustBootstrap(t, err)
		return out
	}
	executor := actor("system:serviceaccount:dev-env-system:dev-env-operator")
	checkAccess := func(user, verb, ns, name string, want bool) {
		t.Helper()
		err := wait.PollUntilContextTimeout(ctx, 20*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
			sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{User: user, Groups: []string{"system:authenticated"}, ResourceAttributes: &authorizationv1.ResourceAttributes{Namespace: ns, Verb: verb, Group: "", Resource: "configmaps", Name: name}}}
			if err := admin.Create(ctx, sar); err != nil {
				return false, err
			}
			return sar.Status.Allowed == want, nil
		})
		mustBootstrap(t, err)
	}
	user := "system:serviceaccount:dev-env-system:dev-env-operator"
	checkAccess(user, "create", "dev-agents", "", true)
	for _, tc := range []struct {
		verb, ns, name string
		allow          bool
	}{{"get", "dev-agents", bootstrapKey.Name, true}, {"get", "dev-env-system", bootstrapPermitKey.Name, true}, {"update", "dev-env-system", bootstrapPermitKey.Name, true}, {"list", "dev-agents", "", false}, {"get", "dev-agents", "other", false}, {"update", "dev-agents", bootstrapKey.Name, false}, {"delete", "dev-agents", bootstrapKey.Name, false}, {"get", "dev-env-system", "other", false}, {"create", "dev-env-system", "", false}, {"create", "portal-bootstrap-negative", "", false}} {
		checkAccess(user, tc.verb, tc.ns, tc.name, tc.allow)
	}
	var got corev1.ConfigMap
	mustBootstrap(t, executor.Get(ctx, bootstrapPermitKey, &got))
	got.Data["fixture"] = "update"
	mustBootstrap(t, executor.Update(ctx, &got))
	if err := executor.List(ctx, &corev1.ConfigMapList{}, client.InNamespace("dev-agents")); !apierrors.IsForbidden(err) {
		t.Fatalf("broad list %v", err)
	}
	if err := executor.Get(ctx, types.NamespacedName{Namespace: "dev-env-system", Name: "other"}, &corev1.ConfigMap{}); !apierrors.IsForbidden(err) {
		t.Fatalf("unrelated GET %v", err)
	}
	// Prove resourceNames does not authorize CREATE, even for that name.
	named := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "dev-env-system", Name: "named-create-fixture"}
	role("dev-agents", "named-create-fixture", []rbacv1.PolicyRule{cmRule([]string{"create"}, bootstrapKey.Name)}, named)
	checkAccess("system:serviceaccount:dev-env-system:named-create-fixture", "create", "dev-agents", "", false)
	if err := actor("system:serviceaccount:dev-env-system:named-create-fixture").Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapKey.Namespace, Name: bootstrapKey.Name}, Data: map[string]string{"schema": "1", "state": "closed"}}); !apierrors.IsForbidden(err) {
		t.Fatalf("resourceNames must not authorize named CREATE: %v", err)
	}
	for _, verb := range []string{"get", "list", "create", "update", "delete"} {
		checkAccess("system:serviceaccount:dev-agents:dev-env-agent", verb, "dev-agents", bootstrapKey.Name, false)
	}
	// Adversarial RBAC allows bad actors/namespaces; admission must be the
	// rejecting layer, rather than an unrelated missing permission.
	wrong := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: "dev-agents", Name: "dev-env-agent"}
	role("dev-agents", "admission-negative-actor", []rbacv1.PolicyRule{cmRule([]string{"create"})}, wrong)
	role("portal-bootstrap-negative", "admission-negative-namespace", []rbacv1.PolicyRule{cmRule([]string{"create"})}, subject)
	checkAccess("system:serviceaccount:dev-agents:dev-env-agent", "create", "dev-agents", "", true)
	checkAccess(user, "create", "portal-bootstrap-negative", "", true)
	data, err := os.ReadFile("testdata/portal-bootstrap/admission.yaml")
	mustBootstrap(t, err)
	docs := strings.Split(string(data), "\n---\n")
	var policy admissionv1.ValidatingAdmissionPolicy
	var binding admissionv1.ValidatingAdmissionPolicyBinding
	mustBootstrap(t, yaml.Unmarshal([]byte(docs[0]), &policy))
	mustBootstrap(t, yaml.Unmarshal([]byte(docs[1]), &binding))
	if policy.Spec.FailurePolicy == nil || *policy.Spec.FailurePolicy != admissionv1.Fail || binding.Spec.PolicyName != policy.Name || binding.Spec.MatchResources == nil {
		t.Fatal("fixture not fail-closed and bound")
	}
	mustBootstrap(t, admin.Create(ctx, &policy))
	mustBootstrap(t, admin.Create(ctx, &binding))
	t.Cleanup(func() {
		_ = admin.Delete(context.Background(), &binding)
		_ = admin.Delete(context.Background(), &policy)
	})
	target := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: bootstrapKey.Namespace, Name: bootstrapKey.Name}, Data: map[string]string{"schema": "1", "state": "closed"}}
	}
	// Wait only for API admission registration, using server dry-run (no writes).
	var admissionErr error
	var readinessResponses []string
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		bad := target()
		bad.Data["state"] = "open"
		admissionErr = executor.Create(ctx, bad, client.DryRunAll)
		if len(readinessResponses) < 5 {
			if admissionErr == nil {
				readinessResponses = append(readinessResponses, "allowed")
			} else {
				readinessResponses = append(readinessResponses, admissionErr.Error())
			}
		}
		return apierrors.IsForbidden(admissionErr) && strings.Contains(admissionErr.Error(), policy.Name), nil
	})
	if err != nil {
		_ = admin.Get(context.Background(), types.NamespacedName{Name: policy.Name}, &policy)
		_ = admin.Get(context.Background(), types.NamespacedName{Name: binding.Name}, &binding)
		logData, _ := os.ReadFile(logFile.Name())
		t.Fatalf("admission readiness: %v; last response: %v; first responses: %v; policy=%+v status=%+v binding=%+v; server log: %s", err, admissionErr, readinessResponses, policy.Spec, policy.Status, binding.Spec, logData)
	}
	for _, tc := range []struct {
		name   string
		who    client.Client
		mutate func(*corev1.ConfigMap)
	}{
		{"wrong-actor", actor("system:serviceaccount:dev-agents:dev-env-agent"), func(*corev1.ConfigMap) {}},
		{"wrong-name", executor, func(c *corev1.ConfigMap) { c.Name = "other" }},
		{"wrong-namespace", executor, func(c *corev1.ConfigMap) { c.Namespace = "portal-bootstrap-negative" }},
		{"open", executor, func(c *corev1.ConfigMap) { c.Data["state"] = "open" }},
		{"schema", executor, func(c *corev1.ConfigMap) { c.Data["schema"] = "2" }},
		{"missing", executor, func(c *corev1.ConfigMap) { delete(c.Data, "schema") }},
		{"extra-data", executor, func(c *corev1.ConfigMap) { c.Data["extra"] = "no" }},
		{"binary", executor, func(c *corev1.ConfigMap) { c.BinaryData = map[string][]byte{"extra": {1}} }},
		{"labels", executor, func(c *corev1.ConfigMap) { c.Labels = map[string]string{"extra": "no"} }},
		{"annotations", executor, func(c *corev1.ConfigMap) { c.Annotations = map[string]string{"extra": "no"} }},
		{"finalizers", executor, func(c *corev1.ConfigMap) { c.Finalizers = []string{"example.test/hold"} }},
		{"owner", executor, func(c *corev1.ConfigMap) {
			c.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "other", UID: "other"}}
		}},
		{"immutable", executor, func(c *corev1.ConfigMap) { c.Immutable = ptr.To(true) }},
		{"generate-name", executor, func(c *corev1.ConfigMap) { c.GenerateName = "other-" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := target()
			tc.mutate(cm)
			err := tc.who.Create(ctx, cm)
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), policy.Name) {
				t.Fatalf("must be denied by admission policy: %v", err)
			}
		})
	}
	cm := target()
	mustBootstrap(t, executor.Create(ctx, cm))
	if cm.UID == "" || cm.ResourceVersion == "" {
		t.Fatal("real receipt missing")
	}
	mustBootstrap(t, executor.Get(ctx, bootstrapKey, &got))
	if got.UID != cm.UID {
		t.Fatal("UID mismatch")
	}
	if err := executor.Update(ctx, &got); !apierrors.IsForbidden(err) {
		t.Fatalf("target update %v", err)
	}
	if err := executor.Delete(ctx, &got); !apierrors.IsForbidden(err) {
		t.Fatalf("target delete %v", err)
	}
	mustBootstrap(t, admin.Get(ctx, types.NamespacedName{Name: policy.Name}, &policy))
	if policy.Status.TypeChecking != nil && len(policy.Status.TypeChecking.ExpressionWarnings) > 0 {
		t.Fatalf("CEL warnings: %+v", policy.Status.TypeChecking.ExpressionWarnings)
	}
	// Force an evaluation error to prove failurePolicy Fail on the real server.
	policy.Spec.Validations = []admissionv1.Validation{{Expression: "1 / 0 == 0", Message: "deliberate fixture evaluation error"}}
	mustBootstrap(t, admin.Update(ctx, &policy))
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(ctx context.Context) (bool, error) {
		err := executor.Create(ctx, target(), client.DryRunAll)
		return (apierrors.IsForbidden(err) || apierrors.IsInvalid(err)) && strings.Contains(err.Error(), "division by zero"), nil
	})
	mustBootstrap(t, err)
}
