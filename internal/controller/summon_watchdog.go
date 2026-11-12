package controller

// The leader's watchdog, stops and the operator's own suspend for summoned
// sessions (DESIGN-064 6.6 and 6.7, OP779, slice c of item 741):
//
//   - The watchdog (summonWatch) runs in the leader's admission pass, every
//     SummonAdmissionEvery, on the reconciler's clock (Reconciler.Now). For an
//     admitted session whose outcome is open it adds the latch's loss and the
//     time bounds to summonoutcome.Inputs (summonWatchdogInputs) and closes
//     through closeSummon, the reconciler's own close step: one ordering
//     (summonoutcome.FirstReason), one terminal write. A Queued session's
//     hold bound is judged in the pass's queued loop, after the grants are
//     known (summonHoldExpired).
//   - A stop (summonStop, ruling D1 on item 741): a reap or a suspend recorded
//     before the close. A person's stop closes failed, reason stopped; any
//     other closes by onUnreported, reason suspended-unreported.
//   - The operator's own suspend (suspendClosedSummon, item 779 comment 7377
//     and ruling D3): an operator close of an admitted session whose release
//     waits for its pod suspends the session with SummonCloseSuspender, so
//     its pod goes. A remote session's pod is rescued and deleted as on any
//     suspend; a task's recorded executor is asked to end by the private
//     executor stop (summonStopDue, DESIGN-025 4), and release then accepts
//     its terminal phase with its terminal record (coordinator answer
//     op779-q-1, option A). The volume is kept; nothing is deleted.
//
// Every branch keys on spec.caller, so an ordinary session is unchanged.

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

const (
	// SummonCloseSuspender is the value of AnnotationSuspendedBy when the
	// operator suspended a closed summoned session so its pod goes and its
	// lane can be released (OP779). It is written only after the close.
	SummonCloseSuspender = "summon-close"

	// summonStartGrace is the minute past the start deadline before a session
	// that never reported is never-started (DESIGN-064 6.6).
	summonStartGrace = time.Minute
	// summonDeadlineGrace is the 10 minutes after the start allowance and the
	// task's timeout before the task deadline trips (DESIGN-064 6.6, N7).
	summonDeadlineGrace = 10 * time.Minute
	// summonHeartbeatLost is how long a remote session's pod may exist with no
	// heartbeat (DESIGN-064 6.6, this design's default).
	summonHeartbeatLost = 10 * time.Minute
	// Give agentd time to report again after an outage of all API replicas.
	summonHeartbeatRecovery = 10 * time.Minute
	// summonEscalationReminder and summonEscalationBound are a remote
	// escalation's idle bounds (DESIGN-064 6.7).
	summonEscalationReminder = 24 * time.Hour
	summonEscalationBound    = 72 * time.Hour
	// summonEscalationHeld is how long an escalation may be Queued before its
	// one held event (DESIGN-064 6.7).
	summonEscalationHeld = 15 * time.Minute

	// summonMaxPages is status.summon.pages' MaxItems (DESIGN-064 6.8).
	summonMaxPages = 8

	// humanCallerPrefix starts the API's name for a person (the API's
	// caller.String for kindHuman, "human/<namespace>/<name>").
	humanCallerPrefix = "human/"
)

// summonStop reports the stop recorded on a Session (ruling D1): byPerson
// when a person reaped or suspended it through the API, other for any other
// stop: another session, an account that is not a person, a change made past
// the API (no attribution) or the idle timer. The API derives caller names;
// the operator also writes its own non-person suspend markers. Privileged
// Kubernetes writers are outside that API attribution boundary.
func summonStop(s *v1alpha1.AgentSession) (byPerson, other bool) {
	deleting := !s.DeletionTimestamp.IsZero()
	reapedBy, recorded := s.Annotations[v1alpha1.AnnotationReapedBy]
	suspended := s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended
	if !recorded && !deleting && !suspended {
		return false, false
	}
	person := recorded && strings.HasPrefix(reapedBy, humanCallerPrefix) ||
		suspended && strings.HasPrefix(s.Annotations[v1alpha1.AnnotationSuspendedBy], humanCallerPrefix)
	return person, !person
}

