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

// TaskResultState is how a task-mode session's task ended, the operator's
// reading of agent.task (DESIGN-021). It sits beside the phase, which stays the
// pod's (D-44).
// +kubebuilder:validation:Enum=Succeeded;Failed;TimedOut;Unknown
type TaskResultState string

const (
	TaskResultSucceeded TaskResultState = "Succeeded"
	TaskResultFailed    TaskResultState = "Failed"
	TaskResultTimedOut  TaskResultState = "TimedOut"
	// TaskResultUnknown is an exit 0 with no success result event to prove it.
	TaskResultUnknown TaskResultState = "Unknown"
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
// +kubebuilder:validation:XValidation:rule="has(self.base) == has(oldSelf.base) && has(self.effort) == has(oldSelf.effort) && has(self.prompt) == has(oldSelf.prompt) && has(self.size) == has(oldSelf.size) && has(self.profile) == has(oldSelf.profile) && has(self.tools) == has(oldSelf.tools) && has(self.llm) == has(oldSelf.llm) && has(self.parent) == has(oldSelf.parent) && has(self.caller) == has(oldSelf.caller) && has(self.lane) == has(oldSelf.lane) && has(self.idempotencyKey) == has(oldSelf.idempotencyKey) && has(self.limits) == has(oldSelf.limits) && has(self.restore) == has(oldSelf.restore) && has(self.workspace) == has(oldSelf.workspace) && has(self.projectCanary) == has(oldSelf.projectCanary) && has(self.commonRules) == has(oldSelf.commonRules) && has(self.sessionDirectory) == has(oldSelf.sessionDirectory) && has(self.privatePilot) == has(oldSelf.privatePilot) && has(self.usageCaptureParentUID) == has(oldSelf.usageCaptureParentUID) && has(self.coordinatorInstructionParentUID) == has(oldSelf.coordinatorInstructionParentUID)",message="immutable after create: no spec field may be added or removed, except operatingMode and lifecycle"
// +kubebuilder:validation:XValidation:rule="!has(self.projectCanary) || (self.mode == 'task' && self.agent in ['claude', 'codex'] && self.size == 'S' && has(self.parent) && self.parent == self.projectCanary.client && !has(self.workspace) && !has(self.restore) && !has(self.tools) && !has(self.llm) && !has(self.caller) && !has(self.lane) && !has(self.lifecycle) && has(self.limits) && has(self.limits.timeout) && duration(self.limits.timeout) <= duration('180s') && (self.agent == 'codex' ? (!has(self.limits.maxTurns) || self.limits.maxTurns == 0) : (has(self.limits.maxTurns) && self.limits.maxTurns > 0 && self.limits.maxTurns <= 3)))",message="project canaries require their fixed private bounded subscription task contract"
// +kubebuilder:validation:XValidation:rule="!has(self.commonRules) || (has(self.projectCanary) && has(self.projectCanary.commonRevision) && self.projectCanary.commonRevision == self.commonRules.commonRevision) || (has(self.privatePilot) && self.privatePilot.commonRevision == self.commonRules.commonRevision) || (!has(self.projectCanary) && !has(self.privatePilot) && !has(self.workspace) && self.mode == 'task' && self.agent in ['claude', 'codex'] && has(self.profile) && self.profile == 'dev' && has(self.parent) && has(self.limits) && has(self.limits.timeout) && duration(self.limits.timeout) > duration('0s') && duration(self.limits.timeout) <= duration('45m'))",message="common rules require a matching managed canary or finite private task binding"
// +kubebuilder:validation:XValidation:rule="!has(self.sessionDirectory) || has(self.projectCanary) || has(self.privatePilot)",message="directory enrollment requires a managed canary"
// +kubebuilder:validation:XValidation:rule="!has(self.privatePilot) || (!has(self.projectCanary) && self.repo == 'dev-env' && self.base == 'main' && self.mode == 'task' && self.agent == 'codex' && self.model == 'gpt-6-luna' && self.effort == 'high' && self.size == 'S' && self.profile == 'rule-canary-codex' && self.parent == 'dev/dev-env' && !has(self.workspace) && !has(self.restore) && !has(self.tools) && !has(self.llm) && !has(self.caller) && !has(self.lane) && !has(self.lifecycle) && !has(self.idempotencyKey) && has(self.limits) && has(self.limits.timeout) && duration(self.limits.timeout) == duration('15m') && (!has(self.limits.maxTurns) || self.limits.maxTurns == 0) && has(self.commonRules) && self.commonRules.commonRevision == self.privatePilot.commonRevision && has(self.sessionDirectory) && self.sessionDirectory.version == 1 && self.sessionDirectory.project == 'dev-env')",message="sealed pilots require the fixed private bounded Codex contract and matching retained inputs"
type AgentSessionSpec struct {
	// PrivatePilot is authored only by the sealed operator route, never a caller override.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="private pilot binding is immutable"
	// +optional
	PrivatePilot *PrivatePilotBinding `json:"privatePilot,omitempty"`
	// SessionDirectory is selected only by the prospective operator canary route.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="directory enrollment is immutable"
	// +optional
	SessionDirectory *SessionDirectoryEnrollment `json:"sessionDirectory,omitempty"`
	// CommonRules is authored only by the enabled managed common-input route.
	// Absence is legacy Unknown provenance, never an inferred current snapshot.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="common instruction binding is immutable"
	// +optional
	CommonRules *CommonRuleBinding `json:"commonRules,omitempty"`
	// ProjectCanary is authored by the strict operator-owned canary route.
	// It permanently binds the original accepted source and reviewed policy.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="project canary binding is immutable"
	// +optional
	ProjectCanary *ProjectCanaryBinding `json:"projectCanary,omitempty"`
	// Workspace explicitly opts this session into a pre-provisioned workspace.
	// Omission retains private repositories, even when later templates enable one.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create"
	// +optional
	Workspace *WorkspaceSpec `json:"workspace,omitempty"`
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

	// UsageCaptureParentUID is the authenticated parent Session UID when child
	// usage capture is enabled. The API sets it from that parent object. It is
	// not a permission and it is not taken from the request body.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="usage capture parent UID is immutable"
	// +optional
	UsageCaptureParentUID string `json:"usageCaptureParentUID,omitempty"`

	// CoordinatorInstructionParentUID is the authenticated parent Session UID
	// captured at create from a live read of that parent. It designates who may
	// submit in-scope coordinator instructions. It is not a privilege grant, not
	// a usage-capture permission, and not taken from the request body. Absent
	// means the task has no instruction binding and submission fails closed.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; coordinator instruction parent UID does not change"
	// +optional
	CoordinatorInstructionParentUID string `json:"coordinatorInstructionParentUID,omitempty"`

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

	// Restore is a rescue on the shared volume, <session>/<stamp> (D-67). On
	// the session's first boot agentd fetches that rescue's bundle for Repo
	// into refs/rescued/*, after checking it against the rescue's manifest.
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?/[0-9]{8}-[0-9]{4}(-[0-9]+)?$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="immutable after create; only operatingMode and lifecycle may change"
	// +optional
	Restore string `json:"restore,omitempty"`
}

// PrivatePilotBinding binds the immutable two-slot policy to retained inputs.
type PrivatePilotBinding struct {
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`
	SealUID string `json:"sealUID"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	PolicyDigest string `json:"policyDigest"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	CommonRevision string `json:"commonRevision"`
	// +kubebuilder:validation:Enum=0;1
	Slot int `json:"slot"`
}

// SessionDirectoryReceipt prevents reconstructing a lost pre-executor record.
// +kubebuilder:validation:XValidation:rule="oldSelf.state != 'Confirmed' || self.state == 'Confirmed'",message="directory confirmation is permanent"
// +kubebuilder:validation:XValidation:rule="self.state == 'Confirmed' ? size(self.uid) > 0 : size(self.uid) == 0",message="directory confirmation requires original record UID"
type SessionDirectoryReceipt struct {
	// +kubebuilder:validation:Enum=Preparing;Confirmed
	State string `json:"state"`
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original directory name is immutable"
	Name string `json:"name"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original directory digest is immutable"
	SpecDigest string `json:"specDigest"`
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="original directory UID cannot change"
	UID string `json:"uid"`
}

// SessionDirectoryEnrollment records prospective admission before execution.
type SessionDirectoryEnrollment struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`
	Project string `json:"project"`
}

// CommonRuleBinding retains only nonsecret accepted instruction source bytes.
type CommonRuleBinding struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	CommonRevision string `json:"commonRevision"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	SnapshotDigest string `json:"snapshotDigest"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	ProjectDigest string `json:"projectDigest"`
	// The entire source-authored session document has a separate 192KiB cap.
	// +kubebuilder:validation:MaxLength=499712
	Snapshot string `json:"snapshot"`
}

// ProjectCanaryBinding retains the causal inputs of one finite managed task.
type ProjectCanaryBinding struct {
	CaptureOutput bool `json:"captureOutput,omitempty"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +optional
	CommonRevision string `json:"commonRevision,omitempty"`
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	ID string `json:"id"`
	// +kubebuilder:validation:MaxLength=127
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?/[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Client string `json:"client"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	PolicyDigest string `json:"policyDigest"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	SnapshotDigest string `json:"snapshotDigest"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	CatalogUID string `json:"catalogUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	CatalogResourceVersion string `json:"catalogResourceVersion"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	CatalogRevision string `json:"catalogRevision"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	RulesRevision string `json:"rulesRevision"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	TemplateRevision string `json:"templateRevision"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Image string `json:"image"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	CloneOwner string `json:"cloneOwner"`
}

// WorkspaceSpec identifies retained workspace storage, never session-owned.
type WorkspaceSpec struct {
	// ID must match the enabled template and the mounted workspace marker.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	ID string `json:"id"`
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

// CoordinatorInstructionAttemptStatus is the recipient pod's current native
// attempt. The API writes it from that pod's token. It grants no privilege.
type CoordinatorInstructionAttemptStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="instruction attempt session UID is immutable"
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// +kubebuilder:validation:MinLength=36
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	AttemptID string `json:"attemptID"`
	// +kubebuilder:validation:Enum=active;ended
	Phase string `json:"phase"`
}

// CoordinatorInstructionConsumptionStatus is the native task's consume receipt.
// It is not an inbox read acknowledgement and not a grant or human decision.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="native consumption receipt is immutable"
type CoordinatorInstructionConsumptionStatus struct {
	// +kubebuilder:validation:MinLength=36
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	AttemptID string `json:"attemptID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	MessageUID string `json:"messageUID"`
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Nonce string `json:"nonce"`
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	TextSHA256 string      `json:"textSHA256"`
	At         metav1.Time `json:"at"`
}

// CoordinatorInstructionStatus is one server-authored delivery record. The
// instruction text stays in the inbox message. Consumption is added once.
// +kubebuilder:validation:XValidation:rule="!has(self.consumed) || (self.consumed.attemptID == self.attemptID && self.consumed.sessionUID == self.targetSessionUID && self.consumed.podUID == self.targetPodUID && self.consumed.messageUID == self.messageUID && self.consumed.nonce == self.nonce && self.consumed.textSHA256 == self.textSHA256)",message="native consumption must bind this delivery"
// +kubebuilder:validation:XValidation:rule="self.key == oldSelf.key && self.messageName == oldSelf.messageName && self.messageUID == oldSelf.messageUID && self.senderUID == oldSelf.senderUID && self.senderPodUID == oldSelf.senderPodUID && self.targetSessionUID == oldSelf.targetSessionUID && self.targetPodUID == oldSelf.targetPodUID && self.attemptID == oldSelf.attemptID && self.project == oldSelf.project && self.repository == oldSelf.repository && self.taskKey == oldSelf.taskKey && self.textSHA256 == oldSelf.textSHA256 && self.nonce == oldSelf.nonce && self.deliveredAt == oldSelf.deliveredAt && (!has(oldSelf.consumed) || (has(self.consumed) && self.consumed == oldSelf.consumed))",message="instruction delivery is immutable; consumption is set once"
type CoordinatorInstructionStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=120
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([a-z0-9._-]{0,118}[a-z0-9])?$`
	Key string `json:"key"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=80
	// +kubebuilder:validation:Pattern=`^message\.[a-f0-9]{32}\.[a-f0-9]{32}$`
	MessageName string `json:"messageName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9-]+$`
	MessageUID string `json:"messageUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SenderUID string `json:"senderUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SenderPodUID string `json:"senderPodUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	TargetSessionUID string `json:"targetSessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	TargetPodUID string `json:"targetPodUID"`
	// +kubebuilder:validation:MinLength=36
	// +kubebuilder:validation:MaxLength=36
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`
	AttemptID string `json:"attemptID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Project string `json:"project"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	Repository string `json:"repository"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=160
	TaskKey string `json:"taskKey"`
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	TextSHA256 string `json:"textSHA256"`
	// +kubebuilder:validation:MinLength=64
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Nonce       string      `json:"nonce"`
	DeliveredAt metav1.Time `json:"deliveredAt"`
	// +optional
	Consumed *CoordinatorInstructionConsumptionStatus `json:"consumed,omitempty"`
}

// AgentSessionStatus is what the operator and agentd observed. Reconcile is
// level-based (DESIGN-001 5.1): everything a fresh operator needs is here, not
// in its memory.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.sharedAdmission) || has(self.sharedAdmission)",message="shared admission evidence cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.privateHistory) || has(self.privateHistory)",message="private history retention cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.capturedUsage) || has(self.capturedUsage)",message="captured usage ledger cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.sessionDirectory) || has(self.sessionDirectory)",message="directory creation evidence cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.coordinatorInstructionAttempt) || has(self.coordinatorInstructionAttempt)",message="instruction attempt evidence cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.coordinatorInstructions) || (has(self.coordinatorInstructions) && size(self.coordinatorInstructions) >= size(oldSelf.coordinatorInstructions) && oldSelf.coordinatorInstructions.all(old, self.coordinatorInstructions.exists(cur, cur.key == old.key)))",message="instruction delivery records cannot be removed or replaced"
type AgentSessionStatus struct {
	// +optional
	SessionDirectory *SessionDirectoryReceipt `json:"sessionDirectory,omitempty"`
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

	// SharedAdmission records whether this exact fresh shared lifecycle has
	// attempted a resource write. Absence is unknown, never NeverStarted.
	// +optional
	SharedAdmission *SharedAdmissionStatus `json:"sharedAdmission,omitempty"`

	// SharedPrivateHomeUID is bound durably before the first shared executor
	// is admitted. A replacement claim is never adopted into this lifecycle.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	SharedPrivateHomeUID string `json:"sharedPrivateHomeUID,omitempty"`

	// PrivateHistory is the operator-latched private HOME preservation boundary.
	// It is provenance only; it grants no context access or private stop proof.
	// +optional
	PrivateHistory *PrivateHistoryStatus `json:"privateHistory,omitempty"`

	// NodeName is the node the pod runs on.
	// +optional
	NodeName string `json:"nodeName,omitempty"`

	// Agent is the agent's own state, from agentd's heartbeat.
	// +optional
	Agent *AgentStatus `json:"agent,omitempty"`

	// TaskResult is how a task-mode session's task ended, derived by the
	// operator from agent.task (DESIGN-021). Phase stays the pod's: a session
	// whose pod ended is Failed whatever its task did (D-44), so a canary that
	// succeeded and one that failed differ here, not in Phase.
	// +optional
	TaskResult *TaskResultStatus `json:"taskResult,omitempty"`

	// RemoteControl is the session's Remote Control entry, for remote mode
	// (DESIGN-001 6.7).
	// +optional
	RemoteControl *RemoteControlStatus `json:"remoteControl,omitempty"`

	// Outcome is what a summoned session reported about its work.
	// +optional
	Outcome *OutcomeStatus `json:"outcome,omitempty"`

	// DecisionAnswer is written only by the authenticated direct-parent API route.
	// Heartbeat reports cannot create or modify it. No private text is stored.
	// +optional
	DecisionAnswer *DecisionAnswerStatus `json:"decisionAnswer,omitempty"`

	// Usage is the session's cost record (DESIGN-001 3.7, V-16).
	// +optional
	Usage *UsageStatus `json:"usage,omitempty"`

	// TaskOutcome is the immutable task-authored exit snapshot.
	// +optional
	TaskOutcome *TaskOutcomeStatus `json:"taskOutcome,omitempty"`

	// UsageObservation retains one explicitly attributed initial CLI snapshot.
	// +optional
	UsageObservation *UsageObservationStatus `json:"usageObservation,omitempty"`
	// UsageConflict is a bounded unavailable/conflict receipt, never a new total.
	// +optional
	UsageConflict *UsageConflictStatus `json:"usageConflict,omitempty"`

	// ConversationUsage is the registered interactive conversation's cumulative
	// token usage as agentd last measured it (DESIGN-029). It is separate from
	// usageObservation, which covers a headless task's first invocation, and the
	// two are never added. It is written only by the session's own heartbeat,
	// replaced each time agentd reports, and absent for a task session and for an
	// agentd that does not measure it.
	// +optional
	ConversationUsage *ConversationUsageStatus `json:"conversationUsage,omitempty"`

	// CapturedUsage is the parent's append-only ledger of direct children's
	// accepted initial observations (OpenProject #109). It is absent unless
	// capture is enabled. It is not a usage total.
	// +optional
	CapturedUsage *CapturedUsageStatus `json:"capturedUsage,omitempty"`

	// Rescue records the session's newest rescue (D-10, D-51).
	// +optional
	Rescue *RescueStatus `json:"rescue,omitempty"`

	// Restart is the prepare-restart proof for an idle drain (DESIGN-001 5.2).
	// The operator writes it before deletePod may remove that pod. It names
	// the conversation the same volume resumes and the pod the proof was taken in.
	// +optional
	Restart *RestartStatus `json:"restart,omitempty"`

	// SuspendedAt is when the operator first saw the session suspended with
	// no pod of its own: the start of its archive timer (D-09, D-62). A resume
	// clears it.
	// +optional
	SuspendedAt *metav1.Time `json:"suspendedAt,omitempty"`

	// ArchivedAt is when the archive timer deleted the suspended session's
	// volume, after a valid rescue (D-62). An archived session is never
	// resumed: a new volume would start its task again, so it is restored
	// from its bundle instead.
	// +optional
	ArchivedAt *metav1.Time `json:"archivedAt,omitempty"`

	// The operator's condition types are named in internal/controller:
	// PodReady, Outdated, RemovalBlocked, and RescueFailed, the rescueFailed
	// mark of D-10 that blocks archive (D-51). TODO(7.3): QuotaExhausted,
	// with agentd's quota detection.

	// Conditions are the standard conditions.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// CoordinatorInstructionAttempt is the recipient pod's current native attempt
	// for instruction delivery. Absence fails closed. It is not a grant.
	// +optional
	CoordinatorInstructionAttempt *CoordinatorInstructionAttemptStatus `json:"coordinatorInstructionAttempt,omitempty"`

	// CoordinatorInstructions are delivery records for this task. The text lives
	// in the inbox message. A nested consumption receipt is not an inbox read ACK.
	// The list is a map keyed by the immutable delivery key, so item and nested
	// consumption transition rules stay correlatable. MaxItems stays 4. A later
	// update cannot drop the list, remove an old key, or swap an old key for a
	// new key at the same length. Duplicate keys are rejected.
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=key
	// +optional
	CoordinatorInstructions []CoordinatorInstructionStatus `json:"coordinatorInstructions,omitempty"`
}

// PrivateHistoryStatus retains private identity independently of live Pod fields.
// No transition in this slice releases the retention boundary.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.lastExecutor) || has(self.lastExecutor)",message="last private executor cannot be erased"
// +kubebuilder:validation:XValidation:rule="self.admission == 'Started' || size(self.homeUID) == 0",message="unknown original HOME cannot be inferred"
// +kubebuilder:validation:XValidation:rule="!self.executorAttempted || (self.admission == 'Started' && size(self.executorIntent) > 0)",message="executor attempt requires one durable causal intent"
// +kubebuilder:validation:XValidation:rule="!has(self.executorNotCreated) || !self.executorNotCreated || (self.executorAttempted && !has(self.lastExecutor))",message="only an executor attempt with no observed executor can be recorded as not created"
// +kubebuilder:validation:XValidation:rule="!has(self.executorTerminal) || (has(self.lastExecutor) && self.executorTerminal.podUID == self.lastExecutor.podUID)",message="the executor terminal record must name the recorded executor"
// +kubebuilder:validation:XValidation:rule="!has(self.executorMissed) || (has(self.lastExecutor) && self.executorMissed.podUID == self.lastExecutor.podUID)",message="the missed executor marker must name the recorded executor"
// +kubebuilder:validation:XValidation:rule="!has(self.executorTerminal) || !has(self.executorMissed)",message="an executor cannot be both recorded terminal and missed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.executorTerminal) || (has(self.executorTerminal) && self.executorTerminal == oldSelf.executorTerminal)",message="the executor terminal record cannot change or be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.executorMissed) || (has(self.executorMissed) && self.executorMissed == oldSelf.executorMissed)",message="the missed executor marker cannot change or be removed"
// +kubebuilder:validation:XValidation:rule="!has(self.stopRequest) || (has(self.lastExecutor) && self.stopRequest.podUID == self.lastExecutor.podUID)",message="the executor stop request must name the recorded executor"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.stopRequest) || (has(self.stopRequest) && self.stopRequest == oldSelf.stopRequest)",message="the executor stop request cannot change or be removed"
// +kubebuilder:validation:XValidation:rule="!has(self.expiry) || (has(self.executorTerminal) && has(self.lastExecutor) && self.expiry.executorUID == self.executorTerminal.podUID && self.expiry.executorUID == self.lastExecutor.podUID)",message="the HOME expiry record must rest on the recorded executor's terminal record"
// +kubebuilder:validation:XValidation:rule="!has(self.expiry) || self.expiry.homeUID == self.homeUID",message="the HOME expiry record must name the bound HOME"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.expiry) || has(self.expiry) || !(oldSelf.expiry.phase in ['Deleting', 'Expired'])",message="the HOME expiry record cannot be removed once Deleting"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.expiryClearedAt) || (has(self.expiryClearedAt) && self.expiryClearedAt >= oldSelf.expiryClearedAt)",message="expiryClearedAt cannot be removed or move back"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.expiry) || has(self.expiry) || (has(self.expiryClearedAt) && (!has(oldSelf.expiryClearedAt) || self.expiryClearedAt > oldSelf.expiryClearedAt))",message="removing the HOME expiry record (a cancel) records a newer expiryClearedAt in the same write"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.taskTerminal) || (has(self.taskTerminal) && self.taskTerminal == oldSelf.taskTerminal)",message="the terminal task observation cannot change or be removed"
// +kubebuilder:validation:XValidation:rule="!has(self.taskTerminal) || (has(self.lastExecutor) && self.taskTerminal.task.exitSource.podUID == self.lastExecutor.podUID && self.taskTerminal.task.exitSource.sessionUID == self.sessionUID && self.taskTerminal.task.exitSource.provider == self.provider)",message="the terminal task must name the original Session and executor"
type PrivateHistoryStatus struct {
	// +kubebuilder:validation:Enum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="retention version is immutable"
	Version int `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="retention Session UID is immutable"
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original HOME name is immutable"
	HomeClaim string `json:"homeClaim"`
	// Empty means the original claim was not proven before executor admission.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="original HOME UID cannot change"
	HomeUID string `json:"homeUID"`
	// BindingGeneration is the Session generation of fresh resource admission.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="binding generation is immutable"
	BindingGeneration int64 `json:"bindingGeneration"`
	// Unknown old lifecycles cannot be migrated by guessing their original HOME.
	// +kubebuilder:validation:Enum=Unknown;NeverStarted;Started
	// +kubebuilder:validation:XValidation:rule="oldSelf == 'NeverStarted' || self == oldSelf",message="retention admission cannot be reversed or inferred"
	Admission string `json:"admission"`
	// ExecutorAttempted is latched before the first private Pod create.
	// Missing or unacknowledged executors never authorize a replacement model.
	// +kubebuilder:validation:XValidation:rule="!oldSelf || self",message="executor attempt cannot be reversed"
	ExecutorAttempted bool `json:"executorAttempted"`
	// ExecutorIntent identifies this single causal Pod creation attempt.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="oldSelf == '' || self == oldSelf",message="executor intent cannot change"
	ExecutorIntent string `json:"executorIntent"`
	// ExecutorNotCreated records a known non-attempt (DESIGN-019): the operator
	// returned before calling Create for ExecutorIntent because an observation
	// failed. Only then may admission re-arm that same intent, once per mark. A
	// Create that was called, whatever it returned, never sets it, and it is
	// refused once an executor was observed.
	// +optional
	ExecutorNotCreated bool `json:"executorNotCreated,omitempty"`
	// Provider identifies the configured provider without starting it.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="retention provider is immutable"
	Provider string `json:"provider"`
	// StopState stays Unknown: executorTerminal is evidence for HOME expiry
	// and gated compute reclaim, never DESIGN-010's stop contract (DESIGN-025).
	// +kubebuilder:validation:Enum=Unknown
	StopState string `json:"stopState"`
	// TaskTerminal is the confirmed original provider exit and outcome. It
	// authorizes compute reclaim only with its separate default-off flag.
	// +optional
	TaskTerminal *PrivateTaskTerminalStatus `json:"taskTerminal,omitempty"`
	// +optional
	LastExecutor *PrivateExecutorStatus `json:"lastExecutor,omitempty"`
	// ExecutorTerminal is the durable terminal record of the recorded executor
	// (DESIGN-025 section 3): the operator's own uncached observation that the
	// Pod named by lastExecutor.podUID reached a terminal phase, every container
	// terminated, on a Ready node with a fresh lease. It sits beside
	// lastExecutor, which every observation rebuilds. It is evidence for HOME
	// expiry and gated compute reclaim: stopState stays Unknown, with no claim
	// transfer, resume or replacement. Once written it never changes or goes.
	// +optional
	ExecutorTerminal *PrivateExecutorTerminalStatus `json:"executorTerminal,omitempty"`
	// ExecutorMissed records once that an uncached read found the recorded
	// executor gone before its terminal record was written. It never changes
	// or goes, and no terminal record is ever written beside it: absence never
	// proves stop.
	// +optional
	ExecutorMissed *PrivateExecutorMissedStatus `json:"executorMissed,omitempty"`
	// StopRequest is the operator's recorded intent to stop the recorded
	// executor of a Reaping Session (DESIGN-025 section 4). It is written and
	// confirmed before the one Pod patch that sets activeDeadlineSeconds, so an
	// owner hold written first makes it conflict. It names lastExecutor.podUID,
	// never changes and is never removed. It is not stop proof: only
	// executorTerminal is.
	// +optional
	StopRequest *PrivateExecutorStopRequestStatus `json:"stopRequest,omitempty"`
	// Expiry is the HOME expiry record (DESIGN-025 5.1). It exists only while
	// the Session is eligible and triggered: Reaping, or Suspended past its
	// archive deadline, with a terminal record. It rests on executorTerminal
	// and names the bound HOME. Its phase moves only forward, its archive
	// never changes once set, and it can be removed (a cancel) only before
	// Deleting.
	// +optional
	Expiry *PrivateHomeExpiryStatus `json:"expiry,omitempty"`
	// ExpiryClearedAt is when the last expiry record was cancelled (DESIGN-025
	// 5.3). A record made after it starts its own grace. It is never removed
	// and never moves back, and a cancel must set a newer one in the write
	// that removes the record, so an operator that does not know these
	// fields cannot drop them, and with them the cancel (section 14).
	// +optional
	ExpiryClearedAt *metav1.Time `json:"expiryClearedAt,omitempty"`
}

// The phases of a HOME expiry record (DESIGN-025 5.1), in their only order.
const (
	HomeExpiryWaiting   = "Waiting"
	HomeExpiryRescuing  = "Rescuing"
	HomeExpiryExporting = "Exporting"
	HomeExpiryArchived  = "Archived"
	HomeExpiryDeleting  = "Deleting"
	HomeExpiryExpired   = "Expired"
)

// PrivateHomeExpiryStatus is the HOME expiry record of a private Session
// (DESIGN-025 5.1): the grace, the step reached, and what each step relied on.
// +kubebuilder:validation:XValidation:rule="self.executorUID == oldSelf.executorUID && self.homeUID == oldSelf.homeUID && self.graceStart == oldSelf.graceStart && self.dueAt == oldSelf.dueAt",message="a HOME expiry record's executor, HOME and grace never change"
// +kubebuilder:validation:XValidation:rule="{'Waiting': 0, 'Rescuing': 1, 'Exporting': 2, 'Archived': 3, 'Deleting': 4, 'Expired': 5}[self.phase] >= {'Waiting': 0, 'Rescuing': 1, 'Exporting': 2, 'Archived': 3, 'Deleting': 4, 'Expired': 5}[oldSelf.phase]",message="a HOME expiry record's phase moves only forward"
// +kubebuilder:validation:XValidation:rule="oldSelf.phase != 'Expired' || self == oldSelf",message="an Expired HOME expiry record never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.archive) || (has(self.archive) && self.archive == oldSelf.archive)",message="a HOME expiry record's archive never changes once set"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.releaseNowAt) || (has(self.releaseNowAt) && self.releaseNowAt == oldSelf.releaseNowAt)",message="a HOME expiry record's releaseNowAt never changes once set"
// +kubebuilder:validation:XValidation:rule="!(self.phase in ['Exporting', 'Archived', 'Deleting', 'Expired']) || size(self.rescueStamp) > 0",message="a HOME expiry record past Rescuing names its rescue"
// +kubebuilder:validation:XValidation:rule="!(self.phase in ['Archived', 'Deleting', 'Expired']) || has(self.archive)",message="an Archived HOME expiry record holds its archive"
// +kubebuilder:validation:XValidation:rule="!(self.phase in ['Deleting', 'Expired']) || has(self.deletingAt)",message="a Deleting HOME expiry record has deletingAt"
// +kubebuilder:validation:XValidation:rule="self.phase != 'Expired' || has(self.homeDeletedAt)",message="an Expired HOME expiry record has homeDeletedAt"
// +kubebuilder:validation:XValidation:rule="has(self.refusal) == has(self.refusalAt)",message="a HOME expiry record's refusal and refusalAt go together"
type PrivateHomeExpiryStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// ExecutorUID is the terminal record the expiry rests on; it equals
	// lastExecutor.podUID and executorTerminal.podUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ExecutorUID string `json:"executorUID"`
	// HomeUID equals privateHistory.homeUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	HomeUID string `json:"homeUID"`
	// GraceStart is executorTerminal.observedAt for the Session's first
	// record, and the record's creation for one made after a cancel.
	GraceStart metav1.Time `json:"graceStart"`
	// DueAt is graceStart plus the grace in force when the record was made.
	DueAt metav1.Time `json:"dueAt"`
	// +kubebuilder:validation:Enum=Waiting;Rescuing;Exporting;Archived;Deleting;Expired
	Phase string `json:"phase"`
	// ReleaseNowAt is when the owner's release-now was first seen for this
	// record. It skips the grace and nothing else.
	// +optional
	ReleaseNowAt *metav1.Time `json:"releaseNowAt,omitempty"`
	// HoldIntent is written by a confirmed write before the hold pod is
	// created, and set on the pod as an annotation, so a pod created before an
	// operator restart is adopted, never mistaken for a new Pod (6.1).
	// +kubebuilder:validation:MaxLength=64
	// +optional
	HoldIntent string `json:"holdIntent,omitempty"`
	// HoldPodUID is the hold pod the expiry made, recorded once observed.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	HoldPodUID string `json:"holdPodUID,omitempty"`
	// RescueStamp is the rescue the export relies on (status.rescue.stamp).
	// +kubebuilder:validation:MaxLength=64
	// +optional
	RescueStamp string `json:"rescueStamp,omitempty"`
	// Archive is the verified history archive entry.
	// +optional
	Archive *PrivateHomeArchiveStatus `json:"archive,omitempty"`
	// +optional
	DeletingAt *metav1.Time `json:"deletingAt,omitempty"`
	// +optional
	HomeDeletedAt *metav1.Time `json:"homeDeletedAt,omitempty"`
	// Refusal is the current refusal reason (DESIGN-025 section 10), cleared
	// by the confirmed write of the next step that passes.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Refusal string `json:"refusal,omitempty"`
	// +optional
	RefusalAt *metav1.Time `json:"refusalAt,omitempty"`
}

