package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/commonrules"
	"github.com/thaynes43/dev-env/internal/taskbudget"
)

// privateBudgetTask is the finite server-owned ordinary task form. It grants
// no launch authority without the separate HOME/intent and ledger proofs.
func privateBudgetTask(s *v1alpha1.AgentSession) bool {
	if s == nil || s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil || s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Profile != "dev" ||
		(s.Spec.Agent != v1alpha1.AgentClaude && s.Spec.Agent != v1alpha1.AgentCodex) || s.Spec.CommonRules == nil || s.Spec.Limits == nil || s.Spec.Limits.Timeout == nil || s.Spec.Limits.Timeout.Duration <= 0 || s.Spec.Limits.Timeout.Duration > 45*time.Minute {
		return false
	}
	_, err := taskbudget.BindingFromAnnotations(s.Annotations)
	return err == nil
}

// inspectPrivateManagedLaunch proves the pre-create private intent, not an
// already-created canary executor. The budget CAS remains the one launch fence.
func inspectPrivateManagedLaunch(ctx context.Context, reader client.Reader, l *taskbudget.Ledger, ref taskbudget.ManagedWorkerRef, s *v1alpha1.AgentSession) error {
	if !l.NativeAdmitted() || !privateBudgetTask(s) || !privateHistoryProtected(s) ||
		s.Spec.Profile != "dev" || s.Spec.Mode != v1alpha1.ModeTask || wantsPodGone(s) ||
		(s.Spec.Agent != v1alpha1.AgentClaude && s.Spec.Agent != v1alpha1.AgentCodex) || ref.PodUID != "" || ref.LaunchRequested {
		return taskbudget.ErrDenied
	}
	id := s.Annotations[taskbudget.WorkerAnnotation]
	if !strings.HasPrefix(id, "child:") {
		return taskbudget.ErrDenied
	}
	found := 0
	for _, w := range l.Workers {
		if w.ID == id {
			found++
			if !w.Active || w.Managed == nil || *w.Managed != ref || !w.StoppedAt.IsZero() || w.StopEvidence != "" {
				return taskbudget.ErrDenied
			}
		}
	}
	if found != 1 {
		return taskbudget.ErrDenied
	}
	r := &Reconciler{APIReader: reader}
	if privateHistoryIdentity(s) != nil {
		return taskbudget.ErrDenied
	}
	home, err := r.privateHistoryHome(ctx, s)
	if err != nil {
		return taskbudget.ErrUnavailable
	}
	h := s.Status.PrivateHistory
	if !h.ExecutorAttempted || h.ExecutorIntent == "" || h.ExecutorNotCreated || h.LastExecutor != nil || h.ExecutorMissed != nil || h.ExecutorTerminal != nil {
		return taskbudget.ErrDenied
	}
	common, err := commonrules.BoundSession(s)
	if err != nil || common.Confirm(ctx, reader) != nil {
		return taskbudget.ErrUnavailable
	}
	var pod corev1.Pod
	if err := reader.Get(ctx, client.ObjectKeyFromObject(s), &pod); !apierrors.IsNotFound(err) {
		return taskbudget.ErrUnavailable
	}
	again, err := r.privateHistoryHome(ctx, s)
	if err != nil || !reflect.DeepEqual(home, again) {
		return taskbudget.ErrUnavailable
	}
	var fresh v1alpha1.AgentSession
	if reader.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID != s.UID || fresh.ResourceVersion != s.ResourceVersion || fresh.Generation != s.Generation ||
		!reflect.DeepEqual(fresh.Spec, s.Spec) || !reflect.DeepEqual(fresh.Status.PrivateHistory, h) || !fresh.DeletionTimestamp.IsZero() {
		return taskbudget.ErrUnavailable
	}
	return nil
}

