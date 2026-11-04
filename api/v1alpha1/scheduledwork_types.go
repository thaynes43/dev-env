package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScheduledWork is agent work that starts at a scheduled time (DESIGN-054,
// OpenProject #397): a due time in an IANA zone with DESIGN-032's recurrence,
// a work order, and the dispatch template that fixes how the session runs. The
// operator's /v1 API creates, edits and cancels it, and is the only writer. A
// leader-only executor in the operator dispatches each occurrence and records
// it in status, so a restart, failover or replay never loses an occurrence and
// never starts its work twice.
//
// The spec holds what the owner asked for. The status holds the human approval
// that may authorise it and what the executor did with each occurrence. Both
// are written with optimistic concurrency (resourceVersion).
//
// Scheduling grants no authority. Every occurrence is revalidated at its due
// time against the owner's standing basis or a human approval bound to the
// exact spec (DESIGN-054 "Authority").

// ScheduledWork modes.
const (
	// ScheduledWorkHeadless runs a task session that reports a result.
	ScheduledWorkHeadless = "headless"
	// ScheduledWorkInteractive opens a remote session Tom joins from his phone.
	ScheduledWorkInteractive = "interactive"
)

// Occurrence states (DESIGN-054 "Occurrence lifecycle").
const (
	OccurrencePending    = "Pending"
	OccurrenceClaimed    = "Claimed"
	OccurrenceDispatched = "Dispatched"
	OccurrenceDelivered  = "Delivered"
	OccurrenceOpened     = "Opened"
	OccurrenceRunning    = "Running"
	OccurrenceSucceeded  = "Succeeded"
	OccurrenceFailed     = "Failed"
	OccurrenceEnded      = "Ended"
	// OccurrenceParked is a failed due-time check. It waits for a human approval,
	// retry or cancel, so it is not terminal.
	OccurrenceParked  = "Parked"
	OccurrenceSkipped = "Skipped"
	// OccurrenceAmbiguous is a derived-name session the executor did not create
	// (its label or annotation does not match). It is never adopted.
	OccurrenceAmbiguous = "Ambiguous"
	OccurrenceCancelled = "Cancelled"
)

// Interactive work-order delivery: a note on the occurrence, not a state.
const (
	WorkOrderAccepted = "accepted"
	WorkOrderRefused  = "refused"
	// WorkOrderAmbiguous is an exec error: the message may have arrived. It is
	// never sent again.
	WorkOrderAmbiguous = "ambiguous"
)

// Bounds of the occurrence record.
const (
	// ScheduledWorkTerminalKept is how many terminal occurrences the executor
	// keeps, newest first. It prunes the oldest terminal ones beyond this before
	// it appends; it never prunes a non-terminal one.
	ScheduledWorkTerminalKept = 50
	// ScheduledWorkMaxOccurrences is the schema's limit on status.occurrences:
	// ScheduledWorkTerminalKept terminal ones plus room for the non-terminal
	// ones. DESIGN-054 allows one unfinished occurrence per ScheduledWork, and a
	// Parked one, which does not count as unfinished, expires to Skipped by the
	// next due time at the latest. So at most three records are non-terminal
	// at once: the unfinished one, one Parked and one Pending. The other eleven
	// slots are margin. A write past the limit is refused, so a bug that stops
	// pruning or expiring fails loudly instead of growing the object.
	ScheduledWorkMaxOccurrences = 64
)

// OccurrenceTerminal reports whether an occurrence state is final: no executor
// path leaves it, and only a terminal occurrence may be pruned. Parked is not
// terminal, because a human approval or retry claims it again (or it expires
// to Skipped).
func OccurrenceTerminal(state string) bool {
	switch state {
	case OccurrenceSucceeded, OccurrenceFailed, OccurrenceEnded, OccurrenceSkipped,
		OccurrenceAmbiguous, OccurrenceCancelled:
		return true
	}
	return false
}

// ScheduledWorkOrder is what the dispatched session works on.
type ScheduledWorkOrder struct {
	// Template names an entry in the scheduled-work ConfigMap
	// (dev-env-system/dev-env-scheduled-work). The template fixes the agent,
	// model id, effort, size, profile, allowed repos, maximum duration and the
	// owner classes it admits; the request cannot raise any of them.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Template string `json:"template"`

	// Repo is the repository the session works in, as AgentSession's repo. The
	// template must allow it, at the due time.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9_.-]+$`
	// +kubebuilder:validation:XValidation:rule="self != '.' && self != '..'",message="repo is a repository name, not a path"
	Repo string `json:"repo"`

	// Prompt is the work order: a headless session's prompt, or the message an
	// interactive session receives once it is ready. At most 8 KiB, counted in
	// bytes (MaxLength counts characters; the rule counts bytes).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=8192
	// +kubebuilder:validation:XValidation:rule="size(bytes(self)) <= 8192",message="prompt is at most 8192 bytes"
	Prompt string `json:"prompt"`
}

// ScheduledWorkSpec is what the owner asked for.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.cancelled) || !oldSelf.cancelled || (has(self.cancelled) && self.cancelled)",message="cancelled scheduled work stays cancelled"
type ScheduledWorkSpec struct {
	// Title names the work in pages, the calendar projection and listings.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=120
	Title string `json:"title"`

	// Owner is the caller that created it, from its token, in the form a
	// Reminder records: session/<name>/<uid>, client/<namespace>/<name> or
	// human/<namespace>/<name>. It never changes.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the owner of scheduled work never changes"
	Owner string `json:"owner"`

	// Editor is the caller of the latest spec write, create included, in the
	// owner's form. The API writes it in the same write as every spec change,
	// so the two cannot diverge. A standing authority basis holds only while
	// the editor is the owner; any other editor leaves the item needing a human
	// approval of that exact spec.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Editor string `json:"editor"`

	// Due is the local wall time of the first occurrence, YYYY-MM-DDTHH:MM,
	// read in TimeZone, with DESIGN-032's gap and fold rules.
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}$`
	Due string `json:"due"`

	// TimeZone is an IANA zone name, such as America/New_York.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9_+/-]*$`
	TimeZone string `json:"timeZone"`

	// Recurrence repeats the work, in DESIGN-032's shape; absent is one-shot.
	// +optional
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`

	// NotAfter ends the item: no occurrence is dispatched after it. It is
	// required, so no scheduled work runs forever. Its upper bound, 180 days
	// after creation, needs the creation time, which this schema cannot read
	// on create; the API enforces it (DESIGN-054 S5).
	NotAfter metav1.Time `json:"notAfter"`

	// Mode is headless (a task session) or interactive (a remote session).
	// +kubebuilder:validation:Enum=headless;interactive
	Mode string `json:"mode"`

	// Work is the work order and its dispatch template.
	Work ScheduledWorkOrder `json:"work"`

	// Notify are the page references, pushover:<ref> in Reminder target form.
	// Pages go through keyed one-shot Reminders, which the keeper sends. A
	// calendar is not a page: it is the separate Calendar projection.
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=128
	// +kubebuilder:validation:items:Pattern=`^pushover:[a-z0-9][a-z0-9-]{0,62}$`
	// +listType=set
	// +optional
	Notify []string `json:"notify,omitempty"`

	// Calendar is an optional calendar projection, calendar:<ref>, kept
	// through a companion Reminder. The CRD stays the state of record; the
	// calendar never changes it.
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:Pattern=`^calendar:[a-z0-9][a-z0-9-]{0,62}$`
	// +optional
	Calendar string `json:"calendar,omitempty"`

	// Cancelled is the cancel: no later occurrence is dispatched. It does not
	// stop a running session, and it cannot be undone: a cancelled item is
	// never re-armed by an edit.
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`
}

