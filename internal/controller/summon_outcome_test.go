package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

func summonOutcomeFixture(t *testing.T) (client.WithWatch, *Reconciler, *v1alpha1.AgentSession, *corev1.Pod) {
	t.Helper()
	s := taskSession()
	s.UID = "summon-outcome-session"
	s.Spec.Caller = "upgrade-agent.alert-responder"
	s.Spec.Lane = v1alpha1.LaneRemediation
	s.Spec.Summon = remediationSummon()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace, UID: "summon-pod", OwnerReferences: []metav1.OwnerReference{ownerRef(s)}}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{Name: "agent"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
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
	for _, mode := range []string{"never", "always", "missing", "sidecar-running", "ephemeral-running", "foreign", "succeeded", "failed"} {
		t.Run(mode, func(t *testing.T) {
			c, r, s, p := summonOutcomeFixture(t)
			p.Spec.RestartPolicy = corev1.RestartPolicyNever
			terminated := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: metav1.NewTime(r.Now())}}
			p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "agent", State: terminated}}
			p.Spec.InitContainers = []corev1.Container{{Name: "init"}}
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: terminated}}
			switch mode {
			case "always":
				p.Spec.RestartPolicy = corev1.RestartPolicyAlways
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
				p.Spec.RestartPolicy = corev1.RestartPolicyAlways
				p.Status.Phase = corev1.PodSucceeded
				p.Status.ContainerStatuses = nil
			case "failed":
				p.Spec.RestartPolicy = corev1.RestartPolicyAlways
				p.Status.Phase = corev1.PodFailed
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
			wantClose := mode == "never" || mode == "succeeded" || mode == "failed"
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

func TestSummonOutcomeOperatorStopSkipsCrashInputs(t *testing.T) {
	for _, stop := range []string{"deleting", "suspended"} {
		for _, input := range []string{"interrupted", "pod", "task-result", "exited", "failed"} {
			t.Run(stop+"/"+input, func(t *testing.T) {
				c, r, s, p := summonOutcomeFixture(t)
				if stop == "suspended" {
					s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
				} else {
					s.Finalizers = []string{Finalizer}
				}
				if err := c.Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if stop == "deleting" {
					if err := c.Delete(context.Background(), s); err != nil {
						t.Fatal(err)
					}
					if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
						t.Fatal(err)
					}
				}
				s.Status.Outcome = &v1alpha1.OutcomeStatus{State: v1alpha1.OutcomeRunning}
				s.Status.Agent = &v1alpha1.AgentStatus{Status: input}
				wantReason := ""
				switch input {
				case "pod":
					s.Status.Agent.Status = "busy"
					p.Status.Phase = corev1.PodFailed
					if err := c.Status().Update(context.Background(), p); err != nil {
						t.Fatal(err)
					}
				case "task-result":
					s.Status.TaskResult = &v1alpha1.TaskResultStatus{State: v1alpha1.TaskResultTimedOut}
					wantReason = "timed-out"
				case "exited", "failed":
					wantReason = "agent-" + input
				}
				if err := c.Status().Update(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				before := s.Status.DeepCopy()
				changed, err := r.reconcileSummonOutcome(context.Background(), s)
				if err != nil {
					t.Fatal(err)
				}
				if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
					t.Fatal(err)
				}
				if wantReason == "" {
					if changed || !reflect.DeepEqual(before, &s.Status) {
						t.Fatal("operator stop closed or changed the session through a crash input")
					}
				} else if !changed || s.Status.Summon == nil || s.Status.Summon.Closed == nil || s.Status.Summon.Closed.Reason != wantReason {
					t.Fatalf("operator stop suppressed an unchanged row: %+v", s.Status)
				}
			})
		}
	}
}

func TestSummonOutcomeInvalidSnapshotLeavesLifecycleOpen(t *testing.T) {
	for _, invalid := range []string{"missing", "invalid"} {
		t.Run(invalid, func(t *testing.T) {
			c, r, s, _ := summonOutcomeFixture(t)
			if invalid == "missing" {
				s.Spec.Summon = nil
			} else {
				s.Spec.Summon.OnUnreported = "invalid"
			}
			if err := c.Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			s.Status.Agent = &v1alpha1.AgentStatus{Status: "exited"}
			if err := c.Status().Update(context.Background(), s); err != nil {
				t.Fatal(err)
			}
			before := s.DeepCopy()
			var logs strings.Builder
			ctx := logr.NewContext(context.Background(), funcr.New(func(prefix, args string) { logs.WriteString(prefix + args) }, funcr.Options{}))
			changed, err := r.reconcileSummonOutcome(ctx, s)
			if changed || err != nil {
				t.Errorf("invalid snapshot blocks lifecycle: changed=%v err=%v", changed, err)
			}
			if !strings.Contains(logs.String(), "onUnreported snapshot") || !strings.Contains(logs.String(), s.Name) {
				t.Errorf("missing bounded snapshot diagnostic: %q", logs.String())
			}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(s), s); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, s) {
				t.Fatal("invalid snapshot wrote the session")
			}
		})
	}
}

func TestSummonOutcomeCachedTerminalSkipsLiveRead(t *testing.T) {
	for _, state := range []v1alpha1.OutcomeState{v1alpha1.OutcomeDone, v1alpha1.OutcomeFailed, v1alpha1.OutcomeEscalated} {
		t.Run(string(state), func(t *testing.T) {
			c, r, s, _ := summonOutcomeFixture(t)
			s.Status.Outcome = &v1alpha1.OutcomeStatus{State: state}
			reads := 0
			r.APIReader = interceptor.NewClient(c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
				reads++
				return errors.New("unexpected live read")
			}})
			changed, err := r.reconcileSummonOutcome(context.Background(), s)
			if changed || err != nil || reads != 0 {
				t.Fatalf("terminal cache: changed=%v err=%v reads=%d", changed, err, reads)
			}
		})
	}
}

func TestSummonOutcomeErrorHasNoRequeueAfter(t *testing.T) {
	c, r, s, _ := summonOutcomeFixture(t)
	wantErr := errors.New("live read unavailable")
	r.APIReader = interceptor.NewClient(c, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
		return wantErr
	}})
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(s)})
	if !errors.Is(err, wantErr) || result != (ctrl.Result{}) {
		t.Fatalf("error returned with requeue result: result=%+v err=%v", result, err)
	}
}
