package protocol

import (
	"net/url"
	"time"
)

// HeartbeatPath is the API route agentd posts its status to (D-41), with the
// pod's projected ServiceAccount token. The operator checks that the token's
// bound pod belongs to the session named in the path.
func HeartbeatPath(session string) string {
	return "/v1/sessions/" + url.PathEscape(session) + "/heartbeat"
}

// Boot phases: agentd's own view of its boot (DESIGN-001 3.6).
const (
	BootBooting = "booting"
	BootReady   = "ready"
	BootFailed  = "failed"
)

// Agent states. Claude's interactive states (busy, idle, waiting) arrive with
// plan 02's idle detection; a task, or a TUI (D-58), is busy while it runs.
const (
	// AgentPending: the boot has not started the agent yet.
	AgentPending = "pending"
	// AgentBusy: the agent process is running, and for a TUI, Claude says it
	// is working (D-59).
	AgentBusy = "busy"
	// AgentIdle: a TUI whose Claude waits for its next prompt (D-59).
	AgentIdle = "idle"
	// AgentWaiting: a TUI whose Claude waits on a person, such as a question
	// it asked (D-59). It is not working either.
	AgentWaiting = "waiting"
	// AgentExited: the agent finished; Task holds how.
	AgentExited = "exited"
	// AgentFailed: agentd could not start the agent; Error says why.
	AgentFailed = "failed"
	// AgentInterrupted: the agent was started and its process is gone without
	// a result. agentd never runs a task's prompt twice (D-42); the next boot
	// resumes the conversation in the TUI (D-58).
	AgentInterrupted = "interrupted"
)

// Step is the outcome of one boot step. Steps never stop the boot: a failed
// one is reported and the next one runs.
type Step struct {
	Name string `json:"name"`
	// State is ok, warn (done with a problem worth reading), skip (nothing to
	// do here) or fail.
	State string   `json:"state"`
	Notes []string `json:"notes,omitempty"`
}

// Status is what agentd reports, in each heartbeat and from
// `agentd ctl status`. The operator copies it into the AgentSession's status.
type Status struct {
	Session string `json:"session"`
	// Agentd is agentd's version line.
	Agentd   string    `json:"agentd"`
	BootID   string    `json:"bootId"`
	BootedAt time.Time `json:"bootedAt"`
	Boot     string    `json:"boot"`
	// Problems are the boot steps that warned or failed.
	Problems   []Step     `json:"problems,omitempty"`
	Workspace  *Workspace `json:"workspace,omitempty"`
	Agent      AgentState `json:"agent"`
	Usage      *Usage     `json:"usage,omitempty"`
	ObservedAt time.Time  `json:"observedAt"`
}

// Workspace is the session's clone and worktree (D-15).
type Workspace struct {
	Clone    string `json:"clone"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch,omitempty"`
	Head     string `json:"head,omitempty"`
}

// AgentState is the agent process's state.
type AgentState struct {
	State string `json:"state"`
	// ConversationID is the agent's session id (claude --session-id), the
	// handle a later resume uses.
	ConversationID string     `json:"conversationId,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	// LastActivity is the newest sign of activity (D-59): the task's log, the
	// last change of Claude's own status, an attached client (now), or a
	// change in the worktree. The operator's idle timers read it.
	LastActivity *time.Time `json:"lastActivity,omitempty"`
	// Attached is how many clients are attached to the agent's tmux session.
	Attached int         `json:"attached,omitempty"`
	Task     *TaskResult `json:"task,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// TaskResult is how a task ended.
type TaskResult struct {
	ExitCode   int       `json:"exitCode"`
	FinishedAt time.Time `json:"finishedAt"`
	// TimedOut is set when agentd stopped the task at limits.timeout.
	TimedOut bool `json:"timedOut,omitempty"`
	// Subtype and IsError come from the CLI's result event: success,
	// error_max_turns, error_during_execution.
	Subtype  string `json:"subtype,omitempty"`
	IsError  bool   `json:"isError,omitempty"`
	NumTurns int    `json:"numTurns,omitempty"`
}

// Usage is the cost record of V-16, from the CLI's result event.
type Usage struct {
	CostUSD                  float64 `json:"costUSD"`
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens,omitempty"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens,omitempty"`
}

// RestartReport is what `agentd ctl prepare-restart` prints (DESIGN-001 5.2
// step 2, D-58): what the next boot on the volume resumes, and what stopping
// the agent did. The conversation is on the volume (~/.agentd/launch.json), so
// a new pod on the same volume resumes it.
type RestartReport struct {
	Session string `json:"session"`
	// ConversationID is the conversation the next boot resumes; empty when
	// the agent was never launched, so the next boot starts it fresh.
	ConversationID string `json:"conversationId,omitempty"`
	// Resumable: the next boot resumes ConversationID in the TUI.
	Resumable bool `json:"resumable"`
	// Agent is what the stop did to the agent CLI.
	Agent *AgentStop `json:"agent,omitempty"`
}
