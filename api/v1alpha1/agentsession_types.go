package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The shape follows DESIGN-001 3.3, which follows kubernetes-sigs/agent-sandbox's
// Sandbox (operatingMode, a timer-style lifecycle) so a later move to that
// project stays mechanical (DESIGN-001 9).
//
// TODO(plan 01 step 1, KICKOFF section 4): with the envtest suite, add the
// cross-field rules the design states in prose as CEL validations: prompt only in
// task mode, llm only for opencode, caller, lane and idempotencyKey only on
// summoned sessions, urgent priority for the escalation and remediation lanes
// (7.3), metadata.name at most 63 characters (it is the pod's hostname), and
// which spec fields are immutable after create.

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
// it; agents never write AgentSession objects themselves (DESIGN-001 6.11).
type AgentSessionSpec struct {
	// Repo is the repository the session works in, for example haynes-ops.
	// +kubebuilder:validation:MinLength=1
	// +required
	Repo string `json:"repo"`

	// Base is the ref the session's worktree branches from.
	// +kubebuilder:default="origin/main"
	// +optional
	Base string `json:"base,omitempty"`

	// Agent is the agent CLI to run.
	// +required
	Agent AgentKind `json:"agent"`

	// Mode is how the agent runs.
	// +required
	Mode SessionMode `json:"mode"`

	// Model is a full model id, never an alias (for example claude-opus-5-5).
	// For opencode it is the LLM pool's model id.
	// +kubebuilder:validation:MinLength=1
	// +required
	Model string `json:"model"`

	// Effort is the reasoning effort. The levels differ per model, so the API
	// checks them, not this schema.
	// +optional
	Effort string `json:"effort,omitempty"`

	// Prompt is the task. Task mode only.
	// +optional
	Prompt string `json:"prompt,omitempty"`

	// Size picks the requests and limits preset.
	// +kubebuilder:default=M
	// +optional
	Size SizeClass `json:"size,omitempty"`

	// TODO(plan 01 step 2, KICKOFF section 4): set the default profile when the
	// templates land.

	// Profile names the Secrets, egress tier and standing grants the pod gets
	// (D-18): full, dev or ops. Profiles are template data.
	// +optional
	Profile string `json:"profile,omitempty"`

	// Tools are the tool pools registered at boot (DESIGN-001 8.1). Empty means
	// the profile's default.
	// +listType=set
	// +optional
	Tools []string `json:"tools,omitempty"`

	// LLM is the LLM pool an opencode session leases (DESIGN-001 8.3).
	// +optional
	LLM *LLMSpec `json:"llm,omitempty"`

	// Parent is the session or caller that created this one. The API sets it from
	// the caller's token, never from the request body (D-05).
	// +optional
	Parent string `json:"parent,omitempty"`

	// Caller is the CallerPolicy of a summoned session, for example
	// alert-responder (DESIGN-001 3.7). Empty for Tom's own sessions.
	// +optional
	Caller string `json:"caller,omitempty"`

	// Lane is a summoned session's lane.
	// +optional
	Lane Lane `json:"lane,omitempty"`

	// IdempotencyKey lets a summoning caller repeat a create safely, for example
	// with an alert signature: a repeated key returns the existing session.
	// +optional
	IdempotencyKey string `json:"idempotencyKey,omitempty"`

	// Limits caps a task session. Summoned callers get them from their policy.
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
	// +required
	Pool string `json:"pool"`
}

// SessionLimits caps a task session.
type SessionLimits struct {
	// Timeout is the wall-clock limit, for example 3h.
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
	// +optional
	IdleSuspendAfter *metav1.Duration `json:"idleSuspendAfter,omitempty"`

	// ArchiveAfter archives a suspended session after this long, once its rescue
	// bundle is verified (D-09: 168h).
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
// name is also the pod name, the hostname and the Remote Control name.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Repo",type=string,JSONPath=`.spec.repo`
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agent`
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
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
