package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Reminder is one durable reminder (DESIGN-032, OpenProject #206): a title and
// body, a due time in an IANA time zone, an optional narrow recurrence, and
// targets named by reference. The operator's /v1 API creates, edits and
// cancels it. The keeper's leader-only scheduler delivers it and keeps every
// delivery decision in status, so a restart repeats nothing.
//
// The spec holds what the owner asked for. The status holds what the
// scheduler did: the next occurrence, each target's delivery for the current
// occurrence and a bounded attempt log. Both are written with optimistic
// concurrency (resourceVersion), which is how an edit, a cancel and a fire
// race safely.

// Reminder target references. A target is a reference, never a destination:
// "pushover:<ref>" names a Pushover destination, "calendar:<ref>" a calendar,
// and the keeper resolves both from its own configuration. A free-form address
// or key is not a reference and the API refuses it.
const (
	ReminderTargetPushover = "pushover"
	ReminderTargetCalendar = "calendar"
	// ReminderDefaultPushover is the one Pushover reference the MVP knows.
	ReminderDefaultPushover = "pushover:default"
)

// Reminder recurrence frequencies.
const (
	ReminderDaily   = "daily"
	ReminderWeekly  = "weekly"
	ReminderMonthly = "monthly"
)

// Reminder states.
const (
	ReminderPending = "pending"
	// ReminderSent means at least one delivery was actually sent.
	ReminderSent   = "sent"
	ReminderFailed = "failed"
	// ReminderSkipped means the reminder ended with nothing delivered and
	// nothing failed: no deliverable or configured target (a calendar-only
	// reminder, an unconfigured Pushover, a target an edit removed).
	ReminderSkipped   = "skipped"
	ReminderCancelled = "cancelled"
)

// Delivery states of one target for one occurrence.
const (
	// DeliveryClaimed is written (compare-and-set) before a request is made. A
	// claim found on a later pass was never resolved: the process died or the
	// result write failed, so the request may or may not have been sent.
	DeliveryClaimed = "claimed"
	// DeliveryRetry is a failure before any request reached the provider.
	DeliveryRetry = "retry"
	DeliverySent  = "sent"
	// DeliveryFailed is a final failure: the provider refused, or the retries
	// ran out, with no request accepted.
	DeliveryFailed = "failed"
	// DeliveryAmbiguous means a request may have reached the provider. It is
	// never sent again automatically.
	DeliveryAmbiguous = "ambiguous"
	// DeliverySkipped is an honest skip, such as an unconfigured calendar.
	DeliverySkipped = "skipped"
)

// Calendar sync states.
const (
	CalendarSynced  = "synced"
	CalendarSkipped = "skipped"
	CalendarFailed  = "failed"
	CalendarDeleted = "deleted"
)

// ReminderRecurrence is the narrow RRULE subset: every day, every week on some
// weekdays, or every month on one day, ending by UNTIL or COUNT or never.
// +kubebuilder:validation:XValidation:rule="self.freq == 'weekly' || !has(self.byDay)",message="byDay is for a weekly recurrence"
// +kubebuilder:validation:XValidation:rule="self.freq == 'monthly' || !has(self.byMonthDay)",message="byMonthDay is for a monthly recurrence"
// +kubebuilder:validation:XValidation:rule="!(has(self.until) && has(self.count))",message="until and count are exclusive"
type ReminderRecurrence struct {
	// Freq is daily, weekly or monthly.
	// +kubebuilder:validation:Enum=daily;weekly;monthly
	Freq string `json:"freq"`

	// ByDay lists the weekdays of a weekly recurrence (MO..SU). Empty means
	// the weekday of the due time.
	// +kubebuilder:validation:MaxItems=7
	// +kubebuilder:validation:items:Enum=MO;TU;WE;TH;FR;SA;SU
	// +listType=set
	// +optional
	ByDay []string `json:"byDay,omitempty"`

	// ByMonthDay is the day of the month of a monthly recurrence (1..31). A
	// day a month lacks is clamped to that month's last day. Zero means the
	// day of the due time.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=31
	// +optional
	ByMonthDay int32 `json:"byMonthDay,omitempty"`

	// Until ends the recurrence at this instant, inclusive.
	// +optional
	Until *metav1.Time `json:"until,omitempty"`

	// Count ends the recurrence after this many occurrences, the first
	// included.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +optional
	Count int32 `json:"count,omitempty"`
}

// ReminderSpec is what the owner asked for.
type ReminderSpec struct {
	// Title is the notification and calendar title.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=120
	Title string `json:"title"`

	// Body is the notification and calendar description. It is the Pushover
	// message, so it is held to Pushover's 1024 limit (the API counts bytes).
	// +kubebuilder:validation:MaxLength=1024
	// +optional
	Body string `json:"body,omitempty"`

	// Owner is the caller that created it, from its token:
	// session/<name>/<uid> (the session's UID, so a later session that reuses
	// the name inherits nothing), client/<namespace>/<name> or
	// human/<namespace>/<name>. It never changes.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the owner of a reminder never changes"
	Owner string `json:"owner"`

	// Due is the local wall time of the first occurrence, YYYY-MM-DDTHH:MM,
	// read in TimeZone. A time the zone skips (a DST gap) or repeats (a fold)
	// is resolved by DESIGN-032: the gap shifts forward by its length, the fold
	// takes the first of the two.
	// +kubebuilder:validation:MaxLength=16
	// +kubebuilder:validation:Pattern=`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}$`
	Due string `json:"due"`

	// TimeZone is an IANA zone name, such as America/New_York.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[A-Za-z][A-Za-z0-9_+/-]*$`
	TimeZone string `json:"timeZone"`

	// Recurrence repeats the reminder; absent is one-shot.
	// +optional
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`

	// Targets are references: pushover:<ref> and calendar:<ref>.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=128
	// +kubebuilder:validation:items:Pattern=`^(pushover|calendar):[a-z0-9][a-z0-9-]{0,62}$`
	// +listType=set
	Targets []string `json:"targets"`

	// Cancelled is the owner's cancel. The scheduler turns it into the
	// cancelled state and removes the calendar entry.
	// +optional
	Cancelled bool `json:"cancelled,omitempty"`
}

// ReminderDelivery is one target's delivery of the current occurrence.
type ReminderDelivery struct {
	// +kubebuilder:validation:MaxLength=128
	Target string `json:"target"`
	// Occurrence is the 1-based index of the occurrence in its series.
	Occurrence int32 `json:"occurrence"`
	// +kubebuilder:validation:Enum=claimed;retry;sent;failed;ambiguous;skipped
	State string `json:"state"`
	// Attempts counts the claims made for this occurrence.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Attempts int32 `json:"attempts"`
	// NotBefore is the earliest a retry may be claimed.
	// +optional
	NotBefore *metav1.Time `json:"notBefore,omitempty"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason string `json:"reason,omitempty"`
	// Receipt is the provider's receipt when it gives one. Pushover's standard
	// priorities give none.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Receipt   string      `json:"receipt,omitempty"`
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// ReminderCalendarSync is one calendar target's entry for the whole series.
type ReminderCalendarSync struct {
	// +kubebuilder:validation:MaxLength=128
	Target string `json:"target"`
	// EventID is derived from the reminder's name, so a replayed create finds
	// the entry it already made.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	EventID string `json:"eventID,omitempty"`
	// SpecHash is the hash of the fields the entry was last written from.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	SpecHash string `json:"specHash,omitempty"`
	// +kubebuilder:validation:Enum=synced;skipped;failed;deleted
	State string `json:"state"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Reason    string      `json:"reason,omitempty"`
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// ReminderAttempt is one entry of the bounded attempt log.
type ReminderAttempt struct {
	At         metav1.Time `json:"at"`
	Occurrence int32       `json:"occurrence"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Target string `json:"target,omitempty"`
	// +kubebuilder:validation:Enum=sent;failed;ambiguous;skipped;missed;retry
	Outcome string `json:"outcome"`
	// +kubebuilder:validation:MaxLength=128
	// +optional
	Detail string `json:"detail,omitempty"`
}

// ReminderStatus is what the scheduler did.
type ReminderStatus struct {
	// State is pending, sent, failed, skipped or cancelled. Absent means pending.
	// +kubebuilder:validation:Enum=pending;sent;failed;skipped;cancelled
	// +optional
	State string `json:"state,omitempty"`

	// NextDue is the instant of the next occurrence; absent when none is left.
	// +optional
	NextDue *metav1.Time `json:"nextDue,omitempty"`

	// Occurrence is the 1-based index of NextDue in its series.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Occurrence int32 `json:"occurrence,omitempty"`

	// PlanHash is the hash of the schedule fields NextDue was computed from.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	PlanHash string `json:"planHash,omitempty"`

	// Deliveries are the current occurrence's, one per delivered target.
	// +kubebuilder:validation:MaxItems=8
	// +listType=atomic
	// +optional
	Deliveries []ReminderDelivery `json:"deliveries,omitempty"`

	// Calendar is the sync state of each calendar target.
	// +kubebuilder:validation:MaxItems=8
	// +listType=atomic
	// +optional
	Calendar []ReminderCalendarSync `json:"calendar,omitempty"`

	// Attempts is the last attempts, oldest first, at most 20.
	// +kubebuilder:validation:MaxItems=20
	// +listType=atomic
	// +optional
	Attempts []ReminderAttempt `json:"attempts,omitempty"`

	// Message says why the state is what it is.
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Message string `json:"message,omitempty"`
}

// Reminder is one durable reminder.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rem
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Next",type=date,JSONPath=`.status.nextDue`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.owner`
// +kubebuilder:printcolumn:name="Title",type=string,JSONPath=`.spec.title`
type Reminder struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ReminderSpec   `json:"spec"`
	Status ReminderStatus `json:"status,omitempty"`
}

// ReminderList is a list of reminders.
// +kubebuilder:object:root=true
type ReminderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Reminder `json:"items"`
}
