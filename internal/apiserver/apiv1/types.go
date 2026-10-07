// Package apiv1 holds the wire types of the operator's /v1 API (DESIGN-001 3.4,
// D-46): the requests and responses of /v1/sessions and /v1/fleet, and the error
// document every refusal carries. agentd's heartbeat body is
// protocol.Status (internal/agentd/protocol, D-41).
//
// It depends on the standard library only, so agent-run (plan 01 step 7), which
// is one static binary (D-06), imports it without the Kubernetes libraries.
package apiv1

import "time"

// Paths. A session's own routes hang off SessionPath.
const (
	SessionsPath = "/v1/sessions"
	FleetPath    = "/v1/fleet"
)

// SessionPath is the route of one session.
func SessionPath(name string) string { return SessionsPath + "/" + name }

// TokenAudience is the audience of every token the API accepts (D-05). A token
// for the API server's own audience is refused.
const TokenAudience = "dev-env-operator"

// CreateSessionRequest is the body of POST /v1/sessions. Field names are the
// AgentSession spec's (DESIGN-001 3.3). Unknown fields are refused.
//
// The caller never names the session's parent: the API sets it from the token
// (D-05). Plan 01 creates task sessions of agent claude only; the other modes and
// agents arrive with plans 02, 03, 04 and 09, summoning (name, lane) with plan 10
// and tool pools with plan 08.
type CreateSessionRequest struct {
	// Repo is the repository name under the GitHub owner, for example haynes-ops.
	Repo string `json:"repo"`
	// Base is the ref the worktree branches from. Empty means origin/main.
	Base string `json:"base,omitempty"`
	// Agent is the agent CLI: claude.
	Agent string `json:"agent"`
	// Mode is task.
	Mode string `json:"mode"`
	// Model is a full model id, never an alias, for example claude-opus-5-5.
	Model string `json:"model"`
	// Effort is a level the model takes (low, medium, high, xhigh, max, or
	// ultracode where the model takes xhigh). Empty leaves the CLI's default.
	Effort string `json:"effort,omitempty"`
	// Prompt is the task, at most 64 KiB.
	Prompt string `json:"prompt,omitempty"`
	// Size is S, M or L. Empty means M.
	Size string `json:"size,omitempty"`
	// Profile is a profile in dev-env-templates. Empty means the templates'
	// default; a session's child runs on its parent's profile.
	Profile string `json:"profile,omitempty"`
	// Tools are tool pools (plan 08). Plan 01 refuses a non-empty list.
	Tools []string `json:"tools,omitempty"`
	// Limits caps the task.
	Limits *Limits `json:"limits,omitempty"`
	// IdempotencyKey makes a retry safe: a repeated key, from the same caller,
	// returns the session the first request created while it is unfinished. A
	// label value: at most 63 characters of letters, digits, '-', '_' and '.',
	// starting and ending with a letter or digit.
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// Name is a summoned caller's session name (plan 10). Others get a
	// generated one: <repo>-<mmdd>-<HHMMSS> in UTC.
	Name string `json:"name,omitempty"`
	// Lane is a summoned caller's lane (plan 10).
	Lane string `json:"lane,omitempty"`
}

// Limits caps a task session.
type Limits struct {
	// Timeout is a positive Go duration, for example 40m.
	Timeout string `json:"timeout,omitempty"`
	// MaxTurns caps the agent's turns; 0 means no cap.
	MaxTurns int32 `json:"maxTurns,omitempty"`
}

// Session is one session as the API shows it: GET /v1/sessions/{name}, the
// items of GET /v1/sessions, and the answer to a create or a reap.
type Session struct {
	Name           string   `json:"name"`
	Repo           string   `json:"repo"`
	Base           string   `json:"base,omitempty"`
	Agent          string   `json:"agent"`
	Mode           string   `json:"mode"`
	Model          string   `json:"model"`
	Effort         string   `json:"effort,omitempty"`
	Size           string   `json:"size,omitempty"`
	Profile        string   `json:"profile,omitempty"`
	Tools          []string `json:"tools,omitempty"`
	Limits         *Limits  `json:"limits,omitempty"`
	Parent         string   `json:"parent,omitempty"`
	Caller         string   `json:"caller,omitempty"`
	Lane           string   `json:"lane,omitempty"`
	IdempotencyKey string   `json:"idempotencyKey,omitempty"`
	// Prompt is in GET /v1/sessions/{name} only, never in a list.
	Prompt        string    `json:"prompt,omitempty"`
	OperatingMode string    `json:"operatingMode,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	// Reaping is set once the session is deleted: the operator rescues it,
	// suspends it and archives it (D-10, D-45).
	Reaping bool `json:"reaping,omitempty"`

	Phase string `json:"phase,omitempty"`
	// Pending is the scheduler's reason while the session is Pending.
	Pending  string `json:"pending,omitempty"`
	Node     string `json:"node,omitempty"`
	Revision string `json:"revision,omitempty"`
	// Outdated is set when the pod runs an older template revision.
	Outdated      bool           `json:"outdated,omitempty"`
	AgentStatus   *AgentStatus   `json:"agentStatus,omitempty"`
	RemoteControl *RemoteControl `json:"remoteControl,omitempty"`
	Outcome       *Outcome       `json:"outcome,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	Conditions    []Condition    `json:"conditions,omitempty"`
}

// AgentStatus is the agent's state from agentd's last heartbeat.
type AgentStatus struct {
	State          string      `json:"state,omitempty"`
	Boot           string      `json:"boot,omitempty"`
	Problems       []string    `json:"problems,omitempty"`
	Branch         string      `json:"branch,omitempty"`
	Head           string      `json:"head,omitempty"`
	ConversationID string      `json:"conversationId,omitempty"`
	LastActivity   *time.Time  `json:"lastActivity,omitempty"`
	LastHeartbeat  *time.Time  `json:"lastHeartbeat,omitempty"`
	Task           *TaskResult `json:"task,omitempty"`
	Message        string      `json:"message,omitempty"`
	Agentd         string      `json:"agentd,omitempty"`
}

