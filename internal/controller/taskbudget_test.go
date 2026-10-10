package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

func budgetAnnotations(b taskbudget.Binding, id string) map[string]string {
	return map[string]string{taskbudget.UIDAnnotation: b.TaskUID, taskbudget.EpochAnnotation: strconv.FormatUint(b.Epoch, 10), taskbudget.DeadlineAnnotation: b.Deadline.Format(time.RFC3339Nano), taskbudget.HostAnnotation: b.HostID, taskbudget.PodAnnotation: b.PodUID, taskbudget.WorkerAnnotation: id}
}

func budgetService(t *testing.T, c client.Client, now time.Time) (*taskbudget.Service, taskbudget.Binding) {
	t.Helper()
	s := &taskbudget.Service{Store: taskbudget.KubeStore{Client: c, Live: c, Namespace: "protected-budgets"}, Validator: ManagedBudgetEvidenceValidator{Reader: c, Now: func() time.Time { return now }}, Now: func() time.Time { return now }}
	l, err := s.Create(context.Background(), "campaign", "host", "root-pod", "system/parent", taskbudget.Spec{SuccessCondition: "verified preservation", OverallSeconds: 2700, EffortSeconds: 3600, CheckpointSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	return s, l.Binding
}

func bindBudgetWorker(t *testing.T, s *taskbudget.Service, b taskbudget.Binding, f workspaceStopFixture, id string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "start-" + id, Kind: "worker-start", WorkerID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BindManagedWorker(ctx, b, id, taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID), PodUID: string(f.pod.UID)}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedBudgetIndependentFailuresShareServerCheckpointAcrossChildren(t *testing.T) {
	ctx := context.Background()
	base := stoppedWorkspaceFixture()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.AgentSession{}, &corev1.Pod{}).WithObjects(base.node, base.lease).Build()
	svc, b := budgetService(t, c, base.now)
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	for i := 1; i <= 3; i++ {
		f := stoppedWorkspaceFixture()
		id := fmt.Sprintf("child-%d", i)
		f.s.Name = id
		f.s.UID = types.UID(fmt.Sprintf("session-%d", i))
		f.s.Spec.Parent = "system/parent"
		f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
		f.s.Annotations = budgetAnnotations(b, id)
		f.pod.Name = id
		f.pod.UID = types.UID(fmt.Sprintf("pod-%d", i))
		f.pod.OwnerReferences = []metav1.OwnerReference{ownerRef(f.s)}
		f.pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
		f.s.ResourceVersion = ""
		f.pod.ResourceVersion = ""
		if err := c.Create(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		if err := c.Create(ctx, f.pod); err != nil {
			t.Fatal(err)
		}
		bindBudgetWorker(t, svc, b, f, id)
		// Status files and caller relabeling cannot certify a different checkpoint.
		forged := apiv1.TaskBudgetEvent{Binding: b, ID: "forged-" + id, Kind: "failure", WorkerID: id, AttemptID: "managed:" + string(f.s.UID), BlockerID: "new-technique", Evidence: apiv1.TaskBudgetEvidence{ID: "receipt-" + id, Kind: "managed-container-exit", Reference: managedBudgetReference(taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID), PodUID: string(f.pod.UID)})}}
		if _, err := svc.Record(ctx, forged); !errors.Is(err, taskbudget.ErrDenied) {
			t.Fatal("relabel accepted", err)
		}
		if err := r.recordTaskBudgetFailure(ctx, f.s, f.pod); err != nil {
			t.Fatal(err)
		}
		if err := r.recordTaskBudgetFailure(ctx, f.s, f.pod); err != nil {
			t.Fatal("stable attempt replay", err)
		}
	}
	l, err := svc.Observe(ctx, b)
	if err != nil || !l.Latched || l.Reason != "RepeatedFailure" || len(l.Events) != 6 {
		t.Fatalf("history/latch %+v %v", l, err)
	}
	var last v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKey{Namespace: base.s.Namespace, Name: "child-3"}, &last); err != nil {
		t.Fatal(err)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(&last), &pod); err != nil {
		t.Fatal(err)
	}
	if changed, err := r.enforceTaskBudget(ctx, &last, &pod); err != nil || !changed || last.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("latched child not suspended", err)
	}
}