// PrivateHomeArchiveStatus is the history archive entry an expiry wrote and
// verified (DESIGN-025 6.4).
type PrivateHomeArchiveStatus struct {
	// Entry is the entry's directory relative to the archive's root.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	Entry string `json:"entry"`
	// ContentSHA256 is the SHA-256 of the entry's content.json.
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	ContentSHA256 string `json:"contentSHA256"`
	// +kubebuilder:validation:Minimum=0
	Bytes int64 `json:"bytes"`
	// +kubebuilder:validation:Minimum=0
	Files int32 `json:"files"`
	// GitFiles counts the Git rescue's files copied beside the history: its
	// manifest and bundles.
	// +kubebuilder:validation:Minimum=0
	GitFiles   int32       `json:"gitFiles"`
	ArchivedAt metav1.Time `json:"archivedAt"`
}

// PrivateExecutorStopRequestStatus records the stop intent for the recorded
// private executor (DESIGN-025 section 4).
type PrivateExecutorStopRequestStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// PodUID must equal lastExecutor.podUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// RequestedAt is the operator's clock when it first recorded the intent.
	RequestedAt metav1.Time `json:"requestedAt"`
}

// PrivateExecutorTerminalStatus is the operator's confirmed observation that
// the recorded private executor ended (DESIGN-025 3.1).
type PrivateExecutorTerminalStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// PodUID must equal lastExecutor.podUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// Phase is the Pod's terminal phase.
	// +kubebuilder:validation:Enum=Succeeded;Failed
	Phase string `json:"phase"`
	// Reason is the Pod's status.reason, such as DeadlineExceeded or Evicted.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason string `json:"reason,omitempty"`
	// PodResourceVersion is the observed Pod revision.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	PodResourceVersion string `json:"podResourceVersion"`
	// NodeName and NodeUID are the Ready node that reported the termination.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	NodeUID string `json:"nodeUID"`
	// LeaseRenewedAt is the node lease's renew time at the observation.
	LeaseRenewedAt metav1.Time `json:"leaseRenewedAt"`
	// ObservedAt is the operator's clock at the observation.
	ObservedAt metav1.Time `json:"observedAt"`
	// Containers are the regular, init and ephemeral containers' terminations.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	Containers []PrivateExecutorContainerTermination `json:"containers"`
	// ClaudeConfigDir and CodexHome are the executor's provider directories,
	// relative to HOME, recorded only when the agent container's env sets each
	// literally to a path inside the HOME volume; empty otherwise (6.3).
	// +kubebuilder:validation:MaxLength=256
	// +optional
	ClaudeConfigDir string `json:"claudeConfigDir,omitempty"`
	// +kubebuilder:validation:MaxLength=256
	// +optional
	CodexHome string `json:"codexHome,omitempty"`
}