// TaskResult is how a task ended.
type TaskResult struct {
	ExitCode   int32     `json:"exitCode"`
	FinishedAt time.Time `json:"finishedAt"`
	TimedOut   bool      `json:"timedOut,omitempty"`
	Subtype    string    `json:"subtype,omitempty"`
	IsError    bool      `json:"isError,omitempty"`
	NumTurns   int32     `json:"numTurns,omitempty"`
}

// RemoteControl is a remote session's Remote Control entry (plan 03).
type RemoteControl struct {
	Name  string `json:"name,omitempty"`
	URL   string `json:"url,omitempty"`
	State string `json:"state,omitempty"`
}

// Outcome is what a summoned session reported (plan 10).
type Outcome struct {
	State string     `json:"state,omitempty"`
	Note  string     `json:"note,omitempty"`
	At    *time.Time `json:"at,omitempty"`
}

// Usage is the session's cost record (V-16). CostUSD is a decimal string.
type Usage struct {
	CostUSD      string `json:"costUSD,omitempty"`
	InputTokens  int64  `json:"inputTokens,omitempty"`
	OutputTokens int64  `json:"outputTokens,omitempty"`
}

// Condition is one of the session's status conditions.
type Condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Message string `json:"message,omitempty"`
}

// SessionList is the body of GET /v1/sessions, newest first.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}

// List filters: the query parameters of GET /v1/sessions (DESIGN-001 3.4). Each
// is optional; an unknown parameter is refused.
const (
	// FilterRepo matches spec.repo.
	FilterRepo = "repo"
	// FilterState matches the phase (Pending, Running, ...), ignoring case.
	FilterState = "state"
	// FilterMine, "true", keeps the sessions whose parent is the caller.
	FilterMine = "mine"
	// FilterCaller matches spec.caller.
	FilterCaller = "caller"
	// FilterLane matches spec.lane.
	FilterLane = "lane"
	// FilterOutcome matches status.outcome.state.
	FilterOutcome = "outcome"
)

// Fleet is the body of GET /v1/fleet: what is running and waiting, and on which
// revision. It reports; it gates nothing (D-21). Storage health arrives with
// plan 04 and the plan-quota state with the keeper (S-16, plan 03).
type Fleet struct {
	// Revision is the current template revision; RevisionError says why it is
	// unknown when it is.
	Revision      string `json:"revision,omitempty"`
	RevisionError string `json:"revisionError,omitempty"`
	// Phases counts every session by phase; a session with no phase yet counts
	// as Pending.
	Phases map[string]int `json:"phases"`
	// Sessions are the sessions that hold or wait for a pod: every phase except
	// Suspended and Archived. Pending ones carry the scheduler's reason.
	Sessions []FleetSession `json:"sessions"`
	// Nodes counts those sessions' pods per node.
	Nodes []FleetNode `json:"nodes"`
	// Outdated names the sessions whose pod runs an older revision.
	Outdated []string `json:"outdated"`
}

// FleetSession is one session in the fleet view.
type FleetSession struct {
	Name          string     `json:"name"`
	Repo          string     `json:"repo"`
	Mode          string     `json:"mode"`
	Phase         string     `json:"phase"`
	Pending       string     `json:"pending,omitempty"`
	Node          string     `json:"node,omitempty"`
	Revision      string     `json:"revision,omitempty"`
	Outdated      bool       `json:"outdated,omitempty"`
	Parent        string     `json:"parent,omitempty"`
	AgentState    string     `json:"agentState,omitempty"`
	LastHeartbeat *time.Time `json:"lastHeartbeat,omitempty"`
	Reaping       bool       `json:"reaping,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// FleetNode is the number of session pods on one node.
type FleetNode struct {
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
}

// ErrorResponse is the body of every refusal.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// Error is a refusal: a stable code for programs, a sentence for people, and the
// fields at fault when the request was invalid.
type Error struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []FieldError `json:"fields,omitempty"`
}

// FieldError is one invalid field, named as in the request (limits.timeout).
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error codes, with the HTTP status each comes with.
const (
	// CodeBadRequest (400): the body is not JSON of the request's shape, or a
	// query parameter is unknown or malformed.
	CodeBadRequest = "bad_request"
	// CodeUnauthenticated (401): no bearer token, or the API server did not
	// accept it for the dev-env-operator audience.
	CodeUnauthenticated = "unauthenticated"
	// CodeForbidden (403): the caller may not do this.
	CodeForbidden = "forbidden"
	// CodeNotFound (404): no such session or route.
	CodeNotFound = "not_found"
	// CodeMethodNotAllowed (405).
	CodeMethodNotAllowed = "method_not_allowed"
	// CodeConflict (409): the idempotency key was used for a different request.
	CodeConflict = "conflict"
	// CodeTooLarge (413): the body is over the route's limit.
	CodeTooLarge = "too_large"
	// CodeUnsupportedMediaType (415): the body is not application/json.
	CodeUnsupportedMediaType = "unsupported_media_type"
	// CodeInvalid (422): the request is well formed but breaks a rule; Fields
	// names each field at fault.
	CodeInvalid = "invalid"
	// CodeLimitExceeded (429): a limit that frees up later, such as a session's
	// four running children.
	CodeLimitExceeded = "limit_exceeded"
	// CodeInternal (500).
	CodeInternal = "internal"
	// CodeUnavailable (503): the API server could not be reached; retry.
	CodeUnavailable = "unavailable"
)
