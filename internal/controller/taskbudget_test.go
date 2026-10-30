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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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
	obs.templates.Env = append(obs.templates.Env, corev1.EnvVar{Name: protocol.TaskBudgetEnv, Value: "arbitrary profile override"})
	pod, err := buildManagedPod(f.s, obs.templates, "", true, false, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == protocol.TaskBudgetEnv && env.Value != "arbitrary profile override" {
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
	effective := "missing"
	for _, env := range hold.Spec.Containers[0].Env {
		if env.Name == protocol.TaskBudgetEnv {
			effective = env.Value
		}
	}
	if effective != "" {
		t.Fatal("rescue armed executor timer")
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

// interleavedLedgerClient wraps the fixture client. After each accepted ledger
// ConfigMap write, before the response returns, it runs between: another
// writer committing next, as a sibling reconcile or the other replica would.
func interleavedLedgerClient(c client.Client, update func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error) client.Client {
	return interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{Update: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
		if _, ok := obj.(*corev1.ConfigMap); !ok {
			return cl.Update(ctx, obj, opts...)
		}
		return update(ctx, cl, obj, opts...)
	}})
}

// Contention between real ledger writers is not unknown authority. A write the
// API server accepted stays accepted when another writer commits next, and a
// typed conflict retries; neither may irreversibly suspend an admitted child.
func TestManagedBudgetLedgerContentionDoesNotSuspend(t *testing.T) {
	setup := func(t *testing.T) (workspaceStopFixture, client.Client, *taskbudget.Service, taskbudget.Binding) {
		t.Helper()
		ctx := context.Background()
		f := stoppedWorkspaceFixture()
		c := f.client()
		svc, b := budgetService(t, c, f.now)
		f.s.Annotations = budgetAnnotations(b, "child")
		f.s.Spec.Parent = "system/parent"
		f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
		if err := c.Update(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		// Launch reserved but the Pod UID not yet bound: the next observation
		// of the Pod must write the ledger.
		if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "start-child", Kind: "worker-start", WorkerID: "child"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.BindManagedWorker(ctx, b, "child", taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID)}); err != nil {
			t.Fatal(err)
		}
		return f, c, svc, b
	}
	assertRunning := func(t *testing.T, c client.Client, f workspaceStopFixture, changed bool) {
		t.Helper()
		var live v1alpha1.AgentSession
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.s), &live); err != nil {
			t.Fatal(err)
		}
		if changed || f.s.Spec.OperatingMode != v1alpha1.OperatingModeRunning || live.Spec.OperatingMode != v1alpha1.OperatingModeRunning {
			t.Fatal("contention suspended worker", changed, live.Spec.OperatingMode)
		}
	}

	t.Run("interleaved-writer", func(t *testing.T) {
		ctx := context.Background()
		f, c, svc, b := setup(t)
		sibling := &taskbudget.Service{Store: svc.Store, Validator: svc.Validator, Now: func() time.Time { return f.now.Add(time.Millisecond) }}
		interleaved := 0
		svc.Store = taskbudget.KubeStore{Namespace: "protected-budgets", Live: c, Client: interleavedLedgerClient(c, func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			if err := cl.Update(ctx, obj, opts...); err != nil {
				return err
			}
			if interleaved == 0 {
				interleaved++
				if _, err := sibling.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "start-sibling", Kind: "worker-start", WorkerID: "sibling"}); err != nil {
					t.Error("sibling writer", err)
				}
			}
			return nil
		})}
		r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
		changed, err := r.enforceTaskBudget(ctx, f.s, f.pod)
		if err != nil || interleaved != 1 {
			t.Fatal("accepted ledger write became unknown authority", err, interleaved)
		}
		assertRunning(t, c, f, changed)
		l, err := sibling.Observe(ctx, b)
		if err != nil || len(l.Workers) != 2 || !l.Workers[0].Active || l.Workers[0].Managed.PodUID != string(f.pod.UID) {
			t.Fatalf("interleaved writes lost: %+v %v", l, err)
		}
	})

	t.Run("exhausted-conflicts", func(t *testing.T) {
		ctx := context.Background()
		f, c, svc, _ := setup(t)
		updates := 0
		svc.Store = taskbudget.KubeStore{Namespace: "protected-budgets", Live: c, Client: interleavedLedgerClient(c, func(context.Context, client.WithWatch, client.Object, ...client.UpdateOption) error {
			updates++
			return apierrors.NewConflict(corev1.Resource("configmaps"), "ledger", errors.New("another writer"))
		})}
		r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
		changed, err := r.enforceTaskBudget(ctx, f.s, f.pod)
		if !errors.Is(err, taskbudget.ErrConflict) || updates != 5 {
			t.Fatal("typed conflict not retried as contention", err, updates)
		}
		assertRunning(t, c, f, changed)
	})
}

