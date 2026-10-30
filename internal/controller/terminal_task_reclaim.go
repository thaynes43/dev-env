package controller

// DESIGN-025 section 24: compute-only reclaim of an original terminal task.
// No rescue, HOME write, finalizer release or archive operation belongs here.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/homeguard"
)

const DefaultTerminalTaskReclaimGrace = 10 * time.Minute
const ConditionTerminalTaskReclaim = "TerminalTaskPodReclaim"
const terminalTaskReclaimRetry = 30 * time.Second

func (r *Reconciler) terminalTaskReclaimOn() bool {
	return r.TerminalTaskPodReclaim && r.PrivateTerminalRecord && r.PrivateExecutorStop
}

func (r *Reconciler) terminalTaskReclaimGrace() time.Duration {
	if r.TerminalTaskReclaimGrace > 0 {
		return r.TerminalTaskReclaimGrace
	}
	return DefaultTerminalTaskReclaimGrace
}

// Unknown states are deliberately retained, including a child whose executor
// vanished before a terminal observation. Pod absence alone proves no stop.
func (r *Reconciler) terminalTaskChildrenStopped(ctx context.Context, s *v1alpha1.AgentSession) error {
	var children v1alpha1.AgentSessionList
	if err := r.APIReader.List(ctx, &children, client.InNamespace(s.Namespace)); err != nil {
		return fmt.Errorf("children cannot be observed: %w", err)
	}
	for i := range children.Items {
		child := &children.Items[i]
		if child.Spec.Parent != s.Name {
			continue
		}
		var pod corev1.Pod
		err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(child), &pod)
		if err == nil && (!controlledBySession(&pod, child) || !podEnded(&pod)) || err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("child %s has live or uncertain compute", child.Name)
		}
		if a := child.Status.Agent; a != nil && (a.Status != protocol.AgentExited || a.Attached != 0) {
			return fmt.Errorf("child %s still reports active work", child.Name)
		}
		if child.Status.Phase != v1alpha1.PhaseArchived && (!executorTerminalRecorded(child.Status.PrivateHistory) ||
			child.Status.Agent == nil || child.Status.Agent.Status != protocol.AgentExited || child.Status.Agent.Attached != 0) {
			return fmt.Errorf("child %s has no confirmed terminal outcome", child.Name)
		}
	}
	return nil
}

// taskReclaimProof checks the retained outcome, not an elapsed idle timer.
func taskReclaimProof(s *v1alpha1.AgentSession, now time.Time) error {
	h, a := s.Status.PrivateHistory, s.Status.Agent
	if err := privateHistoryIdentity(s); err != nil {
		return err
	}
	if h.Admission != sharedStarted || h.HomeUID == "" || !h.ExecutorAttempted || h.ExecutorIntent == "" ||
		h.LastExecutor == nil || h.ExecutorMissed != nil {
		return errors.New("original private admission and executor are unproved")
	}
	if a == nil || a.Status != protocol.AgentExited || a.Attached != 0 || a.Task == nil || a.Task.ExitSource == nil {
		return errors.New("original provider exit and task result are unproved, or work is active")
	}
	task, source := a.Task, a.Task.ExitSource
	if !protocol.UsageSource(*source).Valid() || source.SessionUID != string(s.UID) || source.PodUID != h.LastExecutor.PodUID ||
		source.Provider != string(s.Spec.Agent) || source.RequestedModel != s.Spec.Model || task.FinishedAt.IsZero() || task.FinishedAt.After(now) {
		return errors.New("provider exit does not bind this Session, executor and attempt")
	}
	// The immutable original-attempt receipt is independent of mutable agent
	// status. A later or conflicting attempt cannot borrow the old outcome.
	u := s.Status.UsageObservation
	if u == nil || u.Source != *source || !u.ObservedAt.Equal(&task.FinishedAt) {
		return errors.New("task result and retained original attempt contradict or lack proof")
	}
	if rec := h.TaskTerminal; rec != nil && (!apiequality.Semantic.DeepEqual(&rec.Task, task) ||
		rec.ObservedAt.After(now) || !rec.DueAt.After(rec.ObservedAt.Time)) {
		return errors.New("task result contradicts its confirmed terminal observation")
	}
	return nil
}