// PrivateExecutorContainerTermination is one container's terminated state.
type PrivateExecutorContainerTermination struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name     string `json:"name"`
	ExitCode int32  `json:"exitCode"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason     string      `json:"reason,omitempty"`
	FinishedAt metav1.Time `json:"finishedAt"`
}

// PrivateExecutorMissedStatus records once that the recorded executor was
// found gone with no terminal record (DESIGN-025 3.4).
type PrivateExecutorMissedStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// PodUID must equal lastExecutor.podUID.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID     string      `json:"podUID"`
	ObservedAt metav1.Time `json:"observedAt"`
}

// PrivateExecutorStatus is an owned executor observation, not a stop receipt.
type PrivateExecutorStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original private executor UID cannot change"
	PodUID string `json:"podUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	PodName string `json:"podName"`
	// +kubebuilder:validation:MaxLength=253
	NodeName string `json:"nodeName,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	Revision string `json:"revision,omitempty"`
	// Generation is the Session generation at observation, not stop evidence.
	// +kubebuilder:validation:Minimum=1
	Generation int64 `json:"generation"`
	// +kubebuilder:validation:MaxLength=256
	ConversationID string `json:"conversationId,omitempty"`
}

// SharedAdmissionStatus is initialized only before D-45 adds the finalizer.
// Started is recorded and confirmed before any shared PVC or Pod write.
type SharedAdmissionStatus struct {
	// +kubebuilder:validation:Enum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="admission version is immutable"
	Version int `json:"version"`

	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="admission Session UID is immutable"
	SessionUID string `json:"sessionUID"`

	// +kubebuilder:validation:Enum=NeverStarted;Started
	// +kubebuilder:validation:XValidation:rule="oldSelf != 'Started' || self == 'Started'",message="Started admission cannot be reversed"
	State string `json:"state"`
}

// AgentStatus is the agent's own state. The /v1 API copies it from agentd's
// heartbeat (D-41, D-46); nothing else writes it.
type AgentStatus struct {
	// Status is the agent's state, agentd's: pending, busy, idle, waiting,
	// exited, failed or interrupted. idle and waiting are Claude's own, for a
	// TUI (D-59); plan 04 derives them for Codex.
	// +optional
	Status string `json:"status,omitempty"`

	// LastActivity is the last time agentd saw the agent or its worktree change.
	// +optional
	LastActivity *metav1.Time `json:"lastActivity,omitempty"`

	// Attached is how many clients are attached to the agent's tmux session
	// (DESIGN-001 4.2, D-59). Drain treats any attached client as not idle.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Attached int `json:"attached,omitempty"`

	// LastHeartbeat is when the operator last accepted a heartbeat from the
	// session's pod, by the operator's clock.
	// +optional
	LastHeartbeat *metav1.Time `json:"lastHeartbeat,omitempty"`

	// Agentd is agentd's version line.
	// +optional
	Agentd string `json:"agentd,omitempty"`

	// Boot is agentd's boot phase: booting, ready or failed.
	// +optional
	Boot string `json:"boot,omitempty"`

	// Problems are the boot steps that warned or failed, each as
	// "<step>: <state>", with its first note when it has one.
	// +listType=atomic
	// +optional
	Problems []string `json:"problems,omitempty"`

	// Branch is the worktree's branch, for example agent/haynes-ops-1005-202504.
	// +optional
	Branch string `json:"branch,omitempty"`

	// Head is the worktree's commit.
	// +optional
	Head string `json:"head,omitempty"`

	// ConversationID is the agent's own session id (claude --session-id), the
	// handle a resume uses.
	// +optional
	ConversationID string `json:"conversationId,omitempty"`

	// Task is how a task ended, once it has.
	// +optional
	Task *TaskStatus `json:"task,omitempty"`

	// Message is why agentd could not start the agent, when it could not.
	// +optional
	Message string `json:"message,omitempty"`

	// ProjectBootstrap is the boot's private catalog bootstrap, when it ran
	// (DESIGN-006, reason codes): catalog names, states, steps and closed-set
	// reason codes, never a path, an object id or detail text.
	// +optional
	ProjectBootstrap *ProjectBootstrapStatus `json:"projectBootstrap,omitempty"`
}

// ProjectBootstrapStatus is a private catalog bootstrap's outcome. The API
// server copies only known values from agentd's heartbeat.
type ProjectBootstrapStatus struct {
	// State is the catalog outcome.
	// +kubebuilder:validation:Enum=Ready;Partial;Failed
	// +required
	State string `json:"state"`

	// Reason is why a Failed bootstrap stopped before or between its items.
	// +kubebuilder:validation:Enum=ConfigInvalid;StateUncertain;LockUnavailable;ReportUnretained;CatalogUnconfirmed;CatalogInvalid;OwnerMismatch;TimedOut;Stopped
	// +optional
	Reason string `json:"reason,omitempty"`

	// CatalogRevision is the accepted catalog's content digest.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-f0-9]{64}$`
	// +optional
	CatalogRevision string `json:"catalogRevision,omitempty"`

	// Repositories are the catalog repositories' reference refreshes.
	// +kubebuilder:validation:MaxItems=128
	// +listType=atomic
	// +optional
	Repositories []ProjectBootstrapRepositoryStatus `json:"repositories,omitempty"`

	// Projects are the project roots' refreshes (DESIGN-007).
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +optional
	Projects []ProjectBootstrapProjectStatus `json:"projects,omitempty"`
}