// privateBudgetStop verifies whole-container termination while the original
// private HOME and executor remain retained. It authorizes no deletion/resume.
func privateBudgetStop(ctx context.Context, reader client.Reader, s *v1alpha1.AgentSession, uid types.UID, now func() time.Time) (*corev1.Pod, error) {
	if reader == nil || uid == "" || !privateBudgetTask(s) || privateHistoryIdentity(s) != nil {
		return nil, fmt.Errorf("%w: private task/retention identity", taskbudget.ErrDenied)
	}
	r := &Reconciler{APIReader: reader}
	home, err := r.privateHistoryHome(ctx, s)
	if err != nil {
		return nil, fmt.Errorf("%w: original HOME unconfirmed", taskbudget.ErrUnavailable)
	}
	h := s.Status.PrivateHistory
	if !h.ExecutorAttempted || h.ExecutorIntent == "" || h.ExecutorNotCreated || h.LastExecutor == nil || h.LastExecutor.PodUID != string(uid) || h.LastExecutor.PodName != s.Name || h.ExecutorMissed != nil {
		return nil, fmt.Errorf("%w: original executor identity unconfirmed", taskbudget.ErrUnavailable)
	}
	var pod corev1.Pod
	if reader.Get(ctx, client.ObjectKeyFromObject(s), &pod) != nil {
		return nil, fmt.Errorf("%w: original Pod unconfirmed", taskbudget.ErrUnavailable)
	}
	if pod.UID != uid || !controlledBySession(&pod, s) || !pod.DeletionTimestamp.IsZero() || isHoldPod(&pod) || pod.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		pod.Annotations[privateHistoryHomeAnnotation] != h.HomeUID || pod.Annotations[privateHistoryExecutorAnnotation] != h.ExecutorIntent || !privateExecutorUsesHome(&pod, h.HomeClaim) || allContainersTerminated(&pod) != nil {
		return nil, fmt.Errorf("%w: retained Pod ownership or whole-container termination invalid", taskbudget.ErrDenied)
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.DisruptionTarget && condition.Reason == podGCDeletionReason {
			return nil, fmt.Errorf("%w: PodGC status is not kubelet termination proof", taskbudget.ErrDenied)
		}
	}
	if now == nil {
		now = time.Now
	}
	if _, _, _, err := executorNodeLive(ctx, reader, pod.Spec.NodeName, now); err != nil {
		return nil, fmt.Errorf("%w: executor Node/Lease unconfirmed", taskbudget.ErrUnavailable)
	}
	again, err := r.privateHistoryHome(ctx, s)
	if err != nil || !reflect.DeepEqual(home, again) {
		return nil, fmt.Errorf("%w: original HOME changed during observation", taskbudget.ErrUnavailable)
	}
	var fresh v1alpha1.AgentSession
	if reader.Get(ctx, client.ObjectKeyFromObject(s), &fresh) != nil || fresh.UID != s.UID || fresh.ResourceVersion != s.ResourceVersion || !reflect.DeepEqual(fresh.Spec, s.Spec) || !reflect.DeepEqual(fresh.Status.PrivateHistory, h) {
		return nil, fmt.Errorf("%w: private Session changed during observation", taskbudget.ErrUnavailable)
	}
	var confirmed corev1.Pod
	if reader.Get(ctx, client.ObjectKeyFromObject(&pod), &confirmed) != nil || !reflect.DeepEqual(&pod, &confirmed) {
		return nil, fmt.Errorf("%w: original Pod changed during stop observation", taskbudget.ErrUnavailable)
	}
	return &pod, nil
}

// reconcilePrivateTaskBudget never enters the legacy group-stop/rescue path.
// Finite private agentd owns self-stop; this controller only observes/accounting.
func (r *Reconciler) reconcilePrivateTaskBudget(ctx context.Context, s *v1alpha1.AgentSession, pod *owned[*corev1.Pod]) (bool, error) {
	if r.APIReader == nil || !privateHistoryProtected(s) || !privateBudgetTask(s) {
		_, err := r.suspendTaskBudget(ctx, s, "private budget ownership unavailable; all resources retained")
		return true, err
	}
	fresh, err := getOwned(ctx, r.APIReader, s, s.Name, &corev1.Pod{})
	if err != nil || fresh.foreign != "" {
		_, stopErr := r.suspendTaskBudget(ctx, s, "private executor observation unavailable; all resources retained")
		return true, stopErr
	}
	*pod = fresh
	l, err := r.observeTaskBudget(ctx, s, pod.obj)
	active := true
	if l != nil {
		for _, w := range l.Workers {
			if w.ID == s.Annotations[taskbudget.WorkerAnnotation] {
				active = w.Active
			}
		}
	}
	if err == nil && !active {
		if s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended && s.DeletionTimestamp.IsZero() {
			changed, err := r.suspendTaskBudget(ctx, s, "private executor independently stopped; HOME and original executor retained")
			return changed, err
		}
		meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: ConditionTaskBudget, Status: metav1.ConditionFalse, Reason: "Stopped", Message: "private executor independently stopped; HOME and original executor retained", ObservedGeneration: s.Generation})
		return false, nil
	}
	if err != nil || l == nil || l.Latched || managedBudgetExecutorExited(pod.obj) {
		changed, stopErr := r.suspendTaskBudget(ctx, s, "finite private executor must stop; waiting for full-container proof with HOME retained")
		if changed || stopErr != nil {
			return true, stopErr
		}
	}
	if pod.missing && wantsPodGone(s) {
		// The launch CAS, not Pod absence, decides whether this can be closed.
		if err == nil {
			_ = r.recordTaskBudgetNeverLaunched(ctx, s)
		}
		return false, nil
	}
	if err == nil && managedBudgetExecutorExited(pod.obj) {
		// A failed proof keeps the worker charged and lets normal private-history
		// observation retain the terminal/missed evidence on this reconcile.
		if r.recordTaskBudgetFailure(ctx, s, pod.obj) == nil {
			_ = r.recordTaskBudgetStop(ctx, s, pod.obj)
		}
	}
	return false, nil
}
