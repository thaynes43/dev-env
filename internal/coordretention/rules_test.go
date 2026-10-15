package coordretention

import (
	"context"
	"testing"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	devv1 "github.com/thaynes43/dev-env/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEvaluateMemoComparesEachRequestedUID(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := devv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	live := &devv1.AgentSession{ObjectMeta: metav1.ObjectMeta{Namespace: "dev-agents", Name: "s", UID: types.UID("uid-live")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(live).Build()
	base := Summary{Kind: TaskClaim, Namespace: "dev-env-system", Valid: true}
	old, current := base, base
	old.Owner = SessionRef{"dev-agents", "s", "uid-old"}
	current.Owner = SessionRef{"dev-agents", "s", "uid-live"}
	got := Evaluate(context.Background(), EvalConfig{Reader: c, OperatorNamespace: "dev-env-system", ReconciledNamespaces: map[string]bool{"dev-agents": true}}, []Summary{old, current})
	if got[0].Decision != Released || got[1].Decision != Kept {
		t.Fatalf("decisions = %#v", got)
	}
}

func TestAckNeedsArchiveProof(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := devv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coord.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	m := MessageRef{Namespace: "dev-env-system", Project: "p", Name: "message.aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", UID: "message-uid"}
	s := Summary{Kind: InboxReadAck, Namespace: "dev-env-system", Valid: true, Message: m}
	config := EvalConfig{Reader: c, OperatorNamespace: "dev-env-system", Archive: map[MessageRef]ArchiveEvidence{m: {Known: true, Holds: false}}}
	if got := Evaluate(context.Background(), config, []Summary{s})[0]; got.Decision != Kept {
		t.Fatalf("no archive = %s", got.Decision)
	}
	config.Archive[m] = ArchiveEvidence{Known: true, Holds: true}
	if got := Evaluate(context.Background(), config, []Summary{s})[0]; got.Decision != Released {
		t.Fatalf("archive proof = %s", got.Decision)
	}
}
