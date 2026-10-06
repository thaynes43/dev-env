package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The shape follows DESIGN-001 3.3, which follows kubernetes-sigs/agent-sandbox's
// Sandbox (operatingMode, a timer-style lifecycle) so a later move to that
// project stays mechanical (DESIGN-001 9).
//
// The schema enforces every rule about a single session that DESIGN-001 states
// (D-39): the API server refuses a bad object, whoever writes it. The cross-field
// rules are CEL (x-kubernetes-validations) on AgentSession and AgentSessionSpec:
//
//   - metadata.name is a DNS label of at most 63 characters (it is the pod's
//     hostname and the Remote Control name);
//   - prompt is set in task mode and only there; limits are task mode only;
//   - llm is set for opencode and only there;
//   - a Claude model is a full id (claude-...), never an alias;
//   - caller and lane go together and mark a summoned session; idempotencyKey
//     needs them; a summoned session runs in task or remote mode and names a
//     profile other than full (3.7, D-36);
//   - spec is immutable after create except operatingMode and lifecycle.
//
// Rules about a caller rather than a session (urgent priority for the
// remediation and escalation lanes, a fallback model that differs from the
// primary, no profile full in a policy) belong to CallerPolicy's schema (plan 10).
// The envtest suite in this package proves each rule against a real API server.

// AgentKind is the agent CLI a session runs.
// +kubebuilder:validation:Enum=claude;codex;opencode
type AgentKind string

const (
	AgentClaude AgentKind = "claude"
	AgentCodex  AgentKind = "codex"
	// AgentOpencode runs a local model through an LLM pool (DESIGN-001 6.13).
	AgentOpencode AgentKind = "opencode"
)

// SessionMode is how the agent runs: v1's task, local and both become task,
// local and remote. Summoned sessions use task or remote (DESIGN-001 3.7).
// +kubebuilder:validation:Enum=task;local;remote
type SessionMode string

const (
	// ModeTask runs one prompt headless and finishes.
	ModeTask SessionMode = "task"
	// ModeLocal runs an interactive TUI that Tom attaches to.
	ModeLocal SessionMode = "local"
	// ModeRemote runs an interactive session that Tom drives through Remote
	// Control (claude.ai or the phone).
	ModeRemote SessionMode = "remote"
)

// SizeClass is a preset for the pod's requests and limits (DESIGN-001 7.2). The
// numbers are template data in haynes-ops (D-04), not code.
// +kubebuilder:validation:Enum=S;M;L
type SizeClass string

const (
	SizeS SizeClass = "S"
	SizeM SizeClass = "M"
	SizeL SizeClass = "L"
)

// OperatingMode is the desired state of the session's pod. Suspended deletes the
// pod, after a rescue, and keeps the volume.
// +kubebuilder:validation:Enum=Running;Suspended
type OperatingMode string

const (
	OperatingModeRunning   OperatingMode = "Running"
	OperatingModeSuspended OperatingMode = "Suspended"
)

// Lane groups summoned sessions (DESIGN-001 3.7). remediation, upgrade and
// curation are single-flight; escalation is a label only.
// +kubebuilder:validation:Enum=remediation;upgrade;curation;escalation
type Lane string

const (
	LaneRemediation Lane = "remediation"
	LaneUpgrade     Lane = "upgrade"
	LaneCuration    Lane = "curation"
	LaneEscalation  Lane = "escalation"
)

// SessionPhase is the lifecycle state of DESIGN-001 4.1.
// +kubebuilder:validation:Enum=Pending;Running;Idle;Draining;Suspended;Archived;Failed
type SessionPhase string

const (
	PhasePending   SessionPhase = "Pending"
	PhaseRunning   SessionPhase = "Running"
	PhaseIdle      SessionPhase = "Idle"
	PhaseDraining  SessionPhase = "Draining"
	PhaseSuspended SessionPhase = "Suspended"
	PhaseArchived  SessionPhase = "Archived"
	PhaseFailed    SessionPhase = "Failed"
)

// OutcomeState is what a summoned session reports about its own work
// (DESIGN-001 3.7).
// +kubebuilder:validation:Enum=pending;running;done;failed;escalated
type OutcomeState string

