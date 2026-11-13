package broker

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestDepth3Readers is the section 8.3 table for the broker. A child at
// depth 3 is not an ancestry authority, not a maintenance approver and not
// a legacy delegate.
func TestDepth3Readers(t *testing.T) {
	sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "deep", UID: "uid-deep", Labels: map[string]string{v1alpha1.LabelDepth: "3"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude, Profile: "full", Parent: "dev-env-system/dev-env-human"}}
	_, err := ancestry(context.Background(), nil, "dev-agents", sess)
	if err == nil || !strings.Contains(err.Error(), "ambiguous ancestry depth") {
		t.Fatalf("ancestry: %v", err)
	}
	if topLevelSession(sess) || listedCreator(sess, []string{sess.Spec.Parent}) {
		t.Fatal("a depth-3 session was a maintenance approver")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "deep", UID: "pod-deep", Labels: map[string]string{v1alpha1.LabelProfile: "full"}}, Spec: corev1.PodSpec{ServiceAccountName: "dev-env-agent"}}
	if err := CheckLegacyDelegate(context.Background(), nil, nil, "dev-agents/dev-env-agent", &DelegateIdentity{Form: 3, ServiceAccountUID: "sa-uid"}, sess, pod); err == nil {
		t.Fatal("a depth-3 session passed the legacy delegate check")
	}
}

// A binding that matches in every field but depth is still refused. The same
// binding at depth 1 is accepted, so the refusal is the depth.
func TestLegacyDelegateDepthIsTheOnlyDifference(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	const saName = "dev-env-agent"
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: saName, UID: "sa-uid"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sa).Build()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "deep", Namespace: "dev-agents", UID: "pod-deep", Labels: map[string]string{v1alpha1.LabelProfile: "full"}}, Spec: corev1.PodSpec{ServiceAccountName: saName}}
	sess := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "deep", UID: "uid-deep", Labels: map[string]string{v1alpha1.LabelDepth: "3"}}, Spec: v1alpha1.AgentSessionSpec{Agent: v1alpha1.AgentClaude, Profile: "full"}}
	d := &DelegateIdentity{Form: 3, ServiceAccountUID: "sa-uid", Session: "deep", SessionUID: "uid-deep", PodName: "deep", PodUID: "pod-deep"}
	b := []LegacyDelegateBinding{{ServiceAccount: "dev-agents/" + saName, ServiceAccountUID: "sa-uid", Session: "deep", SessionUID: "uid-deep", PodName: "deep", PodUID: "pod-deep"}}
	by := DelegateApprover + "dev-agents/" + saName
	if err := CheckLegacyDelegate(context.Background(), c, b, by, d, sess, pod); err == nil {
		t.Fatal("depth 3 passed a binding that matches in every other field")
	}
	sess.Labels[v1alpha1.LabelDepth] = "1"
	if err := CheckLegacyDelegate(context.Background(), c, b, by, d, sess, pod); err != nil {
		t.Fatalf("depth 1 was refused: %v", err)
	}
}
