package v1alpha1_test

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	commonfixture "github.com/thaynes43/dev-env/internal/commonrules/testutil"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestCommonRuleSchemaRetainsImmutableBindingAndLegacyAbsence(t *testing.T) {
	f := testutil.New(t, "claude", "dev/dev-env")
	snapshot := commonfixture.Snapshot(t, []byte(f.Session.Annotations[v1alpha1.AnnotationProjectSnapshot]), "COMMON_MARKER", "HEADER")
	f.Session.Spec.CommonRules, _ = snapshot.Binding()
	f.Session.Spec.ProjectCanary.CommonRevision = snapshot.CommonRevision
	s := f.Session
	s.Name = name()
	s.Namespace = ns
	s.UID = ""
	s.Generation = 0
	if err := k8s.Create(ctx(t), s); err != nil {
		t.Fatal("valid common binding refused", err)
	}
	var stored v1alpha1.AgentSession
	if err := k8s.Get(ctx(t), client.ObjectKeyFromObject(s), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.CommonRules == nil || stored.Spec.CommonRules.Snapshot != s.Spec.CommonRules.Snapshot || stored.Spec.CommonRules.SnapshotDigest != s.Spec.CommonRules.SnapshotDigest {
		t.Fatal("schema pruned retained source")
	}
	for _, change := range []func(*v1alpha1.AgentSession){func(s *v1alpha1.AgentSession) { s.Spec.CommonRules = nil }, func(s *v1alpha1.AgentSession) { s.Spec.CommonRules.Snapshot += " " }, func(s *v1alpha1.AgentSession) {
		s.Spec.CommonRules.CommonRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}, func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary.CommonRevision = "" }} {
		copy := stored.DeepCopy()
		change(copy)
		if err := k8s.Update(ctx(t), copy); err == nil {
			t.Fatal("common immutable input changed")
		}
	}
	legacy := taskSession()
	if err := k8s.Create(ctx(t), legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Spec.CommonRules = s.Spec.CommonRules
	wantInvalid(t, k8s.Update(ctx(t), legacy), "immutable")
	invalid := taskSession()
	invalid.Spec.CommonRules = s.Spec.CommonRules
	if err := k8s.Create(ctx(t), invalid); err == nil {
		t.Fatal("common rules widened ordinary route")
	}
}
