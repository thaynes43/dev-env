package v1alpha1_test

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// TestDeleteBumpsGenerationOnce pins the API server behaviour the fixed canary
// result reader relies on (DESIGN-022 amendment, history-03): a DELETE of a
// finalizer-held AgentSession sets deletionTimestamp and bumps
// metadata.generation exactly once. A second DELETE does not bump it again,
// status and metadata writes never do, and a later spec change still does.
func TestDeleteBumpsGenerationOnce(t *testing.T) {
	s := taskSession()
	s.Finalizers = []string{v1alpha1.ProjectCanaryFinalizer}
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		got := &v1alpha1.AgentSession{}
		if k8s.Get(ctx(t), client.ObjectKeyFromObject(s), got) == nil {
			got.Finalizers = nil
			_ = k8s.Update(ctx(t), got)
		}
	})
	if g := get(t, s).Generation; g != 1 {
		t.Fatalf("created generation %d, want 1", g)
	}
	// A natural stop is a status write; labels are metadata. Neither moves it.
	stopped := get(t, s)
	stopped.Status.Phase = v1alpha1.PhaseFailed
	if err := k8s.Status().Update(ctx(t), stopped); err != nil {
		t.Fatal(err)
	}
	labelled := get(t, s)
	labelled.Labels = map[string]string{"example": "metadata-only"}
	if err := k8s.Update(ctx(t), labelled); err != nil {
		t.Fatal(err)
	}
	if g := get(t, s).Generation; g != 1 {
		t.Fatalf("status/metadata writes moved generation to %d", g)
	}
	if err := k8s.Delete(ctx(t), get(t, s)); err != nil {
		t.Fatal(err)
	}
	reaped := get(t, s)
	if reaped.DeletionTimestamp.IsZero() || reaped.Generation != 2 {
		t.Fatalf("reap: deletionTimestamp=%v generation=%d, want set and 2", reaped.DeletionTimestamp, reaped.Generation)
	}
	if err := k8s.Delete(ctx(t), reaped); err != nil {
		t.Fatal(err)
	}
	if g := get(t, s).Generation; g != 2 {
		t.Fatalf("second DELETE moved generation to %d", g)
	}
	suspended := get(t, s)
	suspended.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if err := k8s.Update(ctx(t), suspended); err != nil {
		t.Fatal(err)
	}
	if g := get(t, s).Generation; g != 3 {
		t.Fatalf("spec change after the reap left generation at %d, want 3", g)
	}
}
