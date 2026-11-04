package apiv1

import "time"

// The scheduled-work routes (DESIGN-054 S5, OpenProject #397). A scheduled
// item is agent work that starts at a due time: a headless task session or an
// interactive remote session. These are the wire types only; the operator
// serves the routes (S5b, internal/apiserver/scheduled_work.go) only when its
// Server is given a scheduled-work config, which a proposed, default-off
// --enable-scheduled-work-api flag sets; without it every route answers 404.
//
//	POST   /v1/scheduled-work                  create one; the owner and editor are the caller's token
//	GET    /v1/scheduled-work                  the caller's own (all=true: a human's or coordinator's, everyone's)
//	GET    /v1/scheduled-work/{name}           one
//	PATCH  /v1/scheduled-work/{name}           edit the spec: the owner or a human only
//	POST   /v1/scheduled-work/{name}/cancel    cancel: the owner, a human or a coordinator
//	POST   /v1/scheduled-work/{name}/approve   approve the exact spec: a human only
//	POST   /v1/scheduled-work/{name}/retry     retry a Parked occurrence: a human only
//
// The server sets spec.owner on create and spec.editor on every spec write
// from the caller's token; no request carries either. Field errors name the
// field by its JSON path in the request (title, due, timeZone, recurrence,
// notAfter, mode, work.template, work.repo, work.prompt, notify, calendar,
// key, specHash, at).
//
// Scheduling grants no authority. An occurrence runs only on a basis that
// holds at its due time (DESIGN-054 "Authority"), and nothing runs until the
// operator has a dispatch template the item names.
const ScheduledWorkPath = "/v1/scheduled-work"

// ScheduledWorkItemPath is one item's route.
func ScheduledWorkItemPath(name string) string { return ScheduledWorkPath + "/" + name }

// ScheduledWorkCancelPath is an item's cancel route.
func ScheduledWorkCancelPath(name string) string { return ScheduledWorkItemPath(name) + "/cancel" }

// ScheduledWorkApprovePath is an item's human approve route.
func ScheduledWorkApprovePath(name string) string { return ScheduledWorkItemPath(name) + "/approve" }

// ScheduledWorkRetryPath is an item's human retry route.
func ScheduledWorkRetryPath(name string) string { return ScheduledWorkItemPath(name) + "/retry" }

// Scheduled-work modes.
const (
	// ScheduledWorkHeadless runs a task session that reports a result.
	ScheduledWorkHeadless = "headless"
	// ScheduledWorkInteractive opens a remote session Tom joins from his
	// phone. It needs at least one notify target: the link is paged.
	ScheduledWorkInteractive = "interactive"
)

// Item states, as the view's State reports them.
const (
	// ScheduledWorkActive: not cancelled, and an occurrence is still ahead or
	// not yet finished.
	ScheduledWorkActive = "active"
	// ScheduledWorkCancelled: cancelled. No later occurrence runs; a session
	// already running is not stopped.
	ScheduledWorkCancelled = "cancelled"
	// ScheduledWorkEnded: notAfter has passed or the series has no occurrence
	// left, and every recorded occurrence is terminal.
	ScheduledWorkEnded = "ended"
)

// Occurrence states, the CRD's values verbatim (DESIGN-054 "Occurrence
// lifecycle"). Parked is not terminal: it waits for a human approve, retry or
// cancel, or expires to Skipped.
const (
	ScheduledOccurrencePending    = "Pending"
	ScheduledOccurrenceClaimed    = "Claimed"
	ScheduledOccurrenceDispatched = "Dispatched"
	ScheduledOccurrenceDelivered  = "Delivered"
	ScheduledOccurrenceOpened     = "Opened"
	ScheduledOccurrenceRunning    = "Running"
	ScheduledOccurrenceSucceeded  = "Succeeded"
	ScheduledOccurrenceFailed     = "Failed"
	ScheduledOccurrenceEnded      = "Ended"
	ScheduledOccurrenceParked     = "Parked"
	ScheduledOccurrenceSkipped    = "Skipped"
	ScheduledOccurrenceAmbiguous  = "Ambiguous"
	ScheduledOccurrenceCancelled  = "Cancelled"
)