// ScheduledWorkApproval is a human's approval of one exact spec. Only the
// API's human approve route writes it. A spec edit changes the spec hash, so
// the approval no longer matches and is void.
type ScheduledWorkApproval struct {
	// By is the approving human, human/<namespace>/<name>.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:Pattern=`^human/.+$`
	By string `json:"by"`

	At metav1.Time `json:"at"`

	// SpecHash is the SHA-256, in hex, of the owner, mode, work, schedule and
	// notAfter the human approved.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	SpecHash string `json:"specHash"`
}

// ScheduledWorkCounts counts occurrences by outcome, over the item's life.
type ScheduledWorkCounts struct {
	// +kubebuilder:validation:Minimum=0
	// +optional
	Succeeded int32 `json:"succeeded,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Failed int32 `json:"failed,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Parked int32 `json:"parked,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Skipped int32 `json:"skipped,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Ended int32 `json:"ended,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Ambiguous int32 `json:"ambiguous,omitempty"`
	// +kubebuilder:validation:Minimum=0
	// +optional
	Cancelled int32 `json:"cancelled,omitempty"`
}

// Page events: the moment of an occurrence a page reports.
const (
	PageEventDue    = "due"
	PageEventLink   = "link"
	PageEventParked = "parked"
	PageEventFailed = "failed"
	PageEventResult = "result"
)

// ScheduledWorkMaxPages bounds the pages of one occurrence.
const ScheduledWorkMaxPages = 4

// ScheduledWorkPage is one page an occurrence sent. Each page is its own
// one-shot Reminder, keyed by the occurrence and the event, and a sent Reminder
// is never reused. Pages are best effort: a page Reminder that cannot be
// created, or a Reminder kind that is not served, is recorded as skipped and
// never holds a finalizer, a claim or a state change.
type ScheduledWorkPage struct {
	// Event is what the page reports: due, link, parked, failed or result.
	// +kubebuilder:validation:Enum=due;link;parked;failed;result
	Event string `json:"event"`

	// Reminder is the name of the Reminder that carries the page; absent when
	// none was created.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +optional
	Reminder string `json:"reminder,omitempty"`

	// State is the Reminder's final delivery state, copied, or skipped when no
	// Reminder could be created; absent until there is one.
	// +kubebuilder:validation:Enum=sent;failed;ambiguous;skipped
	// +optional
	State string `json:"state,omitempty"`
}