func TestManagedBudgetUnknownFailureProofRequestsSuspendAndRetainsPod(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	c := f.client()
	svc, b := budgetService(t, c, f.now)
	f.s.Annotations = budgetAnnotations(b, "child")
	f.s.Spec.Parent = "system/parent"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	f.pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
	if err := c.Status().Update(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	bindBudgetWorker(t, svc, b, f, "child")
	f.lease.Spec.RenewTime.Time = f.now.Add(-time.Minute)
	if err := c.Update(ctx, f.lease); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	proofErr := r.recordTaskBudgetFailure(ctx, f.s, f.pod)
	if proofErr == nil {
		t.Fatal("stale proof accepted")
	}
	if changed, err := r.suspendTaskBudget(ctx, f.s, "failure proof unavailable"); err != nil || !changed {
		t.Fatal("unknown failure did not request suspend", err)
	}
	var suspended v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.s), &suspended); err != nil || suspended.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || condition(&suspended.Status, ConditionTaskBudget) == nil {
		t.Fatal("stop intent/uncertainty not durable", err)
	}
	var retained corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.pod), &retained); err != nil {
		t.Fatal("uncertain executor deleted", err)
	}
	l, err := svc.Observe(ctx, b)
	if err != nil || !l.Workers[0].Active || len(l.Events) != 1 {
		t.Fatal("unproved exit counted or uncharged", err)
	}
}

func TestManagedRecordedFailureSuspendsThroughReconcileBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	f.s.Spec.Parent = "system/parent"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	c := f.client()
	svc, b := budgetService(t, c, f.now)
	f.s.Annotations = budgetAnnotations(b, "child")
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	f.pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 1
	if err := c.Status().Update(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	bindBudgetWorker(t, svc, b, f, "child")
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
		t.Fatal(err)
	}
	var stopped v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.s), &stopped); err != nil || stopped.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("recorded failure not suspended", err)
	}
	l, err := svc.Observe(ctx, b)
	if err != nil || l.Latched || len(l.Events) != 2 || !l.Workers[0].Active {
		t.Fatal("failure/charge history invalid", err)
	}
	var retained corev1.Pod
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.pod), &retained); err != nil {
		t.Fatal("executor lost before rescue", err)
	}
}

// A child reserved at dispatch whose one Pod create was never reserved cannot
// be running: every managed create is fenced in the ledger first. Once its
// session is stopping and no Pod exists, the controller closes it, so it stops
// being charged, and the closed worker can never launch afterwards.
func TestManagedNeverLaunchedChildIsClosedAndStopsCharging(t *testing.T) {
	ctx := context.Background()
	f := stoppedWorkspaceFixture()
	f.s.Spec.Parent = "system/parent"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.PodName = ""
	c := f.client()
	if err := c.Delete(ctx, f.pod); err != nil {
		t.Fatal(err)
	}
	now := f.now
	svc, b := budgetService(t, c, f.now)
	svc.Now = func() time.Time { return now }
	f.s.Annotations = budgetAnnotations(b, "child")
	if err := c.Update(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch", Kind: "worker-start", WorkerID: "child"}); err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	if _, err := r.observeTaskBudget(ctx, f.s, nil); err != nil {
		t.Fatal(err)
	}
	// A running session may still launch: nothing proves non-launch yet.
	if err := r.recordTaskBudgetNeverLaunched(ctx, f.s); err != nil {
		t.Fatal(err)
	}
	l, err := svc.Observe(ctx, b)
	if err != nil || !l.Workers[0].Active || l.Workers[0].Managed == nil {
		t.Fatal("running child closed or never bound", err)
	}
	ref := *l.Workers[0].Managed
	if _, err := svc.Record(ctx, managedNeverLaunchedEvent(b, "child", ref)); !errors.Is(err, taskbudget.ErrDenied) {
		t.Fatal("never-launched receipt accepted for a running session", err)
	}
	// The first checkpoint latches before the controller reserved any launch.
	now = now.Add(11 * time.Minute)
	for range 3 {
		if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.s)}); err != nil {
			t.Fatal(err)
		}
	}
	var live v1alpha1.AgentSession
	if err := c.Get(ctx, client.ObjectKeyFromObject(f.s), &live); err != nil || live.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		t.Fatal("latched child not suspended", err)
	}
	l, err = svc.Observe(ctx, b)
	if err != nil || l.Workers[0].Active || l.Workers[0].StopEvidence != managedBudgetReference(ref) {
		t.Fatalf("never-launched child still charged: %+v %v", l.Workers[0], err)
	}
	effort := l.EffortMilliseconds
	now = now.Add(20 * time.Minute)
	if l, err = svc.Observe(ctx, b); err != nil || l.EffortMilliseconds != effort {
		t.Fatal("closed child still charged", err)
	}
	svc.ManagedInspector = managedLaunchFixture{}
	if _, err := svc.ReserveManagedLaunch(ctx, b, "child"); !errors.Is(err, taskbudget.ErrDenied) {
		t.Fatal("closed child could still launch", err)
	}
}

