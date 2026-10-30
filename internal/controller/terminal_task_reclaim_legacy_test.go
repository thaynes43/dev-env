package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
	"github.com/thaynes43/dev-env/internal/projectcanary"
)

func legacyTerminalTaskFixture(recorded bool) terminalFixture {
	f := terminalTaskFixture(recorded)
	f.s.Status.Agent.Task.ExitSource = nil
	f.s.Status.Agent.Agentd = "agentd v2.45.0 (commit abcdef, go1.27.0, linux/amd64)"
	f.s.Status.Agent.LastHeartbeat = &metav1.Time{Time: f.now.Add(-time.Second)}
	f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:2.45.0@sha256:" + strings.Repeat("a", 64)
	f.pod.Spec.Containers = append(f.pod.Spec.Containers, corev1.Container{Name: "sidecar", Image: "synthetic.invalid/sidecar:1"})
	f.pod.Spec.InitContainers = []corev1.Container{{Name: "init", Image: "synthetic.invalid/init:1"}}
	f.pod.Status.ContainerStatuses = append(f.pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: "sidecar"})
	f.pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init"}}
	return f
}

func assertLegacyHOMEUnchanged(t *testing.T, c client.Reader, before *corev1.PersistentVolumeClaim) {
	t.Helper()
	var after corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(before), &after); err != nil || !apiequality.Semantic.DeepEqual(before, &after) {
		t.Fatalf("legacy reclaim changed HOME: %v", err)
	}
}

func storedLegacyHOME(t *testing.T, c client.Reader, f terminalFixture) *corev1.PersistentVolumeClaim {
	t.Helper()
	var home corev1.PersistentVolumeClaim
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(f.home), &home); err != nil {
		t.Fatal(err)
	}
	return &home
}

// legacyRefusalMessages names the check each ProofUnknown row must hit.
var legacyRefusalMessages = func() map[string]string {
	m := map[string]string{}
	for msg, rows := range map[string][]string{
		"original provider exit and task result are unproved, or work is active": {
			"agentd-2.71", "agentd-newer", "agentd-development-2.71", "agentd-unparseable", "agentd-wrong-format",
			"missing-task", "not-exited", "attached",
		},
		"legacy executor image is not provably older than 2.71.0":         {"image-2.71", "image-newer", "image-unparseable", "image-digest-only"},
		"legacy provider exit lacks an unconflicted pre-2.71 usage proof": {"spawn-failure", "missing-usage", "usage-conflict"},
		"provider exit does not bind this Session, executor and attempt": {
			"future-finish", "missing-finish", "source-session", "source-last-executor", "source-provider", "source-model", "empty-attempt",
		},
		"legacy usage proof does not bind the live executor":                 {"source-live-pod"},
		"task result and retained original attempt contradict or lack proof": {"observed-finish-mismatch"},
		"legacy executor heartbeat is missing, future or older than two minutes": {
			"stale-heartbeat", "missing-heartbeat", "future-heartbeat",
		},
		"legacy executor has a restarted container":              {"main-restart", "sidecar-restart", "init-restart", "terminal-restart"},
		"legacy executor container restart count is unavailable": {"unknown-restarts"},
	} {
		for _, row := range rows {
			m[row] = msg
		}
	}
	return m
}()

// terminateLegacyPod marks the fixture executor as ended by the kubelet.
func terminateLegacyPod(pod *corev1.Pod, at time.Time) {
	pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "DeadlineExceeded"
	for i := range pod.Status.ContainerStatuses {
		pod.Status.ContainerStatuses[i].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, FinishedAt: metav1.NewTime(at)}}
	}
	for i := range pod.Status.InitContainerStatuses {
		pod.Status.InitContainerStatuses[i].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(at.Add(-time.Hour))}}
	}
}

