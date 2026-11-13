package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// SummonStatus is the operator's durable accounting for a summoned session
// (DESIGN-064 5.9): when the leader admitted it, and its one terminal
// transition with the release of its reservation.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.admittedAt) || (has(self.admittedAt) && self.admittedAt == oldSelf.admittedAt)",message="admittedAt is set once and never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.closed) || has(self.closed)",message="the closed record cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.pages) || (has(self.pages) && oldSelf.pages.all(p, p in self.pages))",message="a page event is never removed or changed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.executor) || has(self.executor)",message="the executor latch cannot be removed"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.registration) || has(self.registration)",message="the registration record cannot be removed"
// +kubebuilder:validation:XValidation:rule="(has(self.registration) && has(self.registration.result)) == (has(self.pages) && self.pages.exists(p, p.event == 'escalation-started'))",message="pages hold escalation-started exactly when the registration has a result"
type SummonStatus struct {
	// AdmittedAt is when the operator leader admitted the session: its charge
	// was in the create ledger under its own name, its single-flight lane was
	// free and its pool had a slot (DESIGN-064 5.9, OP777). Until it is set the
	// session has no volume and no pod. It is set once and never changes.
	// +optional
	AdmittedAt *metav1.Time `json:"admittedAt,omitempty"`
	// Closed is written atomically with the first terminal outcome.
	// +optional
	Closed *SummonClosedStatus `json:"closed,omitempty"`
	// Escalation is the pending filing for a remediation ending escalated.
	// OP768 adds filing, blocked and abandoned transitions; this slice files nothing.
	// +optional
	Escalation *SummonEscalationStatus `json:"escalation,omitempty"`
	// Pages are the session's lifecycle page events, each recorded once by
	// the operator leader (DESIGN-064 6.8, ruling D9 on item 741, OP779). The
	// keeper sends them (story 742); nothing sends them yet. No terminal event
	// is stored here or anywhere: the keeper derives a session's terminal page
	// from its close record (ruling Q1 on item 742, built in PR 1102). An
	// entry is never removed or changed.
	// +kubebuilder:validation:MaxItems=8
	// +listType=atomic
	// +optional
	Pages []SummonPage `json:"pages,omitempty"`
	// Executor is a summoned remote session's executor latch (DESIGN-064 5.9,
	// F3, OP780): the operator records its one pod attempt before the Pod
	// create and the pod's UID once it appears, so no path builds a second
	// pod. A summoned task uses status.privateHistory instead. It is never
	// removed.
	// +optional
	Executor *SummonExecutorStatus `json:"executor,omitempty"`
	// Registration is a remote escalation's settled Remote Control
	// registration (DESIGN-064 6.10; story 794, coordinator rulings O1, O2,
	// O3 and O13 on item 745 comments 7680 and 7685, not rulings by Tom).
	// The operator leader writes it, and appends escalation-started to
	// pages in the same write as its result. Each field is set once; the
	// record is never removed.
	// +optional
	Registration *SummonRegistrationStatus `json:"registration,omitempty"`
}

// SummonRegistrationResult is a settled registration: Remote Control
// registered under the session's name with a verified link, or it did not
// within its bound.
// +kubebuilder:validation:Enum=registered;unavailable
// +kubebuilder:validation:MaxLength=16
type SummonRegistrationResult string

const (
	RegistrationRegistered  SummonRegistrationResult = "registered"
	RegistrationUnavailable SummonRegistrationResult = "unavailable"
)

// SummonRegistrationReason is why a registration settled unavailable, read
// at the pass that settled it, from a closed set. No other text from the
// status is ever copied into it.
// +kubebuilder:validation:Enum=starting;offline;login-required;link-unverified;name-mismatch;not-reported
// +kubebuilder:validation:MaxLength=32
type SummonRegistrationReason string

const (
	// The Remote Control state word, as reported.
	RegistrationReasonStarting      SummonRegistrationReason = "starting"
	RegistrationReasonOffline       SummonRegistrationReason = "offline"
	RegistrationReasonLoginRequired SummonRegistrationReason = "login-required"
	// State registered and the name matches, but the URL does not match the
	// verified link pattern.
	RegistrationReasonLinkUnverified SummonRegistrationReason = "link-unverified"
	// State registered under a name that is not the session's.
	RegistrationReasonNameMismatch SummonRegistrationReason = "name-mismatch"
	// No Remote Control record, or a state word outside the four known ones.
	RegistrationReasonNotReported SummonRegistrationReason = "not-reported"
)

