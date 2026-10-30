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
		task.FinishedAt = recordTime(f.now.Add(-2 * time.Hour))
		f.s.Status.UsageObservation.ObservedAt = task.FinishedAt
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
	if ready := meta.FindStatusCondition(got.Status.Conditions, ConditionPodReady); ready == nil || ready.Reason != "TaskPodReclaimed" {
		t.Fatalf("compute removal still reports unproved stop: %+v", ready)
	}
	if result := taskResult(got, nil); result == nil || result.State != v1alpha1.TaskResultFailed {
		t.Fatal("missing heartbeat cleared the preserved task outcome")
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
		{"no-start-time", func(f *terminalFixture, r *Reconciler) { f.pod.Status.StartTime = nil }},
		{"terminal-container-contradiction", func(f *terminalFixture, r *Reconciler) { f.pod.Status.Phase = corev1.PodFailed }},
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
	for _, failure := range []string{"read", "hold-before-confirmation", "resume-before-confirmation", "pod-version"} {
		t.Run(failure, func(t *testing.T) {
			f := terminalTaskFixture(true)
			f.pod.Status.Phase = corev1.PodFailed
			f.pod.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: ContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0, FinishedAt: recordTime(f.now.Add(-time.Minute))}}}}
			f.s.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{PodUID: string(f.pod.UID)}
			base := f.client()
			once := false
			deleteAttempted := false
			c := interceptor.NewClient(base, interceptor.Funcs{SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, o client.Object, opts ...client.SubResourceUpdateOption) error {
				if !once && (failure == "hold-before-confirmation" || failure == "resume-before-confirmation") {
					once = true
					s := storedHistory(t, base, f.s)
					s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
					if failure == "resume-before-confirmation" {
						s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
						s.Annotations = map[string]string{v1alpha1.AnnotationResumedAt: f.now.UTC().Format(time.RFC3339)}
					}
					if err := base.Update(ctx, s); err != nil {
						return err
					}
				}
				return c.SubResource(sub).Update(ctx, o, opts...)
			}, Delete: func(ctx context.Context, c client.WithWatch, o client.Object, opts ...client.DeleteOption) error {
				if failure == "pod-version" {
					deleteAttempted = true
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
			result, _, _ := r.reconcileTerminalTaskReclaim(context.Background(), s)
			if failure == "read" && result.RequeueAfter < homeExpiryUnknownRetry {
				t.Fatal("transient read failure polls too quickly")
			}
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil {
				t.Fatal("compute removed after failed proof", err)
			}
			if (failure == "hold-before-confirmation" || failure == "resume-before-confirmation") && !once || failure == "pod-version" && !deleteAttempted {
				t.Fatal("test did not reach its intended confirmation/delete boundary")
			}
		})
	}
}

func TestTerminalTaskReclaimProtectsDescendantsAndCycles(t *testing.T) {
	for _, state := range []string{"live-grandchild", "unproved-grandchild", "ownership-cycle"} {
		t.Run(state, func(t *testing.T) {
			f := terminalTaskFixture(true)
			if state == "ownership-cycle" {
				f.s.Spec.Parent = "child"
			}
			base := f.client()
			child := f.s.DeepCopy()
			child.Name, child.UID, child.ResourceVersion = "child", "child-session", ""
			child.Spec.Parent = f.s.Name
			child.Status.PrivateHistory.TaskTerminal = nil
			child.Status.PrivateHistory.SessionUID = string(child.UID)
			child.Status.PrivateHistory.HomeClaim = HomeClaimName(child.Name)
			child.Status.PrivateHistory.LastExecutor.PodName = child.Name
			child.Status.PrivateHistory.LastExecutor.PodUID = "child-pod"
			child.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{PodUID: "child-pod"}
			if err := base.Create(context.Background(), child); err != nil {
				t.Fatal(err)
			}
			if state != "ownership-cycle" {
				grandchild := child.DeepCopy()
				grandchild.Name, grandchild.UID, grandchild.ResourceVersion = "grandchild", "grandchild-session", ""
				grandchild.Spec.Parent = child.Name
				grandchild.Status.PrivateHistory.ExecutorTerminal = nil
				if err := base.Create(context.Background(), grandchild); err != nil {
					t.Fatal(err)
				}
				if state == "live-grandchild" {
					pod := f.pod.DeepCopy()
					pod.Name, pod.UID, pod.ResourceVersion = grandchild.Name, "grandchild-pod", ""
					pod.OwnerReferences = []metav1.OwnerReference{ownerRef(grandchild)}
					pod.Spec.Volumes[0].PersistentVolumeClaim.ClaimName = HomeClaimName(grandchild.Name)
					if err := base.Create(context.Background(), pod); err != nil {
						t.Fatal(err)
					}
				}
			}
			r := terminalTaskReconciler(f, base, base)
			if err := r.terminalTaskChildrenStopped(context.Background(), f.s); err == nil {
				t.Fatal("active or uncertain descendant tree accepted")
			}
		})
	}
}

