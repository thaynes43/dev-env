// Package apiv1 holds the wire types of the operator's /v1 API (DESIGN-001 3.4,
// D-46, D-56): the requests and responses of /v1/sessions, /v1/fleet and
// /v1/grants, and the error document every refusal carries. agentd's heartbeat body is
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

// SessionSuspendPath and SessionResumePath are a session's suspend and resume
// routes (DESIGN-001 3.4, D-60). Each is a POST with no body.
func SessionSuspendPath(name string) string { return SessionPath(name) + "/suspend" }

// SessionResumePath is a session's resume route; see SessionSuspendPath.
func SessionResumePath(name string) string { return SessionPath(name) + "/resume" }

// SessionLogPath is a session's log route, GET with ?tail=N (D-65).
func SessionLogPath(name string) string { return SessionPath(name) + "/log" }

// SessionMessagesPath is a session's message route, POST (D-16, D-65).
func SessionMessagesPath(name string) string { return SessionPath(name) + "/messages" }

// MaxMessageBytes caps a message's text (D-65).
const MaxMessageBytes = 16 << 10

// SessionLog is the answer of GET /v1/sessions/{name}/log: the last lines of
// the session's log, read in its running pod.
type SessionLog struct {
	Session string `json:"session"`
	Tail    int    `json:"tail"`
	Text    string `json:"text"`
	// Truncated is set when the lines asked for were more than the API sends
	// (4 MiB): Text then holds the newest whole lines that fit.
	Truncated bool `json:"truncated,omitempty"`
}

// MessageRequest is the body of POST /v1/sessions/{name}/messages: the text,
// at most MaxMessageBytes. The API adds who sent it, from the caller's token.
type MessageRequest struct {
	Text string `json:"text"`
}

// MessageResult is the answer of a delivered message.
type MessageResult struct {
	Session string `json:"session"`
	// From is the sender as the session's agent reads it.
	From      string `json:"from"`
	Delivered bool   `json:"delivered"`
}

// RescuesPath lists the rescues on the shared volume, read through the shelf
// pod (D-67). ?session= narrows it to one session's. A restore is a create:
// CreateSessionRequest.Restore.
const RescuesPath = "/v1/rescues"

// ActivitiesPath is declare-activity's route (DESIGN-001 6.9, D-17, D-66):
// GET lists the live declarations, POST declares one.
const ActivitiesPath = "/v1/activities"

// ActivityPath is one declaration's route: DELETE ends it.
func ActivityPath(name string) string { return ActivitiesPath + "/" + name }

// DeclareActivityRequest is the body of POST /v1/activities (D-17): what is
// being done, what it can disturb, and for how long. The API takes the
// declarer from the caller's token.
type DeclareActivityRequest struct {
	// Description says what is being done, at most 512 bytes.
	Description string `json:"description"`
	// Scope names what the work can disturb: namespaces, apps, nodes, or
	// "cluster" (at most 2h). At least one, at most 32, each at most 63 bytes.
	Scope []string `json:"scope"`
	// TTL is a Go duration; empty is 45m. At most 8h, and 2h for cluster.
	TTL string `json:"ttl,omitempty"`
}

// Activity is one live declaration.
type Activity struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Scope       []string  `json:"scope"`
	DeclaredBy  string    `json:"declaredBy"`
	Session     string    `json:"session,omitempty"`
	DeclaredAt  time.Time `json:"declaredAt"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// ActivityList is GET /v1/activities: the declarations that have not
// expired, newest first.
type ActivityList struct {
	Activities []Activity `json:"activities"`
}

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
	// Lifecycle sets this session's own timers (D-09, D-60); the templates'
	// apply to the rest.
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`
	// Restore is a rescue's id, <session>/<stamp> (D-67): the new session's
	// clone fetches that rescue's bundle for Repo into refs/rescued/* on its
	// first boot. The rescue must be complete and hold a bundle for Repo. With
	// no Base, the worktree starts at the old session's rescued branch when the
	// bundle holds it.
	Restore string `json:"restore,omitempty"`
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

// Lifecycle is a session's own timers (D-09, D-60): positive Go durations.
// Empty fields take the templates' timers.
type Lifecycle struct {
	// IdleSuspendAfter suspends the session once its agent has been idle this
	// long (D-60).
	IdleSuspendAfter string `json:"idleSuspendAfter,omitempty"`
	// ArchiveAfter archives the suspended session's volume this long after the
	// suspend (D-62).
	ArchiveAfter string `json:"archiveAfter,omitempty"`
}

