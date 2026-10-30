package controller

// DESIGN-025 section 24: compute-only reclaim of an original terminal task.
// No rescue, HOME write, finalizer release or archive operation belongs here.

import (
	"context"
	"errors"
	"fmt"
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
const terminalTaskReclaimRetry = homeExpiryUnknownRetry
const TerminalTaskReclaimSuspender = "terminal-task-reclaim"

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
	return terminalTaskChildrenStopped(ctx, r.APIReader, s)
}

func terminalTaskChildrenStopped(ctx context.Context, reader client.Reader, s *v1alpha1.AgentSession) error {
	var children v1alpha1.AgentSessionList
	if err := reader.List(ctx, &children, client.InNamespace(s.Namespace)); err != nil {
		return fmt.Errorf("children cannot be observed: %w", err)
	}
	byParent := make(map[string][]int)
	for i := range children.Items {
		parent := children.Items[i].Spec.Parent
		byParent[parent] = append(byParent[parent], i)
	}
	queue := []string{s.Name}
	seen := map[string]bool{s.Name: true}
	for len(queue) != 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, i := range byParent[parent] {
			child := &children.Items[i]
			if seen[child.Name] {
				return errors.New("descendant ownership is cyclic or uncertain")
			}
			seen[child.Name] = true
			queue = append(queue, child.Name)
			var pod corev1.Pod
			err := reader.Get(ctx, client.ObjectKeyFromObject(child), &pod)
			if err == nil && (!controlledBySession(&pod, child) || (!podEnded(&pod) || allContainersTerminated(&pod) != nil)) || err != nil && !apierrors.IsNotFound(err) {
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
		rec.ObservedAt.After(now) || rec.Task.FinishedAt.After(rec.ObservedAt.Time) || !rec.DueAt.After(rec.ObservedAt.Time)) {
		return errors.New("task result contradicts its confirmed terminal observation")
	}
	return nil
}

// terminalTaskReclaimScope is the static part of reclaim eligibility. The
// session document's retention latch uses the same scope (pod.go), so a
// Session reclaim never stops keeps D-58's TUI resume.
func terminalTaskReclaimScope(s *v1alpha1.AgentSession) bool {
	_, originalHome := s.Annotations[homeguard.Annotation]
	return s.Spec.Mode == v1alpha1.ModeTask && s.Spec.Workspace == nil && s.Spec.ProjectCanary == nil && s.Spec.PrivatePilot == nil && !originalHome
}

// terminalTaskSessionAllowed is also safe on the cache: refusals grant no action.
func terminalTaskSessionAllowed(s *v1alpha1.AgentSession, now time.Time) (string, error) {
	if !terminalTaskReclaimScope(s) {
		return "Protected", errors.New("interactive, reusable or pilot session is protected")
	}
	if homeExpiryHeld(s) {
		return "Held", errors.New("owner HOME-expiry hold blocks compute reclaim")
	}
	if err := taskReclaimProof(s, now); err != nil {
		return "ProofUnknown", err
	}
	if s.Status.PrivateHistory.Expiry != nil {
		return "Protected", errors.New("separate HOME expiry is in progress")
	}
	return "", nil
}

func (r *Reconciler) terminalTaskViewAllowed(ctx context.Context, v *expiryView) (string, error) {
	s, pod := v.s, v.pod
	if reason, err := terminalTaskSessionAllowed(s, v.now); err != nil {
		return reason, err
	}
	if err := homeClaimBound(s, v.claim); err != nil {
		return "IdentityUnknown", err
	}
	if len(v.mounts) != 0 {
		return "Protected", errors.New("HOME is mounted elsewhere")
	}
	if pod == nil {
		if !executorTerminalRecorded(s.Status.PrivateHistory) {
			return "ProofUnknown", errors.New("pod absence has no whole-executor terminal proof")
		}
		return "Reclaimed", nil
	}
	if pod.UID == "" || pod.ResourceVersion == "" || string(pod.UID) != s.Status.PrivateHistory.LastExecutor.PodUID ||
		!controlledBySession(pod, s) || isHoldPod(pod) ||
		pod.Annotations[privateHistoryExecutorAnnotation] != s.Status.PrivateHistory.ExecutorIntent ||
		pod.Annotations[privateHistoryHomeAnnotation] != s.Status.PrivateHistory.HomeUID || !privateExecutorUsesHome(pod, s.Status.PrivateHistory.HomeClaim) {
		return "IdentityUnknown", errors.New("pod is not the original private task executor, or is a protected host")
	}
	if err := r.terminalTaskChildrenStopped(ctx, s); err != nil {
		return "ChildrenActive", err
	}
	if !podPhaseTerminal(pod) && pod.Status.StartTime == nil {
		return "ProofUnknown", errors.New("executor start time is unavailable; no safe stop deadline")
	}
	if podPhaseTerminal(pod) && allContainersTerminated(pod) != nil {
		return "ProofUnknown", errors.New("terminal Pod has contradictory or missing container stop proof")
	}
	return "", nil
}

func (r *Reconciler) terminalTaskReclaimCondition(s *v1alpha1.AgentSession, reason, detail string) {
	owner := s.Spec.Parent
	if owner == "" {
		owner = s.Name
	}
	deadline := "unknown"
	if due, err := r.terminalTaskDeadline(s); err == nil && !due.IsZero() {
		deadline = due.UTC().Format(time.RFC3339)
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
	sessionUID, podUID, podVersion string
}

func terminalTaskPodRemovalAllowed(s *v1alpha1.AgentSession, pod *corev1.Pod, proof terminalTaskPodRemovalProof) bool {
	return proof.sessionUID != "" && proof.podUID != "" && proof.podVersion != "" &&
		proof.sessionUID == string(s.UID) &&
		proof.podUID == string(pod.UID) && proof.podVersion == pod.ResourceVersion &&
		s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended && !homeExpiryHeld(s) &&
		executorTerminalRecorded(s.Status.PrivateHistory) && s.Status.PrivateHistory.TaskTerminal != nil &&
		string(pod.UID) == s.Status.PrivateHistory.ExecutorTerminal.PodUID && controlledBySession(pod, s) && !isHoldPod(pod) && podEnded(pod) && allContainersTerminated(pod) == nil
}

// The observation is immutable; an explicit resume extends only its effective deadline.
func (r *Reconciler) terminalTaskDeadline(s *v1alpha1.AgentSession) (time.Time, error) {
	h := s.Status.PrivateHistory
	if h == nil || h.TaskTerminal == nil {
		return time.Time{}, nil
	}
	due := h.TaskTerminal.DueAt.Time
	if raw, ok := s.Annotations[v1alpha1.AnnotationResumedAt]; ok {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, errors.New("explicit resume time is invalid")
		}
		if resumedDue := at.Add(r.terminalTaskReclaimGrace()); resumedDue.After(due) {
			due = resumedDue
		}
	}
	return due, nil
}

func (r *Reconciler) terminalTaskStopDue(s *v1alpha1.AgentSession) bool {
	due, err := r.terminalTaskDeadline(s)
	return r.terminalTaskReclaimOn() && s.Spec.Mode == v1alpha1.ModeTask && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended &&
		err == nil && !due.IsZero() && !r.clock().Before(due) && taskReclaimProof(s, r.clock()) == nil
}

// reconcileTerminalTaskReclaim reports a retained reason even when the proof
// is incomplete. Mutations are serialized by the existing confirmed status
// write, stop intent and guarded delete; nothing is recreated after a loss.
func (r *Reconciler) reconcileTerminalTaskReclaim(ctx context.Context, s *v1alpha1.AgentSession) (ctrl.Result, bool, error) {
	// A Reaping Session belongs to the reap path's own stop (section 4).
	if !r.terminalTaskReclaimOn() || !privateHistoryProtected(s) || s.Spec.Mode != v1alpha1.ModeTask || !s.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, false, nil
	}
	if h, a := s.Status.PrivateHistory, s.Status.Agent; s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended &&
		(h == nil || h.TaskTerminal == nil) && (a == nil || a.Task == nil && a.Status != protocol.AgentExited && a.Status != protocol.AgentInterrupted && a.Status != protocol.AgentFailed) {
		// A new task still running normally is not retained terminal compute.
		return ctrl.Result{}, false, nil
	}
	// Cached refusals need no API traffic or polling: Session/Pod events can
	// supply changed proof or lift a protection. A settled reclaim is final.
	if reason, err := terminalTaskSessionAllowed(s, r.clock()); err != nil {
		r.terminalTaskReclaimCondition(s, reason, err.Error())
		return ctrl.Result{}, false, nil
	}
	if cond := meta.FindStatusCondition(s.Status.Conditions, ConditionTerminalTaskReclaim); cond != nil && cond.Reason == "Reclaimed" {
		return ctrl.Result{}, false, nil
	}
	if err := terminalTaskChildrenStopped(ctx, r.Client, s); err != nil {
		r.terminalTaskReclaimCondition(s, "ChildrenActive", err.Error())
		return ctrl.Result{RequeueAfter: homeExpiryRefusalRetry}, false, nil
	}
	h := s.Status.PrivateHistory
	if h.TaskTerminal == nil {
		// This cached CAS records an observation only. No resource action is
		// authorized until a later due reconcile confirms the record uncached.
		h.TaskTerminal = &v1alpha1.PrivateTaskTerminalStatus{Task: *s.Status.Agent.Task.DeepCopy(), ObservedAt: recordTime(r.clock()), DueAt: recordTime(r.clock().Add(r.terminalTaskReclaimGrace()))}
		err := r.Client.Status().Update(ctx, s)
		return ctrl.Result{RequeueAfter: r.terminalTaskReclaimGrace()}, true, err
	}
	due, err := r.terminalTaskDeadline(s)
	if err != nil {
		r.terminalTaskReclaimCondition(s, "ProofUnknown", err.Error())
		return ctrl.Result{}, false, nil
	}
	if r.clock().Before(due) {
		r.terminalTaskReclaimCondition(s, "Grace", "original provider exited; waiting for compute grace")
		return ctrl.Result{RequeueAfter: due.Sub(r.clock())}, false, nil
	}
	v, err := r.readExpiryView(ctx, s)
	if err != nil {
		r.terminalTaskReclaimCondition(s, "ProofUnknown", "uncached reclaim observation unavailable")
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	reason, refusal := r.terminalTaskViewAllowed(ctx, v)
	if refusal != nil || reason == "Reclaimed" {
		detail := "original executor Pod is absent"
		result := ctrl.Result{}
		if refusal != nil {
			detail = refusal.Error()
			result.RequeueAfter = homeExpiryRefusalRetry
		}
		r.terminalTaskReclaimCondition(s, reason, detail)
		return result, false, nil
	}
	// A fresh resume/hold/result can overtake the cached deadline. The later
	// resource CAS still fences changes after this uncached observation.
	due, err = r.terminalTaskDeadline(v.s)
	if err != nil || due.IsZero() {
		r.terminalTaskReclaimCondition(s, "ProofUnknown", "fresh reclaim deadline unavailable")
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	if v.now.Before(due) {
		r.terminalTaskReclaimCondition(s, "Grace", "explicit resume restarted compute grace")
		return ctrl.Result{RequeueAfter: due.Sub(v.now)}, false, nil
	}
	h = v.s.Status.PrivateHistory
	if v.s.Spec.OperatingMode != v1alpha1.OperatingModeSuspended {
		base := v.s.DeepCopy()
		v.s.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
		if v.s.Annotations == nil {
			v.s.Annotations = map[string]string{}
		}
		v.s.Annotations[v1alpha1.AnnotationSuspendedBy] = TerminalTaskReclaimSuspender
		err := r.Client.Patch(ctx, v.s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
		return ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
	}
	if !podPhaseTerminal(v.pod) {
		r.terminalTaskReclaimCondition(s, "Stopping", "waiting for kubelet terminal proof after the confirmed stop request")
		obs := observation{pod: owned[*corev1.Pod]{obj: v.pod}}
		res, stop, err := r.requestPrivateExecutorStop(ctx, v.s, &obs)
		if err != nil || stop {
			return res, stop, err
		}
		// Adopt the confirmed stop intent's new Session resourceVersion before
		// the ordinary status write at the end of this reconcile.
		*s = *v.s
		r.terminalTaskReclaimCondition(s, "Stopping", "waiting for kubelet terminal proof after the confirmed stop request")
		res.RequeueAfter = max(res.RequeueAfter, terminalTaskReclaimRetry)
		return res, false, nil
	}
	if !executorTerminalRecorded(h) {
		r.terminalTaskReclaimCondition(s, "ProofUnknown", "whole-Pod terminal record has not been confirmed")
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, false, nil
	}
	// A confirming CAS fences holds/resumes/heartbeats that overtook the read.
	if err := r.writePrivateHistory(ctx, s, v.s); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, err
	}
	proof := terminalTaskPodRemovalProof{string(s.UID), string(v.pod.UID), v.pod.ResourceVersion}
	if !terminalTaskPodRemovalAllowed(s, v.pod, proof) {
		return ctrl.Result{RequeueAfter: terminalTaskReclaimRetry}, true, nil
	}
	if err := deletePod(ctx, r.Client, s, v.pod, proof); err != nil {
		return ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
	}
	r.terminalTaskReclaimCondition(s, "Reclaimed", "original terminal executor Pod removed with normal grace")
	return ctrl.Result{}, true, r.Client.Status().Update(ctx, s)
}
