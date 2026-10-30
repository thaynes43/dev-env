package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

func terminalTaskFixture(recorded bool) terminalFixture {
	f := newTerminalFixture()
	f.s.Spec.Mode = v1alpha1.ModeTask
	f.s.Spec.Model = "fixture-model"
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	f.s.Status.Rescue = nil
	f.pod.Status = corev1.PodStatus{Phase: corev1.PodRunning, StartTime: &metav1.Time{Time: f.now.Add(-time.Hour)},
		ContainerStatuses: []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(f.now.Add(-time.Hour))}}}}}
	source := v1alpha1.UsageSourceStatus{SessionUID: string(f.s.UID), PodUID: string(f.pod.UID), Provider: string(f.s.Spec.Agent), RequestedModel: f.s.Spec.Model, AttemptID: strings.Repeat("a", 64)}
	task := &v1alpha1.TaskStatus{ExitSource: &source, ExitCode: 1, Subtype: "error_during_execution", IsError: true, NumTurns: 2, FinishedAt: recordTime(f.now.Add(-time.Minute))}
	f.s.Status.Agent = &v1alpha1.AgentStatus{Status: protocol.AgentExited, Task: task, ConversationID: f.s.Status.PrivateHistory.LastExecutor.ConversationID}
	f.s.Status.UsageObservation = &v1alpha1.UsageObservationStatus{Source: source, ObservedAt: task.FinishedAt}
	if recorded {
		f.s.Status.PrivateHistory.TaskTerminal = &v1alpha1.PrivateTaskTerminalStatus{Task: *task.DeepCopy(), ObservedAt: recordTime(f.now.Add(-time.Hour)), DueAt: recordTime(f.now.Add(-time.Minute))}
	}
	return f
}

func terminalTaskReconciler(f terminalFixture, c client.Client, reader client.Reader) *Reconciler {
	r := f.reconciler(c, reader, nil)
	r.PrivateExecutorStop, r.TerminalTaskPodReclaim = true, true
	r.TerminalTaskReclaimGrace = time.Minute
	return r
}

func TestTerminalTaskReclaimGraceStopProofOutcomeAndRestart(t *testing.T) {
	f := terminalTaskFixture(false)
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	base := f.client()
	deletes := 0
	c := interceptor.NewClient(base, interceptor.Funcs{Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
		pod, ok := o.(*corev1.Pod)
		if !ok {
			t.Fatalf("reclaim deleted non-Pod %T", o)
		}
		options := &client.DeleteOptions{}
		for _, opt := range opts {
			opt.ApplyToDelete(options)
		}
		if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != pod.UID || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != pod.ResourceVersion || options.GracePeriodSeconds != nil {
			t.Fatal("delete lost UID/RV or changed normal grace")
		}
		deletes++
		return c.Delete(ctx, o, opts...)
	}})
	now := f.now
	r := terminalTaskReconciler(f, c, base)
	r.Now = func() time.Time { return now }
	reconcileSession(t, r, f.s, 3)
	got := storedHistory(t, base, f.s)
	rec := got.Status.PrivateHistory.TaskTerminal.DeepCopy()
	if rec == nil || !rec.DueAt.Equal(&metav1.Time{Time: now.Add(time.Minute)}) || deletes != 0 || got.Status.PrivateHistory.StopRequest != nil {
		t.Fatalf("grace was skipped: %+v", got.Status.PrivateHistory)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, ConditionTerminalTaskReclaim); cond == nil || cond.Reason != "Grace" || !strings.Contains(cond.Message, "deadline=") || !strings.Contains(cond.Message, "owner=") {
		t.Fatalf("missing grace/owner status: %+v", cond)
	}
	now = now.Add(time.Minute)
	reconcileSession(t, r, f.s, 3)
	got = storedHistory(t, base, f.s)
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil {
		t.Fatal(err)
	}
	if got.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || got.Status.PrivateHistory.StopRequest == nil || pod.Spec.ActiveDeadlineSeconds == nil || deletes != 0 {
		t.Fatal("did not wait for kubelet terminal proof", got.Status.PrivateHistory)
	}
	pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "DeadlineExceeded"
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, FinishedAt: recordTime(now)}}}}
	if err := base.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	var lease = f.lease.DeepCopy()
	lease.Spec.RenewTime.Time = now.Add(-time.Second)
	if err := base.Update(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, f.s, 4)
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); !apierrors.IsNotFound(err) {
		t.Fatalf("terminal executor retained: %v", err)
	}
	got = storedHistory(t, base, f.s)
	if deletes != 1 || !executorTerminalRecorded(got.Status.PrivateHistory) || !apiequality.Semantic.DeepEqual(rec, got.Status.PrivateHistory.TaskTerminal) || got.Status.TaskResult == nil || got.Status.TaskResult.State != v1alpha1.TaskResultFailed {
		t.Fatal("outcome or once-only proof lost", got.Status)
	}
	var home corev1.PersistentVolumeClaim
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil || !apiequality.Semantic.DeepEqual(home.Spec, f.home.Spec) || !apiequality.Semantic.DeepEqual(home.Annotations, f.home.Annotations) || len(home.Finalizers) == 0 || got.Status.PrivateHistory.Expiry != nil || got.Status.Rescue != nil {
		t.Fatal("HOME/history retention or rescue boundary changed", err)
	}
	// Reconstruct the operator and reconcile again; no replacement, result
	// clearing, repeated delete, HOME release or grace restart is permitted.
	r = terminalTaskReconciler(f, c, base)
	r.Now = func() time.Time { return now.Add(time.Hour) }
	reconcileSession(t, r, f.s, 3)
	got = storedHistory(t, base, f.s)
	if deletes != 1 || !apiequality.Semantic.DeepEqual(rec, got.Status.PrivateHistory.TaskTerminal) || got.Status.TaskResult == nil || len(got.Finalizers) == 0 {
		t.Fatal("restart changed terminal result or retention")
	}
}