func TestManagedBudgetStopNeedsWholeExecutorAndRecordedRescue(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	c := f.client()
	svc, b := budgetService(t, c, f.now)
	f.s.Spec.Parent = "system/parent"
	f.s.Annotations = budgetAnnotations(b, "child")
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	bindBudgetWorker(t, svc, b, f, "child")
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	if err := r.recordTaskBudgetStop(ctx, f.s, f.pod); err == nil {
		t.Fatal("stop request without rescue accepted")
	}
	f.s.Status.Rescue = &v1alpha1.RescueStatus{PodUID: "hold", SourcePodUID: string(f.pod.UID), Generation: f.s.Generation, Result: v1alpha1.RescueCleanAndPushed}
	if err := c.Status().Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	f.lease.Spec.RenewTime.Time = f.now.Add(-time.Minute)
	if err := c.Update(ctx, f.lease); err != nil {
		t.Fatal(err)
	}
	if err := r.recordTaskBudgetStop(ctx, f.s, f.pod); err == nil {
		t.Fatal("stale node proof accepted")
	}
	l, err := svc.Observe(ctx, b)
	if err != nil || !l.Workers[0].Active {
		t.Fatal("uncertain stop uncharged worker", err)
	}
	f.lease.Spec.RenewTime.Time = f.now.Add(-time.Second)
	if err := c.Update(ctx, f.lease); err != nil {
		t.Fatal(err)
	}
	if err := r.recordTaskBudgetStop(ctx, f.s, f.pod); err != nil {
		t.Fatal(err)
	}
	l, err = svc.Observe(ctx, b)
	if err != nil || l.Workers[0].Active {
		t.Fatal("verified stop not recorded", err)
	}
	if err := c.Delete(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	if _, err := r.observeTaskBudget(ctx, f.s, nil); err == nil {
		t.Fatal("missing Pod treated as fresh executor")
	}
}

func TestManagedBudgetPodEnvironmentIsFixedAndHoldHasNoTimer(t *testing.T) {
	f := stoppedWorkspaceFixture()
	c := f.client()
	_, b := budgetService(t, c, f.now)
	f.s.Annotations = budgetAnnotations(b, "child")
	_, obs := workspaceFixtureController(t, f, c)
	pod, err := buildManagedPod(f.s, obs.templates, "", true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == protocol.TaskBudgetEnv {
			deadline, err := protocol.ParseTaskBudgetDeadline(env.Value, string(f.s.UID), "created-pod", f.now)
			if err != nil || !deadline.Deadline.Equal(b.Deadline) || deadline.RootPodUID != b.PodUID {
				t.Fatal("binding changed", err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("budget environment missing")
	}
	hold, err := buildHoldPod(f.s, obs.templates)
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range hold.Spec.Containers[0].Env {
		if env.Name == protocol.TaskBudgetEnv && env.Value != "" {
			t.Fatal("rescue armed executor timer")
		}
	}
}

type managedLaunchFixture struct{}

func (managedLaunchFixture) InspectManagedAdmission(context.Context, *taskbudget.Ledger, taskbudget.ManagedWorkerRef) error {
	return nil
}

func TestManagedDelayedLaunchUsesFirstCheckpointAndCannotReplay(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	f.s.Spec.Parent = "system/parent"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.PodName = ""
	c := f.client()
	svc, b := budgetService(t, c, f.now)
	svc.ManagedInspector = managedLaunchFixture{}
	f.s.Annotations = budgetAnnotations(b, "child")
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch", Kind: "worker-start", WorkerID: "child"}); err != nil {
		t.Fatal(err)
	}
	r, obs := workspaceFixtureController(t, f, c)
	r.TaskBudgets = svc
	r.TaskBudgetWorkerImage = obs.templates.Image
	obs.pod = owned[*corev1.Pod]{missing: true}
	if err := r.ensure(ctx, f.s, obs.templates, &obs); err != nil || obs.removalBlocked != nil {
		t.Fatal("bounded launch refused", err, obs.removalBlocked)
	}
	var pod corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.s), &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds != 600 || *pod.Spec.TerminationGracePeriodSeconds != 5 || pod.Annotations["k8tz.io/inject"] != "false" {
		t.Fatalf("finite Pod backstop missing: %+v", pod.Spec)
	}
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == protocol.TaskBudgetEnv {
			deadline, err := protocol.ParseTaskBudgetDeadline(env.Value, string(f.s.UID), "created-pod", f.now)
			if err != nil || !deadline.Deadline.Equal(f.now.Add(10*time.Minute)) {
				t.Fatal("first checkpoint not enforced locally", err)
			}
		}
	}
	// Even an uncertain absent read cannot replay the already reserved create.
	obs.pod = owned[*corev1.Pod]{missing: true}
	obs.removalBlocked = nil
	if err := r.ensure(ctx, f.s, obs.templates, &obs); err != nil || obs.removalBlocked == nil {
		t.Fatal("launch replay not refused", err)
	}
}
