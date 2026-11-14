package apiserver

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/templates"
)

// TestDepth3Readers is the section 8.3 table for this package. A child at
// depth 3 starts nothing. Its usage still rolls up. The ancestry walk does
// not refuse it for the label alone.
func TestDepth3Readers(t *testing.T) {
	deep := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "deep", Namespace: sessionNS, UID: "deep", Labels: map[string]string{v1alpha1.LabelDepth: "3"}}}
	if sessionDepth(deep) != 3 {
		t.Fatalf("sessionDepth: %d", sessionDepth(deep))
	}
	p := Policy{NamedCoordinatorIdentities: true, Human: humanSA, Clients: []string{clientSA}, WorkbenchServiceAccount: templates.WorkbenchServiceAccount}
	if p.NamedCoordinatorLaunchAllowed(LauncherHuman, p.Human, 3) || p.NamedCoordinatorLaunchAllowed(LauncherClient, clientSA, 3) || p.NamedCoordinatorLaunchAllowed(LauncherWorkbenchSession, "bench", 3) {
		t.Fatal("a depth of 3 was admitted as a named coordinator")
	}
	marked := deep.DeepCopy()
	marked.Labels[templates.CoordinatorIdentityLabel] = "dev-env-coordinator-lead"
	marked.Annotations = map[string]string{NamedCoordinatorLauncherAnnotation: string(LauncherHuman)}
	marked.Spec.Parent = p.Human
	if p.NamedCoordinatorSessionEligible(marked) {
		t.Fatal("a depth-3 session was eligible as a named coordinator")
	}
	f := newFixture(t)
	if got := f.srv.sessionAncestry(context.Background(), deep, "root", "root"); got != ancestryOutside {
		t.Fatalf("ancestry treated the depth label as a refusal: %d", got)
	}
	early := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	later := metav1.NewTime(early.Add(time.Minute))
	root := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "root", UID: "root", CreationTimestamp: early}}
	child := v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "child", UID: "child", Labels: map[string]string{v1alpha1.LabelDepth: "3"}, CreationTimestamp: later}, Spec: v1alpha1.AgentSessionSpec{Parent: "root"}}
	roll := usageRollup(root, []v1alpha1.AgentSession{*root, child})
	if roll == nil || roll.Sessions != 1 {
		t.Fatalf("a depth-3 child was not counted: %+v", roll)
	}
}