// ProjectBootstrapRepositoryStatus is one repository's reference refresh.
type ProjectBootstrapRepositoryStatus struct {
	// Name is the repository's catalog name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// State is Ready, or Preserved when the refresh stopped and kept it.
	// +kubebuilder:validation:Enum=Ready;Preserved
	// +required
	State string `json:"state"`

	// Step is where a Preserved refresh stopped.
	// +kubebuilder:validation:Enum=Clone;Inspect;Fetch;Advance
	// +optional
	Step string `json:"step,omitempty"`

	// Reason is why a Preserved refresh stopped.
	// +kubebuilder:validation:Enum=CloneFailed;StagingPreserved;ReferenceUncertain;IdentityMismatch;GitOperationPresent;CatalogUnconfirmed;FetchFailed;UntrackedFiles;UnsupportedTrackedEntry;IndexBoundExceeded;WorkingTreeDiffers;StagedChanges;NonDefaultBranch;BranchInOtherWorktree;LocalOnlyCommits;ChangedDuringProof;AdvanceFailed;TimedOut;Stopped;Uncertain
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ProjectBootstrapProjectStatus is one project root's refresh.
type ProjectBootstrapProjectStatus struct {
	// Name is the project's catalog name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// State is Ready or Failed.
	// +kubebuilder:validation:Enum=Ready;Failed
	// +required
	State string `json:"state"`

	// Reason is why a Failed refresh stopped.
	// +kubebuilder:validation:Enum=StateUncertain;ReceiptUnconfirmed;CatalogUnconfirmed;RootUnproven;AnchorRemoved;RepositoryNotReady;FetchFailed;AnchorNotFastForward;RootPrepareFailed;AnchorAddFailed;AnchorUnproven;AnchorAdvanceFailed;RootAdvanceFailed;BudgetReserve;TimedOut;Stopped
	// +optional
	Reason string `json:"reason,omitempty"`

	// Repository is the member repository that caused it, when one did.
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`
	// +optional
	Repository string `json:"repository,omitempty"`

	// Cause is that repository's reason, when its clean-checkout or reference
	// proof refused.
	// +kubebuilder:validation:Enum=CloneFailed;StagingPreserved;ReferenceUncertain;IdentityMismatch;GitOperationPresent;CatalogUnconfirmed;FetchFailed;UntrackedFiles;UnsupportedTrackedEntry;IndexBoundExceeded;WorkingTreeDiffers;StagedChanges;NonDefaultBranch;BranchInOtherWorktree;LocalOnlyCommits;ChangedDuringProof;AdvanceFailed;TimedOut;Stopped;Uncertain
	// +optional
	Cause string `json:"cause,omitempty"`
}

// TaskStatus is how a task ended (D-42): the CLI's exit and its result event.
type TaskStatus struct {
	// ExitSource binds a post-Wait result to the original provider attempt.
	// +optional
	ExitSource *UsageSourceStatus `json:"exitSource,omitempty"`
	// ExitCode is the CLI's exit code.
	// +required
	ExitCode int32 `json:"exitCode"`

	// FinishedAt is when the CLI exited.
	// +required
	FinishedAt metav1.Time `json:"finishedAt"`

	// TimedOut is set when agentd stopped the task at limits.timeout.
	// +optional
	TimedOut bool `json:"timedOut,omitempty"`

	// Subtype is the result event's subtype: success, error_max_turns or
	// error_during_execution.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Subtype string `json:"subtype,omitempty"`

	// IsError is the result event's is_error.
	// +optional
	IsError bool `json:"isError,omitempty"`

	// NumTurns is the number of turns the task took.
	// +optional
	NumTurns int32 `json:"numTurns,omitempty"`
}

// PrivateTaskTerminalStatus preserves the task outcome independently of later
// heartbeats. The whole-Pod terminal record remains required before deletion.
// +kubebuilder:validation:XValidation:rule="has(self.task.exitSource)",message="terminal task requires original provider exit proof"
// +kubebuilder:validation:XValidation:rule="self.dueAt > self.observedAt && self.task.finishedAt <= self.observedAt",message="terminal task grace follows a real observed exit"
type PrivateTaskTerminalStatus struct {
	Task       TaskStatus  `json:"task"`
	ObservedAt metav1.Time `json:"observedAt"`
	DueAt      metav1.Time `json:"dueAt"`
}

// TaskResultStatus is the operator's reading of agent.task: a state, a
// CamelCase reason and one sentence. It is not Outcome, which is a summoned
// session's own report.
type TaskResultStatus struct {
	// State is the task's result.
	// +required
	State TaskResultState `json:"state"`

	// Reason is a CamelCase word, for example TaskSucceeded or TaskExitedNonZero.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason string `json:"reason,omitempty"`

	// Message says how the task ended, for people.
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Message string `json:"message,omitempty"`
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

// TokenObservationStatus preserves independent metric presence.
// +kubebuilder:validation:XValidation:rule="(self.state == 'Reported') == has(self.amount)",message="reported token count requires an amount; Unknown omits it"
type TokenObservationStatus struct {
	// +kubebuilder:validation:Enum=Unknown;Reported
	// +kubebuilder:validation:MaxLength=64
	State string `json:"state"`
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000000000
	// +optional
	Amount *int64 `json:"amount,omitempty"`
}

// ModelUsageStatus is one provider-reported served model's share of the
// initial invocation, with independent counter and cost presence.
type ModelUsageStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9._:/@+\[\]-]*$`
	Model                    string                 `json:"model"`
	InputTokens              TokenObservationStatus `json:"inputTokens"`
	OutputTokens             TokenObservationStatus `json:"outputTokens"`
	CacheReadInputTokens     TokenObservationStatus `json:"cacheReadInputTokens"`
	CacheCreationInputTokens TokenObservationStatus `json:"cacheCreationInputTokens"`
	Cost                     CostObservationStatus  `json:"cost"`
}

// CostObservationStatus is a CLI equivalent estimate, never billed dollars.
// +kubebuilder:validation:XValidation:rule="self.state == 'Reported' ? (has(self.amount) && has(self.unit) && self.unit == 'USD' && self.kind == 'ProviderCLIEquivalentEstimate') : (!has(self.amount) && !has(self.unit) && self.kind == 'Unknown')",message="cost requires explicit presence and CLI estimate provenance"
type CostObservationStatus struct {
	// +kubebuilder:validation:Enum=Unknown;Reported
	// +kubebuilder:validation:MaxLength=64
	State string `json:"state"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^(0|[1-9][0-9]*)(\.[0-9]*[1-9])?$`
	// +optional
	Amount string `json:"amount,omitempty"`
	// +kubebuilder:validation:Enum=USD
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Unit string `json:"unit,omitempty"`
	// +kubebuilder:validation:Enum=Unknown;ProviderCLIEquivalentEstimate
	// +kubebuilder:validation:MaxLength=64
	Kind string `json:"kind"`
}

type UsageSourceStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// +kubebuilder:validation:Enum=claude;codex
	// +kubebuilder:validation:MaxLength=64
	Provider string `json:"provider"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	RequestedModel string `json:"requestedModel"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	AttemptID string `json:"attemptID"`
}