func TestTerminalTaskReclaimProtectionTable(t *testing.T) {
	cases := []struct {
		name   string
		change func(*terminalFixture, *Reconciler)
	}{
		{"default-off", func(f *terminalFixture, r *Reconciler) { r.TerminalTaskPodReclaim = false }},
		{"record-off", func(f *terminalFixture, r *Reconciler) { r.PrivateTerminalRecord = false }},
		{"stop-off", func(f *terminalFixture, r *Reconciler) { r.PrivateExecutorStop = false }},
		{"interactive", func(f *terminalFixture, r *Reconciler) { f.s.Spec.Mode = v1alpha1.ModeLocal }},
		{"remote", func(f *terminalFixture, r *Reconciler) { f.s.Spec.Mode = v1alpha1.ModeRemote }},
		{"reusable-host", func(f *terminalFixture, r *Reconciler) {
			f.s.Annotations = map[string]string{homeguard.Annotation: "retained-host"}
		}},
		{"configured-coordinator", func(f *terminalFixture, r *Reconciler) {
			r.TerminalTaskProtectedSessions = []string{f.s.Namespace + "/" + f.s.Name}
		}},
		{"coordinator-pod", func(f *terminalFixture, r *Reconciler) {
			f.pod.Annotations["dev-env.haynesops.com/coordinator-host"] = "host"
		}},
		{"busy", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Status = protocol.AgentBusy }},
		{"attached", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Attached = 1 }},
		{"no-result", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Task = nil }},
		{"legacy-result", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Task.ExitSource = nil }},
		{"no-attempt-receipt", func(f *terminalFixture, r *Reconciler) { f.s.Status.UsageObservation = nil }},
		{"other-attempt", func(f *terminalFixture, r *Reconciler) {
			f.s.Status.Agent.Task.ExitSource.AttemptID = strings.Repeat("b", 64)
		}},
		{"other-pod", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Task.ExitSource.PodUID = "replacement" }},
		{"other-session", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Task.ExitSource.SessionUID = "replacement" }},
		{"contradictory-result", func(f *terminalFixture, r *Reconciler) { f.s.Status.Agent.Task.ExitCode++ }},
		{"future-exit", func(f *terminalFixture, r *Reconciler) {
			f.s.Status.Agent.Task.FinishedAt = recordTime(f.now.Add(time.Hour))
		}},
		{"no-home-proof", func(f *terminalFixture, r *Reconciler) { f.s.Status.PrivateHistory.HomeUID = "" }},
		{"replaced-home", func(f *terminalFixture, r *Reconciler) { f.home.UID = "replacement" }},
		{"replaced-pod", func(f *terminalFixture, r *Reconciler) { f.pod.UID = "replacement" }},
		{"foreign-pod", func(f *terminalFixture, r *Reconciler) { f.pod.OwnerReferences = nil }},
		{"hold-pod", func(f *terminalFixture, r *Reconciler) { f.pod.Labels[v1alpha1.LabelHold] = "true" }},
		{"owner-hold", func(f *terminalFixture, r *Reconciler) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}},
		{"invalid-hold", func(f *terminalFixture, r *Reconciler) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: "holdd"}
		}},
		{"shared", func(f *terminalFixture, r *Reconciler) { f.s.Spec.Workspace = &v1alpha1.WorkspaceSpec{} }},
		{"canary", func(f *terminalFixture, r *Reconciler) { f.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} }},
		{"pilot", func(f *terminalFixture, r *Reconciler) { f.s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			f := terminalTaskFixture(true)
			r := terminalTaskReconciler(f, nil, nil)
			test.change(&f, r)
			base := f.client()
			r.Client, r.APIReader = base, base
			for range 2 {
				s := storedHistory(t, base, f.s)
				_, _, _ = r.reconcileTerminalTaskReclaim(context.Background(), s)
			}
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds != nil {
				t.Fatal("protected task compute changed", err)
			}
			if s := storedHistory(t, base, f.s); s.Status.PrivateHistory.StopRequest != nil {
				t.Fatal("protected task got a stop intent")
			}
		})
	}
}

