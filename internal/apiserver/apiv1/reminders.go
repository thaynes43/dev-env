package apiv1

import "time"

// The reminders routes (DESIGN-032, OpenProject #206). They exist only when the
// operator runs with --enable-reminders-api; otherwise every one answers 404.
//
//	POST   /v1/reminders              create one; the owner is the caller's token
//	GET    /v1/reminders              the caller's own (all=true: a human's or coordinator's, everyone's)
//	GET    /v1/reminders/{name}       one
//	PATCH  /v1/reminders/{name}       edit a pending one
//	POST   /v1/reminders/{name}/cancel cancel one
const RemindersPath = "/v1/reminders"

// ReminderPath is one reminder's route.
func ReminderPath(name string) string { return RemindersPath + "/" + name }

// ReminderCancelPath is a reminder's cancel route.
func ReminderCancelPath(name string) string { return ReminderPath(name) + "/cancel" }

// ReminderRecurrence is the narrow RRULE subset: daily, weekly on some
// weekdays, or monthly on one day.
type ReminderRecurrence struct {
	Freq       string     `json:"freq"`
	ByDay      []string   `json:"byDay,omitempty"`
	ByMonthDay int        `json:"byMonthDay,omitempty"`
	Until      *time.Time `json:"until,omitempty"`
	Count      int        `json:"count,omitempty"`
}

// CreateReminderRequest is the body of POST /v1/reminders.
type CreateReminderRequest struct {
	// Key makes a retried create idempotent: the same caller and key name the
	// same reminder. Optional.
	Key   string `json:"key,omitempty"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	// Due is the local wall time of the first occurrence, YYYY-MM-DDTHH:MM,
	// read in TimeZone.
	Due        string              `json:"due"`
	TimeZone   string              `json:"timeZone"`
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`
	// Targets are references from the fixed allowlist; pushover:default when
	// empty.
	Targets []string `json:"targets,omitempty"`
}

// EditReminderRequest is the body of PATCH /v1/reminders/{name}. An absent
// field is left as it is.
type EditReminderRequest struct {
	Title    *string `json:"title,omitempty"`
	Body     *string `json:"body,omitempty"`
	Due      *string `json:"due,omitempty"`
	TimeZone *string `json:"timeZone,omitempty"`
	// Recurrence replaces the recurrence; ClearRecurrence makes it one-shot.
	Recurrence      *ReminderRecurrence `json:"recurrence,omitempty"`
	ClearRecurrence bool                `json:"clearRecurrence,omitempty"`
	Targets         []string            `json:"targets,omitempty"`
}

// ReminderDelivery is one target's delivery of an occurrence.
type ReminderDelivery struct {
	Target     string     `json:"target"`
	Occurrence int        `json:"occurrence"`
	State      string     `json:"state"`
	Attempts   int        `json:"attempts"`
	NotBefore  *time.Time `json:"notBefore,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Receipt    string     `json:"receipt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

// ReminderCalendar is one calendar target's sync state.
type ReminderCalendar struct {
	Target    string    `json:"target"`
	EventID   string    `json:"eventID,omitempty"`
	State     string    `json:"state"`
	Reason    string    `json:"reason,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ReminderAttempt is one entry of the attempt log.
type ReminderAttempt struct {
	At         time.Time `json:"at"`
	Occurrence int       `json:"occurrence"`
	Target     string    `json:"target,omitempty"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}

// Reminder is one reminder as the API shows it.
type Reminder struct {
	Name       string              `json:"name"`
	Owner      string              `json:"owner"`
	Title      string              `json:"title"`
	Body       string              `json:"body,omitempty"`
	Due        string              `json:"due"`
	TimeZone   string              `json:"timeZone"`
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`
	Targets    []string            `json:"targets"`
	// State is pending, sent, failed or cancelled.
	State      string             `json:"state"`
	NextDue    *time.Time         `json:"nextDue,omitempty"`
	Occurrence int                `json:"occurrence,omitempty"`
	Deliveries []ReminderDelivery `json:"deliveries,omitempty"`
	Calendar   []ReminderCalendar `json:"calendar,omitempty"`
	Attempts   []ReminderAttempt  `json:"attempts,omitempty"`
	Message    string             `json:"message,omitempty"`
	// Warning is set when a cancel or edit raced a delivery that was already
	// in flight, which the cancel cannot take back.
	Warning   string    `json:"warning,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ReminderList is GET /v1/reminders.
type ReminderList struct {
	Reminders []Reminder `json:"reminders"`
}
