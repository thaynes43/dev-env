package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Characterize M4, without implementing its proposed recovery. Only envtest's
// objects and a fake rescue are used; no container exists or receives a signal.
func TestCodexRemoteResumeAfterRefusedStopKeepsTheExistingPod(t *testing.T) {
	ctx := context.Background()
	s := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Agent = v1alpha1.AgentCodex
		s.Spec.Mode = v1alpha1.ModeRemote
		s.Spec.Model = "gpt-6.1-sol"
		s.Spec.Effort = "medium"
		s.Spec.Prompt = ""
	})
	writes := &recordingClient{Client: k8s}
	fake := &fakeRescuer{answers: map[string]func(*corev1.Pod) (protocol.RescueReport, error){}, calls: map[string]int{}}
	fake.answers[s.Name] = func(*corev1.Pod) (protocol.RescueReport, error) {
		return protocol.RescueReport{}, errors.New("codex writers did not quiesce within the stop budget; rescue blocked")
	}
	r := &Reconciler{Client: writes, APIReader: k8s, Templates: templatesKey, Rescuer: fake, SessionNamespace: sessionNS}
	reconcile := func() ctrl.Result {
		t.Helper()
		result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	reconcile()
	original := markRunning(t, waitPod(t, s.Name), "envtest-node")
	home := waitClaim(t, s.Name)
	reconcile()
	setMode(t, s.Name, v1alpha1.OperatingModeSuspended)
	if result := reconcile(); result.RequeueAfter != rescueRetry {
		t.Fatalf("refused stop retry=%v", result)
	}
	blocked := session(t, s.Name)
	c := condition(&blocked.Status, ConditionRemovalBlocked)
	if fake.callsFor(s.Name) != 1 || c == nil || !strings.Contains(c.Message, "rescue blocked") || blocked.Status.Rescue != nil {
		t.Fatalf("stop refusal: calls=%d condition=%+v rescue=%+v", fake.callsFor(s.Name), c, blocked.Status.Rescue)
	}
	setMode(t, s.Name, v1alpha1.OperatingModeRunning)
	reconcile()
	reconcile() // Repeated observation must not replace the retained pod either.
	got := waitPod(t, s.Name)
	resumed := session(t, s.Name)
	if got.UID != original.UID || !got.DeletionTimestamp.IsZero() || resumed.Status.Phase != v1alpha1.PhaseRunning || condition(&resumed.Status, ConditionRemovalBlocked) != nil || fake.callsFor(s.Name) != 1 {
		t.Fatalf("resume changed retained pod or retried stop: pod=%s original=%s deletion=%v phase=%s condition=%+v calls=%d", got.UID, original.UID, got.DeletionTimestamp, resumed.Status.Phase, condition(&resumed.Status, ConditionRemovalBlocked), fake.callsFor(s.Name))
	}
	if gotHome := waitClaim(t, s.Name); gotHome.UID != home.UID || !gotHome.DeletionTimestamp.IsZero() {
		t.Fatal("resume replaced the original HOME")
	}
	for _, write := range writes.orderLog() {
		if strings.HasPrefix(write, "delete ") {
			t.Fatalf("unexpected deletion: %s", write)
		}
	}
	t.Log("resume retains the same pod, reports Running, clears RemovalBlocked and does not retry the refused stop; envtest cannot observe its frozen processes or HOME fence")
}