func TestTerminalTaskReclaimSkipsOrdinaryActiveTaskAndKeepsMissingStopUnknown(t *testing.T) {
	f := terminalTaskFixture(false)
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Status.Agent.Status, f.s.Status.Agent.Task = protocol.AgentBusy, nil
	c := f.client()
	r := terminalTaskReconciler(f, c, c)
	_, stop, err := r.reconcileTerminalTaskReclaim(context.Background(), f.s)
	if err != nil || stop || meta.FindStatusCondition(f.s.Status.Conditions, ConditionTerminalTaskReclaim) != nil {
		t.Fatal("ordinary active compute reported as retained terminal compute")
	}
	f = terminalTaskFixture(true)
	c = f.client(f.pod)
	r = terminalTaskReconciler(f, c, c)
	v, err := r.readExpiryView(context.Background(), f.s)
	if err != nil {
		t.Fatal(err)
	}
	if reason, err := r.terminalTaskViewAllowed(context.Background(), v); err == nil || reason != "ProofUnknown" {
		t.Fatal("absence manufactured stop proof", reason, err)
	}
}

func TestTerminalTaskReclaimCachedGatesAvoidUncachedReadsAndPolling(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*terminalFixture)
		wait   time.Duration
	}{
		{"settled", func(f *terminalFixture) {
			meta.SetStatusCondition(&f.s.Status.Conditions, metav1.Condition{Type: ConditionTerminalTaskReclaim, Status: metav1.ConditionFalse, Reason: "Reclaimed"})
		}, 0},
		{"interactive", func(f *terminalFixture) { f.s.Spec.Mode = v1alpha1.ModeLocal }, 0},
		{"shared", func(f *terminalFixture) { f.s.Spec.Workspace = &v1alpha1.WorkspaceSpec{} }, 0},
		{"held", func(f *terminalFixture) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}, 0},
		{"legacy-result", func(f *terminalFixture) { f.s.Status.Agent.Task.ExitSource = nil }, 0},
		{"busy", func(f *terminalFixture) { f.s.Status.Agent.Status = protocol.AgentBusy }, 0},
		{"attached", func(f *terminalFixture) { f.s.Status.Agent.Attached = 1 }, 0},
		{"reusable", func(f *terminalFixture) { f.s.Annotations = map[string]string{homeguard.Annotation: "retained-host"} }, 0},
		{"grace", func(f *terminalFixture) {
			f.s.Status.PrivateHistory.TaskTerminal.DueAt = recordTime(f.now.Add(time.Hour))
		}, time.Hour},
		{"first-observation", func(f *terminalFixture) { f.s.Status.PrivateHistory.TaskTerminal = nil }, terminalTaskReclaimRetry},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := terminalTaskFixture(true)
			test.change(&f)
			base := f.client()
			reads := 0
			reader := interceptor.NewClient(base, interceptor.Funcs{
				Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					reads++
					return c.Get(ctx, key, obj, opts...)
				},
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					reads++
					return c.List(ctx, list, opts...)
				},
			})
			r := terminalTaskReconciler(f, base, reader)
			s := storedHistory(t, base, f.s)
			result, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s)
			if err != nil || reads != 0 || result.RequeueAfter != test.wait {
				t.Fatalf("uncached reads=%d result=%+v want wait=%s error=%v", reads, result, test.wait, err)
			}
		})
	}
}

func TestTerminalTaskReclaimAbsentExecutorSettlesWithoutPolling(t *testing.T) {
	f := terminalTaskFixture(true)
	f.s.Status.PrivateHistory.ExecutorTerminal = &v1alpha1.PrivateExecutorTerminalStatus{PodUID: string(f.pod.UID)}
	base := f.client(f.pod)
	reads := 0
	reader := interceptor.NewClient(base, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			reads++
			return c.Get(ctx, key, obj, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			reads++
			return c.List(ctx, list, opts...)
		},
	})
	r := terminalTaskReconciler(f, base, reader)
	s := storedHistory(t, base, f.s)
	for i := range 2 {
		result, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s)
		if err != nil || result.RequeueAfter != 0 || reads != 4 {
			t.Fatalf("pass %d reads=%d result=%+v error=%v", i, reads, result, err)
		}
		if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); cond == nil || cond.Reason != "Reclaimed" {
			t.Fatalf("absence not settled: %+v", cond)
		}
	}
}