func (r *Reconciler) terminalTaskViewAllowed(ctx context.Context, v *expiryView) (string, error) {
	s, pod := v.s, v.pod
	_, originalHome := s.Annotations[homeguard.Annotation]
	if s.Spec.Mode != v1alpha1.ModeTask || s.Spec.Workspace != nil || s.Spec.ProjectCanary != nil || s.Spec.PrivatePilot != nil ||
		originalHome || slices.Contains(r.TerminalTaskProtectedSessions, s.Namespace+"/"+s.Name) {
		return "Protected", errors.New("interactive, reusable, pilot or configured coordinator Session is protected")
	}
	if homeExpiryHeld(s) {
		return "Held", errors.New("owner HOME-expiry hold blocks compute reclaim")
	}
	if err := taskReclaimProof(s, v.now); err != nil {
		return "ProofUnknown", err
	}
	if err := homeClaimBound(s, v.claim); err != nil {
		return "IdentityUnknown", err
	}
	if len(v.mounts) != 0 || s.Status.PrivateHistory.Expiry != nil {
		return "Protected", errors.New("HOME is mounted elsewhere or its separate expiry is in progress")
	}
	if pod == nil {
		if !executorTerminalRecorded(s.Status.PrivateHistory) {
			return "ProofUnknown", errors.New("Pod absence has no whole-executor terminal proof")
		}
		return "Reclaimed", nil
	}
	_, coordinator := pod.Annotations["dev-env.haynesops.com/coordinator-host"]
	if pod.UID == "" || pod.ResourceVersion == "" || string(pod.UID) != s.Status.PrivateHistory.LastExecutor.PodUID ||
		!controlledBySession(pod, s) || isHoldPod(pod) || coordinator ||
		pod.Annotations[privateHistoryExecutorAnnotation] != s.Status.PrivateHistory.ExecutorIntent ||
		pod.Annotations[privateHistoryHomeAnnotation] != s.Status.PrivateHistory.HomeUID || !privateExecutorUsesHome(pod, s.Status.PrivateHistory.HomeClaim) {
		return "IdentityUnknown", errors.New("Pod is not the original private task executor, or is a protected host")
	}
	if err := r.terminalTaskChildrenStopped(ctx, s); err != nil {
		return "ChildrenActive", err
	}
	return "", nil
}

func terminalTaskReclaimCondition(s *v1alpha1.AgentSession, reason, detail string) {
	owner := s.Spec.Parent
	if owner == "" {
		owner = s.Name
	}
	deadline := "unknown"
	if h := s.Status.PrivateHistory; h != nil && h.TaskTerminal != nil {
		deadline = h.TaskTerminal.DueAt.UTC().Format(time.RFC3339)
	}
	status := metav1.ConditionTrue
	if reason == "Reclaimed" {
		status = metav1.ConditionFalse
	}
	meta.SetStatusCondition(&s.Status.Conditions, metav1.Condition{Type: ConditionTerminalTaskReclaim, Status: status,
		ObservedGeneration: s.Generation, Reason: reason,
		Message: truncate(fmt.Sprintf("owner=%s; deadline=%s; %s; original HOME, history and outcome retained", owner, deadline, detail))})
}

// The private proof is constructed only after uncached eligibility checks and
// a confirmed Session CAS. deletePod still validates its exact object versions.
// It gives no authority to any volume or finalizer guard.
type terminalTaskPodRemovalProof struct {
	sessionUID, sessionVersion, podUID, podVersion string
}

func terminalTaskPodRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod, proof terminalTaskPodRemovalProof) bool {
	return proof.sessionUID != "" && proof.sessionVersion != "" && proof.podUID != "" && proof.podVersion != "" &&
		proof.sessionUID == string(s.UID) && proof.sessionVersion == s.ResourceVersion &&
		proof.podUID == string(pod.UID) && proof.podVersion == pod.ResourceVersion &&
		s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended && !homeExpiryHeld(s) &&
		executorTerminalRecorded(s.Status.PrivateHistory) && s.Status.PrivateHistory.TaskTerminal != nil &&
		string(pod.UID) == s.Status.PrivateHistory.ExecutorTerminal.PodUID && controlledBySession(pod, s) && !isHoldPod(pod) && podEnded(pod)
}

