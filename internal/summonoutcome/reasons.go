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
)

// Inputs contains evidence, never inferred from heartbeat freshness. Only the
// five existing inputs are populated today. Future producers fill these fields
// and use FirstReason; they must not introduce a second precedence table.
type Inputs struct {
	TaskResult    *v1alpha1.TaskResultStatus // task sessions only
	AgentStatus   string
	NoUsableModel bool // OP743: the durable no-usable-model start evidence
	PodTerminated bool
	// Operator stops skip only rows 5 and 6. OP741 defines what the stop becomes.
	SessionDeleting, SessionSuspended bool
	// OP741: latch, admission, deadlines, watchdog, queued hold and suspend evidence.
	SessionLost, NeverStarted, Deadline, HeartbeatLost               bool
	Stale, EscalationBound, HoldExpired, QuotaHoldExpired, Suspended bool
}

// FirstReason is the single earliest-row-wins ordering of DESIGN-064 6.6.
func FirstReason(in Inputs) string {
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
	case in.AgentStatus == "interrupted" && !in.SessionDeleting && !in.SessionSuspended:
		return ReasonAgentInterrupted
	case in.PodTerminated && !in.SessionDeleting && !in.SessionSuspended:
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
	case in.QuotaHoldExpired:
		return ReasonQuotaHoldExpired
	case in.HoldExpired:
		return ReasonHoldExpired
	case in.Suspended:
		return ReasonSuspendedUnreported
	default:
		return ""
	}
}
