package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// The envtest cases of D-60, the idle timer.

func TestSharedIdleDeadlineExcludesBootAndAdmissionWait(t *testing.T) {
	now := time.Now().UTC()
	pod := owned[*corev1.Pod]{obj: &corev1.Pod{Status: corev1.PodStatus{
		Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
	}}}
	s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
		Spec: v1alpha1.AgentSessionSpec{Workspace: &v1alpha1.WorkspaceSpec{ID: "w"},
			Lifecycle: &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: time.Second}}}}
	for _, a := range []v1alpha1.AgentStatus{
		{Boot: protocol.BootBooting, Status: protocol.AgentIdle},
		{Boot: protocol.BootReady, Status: protocol.AgentPending},
	} {
		s.Status.Agent = &a
		if _, _, ok := idleDeadline(s, &templates.Templates{}, pod); ok {
			t.Fatalf("shared boot/admission wait was eligible for idle suspension: %+v", a)
		}
	}
	s.Status.Agent = &v1alpha1.AgentStatus{Boot: protocol.BootReady, Status: protocol.AgentIdle}
	if _, _, ok := idleDeadline(s, &templates.Templates{}, pod); !ok {
		t.Fatal("shared ready idle session lost its idle timer")
	}
	s.Spec.Workspace = nil
	s.Status.Agent = &v1alpha1.AgentStatus{Boot: protocol.BootBooting, Status: protocol.AgentPending}
	if _, _, ok := idleDeadline(s, &templates.Templates{}, pod); !ok {
		t.Fatal("private pending-session behavior changed")
	}
}

// setAgent writes what a heartbeat would: the agent's state and last activity.
func setAgent(t *testing.T, name, state string, last time.Time) {
	t.Helper()
	eventually(t, "the heartbeat's status", func() error {
		live := session(t, name)
		lt := metav1.NewTime(last)
		live.Status.Agent = &v1alpha1.AgentStatus{Status: state, LastActivity: &lt}
		return k8s.Status().Update(context.Background(), live)
	})
}

func idleSession(t *testing.T, window time.Duration) *v1alpha1.AgentSession {
	t.Helper()
	s := newSession(t, func(s *v1alpha1.AgentSession) {
		s.Spec.Lifecycle = &v1alpha1.Lifecycle{IdleSuspendAfter: &metav1.Duration{Duration: window}}
	})
	waitClaim(t, s.Name)
	markRunning(t, waitPod(t, s.Name), "talosw02")
	waitStatus(t, s.Name, "Running", phaseIs(v1alpha1.PhaseRunning))
	return s
}

// An agent that is not busy, with nothing new for longer than its window, is
// suspended: operatingMode goes Suspended, marked as the idle timer's, and the
// usual rescue and pod delete follow.
func TestTheIdleTimerSuspendsAnIdleSession(t *testing.T) {
	startOperator(t)
	s := idleSession(t, 2*time.Second)
	pod := waitPod(t, s.Name)
	rescuer.answer(t, s.Name, reportFor(cleanReport))
	setAgent(t, s.Name, "idle", time.Now().Add(-time.Hour))
	eventually(t, "the idle timer suspends it", func() error {
		live := session(t, s.Name)
		if live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || live.Annotations[v1alpha1.AnnotationSuspendedBy] != IdleTimerSuspender {
			return fmt.Errorf("operatingMode %q, annotations %v", live.Spec.OperatingMode, live.Annotations)
		}
		return nil
	})
	finishPodDelete(t, pod)
	waitStatus(t, s.Name, "Suspended", phaseIs(v1alpha1.PhaseSuspended))
}

// A busy agent, or one that has not reported, is never suspended; nor is one
// whose window has not passed.
func TestTheIdleTimerLeavesABusySession(t *testing.T) {
	startOperator(t)
	silent := idleSession(t, 2*time.Second)
	busy := idleSession(t, 2*time.Second)
	recent := idleSession(t, time.Hour)
	setAgent(t, busy.Name, protocol.AgentBusy, time.Now().Add(-time.Hour))
	setAgent(t, recent.Name, "idle", time.Now().Add(-time.Minute))
	time.Sleep(4 * time.Second)
	for _, name := range []string{silent.Name, busy.Name, recent.Name} {
		if m := session(t, name).Spec.OperatingMode; m == v1alpha1.OperatingModeSuspended {
			t.Errorf("%s was suspended", name)
		}
	}
}

// A resume that lands while the idle suspend's rescue runs keeps the old pod
// (the rescue's record is dropped, D-51). Its start and activity are old, but
// the timer counts from the resume too, so it does not suspend again at once.
func TestAResumeDuringTheIdleRescueSticks(t *testing.T) {
	startOperator(t)
	s := idleSession(t, 5*time.Second)
	time.Sleep(5 * time.Second) // the session is now older than its window
	release := make(chan struct{})
	rescuer.answer(t, s.Name, func(p *corev1.Pod) (protocol.RescueReport, error) {
		<-release
		return cleanReport(p.Name), nil
	})
	setAgent(t, s.Name, "idle", time.Now().Add(-time.Hour))
	eventually(t, "the idle timer suspends it", func() error {
		if m := session(t, s.Name).Spec.OperatingMode; m != v1alpha1.OperatingModeSuspended {
			return fmt.Errorf("operatingMode %q", m)
		}
		return nil
	})
	eventually(t, "the API's resume", func() error {
		live := session(t, s.Name)
		live.Spec.OperatingMode = v1alpha1.OperatingModeRunning
		delete(live.Annotations, v1alpha1.AnnotationSuspendedBy)
		live.Annotations[v1alpha1.AnnotationResumedAt] = time.Now().UTC().Format(time.RFC3339)
		return k8s.Update(context.Background(), live)
	})
	close(release)
	time.Sleep(3 * time.Second)
	if m := session(t, s.Name).Spec.OperatingMode; m != v1alpha1.OperatingModeRunning {
		t.Errorf("the resume was undone: operatingMode %q", m)
	}
}
