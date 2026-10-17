package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/templates"
)

// Restarter is agentd's prepare-restart in a session pod (DESIGN-001 5.2).
// ReadRestart only reads. PrepareRestart is the existing stop. An error means
// the command did not run. A report with no conversation id is not an error;
// drain refuses that report, does not stop the agent, and keeps the pod.
type Restarter interface {
	ReadRestart(ctx context.Context, pod *corev1.Pod) (protocol.RestartReport, error)
	PrepareRestart(ctx context.Context, pod *corev1.Pod) (protocol.RestartReport, error)
}

// drainRetry is how soon a prepare-restart that failed, or recorded no
// conversation, is tried again. The pod stays until then (DESIGN-001 5.2).
const drainRetry = time.Minute

// summonedSession reports a session an unattended caller started (DESIGN-001
// 3.7). Caller and lane are set together. Those sessions are never drained.
func summonedSession(s *v1alpha1.AgentSession) bool {
	return s != nil && (s.Spec.Caller != "" || s.Spec.Lane != "")
}

// sessionIdle reports the idle moment drain waits for (DESIGN-001 4.2, Q-03).
// The agent has reported idle or waiting, and no tmux client is attached. Busy,
// pending, exited, and a client that is still attached are not that moment.
// This is not D-09's suspend window: drain rolls the session at the next idle
// moment, and the suspend timer is the separate park.
func sessionIdle(s *v1alpha1.AgentSession, pod *corev1.Pod) bool {
	if s == nil || pod == nil || isHoldPod(pod) || !pod.DeletionTimestamp.IsZero() ||
		pod.Status.Phase != corev1.PodRunning || !podReady(pod) {
		return false
	}
	a := s.Status.Agent
	if a == nil || a.Attached > 0 {
		return false
	}
	return a.Status == protocol.AgentIdle || a.Status == protocol.AgentWaiting
}

// drainEligible is the only time the operator marks a session Draining
// (DESIGN-001 5.2): it still wants its pod, the pod's template revision is
// not the current one, the session is idle, and it is not summoned, shared,
// private-history protected, or a canary original. A busy session stays Running.
func drainEligible(s *v1alpha1.AgentSession, pod *corev1.Pod, currentRevision string) bool {
	if s == nil || pod == nil || currentRevision == "" || !s.DeletionTimestamp.IsZero() ||
		s.Spec.OperatingMode != v1alpha1.OperatingModeRunning || s.Spec.Workspace != nil ||
		privateHistoryProtected(s) || summonedSession(s) {
		return false
	}
	rev := pod.Labels[v1alpha1.LabelRevision]
	if rev == "" || rev == currentRevision {
		return false
	}
	return sessionIdle(s, pod)
}

// templateRevision is the current template hash, or empty when the templates
// did not load. An unknown revision is not outdated (DESIGN-001 5.2).
func templateRevision(obs observation) string {
	if obs.templates == nil {
		return ""
	}
	return obs.templates.Revision()
}

// restartProofFor reports whether prepare-restart recorded a conversation for
// this pod at the session's current generation (DESIGN-001 5.2). Anything else
// keeps the pod.
func restartProofFor(s *v1alpha1.AgentSession, pod *corev1.Pod) bool {
	if s == nil || pod == nil || pod.UID == "" {
		return false
	}
	r := s.Status.Restart
	return r != nil && r.ConversationID != "" && r.Generation > 0 && r.Generation == s.Generation &&
		r.PodUID != "" && r.PodUID == string(pod.UID)
}