func TestLegacyTerminalTaskReclaimProofTable(t *testing.T) {
	cases := []struct {
		name   string
		change func(*terminalFixture, *Reconciler)
		reason string
	}{
		{"accept", func(*terminalFixture, *Reconciler) {}, ""},
		{"accept-two-minute-heartbeat", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(-2 * time.Minute)
		}, ""},
		{"accept-2.63", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.Agent.Agentd = "agentd v2.63.0 (commit abcdef)"
			f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:2.63.0"
		}, ""},
		{"flag-off", func(_ *terminalFixture, r *Reconciler) { r.LegacyTerminalTaskReclaim = false }, "ProofUnknown"},
		{"agentd-2.71", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Agentd = "agentd v2.71.0 (commit abcdef)" }, "ProofUnknown"},
		{"agentd-newer", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Agentd = "agentd v3.0.0 (commit abcdef)" }, "ProofUnknown"},
		{"agentd-development-2.71", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.Agent.Agentd = "agentd v2.71.0-1-gabcdef (commit abcdef)"
		}, "ProofUnknown"},
		{"image-2.71", func(f *terminalFixture, _ *Reconciler) {
			f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:2.71.0"
		}, "ProofUnknown"},
		{"image-newer", func(f *terminalFixture, _ *Reconciler) {
			f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:2.72.0"
		}, "ProofUnknown"},
		{"agentd-unparseable", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Agentd = "agentd dev (commit unknown)" }, "ProofUnknown"},
		{"agentd-wrong-format", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Agentd = "v2.45.0" }, "ProofUnknown"},
		{"image-unparseable", func(f *terminalFixture, _ *Reconciler) {
			f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:original"
		}, "ProofUnknown"},
		{"image-digest-only", func(f *terminalFixture, _ *Reconciler) {
			f.pod.Spec.Containers[0].Image = "synthetic.invalid:443/agent@sha256:" + strings.Repeat("a", 64)
		}, "ProofUnknown"},
		{"spawn-failure", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Task.ExitCode = 127 }, "ProofUnknown"},
		{"future-finish", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.Agent.Task.FinishedAt = metav1.NewTime(f.now.Add(time.Second))
		}, "ProofUnknown"},
		{"missing-finish", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Task.FinishedAt = metav1.Time{} }, "ProofUnknown"},
		{"missing-task", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Task = nil }, "ProofUnknown"},
		{"not-exited", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Status = protocol.AgentIdle }, "ProofUnknown"},
		{"attached", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.Attached = 1 }, "ProofUnknown"},
		{"source-session", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.UsageObservation.Source.SessionUID = "other-session"
		}, "ProofUnknown"},
		{"source-last-executor", func(f *terminalFixture, _ *Reconciler) { f.s.Status.PrivateHistory.LastExecutor.PodUID = "other-pod" }, "ProofUnknown"},
		{"source-live-pod", func(f *terminalFixture, _ *Reconciler) { f.pod.UID = "other-pod" }, "ProofUnknown"},
		{"source-provider", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.UsageObservation.Source.Provider = "other-provider"
		}, "ProofUnknown"},
		{"source-model", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.UsageObservation.Source.RequestedModel = "other-model"
		}, "ProofUnknown"},
		{"empty-attempt", func(f *terminalFixture, _ *Reconciler) { f.s.Status.UsageObservation.Source.AttemptID = "" }, "ProofUnknown"},
		{"missing-usage", func(f *terminalFixture, _ *Reconciler) { f.s.Status.UsageObservation = nil }, "ProofUnknown"},
		{"observed-finish-mismatch", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.UsageObservation.ObservedAt.Time = f.s.Status.UsageObservation.ObservedAt.Add(-time.Second)
		}, "ProofUnknown"},
		{"usage-conflict", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.UsageConflict = &v1alpha1.UsageConflictStatus{Reason: "ConflictingSnapshot"}
		}, "ProofUnknown"},
		{"stale-heartbeat", func(f *terminalFixture, _ *Reconciler) {
			f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(-2*time.Minute - time.Second)
		}, "ProofUnknown"},
		{"missing-heartbeat", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.LastHeartbeat = nil }, "ProofUnknown"},
		{"future-heartbeat", func(f *terminalFixture, _ *Reconciler) { f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(time.Second) }, "ProofUnknown"},
		{"main-restart", func(f *terminalFixture, _ *Reconciler) { f.pod.Status.ContainerStatuses[0].RestartCount = 1 }, "ProofUnknown"},
		{"sidecar-restart", func(f *terminalFixture, _ *Reconciler) { f.pod.Status.ContainerStatuses[1].RestartCount = 1 }, "ProofUnknown"},
		{"init-restart", func(f *terminalFixture, _ *Reconciler) { f.pod.Status.InitContainerStatuses[0].RestartCount = 1 }, "ProofUnknown"},
		{"unknown-restarts", func(f *terminalFixture, _ *Reconciler) { f.pod.Status.InitContainerStatuses = nil }, "ProofUnknown"},
		// A terminal executor runs no work, so its heartbeat may be old; restarts still refuse.
		{"accept-terminal-stale-heartbeat", func(f *terminalFixture, _ *Reconciler) {
			terminateLegacyPod(f.pod, f.now.Add(-10*time.Minute))
			f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(-10 * time.Minute)
		}, ""},
		{"terminal-restart", func(f *terminalFixture, _ *Reconciler) {
			terminateLegacyPod(f.pod, f.now.Add(-10*time.Minute))
			f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(-10 * time.Minute)
			f.pod.Status.ContainerStatuses[0].RestartCount = 1
		}, "ProofUnknown"},
		{"reaping", func(f *terminalFixture, _ *Reconciler) {
			f.s.DeletionTimestamp = &metav1.Time{Time: f.now}
			meta.SetStatusCondition(&f.s.Status.Conditions, metav1.Condition{Type: ConditionTerminalTaskReclaim, Status: metav1.ConditionTrue, Reason: "Grace", Message: "fixture"})
		}, "Protected"},
		{"local", func(f *terminalFixture, _ *Reconciler) { f.s.Spec.Mode = v1alpha1.ModeLocal }, ""},
		{"remote", func(f *terminalFixture, _ *Reconciler) { f.s.Spec.Mode = v1alpha1.ModeRemote }, ""},
		{"original-home", func(f *terminalFixture, _ *Reconciler) {
			f.s.Annotations = map[string]string{homeguard.Annotation: "original"}
		}, "Protected"},
		{"workspace", func(f *terminalFixture, _ *Reconciler) { f.s.Spec.Workspace = &v1alpha1.WorkspaceSpec{} }, ""},
		{"canary", func(f *terminalFixture, _ *Reconciler) { f.s.Spec.ProjectCanary = &v1alpha1.ProjectCanaryBinding{} }, "Protected"},
		{"pilot", func(f *terminalFixture, _ *Reconciler) { f.s.Spec.PrivatePilot = &v1alpha1.PrivatePilotBinding{} }, "Protected"},
		{"pilot-annotation", func(f *terminalFixture, _ *Reconciler) {
			f.s.Annotations = map[string]string{projectcanary.PilotAnnotation: "fixture"}
		}, "Protected"},
		{"owner-hold", func(f *terminalFixture, _ *Reconciler) {
			f.s.Annotations = map[string]string{v1alpha1.AnnotationHomeExpiry: v1alpha1.HomeExpiryHold}
		}, "Held"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := legacyTerminalTaskFixture(false)
			r := terminalTaskReconciler(f, nil, nil)
			r.LegacyTerminalTaskReclaim = true
			tc.change(&f, r)
			base := f.client()
			home := storedLegacyHOME(t, base, f)
			r.Client, r.APIReader = base, base
			s := storedHistory(t, base, f.s)
			_, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			accept := strings.HasPrefix(tc.name, "accept")
			if accept {
				rec := s.Status.PrivateHistory.TaskTerminal
				if rec == nil || rec.Task.ExitSource == nil || *rec.Task.ExitSource != s.Status.UsageObservation.Source || s.Status.Agent.Task.ExitSource != nil {
					t.Fatal("legacy snapshot lost binding or mutated agent result")
				}
				if _, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s); err != nil {
					t.Fatal(err)
				}
				if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); cond == nil || cond.Reason != "Grace" || !strings.Contains(cond.Message, "proof=legacy-usage") {
					t.Fatalf("legacy condition missing: %+v", cond)
				}
			} else {
				if s.Status.PrivateHistory.TaskTerminal != nil {
					t.Fatal("refused proof produced a terminal snapshot")
				}
				cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim)
				if tc.reason == "" && cond != nil || tc.reason != "" && (cond == nil || cond.Reason != tc.reason) {
					t.Fatalf("condition: %+v, want %q", cond, tc.reason)
				}
				// ProofUnknown is shared by many checks; the message pins the one that refused.
				if want, ok := legacyRefusalMessages[tc.name]; ok && !strings.Contains(cond.Message, want) {
					t.Fatalf("refused by the wrong check: %q, want %q", cond.Message, want)
				}
				if tc.name == "flag-off" {
					want := "owner=" + s.Name + "; deadline=unknown; original provider exit and task result are unproved, or work is active; original HOME, history and outcome retained"
					if cond.Message != want {
						t.Fatalf("flag-off condition changed: %q", cond.Message)
					}
				}
			}
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds != nil || s.Status.PrivateHistory.StopRequest != nil {
				t.Fatal("proof/grace pass stopped or removed compute", err)
			}
			assertLegacyHOMEUnchanged(t, base, home)
		})
	}
}

