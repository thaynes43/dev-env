package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// A title is not part of the pod template. Changing the title annotation does
// not make a current pod look outdated, and ensure does not build a
// replacement while the pod is already there.
func TestTitleDoesNotStartADrainOrReplaceAPod(t *testing.T) {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "title-only", Namespace: "dev-agents"}}
	s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	s.Spec.Mode = v1alpha1.ModeLocal
	s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentIdle, Attached: 0}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "title-only", Namespace: "dev-agents", Labels: map[string]string{v1alpha1.LabelRevision: "old"}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	if !drainEligible(s, pod, "new") {
		t.Fatal("an outdated idle local pod was not drain eligible")
	}
	s.Annotations = map[string]string{v1alpha1.AnnotationTitle: "Other"}
	if !drainEligible(s, pod, "new") {
		t.Fatal("a title annotation made an outdated idle pod ineligible")
	}
	s.Spec.Mode = v1alpha1.ModeRemote
	if drainEligible(s, pod, "new") {
		t.Fatal("a remote session became drain eligible")
	}
	r := &Reconciler{}
	obs := &observation{pod: owned[*corev1.Pod]{obj: pod}}
	if err := r.ensure(context.Background(), s, nil, obs); err != nil {
		t.Fatal(err)
	}
}

// ADR-027: the title annotation is not spec, so metadata.generation stays.
// The drain restart proof, the rescue record and the shared rescue each
// compare that generation. A canary publication uses the same generation
// (publishedGeneration in the API). A decision write denies when the live
// generation differs from the caller's snapshot. None of those move here.
func TestTitleAnnotationLeavesGenerationProofs(t *testing.T) {
	s := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Title = "Old"
	})
	key := client.ObjectKeyFromObject(s)
	before := &v1alpha1.AgentSession{}
	if err := k8s.Get(context.Background(), key, before); err != nil {
		t.Fatal(err)
	}
	if before.Generation == 0 {
		t.Fatal("create left generation 0")
	}
	orig := before.DeepCopy()
	if before.Annotations == nil {
		before.Annotations = map[string]string{}
	}
	before.Annotations[v1alpha1.AnnotationTitle] = "New"
	if err := k8s.Patch(context.Background(), before, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	after := &v1alpha1.AgentSession{}
	if err := k8s.Get(context.Background(), key, after); err != nil {
		t.Fatal(err)
	}
	if after.Generation != orig.Generation || after.Spec.Title != "Old" || after.Annotations[v1alpha1.AnnotationTitle] != "New" || after.UID != orig.UID {
		t.Fatalf("generation %d/%d spec %q annotation %q", orig.Generation, after.Generation, after.Spec.Title, after.Annotations[v1alpha1.AnnotationTitle])
	}
	// writeDecisionAuthority denies when these differ.
	if after.Generation != orig.Generation || after.UID != orig.UID {
		t.Fatal("a title change would deny an in-flight decision write")
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "pod-1"}}
	after.Status.Restart = &v1alpha1.RestartStatus{ConversationID: "conv-1", PodUID: "pod-1", Generation: after.Generation}
	after.Status.Rescue = &v1alpha1.RescueStatus{
		Result: v1alpha1.RescueVerified, PodUID: "pod-1", SourcePodUID: "src-1", Generation: after.Generation,
	}
	if !restartProofFor(after, pod) || !rescueRanIn(after, pod) || !sharedRescued(after) {
		t.Fatal("a title annotation broke a same-generation restart or rescue proof")
	}
	bumped := after.DeepCopy()
	bumped.Generation++
	if restartProofFor(bumped, pod) || rescueRanIn(bumped, pod) || sharedRescued(bumped) {
		t.Fatal("the proofs do not compare generation")
	}
}
