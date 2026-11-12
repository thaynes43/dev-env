package controller

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func summonOutcomeFixture(t *testing.T) (client.WithWatch, *Reconciler, *v1alpha1.AgentSession, *corev1.Pod) {
	t.Helper()
	s := taskSession()
	s.UID = "summon-outcome-session"
	s.Spec.Caller = "upgrade-agent.alert-responder"
	s.Spec.Lane = v1alpha1.LaneRemediation
	s.Spec.Summon = remediationSummon()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "summon-pod", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "agent"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}).WithObjects(s, p).Build()
	r := &Reconciler{Client: c, APIReader: c, Now: func() time.Time { return time.Date(2026, 11, 12, 5, 30, 0, 0, time.UTC) }}
	return c, r, s, p
}

func TestSummonOutcomePendingThenRunning(t *testing.T) {
	c, r, s, _ := summonOutcomeFixture(t)
	key := client.ObjectKeyFromObject(s)
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, s); err != nil {
		t.Fatal(err)
	}
	if s.Status.Outcome == nil || s.Status.Outcome.State != v1alpha1.OutcomePending {
		t.Fatalf("first reconcile: %+v", s.Status.Outcome)
	}
	at := metav1.NewTime(r.Now())
	s.Status.Agent = &v1alpha1.AgentStatus{Status: "busy"}
	if err := c.Status().Update(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, s); err != nil {
		t.Fatal(err)
	}
	if s.Status.Outcome.State != v1alpha1.OutcomeRunning {
		t.Fatalf("started: %+v", s.Status.Outcome)
	}
	original := s.Status.Outcome.DeepCopy()
	r.Now = func() time.Time { return at.Add(time.Hour) }
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), key, s); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(original, s.Status.Outcome) {
		t.Fatal("ordinary observation pretended to be progress")
	}
}

func TestSummonOutcomeAllContainersTerminated(t *testing.T) {
	for _, mode := range []string{"all", "missing", "sidecar-running", "ephemeral-running", "foreign", "succeeded"} {
		t.Run(mode, func(t *testing.T) {
			c, r, s, p := summonOutcomeFixture(t)
			terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.NewTime(r.Now())}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", State: terminated}}
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: terminated}}
			switch mode {
			case "missing":
				p.Status.InitContainerStatuses = nil
			case "sidecar-running":
				p.Status.InitContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
			case "ephemeral-running":
				p.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debug"}}}
				p.Status.EphemeralContainerStatuses = []corev1.ContainerStatus{{Name: "debug", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
			case "foreign":
				p.OwnerReferences[0].UID = "other-session"
			case "succeeded":
				p.Status.Phase = corev1.PodSucceeded
				p.Status.ContainerStatuses = nil
			}
			status := p.Status
			if err := c.Update(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			p.Status = status
			if err := c.Status().Update(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)}); err != nil {
				t.Fatal(err)
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			wantClose := mode == "all" || mode == "succeeded"
			if wantClose {
				if s.Status.Outcome == nil || s.Status.Outcome.Note != "unreported: pod-terminated" {
					t.Fatalf("outcome: %+v", s.Status.Outcome)
				}
			} else if s.Status.Outcome != nil && s.Status.Outcome.State == v1alpha1.OutcomeEscalated {
				t.Fatal("incomplete or foreign pod closed the session")
			}
		})
	}
}

// taskResult is a pure projection of stored status. It supplies row 1 even
// before the ordinary observer has materialized status.taskResult (OP740 Q-4).
func TestSummonOutcomeDerivedTaskResultClosesImmediately(t *testing.T) {
	for _, source := range []string{"heartbeat", "private-terminal"} {
		t.Run(source, func(t *testing.T) {
			c, r, s, _ := summonOutcomeFixture(t)
			task := v1alpha1.TaskStatus{TimedOut: true, ExitCode: 143, FinishedAt: metav1.Time{Time: r.Now()}}
			s.Status.Agent = &v1alpha1.AgentStatus{Status: "exited"}
			if source == "heartbeat" {
				s.Status.Agent.Task = &task
			} else {
				s.Status.PrivateHistory = &v1alpha1.PrivateHistoryStatus{TaskTerminal: &v1alpha1.PrivateTaskTerminalStatus{Task: task}}
			}
			if err := c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			// Lifecycle handling can wait on a grace period or preservation. The close
			// still happens first and writes no process-result field of its own.
			r.PrivateHistoryRetention = true
			r.PrivateTerminalRecord = true
			r.TerminalTaskPodReclaim = true
			key := client.ObjectKeyFromObject(s)
			run := func() {
				t.Helper()
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
					t.Fatal(err)
				}
				if err := c.Get(context.Background(), key, s); err != nil {
					t.Fatal(err)
				}
			}
			run()
			if s.Status.Outcome == nil || s.Status.Outcome.Note != "unreported: timed-out" || s.Status.TaskResult != nil {
				t.Fatal("first pass did not close from the durable task, or wrote taskResult")
			}
			outcome := s.Status.Outcome.DeepCopy()
			summon := s.Status.Summon.DeepCopy()
			if source == "heartbeat" {
				r.PrivateHistoryRetention = false
				r.PrivateTerminalRecord = false
				r.TerminalTaskPodReclaim = false
				run()
				if s.Status.TaskResult == nil || s.Status.TaskResult.State != v1alpha1.TaskResultTimedOut {
					t.Fatal("ordinary observation did not materialize the same result")
				}
				if !reflect.DeepEqual(outcome, s.Status.Outcome) || !reflect.DeepEqual(summon, s.Status.Summon) {
					t.Fatal("ordinary observation changed the terminal record")
				}
			}
		})
	}
}
