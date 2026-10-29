package apiv1

// Wire types for agent asks and approvals (Epic #254, lane 0). An agent
// creates an Ask (a question for a human, or an approval request for one
// action on one target); a human answers it through the portal. This file
// holds only the shapes, the route constants and the validators. The server
// derives ids, timestamps, session context, state and delivery; a request
// carries intent only. Standard library only, like the rest of apiv1.

import (
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
// API answers them. Both hang one ask off "/{id}".
const (
	AsksPath       = "/v1/asks"
	PortalAsksPath = "/v1/portal/asks"

	AskWithdrawSuffix = "/withdraw"
	AskAnswerSuffix   = "/answer"
)

// AskPath is the agent route of one ask: GET reads it.
func AskPath(id string) string { return AsksPath + "/" + id }

// AskWithdrawPath is the agent route that withdraws an open ask (POST).
func AskWithdrawPath(id string) string { return AskPath(id) + AskWithdrawSuffix }

// PortalAskPath is the portal route of one ask: GET reads it.
func PortalAskPath(id string) string { return PortalAsksPath + "/" + id }

// PortalAskAnswerPath is the portal route that answers an open ask (POST).
func PortalAskAnswerPath(id string) string { return PortalAskPath(id) + AskAnswerSuffix }

// PortalAskWithdrawPath is the portal route that withdraws an open ask (POST).
func PortalAskWithdrawPath(id string) string { return PortalAskPath(id) + AskWithdrawSuffix }

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

	AskDefaultTTL = 24 * time.Hour
	AskMaxTTL     = 7 * 24 * time.Hour
)

// AskType says what the ask wants from the human.
type AskType string

const (
	AskTypeInfo     AskType = "info"
	AskTypeApproval AskType = "approval"
)

// AskState is the lifecycle of one ask. Only "open" can change.
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

// ErrAskInvalid wraps every validation failure, so a handler can map it to a
// 400 with errors.Is. The message names the field and the rule, never a value.
var ErrAskInvalid = errors.New("invalid ask")

func askInvalid(field, rule string) error {
	return fmt.Errorf("%w: %s: %s", ErrAskInvalid, field, rule)
}

// ValidAskID reports whether id has the server-generated shape "ask-" plus 16
// lowercase hex digits.
func ValidAskID(id string) bool { return askIDPattern.MatchString(id) }

// ValidAskRequestID reports whether id is a well-formed idempotency key.
func ValidAskRequestID(id string) bool { return askRequestIDPattern.MatchString(id) }

// AskOption is one choice the human can pick.
type AskOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AskScope names the single action an approval authorises. It is required for
// type approval and forbidden for type info.
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
type AskActor struct {
	Issuer      string `json:"issuer"`
	Subject     string `json:"subject"`
	DisplayName string `json:"displayName,omitempty"`
}

// AskAnswer is the retained answer of an answered ask. Approved is set (true
// or false) for type approval and nil for type info. Text may span lines.
type AskAnswer struct {
	OptionID   string    `json:"optionId"`
	Text       string    `json:"text,omitempty"`
	Approved   *bool     `json:"approved,omitempty"`
	AnsweredBy AskActor  `json:"answeredBy"`
	AnsweredAt time.Time `json:"answeredAt"`
}

// AskDelivery records the inbox message that carried the answer back.
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

// AskCreateRequest is the body of POST AsksPath. RequestID makes the create
// idempotent. ExpiresIn is a Go duration string such as "24h" or "90m" (not
// a number of seconds); empty means AskDefaultTTL, and it may not exceed
// AskMaxTTL. Scope is required for type approval and forbidden for info.
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
// an unset decision differs from false) and must be nil for type info. Text
// may span lines (a portal textarea).
type AskAnswerRequest struct {
	OptionID string   `json:"optionId"`
	Text     string   `json:"text,omitempty"`
	Approved *bool    `json:"approved,omitempty"`
	Actor    AskActor `json:"actor"`
}

// AskList is one page of asks. Complete is false when NextCursor leads to
// more.
type AskList struct {
	Asks       []Ask  `json:"asks"`
	NextCursor string `json:"nextCursor,omitempty"`
	Complete   bool   `json:"complete"`
}

// TTL returns the requested lifetime: AskDefaultTTL for an empty ExpiresIn,
// otherwise the parsed duration, which must be positive and at most AskMaxTTL.
func (r AskCreateRequest) TTL() (time.Duration, error) {
	if r.ExpiresIn == "" {
		return AskDefaultTTL, nil
	}
	d, err := time.ParseDuration(r.ExpiresIn)
	if err != nil {
		return 0, askInvalid("expiresIn", "not a Go duration such as 24h")
	}
	if d <= 0 || d > AskMaxTTL {
		return 0, askInvalid("expiresIn", "must be positive and at most 168h")
	}
	return d, nil
}

// Validate checks the request against the current time.
func (r AskCreateRequest) Validate() error { return r.ValidateAt(time.Now()) }

// ValidateAt checks the request as if it arrived at now. The scope's expiry
// must be after now and at most AskMaxTTL after it.
func (r AskCreateRequest) ValidateAt(now time.Time) error {
	if !askRequestIDPattern.MatchString(r.RequestID) {
		return askInvalid("requestId", "must match ^[A-Za-z0-9-]{8,64}$")
	}
	if _, err := r.TTL(); err != nil {
		return err
	}
	if err := validateAskBody(r.Type, r.Question, r.Options, r.Recommended, r.AllowFreeText,
		r.Context.WorkPackage, r.Context.Links, r.Scope); err != nil {
		return err
	}
	if r.Scope != nil {
		if !r.Scope.ExpiresAt.After(now) || r.Scope.ExpiresAt.After(now.Add(AskMaxTTL)) {
			return askInvalid("scope.expiresAt", "must be after now and at most 7d from now")
		}
	}
	return nil
}