// ScheduledWorkExecution is a headless session's task result, as the operator
// read it into the session's status.taskResult.
type ScheduledWorkExecution struct {
	// State is Succeeded, Failed, TimedOut or Unknown.
	// +required
	State TaskResultState `json:"state"`

	// Reason is the task result's CamelCase reason, such as TaskExitedNonZero.
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason string `json:"reason,omitempty"`
}

// ScheduledWorkOccurrence is one due time and what the executor did with it.
type ScheduledWorkOccurrence struct {
	// At is the occurrence's instant, its due time resolved in the zone.
	At metav1.Time `json:"at"`

	// +kubebuilder:validation:Enum=Pending;Claimed;Dispatched;Delivered;Opened;Running;Succeeded;Failed;Ended;Parked;Skipped;Ambiguous;Cancelled
	State string `json:"state"`

	// Reason says why the state is what it is, such as "no standing
	// authority", "previous still running" or, for interactive work that
	// ended, "participation not observed".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Reason string `json:"reason,omitempty"`

	// Missed is how many due times this one Skipped record stands for, when
	// the executor found several missed at once after an outage. Absent means
	// one.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100000
	// +optional
	Missed int32 `json:"missed,omitempty"`

	// ClaimedAt is when the executor claimed the occurrence.
	// +optional
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`

	// RetriedAt is when a human approved or retried this occurrence while it
	// was Parked. It opens a fresh window for this occurrence only: its grace,
	// the claim-without-session rule and the start deadline count from here,
	// not from At.
	// +optional
	RetriedAt *metav1.Time `json:"retriedAt,omitempty"`

	// Session is the derived session name:
	// sw-<first 12 hex of the ScheduledWork UID>-<occurrence UTC YYYYMMDDHHMM>.
	// +kubebuilder:validation:MaxLength=28
	// +kubebuilder:validation:Pattern=`^sw-[0-9a-f]{12}-[0-9]{12}$`
	// +optional
	Session string `json:"session,omitempty"`

	// SpecHash is the hash of the spec the occurrence was claimed with, in the
	// form of status.approval.specHash. An edit applies from the next
	// occurrence; this one runs on the spec it was claimed with.
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{64}$`
	// +optional
	SpecHash string `json:"specHash,omitempty"`

	// Delivery is the interactive work order's delivery: accepted, refused or
	// ambiguous.
	// +kubebuilder:validation:Enum=accepted;refused;ambiguous
	// +optional
	Delivery string `json:"delivery,omitempty"`

	// Execution is how a headless task ended, copied from the session's
	// status.taskResult. It decides the terminal state: Succeeded is Succeeded,
	// Failed and TimedOut are Failed, Unknown is Ended. It says only what the
	// task process reported, not that the work is right.
	// +optional
	Execution *ScheduledWorkExecution `json:"execution,omitempty"`

	// Reported is a headless agent's own retained outcome, copied with the
	// Session outcome's limits. It is the agent's claim about the work and is
	// not validated; the receiver judges it. Interactive work has none.
	// +optional
	Reported *TaskOutcomeRecordStatus `json:"reported,omitempty"`

	// Pages are the pages this occurrence sent, at most one per event.
	// +kubebuilder:validation:MaxItems=4
	// +listType=map
	// +listMapKey=event
	// +optional
	Pages []ScheduledWorkPage `json:"pages,omitempty"`
}

// ScheduledWorkStatus is what the executor did, and the approval that may
// authorise it.
type ScheduledWorkStatus struct {
	// Approval is a human's approval of the current spec, if any.
	// +optional
	Approval *ScheduledWorkApproval `json:"approval,omitempty"`

	// NextDue is the instant of the next occurrence; absent when none is left.
	// +optional
	NextDue *metav1.Time `json:"nextDue,omitempty"`

	// PlanHash is the hash of the schedule fields NextDue was computed from.
	// An edit re-plans from now.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	PlanHash string `json:"planHash,omitempty"`

	// +optional
	Counts ScheduledWorkCounts `json:"counts,omitempty"`

	// Occurrences are the recorded occurrences, oldest first: the newest
	// ScheduledWorkTerminalKept terminal ones and every non-terminal one.
	// +kubebuilder:validation:MaxItems=64
	// +listType=atomic
	// +optional
	Occurrences []ScheduledWorkOccurrence `json:"occurrences,omitempty"`
}

// ScheduledWork is agent work that starts at a scheduled time.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sw
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="Next",type=date,JSONPath=`.status.nextDue`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
type ScheduledWork struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScheduledWorkSpec   `json:"spec"`
	Status ScheduledWorkStatus `json:"status,omitempty"`
}

// ScheduledWorkList is a list of scheduled work.
// +kubebuilder:object:root=true
type ScheduledWorkList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScheduledWork `json:"items"`
}
