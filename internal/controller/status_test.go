package controller

import (
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// The phase and PodReady condition for each pod state, as a pure function of what
// one reconcile saw.
func TestPodPhase(t *testing.T) {
	now := metav1.Now()
	placed := func(edit func(*corev1.Pod)) owned[*corev1.Pod] {
		p := &corev1.Pod{Spec: corev1.PodSpec{NodeName: "talosw02"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
		edit(p)
		return owned[*corev1.Pod]{obj: p}
	}
	claim := owned[*corev1.PersistentVolumeClaim]{obj: &corev1.PersistentVolumeClaim{}}
	cases := []struct {
		name   string
		obs    observation
		edit   func(*v1alpha1.AgentSession)
		phase  v1alpha1.SessionPhase
		reason string
	}{
		{"no pod yet", observation{pod: owned[*corev1.Pod]{missing: true}, claim: claim}, nil, v1alpha1.PhasePending, "Creating"},
		{"no templates", observation{pod: owned[*corev1.Pod]{missing: true}, claim: claim, templatesErr: errors.New("templates: not found")}, nil, v1alpha1.PhasePending, "TemplatesInvalid"},
		{"an unknown profile", observation{pod: owned[*corev1.Pod]{missing: true}, claim: claim, buildErr: errors.New("profile x")}, nil, v1alpha1.PhasePending, "PodSpecInvalid"},
		{"a session agentd refuses", observation{pod: owned[*corev1.Pod]{missing: true}, claim: claim, buildErr: sessionInvalidError{errors.New("prompt")}}, nil, v1alpha1.PhaseFailed, "SessionInvalid"},
		{"suspended, no pod", observation{pod: owned[*corev1.Pod]{missing: true}, claim: claim}, func(s *v1alpha1.AgentSession) { s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended }, v1alpha1.PhaseSuspended, "Suspended"},
		{"a foreign pod", observation{pod: owned[*corev1.Pod]{foreign: "no controller"}, claim: claim}, nil, v1alpha1.PhasePending, "NameTaken"},
		{"a foreign volume", observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{foreign: "no controller"}}, nil, v1alpha1.PhasePending, "NameTaken"},
		{"a volume being deleted", observation{pod: owned[*corev1.Pod]{missing: true}, claim: owned[*corev1.PersistentVolumeClaim]{obj: &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now}}}}, nil, v1alpha1.PhasePending, "VolumeTerminating"},
		{"unscheduled", observation{claim: claim, pod: owned[*corev1.Pod]{obj: &corev1.Pod{}}}, nil, v1alpha1.PhasePending, "Scheduling"},
		{"unschedulable", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Spec.NodeName = ""
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/9 nodes"}}
		})}, nil, v1alpha1.PhasePending, "Unschedulable"},
		{"pulling", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "back-off"}}}}
		})}, nil, v1alpha1.PhasePending, "ImagePullBackOff"},
		{"crashed, restarting in place", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error"}}}}
		})}, nil, v1alpha1.PhasePending, "ContainerTerminated"},
		{"ready", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
		})}, nil, v1alpha1.PhaseRunning, "PodReady"},
		{"terminating", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.DeletionTimestamp = &now
		})}, nil, v1alpha1.PhasePending, "PodTerminating"},
		{"evicted", observation{claim: claim, pod: placed(func(p *corev1.Pod) {
			p.Status.Phase, p.Status.Reason = corev1.PodFailed, "Evicted"
		})}, nil, v1alpha1.PhaseFailed, "PodEnded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: "s"}, Spec: v1alpha1.AgentSessionSpec{OperatingMode: v1alpha1.OperatingModeRunning}}
			if c.edit != nil {
				c.edit(s)
			}
			phase, cond := podPhase(s, c.obs)
			if phase != c.phase || cond.Reason != c.reason {
				t.Errorf("phase %s, reason %s (%s); want %s, %s", phase, cond.Reason, cond.Message, c.phase, c.reason)
			}
			if (cond.Status == metav1.ConditionTrue) != (c.phase == v1alpha1.PhaseRunning) {
				t.Errorf("PodReady %s with phase %s", cond.Status, phase)
			}
		})
	}
}

func TestConditionReasonAndMessage(t *testing.T) {
	for in, want := range map[string]string{
		"ImagePullBackOff": "ImagePullBackOff",
		"has space":        "NotReady",
		"":                 "NotReady",
		"9Starts":          "NotReady",
	} {
		if got := conditionReason(in); got != want {
			t.Errorf("conditionReason(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("x", maxMessage+10)
	if got := truncate(long); len(got) != maxMessage || !strings.HasSuffix(got, "...") {
		t.Errorf("truncate gave %d bytes", len(got))
	}
}
