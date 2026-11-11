package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CallerPolicy is one caller's contract (DESIGN-060 7.1, DESIGN-064 6.4).
// The front-door shape is Meatball's seven fields, all required together and
// unchanged. The summon shape is spec.summon. An object has exactly one.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:validation:XValidation:rule="!has(self.spec.summon) || self.metadata.name == self.spec.summon.serviceAccount.replace('/', '.')",message="metadata.name must be the summon service account with the slash replaced by a dot"
type CallerPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +required
	Spec CallerPolicySpec `json:"spec"`
}

// SummonKind is the mode a summon entry starts: task or remote.
// +kubebuilder:validation:Enum=task;remote
// +kubebuilder:validation:MaxLength=6
type SummonKind string

const (
	SummonKindTask   SummonKind = "task"
	SummonKindRemote SummonKind = "remote"
)

// SummonPriority is the admission priority of a summon entry (DESIGN-064 5.9).
// +kubebuilder:validation:Enum=urgent;normal;bulk
// +kubebuilder:validation:MaxLength=6
type SummonPriority string

const (
	SummonPriorityUrgent SummonPriority = "urgent"
	SummonPriorityNormal SummonPriority = "normal"
	SummonPriorityBulk   SummonPriority = "bulk"
)

// SummonOnUnreported is the outcome written when a summoned session does not
// report (DESIGN-064 6.4).
// +kubebuilder:validation:Enum=escalated;failed
// +kubebuilder:validation:MaxLength=9
type SummonOnUnreported string

const (
	SummonOnUnreportedEscalated SummonOnUnreported = "escalated"
	SummonOnUnreportedFailed    SummonOnUnreported = "failed"
)

// CallerPolicySpec is exactly one shape. The seven front-door fields have no
// defaults and are all present together; effort and route rights for that
// shape stay fixed by the API. A summon entry names its own effort.
//
// +kubebuilder:validation:XValidation:rule="(has(self.serviceAccount) && has(self.mode) && has(self.profile) && has(self.models) && has(self.maxConcurrent) && has(self.maxTimeout) && has(self.maxWorkers) && !has(self.summon)) || (!has(self.serviceAccount) && !has(self.mode) && !has(self.profile) && !has(self.models) && !has(self.maxConcurrent) && !has(self.maxTimeout) && !has(self.maxWorkers) && has(self.summon))",message="exactly one of the front-door shape (serviceAccount, mode, profile, models, maxConcurrent, maxTimeout, maxWorkers) and summon"
type CallerPolicySpec struct {
	// ServiceAccount, with Mode, Profile, Models, MaxConcurrent, MaxTimeout
	// and MaxWorkers, is the front-door shape. All seven are set together, or
	// none are, when spec.summon is set.
	// +kubebuilder:validation:Enum="dev-env-system/dev-env-meatball"
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// +kubebuilder:validation:Enum=task
	// +kubebuilder:validation:MaxLength=4
	// +optional
	Mode string `json:"mode,omitempty"`
	// +kubebuilder:validation:Enum=dev
	// +kubebuilder:validation:MaxLength=3
	// +optional
	Profile string `json:"profile,omitempty"`
	// Models has no default. The request must name one of these full IDs.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=3
	// +kubebuilder:validation:items:MaxLength=64
	// +kubebuilder:validation:items:Enum=claude-opus-5-5;gpt-6-astra;claude-fable-5-2
	// +optional
	Models []string `json:"models,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('4h')",message="maxTimeout must be a positive Go duration at most 4h"
	// +optional
	MaxTimeout string `json:"maxTimeout,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +optional
	MaxWorkers int32 `json:"maxWorkers,omitempty"`

	// Summon is the contract for one summoning caller (DESIGN-064 6.4).
	// +optional
	Summon *CallerPolicySummon `json:"summon,omitempty"`
}