// drainIdle marks, proves, and deletes one outdated idle pod (DESIGN-001 5.2).
// holdIdle means the idle-suspend timer waits. It is false when drain is
// blocked, so D-09 can still park the session. It stays true while a client
// is attached or the drain is about to roll the pod. The 72h session message
// and the 24h owner line are not sent; no notifier in this process sends
// them, and drain does not wait on that notice.
//
// The phase is written by observe before any exec, so a fresh operator sees
// Draining and does not stop the agent on a session it has not marked. The
// proof comes only from the dry-run read. The existing stop runs after that
// proof is stored, the live attach count is zero, and deletePod is next. A
// read that is not resumable, a record that is not written, or a live attach
// count above zero does not stop the agent and does not delete the pod.
func (r *Reconciler) drainIdle(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates, obs *observation) (bool, ctrl.Result, bool, error) {
	if t == nil || obs == nil || obs.pod.missing || obs.pod.foreign != "" {
		return false, ctrl.Result{}, false, nil
	}
	pod := obs.pod.obj
	// The conversation is already stored for this pod. A later heartbeat can
	// report the agent exited, or the tmux count can be unset, and those
	// gates would keep the pod with no agent. Delete it.
	if s != nil && s.Status.Phase == v1alpha1.PhaseDraining && restartProofFor(s, pod) && s.Status.Restart.Stopped {
		return r.finishDrain(ctx, s, pod, obs)
	}
	if !drainEligible(s, pod, t.Revision()) {
		return false, ctrl.Result{}, false, nil
	}
	// observe writes Draining at the end of the reconcile that first notices
	// the idle outdated pod. Do not exec until that mark is stored.
	if s.Status.Phase != v1alpha1.PhaseDraining {
		return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
	}
	if r.APIReader == nil {
		return blockDrain(obs, errors.New("no uncached reader"))
	}
	recorded := false
	for range 2 {
		if behind, err := r.cacheBehind(ctx, s, pod); err != nil || behind {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
		}
		rep, err := r.readRestart(ctx, pod)
		if err != nil {
			return blockDrain(obs, err)
		}
		if !restartReportProves(s, rep) {
			return blockDrain(obs, errors.New("prepare-restart recorded no conversation identity"))
		}
		if rep.Attached == nil {
			return blockDrain(obs, errors.New("prepare-restart reported no live attach count"))
		}
		if *rep.Attached > 0 {
			return true, ctrl.Result{RequeueAfter: drainRetry}, false, nil
		}
		if restartProofFor(s, pod) && s.Status.Restart.ConversationID == rep.ConversationID {
			recorded = true
			break
		}
		written, err := r.recordRestart(ctx, s, &v1alpha1.RestartStatus{
			ConversationID: rep.ConversationID, PodUID: string(pod.UID), Generation: s.Generation,
		})
		if err != nil || !written {
			// The read was not stored. Do not stop, and do not delete.
			if err != nil {
				return blockDrain(obs, err)
			}
			return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
		}
		var cur corev1.Pod
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &cur); err != nil {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
		}
		pod = &cur
	}
	if !recorded {
		return blockDrain(obs, errors.New("prepare-restart recorded no conversation identity"))
	}
	// The conversation is already stored. A stop exec can fail after agentd
	// has stopped the CLI (a dropped stream, or the 90s timeout against the
	// 30s grace). Keeping the pod then lets the next heartbeat report the
	// agent exited, drain becomes ineligible, and the session stays on the
	// old revision with no agent. Delete the pod anyway.
	if s.Status.Restart == nil {
		return blockDrain(obs, errors.New("prepare-restart recorded no conversation identity"))
	}
	written, err := r.recordRestart(ctx, s, &v1alpha1.RestartStatus{
		ConversationID: s.Status.Restart.ConversationID, PodUID: s.Status.Restart.PodUID,
		Generation: s.Status.Restart.Generation, Stopped: true,
	})
	if err != nil || !written {
		if err != nil {
			return blockDrain(obs, err)
		}
		return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
	}
	if _, err := r.prepareRestart(ctx, pod); err != nil {
		log.FromContext(ctx).Error(err, "prepare-restart stop failed after the conversation was stored; deleting the pod")
	}
	return r.finishDrain(ctx, s, pod, obs)
}