// UsageObservationStatus is immutable publisher evidence for the first
// managed invocation. Version 2 records the provider-reported served models
// and billing basis (DESIGN-018, 2026-10-12). It never attests a charge,
// subscription quota or another Session's usage.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="original usage observation cannot change"
// +kubebuilder:validation:XValidation:rule="has(self.logicalTaskUID) == has(self.logicalTaskEpoch) && has(self.logicalTaskUID) == has(self.workerID)",message="logical task provenance requires one complete server binding"
// +kubebuilder:validation:XValidation:rule="self.source.provider != 'codex' || (self.cost.state == 'Unknown' && (!has(self.servedModels) || self.servedModels.all(m, m.cost.state == 'Unknown')))",message="Codex token-only usage cannot assert cost"
// +kubebuilder:validation:XValidation:rule="self.version == 1 ? (!has(self.provenance) && !has(self.billingBasis) && !has(self.servedModels) && self.actualModelState == 'Unknown') : (has(self.provenance) && has(self.billingBasis))",message="version 2 requires provenance and billing basis; version 1 carries neither"
// +kubebuilder:validation:XValidation:rule="(self.actualModelState == 'ProviderReported') == (has(self.servedModels) && size(self.servedModels) > 0)",message="actual model is ProviderReported exactly when served models are listed"
type UsageObservationStatus struct {
	// +kubebuilder:validation:Enum=1;2
	Version int32 `json:"version"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	ID         string            `json:"id"`
	Source     UsageSourceStatus `json:"source"`
	ObservedAt metav1.Time       `json:"observedAt"`
	// +kubebuilder:validation:Enum=InitialManagedInvocation
	// +kubebuilder:validation:MaxLength=64
	Coverage string `json:"coverage"`
	// +kubebuilder:validation:Enum=InvocationFinalTotal
	// +kubebuilder:validation:MaxLength=64
	CounterSemantics string `json:"counterSemantics"`
	// +kubebuilder:validation:Enum=Final;Partial
	// +kubebuilder:validation:MaxLength=64
	ResultState              string                 `json:"resultState"`
	InputTokens              TokenObservationStatus `json:"inputTokens"`
	OutputTokens             TokenObservationStatus `json:"outputTokens"`
	CacheReadInputTokens     TokenObservationStatus `json:"cacheReadInputTokens"`
	CacheCreationInputTokens TokenObservationStatus `json:"cacheCreationInputTokens"`
	Cost                     CostObservationStatus  `json:"cost"`
	// +kubebuilder:validation:Enum=Unknown
	// +kubebuilder:validation:MaxLength=64
	AggregationScope string `json:"aggregationScope"`
	// +kubebuilder:validation:Enum=Unknown;ProviderReported
	// +kubebuilder:validation:MaxLength=64
	ActualModelState string `json:"actualModelState"`
	// +kubebuilder:validation:Enum=Unknown
	// +kubebuilder:validation:MaxLength=64
	SubscriptionQuotaState string `json:"subscriptionQuotaState"`
	// +kubebuilder:validation:Enum=Unknown
	// +kubebuilder:validation:MaxLength=64
	APIBillingState string `json:"apiBillingState"`
	// +kubebuilder:validation:Enum=Unknown
	// +kubebuilder:validation:MaxLength=64
	LocalResourceState string `json:"localResourceState"`
	// Provenance labels every Reported value: the serving CLI's final result.
	// +kubebuilder:validation:Enum=ProviderReported
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Provenance string `json:"provenance,omitempty"`
	// BillingBasis is Subscription for a plan-served launch; the cost is then a
	// CLI list-price equivalent, never a charge.
	// +kubebuilder:validation:Enum=Subscription;Unknown
	// +kubebuilder:validation:MaxLength=64
	// +optional
	BillingBasis string `json:"billingBasis,omitempty"`
	// ServedModels are the provider-reported models, sorted by id.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=model
	// +optional
	ServedModels []ModelUsageStatus `json:"servedModels,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	LogicalTaskUID string `json:"logicalTaskUID,omitempty"`
	// +kubebuilder:validation:MaxLength=20
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*$`
	// +optional
	LogicalTaskEpoch string `json:"logicalTaskEpoch,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	WorkerID string `json:"workerID,omitempty"`
}