// CallerPolicySummon is one policy for a summoning ServiceAccount. The object
// name is the account with the slash replaced by a dot.
//
// +kubebuilder:validation:XValidation:rule="self.sessions.all(a, self.sessions.exists_one(b, a.kind == b.kind && a.lane == b.lane))",message="(kind, lane) is unique"
// +kubebuilder:validation:XValidation:rule="has(self.limits.escalations) == self.sessions.exists(s, s.lane == 'escalation')",message="limits.escalations is required exactly when the policy has an escalation entry"
// +kubebuilder:validation:XValidation:rule="!self.sessions.exists(s, s.lane == 'remediation') || self.sessions.exists(s, s.lane == 'escalation')",message="a remediation entry requires an escalation entry"
type CallerPolicySummon struct {
	// ServiceAccount is <namespace>/<name>. The namespace is not dev-agents,
	// dev-env-system or dev-tools.
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?/[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`
	// +kubebuilder:validation:XValidation:rule="!self.matches('^(dev-agents|dev-env-system|dev-tools)/')",message="summon service account namespace is not dev-agents, dev-env-system or dev-tools"
	// +required
	ServiceAccount string `json:"serviceAccount"`

	// +required
	Limits CallerPolicySummonLimits `json:"limits"`

	// Sessions holds one to six entries. Each (kind, lane) pair appears once.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=6
	// +required
	Sessions []CallerPolicySummonSession `json:"sessions"`
}

// CallerPolicySummonLimits are the caller's pools, queues and hourly creates
// (DESIGN-064 5.9, 6.4). Queued and QueuedUrgent default to 4 when omitted.
// Escalations is set exactly when an escalation entry exists.
type CallerPolicySummonLimits struct {
	// Concurrent counts admitted, unreleased sessions of every entry except
	// the escalation entry.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +required
	Concurrent int32 `json:"concurrent"`

	// Escalations counts admitted, unreleased escalations, the platform's
	// filings included.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4
	// +optional
	Escalations *int32 `json:"escalations,omitempty"`

	// Queued counts Queued sessions of priority normal and bulk. 0 refuses
	// that create. The design default is 4.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=16
	// +optional
	Queued *int32 `json:"queued,omitempty"`

	// QueuedUrgent bounds Queued sessions of priority urgent, counted per
	// pool. It is never 0. The design default is 4.
	// +kubebuilder:default=4
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=16
	// +optional
	QueuedUrgent *int32 `json:"queuedUrgent,omitempty"`

	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=30
	// +required
	CreatesPerHour int32 `json:"createsPerHour"`
}

// CallerPolicySummonSession is one lane a caller may start. The brief template
// is fixed text; the API renders it and does not substitute into it (5.6).
// Remediation and curation are task; escalation and upgrade are remote.
// Effort uses the AgentSession effort pattern, ^[a-z]+$, not a closed enum.
// Omitted staleAfter on a remote upgrade entry means the design default of
// 180m; a structural default would land on every entry. maxHold's design
// defaults are 20m for remediation and 12h for curation; the schema requires
// the field there and bounds the duration.
//
// +kubebuilder:validation:XValidation:rule="(self.lane == 'remediation' && self.kind == 'task') || (self.lane == 'curation' && self.kind == 'task') || (self.lane == 'escalation' && self.kind == 'remote') || (self.lane == 'upgrade' && self.kind == 'remote')",message="remediation and curation are task; escalation and upgrade are remote"
// +kubebuilder:validation:XValidation:rule="!(self.lane == 'remediation' || self.lane == 'escalation') || self.priority == 'urgent'",message="priority urgent is required for lanes remediation and escalation"
// +kubebuilder:validation:XValidation:rule="self.namePrefixes.all(p, !p.startsWith('esc-rem-')) && ((self.lane == 'remediation' && self.namePrefixes.all(p, p.startsWith('rem-'))) || (self.lane == 'escalation' && self.namePrefixes.all(p, p.startsWith('esc-'))) || (self.lane == 'upgrade' && self.namePrefixes.all(p, p.startsWith('wo-') && !p.startsWith('wo-cigar-curate-'))) || (self.lane == 'curation' && self.namePrefixes.all(p, p.startsWith('wo-cigar-curate-'))))",message="namePrefixes are DNS prefixes for the lane (rem-, esc-, wo-, wo-cigar-curate-), an upgrade prefix does not start with wo-cigar-curate-, and none start with esc-rem-"
// +kubebuilder:validation:XValidation:rule="(self.kind == 'task') == has(self.timeout)",message="timeout is required for a task entry and not allowed for a remote entry"
// +kubebuilder:validation:XValidation:rule="self.kind == 'task' || !has(self.maxTurns)",message="maxTurns is for a task entry only"
// +kubebuilder:validation:XValidation:rule="!has(self.staleAfter) || (self.kind == 'remote' && self.lane == 'upgrade')",message="staleAfter is for a remote upgrade entry only"
// +kubebuilder:validation:XValidation:rule="(self.lane == 'remediation' || self.lane == 'curation') == has(self.maxHold)",message="maxHold is required for remediation and curation and not allowed for upgrade or escalation"
// +kubebuilder:validation:XValidation:rule="self.lane == 'remediation' ? self.onUnreported == 'escalated' : self.onUnreported == 'failed'",message="onUnreported is escalated for remediation and failed for every other lane"
type CallerPolicySummonSession struct {
	// +required
	Kind SummonKind `json:"kind"`
	// +required
	Lane Lane `json:"lane"`
	// +required
	Priority SummonPriority `json:"priority"`

	// NamePrefixes are DNS label prefixes ending in '-'. One to four, each at
	// most 32 characters. None may start with esc-rem-. An upgrade prefix
	// starts with wo- and does not start with wo-cigar-curate-.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:items:MinLength=2
	// +kubebuilder:validation:items:MaxLength=32
	// +kubebuilder:validation:items:Pattern=`^[a-z0-9]([a-z0-9-]*[a-z0-9])?-$`
	// +required
	NamePrefixes []string `json:"namePrefixes"`

	// Repo is the session's repository. The request cannot name another.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+$`
	// +kubebuilder:validation:XValidation:rule="self != '.' && self != '..'",message="repo is a repository name, not a path"
	// +required
	Repo string `json:"repo"`

	// Profile is ops only, until another summoned profile is designed.
	// +kubebuilder:validation:Enum=ops
	// +kubebuilder:validation:MaxLength=32
	// +required
	Profile string `json:"profile"`

	// +required
	Model CallerPolicySummonModel `json:"model"`

	// Effort is the AgentSession effort pattern. The API checks which level
	// the model takes; this schema does not.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-z]+$`
	// +required
	Effort string `json:"effort"`

	// Timeout is task only, required there, and at most 4h.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('4h')",message="timeout must be a positive Go duration at most 4h"
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// MaxTurns is task only. 0 means no cap.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=500
	// +optional
	MaxTurns *int32 `json:"maxTurns,omitempty"`

	// StaleAfter is a remote upgrade entry only, at most 24h. Omitted means
	// the design default of 180m when that entry is the one that applies.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('24h')",message="staleAfter must be a positive Go duration at most 24h"
	// +optional
	StaleAfter string `json:"staleAfter,omitempty"`

	// MaxHold is how long a Queued session may wait, at most 24h. Required
	// for remediation and curation. Not allowed for upgrade or escalation.
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:XValidation:rule="duration(self) > duration('0s') && duration(self) <= duration('24h')",message="maxHold must be a positive Go duration at most 24h"
	// +optional
	MaxHold string `json:"maxHold,omitempty"`

	// +required
	OnUnreported SummonOnUnreported `json:"onUnreported"`

	// +required
	Brief CallerPolicySummonBrief `json:"brief"`
}

// CallerPolicySummonModel is the entry's default Claude model and a different
// fallback. Both are full ids. An alias cannot match.
//
// +kubebuilder:validation:XValidation:rule="self.default != self.fallback",message="model fallback must differ from the default"
type CallerPolicySummonModel struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^claude-[a-z]+(-[0-9]+)+$`
	// +required
	Default string `json:"default"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^claude-[a-z]+(-[0-9]+)+$`
	// +required
	Fallback string `json:"fallback"`
}

// CallerPolicySummonBrief is the fixed template and the data field names a
// create may send (DESIGN-064 5.6). The template has no substitution syntax
// in this schema; the renderer copies it verbatim.
type CallerPolicySummonBrief struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=8192
	// +required
	Template string `json:"template"`

	// Fields names the data keys a create may send. Zero to eight, each unique.
	// +listType=set
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=32
	// +kubebuilder:validation:items:Pattern=`^[a-z][a-z0-9_]{0,31}$`
	// +optional
	Fields []string `json:"fields,omitempty"`
}

// +kubebuilder:object:root=true
type CallerPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CallerPolicy `json:"items"`
}