// SummonRegistrationStatus is the operator leader's one settlement of a
// remote escalation's registration (DESIGN-064 6.10, story 794). AgentSeenAt
// is the leader's clock at the first pass that read a fresh heartbeat with
// the agent busy, idle or waiting; it is never invented when the session
// settles first. Result and At are written together, with the reason for
// unavailable and the escalation-started page event, in one status write.
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.agentSeenAt) || (has(self.agentSeenAt) && self.agentSeenAt == oldSelf.agentSeenAt)",message="agentSeenAt is set once and never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.result) || (has(self.result) && self.result == oldSelf.result)",message="the registration result is set once and never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.reason) || (has(self.reason) && self.reason == oldSelf.reason)",message="the registration reason is set once and never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.at) || (has(self.at) && self.at == oldSelf.at)",message="the registration time is set once and never changes"
// +kubebuilder:validation:XValidation:rule="has(self.result) == has(self.at)",message="the registration result and its time are set together"
// +kubebuilder:validation:XValidation:rule="has(self.reason) == (has(self.result) && self.result == 'unavailable')",message="a registration reason is set exactly when the result is unavailable"
type SummonRegistrationStatus struct {
	// AgentSeenAt is when the leader first saw the agent started.
	// +optional
	AgentSeenAt *metav1.Time `json:"agentSeenAt,omitempty"`
	// Result is the settlement.
	// +optional
	Result SummonRegistrationResult `json:"result,omitempty"`
	// Reason is set exactly when Result is unavailable.
	// +optional
	Reason SummonRegistrationReason `json:"reason,omitempty"`
	// At is when the leader settled the registration.
	// +optional
	At *metav1.Time `json:"at,omitempty"`
}

// SummonExecutorStatus is the remote executor latch, in the shape of the
// private task latch (status.privateHistory's executorAttempted,
// executorIntent, executorNotCreated and lastExecutor.podUID): an attempt is
// never reversed and a recorded pod UID never changes.
//
// +kubebuilder:validation:XValidation:rule="!oldSelf.attempted || self.attempted",message="an executor attempt is never reversed"
// +kubebuilder:validation:XValidation:rule="size(oldSelf.intent) == 0 || self.intent == oldSelf.intent",message="the executor intent never changes"
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.podUID) || (has(self.podUID) && self.podUID == oldSelf.podUID)",message="the recorded executor pod UID never changes or goes"
// +kubebuilder:validation:XValidation:rule="!self.attempted || size(self.intent) > 0",message="an executor attempt has one intent"
// +kubebuilder:validation:XValidation:rule="!has(self.podUID) || self.attempted",message="an executor pod is recorded only for an attempt"
// +kubebuilder:validation:XValidation:rule="!has(self.notCreated) || !self.notCreated || (self.attempted && !has(self.podUID))",message="only an attempt with no recorded pod can be marked not created"
type SummonExecutorStatus struct {
	// Attempted is written, and confirmed, before the one Pod create. It is
	// never reversed: a create whose result is unknown stays an attempt.
	Attempted bool `json:"attempted"`
	// Intent names that one create; the pod carries it in an annotation.
	// +kubebuilder:validation:MaxLength=128
	Intent string `json:"intent"`
	// PodUID is the pod that create made, recorded once an uncached read
	// finds it with the intent. It never changes.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +optional
	PodUID string `json:"podUID,omitempty"`
	// NotCreated is DESIGN-019's known non-attempt: the operator returned
	// before calling Create for Intent because an observation failed. Only
	// then may the same intent re-arm, once per mark.
	// +optional
	NotCreated bool `json:"notCreated,omitempty"`
}

// SummonPageEvent is a lifecycle page event of DESIGN-064 6.8 recorded on a
// session. Story 779 writes escalation-held and escalation-reminder; story
// 794 writes escalation-started, only with status.summon.registration's
// result (6.10); story 742 adds the rest of 6.8's lifecycle events. A
// terminal event is never a page
// event here: the keeper derives it from the close record (ruling Q1 on item
// 742, built in PR 1102).
// +kubebuilder:validation:Enum=escalation-held;escalation-reminder;escalation-started
// +kubebuilder:validation:MaxLength=32
type SummonPageEvent string

const (
	// PageEscalationHeld: an escalation Queued over 15 minutes (6.7).
	PageEscalationHeld SummonPageEvent = "escalation-held"
	// PageEscalationReminder: a remote escalation idle 24 hours (6.7).
	PageEscalationReminder SummonPageEvent = "escalation-reminder"
	// PageEscalationStarted: a remote escalation's registration settled
	// (6.10, story 794), written with status.summon.registration's result.
	PageEscalationStarted SummonPageEvent = "escalation-started"
)

// SummonPage is one page event: what happened and when the operator
// recorded it (DESIGN-064 6.8's {event, at}).
type SummonPage struct {
	// +required
	Event SummonPageEvent `json:"event"`
	// +required
	At metav1.Time `json:"at"`
}

// SummonClosedStatus identifies the one terminal transition (DESIGN-064 5.9).
//
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.released) || !oldSelf.released || (has(self.released) && self.released)",message="released is never reversed"
type SummonClosedStatus struct {
	At metav1.Time `json:"at"`
	// +kubebuilder:validation:MaxLength=10
	// +kubebuilder:validation:Enum=report;unreported
	Source string `json:"source"`
	// Reason is the report verb or the guaranteed-close reason from section 6.6.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	Reason string `json:"reason"`
	// Released is true once the session's reservation, its lane and its pool
	// slot, is free: at once for a report, and for an operator close once no
	// further launch can follow (DESIGN-064 5.9). True is never reversed.
	// +optional
	Released bool `json:"released,omitempty"`
}

// SummonEscalationStatus reserves a filing name in the terminal write.
// OP768 resolves name collisions and records the child; spec.summon.filedFor
// and the filing's charge, retry and abandonment are deliberately its seam.
type SummonEscalationStatus struct {
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Enum=Pending
	State string `json:"state"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}
