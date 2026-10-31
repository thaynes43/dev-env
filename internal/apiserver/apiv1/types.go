// Package apiv1 holds the wire types of the operator's /v1 API (DESIGN-001 3.4,
// D-46, D-56): the requests and responses of /v1/sessions, /v1/fleet and
// /v1/grants, and the error document every refusal carries. agentd's heartbeat body is
// protocol.Status (internal/agentd/protocol, D-41).
//
// It depends on the standard library and agentd's standard-library protocol, so agent-run (plan 01 step 7), which
// is one static binary (D-06), imports it without the Kubernetes libraries.
package apiv1

import (
	"fmt"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

// Paths. A session's own routes hang off SessionPath.
const (
	SessionsPath       = "/v1/sessions"
	PilotPreflightPath = "/v1/private-pilot/preflight"
	FleetPath          = "/v1/fleet"
	CanariesPath       = "/v1/canaries"
)

func CanaryResultPath(id string) string { return CanariesPath + "/" + id + "/result" }

// CanaryResult exposes one retained managed publication to its fixed initiating
// client. Context contains existing publisher assertions, not private receipts.
type CanaryResult struct {
	CanaryID   string           `json:"canaryID"`
	SessionUID string           `json:"sessionUID"`
	Context    PublishedContext `json:"context"`
}

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

func SessionDecisionPath(name string) string { return SessionPath(name) + "/decision" }

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
	// CanaryID selects one fixed operator-owned private managed rule canary.
	// A canary request contains this field only; its policy supplies the task.
	CanaryID string `json:"canaryID,omitempty"`
	Project  string `json:"project,omitempty"`
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
// PilotPreflight is an explicitly nonpersistent Kubernetes admission result.
type PilotPreflight struct {
	DryRun    string  `json:"dryRun"`
	Persisted bool    `json:"persisted"`
	Session   Session `json:"session"`
}

type Session struct {
	TaskClaims *SessionTaskClaims `json:"taskClaims,omitempty"`

	UID            string          `json:"uid,omitempty"`
	PrivateHistory *PrivateHistory `json:"privateHistory,omitempty"`
	Name           string          `json:"name"`
	Repo           string          `json:"repo"`
	Base           string          `json:"base,omitempty"`
	Agent          string          `json:"agent"`
	Mode           string          `json:"mode"`
	Model          string          `json:"model"`
	Effort         string          `json:"effort,omitempty"`
	Size           string          `json:"size,omitempty"`
	Profile        string          `json:"profile,omitempty"`
	Tools          []string        `json:"tools,omitempty"`
	Limits         *Limits         `json:"limits,omitempty"`
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
	Outdated         bool                  `json:"outdated,omitempty"`
	AgentStatus      *AgentStatus          `json:"agentStatus,omitempty"`
	RemoteControl    *RemoteControl        `json:"remoteControl,omitempty"`
	Outcome          *Outcome              `json:"outcome,omitempty"`
	Usage            *Usage                `json:"usage,omitempty"`
	UsageObservation *UsageObservation     `json:"usageObservation,omitempty"`
	TaskOutcome      *protocol.TaskOutcome `json:"taskOutcome,omitempty"`
	UsageConflict    *UsageConflict        `json:"usageConflict,omitempty"`
	// CapturedUsage is set only for Dev-Env-Usage-Capture: direct-v1. It is not
	// a usage total. Nil means this response did not carry the receipt view.
	CapturedUsage *CapturedUsage `json:"capturedUsage,omitempty"`
	// UsageRollup sums the retained descendants' own initial invocations. It is
	// present only on a single Session's view, and only when it has
	// descendants.
	UsageRollup *UsageRollup `json:"usageRollup,omitempty"`
	Conditions  []Condition  `json:"conditions,omitempty"`
}

// PrivateHistory is bounded retained provenance under the existing Session ACL.
// It is not provider history, a private stop receipt or an access grant.
type PrivateHistory struct {
	Version           int              `json:"version"`
	SessionUID        string           `json:"sessionUID"`
	HomeClaim         string           `json:"homeClaim"`
	HomeUID           string           `json:"homeUID"`
	BindingGeneration int64            `json:"bindingGeneration"`
	Admission         string           `json:"admission"`
	ExecutorAttempted bool             `json:"executorAttempted"`
	ExecutorIntent    string           `json:"executorIntent"`
	Provider          string           `json:"provider"`
	StopState         string           `json:"stopState"`
	LastExecutor      *PrivateExecutor `json:"lastExecutor,omitempty"`
}

type PrivateExecutor struct {
	PodUID         string `json:"podUID"`
	PodName        string `json:"podName"`
	NodeName       string `json:"nodeName,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Generation     int64  `json:"generation"`
	ConversationID string `json:"conversationId,omitempty"`
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
	// ProjectBootstrap is the boot's private catalog bootstrap: catalog
	// names, states, steps and reason codes only (DESIGN-006).
	ProjectBootstrap *ProjectBootstrap `json:"projectBootstrap,omitempty"`
}

// ProjectBootstrap is a private catalog bootstrap's outcome.
type ProjectBootstrap struct {
	State           string                       `json:"state"`
	Reason          string                       `json:"reason,omitempty"`
	CatalogRevision string                       `json:"catalogRevision,omitempty"`
	Repositories    []ProjectBootstrapRepository `json:"repositories,omitempty"`
	Projects        []ProjectBootstrapProject    `json:"projects,omitempty"`
}

// ProjectBootstrapRepository is one repository's reference refresh.
type ProjectBootstrapRepository struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Step   string `json:"step,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// ProjectBootstrapProject is one project root's refresh.
type ProjectBootstrapProject struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Reason     string `json:"reason,omitempty"`
	Repository string `json:"repository,omitempty"`
	Cause      string `json:"cause,omitempty"`
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

// Task result states: how a task-mode session's task ended (DESIGN-021). The
// phase stays the pod's (D-44), so a session whose pod ended reads Failed even
// when its task succeeded; the task's own answer is its result.
const (
	TaskSucceeded = "Succeeded"
	TaskFailed    = "Failed"
	TaskTimedOut  = "TimedOut"
	// TaskUnknown is an exit 0 with no success result event to prove it.
	TaskUnknown = "Unknown"
)

// TaskVerdict is a task's result: a state, a CamelCase reason and a sentence.
// The operator records the same in status.taskResult.
type TaskVerdict struct {
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// Verdict is how the task ended, from its exit and its result event. TimedOut
// wins. A non-zero exit, an error flag or an error subtype is Failed.
// Succeeded needs exit 0 and the result subtype success; an exit 0 without
// one is Unknown, because nothing reported the task's success.
func (t TaskResult) Verdict() TaskVerdict {
	var notes []string
	if t.Subtype != "" {
		notes = append(notes, t.Subtype)
	}
	if t.IsError {
		notes = append(notes, "error")
	}
	exit := fmt.Sprintf("exited %d", t.ExitCode)
	if len(notes) > 0 {
		exit += " (" + strings.Join(notes, ", ") + ")"
	}
	switch t.NumTurns {
	case 0:
	case 1:
		exit += " after 1 turn"
	default:
		exit += fmt.Sprintf(" after %d turns", t.NumTurns)
	}
	switch {
	case t.TimedOut:
		return TaskVerdict{TaskTimedOut, "TaskTimedOut", "agentd stopped the task at limits.timeout; it " + exit}
	case t.ExitCode != 0:
		return TaskVerdict{TaskFailed, "TaskExitedNonZero", "the task " + exit}
	case t.IsError || strings.HasPrefix(t.Subtype, "error"):
		return TaskVerdict{TaskFailed, "TaskReportedError", "the task " + exit}
	case t.Subtype == "success":
		return TaskVerdict{TaskSucceeded, "TaskSucceeded", "the task " + exit}
	default:
		return TaskVerdict{TaskUnknown, "TaskResultMissing", "the task " + exit + " with no success result event"}
	}
}

// TaskVerdict is the session's task result once its task ended, for a
// task-mode session only. The Session view carries the facts in
// AgentStatus.Task and no derived field, because the strict once clients
// refuse unknown fields (DESIGN-021, DESIGN-023).
func (s Session) TaskVerdict() *TaskVerdict {
	if s.Mode != "task" || s.AgentStatus == nil || s.AgentStatus.Task == nil {
		return nil
	}
	v := s.AgentStatus.Task.Verdict()
	return &v
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

// PortalSessionSummary is the only session object the portal reader returns.
// It has no prompt, log, environment, summoned-session outcome, or control
// fields; a task's retained outcome is opt-in in its activity. Activity is
// set only for GET /v1/sessions?view=activity; without it a summary keeps the
// six keys that earlier portal images require.
type PortalSessionSummary struct {
	Name       string                 `json:"name"`
	UID        string                 `json:"uid"`
	Repo       string                 `json:"repo"`
	Phase      string                 `json:"phase"`
	ObservedAt time.Time              `json:"observedAt"`
	Freshness  string                 `json:"freshness"`
	Activity   *PortalSessionActivity `json:"activity,omitempty"`
}

// PortalViewActivity is the one value of the portal reader's view parameter.
const PortalViewActivity = "activity"

// PortalSessionPageMaxBytes is the most the portal reads of one session page
// of 50. A page with every activity field at its bound, task outcomes
// included, stays under it; a larger page fails the whole read.
const PortalSessionPageMaxBytes = 512 << 10

// PortalLauncherPattern is the shape of a launchedBy value: a session name, a
// namespace/name or a ServiceAccount reference. The operator omits a
// spec.parent of any other shape, so one odd session cannot fail the read.
const PortalLauncherPattern = `^[A-Za-z0-9][A-Za-z0-9._:/@-]{0,252}$`

// PortalSessionActivity is DESIGN-029's browser allowlist for one session: its
// mode, agentd's state word, its last activity, its configured model and its
// own initial invocation usage. A value the operator does not hold is Unknown,
// never zero. RequestedModel is the configured model, not the served one.
// LastActivity is absent when it is unknown.
//
// Provider is the agent CLI (claude, codex or opencode). LaunchedBy is the
// session or caller that created it (spec.parent), absent when unset.
// TaskResult is how a task session's task ended (Succeeded, Failed, TimedOut
// or Unknown) and is absent for an interactive session: a Failed phase only
// says the pod ended.
type PortalSessionActivity struct {
	Mode           string               `json:"mode"`
	Provider       string               `json:"provider"`
	AgentState     string               `json:"agentState"`
	LastActivity   *time.Time           `json:"lastActivity,omitempty"`
	RequestedModel string               `json:"requestedModel"`
	LaunchedBy     string               `json:"launchedBy,omitempty"`
	TaskResult     string               `json:"taskResult,omitempty"`
	RemoteControl  *PortalRemoteControl `json:"remoteControl,omitempty"`
	// TaskOutcome is a task session's retained status.taskOutcome: the task's
	// own record at its first exit, bound to the original Session and Pod UIDs.
	// It is sent only when the portal asks with TaskOutcomeHeader, because an
	// older portal refuses an unknown activity key. It is separate from
	// TaskResult, which says only how the process ended.
	TaskOutcome *protocol.TaskOutcome `json:"taskOutcome,omitempty"`
	// ConversationUsage is the registered interactive conversation's measured
	// usage. It is sent only when the operator is started with
	// --portal-conversation-usage, because an older portal refuses an unknown
	// activity key. It is separate from Usage, the headless task's first
	// invocation, and the two are never added.
	ConversationUsage *PortalConversationUsage `json:"conversationUsage,omitempty"`
	Usage             PortalSessionUsage       `json:"usage"`
}

// PortalRemoteControlURLPattern is the one shape of a Remote Control link the
// portal carries: https, host claude.ai, path /code/session_<id>, no query,
// fragment or whitespace.
const PortalRemoteControlURLPattern = `^https://claude\.ai/code/session_[A-Za-z0-9_-]{8,128}$`

// PortalRemoteControlStatePattern is the shape of a Remote Control state word
// such as registered, starting or pair-code-issued.
const PortalRemoteControlStatePattern = `^[a-z][a-z-]{0,31}$`

// PortalConversationUsage is DESIGN-029's allowlist of a session's measured
// conversation usage: the result word and reason, the per-served-model counters,
// the counts and when it was observed. It carries no path, no id, no record text,
// no requested model and no cost. Unknown is not sent: the key is absent. Each
// counter keeps Reported apart from Unknown, and for Codex input already
// includes the cached tokens.
type PortalConversationUsage struct {
	// ResultState is Final or Partial. Partial always has a ReasonCode.
	ResultState string                         `json:"resultState"`
	ReasonCode  string                         `json:"reasonCode,omitempty"`
	Models      []PortalConversationModelUsage `json:"models"`
	Turns       int64                          `json:"turns"`
	Skipped     int64                          `json:"skipped"`
	ObservedAt  time.Time                      `json:"observedAt"`
}

// PortalConversationModelUsage is one served model's counters.
type PortalConversationModelUsage struct {
	Model                    string                    `json:"model"`
	InputTokens              protocol.TokenObservation `json:"inputTokens"`
	OutputTokens             protocol.TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     protocol.TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens protocol.TokenObservation `json:"cacheCreationInputTokens"`
}

// PortalRemoteControl is a session's Remote Control entry as the portal may see
// it: the state word and, only when it has the exact claude.ai shape, the link.
// It never carries the entry's name or session id. The link is already in the
// AgentSession status that cluster readers see; using it needs the owner's
// claude.ai sign-in.
type PortalRemoteControl struct {
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// PortalSessionUsage is a task session's own initial managed invocation as its
// provider reported it (DESIGN-018). Scope is InitialManagedInvocation when the
// operator holds a valid observation; otherwise Scope, ResultState and every
// counter are Unknown, and an interactive or remote session is always Unknown.
// ServedModels lists provider-reported model ids only. Cache read tokens can
// overlap input tokens and are never added to them. Cost is a CLI list-price
// equivalent estimate, never a charge or a subscription quota. Nothing is summed.
type PortalSessionUsage struct {
	Scope                    string                    `json:"scope"`
	ResultState              string                    `json:"resultState"`
	ServedModels             []string                  `json:"servedModels"`
	InputTokens              protocol.TokenObservation `json:"inputTokens"`
	OutputTokens             protocol.TokenObservation `json:"outputTokens"`
	CacheReadInputTokens     protocol.TokenObservation `json:"cacheReadInputTokens"`
	CacheCreationInputTokens protocol.TokenObservation `json:"cacheCreationInputTokens"`
	Cost                     protocol.CostObservation  `json:"cost"`
}

// PortalSessionPage is GET /v1/sessions for the portal reader. Complete is false
// when another page remains. Freshness is observed, stale, or unknown.
type PortalSessionPage struct {
	Sessions   []PortalSessionSummary `json:"sessions"`
	Limit      int                    `json:"limit"`
	Complete   bool                   `json:"complete"`
	Freshness  string                 `json:"freshness"`
	NextCursor string                 `json:"nextCursor,omitempty"`
	ObservedAt time.Time              `json:"observedAt"`
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
	// TaskResult is how a task-mode session's task ended, once it has. Phase
	// stays the pod's (D-44): an ended canary is Failed whatever its task did.
	TaskResult *TaskVerdict `json:"taskResult,omitempty"`
	// Resources is set only for a request that opted in with Dev-Env-Fleet:
	// resources-v1. It is the bound Pod's configured spec. Nil means this
	// response did not carry that observation.
	Resources *FleetResources `json:"resources,omitempty"`
}

// Fleet resource observation states and the request header that opts in.
const (
	FleetHeader            = "Dev-Env-Fleet"
	FleetResourcesV1       = "resources-v1"
	FleetResourcesObserved = "Observed"
	FleetResourcesUnknown  = "Unknown"
	FleetQuantitySet       = "Set"
	FleetQuantityAbsent    = "Absent"
	FleetNodeSet           = "Set"
	FleetNodeUnscheduled   = "Unscheduled"
	FleetContainerOrdinary = "ordinary"
	FleetContainerInit     = "init"
	// Closed reasons. They name the binding result, not a Pod message.
	FleetResourcesMissing     = "missing"
	FleetResourcesForeign     = "foreign"
	FleetResourcesAmbiguous   = "ambiguous"
	FleetResourcesReplaced    = "replaced"
	FleetResourcesUnreadable  = "unreadable"
	FleetResourcesOversized   = "oversized"
	FleetResourcesUnavailable = "unavailable"
)

// FleetResources is one session's bound Pod spec, or Unknown when that spec
// was not verified. It is not measured consumption, free node capacity, or a
// scheduling result.
type FleetResources struct {
	State          string                    `json:"state"`
	Reason         string                    `json:"reason,omitempty"`
	ObservedAt     *time.Time                `json:"observedAt,omitempty"`
	PodUID         string                    `json:"podUID,omitempty"`
	Node           *FleetResourceNode        `json:"node,omitempty"`
	Containers     []FleetContainerResources `json:"containers,omitempty"`
	InitContainers []FleetContainerResources `json:"initContainers,omitempty"`
}

// FleetResourceNode is Pod spec.nodeName. Unscheduled means the spec names no node.
type FleetResourceNode struct {
	State string `json:"state"`
	Name  string `json:"name,omitempty"`
}

// FleetContainerResources is one ordinary or init container's CPU and memory
// requests and limits. An absent quantity was not set. A set value of 0 was
// configured as zero.
type FleetContainerResources struct {
	Kind          string        `json:"kind"`
	Name          string        `json:"name"`
	CPURequest    FleetQuantity `json:"cpuRequest"`
	CPULimit      FleetQuantity `json:"cpuLimit"`
	MemoryRequest FleetQuantity `json:"memoryRequest"`
	MemoryLimit   FleetQuantity `json:"memoryLimit"`
}

// FleetQuantity is one configured quantity, or an explicit absence.
type FleetQuantity struct {
	State string `json:"state"`
	Value string `json:"value,omitempty"`
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
	// CodeLimitExceeded (429): a count limit was reached. Most free up later,
	// such as a session's four running children or its three pending grants.
	// Coordination Session bounds (TaskClaims, published contexts and inbox
	// messages) are lifetime for that Session UID. Project bounds count live
	// records and can free only after enabled successful archive-backed retention,
	// at least 30 days after the required Sessions are absent; immediate retry
	// does not help.
	CodeLimitExceeded = "limit_exceeded"
	// CodeInternal (500).
	CodeInternal = "internal"
	// CodeUnavailable (503): the API server could not be reached; retry.
	CodeUnavailable = "unavailable"
	// CodeNotLeader (503): this replica does not hold a keeper login ceremony.
	// Only the leader opens one, so the caller reconnects (a new connection may
	// land on the leader) and sends the request again; nothing was done.
	CodeNotLeader = "not_leader"
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
