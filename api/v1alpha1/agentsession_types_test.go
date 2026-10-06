package v1alpha1

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestAddToSchemeRegistersAgentSession(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}

	for _, obj := range []runtime.Object{&AgentSession{}, &AgentSessionList{}} {
		gvks, _, err := s.ObjectKinds(obj)
		if err != nil {
			t.Fatalf("ObjectKinds(%T): %v", obj, err)
		}
		if len(gvks) != 1 {
			t.Fatalf("ObjectKinds(%T) = %v, want one kind", obj, gvks)
		}
		if gvks[0].Group != "dev-env.haynesops.com" || gvks[0].Version != "v1alpha1" {
			t.Errorf("%T registered as %s, want group dev-env.haynesops.com, version v1alpha1", obj, gvks[0])
		}
	}
}

func TestDeepCopyIsIndependent(t *testing.T) {
	orig := &AgentSession{
		Spec: AgentSessionSpec{
			Repo:  "haynes-ops",
			Agent: AgentClaude,
			Mode:  ModeTask,
			Model: "claude-opus-5-5",
			Tools: []string{"blender"},
			LLM:   &LLMSpec{Pool: "llm-coder"},
		},
	}

	cp := orig.DeepCopy()
	cp.Spec.Tools[0] = "audio"
	cp.Spec.LLM.Pool = "other"

	if orig.Spec.Tools[0] != "blender" || orig.Spec.LLM.Pool != "llm-coder" {
		t.Errorf("DeepCopy shares memory with the original: %+v", orig.Spec)
	}
}