// finishDrain deletes a pod whose restart proof is already stored. A conflict
// or another delete error retries here, not through the idle and attach gates.
func (r *Reconciler) finishDrain(ctx context.Context, s *v1alpha1.AgentSession, pod *corev1.Pod, obs *observation) (bool, ctrl.Result, bool, error) {
	if err := drainPod(ctx, r.Client, s, pod); err != nil {
		if apierrors.IsConflict(err) {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		obs.removalBlocked = err
		return false, ctrl.Result{RequeueAfter: drainRetry}, false, nil
	}
	log.FromContext(ctx).Info("drained an idle outdated session pod", "pod", pod.Name, "uid", pod.UID, "revision", pod.Labels[v1alpha1.LabelRevision])
	r.event(s, corev1.EventTypeNormal, "Drain", "IdleDrain", "deleted the outdated pod after the launch-record read was stored and the agent was stopped (DESIGN-001 5.2)")
	return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
}

// blockDrain keeps the pod and lets the idle-suspend timer run. removalBlocked
// is set, so holdIdle is false (DESIGN-001 5.2, D-09).
func blockDrain(obs *observation, why error) (bool, ctrl.Result, bool, error) {
	obs.removalBlocked = fmt.Errorf("%w: %s", errDrainNeedsRestart, why.Error())
	return false, ctrl.Result{RequeueAfter: drainRetry}, false, nil
}

// restartReportProves is the prepare-restart report drain accepts: this
// session, a resumable conversation id the status field can store.
func restartReportProves(s *v1alpha1.AgentSession, rep protocol.RestartReport) bool {
	return s != nil && rep.Resumable && rep.Session == s.Name && rep.ConversationID != "" && len(rep.ConversationID) <= 256
}

// restarter is the configured value, or the rescuer when that value implements
// Restarter. ExecRescuer does, so the operator process picks both commands up
// without a second wiring.
func (r *Reconciler) restarter() (Restarter, error) {
	if r.Restarter != nil {
		return r.Restarter, nil
	}
	if existing, ok := r.Rescuer.(Restarter); ok {
		return existing, nil
	}
	return nil, errors.New("prepare-restart is not available")
}

// readRestart is the dry-run. It does not stop the agent.
func (r *Reconciler) readRestart(ctx context.Context, pod *corev1.Pod) (protocol.RestartReport, error) {
	restarter, err := r.restarter()
	if err != nil {
		return protocol.RestartReport{}, err
	}
	return restarter.ReadRestart(ctx, pod)
}

// prepareRestart is the existing stop. Drain calls it only when deletePod is next.
func (r *Reconciler) prepareRestart(ctx context.Context, pod *corev1.Pod) (protocol.RestartReport, error) {
	restarter, err := r.restarter()
	if err != nil {
		return protocol.RestartReport{}, err
	}
	return restarter.PrepareRestart(ctx, pod)
}

// recordRestart writes the prepare-restart proof before the pod is deleted
// (DESIGN-001 5.2, the same record-first rule as rescue). It writes only while
// this is still the session the report was for.
func (r *Reconciler) recordRestart(ctx context.Context, s *v1alpha1.AgentSession, rec *v1alpha1.RestartStatus) (bool, error) {
	if r.APIReader == nil || rec == nil || rec.ConversationID == "" {
		return false, errors.New("drain needs an uncached session read and a conversation id")
	}
	for range 5 {
		var fresh v1alpha1.AgentSession
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(s), &fresh); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		if fresh.UID != s.UID || fresh.Generation != rec.Generation || !fresh.DeletionTimestamp.IsZero() ||
			fresh.Spec.OperatingMode != v1alpha1.OperatingModeRunning || fresh.Spec.Workspace != nil ||
			privateHistoryProtected(&fresh) || summonedSession(&fresh) {
			return false, nil
		}
		fresh.Status.Restart = rec
		err := r.Client.Status().Update(ctx, &fresh)
		if err == nil {
			*s = fresh
			return true, nil
		}
		if !apierrors.IsConflict(err) {
			return false, client.IgnoreNotFound(err)
		}
	}
	return false, nil
}

// drainPod deletes the session pod after the drain guard allows it
// (DESIGN-001 5.2). deletePod is the only pod delete. The volume finalizer
// is not lifted.
func drainPod(ctx context.Context, c client.Client, s *v1alpha1.AgentSession, pod *corev1.Pod) error {
	if err := podRemovalAllowed(s, pod); err != nil {
		return err
	}
	return deletePod(ctx, c, s, pod)
}
