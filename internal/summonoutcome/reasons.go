package summonoutcome

import (
	"strings"

	"github.com/thaynes43/dev-env/api/v1alpha1"
)

// Every reason in DESIGN-064 6.6, including inputs supplied by later stories.
const (
	ReasonTimedOut            = "timed-out"
	ReasonExitUnknown         = "exit-unknown"
	ReasonAgentExited         = "agent-exited"
	ReasonTurnCap             = "turn-cap"
	ReasonNoUsableModel       = "no-usable-model"
	ReasonAgentFailed         = "agent-failed"
	ReasonAgentInterrupted    = "agent-interrupted"
	ReasonPodTerminated       = "pod-terminated"
	ReasonSessionLost         = "session-lost"
	ReasonNeverStarted        = "never-started"
	ReasonDeadline            = "deadline"
	ReasonHeartbeatLost       = "heartbeat-lost"
	ReasonStale               = "stale"
	ReasonEscalationBound     = "escalation-bound"
	ReasonHoldExpired         = "hold-expired"
	ReasonQuotaHoldExpired    = "quota-hold-expired"
	ReasonSuspendedUnreported = "suspended-unreported"
	// ReasonNotCharged is evidence the operator leader read, not an agent
	// state: the create ledger does not hold the session's dispatch id bound
	// to its name, so it is never admitted (OP777, ruling D5 on item 741).
	ReasonNotCharged = "not-charged"
	// ReasonStopped is a stop a person made: a reap or a suspend (OP779,
	// ruling D1 on item 741). It always closes failed and files nothing.
	ReasonStopped = "stopped"
)

// Inputs contains evidence, never inferred from heartbeat freshness. The
// operator's reconciler fills the agent, pod and stop inputs; the leader's
// watchdog (OP779) fills the deadline, start, loss and hold inputs. Story 791
// fills NoUsableModel from status.agent.start; story 743 QuotaHoldExpired. Every producer uses FirstReason;
// none may introduce a second precedence table.
type Inputs struct {
	TaskResult    *v1alpha1.TaskResultStatus // task sessions only
	AgentStatus   string
	NoUsableModel bool // OP791: status.agent.start names a failure (DESIGN-064 6.9)
	PodTerminated bool
	// A recorded stop, a reap or a suspend (OP779, ruling D1 on item 741).
	// StoppedByPerson is one a person made; Suspended is any other: another
	// session, an account that is not a person, an unattributed change or the
	// idle timer. A stop wins over every row but the two start failures.
	StoppedByPerson, Suspended bool
	// OP741: latch, admission, deadlines, watchdog and queued hold evidence.
	SessionLost, NeverStarted, Deadline, HeartbeatLost    bool
	Stale, EscalationBound, HoldExpired, QuotaHoldExpired bool
	// NotCharged: the leader's uncached ledger read found no charge for the
	// session's dispatch id bound to its name (OP777). Only a session that was
	// never admitted has it, so no agent row can be true beside it.
	NotCharged bool
}

// FirstReason is the single earliest-row-wins ordering of DESIGN-064 6.6,
// with ruling D1's stop rows: a stop recorded before the close wins over every
// row except no-usable-model and agent-failed, which are start failures and
// earlier than any stop.
func FirstReason(in Inputs) string {
	if in.StoppedByPerson || in.Suspended {
		switch {
		case in.NoUsableModel:
			return ReasonNoUsableModel
		case in.AgentStatus == "failed":
			return ReasonAgentFailed
		case in.StoppedByPerson:
			return ReasonStopped
		default:
			return ReasonSuspendedUnreported
		}
	}
	switch {
	case in.TaskResult != nil:
		switch in.TaskResult.State {
		case v1alpha1.TaskResultTimedOut:
			return ReasonTimedOut
		case v1alpha1.TaskResultUnknown:
			return ReasonExitUnknown
		}
		// Current task-result producers do not name a turn cap. Only an explicit
		// result reason may select it; never infer it from NumTurns or a message.
		reason := strings.ToLower(in.TaskResult.Reason)
		if strings.Contains(reason, "turncap") || strings.Contains(reason, "turn-cap") {
			return ReasonTurnCap
		}
		return ReasonAgentExited
	case in.AgentStatus == "exited":
		return ReasonAgentExited
	case in.NoUsableModel:
		return ReasonNoUsableModel
	case in.AgentStatus == "failed":
		return ReasonAgentFailed
	case in.AgentStatus == "interrupted":
		return ReasonAgentInterrupted
	case in.PodTerminated:
		return ReasonPodTerminated
	case in.SessionLost:
		return ReasonSessionLost
	case in.NeverStarted:
		return ReasonNeverStarted
	case in.Deadline:
		return ReasonDeadline
	case in.HeartbeatLost:
		return ReasonHeartbeatLost
	case in.Stale:
		return ReasonStale
	case in.EscalationBound:
		return ReasonEscalationBound
	case in.NotCharged:
		return ReasonNotCharged
	case in.QuotaHoldExpired:
		return ReasonQuotaHoldExpired
	case in.HoldExpired:
		return ReasonHoldExpired
	default:
		return ""
	}
}