// Session is one session as the API shows it: GET /v1/sessions/{name}, the
// items of GET /v1/sessions, and the answer to a create or a reap.
type Session struct {
	Name    string   `json:"name"`
	Repo    string   `json:"repo"`
	Base    string   `json:"base,omitempty"`
	Agent   string   `json:"agent"`
	Mode    string   `json:"mode"`
	Model   string   `json:"model"`
	Effort  string   `json:"effort,omitempty"`
	Size    string   `json:"size,omitempty"`
	Profile string   `json:"profile,omitempty"`
	Tools   []string `json:"tools,omitempty"`
	Limits  *Limits  `json:"limits,omitempty"`
	// Lifecycle is the session's own timers, when it has any.
	Lifecycle      *Lifecycle `json:"lifecycle,omitempty"`
	Parent         string     `json:"parent,omitempty"`
	Caller         string     `json:"caller,omitempty"`
	Lane           string     `json:"lane,omitempty"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	// Prompt is in GET /v1/sessions/{name} only, never in a list.
	Prompt        string `json:"prompt,omitempty"`
	OperatingMode string `json:"operatingMode,omitempty"`
	// SuspendedBy says who suspended the session: "idle-timer" for the
	// operator's idle timer (D-60), else the API caller.
	SuspendedBy string    `json:"suspendedBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	// SuspendedAt is when the operator last suspended the session, and
	// ArchivedAt when it archived the volume (D-60, D-62).
	SuspendedAt *time.Time `json:"suspendedAt,omitempty"`
	ArchivedAt  *time.Time `json:"archivedAt,omitempty"`
	// Restore is the rescue the session was restored from (D-67).
	Restore string `json:"restore,omitempty"`
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
	// CodeNotFound (404): no such session, grant or route.
	CodeNotFound = "not_found"
	// CodeMethodNotAllowed (405).
	CodeMethodNotAllowed = "method_not_allowed"
	// CodeConflict (409): the idempotency key was used for a different request,
	// or a session being reaped asked for a grant.
	CodeConflict = "conflict"
	// CodeTooLarge (413): the body is over the route's limit.
	CodeTooLarge = "too_large"
	// CodeUnsupportedMediaType (415): the body is not application/json.
	CodeUnsupportedMediaType = "unsupported_media_type"
	// CodeInvalid (422): the request is well formed but breaks a rule; Fields
	// names each field at fault.
	CodeInvalid = "invalid"
	// CodeLimitExceeded (429): a limit that frees up later, such as a session's
	// four running children or its three pending grants.
	CodeLimitExceeded = "limit_exceeded"
	// CodeInternal (500).
	CodeInternal = "internal"
	// CodeUnavailable (503): the API server could not be reached; retry.
	CodeUnavailable = "unavailable"
)

// Rescue is one rescue on the shared volume, as GET /v1/rescues lists it (D-48,
// D-67). The bundles never leave the cluster; this is what they hold.
type Rescue struct {
	// ID is <session>/<stamp>, what a restore names.
	ID      string `json:"id"`
	Session string `json:"session"`
	// CreatedAt is the manifest's time, or the directory's for a rescue that
	// wrote none.
	CreatedAt time.Time `json:"createdAt"`
	// Finished: the rescue wrote its manifest. Complete: every bundle in it
	// was written and verified. Only a complete rescue can be restored.
	Finished bool `json:"finished"`
	Complete bool `json:"complete"`
	// Repos are the clones it holds a bundle for.
	Repos []RescueRepo `json:"repos,omitempty"`
	// Bytes is the size of the rescue's files.
	Bytes int64 `json:"bytes"`
	// Error says what is wrong with the rescue's manifest, if anything.
	Error string `json:"error,omitempty"`
	// SessionExists: the session still exists, so the rescue is kept.
	SessionExists bool `json:"sessionExists"`
	// PruneAfter is when the operator's pruner may remove the rescue: its
	// age passes the templates' bundleRetention (D-09). Empty while its
	// session exists.
	PruneAfter *time.Time `json:"pruneAfter,omitempty"`
}

// RescueRepo is one clone's bundle in a rescue.
type RescueRepo struct {
	// Repo is the clone's directory name, the session's repo.
	Repo string `json:"repo"`
	// Refs are the refs the bundle holds, such as refs/heads/agent/<session>.
	// A restore fetches them under refs/rescued/.
	Refs  []string `json:"refs"`
	Bytes int64    `json:"bytes"`
}

// RescueList is GET /v1/rescues, newest first.
type RescueList struct {
	Rescues []Rescue `json:"rescues"`
	// Unrecognized are paths under rescue/ that do not fit the layout; they
	// are never pruned.
	Unrecognized []string `json:"unrecognized,omitempty"`
}
