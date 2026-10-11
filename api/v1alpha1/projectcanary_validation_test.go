package v1alpha1_test

import (
	"testing"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/projectcanary/testutil"
)

func TestProjectCanarySchemaImmutableBindingAndBoundedContract(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := testutil.New(t, provider, "dev/dev-env")
			s := f.Session
			s.Name = name()
			s.Namespace = ns
			s.UID = ""
			s.Generation = 0
			if err := k8s.Create(ctx(t), s); err != nil {
				t.Fatal("valid typed canary refused", err)
			}
			for _, mutate := range []func(*v1alpha1.AgentSession){func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary.ID = "replacement" }, func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary = nil }, func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary.CatalogResourceVersion = "2" }, func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary.Image += "foreign" }} {
				copy := s.DeepCopy()
				mutate(copy)
				wantInvalid(t, k8s.Update(ctx(t), copy), "immutable")
			}
		})
	}
	ordinary := taskSession()
	if err := k8s.Create(ctx(t), ordinary); err != nil {
		t.Fatal(err)
	}
	ordinary.Spec.ProjectCanary = testutil.New(t, "claude", "dev/dev-env").Session.Spec.ProjectCanary
	wantInvalid(t, k8s.Update(ctx(t), ordinary), "immutable")
	for _, mutate := range []func(*v1alpha1.AgentSession){func(s *v1alpha1.AgentSession) { s.Spec.Limits.Timeout = dur(181 * time.Second) }, func(s *v1alpha1.AgentSession) { s.Spec.Limits.MaxTurns = 4 }, func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"} }, func(s *v1alpha1.AgentSession) { s.Spec.Restore = "bundle" }, func(s *v1alpha1.AgentSession) { s.Spec.Size = v1alpha1.SizeM }, func(s *v1alpha1.AgentSession) { s.Spec.Parent = "foreign/client" }, func(s *v1alpha1.AgentSession) { s.Spec.ProjectCanary.PolicyDigest = "wrong" }} {
		s := testutil.New(t, "claude", "dev/dev-env").Session
		s.Name = name()
		s.Namespace = ns
		s.UID = ""
		s.Generation = 0
		mutate(s)
		if err := k8s.Create(ctx(t), s); err == nil {
			t.Fatal("schema accepted weaker canary contract")
		}
	}
}
