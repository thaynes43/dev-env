package apiv1

// Wire types for agent asks and approvals (Epic #254, lane 0; lead contract
// v1.1 on OpenProject #254). An agent creates an Ask (a question for a human,
// or an approval request for one action on one target); a human answers it
// through the portal. This file holds only the shapes, the route and query
// constants, and the validators. The server derives ids, timestamps, session
// context and state; a request carries intent only. Standard library only,
// like the rest of apiv1.
//
// Text: browsers send textarea newlines as CRLF, and the validators refuse
// CR. Callers (the portal and the MCP tools) pass every multi-line field
// (question, option descriptions, answer text) through NormalizeAskText
// before they validate it.
//
// Time: an ask and an approval scope expire. Answer handlers check an answer
// with AskAnswerRequest.ValidateForAt at their own clock reading, and readers
// use Ask.EffectiveState and Ask.ApprovalValid rather than the stored state
// alone.
//
// Whole seconds: an ask's times are whole seconds. A server truncates its
// clock reading to whole seconds (now.Truncate(time.Second)) before it calls
// ExpiresAtFrom and ValidateAt, and uses the same value as createdAt, so the
// stored expiresAt and the validated scope window agree. TTL refuses an
// expiresIn with a sub-second part for the same reason.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Routes. The agent API creates, lists, reads and withdraws asks; the portal
// API lists, reads and answers them. Only the requester session withdraws;
// the portal has no withdraw route (contract v1.1 A3).
const (
	AsksPath       = "/v1/asks"
	PortalAsksPath = "/v1/portal/asks"

	AskWithdrawSuffix = "/withdraw"
	AskAnswerSuffix   = "/answer"
)

// AskPath is the agent route of one ask: GET reads it. It returns "" when id
// is not a ValidAskID, so an unchecked id never becomes a path; callers check
// ValidAskID first and treat "" as a refusal. The other path helpers below
// follow the same rule.
func AskPath(id string) string {
	if !ValidAskID(id) {
		return ""
	}
	return AsksPath + "/" + id
}

// AskWithdrawPath is the agent route that withdraws an open ask (POST). It
// returns "" for an invalid id.
func AskWithdrawPath(id string) string {
	if p := AskPath(id); p != "" {
		return p + AskWithdrawSuffix
	}
	return ""
}

// PortalAskPath is the portal route of one ask: GET reads it in any state
// (push links and board selection). It returns "" for an invalid id.
func PortalAskPath(id string) string {
	if !ValidAskID(id) {
		return ""
	}
	return PortalAsksPath + "/" + id
}

// PortalAskAnswerPath is the portal route that answers an open ask (POST). It
// returns "" for an invalid id.
func PortalAskAnswerPath(id string) string {
	if p := PortalAskPath(id); p != "" {
		return p + AskAnswerSuffix
	}
	return ""
}

// List queries. GET AsksPath takes AskQueryMine ("true": the caller's own
// asks); GET PortalAsksPath takes AskQueryState (AskStateFilterOpen or
// AskStateFilterAll). Both take AskQueryLimit (1 to AskListMaxLimit;
// AskListDefaultLimit when absent) and AskQueryCursor (the previous page's
// opaque nextCursor). Pages are newest first.
const (
	AskQueryMine   = "mine"
	AskQueryState  = "state"
	AskQueryLimit  = "limit"
	AskQueryCursor = "cursor"

	AskStateFilterOpen = "open"
	AskStateFilterAll  = "all"

	AskListDefaultLimit = 20
	AskListMaxLimit     = 100
)