func TestLegacyTerminalTaskFlagLeavesModernProofUnchanged(t *testing.T) {
	f := terminalTaskFixture(true)
	f.s.Status.Agent.Agentd = "agentd v2.71.0 (commit abcdef)"
	f.pod.Spec.Containers[0].Image = "synthetic.invalid/agent:2.71.0"
	f.pod.Status.ContainerStatuses[0].RestartCount = 1
	// The pre-existing modern proof does not depend on the legacy heartbeat,
	// no-restart or usage-conflict rules. The new flag must not change that.
	f.s.Status.Agent.LastHeartbeat = nil
	f.s.Status.UsageConflict = &v1alpha1.UsageConflictStatus{Reason: "ConflictingSnapshot"}
	base := f.client()
	home := storedLegacyHOME(t, base, f)
	r := terminalTaskReconciler(f, base, base)
	for _, enabled := range []bool{false, true} {
		r.LegacyTerminalTaskReclaim = enabled
		v, err := r.readExpiryView(context.Background(), f.s)
		if err != nil {
			t.Fatal(err)
		}
		if reason, err := r.terminalTaskViewAllowed(context.Background(), v); err != nil || reason != "" {
			t.Fatalf("legacy=%v changed modern proof: %s, %v", enabled, reason, err)
		}
	}
	// Losing a modern source cannot be relabelled as successful legacy proof,
	// even when a previous modern terminal snapshot is still retained.
	wantMessage := ""
	for _, enabled := range []bool{false, true} {
		r.LegacyTerminalTaskReclaim = enabled
		s := f.s.DeepCopy()
		s.Status.Agent.Task.ExitSource = nil
		if _, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s); err != nil {
			t.Fatal(err)
		}
		cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim)
		if cond == nil || cond.Reason != "ProofUnknown" || strings.Contains(cond.Message, "proof=legacy-usage") {
			t.Fatalf("contradictory modern task labelled legacy: %+v", cond)
		}
		if !enabled {
			wantMessage = cond.Message
		} else if cond.Message != wantMessage {
			t.Fatalf("flag changed modern refusal: %q != %q", cond.Message, wantMessage)
		}
	}
	assertLegacyHOMEUnchanged(t, base, home)
}

