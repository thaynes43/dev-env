package broker

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