// ConversationModelUsageStatus is one served model's cumulative counters over
// the registered conversation. Each counter keeps its own presence: Unknown is
// not zero. Cache counters are their own counters and are never added to input;
// for Codex, input already includes the cached tokens.
type ConversationModelUsageStatus struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Model                    string                 `json:"model"`
	InputTokens              TokenObservationStatus `json:"inputTokens"`
	OutputTokens             TokenObservationStatus `json:"outputTokens"`
	CacheReadInputTokens     TokenObservationStatus `json:"cacheReadInputTokens"`
	CacheCreationInputTokens TokenObservationStatus `json:"cacheCreationInputTokens"`
}

// ConversationUsageStatus is the measured usage of one registered interactive
// conversation: cumulative as of ObservedAt, per served model, with no cost, no
// requested model and no text. Final means every record was read and counted at
// that time (the conversation may still grow); Partial is a lower bound with a
// reason; Unknown carries no models and is never zero.
// +kubebuilder:validation:XValidation:rule="self.resultState == 'Unknown' ? (!has(self.models) || size(self.models) == 0) : (has(self.models) && size(self.models) > 0)",message="models are listed exactly when the result is not Unknown"
// +kubebuilder:validation:XValidation:rule="(self.resultState == 'Final') == !has(self.reasonCode)",message="a reason is given exactly when the result is not Final"
type ConversationUsageStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int32 `json:"version"`
	// +kubebuilder:validation:Enum=claude;codex
	// +kubebuilder:validation:MaxLength=64
	Provider   string      `json:"provider"`
	ObservedAt metav1.Time `json:"observedAt"`
	// +kubebuilder:validation:Enum=RegisteredConversation
	// +kubebuilder:validation:MaxLength=64
	Coverage string `json:"coverage"`
	// +kubebuilder:validation:Enum=ConversationCumulative
	// +kubebuilder:validation:MaxLength=64
	CounterSemantics string `json:"counterSemantics"`
	// +kubebuilder:validation:Enum=Final;Partial;Unknown
	// +kubebuilder:validation:MaxLength=64
	ResultState string `json:"resultState"`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-z][a-z_]*$`
	// +optional
	ReasonCode string `json:"reasonCode,omitempty"`
	// Models are the served models, sorted by id.
	// +kubebuilder:validation:MaxItems=8
	// +listType=map
	// +listMapKey=model
	// +optional
	Models []ConversationModelUsageStatus `json:"models,omitempty"`
	// Turns counts the provider calls that were counted.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	Turns int64 `json:"turns"`
	// Skipped counts records that could not be counted.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=1000000000
	Skipped int64 `json:"skipped"`
}

// Captured usage ledger limits. A larger ledger does not evict a row.
const (
	CapturedUsageMaxReceipts = 64
	CapturedUsageMaxBytes    = 262144
	CapturedUsageScope       = "CapturedDirectChildInitialInvocations"
	CapturedUsageCoverage    = "CaptureOnly"
	CapturedUsageRetention   = "ParentLifetime"
)

// CapturedUsageStatus is version 1 of the parent-lifetime receipt ledger.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.overflow) || !oldSelf.overflow || (has(self.overflow) && self.overflow)",message="captured usage overflow is sticky"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.receipts) || size(self.receipts) >= size(oldSelf.receipts)",message="captured usage receipts are append-only"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.receipts) || oldSelf.receipts.all(old, self.receipts.exists(new, new == old))",message="captured usage receipts do not change"
type CapturedUsageStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int32 `json:"version"`
	// +kubebuilder:validation:Enum=CapturedDirectChildInitialInvocations
	Scope string `json:"scope"`
	// +kubebuilder:validation:Enum=CaptureOnly
	Coverage string `json:"coverage"`
	// +kubebuilder:validation:Enum=ParentLifetime
	Retention string `json:"retention"`
	// +optional
	Overflow bool `json:"overflow,omitempty"`
	// +kubebuilder:validation:MaxItems=64
	// +listType=map
	// +listMapKey=key
	// +optional
	Receipts []CapturedUsageReceipt `json:"receipts,omitempty"`
}

// CapturedUsageReceipt is one accepted initial observation copied onto its parent.
type CapturedUsageReceipt struct {
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	Key string `json:"key"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	ParentUID string `json:"parentUID"`
	// +kubebuilder:validation:MaxLength=253
	// +optional
	ChildName   string                 `json:"childName,omitempty"`
	CapturedAt  metav1.Time            `json:"capturedAt"`
	Observation UsageObservationStatus `json:"observation"`
}