func TestLegacyTerminalTaskReclaimUncachedRefusals(t *testing.T) {
	for _, change := range []string{"heartbeat", "restart-main", "restart-sidecar", "restart-init", "usage-conflict", "image", "reaping"} {
		t.Run(change, func(t *testing.T) {
			f := legacyTerminalTaskFixture(true)
			base := f.client()
			home := storedLegacyHOME(t, base, f)
			read := false
			reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				read = true
				switch o := obj.(type) {
				case *v1alpha1.AgentSession:
					switch change {
					case "heartbeat":
						o.Status.Agent.LastHeartbeat.Time = f.now.Add(-2*time.Minute - time.Second)
					case "usage-conflict":
						o.Status.UsageConflict = &v1alpha1.UsageConflictStatus{Reason: "ConflictingSnapshot"}
					case "reaping":
						o.DeletionTimestamp = &metav1.Time{Time: f.now}
					}
				case *corev1.Pod:
					switch change {
					case "restart-main":
						o.Status.ContainerStatuses[0].RestartCount = 1
					case "restart-sidecar":
						o.Status.ContainerStatuses[1].RestartCount = 1
					case "restart-init":
						o.Status.InitContainerStatuses[0].RestartCount = 1
					case "image":
						o.Spec.Containers[0].Image = "synthetic.invalid/agent:2.71.0"
					}
				}
				return nil
			}})
			r := terminalTaskReconciler(f, base, reader)
			r.LegacyTerminalTaskReclaim = true
			s := storedHistory(t, base, f.s)
			_, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s)
			want := "ProofUnknown"
			if change == "reaping" {
				want = "Protected"
			}
			cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim)
			if err != nil || !read || cond == nil || cond.Reason != want || storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest != nil {
				t.Fatalf("uncached refusal missing: %+v, %v, read=%v", cond, err, read)
			}
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds != nil {
				t.Fatal("uncached uncertainty stopped compute", err)
			}
			assertLegacyHOMEUnchanged(t, base, home)
		})
	}
}