const (
	OutcomePending   OutcomeState = "pending"
	OutcomeRunning   OutcomeState = "running"
	OutcomeDone      OutcomeState = "done"
	OutcomeFailed    OutcomeState = "failed"
	OutcomeEscalated OutcomeState = "escalated"
)

// AgentSessionSpec is what the caller asked for. The operator's /v1 API writes
// it; agents never write AgentSession objects themselves (DESIGN-001 6.11). The
// operator never writes spec either: what it resolves (the default profile and
// tools, the timers per mode) it reads from the templates or records in status.
//
// +kubebuilder:validation:XValidation:rule="self.mode == 'task' ? has(self.prompt) : !has(self.prompt)",message="prompt is required in task mode and not allowed in local or remote mode"
// +kubebuilder:validation:XValidation:rule="!has(self.limits) || self.mode == 'task'",message="limits apply to task mode only"
// +kubebuilder:validation:XValidation:rule="self.agent == 'opencode' ? has(self.llm) : !has(self.llm)",message="llm is required for agent opencode and not allowed for claude or codex"
// +kubebuilder:validation:XValidation:rule="self.agent != 'claude' || self.model.startsWith('claude-')",message="a Claude model is a full id such as claude-opus-5-5, never an alias"
// +kubebuilder:validation:XValidation:rule="has(self.caller) == has(self.lane)",message="caller and lane go together: a summoned session sets both, Tom's own sessions neither"
// +kubebuilder:validation:XValidation:rule="!has(self.idempotencyKey) || has(self.caller)",message="idempotencyKey is for summoned sessions only: set caller and lane"
// +kubebuilder:validation:XValidation:rule="!has(self.caller) || self.mode != 'local'",message="a summoned session runs in task or remote mode, never local"
// +kubebuilder:validation:XValidation:rule="!has(self.caller) || (has(self.profile) && self.profile != 'full')",message="a summoned session names its profile, and it is never full (D-36)"
// +kubebuilder:validation:XValidation:rule="has(self.base) == has(oldSelf.base) && has(self.effort) == has(oldSelf.effort) && has(self.prompt) == has(oldSelf.prompt) && has(self.size) == has(oldSelf.size) && has(self.profile) == has(oldSelf.profile) && has(self.tools) == has(oldSelf.tools) && has(self.llm) == has(oldSelf.llm) && has(self.parent) == has(oldSelf.parent) && has(self.caller) == has(oldSelf.caller) && has(self.lane) == has(oldSelf.lane) && has(self.idempotencyKey) == has(oldSelf.idempotencyKey) && has(self.limits) == has(oldSelf.limits)",message="immutable after create: no spec field may be added or removed, except operatingMode and lifecycle"
type AgentSessionSpec struct {
	// Repo is the repository the session works in, for example haynes-ops: a
	// name, not a path (it becomes a directory in the pod).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+$`
	// +kubebuilder:validation:XValidation:rule="self != '.' && self != '..'",message="repo is a repository name, not a path"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +required
	Repo string `json:"repo"`

	// Base is the ref the session's worktree branches from. It cannot start
	// with '-', so git never reads it as an option.
	// +kubebuilder:default="origin/main"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=255
	// +kubebuilder:validation:Pattern=`^[^-\s]\S*$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Base string `json:"base,omitempty"`

	// Agent is the agent CLI to run.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +required
	Agent AgentKind `json:"agent"`

	// Mode is how the agent runs.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +required
	Mode SessionMode `json:"mode"`

	// Model is a full model id, never an alias (for example claude-opus-5-5), so
	// a Claude model starts with claude-. For opencode it is the LLM pool's model
	// id.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^\S+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +required
	Model string `json:"model"`

	// Effort is the reasoning effort. The levels differ per model, so the API
	// checks them, not this schema.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-z]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Effort string `json:"effort,omitempty"`

	// Prompt is the task. Task mode only, and required there.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=262144
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// Size picks the requests and limits preset.
	// +kubebuilder:default=M
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Size SizeClass `json:"size,omitempty"`

	// TODO(plan 01 step 2, KICKOFF section 4): the operator resolves an empty
	// profile to the templates' default when it builds the pod. It never writes
	// the default back into spec.

	// Profile names the Secrets, egress tier and standing grants the pod gets
	// (D-18): full, dev or ops. Profiles are template data, so the schema checks
	// the form, not the name. Empty means the templates' default; a summoned
	// session always names one.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Profile string `json:"profile,omitempty"`

	// Tools are the tool pools registered at boot (DESIGN-001 8.1), by ToolPool
	// name. Empty means the profile's default.
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Tools []string `json:"tools,omitempty"`

	// LLM is the LLM pool an opencode session leases (DESIGN-001 8.3).
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	LLM *LLMSpec `json:"llm,omitempty"`

	// Parent is the session or caller that created this one. The API sets it from
	// the caller's token, never from the request body (D-05).
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Parent string `json:"parent,omitempty"`

	// Caller is the CallerPolicy of a summoned session, for example
	// alert-responder (DESIGN-001 3.7). Empty for Tom's own sessions. A
	// summoned session sets caller and lane together.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Caller string `json:"caller,omitempty"`

	// Lane is a summoned session's lane.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Lane Lane `json:"lane,omitempty"`

	// IdempotencyKey lets a summoning caller repeat a create safely, for example
	// with an alert signature: a repeated key returns the existing session. It has
	// the form of a label value, so the API can find that session with a label
	// selector; a caller with a longer signature sends a hash of it.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9]([-A-Za-z0-9_.]*[A-Za-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// Limits caps a task session; task mode only. Summoned callers get them from
	// their policy.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Limits *SessionLimits `json:"limits,omitempty"`

	// OperatingMode is the desired state of the pod.
	// +kubebuilder:default=Running
	// +optional
	OperatingMode OperatingMode `json:"operatingMode,omitempty"`

	// Lifecycle overrides the default timers of D-09.
	// +optional
	Lifecycle *Lifecycle `json:"lifecycle,omitempty"`
}

