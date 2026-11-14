package apiserver

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestRaiseAnnotationLeavesGenerationUnchanged is the envtest proof that a
// raise annotation is metadata and does not move metadata.generation. A spec
// write in the same test does.
func TestRaiseAnnotationLeavesGenerationUnchanged(t *testing.T) {
	cfg := envConfig(t)
	k8s, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := k8s.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: sessionNS}}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "raise-gen-" + string(uuid.NewUUID())[:8], Namespace: sessionNS}, Spec: v1alpha1.AgentSessionSpec{
		Repo: "haynes-ops", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeTask, Model: "claude-opus-5-5", Prompt: "generation proof",
	}}
	if err := k8s.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	gen := s.Generation
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Annotations[v1alpha1.AnnotationRaise] = "pending-record"
	if err := k8s.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	if s.Generation != gen {
		t.Fatalf("annotation moved generation from %d to %d", gen, s.Generation)
	}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := k8s.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		t.Fatal(err)
	}
	if s.Generation == gen {
		t.Fatal("a spec write left generation unchanged")
	}
}