type UsageConflictStatus struct {
	// +kubebuilder:validation:Enum=Unknown
	// +kubebuilder:validation:MaxLength=64
	State string `json:"state"`
	// +kubebuilder:validation:Enum=InvalidIdentity;InvalidPayload;ConflictingSnapshot
	// +kubebuilder:validation:MaxLength=64
	Reason string `json:"reason"`
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	IncomingSHA256 string      `json:"incomingSHA256"`
	ObservedAt     metav1.Time `json:"observedAt"`
}

// RescueResult is a rescue's verdict (D-51).
// +kubebuilder:validation:Enum=Verified;CleanAndPushed;NoWorkAdmitted;Failed
type RescueResult string

const (
	// RescueVerified: a bundle on the shared volume covers every ref origin
	// lacked, and agentd checked it there (D-48).
	RescueVerified RescueResult = "Verified"
	// RescueCleanAndPushed: every clone fetched, every worktree clean and every
	// ref on origin, so nothing needed a bundle.
	RescueCleanAndPushed RescueResult = "CleanAndPushed"
	// RescueNoWorkAdmitted is shared-only proof of absent task work and refs,
	// never proof of an empty private home or a clean pushed worktree.
	RescueNoWorkAdmitted RescueResult = "NoWorkAdmitted"
	// RescueFailed: the rescue ran and could not make the work safe: a refused
	// worktree, a bundle that could not be written, a clone that could not
	// fetch, or an agent that would not stop. The volume is kept and archive
	// waits (D-10).
	RescueFailed RescueResult = "Failed"
)

