package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// The envtest cases of D-60, the idle timer.

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