// Limits of a scheduled item, the CRD's and the API's.
const (
	// MaxScheduledWorkPromptBytes bounds work.prompt, counted in bytes.
	MaxScheduledWorkPromptBytes = 8192
	// MaxScheduledWorkTitle bounds title, counted in characters.
	MaxScheduledWorkTitle = 120
	// MaxScheduledWorkNotify bounds notify.
	MaxScheduledWorkNotify = 8
	// MaxScheduledWorkKeyBytes bounds a create's key.
	MaxScheduledWorkKeyBytes = 128
	// MaxScheduledWorkNotAfter is how far after creation notAfter may be.
	// The API enforces it with its own clock.
	MaxScheduledWorkNotAfter = 180 * 24 * time.Hour
	// MaxScheduledWorkAttempt is an occurrence's highest attempt: 0 first,
	// one more for each human approve or retry, so a fourth retry is refused.
	MaxScheduledWorkAttempt = 3
)

// ScheduledWorkOrder is what the dispatched session works on.
type ScheduledWorkOrder struct {
	// Template names an entry of the operator's dispatch templates. The
	// template fixes the agent, model, effort, size, profile, allowed repos,
	// maximum duration and the authority bases it admits.
	Template string `json:"template"`
	// Repo is the repository; the template must allow it at the due time.
	Repo string `json:"repo"`
	// Prompt is the work order, at most MaxScheduledWorkPromptBytes: a
	// headless session's prompt, or the message an interactive session gets
	// once it is ready.
	Prompt string `json:"prompt"`
}