// Non-launch is proved only by the ledger fence plus an absent Pod. A reserved
// launch or a present Pod keeps the worker charged.
func TestManagedNeverLaunchedReceiptNeedsUnreservedLaunchAndAbsentPod(t *testing.T) {
	setup := func(t *testing.T) (workspaceStopFixture, client.Client, *taskbudget.Service, taskbudget.Binding, *Reconciler) {
		t.Helper()
		ctx := context.Background()
		f := stoppedWorkspaceFixture()
		f.s.Spec.Parent = "system/parent"
		f.s.Status.PodName = ""
		c := f.client()
		if err := c.Delete(ctx, f.pod); err != nil {
			t.Fatal(err)
		}
		svc, b := budgetService(t, c, f.now)
		f.s.Annotations = budgetAnnotations(b, "child")
		if err := c.Update(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Record(ctx, apiv1.TaskBudgetEvent{Binding: b, ID: "dispatch", Kind: "worker-start", WorkerID: "child"}); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.BindManagedWorker(ctx, b, "child", taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID)}); err != nil {
			t.Fatal(err)
		}
		return f, c, svc, b, &Reconciler{Client: c, APIReader: c, TaskBudgets: svc}
	}
	active := func(t *testing.T, svc *taskbudget.Service, b taskbudget.Binding) {
		t.Helper()
		l, err := svc.Observe(context.Background(), b)
		if err != nil || !l.Workers[0].Active {
			t.Fatal("unproved non-launch uncharged worker", err)
		}
	}
	t.Run("launch-reserved", func(t *testing.T) {
		ctx := context.Background()
		f, _, svc, b, r := setup(t)
		svc.ManagedInspector = managedLaunchFixture{}
		l, err := svc.ReserveManagedLaunch(ctx, b, "child")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.recordTaskBudgetNeverLaunched(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		ref := *l.Workers[0].Managed
		ref.LaunchRequested = false
		if _, err := svc.Record(ctx, managedNeverLaunchedEvent(b, "child", ref)); !errors.Is(err, taskbudget.ErrDenied) {
			t.Fatal("reserved launch closed as never launched", err)
		}
		active(t, svc, b)
	})
	t.Run("pod-present", func(t *testing.T) {
		ctx := context.Background()
		f, c, svc, b, _ := setup(t)
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: f.s.Name, Namespace: f.s.Namespace, UID: "unreserved"}}
		if err := c.Create(ctx, pod); err != nil {
			t.Fatal(err)
		}
		ref := taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID)}
		if _, err := svc.Record(ctx, managedNeverLaunchedEvent(b, "child", ref)); !errors.Is(err, taskbudget.ErrDenied) {
			t.Fatal("present Pod closed as never launched", err)
		}
		active(t, svc, b)
	})
	t.Run("pod-recorded", func(t *testing.T) {
		ctx := context.Background()
		f, c, svc, b, _ := setup(t)
		f.s.Status.PodName = f.s.Name
		if err := c.Status().Update(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		ref := taskbudget.ManagedWorkerRef{Namespace: f.s.Namespace, Name: f.s.Name, SessionUID: string(f.s.UID)}
		if _, err := svc.Record(ctx, managedNeverLaunchedEvent(b, "child", ref)); !errors.Is(err, taskbudget.ErrDenied) {
			t.Fatal("recorded Pod closed as never launched", err)
		}
		active(t, svc, b)
	})
	t.Run("suspended-and-absent", func(t *testing.T) {
		ctx := context.Background()
		f, _, svc, b, r := setup(t)
		if err := r.recordTaskBudgetNeverLaunched(ctx, f.s); err != nil {
			t.Fatal(err)
		}
		if err := r.recordTaskBudgetNeverLaunched(ctx, f.s); err != nil {
			t.Fatal("closed worker replay failed", err)
		}
		l, err := svc.Observe(ctx, b)
		if err != nil || l.Workers[0].Active || len(l.Events) != 2 {
			t.Fatal("proved non-launch not recorded once", err)
		}
	})
}
