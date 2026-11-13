package summonoutcome

// A remote escalation's registration, settled once by the operator leader
// (DESIGN-064 6.10; story 794, coordinator rulings O1, O2, O3 and O13 on item
// 745 comments 7680 and 7685; not rulings by Tom). Registration is the one
// pure function; the leader's watch pass (internal/controller) writes what it
// answers. The observation it reads is the pod's own claim, copied by the
// heartbeat route: agentd's reading of the CLI's record for its live
// process, not an attestation by the provider.

import (
	"regexp"
	"time"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// RemoteControlLinkPattern is the one verified Remote Control link (ruling
// O13): the URL agentd writes for a registered session. In story 794 only
// Registration uses it; agentd and the keeper move to it in stories 795 and
// 796.
const RemoteControlLinkPattern = `^https://claude\.ai/code/session_[A-Za-z0-9_-]{1,120}$`

var remoteControlLink = regexp.MustCompile(RemoteControlLinkPattern)

// VerifiedRemoteControlLink reports whether url is a verified Remote Control
// link. Nothing else is one.
func VerifiedRemoteControlLink(url string) bool {
	return remoteControlLink.MatchString(url)
}

// agentd's words (internal/agentd/protocol), as the heartbeat copies them to
// status.agent.status and status.remoteControl.state. The test holds them to
// agentd's own constants.
const (
	agentBusy, agentIdle, agentWaiting                                   = "busy", "idle", "waiting"
	remoteRegistered, remoteStarting, remoteOffline, remoteLoginRequired = "registered", "starting", "offline", "login-required"
)

// The bounds of rule 2 (coordinator rulings, item 745 comment 7685).
const (
	// RegistrationHeartbeatFresh: every settlement reads a heartbeat at most
	// this old (rule a).
	RegistrationHeartbeatFresh = 3 * time.Minute
	// RegistrationAfterAgentSeen: unavailable this long after the agent was
	// seen started without registering (rule d).
	RegistrationAfterAgentSeen = 5 * time.Minute
	// RegistrationAfterAdmission: unavailable this long after admission when
	// the agent was never seen (rule e).
	RegistrationAfterAdmission = 10 * time.Minute
)

// RegistrationStep is what one pass does: nothing yet (empty), record that
// the agent was seen, or settle.
type RegistrationStep string

const (
	// RegistrationAgentSeen: record the pass's clock as agentSeenAt.
	RegistrationAgentSeen RegistrationStep = "agent-seen"
	// RegistrationSettled: record Result (and Reason for unavailable) with
	// the escalation-started page event, in one write.
	RegistrationSettled RegistrationStep = "settled"
)

// RegistrationDecision is Registration's answer. The zero value is nothing
// yet.
type RegistrationDecision struct {
	Step   RegistrationStep
	Result v1alpha1.SummonRegistrationResult
	Reason v1alpha1.SummonRegistrationReason
}

// Registration decides one pass for an admitted, open remote escalation
// named name, from its status.remoteControl, status.agent and status.summon,
// at now. It has no clock and no client. In order:
//
//	(a) no heartbeat, or one more than 3 minutes before now: nothing yet;
//	(b) Remote Control state registered, a verified link and the session's
//	    name: registered, whether or not the agent was seen;
//	(c) agentSeenAt unset and the agent busy, idle or waiting: agent seen;
//	(d) agentSeenAt set and now at or after it plus 5 minutes: unavailable;
//	(e) agentSeenAt unset, (c) did not apply, and now at or after
//	    admittedAt plus 10 minutes: unavailable;
//	(f) otherwise nothing yet.
//
// A record that already has a result, or a session not admitted, is nothing,
// whatever the inputs: a settled session is never settled again.
func Registration(name string, rc *v1alpha1.RemoteControlStatus, agent *v1alpha1.AgentStatus, st *v1alpha1.SummonStatus, now time.Time) RegistrationDecision {
	if st == nil || st.AdmittedAt == nil {
		return RegistrationDecision{}
	}
	reg := st.Registration
	if reg != nil && reg.Result != "" {
		return RegistrationDecision{}
	}
	// (a) A fresh heartbeat or nothing.
	if agent == nil || agent.LastHeartbeat == nil || agent.LastHeartbeat.Time.Before(now.Add(-RegistrationHeartbeatFresh)) {
		return RegistrationDecision{}
	}
	// (b) Registered.
	if rc != nil && rc.State == remoteRegistered && VerifiedRemoteControlLink(rc.URL) && rc.Name == name {
		return RegistrationDecision{Step: RegistrationSettled, Result: v1alpha1.RegistrationRegistered}
	}
	seen := reg != nil && reg.AgentSeenAt != nil
	// (c) Agent seen.
	if !seen {
		switch agent.Status {
		case agentBusy, agentIdle, agentWaiting:
			return RegistrationDecision{Step: RegistrationAgentSeen}
		}
	}
	// (d) and (e) The two bounds.
	bound := st.AdmittedAt.Add(RegistrationAfterAdmission)
	if seen {
		bound = reg.AgentSeenAt.Add(RegistrationAfterAgentSeen)
	}
	if now.Before(bound) {
		return RegistrationDecision{}
	}
	return RegistrationDecision{Step: RegistrationSettled, Result: v1alpha1.RegistrationUnavailable, Reason: registrationReason(name, rc)}
}

// registrationReason is the closed reason word of an unavailable settlement,
// read at that pass. No other text from the status is copied.
func registrationReason(name string, rc *v1alpha1.RemoteControlStatus) v1alpha1.SummonRegistrationReason {
	if rc == nil {
		return v1alpha1.RegistrationReasonNotReported
	}
	switch rc.State {
	case remoteRegistered:
		if rc.Name != name {
			return v1alpha1.RegistrationReasonNameMismatch
		}
		return v1alpha1.RegistrationReasonLinkUnverified
	case remoteStarting:
		return v1alpha1.RegistrationReasonStarting
	case remoteOffline:
		return v1alpha1.RegistrationReasonOffline
	case remoteLoginRequired:
		return v1alpha1.RegistrationReasonLoginRequired
	default:
		return v1alpha1.RegistrationReasonNotReported
	}
}