// CreateScheduledWorkRequest is the body of POST /v1/scheduled-work: the
// spec's fields except owner, editor and cancelled.
type CreateScheduledWorkRequest struct {
	// Key makes a retried create idempotent: the same caller and key name the
	// same item. A key that names an item with another spec is a 409.
	Key   string `json:"key,omitempty"`
	Title string `json:"title"`
	// Due is the local wall time of the first occurrence, YYYY-MM-DDTHH:MM,
	// read in TimeZone with DESIGN-032's gap and fold rules.
	Due        string              `json:"due"`
	TimeZone   string              `json:"timeZone"`
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`
	// NotAfter is required: no occurrence runs after it. At most
	// MaxScheduledWorkNotAfter after creation.
	NotAfter time.Time          `json:"notAfter"`
	Mode     string             `json:"mode"`
	Work     ScheduledWorkOrder `json:"work"`
	// Notify are page targets, pushover:<ref>. Interactive work needs one.
	Notify []string `json:"notify,omitempty"`
	// Calendar is an optional calendar projection, calendar:<ref>.
	Calendar string `json:"calendar,omitempty"`
}

// EditScheduledWorkOrder is the work order part of an edit. An absent field
// is left as it is.
type EditScheduledWorkOrder struct {
	Template *string `json:"template,omitempty"`
	Repo     *string `json:"repo,omitempty"`
	Prompt   *string `json:"prompt,omitempty"`
}

// EditScheduledWorkRequest is the body of PATCH /v1/scheduled-work/{name}. An
// absent field is left as it is. Any edit changes the spec hash, so it voids
// a human approval; an occurrence already claimed runs on the spec it was
// claimed with, and the edit applies from the next one.
type EditScheduledWorkRequest struct {
	Title    *string `json:"title,omitempty"`
	Due      *string `json:"due,omitempty"`
	TimeZone *string `json:"timeZone,omitempty"`
	// Recurrence replaces the recurrence; ClearRecurrence makes it one-shot.
	Recurrence      *ReminderRecurrence     `json:"recurrence,omitempty"`
	ClearRecurrence bool                    `json:"clearRecurrence,omitempty"`
	NotAfter        *time.Time              `json:"notAfter,omitempty"`
	Mode            *string                 `json:"mode,omitempty"`
	Work            *EditScheduledWorkOrder `json:"work,omitempty"`
	// Notify replaces the list; an empty list clears it.
	Notify *[]string `json:"notify,omitempty"`
	// Calendar replaces the projection; an empty string removes it.
	Calendar *string `json:"calendar,omitempty"`
}

// ApproveScheduledWorkRequest is the body of POST .../approve, a human's
// approval of one exact spec. The API identifies the approving human from the
// caller's token, never from this body.
type ApproveScheduledWorkRequest struct {
	// SpecHash is the hash of the spec the client showed, the view's
	// SpecHash. It binds the approval to that spec: the API refuses it with
	// 409 unless it equals the current spec's hash, so an edit made after the
	// client fetched the spec is never approved. It is not evidence of who
	// approved or that anyone read the spec.
	SpecHash string `json:"specHash"`
	// At names a Parked occurrence the client showed. When set, the approval
	// also retries that occurrence, and the API refuses it with 409 unless that
	// occurrence is still Parked and under MaxScheduledWorkAttempt, and with
	// 409 when it was claimed with an earlier spec; an occurrence parked before
	// any claim has no claim hash and may be retried. A refusal records
	// nothing. Absent, the approval applies from the next occurrence and no
	// Parked one is retried.
	At *time.Time `json:"at,omitempty"`
	// TemplateDigest, optional, is the digest of the dispatch template the
	// client showed. When set, the API refuses the approval with 409 unless
	// it equals the digest of the template the spec names as the API reads
	// it now, so a template edit made after the client fetched it is never
	// approved. Absent, the API binds the template as it reads it. Either
	// way the approval records the digest the API read.
	TemplateDigest string `json:"templateDigest,omitempty"`
}

// RetryScheduledWorkRequest is the body of POST .../retry: run one Parked
// occurrence again, late on purpose, in a fresh window from now. It grants no
// authority: the occurrence is checked again as at its due time.
type RetryScheduledWorkRequest struct {
	// At names the Parked occurrence. The API refuses it with 409 unless that
	// occurrence is Parked and under MaxScheduledWorkAttempt, and with 409 when
	// it was claimed with an earlier spec; an occurrence parked before any
	// claim has no claim hash and may be retried.
	At time.Time `json:"at"`
	// SpecHash is the hash of the spec the client showed. It binds the retry
	// to that spec: the API refuses it with 409 unless it equals the current
	// spec's hash. It is not evidence of who retried or that anyone read the
	// spec; the API identifies the human from the caller's token.
	SpecHash string `json:"specHash"`
}

// ScheduledWorkSpec is an item's spec as the API shows it: every stored
// field. SpecHash covers all of them except editor and cancelled.
type ScheduledWorkSpec struct {
	Title string `json:"title"`
	// Owner is who created it, in the form session/<name>/<uid>,
	// client/<namespace>/<name>, coordinator/<namespace>/<name> or
	// human/<namespace>/<name>. It never changes.
	Owner string `json:"owner"`
	// Editor is who made the latest spec write, in the owner's form. A
	// standing authority basis holds only while it is the owner.
	Editor     string              `json:"editor"`
	Due        string              `json:"due"`
	TimeZone   string              `json:"timeZone"`
	Recurrence *ReminderRecurrence `json:"recurrence,omitempty"`
	NotAfter   time.Time           `json:"notAfter"`
	Mode       string              `json:"mode"`
	Work       ScheduledWorkOrder  `json:"work"`
	Notify     []string            `json:"notify,omitempty"`
	Calendar   string              `json:"calendar,omitempty"`
	Cancelled  bool                `json:"cancelled,omitempty"`
}

// ScheduledWorkApproval is a human's approval of one exact spec. It is void
// once the spec's hash differs from SpecHash.
type ScheduledWorkApproval struct {
	// By is the human the API authenticated from the approving request's
	// token, never a value from a request body.
	By       string    `json:"by"`
	At       time.Time `json:"at"`
	SpecHash string    `json:"specHash"`
	// TemplateDigest is the digest of the dispatch template the spec named
	// when the human approved it, for display. The API reads the template and
	// records it; no request carries it. The approval is void once the
	// template's digest differs, as it is once the spec's hash does.
	TemplateDigest string `json:"templateDigest,omitempty"`
}

// ScheduledWorkCounts counts occurrences by outcome over the item's life. A
// Skipped record that stands for several missed due times adds them all.
type ScheduledWorkCounts struct {
	Succeeded int `json:"succeeded,omitempty"`
	Failed    int `json:"failed,omitempty"`
	Parked    int `json:"parked,omitempty"`
	Skipped   int `json:"skipped,omitempty"`
	Ended     int `json:"ended,omitempty"`
	Ambiguous int `json:"ambiguous,omitempty"`
	Cancelled int `json:"cancelled,omitempty"`
}

// ScheduledWorkExecution is how a headless task ended, from the session's
// task result: Succeeded, Failed, TimedOut or Unknown.
type ScheduledWorkExecution struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// ScheduledWorkReported is a headless agent's own retained outcome. It is the
// agent's claim about the work, copied and not validated.
type ScheduledWorkReported struct {
	Summary  string   `json:"summary"`
	PRURLs   []string `json:"prURLs,omitempty"`
	HeadSHAs []string `json:"headSHAs,omitempty"`
}

// ScheduledWorkPage is one page an occurrence sent through a one-shot
// Reminder: event due, link, parked, failed, result or skipped; state sent,
// failed, ambiguous or skipped (absent until there is one).
type ScheduledWorkPage struct {
	Event    string `json:"event"`
	Attempt  int    `json:"attempt"`
	Reminder string `json:"reminder,omitempty"`
	State    string `json:"state,omitempty"`
}

// ScheduledWorkOccurrence is one due time and what the executor did with it.
type ScheduledWorkOccurrence struct {
	// At is the occurrence's instant; it names the occurrence in approve and
	// retry.
	At     time.Time `json:"at"`
	State  string    `json:"state"`
	Reason string    `json:"reason,omitempty"`
	// Attempt is 0, then one more for each human approve or retry.
	Attempt int `json:"attempt,omitempty"`
	// Missed is how many due times one Skipped record stands for.
	Missed    int        `json:"missed,omitempty"`
	ClaimedAt *time.Time `json:"claimedAt,omitempty"`
	RetriedAt *time.Time `json:"retriedAt,omitempty"`
	// Session is the derived session name, once claimed.
	Session string `json:"session,omitempty"`
	// SpecHash is the hash of the spec the occurrence was claimed with.
	SpecHash string `json:"specHash,omitempty"`
	// Delivery is an interactive work order's delivery: accepted, refused or
	// ambiguous.
	Delivery  string                  `json:"delivery,omitempty"`
	Execution *ScheduledWorkExecution `json:"execution,omitempty"`
	Reported  *ScheduledWorkReported  `json:"reported,omitempty"`
	Pages     []ScheduledWorkPage     `json:"pages,omitempty"`
}

// ScheduledWork is one item as the API shows it.
type ScheduledWork struct {
	Name string            `json:"name"`
	Spec ScheduledWorkSpec `json:"spec"`
	// SpecHash is the SHA-256, in lowercase hex, of Spec without editor and
	// cancelled: the hash an approval must name, which binds the approval to
	// the spec the client showed. The API computes it from the same stored
	// spec it shows in Spec. It identifies no one.
	SpecHash string `json:"specHash"`
	// State is active, cancelled or ended.
	State    string                 `json:"state"`
	NextDue  *time.Time             `json:"nextDue,omitempty"`
	Approval *ScheduledWorkApproval `json:"approval,omitempty"`
	Counts   ScheduledWorkCounts    `json:"counts"`
	// Occurrences are the recent occurrences and every non-terminal one, in
	// any order. A list may leave them out.
	Occurrences []ScheduledWorkOccurrence `json:"occurrences,omitempty"`
	// Message is a note from the API, such as that the item will need a
	// human approval.
	Message string `json:"message,omitempty"`
	// Warning is set when a write could not take back something already in
	// flight, such as a cancel while a session runs.
	Warning   string    `json:"warning,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ScheduledWorkList is GET /v1/scheduled-work.
type ScheduledWorkList struct {
	Items []ScheduledWork `json:"items"`
}