func TestLegacyTerminalTaskStopIntentRechecksRestarts(t *testing.T) {
	f := legacyTerminalTaskFixture(true)
	base := f.client()
	home := storedLegacyHOME(t, base, f)
	podReads := 0
	reader := interceptor.NewClient(base, interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
		if err := c.Get(ctx, key, obj, opts...); err != nil {
			return err
		}
		if pod, ok := obj.(*corev1.Pod); ok {
			podReads++
			// The first expiry view is clean, as is the stop intent's first
			// Pod read. Only its repeated uncached view sees the restart.
			if podReads >= 3 {
				pod.Status.ContainerStatuses[0].RestartCount = 1
			}
		}
		return nil
	}})
	r := terminalTaskReconciler(f, base, reader)
	r.LegacyTerminalTaskReclaim = true
	_, _, err := r.reconcileTerminalTaskReclaim(context.Background(), storedHistory(t, base, f.s))
	if err != nil || podReads < 3 || storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest != nil {
		t.Fatalf("stop intent did not recheck: reads=%d, err=%v", podReads, err)
	}
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds != nil {
		t.Fatal("stop-intent uncertainty patched compute", err)
	}
	assertLegacyHOMEUnchanged(t, base, home)
}

func TestLegacyTerminalTaskSnapshotPassesExistingCEL(t *testing.T) {
	f := legacyTerminalTaskFixture(false)
	s := newSession(t, nil)
	f.s.UID = s.UID
	f.s.Status.PrivateHistory.SessionUID = string(s.UID)
	f.s.Status.UsageObservation.Source.SessionUID = string(s.UID)
	r := terminalTaskReconciler(f, nil, nil)
	r.LegacyTerminalTaskReclaim = true
	task, err := r.taskReclaimProof(f.s, f.now)
	if err != nil {
		t.Fatal(err)
	}
	h := f.s.Status.PrivateHistory.DeepCopy()
	h.HomeClaim, h.BindingGeneration = HomeClaimName(s.Name), s.Generation
	h.LastExecutor.PodName, h.LastExecutor.Generation = s.Name, s.Generation
	h.TaskTerminal = &v1alpha1.PrivateTaskTerminalStatus{Task: *task, ObservedAt: metav1.NewTime(f.now), DueAt: metav1.NewTime(f.now.Add(time.Minute))}
	s.Status.PrivateHistory = h
	if err := k8s.Status().Update(context.Background(), s); err != nil {
		t.Fatal("existing CRD refused derived legacy exit source", err)
	}
	got := storedHistory(t, k8s, s)
	if !apiequality.Semantic.DeepEqual(h.TaskTerminal, got.Status.PrivateHistory.TaskTerminal) {
		t.Fatal("legacy snapshot was pruned or changed")
	}
}