func TestTerminalTaskReclaimExplicitResumeRestartsGraceAndSetsSuspender(t *testing.T) {
	f := terminalTaskFixture(true)
	f.s.Spec.OperatingMode = v1alpha1.OperatingModeRunning
	f.s.Annotations = map[string]string{v1alpha1.AnnotationResumedAt: f.now.UTC().Format(time.RFC3339)}
	base := f.client()
	r := terminalTaskReconciler(f, base, base)
	s := storedHistory(t, base, f.s)
	result, stop, err := r.reconcileTerminalTaskReclaim(context.Background(), s)
	if err != nil || stop || result.RequeueAfter != time.Minute || storedHistory(t, base, f.s).Spec.OperatingMode != v1alpha1.OperatingModeRunning {
		t.Fatalf("explicit resume immediately suspended: %+v %t %v", result, stop, err)
	}
	s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if r.terminalTaskStopDue(s) {
		t.Fatal("resume grace did not protect stop intent")
	}
	r.Now = func() time.Time { return f.now.Add(time.Minute) }
	s = storedHistory(t, base, f.s)
	_, stop, err = r.reconcileTerminalTaskReclaim(context.Background(), s)
	got := storedHistory(t, base, f.s)
	if err != nil || !stop || got.Spec.OperatingMode != v1alpha1.OperatingModeSuspended || got.Annotations[v1alpha1.AnnotationSuspendedBy] != TerminalTaskReclaimSuspender {
		t.Fatalf("due reclaim lacks suspend convention: %+v %v", got, err)
	}
	if !apiequality.Semantic.DeepEqual(got.Status.PrivateHistory.TaskTerminal, f.s.Status.PrivateHistory.TaskTerminal) {
		t.Fatal("resume changed immutable observation")
	}
}

func TestTerminalTaskRetentionLatchInAgentSessionDocument(t *testing.T) {
	for _, test := range []struct {
		name     string
		change   func(*v1alpha1.AgentSession)
		retained bool
	}{
		{"private-retained-task", func(*v1alpha1.AgentSession) {}, true},
		{"ordinary-task", func(s *v1alpha1.AgentSession) { s.Status.PrivateHistory = nil }, false},
		{"local", func(s *v1alpha1.AgentSession) {
			s.Spec.Mode = v1alpha1.ModeLocal
			s.Spec.Prompt = ""
			s.Spec.Limits = nil
		}, false},
		{"shared", func(s *v1alpha1.AgentSession) { s.Spec.Workspace = &v1alpha1.WorkspaceSpec{ID: "shared"} }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _ := privateHistoryFixture()
			test.change(s)
			doc, err := sessionDocument(s)
			if err != nil {
				t.Fatal(err)
			}
			got, err := protocol.ParseSession([]byte(doc))
			if err != nil || got.PrivateHistoryRetained != test.retained {
				t.Fatalf("retention latch=%t want %t error=%v", got.PrivateHistoryRetained, test.retained, err)
			}
			if !test.retained && strings.Contains(doc, "privateHistoryRetained") {
				t.Fatal("default session document changed")
			}
		})
	}
}

func TestTerminalTaskReclaimFullReconcileRefusalKeepsBackoffAndCondition(t *testing.T) {
	f := terminalTaskFixture(true)
	base := f.client()
	r := terminalTaskReconciler(f, base, base)
	r.TerminalTaskPodReclaim = false
	// Settle the pre-existing observation fields, so only the new reclaim
	// condition changes in the regression reconcile.
	reconcileSession(t, r, f.s, 3)
	r.TerminalTaskPodReclaim = true
	podLists := 0
	r.APIReader = interceptor.NewClient(base, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*corev1.PodList); ok {
				podLists++
				return errors.New("uncached pod list unavailable")
			}
			return c.List(ctx, list, opts...)
		},
	})
	result := reconcileSession(t, r, f.s, 1)
	got := storedHistory(t, base, f.s)
	if result.RequeueAfter < homeExpiryUnknownRetry || podLists != 1 || got.Status.PrivateHistory.StopRequest != nil {
		t.Fatalf("normal suspend repeated reclaim: result=%+v lists=%d stop=%+v", result, podLists, got.Status.PrivateHistory.StopRequest)
	}
	if cond := meta.FindStatusCondition(got.Status.Conditions, ConditionTerminalTaskReclaim); cond == nil || cond.Reason != "ProofUnknown" {
		t.Fatalf("reclaim condition was not persisted: %+v", cond)
	}
}