// reconcileSummonReap completes the API's recorded stop only after its close.
// The delete is bound to that exact Session and terminal status snapshot.
// A racing write is reconsidered on the next reconcile; finalizer cleanup
// remains on the existing deletion path. No marker means no new delete.
func (r *Reconciler) reconcileSummonReap(ctx context.Context, s *v1alpha1.AgentSession) (bool, error) {
	if s.Spec.Caller == "" || !s.DeletionTimestamp.IsZero() || !summonoutcome.Terminal(s.Status.Outcome) {
		return false, nil
	}
	if _, recorded := s.Annotations[v1alpha1.AnnotationReapedBy]; !recorded {
		return false, nil
	}
	err := r.Client.Delete(ctx, s, client.Preconditions{UID: &s.UID, ResourceVersion: &s.ResourceVersion},
		client.PropagationPolicy(metav1.DeletePropagationBackground))
	if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
		return true, nil
	}
	return true, err
}

// summonTaskDeadline is the operator's task deadline (DESIGN-064 6.6, N7):
// admittedAt plus the start allowance, the task's timeout and 10 minutes.
func summonTaskDeadline(admittedAt time.Time, timeout time.Duration) time.Time {
	return admittedAt.Add(SummonStartWindow + timeout + summonDeadlineGrace)
}

// summonIdleSince is when a remote session last did something: its agent's
// lastActivity, never earlier than its admission.
func summonIdleSince(s *v1alpha1.AgentSession) time.Time {
	since := s.Status.Summon.AdmittedAt.Time
	if a := s.Status.Agent; a != nil && a.LastActivity != nil && a.LastActivity.After(since) {
		since = a.LastActivity.Time
	}
	return since
}

// summonWatchdogInputs adds DESIGN-064 6.7's watchdog rows for an admitted,
// open session, by the leader's clock, from durable records and uncached
// reads. It never infers anything from a heartbeat's freshness except the
// heartbeat-lost row itself, and only while the pod exists.
func (r *Reconciler) summonWatchdogInputs(ctx context.Context, s *v1alpha1.AgentSession, in *summonoutcome.Inputs) error {
	st := s.Status.Summon
	if st == nil || st.AdmittedAt == nil {
		return nil // Queued: the pass's queued loop judges its hold bound.
	}
	now := r.now()
	admitted := st.AdmittedAt.Time
	a := s.Status.Agent
	// never-started (ruling D10): agentd never reported, so no row above it
	// can hold. When agentd itself refuses the start it reports failed and
	// agent-failed wins. A heartbeat of Busy or Interrupted before any
	// provider spawn (agent 2.105.0) is a report: it is not "the agent ran",
	// and the deadline still bounds it.
	pending := s.Status.Outcome == nil || s.Status.Outcome.State == v1alpha1.OutcomePending
	reported := a != nil && a.LastHeartbeat != nil
	heartbeatsReady := !r.summonWatchdogStartedAt.IsZero() && !now.Before(r.summonWatchdogStartedAt.Add(summonHeartbeatRecovery))
	in.NeverStarted = heartbeatsReady && pending && !reported && !now.Before(admitted.Add(SummonStartWindow+summonStartGrace))
	switch s.Spec.Mode {
	case v1alpha1.ModeTask:
		if l := s.Spec.Limits; l != nil && l.Timeout != nil {
			in.Deadline = !now.Before(summonTaskDeadline(admitted, l.Timeout.Duration))
		}
		if h := s.Status.PrivateHistory; h != nil && h.LastExecutor != nil && h.LastExecutor.PodUID != "" && h.LastExecutor.PodName != "" {
			// session-lost: the latch's recorded executor is gone, or another
			// pod holds its name, by an uncached read. A stop recorded on the
			// Session wins over it (FirstReason).
			var pod corev1.Pod
			err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: h.LastExecutor.PodName}, &pod)
			switch {
			case apierrors.IsNotFound(err):
				in.SessionLost = true
			case err != nil:
				return fmt.Errorf("read the recorded executor of %s: %w", s.Name, err)
			default:
				in.SessionLost = string(pod.UID) != h.LastExecutor.PodUID
			}
		}
	case v1alpha1.ModeRemote:
		if heartbeatsReady && a != nil && a.LastHeartbeat != nil && !now.Before(a.LastHeartbeat.Add(summonHeartbeatLost)) {
			pod, err := getOwned(ctx, r.APIReader, s, s.Name, &corev1.Pod{})
			if err != nil {
				return err
			}
			in.HeartbeatLost = !pod.missing && pod.foreign == "" && !isHoldPod(pod.obj)
		}
		idle := summonIdleSince(s)
		switch s.Spec.Lane {
		case v1alpha1.LaneUpgrade:
			if d, err := time.ParseDuration(s.Spec.Summon.StaleAfter); err == nil && d > 0 {
				// No agent activity and no working report for staleAfter.
				last := idle
				if o := s.Status.Outcome; o != nil && o.At != nil && o.At.After(last) {
					last = o.At.Time
				}
				in.Stale = !now.Before(last.Add(d))
			}
		case v1alpha1.LaneEscalation:
			in.EscalationBound = !now.Before(idle.Add(summonEscalationBound))
		}
	}
	return nil
}

