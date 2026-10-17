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

// Restarter runs agentd's prepare-restart in a session pod (DESIGN-001 5.2).
// An error means it did not run. A report with no conversation id is not an
// error; drain refuses that report and keeps the pod.
type Restarter interface {
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
// holdIdle means the idle-suspend timer waits: this session is on the drain
// path, so it is rolled forward instead of parked. The 72h session message and
// the 24h owner line are not sent; no notifier in this process sends them, and
// drain does not wait on that notice.
//
// The phase is written by observe before prepare-restart runs, so a fresh
// operator sees Draining and does not stop the agent on a session it has not
// marked. prepare-restart runs before deletePod. A failure or a missing
// conversation id sets RemovalBlocked and keeps the pod.
func (r *Reconciler) drainIdle(ctx context.Context, s *v1alpha1.AgentSession, t *templates.Templates, obs *observation) (holdIdle bool, res ctrl.Result, stop bool, err error) {
	if t == nil || obs == nil || obs.pod.missing || obs.pod.foreign != "" || !drainEligible(s, obs.pod.obj, t.Revision()) {
		return false, ctrl.Result{}, false, nil
	}
	pod := obs.pod.obj
	// observe writes Draining at the end of the reconcile that first notices
	// the idle outdated pod. Stop the agent only after that mark is stored.
	if s.Status.Phase != v1alpha1.PhaseDraining {
		return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
	}
	if !restartProofFor(s, pod) {
		if r.APIReader == nil {
			obs.removalBlocked = fmt.Errorf("%w: %s", errDrainNeedsRestart, "no uncached reader")
			return true, ctrl.Result{RequeueAfter: drainRetry}, false, nil
		}
		if behind, err := r.cacheBehind(ctx, s, pod); err != nil || behind {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
		}
		rep, prepErr := r.prepareRestart(ctx, pod)
		if prepErr != nil || !restartReportProves(s, rep) {
			why := prepErr
			if why == nil {
				why = errors.New("prepare-restart recorded no conversation identity")
			}
			obs.removalBlocked = fmt.Errorf("%w: %s", errDrainNeedsRestart, why.Error())
			return true, ctrl.Result{RequeueAfter: drainRetry}, false, nil
		}
		rec := &v1alpha1.RestartStatus{ConversationID: rep.ConversationID, PodUID: string(pod.UID), Generation: s.Generation}
		written, err := r.recordRestart(ctx, s, rec)
		if err != nil || !written {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, err
		}
		var cur corev1.Pod
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(pod), &cur); err != nil {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, client.IgnoreNotFound(err)
		}
		pod = &cur
	}
	if err := drainPod(ctx, r.Client, s, pod); err != nil {
		if apierrors.IsConflict(err) {
			return true, ctrl.Result{RequeueAfter: time.Second}, true, nil
		}
		obs.removalBlocked = err
		return true, ctrl.Result{RequeueAfter: drainRetry}, false, nil
	}
	log.FromContext(ctx).Info("drained an idle outdated session pod", "pod", pod.Name, "uid", pod.UID, "revision", pod.Labels[v1alpha1.LabelRevision])
	r.event(s, corev1.EventTypeNormal, "Drain", "IdleDrain", "deleted the outdated pod after prepare-restart recorded the conversation (DESIGN-001 5.2)")
	return true, ctrl.Result{RequeueAfter: time.Second}, false, nil
}

// restartReportProves is the prepare-restart report drain accepts: this
// session, a resumable conversation id the status field can store.
func restartReportProves(s *v1alpha1.AgentSession, rep protocol.RestartReport) bool {
	return s != nil && rep.Resumable && rep.Session == s.Name && rep.ConversationID != "" && len(rep.ConversationID) <= 256
}

// prepareRestart calls the configured restarter, or the rescuer when that
// value implements Restarter. ExecRescuer does, so the operator process picks
// the command up without a second wiring.
func (r *Reconciler) prepareRestart(ctx context.Context, pod *corev1.Pod) (protocol.RestartReport, error) {
	var restarter Restarter
	if r.Restarter != nil {
		restarter = r.Restarter
	} else if existing, ok := r.Rescuer.(Restarter); ok {
		restarter = existing
	}
	if restarter == nil {
		return protocol.RestartReport{}, errors.New("prepare-restart is not available")
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