// Limits. Lengths of free text are in runes; AskLinkMaxLen and
// AskOptionIDMaxLen are in bytes (the values are ASCII or URLs).
const (
	AskQuestionMaxRunes          = 1000
	AskMaxOptions                = 6
	AskOptionIDMaxLen            = 24
	AskOptionLabelMaxRunes       = 80
	AskOptionDescriptionMaxRunes = 300
	AskMaxLinks                  = 5
	AskLinkMaxLen                = 300
	AskScopeFieldMaxRunes        = 200
	AskAnswerTextMaxRunes        = 2000
	AskActorIssuerMaxRunes       = 300
	AskActorSubjectMaxRunes      = 200
	AskActorDisplayNameMaxRunes  = 120
	AskSessionNameMaxLen         = 253
	AskSessionUIDMaxLen          = 128
	AskInboxMessageMaxLen        = 253

	// AskMinTTL is the shortest lifetime of an ask and of an approval scope.
	AskMinTTL     = time.Minute
	AskDefaultTTL = 24 * time.Hour
	AskMaxTTL     = 7 * 24 * time.Hour
	// AskScopeMaxTTL is the v1 cap on an approval's scope, counted from the
	// ask's creation (contract v1.1 A8): nothing can revoke an approval until
	// follow-up F2, so none lives longer than a day.
	AskScopeMaxTTL = 24 * time.Hour
)

// AskType says what the ask wants from the human.
type AskType string

const (
	AskTypeInfo     AskType = "info"
	AskTypeApproval AskType = "approval"
)

// AskState is the stored lifecycle of one ask. Only "open" can change. An
// open ask past its expiry may still be stored as open until the sweeper
// runs; Ask.EffectiveState reads it as expired.
type AskState string

const (
	AskStateOpen      AskState = "open"
	AskStateAnswered  AskState = "answered"
	AskStateWithdrawn AskState = "withdrawn"
	AskStateExpired   AskState = "expired"
)