func (r *Reconciler) terminalTaskStopDue(s *v1alpha1.AgentSession) bool {
	return r.terminalTaskReclaimOn() && s.Spec.Mode == v1alpha1.ModeTask && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended &&
		s.Status.PrivateHistory != nil && s.Status.PrivateHistory.TaskTerminal != nil &&
		!r.clock().Before(s.Status.PrivateHistory.TaskTerminal.DueAt.Time) && taskReclaimProof(s, r.clock()) == nil
}

// reconcileTerminalTaskReclaim reports a retained reason even when the proof
// is incomplete. Mutations are serialized by the existing confirmed status
// write, stop intent and guarded delete; nothing is recreated after a loss.
func (r *Reconciler) reconcileTerminalTaskReclaim(ctx context.Context, s *v1alpha1.AgentSession) (ctrl.Result, bool, error) {
	if !r.terminalTaskReclaimOn() || !privateHistoryProtected(s) || s.Spec.Mode != v1alpha1.ModeTask {
		return ctrl.Result{}, false, nil
	}
	v, err := r.readExpiryView(ctx, s)
	if err != nil {
		terminalTaskReclaimCondition(s, "ProofUnknown", "uncached reclaim observation unavailable")
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	reason, refusal := r.terminalTaskViewAllowed(ctx, v)
	if refusal != nil || reason == "Reclaimed" {
		detail := "original executor Pod is absent"
		if refusal != nil {
			detail = refusal.Error()
		}
		terminalTaskReclaimCondition(s, reason, detail)
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	h := v.s.Status.PrivateHistory
	if h.TaskTerminal == nil {
		h.TaskTerminal = &v1alpha1.PrivateTaskTerminalStatus{Task: *v.s.Status.Agent.Task.DeepCopy(), ObservedAt: recordTime(v.now), DueAt: recordTime(v.now.Add(r.terminalTaskReclaimGrace()))}
		return ctrl.Result{RequeueAfter: time.Second}, true, r.writePrivateHistory(ctx, s, v.s)
	}
	if v.now.Before(h.TaskTerminal.DueAt.Time) {
		terminalTaskReclaimCondition(s, "Grace", "original provider exited; waiting for compute grace")
		return ctrl.Result{RequeueAfter: min(h.TaskTerminal.DueAt.Sub(v.now), terminalTaskReclaimRetry)}, false, nil
	}
	if v.s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		base := v.s.DeepCopy()
		v.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		err := r.Client.Patch(ctx, v.s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		return ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
	}
	if !podPhaseTerminal(v.pod) {
		terminalTaskReclaimCondition(s, "Stopping", "waiting for kubelet terminal proof after the confirmed stop request")
		obs := observation{pod: owned[*corev1.Pod]{obj: v.pod}}
		res, stop, err := r.requestPrivateExecutorStop(ctx, v.s, &obs)
		if err != nil || stop {
			return res, stop, err
		}
		// Adopt the confirmed stop intent's new Session resourceVersion before
		// the ordinary status write at the end of this reconcile.
		*s = *v.s
		terminalTaskReclaimCondition(s, "Stopping", "waiting for kubelet terminal proof after the confirmed stop request")
		return sooner(res, ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}), false, nil
	}
	if !executorTerminalRecorded(h) {
		terminalTaskReclaimCondition(s, "ProofUnknown", "whole-Pod terminal record has not been confirmed")
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	// A confirming CAS fences holds/resumes/heartbeats that overtook the read.
	if err := r.writePrivateHistory(ctx, s, v.s); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	}
	proof := terminalTaskPodRemovalProof{string(s.UID), s.ResourceVersion, string(v.pod.UID), v.pod.ResourceVersion}
	if !terminalTaskPodRemovalAllowed(s, v.pod, proof) {
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, true, nil
	}
	if err := deletePod(ctx, r.Client, s, v.pod, proof); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
	}
	terminalTaskReclaimCondition(s, "Reclaimed", "original terminal executor Pod removed with normal grace")
	return ctrl.Result{RequeueAfter: time.Second}, true, r.Client.Status().Update(ctx, s)
}