// LLMSpec names the LLM pool an opencode session leases.
type LLMSpec struct {
	// Pool is the LLMPool name, for example llm-coder.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Pool string `json:"pool"`
}

// SessionLimits caps a task session.
type SessionLimits struct {
	// Timeout is the wall-clock limit, for example 3h.
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="must be a positive Go duration such as 40m or 72h"
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// MaxTurns caps the agent's turns. 0 means no cap.
	// +kubebuilder:validation:Minimum=0
	// +optional
	MaxTurns int32 `json:"maxTurns,omitempty"`
}

// TODO(plan 02): the timers, and the defaults per mode, land with idle
// detection; the operator applies them, so this schema sets no defaults.

// Lifecycle overrides the default timers of D-09. An unset field takes the
// default for the session's mode.
type Lifecycle struct {
	// IdleSuspendAfter suspends an idle session after this long (D-09: 72h for
	// interactive and remote sessions).
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="must be a positive Go duration such as 40m or 72h"
	// +optional
	IdleSuspendAfter *metav1.Duration `json:"idleSuspendAfter,omitempty"`

	// ArchiveAfter archives a suspended session after this long, once its rescue
	// bundle is verified (D-09: 168h).
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s')",message="must be a positive Go duration such as 40m or 72h"
	// +optional
	ArchiveAfter *metav1.Duration `json:"archiveAfter,omitempty"`
}