var (
	askIDPattern        = regexp.MustCompile(`^ask-[0-9a-f]{16}$`)
	askRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{8,64}$`)
	askOptionIDPattern  = regexp.MustCompile(`^[a-z0-9-]{1,24}$`)
)

// Errors. Every validation failure wraps ErrAskInvalid, so a handler maps it
// to 400 with errors.Is; the message names the field and the rule, never a
// value. ErrAskNotOpen (the ask was answered or withdrawn: 409) and
// ErrAskExpired (the ask or its approval scope has expired: 410) are lifecycle
// refusals from ValidateForAt. They do not wrap ErrAskInvalid, so the three
// stay distinguishable.
var (
	ErrAskInvalid = errors.New("invalid ask")
	ErrAskNotOpen = errors.New("ask is not open")
	ErrAskExpired = errors.New("ask has expired")
)

func askInvalid(field, rule string) error {
	return fmt.Errorf("%w: %s: %s", ErrAskInvalid, field, rule)
}

// ValidAskID reports whether id has the server-generated shape "ask-" plus 16
// lowercase hex digits.
func ValidAskID(id string) bool { return askIDPattern.MatchString(id) }

// ValidAskRequestID reports whether id is a well-formed idempotency key.
func ValidAskRequestID(id string) bool { return askRequestIDPattern.MatchString(id) }

// NormalizeAskText converts CRLF and lone CR line endings to LF and changes
// nothing else. The validators refuse CR, so the portal and the MCP tools
// normalize every multi-line field (question, option descriptions, answer
// text) with it before they call Validate.
func NormalizeAskText(s string) string {
	if !strings.ContainsRune(s, '\r') {
		return s
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
}

// AskOption is one choice the human can pick.
type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskScope names the single action an approval authorises. It is required for
// type approval and forbidden for type info. ExpiresAt is 1m to 24h after the
// ask's creation in v1 (AskScopeMaxTTL), and the ask expires no later than it.
type AskScope struct {
	Action    string    `json:"action"`
	Target    string    `json:"target"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// AskRequestContext is the part of the context the caller supplies. The
// server fills the session name and UID from the caller's identity.
type AskRequestContext struct {
	WorkPackage int      `json:"workPackage,omitempty"`
	Links       []string `json:"links,omitempty"`
}

// AskContext ties an ask to the session that raised it.
type AskContext struct {
	SessionName string   `json:"sessionName"`
	SessionUID  string   `json:"sessionUID"`
	WorkPackage int      `json:"workPackage,omitempty"`
	Links       []string `json:"links,omitempty"`
}

// AskActor is the human who answered, as the portal backend authenticated
// them. It is a claim of the portal, not something the browser supplies.
//
// There are three views of it (contract v1.1 A2). The operator stores all
// three fields. The session-facing /v1/asks routes return DisplayName only
// (see SessionView), so Issuer and Subject are omitted there. The portal
// routes return the full view, and the portal may not know a display name, so
// that view can lack DisplayName. ValidateAsk therefore accepts the actor of
// an answered ask when it has a non-blank DisplayName, or both Issuer and
// Subject; it refuses an actor with none of the three, and validates each
// field that is present. AskAnswerRequest.Actor (portal to operator) still
// requires Issuer and Subject; DisplayName stays optional there.
type AskActor struct {
	Issuer      string `json:"issuer,omitempty"`
	Subject     string `json:"subject,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// AskActorFallbackDisplayName is the display name of a session view whose
// actor has none, so a session never sees a blank answerer.
const AskActorFallbackDisplayName = "Portal user"

// SessionView returns the actor as a session-facing route shows it: the
// display name only, with Issuer and Subject empty (and so omitted from JSON).
// An empty display name becomes AskActorFallbackDisplayName, so the view is
// never blank and passes ValidateAsk.
func (a AskActor) SessionView() AskActor {
	if a.DisplayName == "" {
		return AskActor{DisplayName: AskActorFallbackDisplayName}
	}
	return AskActor{DisplayName: a.DisplayName}
}

// String and GoString redact an actor formatted by accident (%v, %+v, %#v,
// a log line): issuer and subject identify a person.
func (AskActor) String() string     { return "[redacted ask actor]" }
func (a AskActor) GoString() string { return a.String() }

// AskAnswer is the retained answer of an answered ask. Text may span lines.
type AskAnswer struct {
	OptionID string `json:"optionId"`
	Text     string `json:"text,omitempty"`
	// Approved IS the decision of a type approval ask: true approves, false
	// denies, and it is never nil there. It is nil for type info. An OptionID
	// on an approval is advisory context and never overrides Approved; the
	// portal keeps the two from contradicting each other (contract v1.1 A15).
	// Use Ask.ApprovalValid to decide whether an approval is in force.
	Approved   *bool     `json:"approved,omitempty"`
	AnsweredBy AskActor  `json:"answeredBy"`
	AnsweredAt time.Time `json:"answeredAt"`
}

// String and GoString redact an answer formatted by accident (%v, %+v, %#v,
// a log line): the text is the human's words and the actor identifies them.
// They show only whether text is set and its length in characters.
func (a AskAnswer) String() string {
	return fmt.Sprintf("[redacted ask answer: text set=%t, %d characters]", a.Text != "", utf8.RuneCountInString(a.Text))
}
func (a AskAnswer) GoString() string { return a.String() }

// AskDelivery records the inbox message that carried the answer back. It is
// always empty in v1: the agent reads the answer from the Ask itself
// (contract v1.1 A1). Operator-origin inbox delivery is follow-up F1.
type AskDelivery struct {
	InboxMessage string    `json:"inboxMessage"`
	DeliveredAt  time.Time `json:"deliveredAt"`
}

// Ask is one record as the server returns it.
type Ask struct {
	ID            string       `json:"id"`
	Type          AskType      `json:"type"`
	Question      string       `json:"question"`
	Options       []AskOption  `json:"options,omitempty"`
	Recommended   string       `json:"recommended"`
	AllowFreeText bool         `json:"allowFreeText"`
	Context       AskContext   `json:"context"`
	Scope         *AskScope    `json:"scope,omitempty"`
	CreatedAt     time.Time    `json:"createdAt"`
	ExpiresAt     time.Time    `json:"expiresAt"`
	State         AskState     `json:"state"`
	Answer        *AskAnswer   `json:"answer,omitempty"`
	Delivery      *AskDelivery `json:"delivery,omitempty"`
}

// EffectiveState is the state as of now. An open ask whose expiresAt, or, for
// an approval, whose scope.expiresAt is at or before now reads as expired,
// whether or not the sweeper has stored that yet. Other states are returned
// as stored.
func (a Ask) EffectiveState(now time.Time) AskState {
	if a.State == AskStateOpen && askLapsed(a, now) {
		return AskStateExpired
	}
	return a.State
}

// ApprovalValid reports whether the ask is a granted approval still in force
// at now: type approval, answered, approved true, and now before
// scope.expiresAt. It is computed from the record, not a server field, and is
// shown as computed (contract v1.1 A5). An expired approval is not an
// approval.
func (a Ask) ApprovalValid(now time.Time) bool {
	return a.Type == AskTypeApproval && a.State == AskStateAnswered &&
		a.Answer != nil && a.Answer.Approved != nil && *a.Answer.Approved &&
		a.Scope != nil && now.Before(a.Scope.ExpiresAt)
}

func askLapsed(a Ask, now time.Time) bool {
	if !now.Before(a.ExpiresAt) {
		return true
	}
	return a.Type == AskTypeApproval && a.Scope != nil && !now.Before(a.Scope.ExpiresAt)
}

// AskCreateRequest is the body of POST AsksPath. RequestID makes the create
// idempotent. ExpiresIn is a Go duration string such as "24h" or "90m" (not
// a number of seconds), 1m to 168h. Empty means AskDefaultTTL, except that an
// approval's default ends at its scope.expiresAt (see ExpiresAtFrom). Scope is
// required for type approval and forbidden for info.
type AskCreateRequest struct {
	RequestID     string            `json:"requestId"`
	Type          AskType           `json:"type"`
	Question      string            `json:"question"`
	Options       []AskOption       `json:"options,omitempty"`
	Recommended   string            `json:"recommended"`
	AllowFreeText bool              `json:"allowFreeText"`
	Context       AskRequestContext `json:"context"`
	Scope         *AskScope         `json:"scope,omitempty"`
	ExpiresIn     string            `json:"expiresIn,omitempty"`
}

// AskAnswerRequest is the body of POST PortalAskAnswerPath. Actor is set by
// the portal backend. Approved is required for type approval (a pointer, so
// an unset decision differs from false) and must be nil for type info; it is
// the decision, as on AskAnswer. Text may span lines (a portal textarea).
type AskAnswerRequest struct {
	OptionID string   `json:"optionId"`
	Text     string   `json:"text,omitempty"`
	Approved *bool    `json:"approved,omitempty"`
	Actor    AskActor `json:"actor"`
}

// String and GoString redact an answer request formatted by accident: it
// carries the human's text and identity.
func (AskAnswerRequest) String() string     { return "[redacted ask answer]" }
func (r AskAnswerRequest) GoString() string { return r.String() }

// AskList is one page of asks, newest first. Complete is false when
// NextCursor leads to more.
type AskList struct {
	Asks       []Ask  `json:"asks"`
	NextCursor string `json:"nextCursor,omitempty"`
	Complete   bool   `json:"complete"`
}

// MarshalJSON encodes an empty page as "asks":[], never null.
func (l AskList) MarshalJSON() ([]byte, error) {
	type plain AskList
	p := plain(l)
	if p.Asks == nil {
		p.Asks = []Ask{}
	}
	return json.Marshal(p)
}

// TTL returns the requested lifetime: AskDefaultTTL for an empty ExpiresIn,
// otherwise the parsed duration, which must be a whole number of seconds
// (no sub-second part: "1m0.5s" is refused) and AskMinTTL to AskMaxTTL.
func (r AskCreateRequest) TTL() (time.Duration, error) {
	if r.ExpiresIn == "" {
		return AskDefaultTTL, nil
	}
	d, err := time.ParseDuration(r.ExpiresIn)
	if err != nil {
		return 0, askInvalid("expiresIn", "not a Go duration such as 24h")
	}
	if d%time.Second != 0 {
		return 0, askInvalid("expiresIn", "must be a whole number of seconds")
	}
	if d < AskMinTTL || d > AskMaxTTL {
		return 0, askInvalid("expiresIn", "must be 1m to 168h")
	}
	return d, nil
}

// ExpiresAtFrom returns the ask's expiresAt for a request created at
// createdAt: createdAt plus TTL. An approval with no ExpiresIn ends at its
// scope.expiresAt when that is sooner, because an approval ask may not
// outlive its scope (contract v1.1 A8); an explicit ExpiresIn that does is
// refused by ValidateAt. Call it after ValidateAt, with the same time. A
// server truncates createdAt to whole seconds first (see the package note).
func (r AskCreateRequest) ExpiresAtFrom(createdAt time.Time) (time.Time, error) {
	d, err := r.TTL()
	if err != nil {
		return time.Time{}, err
	}
	exp := createdAt.Add(d)
	if r.ExpiresIn == "" && r.Type == AskTypeApproval && r.Scope != nil && r.Scope.ExpiresAt.Before(exp) {
		exp = r.Scope.ExpiresAt
	}
	return exp, nil
}

// Validate checks the request against the current time.
func (r AskCreateRequest) Validate() error { return r.ValidateAt(time.Now()) }

// ValidateAt checks the request as if it arrived at now. For an approval, the
// scope's expiry must be 1m to 24h after now, and the ask (ExpiresAtFrom) may
// not outlive it. A server passes now truncated to whole seconds, the same
// value it stores as createdAt.
func (r AskCreateRequest) ValidateAt(now time.Time) error {
	if !askRequestIDPattern.MatchString(r.RequestID) {
		return askInvalid("requestId", "must match ^[A-Za-z0-9-]{8,64}$")
	}
	exp, err := r.ExpiresAtFrom(now)
	if err != nil {
		return err
	}
	if err := validateAskBody(r.Type, r.Question, r.Options, r.Recommended, r.AllowFreeText,
		r.Context.WorkPackage, r.Context.Links, r.Scope); err != nil {
		return err
	}
	if r.Scope != nil {
		if err := validateAskScopeWindow(now, r.Scope.ExpiresAt); err != nil {
			return err
		}
		if exp.After(r.Scope.ExpiresAt) {
			return askInvalid("expiresIn", "an approval ask may not outlive scope.expiresAt")
		}
	}
	return nil
}

// Validate checks the shape of an answer that is not tied to an ask yet. Use
// ValidateForAt to check it against the ask it answers.
func (r AskAnswerRequest) Validate() error {
	return validateAskAnswerShape(r.OptionID, r.Text, r.Actor, false)
}

// ValidateFor is ValidateForAt at the local clock. Prefer ValidateForAt with
// the clock reading the handler also stores as answeredAt.
func (r AskAnswerRequest) ValidateFor(a Ask) error { return r.ValidateForAt(a, time.Now()) }

// ValidateForAt checks the answer against the ask at now, the answer time. A
// malformed answer wraps ErrAskInvalid (400). An ask that was answered or
// withdrawn gives ErrAskNotOpen (409). An ask whose EffectiveState at now is
// expired gives ErrAskExpired (410): stored as expired, now at or after its
// expiresAt, or, for an approval, now at or after scope.expiresAt.
//
// For type approval, approved is required and is the decision; an option is
// advisory, and text is an optional note. For type info, approved must be
// nil, and the answer needs an option or non-blank text, with text only when
// the ask allows free text.
func (r AskAnswerRequest) ValidateForAt(a Ask, now time.Time) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if a.Type == AskTypeApproval && a.Scope == nil {
		return askInvalid("scope", "required for type approval")
	}
	switch a.EffectiveState(now) {
	case AskStateOpen:
	case AskStateExpired:
		return ErrAskExpired
	default:
		return ErrAskNotOpen
	}
	return validateAskAnswerAgainst(a, r.OptionID, r.Text, r.Approved)
}

// ValidateAsk checks a record received from the server: every field rule of
// the create request, plus the id, the times, the state and the answer. It
// does not depend on the clock; use EffectiveState for that.
func ValidateAsk(a Ask) error {
	if !askIDPattern.MatchString(a.ID) {
		return askInvalid("id", "must match ^ask-[0-9a-f]{16}$")
	}
	if err := validateAskBody(a.Type, a.Question, a.Options, a.Recommended, a.AllowFreeText,
		a.Context.WorkPackage, a.Context.Links, a.Scope); err != nil {
		return err
	}
	if a.Context.SessionName == "" || len(a.Context.SessionName) > AskSessionNameMaxLen || !askPlain(a.Context.SessionName) {
		return askInvalid("context.sessionName", "required, at most 253 bytes, no control, format or invisible characters")
	}
	if a.Context.SessionUID == "" || len(a.Context.SessionUID) > AskSessionUIDMaxLen || !askPlain(a.Context.SessionUID) {
		return askInvalid("context.sessionUID", "required, at most 128 bytes, no control, format or invisible characters")
	}
	if a.CreatedAt.IsZero() {
		return askInvalid("createdAt", "required")
	}
	if a.ExpiresAt.Before(a.CreatedAt.Add(AskMinTTL)) || a.ExpiresAt.After(a.CreatedAt.Add(AskMaxTTL)) {
		return askInvalid("expiresAt", "must be 1m to 7d after createdAt")
	}
	if a.Scope != nil {
		if err := validateAskScopeWindow(a.CreatedAt, a.Scope.ExpiresAt); err != nil {
			return err
		}
		if a.ExpiresAt.After(a.Scope.ExpiresAt) {
			return askInvalid("expiresAt", "an approval ask may not outlive scope.expiresAt")
		}
	}
	switch a.State {
	case AskStateOpen, AskStateWithdrawn, AskStateExpired:
		if a.Answer != nil {
			return askInvalid("answer", "only an answered ask has an answer")
		}
	case AskStateAnswered:
		if a.Answer == nil {
			return askInvalid("answer", "an answered ask must carry its answer")
		}
		if err := validateAskAnswerShape(a.Answer.OptionID, a.Answer.Text, a.Answer.AnsweredBy, true); err != nil {
			return err
		}
		if err := validateAskAnswerAgainst(a, a.Answer.OptionID, a.Answer.Text, a.Answer.Approved); err != nil {
			return err
		}
		if a.Answer.AnsweredAt.IsZero() {
			return askInvalid("answer.answeredAt", "required")
		}
		// The answer route refuses an answer at or after expiresAt.
		if a.Answer.AnsweredAt.Before(a.CreatedAt) || !a.Answer.AnsweredAt.Before(a.ExpiresAt) {
			return askInvalid("answer.answeredAt", "must be at or after createdAt and before expiresAt")
		}
	default:
		return askInvalid("state", "must be open, answered, withdrawn or expired")
	}
	if a.Delivery != nil {
		if a.State != AskStateAnswered {
			return askInvalid("delivery", "only an answered ask has a delivery")
		}
		if a.Delivery.InboxMessage == "" || len(a.Delivery.InboxMessage) > AskInboxMessageMaxLen || !askPlain(a.Delivery.InboxMessage) {
			return askInvalid("delivery.inboxMessage", "required, at most 253 bytes, no control, format or invisible characters")
		}
		if a.Delivery.DeliveredAt.IsZero() {
			return askInvalid("delivery.deliveredAt", "required")
		}
	}
	return nil
}

// validateAskScopeWindow applies the v1 approval window: the scope expires
// AskMinTTL to AskScopeMaxTTL after from (the create time).
func validateAskScopeWindow(from, scopeExpiresAt time.Time) error {
	if scopeExpiresAt.Before(from.Add(AskMinTTL)) || scopeExpiresAt.After(from.Add(AskScopeMaxTTL)) {
		return askInvalid("scope.expiresAt", "must be 1m to 24h after the ask is created")
	}
	return nil
}

// validateAskBody checks the fields a create request and a stored ask share.
func validateAskBody(t AskType, question string, options []AskOption, recommended string,
	allowFreeText bool, workPackage int, links []string, scope *AskScope) error {
	switch t {
	case AskTypeInfo:
		if scope != nil {
			return askInvalid("scope", "forbidden for type info")
		}
	case AskTypeApproval:
		if scope == nil {
			return askInvalid("scope", "required for type approval")
		}
		if err := askText("scope.action", scope.Action, 1, AskScopeFieldMaxRunes, false); err != nil {
			return err
		}
		if err := askText("scope.target", scope.Target, 1, AskScopeFieldMaxRunes, false); err != nil {
			return err
		}
		if scope.ExpiresAt.IsZero() {
			return askInvalid("scope.expiresAt", "required")
		}
	default:
		return askInvalid("type", "must be info or approval")
	}
	if err := askText("question", question, 1, AskQuestionMaxRunes, true); err != nil {
		return err
	}
	if len(options) > AskMaxOptions {
		return askInvalid("options", "at most 6")
	}
	seen := make(map[string]bool, len(options))
	for _, o := range options {
		if !askOptionIDPattern.MatchString(o.ID) {
			return askInvalid("options.id", "must match ^[a-z0-9-]{1,24}$")
		}
		if seen[o.ID] {
			return askInvalid("options.id", "must be unique")
		}
		seen[o.ID] = true
		if err := askText("options.label", o.Label, 1, AskOptionLabelMaxRunes, false); err != nil {
			return err
		}
		if err := askText("options.description", o.Description, 0, AskOptionDescriptionMaxRunes, true); err != nil {
			return err
		}
	}
	if recommended != "" && !seen[recommended] {
		return askInvalid("recommended", "must be empty or an option id")
	}
	// An info ask nobody can answer would sit open until it expires. An
	// approval is answered by approve or deny alone.
	if t == AskTypeInfo && len(options) == 0 && !allowFreeText {
		return askInvalid("options", "need at least one option or allowFreeText")
	}
	if workPackage < 0 {
		return askInvalid("context.workPackage", "must be positive when set")
	}
	if len(links) > AskMaxLinks {
		return askInvalid("context.links", "at most 5")
	}
	for _, l := range links {
		if err := validateAskLink(l); err != nil {
			return err
		}
	}
	return nil
}

func validateAskLink(l string) error {
	if len(l) == 0 || len(l) > AskLinkMaxLen {
		return askInvalid("context.links", "each 1 to 300 bytes")
	}
	// Printable ASCII only: no spaces, controls or raw non-ASCII. A URL that
	// needs more is percent-encoded or punycode.
	for i := 0; i < len(l); i++ {
		if l[i] <= 0x20 || l[i] >= 0x7f {
			return askInvalid("context.links", "each must be printable ASCII without spaces")
		}
	}
	u, err := url.Parse(l)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Opaque != "" {
		return askInvalid("context.links", "each must be an absolute https URL without credentials")
	}
	return nil
}

// validateAskAnswerShape checks an answer's option, text and actor. With
// recorded false (the portal's request) issuer and subject are required and
// displayName is optional. With recorded true (a stored or viewed answer) the
// actor needs a displayName, or both issuer and subject, because the session
// view omits issuer and subject and the portal may lack a display name; each
// field that is present is validated either way.
func validateAskAnswerShape(optionID, text string, actor AskActor, recorded bool) error {
	if optionID != "" && !askOptionIDPattern.MatchString(optionID) {
		return askInvalid("optionId", "must be empty or match ^[a-z0-9-]{1,24}$")
	}
	// Answer text is multi-line (the portal textarea).
	if err := askText("text", text, 0, AskAnswerTextMaxRunes, true); err != nil {
		return err
	}
	// A present value always gets the blank check (min 1).
	minFor := func(required bool, v string) int {
		if required || v != "" {
			return 1
		}
		return 0
	}
	if err := askText("actor.issuer", actor.Issuer, minFor(!recorded, actor.Issuer), AskActorIssuerMaxRunes, false); err != nil {
		return err
	}
	if err := askText("actor.subject", actor.Subject, minFor(!recorded, actor.Subject), AskActorSubjectMaxRunes, false); err != nil {
		return err
	}
	if err := askText("actor.displayName", actor.DisplayName, minFor(false, actor.DisplayName), AskActorDisplayNameMaxRunes, false); err != nil {
		return err
	}
	if recorded && actor.DisplayName == "" && (actor.Issuer == "" || actor.Subject == "") {
		return askInvalid("actor", "needs a displayName, or an issuer and a subject")
	}
	return nil
}

func validateAskAnswerAgainst(a Ask, optionID, text string, approved *bool) error {
	if optionID != "" {
		found := false
		for _, o := range a.Options {
			if o.ID == optionID {
				found = true
				break
			}
		}
		if !found {
			return askInvalid("optionId", "not an option of this ask")
		}
	}
	switch a.Type {
	case AskTypeApproval:
		// Approve or deny is the answer; text is always allowed as a note.
		if approved == nil {
			return askInvalid("approved", "required for type approval")
		}
	case AskTypeInfo:
		if approved != nil {
			return askInvalid("approved", "only meaningful for type approval")
		}
		if optionID == "" && askBlank(text) {
			return askInvalid("optionId", "an answer needs an option or non-blank text")
		}
		if text != "" && !a.AllowFreeText {
			return askInvalid("text", "this ask does not allow free text")
		}
	default:
		return askInvalid("type", "must be info or approval")
	}
	return nil
}

// askZWJ is U+200D ZERO WIDTH JOINER, which joins emoji sequences. It is the
// one format character allowed, and only in multi-line fields.
const askZWJ = '\u200d'

// askBrailleBlank (U+2800) is allowed everywhere but counts as blank.
const askBrailleBlank = '\u2800'

// askVariationSelector reports U+FE00 to U+FE0F, which select an emoji or
// text presentation. They are allowed only in multi-line fields.
func askVariationSelector(r rune) bool { return r >= 0xFE00 && r <= 0xFE0F }

// askInvisible reports a character with no visible glyph that is refused in
// every field: the Hangul fillers (U+115F, U+1160, U+3164, U+FFA0), the
// combining grapheme joiner (U+034F) and the variation selectors supplement
// (U+E0100 to U+E01EF). They are not format characters (category Cf), but
// they let text look blank or hide in other text.
func askInvisible(r rune) bool {
	switch r {
	case 0x115F, 0x1160, 0x3164, 0xFFA0, 0x034F:
		return true
	}
	return r >= 0xE0100 && r <= 0xE01EF
}

// askText checks one text field: valid UTF-8, at most max runes, not blank
// when min is positive (blank: only whitespace, format characters and
// invisible characters), at least min runes, and no control, separator,
// format or invisible character. A multi-line field (question, option
// description, answer text) also allows LF, tab, the zero-width joiner and
// the variation selectors U+FE00 to U+FE0F. Format characters (zero-width
// spaces, bidi marks, overrides and isolates, the BOM, soft hyphen, tags)
// let text hide or mimic other text on the approval page.
func askText(field, s string, min, max int, multiLine bool) error {
	if !utf8.ValidString(s) {
		return askInvalid(field, "must be valid UTF-8")
	}
	n := utf8.RuneCountInString(s)
	if n > max {
		return askInvalid(field, fmt.Sprintf("must be %d to %d characters", min, max))
	}
	if min > 0 && askBlank(s) {
		return askInvalid(field, "must not be blank")
	}
	if n < min {
		return askInvalid(field, fmt.Sprintf("must be %d to %d characters", min, max))
	}
	for _, r := range s {
		if multiLine && (r == '\n' || r == '\t' || r == askZWJ || askVariationSelector(r)) {
			continue
		}
		if askForbiddenRune(r) {
			return askInvalid(field, "contains a control, format or invisible character")
		}
	}
	return nil
}

// askBlank reports whether s holds nothing visible: only whitespace, format
// characters, the zero-width joiner, variation selectors, invisible
// characters and the braille blank. A required field counts it as empty.
func askBlank(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) && !askFormatRune(r) && !askVariationSelector(r) &&
			!askInvisible(r) && r != askBrailleBlank {
			return false
		}
	}
	return true
}

func askPlain(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if askForbiddenRune(r) {
			return false
		}
	}
	return true
}

func askForbiddenRune(r rune) bool {
	return unicode.IsControl(r) || r == 0x2028 || r == 0x2029 || askFormatRune(r) ||
		askInvisible(r) || askVariationSelector(r)
}

// askFormatRune reports a Unicode format character (category Cf: U+00AD,
// U+061C, U+180E, U+200B to U+200F, U+202A to U+202E, U+2060 to U+2064,
// U+2066 to U+206F, U+FEFF, U+FFF9 to U+FFFB, the tag characters and more),
// plus the unassigned rest of the U+2060 to U+206F and U+E0000 to U+E007F
// blocks, so a later Unicode version cannot open a gap.
func askFormatRune(r rune) bool {
	return unicode.Is(unicode.Cf, r) || (r >= 0x2060 && r <= 0x206F) || (r >= 0xE0000 && r <= 0xE007F)
}