// summonWatch is the watchdog's step of one admission pass: every admitted,
// open summoned session the pass listed goes through closeSummon with the
// watchdog's inputs. An error for one session is collected and the others
// still run. A session without spec.caller is never read.
func (r *Reconciler) summonWatch(ctx context.Context, items []v1alpha1.AgentSession) []error {
	var errs []error
	for i := range items {
		s := items[i].DeepCopy()
		if st := s.Status.Summon; s.Spec.Caller == "" || st == nil || st.AdmittedAt == nil || summonoutcome.Terminal(s.Status.Outcome) {
			continue
		}
		if _, err := r.closeSummon(ctx, s, true); err != nil {
			errs = append(errs, fmt.Errorf("watch %s: %w", s.Name, err))
			continue
		}
		if err := r.summonReminder(ctx, s); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// summonHoldExpired reports whether a Queued session has waited past its
// entry's maxHold since it was created (DESIGN-064 6.7, C2). Upgrades and
// escalations have no maxHold and never expire.
func summonHoldExpired(s *v1alpha1.AgentSession, now time.Time) bool {
	if s.Spec.Summon == nil || s.Spec.Summon.MaxHold == "" {
		return false
	}
	d, err := time.ParseDuration(s.Spec.Summon.MaxHold)
	return err == nil && d > 0 && !now.Before(s.CreationTimestamp.Add(d))
}

// summonStopDue reports whether a summoned task's recorded executor is asked
// to end: the session is admitted, closed, not released and suspended, by
// the operator's own suspend after its close or by anyone. Its work is over
// and nothing resumes it (N3), so its executor is stopped as a reaped one's
// is (DESIGN-025 4): the kubelet ends the pod, and the pod object and the
// volume stay (coordinator answer op779-q-1, option A).
func summonStopDue(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return s.Spec.Caller != "" && s.Spec.Mode == v1alpha1.ModeTask && st != nil && st.AdmittedAt != nil &&
		st.Closed != nil && !st.Closed.Released && s.Spec.OperatingMode == v1alpha1.OperatingModeSuspended && s.DeletionTimestamp.IsZero()
}

// suspendClosedSummon is the operator's own suspend of a closed, admitted
// summoned session whose release waits for its pod (item 779 comment 7377,
// ruling D3). It writes spec.operatingMode Suspended and suspended-by
// SummonCloseSuspender under the optimistic lock of the uncached read, as the
// idle timer does; the usual suspend path then removes the pod or, for a
// task, ends it. A session already suspended or being reaped is left alone.
func (r *Reconciler) suspendClosedSummon(ctx context.Context, live *v1alpha1.AgentSession) (bool, error) {
	st := live.Status.Summon
	if live.Spec.Caller == "" || st == nil || st.AdmittedAt == nil || st.Closed == nil || st.Closed.Released ||
		live.Spec.OperatingMode == v1alpha1.OperatingModeSuspended || !live.DeletionTimestamp.IsZero() {
		return false, nil
	}
	base := live.DeepCopy()
	live.Spec.OperatingMode = v1alpha1.OperatingModeSuspended
	if live.Annotations == nil {
		live.Annotations = map[string]string{}
	}
	live.Annotations[v1alpha1.AnnotationSuspendedBy] = SummonCloseSuspender
	if err := r.Client.Patch(ctx, live, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("suspend closed summoned session %s: %w", live.Name, err)
	}
	log.FromContext(ctx).Info("suspended a closed summoned session so its pod goes and its lane can be released",
		"session", live.Name, "caller", live.Spec.Caller, "lane", live.Spec.Lane, "reason", st.Closed.Reason)
	r.event(live, corev1.EventTypeNormal, "Suspend", "SummonClose", "closed "+st.Closed.Reason+"; suspended so its pod goes and its lane is released (DESIGN-064 5.9)")
	return true, nil
}

// summonReminder records a remote escalation's one reminder page event once
// it has been idle 24 hours (DESIGN-064 6.7). It is a lifecycle event, never
// a close; the 72-hour bound closes it (escalation-bound).
func (r *Reconciler) summonReminder(ctx context.Context, s *v1alpha1.AgentSession) error {
	st := s.Status.Summon
	if s.Spec.Mode != v1alpha1.ModeRemote || s.Spec.Lane != v1alpha1.LaneEscalation || st == nil || st.AdmittedAt == nil ||
		summonoutcome.Terminal(s.Status.Outcome) || r.now().Before(summonIdleSince(s).Add(summonEscalationReminder)) {
		return nil
	}
	return r.summonRecordPage(ctx, s, v1alpha1.PageEscalationReminder)
}

// summonHeldPage records a Queued escalation's one held page event once it
// has waited 15 minutes (DESIGN-064 6.7). The pass calls it only for a
// session it holds.
func (r *Reconciler) summonHeldPage(ctx context.Context, s *v1alpha1.AgentSession) error {
	if s.Spec.Lane != v1alpha1.LaneEscalation || r.now().Before(s.CreationTimestamp.Add(summonEscalationHeld)) {
		return nil
	}
	return r.summonRecordPage(ctx, s, v1alpha1.PageEscalationHeld)
}

// summonRecordPage appends a page event to status.summon.pages once, under
// the optimistic lock of s (ruling D9). A conflict leaves it to the next
// pass; an event already recorded is never recorded again, so a session gets
// at most one of each.
func (r *Reconciler) summonRecordPage(ctx context.Context, s *v1alpha1.AgentSession, event v1alpha1.SummonPageEvent) error {
	if st := s.Status.Summon; st != nil {
		for _, p := range st.Pages {
			if p.Event == event {
				return nil
			}
		}
		if len(st.Pages) >= summonMaxPages {
			log.FromContext(ctx).Error(nil, "summoned session has no room for a page event", "session", s.Name, "event", event)
			return nil
		}
	}
	base := s.DeepCopy()
	if s.Status.Summon == nil {
		s.Status.Summon = &v1alpha1.SummonStatus{}
	}
	s.Status.Summon.Pages = append(s.Status.Summon.Pages, v1alpha1.SummonPage{Event: event, At: metav1.NewTime(r.now().UTC().Truncate(time.Second))})
	if err := r.Client.Status().Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		*s = *base
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("record page event %s on %s: %w", event, s.Name, err)
	}
	log.FromContext(ctx).Info("recorded a summoned session's page event", "session", s.Name, "caller", s.Spec.Caller, "lane", s.Spec.Lane, "event", event)
	return nil
}