// RestartStatus is prepare-restart's recorded conversation for one pod
// (DESIGN-001 5.2). The operator writes it before a drain deletes that pod.
// A missing conversation id is not proof.
type RestartStatus struct {
	// ConversationID is the conversation the next boot on this volume resumes.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	ConversationID string `json:"conversationId"`

	// PodUID is the pod prepare-restart ran in.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`

	// Generation is the session generation when the proof was recorded.
	// A spec change since then needs a new prepare-restart.
	// +kubebuilder:validation:Minimum=1
	Generation int64 `json:"generation"`

	// Stopped is set immediately before prepare-restart stops the agent.
	// A delete retry requires it. A proof without it has not stopped the agent.
	// +optional
	Stopped bool `json:"stopped,omitempty"`
}

// RescueStatus records a session's newest rescue (D-10, D-51). The operator
// writes it before it deletes the pod the rescue ran in, so a fresh operator
// sees what the last one decided.
type RescueStatus struct {
	// SharedProof binds a typed shared rescue to its admitted writer and private
	// home. Older records without it cannot release a retained private home.
	// +optional
	SharedProof *SharedRescueProof `json:"sharedProof,omitempty"`
	// PreservationKind records the distinct shared preparation result.
	// +kubebuilder:validation:Enum=NoWorkAdmitted;OwnedRefsPreserved
	// +optional
	PreservationKind string `json:"preservationKind,omitempty"`
	// SourcePodUID is the retained shared executor that the hold Pod rescued.
	// +optional
	SourcePodUID string `json:"sourcePodUID,omitempty"`
	// LastBundle is the newest rescue's manifest on the shared volume, for
	// example rescue/haynes-ops-1005-202504/20261006-0130/manifest.json (D-48).
	// It stays when a later rescue needed no bundle, or a new pod superseded
	// it, because a restore starts from it.
	// +optional
	LastBundle string `json:"lastBundle,omitempty"`

	// Result is the newest rescue's verdict.
	// +optional
	Result RescueResult `json:"result,omitempty"`

	// Message says what the rescue saved, or why it failed.
	// +optional
	Message string `json:"message,omitempty"`

	// Stamp is the rescue's stamp (UTC YYYYMMDD-HHMM); At is when the operator
	// recorded it.
	// +optional
	Stamp string `json:"stamp,omitempty"`
	// +optional
	At *metav1.Time `json:"at,omitempty"`

	// PodUID is the pod the rescue ran in. A suspend deletes that pod, and no
	// other, on this record.
	// +optional
	PodUID string `json:"podUID,omitempty"`

	// Generation is the session's metadata.generation when the rescue ran. A
	// spec change since then (a resume, say) means the pod may have done more
	// work, and its next removal needs a new rescue.
	// +optional
	Generation int64 `json:"generation,omitempty"`

	// Superseded is set as soon as the session asks for its pod again (a
	// resume), before the operator starts a new pod on the volume: from then
	// on the rescue does not cover what the volume holds, and a pod delete or
	// an archive needs a new one (D-10: an old bundle never counts).
	// +optional
	Superseded bool `json:"superseded,omitempty"`

	// UnpushedRefs is D-10 step 4's list: the local refs origin lacked at the
	// rescue, each with its clone and the bundle that holds it. At most 256 are
	// kept; OmittedRefs counts the rest. The verdict covers all of them.
	// +kubebuilder:validation:MaxItems=256
	// +optional
	UnpushedRefs []RescuedRef `json:"unpushedRefs,omitempty"`

	// OmittedRefs is how many refs did not fit in UnpushedRefs.
	// +kubebuilder:validation:Minimum=0
	// +optional
	OmittedRefs int32 `json:"omittedRefs,omitempty"`
}

// SharedRescueProof is the controller-checked evidence for retaining a shared
// task's private home. It contains identities and rescue locations only.
type SharedRescueProof struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// +kubebuilder:validation:MaxLength=128
	Workspace string `json:"workspace"`
	// +kubebuilder:validation:MaxLength=63
	Task string `json:"task"`
	// +kubebuilder:validation:MaxLength=100
	Repo string `json:"repo"`
	// +kubebuilder:validation:MaxLength=128
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MaxLength=128
	SourcePodUID string `json:"sourcePodUID"`
	// +kubebuilder:validation:MaxLength=128
	PrivateHomeUID string `json:"privateHomeUID"`
	// +kubebuilder:validation:Minimum=0
	WriterGeneration int64 `json:"writerGeneration"`
	// NoOwner is true only after verified refused admission with no durable owner.
	NoOwner bool `json:"noOwner"`
	// +kubebuilder:validation:Enum=TaskWorkPreserved;NoWorkAdmitted;OwnedRefsPreserved
	Kind string `json:"kind"`
	// Manifest is this rescue's verified manifest, never an inherited LastBundle.
	// +kubebuilder:validation:MaxLength=512
	// +optional
	Manifest string `json:"manifest,omitempty"`
}

// RescuedRef is one local ref origin lacked at a rescue.
type RescuedRef struct {
	// Repo is the clone, for example /home/dev/repos/haynes-ops.
	Repo string `json:"repo"`
	// Name is the ref, for example refs/heads/rescue/haynes-ops-1005-202504-20261006-0130,
	// or stash@{n} for a stash entry.
	Name string `json:"name"`
	// Commit is the commit it named.
	Commit string `json:"commit"`
	// Bundle is the bundle file on the shared volume that holds it.
	// +optional
	Bundle string `json:"bundle,omitempty"`
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
// +kubebuilder:printcolumn:name="Task",type=string,JSONPath=`.status.taskResult.state`
// +kubebuilder:printcolumn:name="Node",type=string,JSONPath=`.status.nodeName`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`,priority=1
// +kubebuilder:printcolumn:name="Lane",type=string,JSONPath=`.spec.lane`,priority=1
// +kubebuilder:printcolumn:name="Outcome",type=string,JSONPath=`.status.outcome.state`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.size() <= 63",message="metadata.name must be at most 63 characters: it is the pod's hostname"
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z0-9]([-a-z0-9]*[a-z0-9])?$')",message="metadata.name must be a DNS label (lowercase letters, digits and '-', no dots): it is the pod's hostname"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.sharedAdmission) || (has(self.status) && has(self.status.sharedAdmission))",message="recorded shared admission cannot be erased with status"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.sessionDirectory) || (has(self.status) && has(self.status.sessionDirectory))",message="directory evidence cannot be erased with status"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.privateHistory) || (has(self.status) && has(self.status.privateHistory))",message="private history retention cannot be erased with status"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.capturedUsage) || (has(self.status) && has(self.status.capturedUsage))",message="captured usage ledger cannot be erased with status"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.usageObservation) || (has(self.status) && has(self.status.usageObservation))",message="original usage observation cannot be erased"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.taskOutcome) || (has(self.status) && has(self.status.taskOutcome))",message="original task outcome cannot be erased"
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

// DecisionAnswerStatus is bounded operator-owned answer provenance. Reserved is
// a durable one-shot fence; only a verified local write permits Confirmed.
type DecisionAnswerStatus struct {
	// +kubebuilder:validation:Enum=1
	Version int `json:"version"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Session string `json:"session"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	SessionUID string `json:"sessionUID"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	PodUID string `json:"podUID"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
	DecisionID string `json:"decisionID"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
	ThreadID string `json:"threadID"`
	// +kubebuilder:validation:Minimum=1
	WriterGeneration int64 `json:"writerGeneration"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{64}$"
	Digest string `json:"digest"`
	// +kubebuilder:validation:Enum=Reserved;Confirmed
	Phase string `json:"phase"`
}
