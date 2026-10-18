package coordretention

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/testenv"
)

// TestEnvtestIdentityGuardRefusesExec is DESIGN-024 E4. RBAC allows the exec.
// dev-env-identity-guard refuses dev-env-agent and a grant ServiceAccount, and
// the refusal names that policy. The workbench exemption is not that refusal.
// The policy is not installed by this test.
func TestEnvtestIdentityGuardRefusesExec(t *testing.T) {
	env, err := testenv.Start()
	if err != nil {
		t.Fatalf("envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Log("envtest stop:", err)
		}
	})
	ctx := context.Background()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, admissionv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(env.Config, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	const ns = "dev-agents"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dev-env-agent", "grant-synthetic", "dev-env-workbench"} {
		if err := admin.Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "shelf"}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "agent", Image: "synthetic"}},
	}}
	if err := admin.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	subjects := []rbacv1.Subject{}
	for _, name := range []string{"dev-env-agent", "grant-synthetic", "dev-env-workbench"} {
		subjects = append(subjects, rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Namespace: ns, Name: name})
	}
	for _, obj := range []client.Object{
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "identity-guard-exec-test"}, Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""}, Resources: []string{"pods/exec"}, Verbs: []string{"create", "get"},
		}}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "identity-guard-exec-test"}, RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "identity-guard-exec-test",
		}, Subjects: subjects},
	} {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}
	for _, obj := range identityGuardManifest(t, scheme) {
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
	}

	agent := waitExec(t, env.Config, ns, pod.Name, "dev-env-agent", true)
	if !policyRefusal(agent) {
		t.Fatalf("agent exec = %v", agent)
	}
	grant := waitExec(t, env.Config, ns, pod.Name, "grant-synthetic", true)
	if !policyRefusal(grant) {
		t.Fatalf("grant exec = %v", grant)
	}
	workbench := waitExec(t, env.Config, ns, pod.Name, "dev-env-workbench", false)
	if workbench == nil || strings.Contains(workbench.Error(), "dev-env-identity-guard") || strings.Contains(workbench.Error(), "is forbidden:") {
		t.Fatalf("workbench exec = %v", workbench)
	}
}

func policyRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is forbidden:") && strings.Contains(err.Error(), "dev-env-identity-guard")
}

func waitExec(t *testing.T, cfg *rest.Config, namespace, pod, sa string, wantPolicy bool) error {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last error
	for {
		last = execAs(cfg, namespace, pod, sa)
		policy := policyRefusal(last)
		rbac := last != nil && strings.Contains(last.Error(), "is forbidden:") && !policy
		if wantPolicy && policy {
			return last
		}
		if !wantPolicy && last != nil && !policy && !rbac {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s exec after waiting: %v", sa, last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func execAs(cfg *rest.Config, namespace, pod, sa string) error {
	imp := rest.CopyConfig(cfg)
	imp.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:dev-agents:" + sa}
	cs, err := kubernetes.NewForConfig(imp)
	if err != nil {
		return err
	}
	req := cs.CoreV1().RESTClient().Post().Resource("pods").Namespace(namespace).Name(pod).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Container: "agent", Command: []string{"true"}, Stdout: true}, clientgoscheme.ParameterCodec)
	executor, err := remotecommand.NewSPDYExecutor(imp, "POST", req.URL())
	if err != nil {
		return err
	}
	return executor.StreamWithContext(context.Background(), remotecommand.StreamOptions{Stdout: &bytes.Buffer{}})
}

func identityGuardManifest(t *testing.T, scheme *runtime.Scheme) []client.Object {
	t.Helper()
	raw, err := os.ReadFile("testdata/haynes-ops/identity-guard.yaml")
	if err != nil {
		t.Fatal(err)
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	codec := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	var objects []client.Object
	for {
		var doc runtime.RawExtension
		if err := decoder.Decode(&doc); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(doc.Raw) == 0 {
			continue
		}
		obj, _, err := codec.Decode(doc.Raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, obj.(client.Object))
	}
	if len(objects) != 2 {
		t.Fatalf("identity guard manifest has %d objects", len(objects))
	}
	return objects
}
