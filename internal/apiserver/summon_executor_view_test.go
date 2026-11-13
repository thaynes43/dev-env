package apiserver

import (
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// OP780, proof 7: the session view, what agent-run show prints, picks its
// status fields one by one and has none for status.summon, so the remote
// executor latch changes no session's view; an ordinary session, which never
// has status.summon, is shown exactly as before.
func TestSessionViewIgnoresTheSummonExecutorLatch(t *testing.T) {
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "ordinary-1", Namespace: "dev-agents", CreationTimestamp: metav1.NewTime(time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC))},
		Spec: v1alpha1.AgentSessionSpec{Repo: "dev-env", Agent: v1alpha1.AgentClaude, Mode: v1alpha1.ModeRemote, Model: "claude-opus-5-5"}}
	before := view(s, false)
	latched := s.DeepCopy()
	at := metav1.NewTime(time.Date(2026, 11, 12, 16, 1, 0, 0, time.UTC))
	latched.Status.Summon = &v1alpha1.SummonStatus{AdmittedAt: &at, Executor: &v1alpha1.SummonExecutorStatus{Attempted: true, Intent: "intent-1", PodUID: "pod-1"}}
	if after := view(latched, false); !reflect.DeepEqual(before, after) {
		t.Fatalf("the latch changed the view:\n%+v\n%+v", before, after)
	}
}