func TestLegacyTerminalTaskReclaimStopAndDeletePreservesHOME(t *testing.T) {
	for _, provider := range []v1alpha1.AgentKind{v1alpha1.AgentClaude, v1alpha1.AgentCodex} {
		t.Run(string(provider), func(t *testing.T) {
			f := legacyTerminalTaskFixture(false)
			f.s.Spec.Agent = provider
			f.s.Status.PrivateHistory.Provider = string(provider)
			f.s.Status.UsageObservation.Source.Provider = string(provider)
			base := f.client()
			home := storedLegacyHOME(t, base, f)
			r := terminalTaskReconciler(f, base, base)
			r.LegacyTerminalTaskReclaim = true
			now := f.now
			r.Now = func() time.Time { return now }
			reconcileSession(t, r, f.s, 3)
			rec := storedHistory(t, base, f.s).Status.PrivateHistory.TaskTerminal.DeepCopy()
			if rec == nil {
				t.Fatal("legacy snapshot missing")
			}
			now = now.Add(time.Minute)
			reconcileSession(t, r, f.s, 3)
			var pod corev1.Pod
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds == nil {
				t.Fatal("legacy stop was not requested", err)
			}
			if storedHistory(t, base, f.s).Status.PrivateHistory.StopRequest == nil {
				t.Fatal("stop intent missing")
			}
			assertLegacyHOMEUnchanged(t, base, home)
			pod.Status.Phase, pod.Status.Reason = corev1.PodFailed, "DeadlineExceeded"
			for i := range pod.Status.ContainerStatuses {
				pod.Status.ContainerStatuses[i].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, FinishedAt: metav1.NewTime(now)}}
			}
			pod.Status.InitContainerStatuses[0].State = corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{FinishedAt: metav1.NewTime(f.now.Add(-time.Hour))}}
			if err := base.Status().Update(context.Background(), &pod); err != nil {
				t.Fatal(err)
			}
			// agentd stopped with the Pod, so its heartbeat ages past the
			// pre-stop window before the guarded delete. The terminal record,
			// not the heartbeat, proves the stopped executor.
			now = now.Add(5 * time.Minute)
			lease := f.lease.DeepCopy()
			lease.Spec.RenewTime.Time = now.Add(-time.Second)
			if err := base.Update(context.Background(), lease); err != nil {
				t.Fatal(err)
			}
			reconcileSession(t, r, f.s, 4)
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); !apierrors.IsNotFound(err) {
				t.Fatal("legacy terminal executor retained", err)
			}
			got := storedHistory(t, base, f.s)
			cond := meta.FindStatusCondition(got.Status.Conditions, ConditionTerminalTaskReclaim)
			if !executorTerminalRecorded(got.Status.PrivateHistory) || !apiequality.Semantic.DeepEqual(rec, got.Status.PrivateHistory.TaskTerminal) || got.Status.Agent.Task.ExitSource != nil ||
				cond == nil || cond.Reason != "Reclaimed" || !strings.Contains(cond.Message, "proof=legacy-usage") || got.Status.PrivateHistory.Expiry != nil || got.Status.Rescue != nil {
				t.Fatalf("legacy outcome or retention lost: %+v", got.Status)
			}
			// A stale post-stop heartbeat cannot undo a settled reclaim.
			r.Now = func() time.Time { return now.Add(time.Hour) }
			reconcileSession(t, r, f.s, 2)
			if cond := meta.FindStatusCondition(storedHistory(t, base, f.s).Status.Conditions, ConditionTerminalTaskReclaim); cond == nil || cond.Reason != "Reclaimed" {
				t.Fatal("settled legacy reclaim changed after heartbeat stopped", cond)
			}
			assertLegacyHOMEUnchanged(t, base, home)
		})
	}
}

