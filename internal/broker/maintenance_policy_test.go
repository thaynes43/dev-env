package broker

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/cel-go/cel"
	celtypes "github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Evaluate the actual packet CEL over JSON admission objects, not a Go rewrite.
// resource.Get is deliberately not emulated: its grant result is supplied here.
// quantity() comparisons use Kubernetes quantity parsing and canonical strings.
// These tests do not qualify Kyverno's lookup RBAC, quantity library, compiler or webhook runtime.
func maintenancePolicyExpressions(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", ".agents", "sagas", "distributed-dev-env", "verification", "bounded-maintenance-2026-10-30", "maintenance-executor.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Spec struct {
			Validations []struct {
				Expression string `json:"expression"`
			} `json:"validations"`
		} `json:"spec"`
	}
	if err := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&policy); err != nil {
		t.Fatal(err)
	}
	if len(policy.Spec.Validations) != 4 {
		t.Fatal("expected ownership/create/update/delete validations")
	}
	result := []string{}
	for _, i := range []int{0, 1, 2, 3} {
		result = append(result, policy.Spec.Validations[i].Expression)
	}
	return result
}

func admissionJSON(t *testing.T, o any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func maintenanceAdmissionAllowed(t *testing.T, operation, caller string, oldPod, pod *corev1.Pod, grant any) bool {
	t.Helper()
	env, err := cel.NewEnv(cel.OptionalTypes(), cel.Function("quantity", cel.Overload("quantity_string", []*cel.Type{cel.StringType}, cel.StringType, cel.UnaryBinding(func(value ref.Val) ref.Val {
		q, err := resource.ParseQuantity(string(value.(celtypes.String)))
		if err != nil {
			return celtypes.NewErr("invalid quantity: %v", err)
		}
		return celtypes.String(q.String())
	}))), cel.Variable("object", cel.DynType), cel.Variable("oldObject", cel.DynType), cel.Variable("request", cel.DynType), cel.Variable("variables", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	var object, oldObject any
	if pod != nil {
		object = admissionJSON(t, pod)
	}
	if oldPod != nil {
		oldObject = admissionJSON(t, oldPod)
	}
	selected := object
	if selected == nil {
		selected = oldObject
	}
	g := admissionJSON(t, grant)
	task := g["spec"].(map[string]any)["maintenance"].(map[string]any)
	selectedPod := selected.(map[string]any)
	container := selectedPod["spec"].(map[string]any)["containers"].([]any)[0]
	mounts, ok := task["readOnlyMounts"]
	if !ok {
		mounts = []any{}
	}
	devices, ok := task["devices"]
	if !ok {
		devices = []any{}
	}
	args := map[string]any{"object": object, "oldObject": oldObject, "request": map[string]any{"operation": operation, "namespace": sessionNS, "subResource": "", "userInfo": map[string]any{"username": caller}}, "variables": map[string]any{"pod": selected, "grant": g, "task": task, "container": container, "mounts": mounts, "devices": devices, "node": map[string]any{"metadata": map[string]any{"uid": task["nodeUID"]}, "spec": map[string]any{}}}}
	for _, expression := range maintenancePolicyExpressions(t) {
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			t.Fatal(issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		result, _, err := program.Eval(args)
		if err != nil {
			t.Fatalf("CEL evaluation error: %v", err)
		}
		if result.Value() != true {
			return false
		}
	}
	return true
}

func TestMaintenanceDeadlineAdmissionNormalized(t *testing.T) {
	b, g := maintenanceFixture(t)
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	old := MaintenancePod(g, b.now().Time)
	old.UID = "executor-uid"
	old.ResourceVersion = "10"
	old.Generation = 1
	old.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "broker", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "v1"}}
	next := old.DeepCopy()
	next.Spec.ActiveDeadlineSeconds = ptr.To(int64(1))
	next.Generation = 2
	next.ResourceVersion = "11"
	next.ManagedFields = []metav1.ManagedFieldsEntry{{Manager: "deadline-cleanup", Operation: metav1.ManagedFieldsOperationUpdate, APIVersion: "v1", Time: ptr.To(b.now())}}
	caller := "system:serviceaccount:dev-env-system:dev-env-broker"
	if !maintenanceAdmissionAllowed(t, "UPDATE", caller, old, next, g) {
		t.Fatal("normalized deadline shortening refused")
	}
	for name, mutate := range map[string]func(*corev1.Pod){
		"UID":           func(p *corev1.Pod) { p.UID = "replacement" },
		"name":          func(p *corev1.Pod) { p.Name = "other" },
		"namespace":     func(p *corev1.Pod) { p.Namespace = "other" },
		"labels":        func(p *corev1.Pod) { p.Labels["extra"] = "forged" },
		"grant UID":     func(p *corev1.Pod) { p.Labels[MaintenanceLabel] = "replacement" },
		"annotations":   func(p *corev1.Pod) { p.Annotations = map[string]string{"extra": "forged"} },
		"owners":        func(p *corev1.Pod) { p.OwnerReferences[0].UID = "replacement" },
		"finalizers":    func(p *corev1.Pod) { p.Finalizers = []string{"other"} },
		"creation time": func(p *corev1.Pod) { p.CreationTimestamp = b.now() },
		"node":          func(p *corev1.Pod) { p.Spec.NodeName = "other" },
		"image":         func(p *corev1.Pod) { p.Spec.Containers[0].Image = "other" },
		"capability": func(p *corev1.Pod) {
			p.Spec.Containers[0].SecurityContext.Capabilities.Add = append(p.Spec.Containers[0].SecurityContext.Capabilities.Add, "SYS_RAWIO")
		},
		"deadline":         func(p *corev1.Pod) { p.Spec.ActiveDeadlineSeconds = ptr.To(int64(2)) },
		"added spec key":   func(p *corev1.Pod) { p.Spec.HostPID = true },
		"removed spec key": func(p *corev1.Pod) { p.Spec.EnableServiceLinks = nil },
	} {
		t.Run(name, func(t *testing.T) {
			p := next.DeepCopy()
			mutate(p)
			if maintenanceAdmissionAllowed(t, "UPDATE", caller, old, p, g) {
				t.Fatal("mutation accepted")
			}
		})
	}
	for _, actor := range []string{"system:serviceaccount:dev-agents:dev-env-agent", "system:serviceaccount:dev-agents:grant-other", "system:serviceaccount:dev-env-system:dev-env-operator"} {
		if maintenanceAdmissionAllowed(t, "UPDATE", actor, old, next, g) {
			t.Fatalf("caller %s accepted", actor)
		}
	}
	// JSON omission of optional maps/lists must not make valid cleanup error.
	old.Annotations = nil
	old.Finalizers = nil
	next.Annotations = nil
	next.Finalizers = nil
	if !maintenanceAdmissionAllowed(t, "UPDATE", caller, old, next, g) {
		t.Fatal("absent optional metadata refused")
	}
	old.Spec.ActiveDeadlineSeconds = ptr.To(int64(1))
	if maintenanceAdmissionAllowed(t, "UPDATE", caller, old, next, g) {
		t.Fatal("non-shortening update accepted")
	}
}

// A real isolated 1.35 API server supplies defaults, generation, managedFields,
// UID and RV. The CEL check is evaluated on its persisted normalized result.
// No Kyverno installation, scheduler, kubelet or actual stop proof is implied.
func TestMaintenanceDeadlineAPIServerLifecycle(t *testing.T) {
	suite(t)
	ctx := context.Background()
	b, g := maintenanceFixture(t)
	approve(g, "human/dev-env-system/dev-env-human", g.Spec.TTL.Duration, b.now())
	p := MaintenancePod(g, b.now().Time)
	p.Name = name("grant-maintenance")
	g.Name = p.Name
	p.Labels["dev-env.haynesops.com/grant"] = g.Name
	p.OwnerReferences[0].Name = g.Name
	if err := admin.Create(ctx, p, client.FieldOwner("maintenance-broker")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Delete(ctx, p, client.PropagationPolicy(metav1.DeletePropagationOrphan)) })
	old := p.DeepCopy()
	// Both JSON Patch test operations must still fail at the real API server.
	for _, field := range []string{"uid", "resourceVersion"} {
		stale := old.DeepCopy()
		if field == "uid" {
			stale.UID = "replacement"
		} else {
			stale.ResourceVersion = "stale"
		}
		if err := admin.Patch(ctx, stale, client.RawPatch(types.JSONPatchType, StopMaintenancePatch(stale))); err == nil {
			t.Fatalf("stale %s patch accepted", field)
		}
	}
	if old.Generation < 1 || len(old.ManagedFields) == 0 || old.Spec.DNSPolicy == "" {
		t.Fatal("API normalization absent")
	}
	if err := admin.Patch(ctx, p, client.RawPatch(types.JSONPatchType, StopMaintenancePatch(p)), client.FieldOwner("maintenance-stop")); err != nil {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
		t.Fatal(err)
	}
	if p.Generation != old.Generation+1 || p.ResourceVersion == old.ResourceVersion || reflect.DeepEqual(p.ManagedFields, old.ManagedFields) {
		t.Fatal("API generation/RV not advanced")
	}
	caller := "system:serviceaccount:dev-env-system:dev-env-broker"
	if !maintenanceAdmissionAllowed(t, "UPDATE", caller, old, p, g) {
		t.Fatal("API-normalized patch refused")
	}
	if maintenanceAdmissionAllowed(t, "DELETE", caller, p, nil, g) {
		t.Fatal("nonterminal delete accepted")
	}
	p.Status.Phase = corev1.PodFailed
	if err := admin.Status().Update(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(p), p); err != nil {
		t.Fatal(err)
	}
	if !maintenanceAdmissionAllowed(t, "DELETE", caller, p, nil, g) {
		t.Fatal("terminal observation refused")
	}
	if err := admin.Delete(ctx, p, client.GracePeriodSeconds(0), client.PropagationPolicy(metav1.DeletePropagationOrphan)); err != nil {
		t.Fatal(err)
	}
}