// Validate checks the shape of an answer that is not tied to an ask yet. Use
// ValidateFor to check it against the ask it answers.
func (r AskAnswerRequest) Validate() error {
	return validateAskAnswerShape(r.OptionID, r.Text, r.Actor)
}

// ValidateFor checks the answer against the ask: the ask must be open and any
// option must exist. For type approval, approved is required and text is an
// optional note. For type info, approved must be nil, and the answer needs an
// option or text, with text only when the ask allows free text.
func (r AskAnswerRequest) ValidateFor(a Ask) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if a.State != AskStateOpen {
		return askInvalid("state", "ask is not open")
	}
	return validateAskAnswerAgainst(a, r.OptionID, r.Text, r.Approved)
}

// ValidateAsk checks a record received from the server: every field rule of
// the create request, plus the id, the times, the state and the answer.
func ValidateAsk(a Ask) error {
	if !askIDPattern.MatchString(a.ID) {
		return askInvalid("id", "must match ^ask-[0-9a-f]{16}$")
	}
	if err := validateAskBody(a.Type, a.Question, a.Options, a.Recommended, a.AllowFreeText,
		a.Context.WorkPackage, a.Context.Links, a.Scope); err != nil {
		return err
	}
	if a.Context.SessionName == "" || len(a.Context.SessionName) > AskSessionNameMaxLen || !askPlain(a.Context.SessionName) {
		return askInvalid("context.sessionName", "required, at most 253 bytes, no control characters")
	}
	if a.Context.SessionUID == "" || len(a.Context.SessionUID) > AskSessionUIDMaxLen || !askPlain(a.Context.SessionUID) {
		return askInvalid("context.sessionUID", "required, at most 128 bytes, no control characters")
	}
	if a.CreatedAt.IsZero() {
		return askInvalid("createdAt", "required")
	}
	if !a.ExpiresAt.After(a.CreatedAt) || a.ExpiresAt.After(a.CreatedAt.Add(AskMaxTTL)) {
		return askInvalid("expiresAt", "must be after createdAt and at most 7d later")
	}
	if a.Scope != nil && (!a.Scope.ExpiresAt.After(a.CreatedAt) || a.Scope.ExpiresAt.After(a.CreatedAt.Add(AskMaxTTL))) {
		return askInvalid("scope.expiresAt", "must be after createdAt and at most 7d later")
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
		if err := validateAskAnswerShape(a.Answer.OptionID, a.Answer.Text, a.Answer.AnsweredBy); err != nil {
			return err
		}
		if err := validateAskAnswerAgainst(a, a.Answer.OptionID, a.Answer.Text, a.Answer.Approved); err != nil {
			return err
		}
		if a.Answer.AnsweredAt.IsZero() {
			return askInvalid("answer.answeredAt", "required")
		}
	default:
		return askInvalid("state", "must be open, answered, withdrawn or expired")
	}
	if a.Delivery != nil {
		if a.Delivery.InboxMessage == "" || len(a.Delivery.InboxMessage) > AskInboxMessageMaxLen || !askPlain(a.Delivery.InboxMessage) {
			return askInvalid("delivery.inboxMessage", "required, at most 253 bytes, no control characters")
		}
		if a.Delivery.DeliveredAt.IsZero() {
			return askInvalid("delivery.deliveredAt", "required")
		}
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

func validateAskAnswerShape(optionID, text string, actor AskActor) error {
	if optionID != "" && !askOptionIDPattern.MatchString(optionID) {
		return askInvalid("optionId", "must be empty or match ^[a-z0-9-]{1,24}$")
	}
	// Answer text may span lines (the portal textarea); other controls stay out.
	if err := askText("text", text, 0, AskAnswerTextMaxRunes, true); err != nil {
		return err
	}
	if err := askText("actor.issuer", actor.Issuer, 1, AskActorIssuerMaxRunes, false); err != nil {
		return err
	}
	if err := askText("actor.subject", actor.Subject, 1, AskActorSubjectMaxRunes, false); err != nil {
		return err
	}
	return askText("actor.displayName", actor.DisplayName, 0, AskActorDisplayNameMaxRunes, false)
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
		if optionID == "" && text == "" {
			return askInvalid("optionId", "an answer needs an option or text")
		}
		if text != "" && !a.AllowFreeText {
			return askInvalid("text", "this ask does not allow free text")
		}
	default:
		return askInvalid("type", "must be info or approval")
	}
	return nil
}

// askText checks one text field: valid UTF-8, a rune count within [min,max],
// not blank when min is positive, and no control characters (a newline is
// allowed only when allowNewline is set). Bidirectional overrides and line or
// paragraph separators are refused too: they let text mimic other text on the
// approval page.
func askText(field, s string, min, max int, allowNewline bool) error {
	if !utf8.ValidString(s) {
		return askInvalid(field, "must be valid UTF-8")
	}
	n := utf8.RuneCountInString(s)
	if n < min || n > max {
		return askInvalid(field, fmt.Sprintf("must be %d to %d characters", min, max))
	}
	if min > 0 && strings.TrimSpace(s) == "" {
		return askInvalid(field, "must not be blank")
	}
	for _, r := range s {
		if r == '\n' && allowNewline {
			continue
		}
		if askForbiddenRune(r) {
			return askInvalid(field, "contains a control character")
		}
	}
	return nil
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
	if unicode.IsControl(r) {
		return true
	}
	switch {
	case r == 0x2028 || r == 0x2029:
		return true
	case r >= 0x202A && r <= 0x202E:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}
