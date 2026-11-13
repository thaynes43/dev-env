package controller

// The registration step of the leader's watch pass (DESIGN-064 6.10; story
// 794, coordinator rulings O1, O2 and O3 on item 745 comments 7680 and 7685;
// not rulings by Tom). For an admitted, open remote escalation the leader
// settles once whether Remote Control registered under the session's name
// with a verified link, or why not, by summonoutcome.Registration, and
// records the lifecycle event escalation-started in the same status write.
// Nothing sends it here: the keeper's pager is story 796's.

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/thaynes43/dev-env/api/v1alpha1"
	"github.com/thaynes43/dev-env/internal/summonoutcome"
)

// summonRegistrationDue reports whether the step reads s at all: spec.caller
// set, mode remote, lane escalation, admitted, no terminal outcome, no
// close record, and no settled result. Lane upgrade is quiet on success, and
// a task, an ordinary session and a Queued one are never read. A session
// this pass has just closed carries its close, so its terminal page speaks.
func summonRegistrationDue(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return s.Spec.Caller != "" && s.Spec.Mode == v1alpha1.ModeRemote && s.Spec.Lane == v1alpha1.LaneEscalation &&
		st != nil && st.AdmittedAt != nil && st.Closed == nil && !summonoutcome.Terminal(s.Status.Outcome) &&
		(st.Registration == nil || st.Registration.Result == "")
}

// summonRegistration is one pass's registration step for s, the session as
// the pass last read or wrote it. It writes at most once, under the
// optimistic lock of s, on the pass's own clock: agentSeenAt when the agent
// is first seen, or the result with escalation-started. A conflict or a
// not-found leaves the decision to the next pass, from the stored record.
// A settled session is never written again.
func (r *Reconciler) summonRegistration(ctx context.Context, s *v1alpha1.AgentSession) error {
	if !summonRegistrationDue(s) {
		return nil
	}
	now := r.now()
	d := summonoutcome.Registration(s.Name, s.Status.RemoteControl, s.Status.Agent, s.Status.Summon, now)
	if d.Step == "" {
		return nil
	}
	if d.Step == summonoutcome.RegistrationSettled && len(s.Status.Summon.Pages) >= summonMaxPages {
		// As summonRecordPage: no room, so neither the event nor the result.
		log.FromContext(ctx).Error(nil, "summoned session has no room for a page event", "session", s.Name, "event", v1alpha1.PageEscalationStarted)
		return nil
	}
	base := s.DeepCopy()
	at := metav1.NewTime(now.UTC().Truncate(time.Second))
	st := s.Status.Summon
	if st.Registration == nil {
		st.Registration = &v1alpha1.SummonRegistrationStatus{}
	}
	switch d.Step {
	case summonoutcome.RegistrationAgentSeen:
		st.Registration.AgentSeenAt = &at
	case summonoutcome.RegistrationSettled:
		// agentSeenAt is never invented here: a session that settles before
		// its agent was seen keeps it unset.
		st.Registration.Result, st.Registration.At = d.Result, &at
		if d.Result == v1alpha1.RegistrationUnavailable {
			st.Registration.Reason = d.Reason
		}
		st.Pages = append(st.Pages, v1alpha1.SummonPage{Event: v1alpha1.PageEscalationStarted, At: at})
	default:
		*s = *base
		return nil
	}
	if err := r.Client.Status().Patch(ctx, s, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		*s = *base
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("record the registration of %s: %w", s.Name, err)
	}
	// Words only: never the URL or any other text from the status.
	l := log.FromContext(ctx)
	if d.Step == summonoutcome.RegistrationAgentSeen {
		l.Info("recorded that a summoned escalation's agent was seen", "session", s.Name, "caller", s.Spec.Caller, "lane", s.Spec.Lane)
		return nil
	}
	l.Info("recorded a summoned escalation's registration", "session", s.Name, "caller", s.Spec.Caller, "lane", s.Spec.Lane,
		"result", d.Result, "reason", d.Reason, "event", v1alpha1.PageEscalationStarted)
	return nil
}
