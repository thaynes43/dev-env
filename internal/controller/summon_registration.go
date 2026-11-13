package controller

// The registration step of the leader's watch pass (DESIGN-064 6.10; story
// 794, coordinator rulings O1, O2 and O3 on item 745 comments 7680 and 7685,
// and fix round 1's rulings in PR 1132 comment 6856616130 and item 745
// comment 7855; not rulings by Tom). For an admitted, open remote escalation
// the leader settles once whether Remote Control registered under the
// session's name with a verified link, or why not, by
// summonoutcome.Registration, and records the lifecycle event
// escalation-started in the same status write. Nothing sends it here: the
// keeper's pager is story 796's.

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
// close record, no settled result, and no escalation-started in pages[]. Lane
// upgrade is quiet on success, and a task, an ordinary session and a Queued
// one are never read. A session this pass has just closed carries its close,
// so its terminal page speaks. A recorded escalation-started settles the
// session even without a result (story 794 fix round 1, ruling 2): a CRD
// copy that kept the event but pruned the record would otherwise get a
// second event every pass.
func summonRegistrationDue(s *v1alpha1.AgentSession) bool {
	st := s.Status.Summon
	return s.Spec.Caller != "" && s.Spec.Mode == v1alpha1.ModeRemote && s.Spec.Lane == v1alpha1.LaneEscalation &&
		st != nil && st.AdmittedAt != nil && st.Closed == nil && !summonoutcome.Terminal(s.Status.Outcome) &&
		(st.Registration == nil || st.Registration.Result == "") && summonStartedEvents(st) == 0
}

// summonStartedEvents counts escalation-started in st's pages[].
func summonStartedEvents(st *v1alpha1.SummonStatus) int {
	n := 0
	if st != nil {
		for _, p := range st.Pages {
			if p.Event == v1alpha1.PageEscalationStarted {
				n++
			}
		}
	}
	return n
}

// summonRegistrationNotKept names what the stored object lacks of the
// step's write d at at, or is empty when it carries all of it: agentSeenAt
// for the agent-seen write; the result, its time and one escalation-started
// for a settlement.
func summonRegistrationNotKept(stored *v1alpha1.AgentSession, d summonoutcome.RegistrationDecision, at metav1.Time) string {
	st := stored.Status.Summon
	var reg *v1alpha1.SummonRegistrationStatus
	if st != nil {
		reg = st.Registration
	}
	switch {
	case d.Step == summonoutcome.RegistrationAgentSeen && (reg == nil || reg.AgentSeenAt == nil || !reg.AgentSeenAt.Equal(&at)):
		return "status.summon.registration.agentSeenAt"
	case d.Step != summonoutcome.RegistrationSettled:
		return ""
	case reg == nil || reg.Result != d.Result:
		return "status.summon.registration.result"
	case reg.At == nil || !reg.At.Equal(&at):
		return "status.summon.registration.at"
	case summonStartedEvents(st) != 1:
		return "one escalation-started in status.summon.pages"
	}
	return ""
}

// summonRegistration is one pass's registration step for s, the session as
// the pass last read or wrote it. It writes at most once, under the
// optimistic lock of s, on the pass's own clock: agentSeenAt when the agent
// is first seen, or the result with escalation-started. A conflict or a
// not-found leaves the decision to the next pass, from the stored record.
// A settled session is never written again. The patch's answer is decoded
// into a separate object, as summonoutcome.Write does, and must carry what
// was written (story 794 fix round 1, ruling 2): a CRD copy older than this
// step prunes the record, so the write succeeds and stores nothing. Then the
// step returns an error that names the CRD install, logs nothing as
// recorded, and keeps no change on s.
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
	// The locked patch is computed once, from base, so decoding the answer
	// into stored does not change what is sent.
	patch := client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})
	data, err := patch.Data(s)
	if err != nil {
		*s = *base
		return fmt.Errorf("record the registration of %s: %w", s.Name, err)
	}
	stored := &v1alpha1.AgentSession{ObjectMeta: metav1.ObjectMeta{Name: s.Name, Namespace: s.Namespace}}
	if err := r.Client.Status().Patch(ctx, s, client.RawPatch(patch.Type(), data), client.WithSubResourceBody(stored)); err != nil {
		*s = *base
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("record the registration of %s: %w", s.Name, err)
	}
	if missing := summonRegistrationNotKept(stored, d, at); missing != "" {
		*s = *base
		return fmt.Errorf("the registration patch of %s did not keep %s; install the current AgentSession CRD before the operator", s.Name, missing)
	}
	*s = *stored
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