// A legacy executor that ended on its own (eviction, deadline) before any
// stop is still bound by its usage proof: recorded, then removed once the
// whole-executor terminal record exists. Its stale heartbeat does not block it.
func TestLegacyTerminalTaskSelfEndedPodIsReclaimed(t *testing.T) {
	f := legacyTerminalTaskFixture(false)
	terminateLegacyPod(f.pod, f.now.Add(-10*time.Minute))
	f.s.Status.Agent.LastHeartbeat.Time = f.now.Add(-10 * time.Minute)
	base := f.client()
	home := storedLegacyHOME(t, base, f)
	r := terminalTaskReconciler(f, base, base)
	r.LegacyTerminalTaskReclaim = true
	now := f.now
	r.Now = func() time.Time { return now }
	reconcileSession(t, r, f.s, 3)
	if storedHistory(t, base, f.s).Status.PrivateHistory.TaskTerminal == nil {
		t.Fatal("self-ended legacy executor was not recorded")
	}
	now = now.Add(5 * time.Minute)
	lease := f.lease.DeepCopy()
	lease.Spec.RenewTime.Time = now.Add(-time.Second)
	if err := base.Update(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, f.s, 4)
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); !apierrors.IsNotFound(err) {
		t.Fatal("self-ended legacy executor retained", err)
	}
	got := storedHistory(t, base, f.s)
	if cond := meta.FindStatusCondition(got.Status.Conditions, ConditionTerminalTaskReclaim); !executorTerminalRecorded(got.Status.PrivateHistory) ||
		cond == nil || cond.Reason != "Reclaimed" {
		t.Fatalf("self-ended legacy outcome: %+v", got.Status)
	}
	assertLegacyHOMEUnchanged(t, base, home)
}

// Without a whole-executor terminal record (here the node lease is stale), a
// terminal legacy executor is kept; the missing heartbeat is not the proof.
func TestLegacyTerminalTaskKeepsTerminalPodWithoutRecord(t *testing.T) {
	f := legacyTerminalTaskFixture(false)
	base := f.client()
	home := storedLegacyHOME(t, base, f)
	r := terminalTaskReconciler(f, base, base)
	r.LegacyTerminalTaskReclaim = true
	now := f.now
	r.Now = func() time.Time { return now }
	reconcileSession(t, r, f.s, 3)
	now = now.Add(time.Minute)
	reconcileSession(t, r, f.s, 3)
	var pod corev1.Pod
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil || pod.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("legacy stop was not requested", err)
	}
	terminateLegacyPod(&pod, now)
	if err := base.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	lease := f.lease.DeepCopy()
	lease.Spec.RenewTime.Time = now.Add(-time.Hour)
	if err := base.Update(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	reconcileSession(t, r, f.s, 4)
	if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.pod), &pod); err != nil {
		t.Fatal("terminal legacy executor without a terminal record was removed", err)
	}
	got := storedHistory(t, base, f.s)
	if cond := meta.FindStatusCondition(got.Status.Conditions, ConditionTerminalTaskReclaim); executorTerminalRecorded(got.Status.PrivateHistory) ||
		cond == nil || cond.Reason == "Reclaimed" {
		t.Fatalf("unrecorded terminal executor settled: %+v", cond)
	}
	assertLegacyHOMEUnchanged(t, base, home)
}

// A legacy Session whose executor Pod is already gone says so plainly and
// records nothing: the usage proof binds a live executor only.
func TestLegacyTerminalTaskAbsentPodSaysSo(t *testing.T) {
	f := legacyTerminalTaskFixture(false)
	base := f.client()
	if err := base.Delete(context.Background(), f.pod.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	r := terminalTaskReconciler(f, base, base)
	r.LegacyTerminalTaskReclaim = true
	s := storedHistory(t, base, f.s)
	if _, _, err := r.reconcileTerminalTaskReclaim(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim)
	if s.Status.PrivateHistory.TaskTerminal != nil || cond == nil || cond.Reason != "ProofUnknown" ||
		!strings.Contains(cond.Message, "legacy executor Pod is absent") {
		t.Fatalf("absent legacy executor: %+v", cond)
	}
}