// AgentSessionStatus is what the operator and agentd observed. Reconcile is
// level-based (DESIGN-001 5.1): everything a fresh operator needs is here, not
// in its memory.
type AgentSessionStatus struct {
	// Phase is the lifecycle state.
	// +optional
	Phase SessionPhase `json:"phase,omitempty"`

	// PendingReason is the scheduler's reason while the session is Pending
	// (DESIGN-001 7.3).
	// +optional
	PendingReason string `json:"pending,omitempty"`

	// Revision is the template revision the pod runs (DESIGN-001 5.2).
	// +optional
	Revision string `json:"revision,omitempty"`

	// PodName is the session's pod. It equals the session's name.
	// +optional
	PodName string `json:"podName,omitempty"`

	// NodeName is the node the pod runs on.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Agent is the agent's own state, from agentd's heartbeat.
	// +optional
	Agent *AgentStatus `json:"agent,omitempty"`

	// RemoteControl is the session's Remote Control entry, for remote mode
	// (DESIGN-001 6.7).
	// +optional
	RemoteControl *RemoteControlStatus `json:"remoteControl,omitempty"`

	// Outcome is what a summoned session reported about its work.
	// +optional
	Outcome *OutcomeStatus `json:"outcome,omitempty"`

	// Usage is the session's cost record (DESIGN-001 3.7, V-16).
	// +optional
	Usage *UsageStatus `json:"usage,omitempty"`

	// Rescue records the session's rescue bundles (D-10).
	// +optional
	Rescue *RescueStatus `json:"rescue,omitempty"`

	// TODO(plan 01 step 5): name the condition types, among them the
	// rescueFailed mark of D-10 that blocks archive; QuotaExhausted (7.3)
	// follows with agentd's quota detection.

	// Conditions are the standard conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// AgentStatus is the agent's own state.
type AgentStatus struct {
	// TODO(plan 02): fix the values with idle detection (DESIGN-001 4.2).

	// Status is the agent's state: busy, idle or waiting for Claude, which
	// writes it itself; agentd derives it for Codex and opencode.
	// +optional
	Status string `json:"status,omitempty"`

	// LastActivity is the last time agentd saw the agent or its worktree change.
	// +optional
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`
}

// RemoteControlStatus is a session's Remote Control entry.
type RemoteControlStatus struct {
	// Name is the entry's name, which equals the session's name.
	// +optional
	Name string `json:"name,omitempty"`

	// SessionID is the Remote Control session id.
	// +optional
	SessionID string `json:"sessionId,omitempty"`

	// URL is the session's claude.ai link.
	// +optional
	URL string `json:"url,omitempty"`

	// TODO(plan 03): fix the values with Remote Control.

	// State is the entry's registration state, for example registered.
	// +optional
	State string `json:"state,omitempty"`
}

// OutcomeStatus is what a summoned session reported about its own work.
type OutcomeStatus struct {
	// State is the reported outcome.
	// +optional
	State OutcomeState `json:"state,omitempty"`

	// Note is the session's one-line note with its report.
	// +optional
	Note string `json:"note,omitempty"`

	// At is when the outcome was reported.
	// +optional
	At *metav1.Time `json:"at,omitempty"`
}

// UsageStatus is a session's cost record: the CLI's total_cost_usd and token
// counts for task sessions, token counts from the transcript for remote ones.
type UsageStatus struct {
	// TODO(plan 10): settle the representation with the cost metrics.

	// CostUSD is the CLI's total_cost_usd as a decimal string, because CRDs
	// avoid floating-point fields.
	// +kubebuilder:validation:Pattern=`^[0-9]+(\.[0-9]+)?$`
	// +optional
	CostUSD string `json:"costUSD,omitempty"`

	// InputTokens is the number of input tokens.
	// +kubebuilder:validation:Minimum=0
	// +optional
	InputTokens int64 `json:"inputTokens,omitempty"`

	// OutputTokens is the number of output tokens.
	// +kubebuilder:validation:Minimum=0
	// +optional
	OutputTokens int64 `json:"outputTokens,omitempty"`
}

// RescueStatus records a session's rescue bundles on the shared volume (D-10).
type RescueStatus struct {
	// LastBundle is the newest bundle's path on the shared volume, for example
	// rescue/haynes-ops-1005-202504/20261006-0130.bundle.
	// +optional
	LastBundle string `json:"lastBundle,omitempty"`

	// TODO(plan 01 step 5): the list of local refs that origin lacks at suspend
	// time, which archive checks the bundle's manifest against (D-10 point 4).
}

// AgentSession is one agent session: its pod, its volume and its lifecycle. The
// name is also the pod name, the hostname and the Remote Control name, so it is a
// DNS label of at most 63 characters.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repo`
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`,priority=1
// +kubebuilder:printcolumn:name="Lane",type=string,JSONPath=`.spec.lane`,priority=1
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome.state`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="metadata.name must be at most 63 characters: it is the pod's hostname"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="metadata.name must be a DNS label (lowercase letters, digits and '-', no dots): it is the pod's hostname"
type AgentSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec AgentSessionSpec `json:"spec"`

	// +optional
	Status AgentSessionStatus `json:"status,omitempty"`
}

// AgentSessionList is a list of AgentSession.
//
// +kubebuilder:object:root=true
type AgentSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentSession `json:"items"`
}