func TestTerminalTaskReclaimProtectsLiveAndUnprovedChildren(t *testing.T) {
	for _, state := range []string{"live", "missing-unproved", "terminal"} {
		t.Run(state, func(t *testing.T) {
			f := terminalTaskFixture(true)
			child := f.s.DeepCopy()
			child.Name = "child"
			child.UID = "child-session"
			child.ResourceVersion = ""
			child.Spec.Parent = f.s.Name
			child.Status.PrivateHistory.LastExecutor.PodUID = "child-pod"
			if state == "terminal" {
				child.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{PodUID: "child-pod"}
			}
			base := f.client()
			if err := base.Create(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			if state == "live" {
				pod := f.pod.DeepCopy()
				pod.Name = child.Name
				pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = HomeClaimName(child.Name)
				pod.UID = "child-pod"
				pod.ResourceVersion = ""
				pod.OwnerReferences = []metav1.OwnerReference{ownerRef(child)}
				if err := base.Create(context.Background(), pod); err != nil {
					t.Fatal(err)
				}
			}
			r := terminalTaskReconciler(f, base, base)
			v, err := r.readExpiryView(context.Background(), f.s)
			if err != nil {
				t.Fatal(err)
			}
			reason, err := r.terminalTaskViewAllowed(context.Background(), v)
			if state == "terminal" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || reason != "ChildrenActive" {
				t.Fatalf("child allowed: %s %v", reason, err)
			}
		})
	}
}

func TestTerminalTaskReclaimReadAndCASFailuresKeepCompute(t *testing.T) {
	for _, failure := range []string{"read", "hold-before-confirmation", "pod-version"} {
		t.Run(failure, func(t *testing.T) {
			f := terminalTaskFixture(true)
			f.pod.Status.Phase = corev1.PodFailed
			f.pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
			f.s.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{PodUID: string(f.pod.UID)}
			base := f.client()
			once := false
			c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				if !once && failure == "hold-before-confirmation" {
					once = true
					s := storedHistory(t, base, f.s)
					s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
					if err := base.Update(ctx, s); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Update(ctx, o, opts...)
			}, Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if failure == "pod-version" {
					return apierrors.NewConflict(schema.GroupResource{Resource: "pods"}, o.GetName(), errors.New("changed"))
				}
				return c.Delete(ctx, o, opts...)
			}})
			var reader client.Reader = base
			if failure == "read" {
				reader = interceptor.NewClient(base, interceptor.Funcs{Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errors.New("read unavailable")
				}})
			}
			r := terminalTaskReconciler(f, c, reader)
			s := storedHistory(t, base, f.s)
			_, _, _ = r.reconcileTerminalTaskReclaim(context.Background(), s)
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil {
				t.Fatal("compute removed after failed proof", err)
			}
		})
	}
}
