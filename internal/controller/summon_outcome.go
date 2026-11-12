package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// reconcileSummonOutcome runs only in the leader's reconciler, before lifecycle
// work can reclaim a terminal pod. Every decision uses an uncached Session and,
// when needed, an owned Pod. A missing pod alone proves no loss in slice a.
func (r *Reconciler) reconcileSummonOutcome(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if s.Spec.Caller == "" || summonoutcome.Terminal(s.Status.Outcome) {
		return false, nil
	}
	if r.APIReader == nil {
		return false, fmt.Errorf("summoned outcome requires the uncached API reader")
	}
	var live v1alpha1.AgentSession
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
		return apierrors.IsNotFound(err), client.IgnoreNotFound(err)
	}
	if live.UID != s.UID {
		return true, nil
	}
	*s = live
	if summonoutcome.Terminal(s.Status.Outcome) {
		return false, nil
	}
	in := summonoutcome.Inputs{
		SessionDeleting:  !s.DeletionTimestamp.IsZero(),
		SessionSuspended: s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended,
	}
	if s.Spec.Mode == v1alpha1.ModeTask {
		in.TaskResult = s.Status.TaskResult
		if in.TaskResult == nil {
			// OP740 Q-4: taskResult is a pure function of stored status.agent.task
			// or status.privateHistory.taskTerminal. That is already durable row-1
			// evidence, before observe materializes status.taskResult. Compute the
			// input here without writing that field or waiting on lifecycle work.
			in.TaskResult = taskResult(s, s.Status.Agent)
		}
	}
	if s.Status.Agent != nil {
		in.AgentStatus = s.Status.Agent.Status
	}
	reason := summonoutcome.FirstReason(in)
	if reason == "" {
		pod, err := getOwned(ctx, r.APIReader, s, s.Name, &corev1.Pod{})
		if err != nil {
			return false, err
		}
		if !pod.missing && pod.foreign == "" {
			in.PodTerminated = summonPodTerminated(pod.obj)
		}
		reason = summonoutcome.FirstReason(in)
	}
	// OP741 supplies admission/latch/deadline/watchdog/hold/suspend evidence;
	// OP743 supplies no-usable-model. Feed them into Inputs above, retaining
	// summonoutcome.FirstReason as the one table. No heartbeat age hides an input.
	next := summonoutcome.Decision{}
	if reason != "" {
		if s.Spec.Summon == nil {
			log.FromContext(ctx).Error(nil, "summoned session lacks its onUnreported snapshot", "session", s.Name)
			return false, nil
		}
		next = summonoutcome.Decision{State: v1alpha1.OutcomeState(s.Spec.Summon.OnUnreported), Note: "unreported: " + reason, Source: summonoutcome.Unreported, Reason: reason}
		if next.State != v1alpha1.OutcomeFailed && next.State != v1alpha1.OutcomeEscalated {
			log.FromContext(ctx).Error(nil, "summoned session has an invalid onUnreported snapshot", "session", s.Name)
			return false, nil
		}
	} else {
		started := in.AgentStatus == protocol.AgentBusy || in.AgentStatus == protocol.AgentIdle || in.AgentStatus == protocol.AgentWaiting
		switch {
		case started && (s.Status.Outcome == nil || s.Status.Outcome.State == v1alpha1.OutcomePending):
			next.State = v1alpha1.OutcomeRunning
		case s.Status.Outcome == nil:
			next.State = v1alpha1.OutcomePending
		default:
			return false, nil // Observations never refresh a working report's at.
		}
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	changed, err := summonoutcome.Write(ctx, r.Client, s, next, now)
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return true, nil
	}
	return changed, err
}

// A phase is terminal evidence on its own. Otherwise the pod must use
// RestartPolicyNever and every admitted regular, init (including sidecars)
// and ephemeral container must have a terminated status.
func summonPodTerminated(p *corev1.Pod) bool {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return true
	}
	if p.Spec.RestartPolicy != corev1.RestartPolicyNever || len(p.Spec.Containers) == 0 {
		return false
	}
	complete := func(names []string, statuses []corev1.ContainerStatus) bool {
		if len(names) != len(statuses) {
			return false
		}
		ended := make(map[string]bool, len(statuses))
		for _, s := range statuses {
			if ended[s.Name] || s.State.Terminated == nil || s.State.Running != nil || s.State.Waiting != nil {
				return false
			}
			ended[s.Name] = true
		}
		for _, name := range names {
			if !ended[name] {
				return false
			}
		}
		return true
	}
	var regular, init, ephemeral []string
	for _, c := range p.Spec.Containers {
		regular = append(regular, c.Name)
	}
	for _, c := range p.Spec.InitContainers {
		init = append(init, c.Name)
	}
	for _, c := range p.Spec.EphemeralContainers {
		ephemeral = append(ephemeral, c.Name)
	}
	return complete(regular, p.Status.ContainerStatuses) && complete(init, p.Status.InitContainerStatuses) && complete(ephemeral, p.Status.EphemeralContainerStatuses)
}
