package portal

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

//go:embed attention.html
var pageSource string

//go:embed views.html
var viewsSource string

//go:embed attention.css
var pageStyle string

//go:embed views.css
var viewsStyle string

//go:embed receipt.html
var receiptSource string

//go:embed launch_result.html
var launchResultSource string

var (
	exactHead     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	contentPolicy = policy(pageStyle)
	// ProductPolicy is the Sessions and Tasks pages' Content-Security-Policy: no
	// script, no form, no external resource, and only the embedded views stylesheet.
	ProductPolicy = policy(viewsStyle)
	// ProductSendPolicy is ProductPolicy for a Sessions page that holds a send
	// form: forms may post to this origin only. Still no script.
	ProductSendPolicy = strings.Replace(ProductPolicy, "form-action 'none'", "form-action 'self'", 1)
	// Only the embedded, repository-owned CSS enters this template source.
	pageTemplate         = template.Must(template.New("attention").Parse(strings.Replace(pageSource, "/*PORTAL_STYLE*/", pageStyle, 1)))
	viewsTemplate        = template.Must(template.New("views").Parse(strings.Replace(viewsSource, "/*PORTAL_STYLE*/", viewsStyle, 1)))
	receiptTemplate      = template.Must(template.New("receipt").Parse(strings.Replace(receiptSource, "/*PORTAL_STYLE*/", viewsStyle, 1)))
	launchResultTemplate = template.Must(template.New("launch-result").Parse(strings.Replace(launchResultSource, "/*PORTAL_STYLE*/", viewsStyle, 1)))
)

// Standalone pages use the product navigation without depending on ProductPage.
func init() {
	for _, target := range []*template.Template{pagesTemplate, receiptTemplate, launchResultTemplate} {
		for _, name := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
			// html/template rewrites trees during escaping; each template set owns its copy.
			template.Must(target.AddParseTree(name, viewsTemplate.Lookup(name).Tree.Copy()))
		}
	}
}

func policy(style string) string {
	digest := sha256.Sum256([]byte(style))
	return "default-src 'none'; style-src 'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

func validText(s string) bool { return len(s) <= 512 && utf8.ValidString(s) }

func display(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	visible := false
	for _, r := range s {
		if !unicode.IsSpace(r) && !unicode.IsMark(r) && r != 0x115f && r != 0x1160 && r != 0x2800 && r != 0x3164 && r != 0xffa0 {
			visible = true
		}
	}
	if !visible {
		return "Unknown"
	}
	return strings.TrimSpace(s)
}

type field struct{ Label, Value, Class string }
type rowView struct {
	ID, Title, Freshness, Link, Summary string
	Primary, Secondary                  []field
}
type groupView struct {
	ID, Title, Count string
	Rows             []rowView
}

// ProductField is one label and value. Mono sets identifiers in monospace.
type ProductField struct {
	Label, Value string
	Mono         bool
}

// ProductStatus is a status badge. Tone is one of ok, busy, wait, bad, info
// or muted; anything else renders muted.
type ProductStatus struct{ Text, Tone string }

// ProductStat is one usage headline figure. A muted tone marks Unknown.
type ProductStat struct{ Label, Value, Note, Tone string }

// ProductState is one badge with a plain sentence beside it: how complete or
// how fresh a panel's figures are.
type ProductState struct {
	Status ProductStatus
	Text   string
}

// ProductSection is one panel of a row's detail: optional state lines, tiles,
// a table, key-value rows and one short note, in that order. A table's Title is
// its accessible caption; its rows are never linked.
type ProductSection struct {
	Title           string
	States          []ProductState
	Metrics, Fields []ProductField
	Table           *ProductUsageTable
	Note            string
}

// ProductAttention explains an existing Needs attention state. Commands are
// displayed as text only; Link is the registered Remote Control header URL.
type ProductAttention struct {
	Reason, When, NextAction, Link string
	Commands                       []string
}

// ProductRow is one list item and its detail. ID is the in-page anchor, s or t
// followed by digits; a row with any other ID is not rendered.
type ProductRow struct {
	ID, Title, Kicker, Description, Meta, Age string
	Crumb, Link, LinkLabel, ContextTitle      string
	MonoTitle                                 bool
	Status                                    ProductStatus
	Badges                                    []string
	Sections                                  []ProductSection
	Context                                   []ProductField
	Messages                                  *ProductMessages
	Attention                                 *ProductAttention
	// Selected marks the one row the request selected by its Session UID
	// (Sessions ?session=). It is the default detail on every viewport and
	// is never trimmed. Only the first selected row counts.
	Selected bool
	// Fixture marks a synthetic task. The list template renders a Fixture
	// badge only when it is set; Sessions leave it false. The word cannot
	// live only in Badges: that slice is the detail meta line, and an owner
	// can be named Fixture.
	Fixture bool
}

// ProductMessages is one session's message box. Without Send the box never
// submits: Gate says why and History says what the portal cannot read. Route
// is the existing delivery route's state for this session; Command, when set,
// is that route. Draftable enables the draft box. Session and SessionUID name
// the target as the operator reported it; a send form posts both.
type ProductMessages struct {
	Agent, Placeholder  string
	History, Gate       string
	Route               ProductStatus
	RouteNote, Command  string
	Session, SessionUID string
	Draftable           bool
	Send                *ProductSend
	// HistoryHref selects this session to load its conversation; Conversation
	// is that conversation, set only on the selected session.
	HistoryHref  string
	Conversation *ProductConversation
}

// ProductTurn is one conversation turn, rendered as escaped plaintext. Role is
// user, assistant or message (a delivered message from From); any other role
// renders as an event. At is display text.
type ProductTurn struct {
	Role, From, At, Text string
	Truncated            bool
}

// Who names a turn's speaker for people.
func (t ProductTurn) Who() string {
	switch t.Role {
	case "user":
		return "User"
	case "assistant":
		return "Agent"
	case "message":
		if t.From != "" {
			return "Message from " + t.From
		}
		return "Message"
	}
	return "Event"
}

// RoleClass is the turn's style class, from a fixed set.
func (t ProductTurn) RoleClass() string {
	switch t.Role {
	case "user", "assistant", "message":
		return "turn-" + t.Role
	}
	return "turn-event"
}

// ProductConversation is the selected session's conversation as its source
// reported it, oldest turn first. State is Complete, Partial, Truncated or
// Unavailable, and Note says what it covers. Unavailable has no turns.
type ProductConversation struct {
	State, Note string
	Turns       []ProductTurn
}

// Tone is the state badge's tone.
func (c ProductConversation) Tone() string {
	switch c.State {
	case "Complete":
		return "ok"
	case "Partial", "Truncated":
		return "wait"
	}
	return "muted"
}

// Rendering bounds for one conversation: the page must stay inside the
// response bound with the rest of the Sessions list.
// maxConversationBytes caps the turn text kept in total; escaping can grow
// text about fivefold, so even a worst case leaves room for the page frame.
const (
	maxTurns             = 60
	maxTurnBytes         = 4 << 10
	maxConversationBytes = 96 << 10
)

// boundConversation keeps a conversation within the rendering bounds, newest
// turns first kept, and marks what it cut. An unknown state is Unavailable.
func boundConversation(c ProductConversation) *ProductConversation {
	switch c.State {
	case "Complete", "Partial", "Truncated":
	default:
		return &ProductConversation{State: "Unavailable", Note: c.Note}
	}
	out := ProductConversation{State: c.State, Note: c.Note}
	turns := c.Turns
	if len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
		out.State = "Truncated"
	}
	for _, turn := range turns {
		turn.Text = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return r
			}
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return -1
			}
			return r
		}, strings.ToValidUTF8(turn.Text, "\uFFFD"))
		if len(turn.Text) > maxTurnBytes {
			cut := maxTurnBytes
			for cut > 0 && !utf8.RuneStart(turn.Text[cut]) {
				cut--
			}
			turn.Text, turn.Truncated = turn.Text[:cut], true
		}
		if turn.Truncated && out.State == "Complete" {
			out.State = "Truncated"
		}
		turn.From, turn.At = display(turn.From), display(turn.At)
		out.Turns = append(out.Turns, turn)
	}
	// Keep the newest turns within the total budget.
	total := 0
	for i := len(out.Turns) - 1; i >= 0; i-- {
		total += len(out.Turns[i].Text) + len(out.Turns[i].From) + len(out.Turns[i].At)
		if total > maxConversationBytes {
			out.Turns, out.State = out.Turns[i+1:], "Truncated"
			break
		}
	}
	return &out
}

// ProductSend makes a Draftable message box a form posting to the portal's
// same-origin send route. CSRF is the caller's portal session value. RequestID
// binds this rendered draft to one receipt, so a resubmitted form cannot send
// twice. A ProductSend that fails validSend is not rendered.
type ProductSend struct {
	Action, CSRF, RequestID string
}

// ProductSessionMessagesHref is the portal send route a form may post to, and
// the prefix of its receipts.
const ProductSessionMessagesHref = "/portal/projects/dev-env/environments/internal/session-messages"

// sendUnavailable replaces the gate of a box whose form failed validation.
const sendUnavailable = "Sending from this page is unavailable. Nothing is sent or queued."

var (
	sendCSRF      = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	sendRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
)

// validSend accepts a form only for a Draftable box with a target, the fixed
// same-origin action, a portal CSRF value and a request ID.
func validSend(m *ProductMessages) bool {
	return m != nil && m.Send != nil && m.Draftable && m.Session != "" && m.SessionUID != "" && validText(m.Session) && validText(m.SessionUID) &&
		m.Send.Action == ProductSessionMessagesHref && sendCSRF.MatchString(m.Send.CSRF) && sendRequestID.MatchString(m.Send.RequestID)
}

// ProductLaunch is the Sessions page's launch form: a POST to the portal's
// same-origin launch route. CSRF is the caller's portal session value and
// RequestID binds this rendered form to one launch, so a resubmitted form cannot
// start two sessions. Repos are the repositories the caller's private scope
// allows; Models and Efforts are what the form offers. A ProductLaunch that
// fails validLaunch is dropped.
type ProductLaunch struct {
	Action, CSRF, RequestID string
	Repos, Models, Efforts  []string
}

// ProductSessionLaunchesHref is the portal launch route a form may post to.
const ProductSessionLaunchesHref = "/portal/projects/dev-env/environments/internal/session-launches"

// validLaunch accepts a form only with the fixed same-origin action, a portal
// CSRF value and request ID, at least one repository name, and only full
// Claude model ids and known effort levels.
func validLaunch(l *ProductLaunch) bool {
	if l == nil || l.Action != ProductSessionLaunchesHref || !sendCSRF.MatchString(l.CSRF) || !sendRequestID.MatchString(l.RequestID) ||
		len(l.Repos) == 0 || len(l.Repos) > apiv1.MaxPortalLaunchRepos || len(l.Models) == 0 || len(l.Models) > 8 || len(l.Efforts) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, r := range l.Repos {
		if !apiv1.ValidRepoName(r) || seen[r] {
			return false
		}
		seen[r] = true
	}
	efforts := apiv1.PortalLaunchEfforts()
	for _, m := range l.Models {
		if apiv1.CheckClaudeModel(m) != apiv1.ClaudeModelOK {
			return false
		}
	}
	for _, e := range l.Efforts {
		if !slices.Contains(efforts, e) {
			return false
		}
	}
	return true
}

// pageLaunch is the page's launch form when it is a valid one on the Sessions
// page, and nil otherwise.
func pageLaunch(page ProductPage) *ProductLaunch {
	if page.Filter.View == "usage" || strings.ToLower(page.Title) != "sessions" || !validLaunch(page.Launch) {
		return nil
	}
	return page.Launch
}

// pageSend reports whether page holds a valid send form.
func pageSend(page ProductPage) bool {
	if page.Connected && page.ReadFailure == nil && page.Filter.View != "usage" {
		for _, g := range page.Groups {
			for _, row := range g.Rows {
				if productID.MatchString(row.ID) && validSend(row.Messages) {
					return true
				}
			}
		}
	}
	return false
}

// ProductPolicyFor is the Content-Security-Policy for page: ProductSendPolicy
// when it holds a valid send form or a valid launch form, ProductPolicy
// otherwise. The response header and the page's own meta policy both come from it.
func ProductPolicyFor(page ProductPage) string {
	if pageSend(page) || pageLaunch(page) != nil {
		return ProductSendPolicy
	}
	return ProductPolicy
}

// ProductGroup is one titled list group. Attention marks the group the rail's
// Needs attention link opens.
// Total, when larger than len(Rows), is how many the group holds before the
// list was bounded; the count then reads "listed of total".
type ProductGroup struct {
	Title     string
	Attention bool
	Rows      []ProductRow
	Total     int
}

// ProductUsageCell is one table cell; Unknown dims it.
type ProductUsageCell struct {
	Label, Value string
	Unknown      bool
}

// ProductUsageRow is one session's usage row, linked by SessionUID. ID is only
// the position used to keep its list and usage rows within the rendering bound.
type ProductUsageRow struct {
	ID, SessionUID, Name, Sub string
	Cells                     []ProductUsageCell
}

// SessionHref selects the session by its stable UID, regardless of list order.
func (r ProductUsageRow) SessionHref() string {
	if !productSessionUID.MatchString(r.SessionUID) {
		return ""
	}
	return productSessionsHref + "?session=" + url.QueryEscape(r.SessionUID)
}

// ProductUsageTable is one titled breakdown table. Group rows have no
// SessionUID and are not linked.
type ProductUsageTable struct {
	Title, Note string
	Columns     []string
	Rows        []ProductUsageRow
}

// ProductUsage is the Sessions page's Usage view: headline counts, account
// allowance kept apart from token observations, breakdowns that sum reported
// values only, the per-session table and what the numbers cover. RowsTitle
// names the per-session table; empty reads "By session".
type ProductUsage struct {
	Stats            []ProductStat
	Scope, ScopeNote string
	Allowance        []ProductField
	Breakdowns       []ProductUsageTable
	Columns          []string
	Rows             []ProductUsageRow
	RowsTitle        string
	RowsNote         string
	Coverage         []ProductField
}

// ProductFilter narrows a product list: to its Needs attention group, or, on
// Sessions, to one project (a repository). The zero value lists everything.
type ProductFilter struct {
	// View selects the standalone Usage view; empty selects the session browser.
	View      string
	Attention bool
	Project   string
	// Session selects one session by UID to load its conversation. It does
	// not narrow the list.
	Session string
}

// ProductCount is one rail entry's name and count. Count is empty when the
// read was incomplete.
type ProductCount struct{ Name, Count string }

// ProductRail is the left rail, counted over the whole read before the
// filter: everything, Needs attention, and on Sessions each project.
type ProductRail struct {
	All, Attention string
	Projects       []ProductCount
}

// ProductReadFailure is a failed Sessions read. ReasonCode is one of
// not_connected, credential_unavailable, operator_unreachable, operator_refused,
// response_contract, clock_ahead, read_not_started or cancelled. Only fixed wording is rendered.
// Page is one-based (0 outside an inventory-page read); RowIndex is zero-based (-1 for no row).
// Times describe reads in this process, not cached session data.
type ProductReadFailure struct {
	ReasonCode                 string
	Page, RowIndex, HTTPStatus int
	TimedOut                   bool
	ObservedAt, LastSuccessAt  string
}

type productReadFailureView struct {
	Reason, RetryHref, ObservedAt, ObservedLabel, LastSuccessAt, LastSuccessLabel string
}

func productReadFailure(f *ProductReadFailure, filter ProductFilter) *productReadFailureView {
	if f == nil {
		return nil
	}
	v := &productReadFailureView{ObservedAt: f.ObservedAt, LastSuccessAt: f.LastSuccessAt, RetryHref: productSessionsHref}
	switch f.ReasonCode {
	case "not_connected":
		v.Reason = "The Sessions source is not connected."
	case "credential_unavailable":
		v.Reason = "The operator credential is unavailable."
	case "read_not_started":
		v.Reason = "The Sessions read could not start within its time budget. Try again."
	case "operator_unreachable":
		v.Reason = "The operator could not be reached."
		if f.TimedOut {
			v.Reason = "The operator read timed out. Try again."
		}
	case "operator_refused":
		v.Reason = "The operator refused the read."
		if f.HTTPStatus >= 100 && f.HTTPStatus <= 599 {
			v.Reason = fmt.Sprintf("The operator refused the read (HTTP %d).", f.HTTPStatus)
		}
	case "response_contract":
		v.Reason = "The returned session data could not be read."
	case "clock_ahead":
		v.Reason = "An operator timestamp is more than 2 seconds ahead of the portal clock."
	case "cancelled":
		v.Reason = "The Sessions read was cancelled."
	default:
		v.Reason = "The read failed; its reason is Unknown."
	}
	if f.Page > 0 && f.Page <= 10 {
		v.Reason += fmt.Sprintf(" Page %d", f.Page)
		if f.RowIndex >= 0 && f.RowIndex < 50 {
			v.Reason += fmt.Sprintf(", row index %d (counting from 0)", f.RowIndex)
		}
		v.Reason += "."
	}
	if filter.View == "usage" {
		v.RetryHref += "?view=usage"
	} else if filter.Attention {
		v.RetryHref += "?show=attention"
	} else if filter.Project != "" {
		v.RetryHref += "?project=" + url.QueryEscape(filter.Project)
	} else if filter.Session != "" {
		v.RetryHref += "?session=" + url.QueryEscape(filter.Session)
	}
	label := func(raw string) (string, string) {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return "", "Unknown"
		}
		return at.UTC().Format(time.RFC3339Nano), at.UTC().Format("2 Jan 2006, 15:04:05 UTC")
	}
	v.ObservedAt, v.ObservedLabel = label(f.ObservedAt)
	v.LastSuccessAt, v.LastSuccessLabel = label(f.LastSuccessAt)
	return v
}

// ProductPage is one product view. Groups and Usage render only when
// Connected, and counts only when Complete: an incomplete read has no count.
// Quiet marks an ordinary notice; Preview labels fixture data.
type ProductPage struct {
	Title, Notice, ObservedAt           string
	ListTitle, ListCount                string
	Connected, Complete, Quiet, Preview bool
	Groups                              []ProductGroup
	Usage                               *ProductUsage
	Rail                                ProductRail
	Filter                              ProductFilter
	// ReadCompletedAt is the completion time of this page's successful Sessions
	// read. An empty value is unknown; ObservedAt is only the render time.
	ReadCompletedAt string
	// Launch is the Sessions page's launch form, set only when the route is
	// configured for this caller.
	Launch      *ProductLaunch
	ReadFailure *ProductReadFailure
}

const (
	productSessionsHref = "/portal/projects/dev-env/environments/internal/sessions"
	productTasksHref    = "/portal/projects/dev-env/environments/internal/tasks"
)

var (
	productTones            = map[string]bool{"ok": true, "busy": true, "wait": true, "bad": true, "info": true, "muted": true}
	productRemoteControlURL = regexp.MustCompile(apiv1.PortalRemoteControlURLPattern)
	productSessionUID       = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	productID               = regexp.MustCompile(`^[st][0-9]{1,4}$`)
)

func productTone(tone string) string {
	if productTones[tone] {
		return tone
	}
	return "muted"
}

// sectionTones returns sections with every state badge's tone in the fixed
// set. It copies what it changes, so the caller's page is left as it was.
func sectionTones(sections []ProductSection) []ProductSection {
	if sections == nil {
		return nil
	}
	out := make([]ProductSection, len(sections))
	for i, s := range sections {
		if len(s.States) > 0 {
			states := make([]ProductState, len(s.States))
			for j, st := range s.States {
				st.Status.Tone = productTone(st.Status.Tone)
				states[j] = st
			}
			s.States = states
		}
		out[i] = s
	}
	return out
}

type productGroupView struct {
	ID, Title, Count string
	Rows             []ProductRow
}

type productRowView struct {
	ProductRow
	Default bool
}

// RenderProductWithin renders page in at most limit bytes. While it is too
// large it keeps fewer list rows, dropping from the end of the list (the
// least urgent group first), together with their usage rows; group counts and
// the Usage breakdowns still cover everything read. The selected session (the
// row with a conversation) is always kept.
func RenderProductWithin(page ProductPage, limit int) ([]byte, error) {
	body, _, err := RenderProductPolicyWithin(page, limit)
	return body, err
}

// RenderProductPolicyWithin is RenderProductWithin that also returns the
// rendered page's policy, for the response header to match its meta policy.
func RenderProductPolicyWithin(page ProductPage, limit int) ([]byte, string, error) {
	rendered := page
	body, err := RenderProduct(rendered)
	for keep := countRows(page); err == nil && len(body) > limit && keep > 0; {
		keep = keep * 3 / 4
		rendered = keepRows(page, keep)
		body, err = RenderProduct(rendered)
	}
	if err == nil && len(body) > limit {
		return nil, "", fmt.Errorf("product page needs %d bytes, over %d", len(body), limit)
	}
	if err != nil {
		return nil, "", err
	}
	return body, ProductPolicyFor(rendered), nil
}

func countRows(page ProductPage) int {
	n := 0
	for _, g := range page.Groups {
		n += len(g.Rows)
	}
	return n
}

// keepRows returns page with only its first keep list rows, in group order.
func keepRows(page ProductPage, keep int) ProductPage {
	total := countRows(page)
	kept := map[string]bool{}
	groups := make([]ProductGroup, len(page.Groups))
	for i, g := range page.Groups {
		if g.Total < len(g.Rows) {
			g.Total = len(g.Rows)
		}
		var rows []ProductRow
		for _, r := range g.Rows {
			selected := r.Selected
			if keep > 0 || selected {
				rows = append(rows, r)
				kept[r.ID] = true
				if !selected {
					keep--
				}
			}
		}
		g.Rows = rows
		groups[i] = g
	}
	page.Groups = groups
	listed := len(kept)
	page.ListCount = "Showing " + strconv.Itoa(listed) + " of " + strconv.Itoa(total) + " listed · narrow with a filter"
	if page.Usage != nil {
		usage := *page.Usage
		usage.Rows = nil
		for _, r := range page.Usage.Rows {
			if kept[r.ID] {
				usage.Rows = append(usage.Rows, r)
			}
		}
		usage.RowsNote = "Only the sessions listed on the left. The breakdowns above cover all of them."
		page.Usage = &usage
	}
	return page
}

type railLinkView struct {
	Name, Href, Count string
	Current           bool
}

func RenderProduct(page ProductPage) ([]byte, error) {
	if page.ReadFailure != nil {
		page.Connected, page.Complete = false, false
	}
	view := struct {
		Policy, Title, Current, Section, Notice, ObservedAt, ObservedLabel string
		FooterUpdatedAt, FooterUpdatedLabel                                string
		SessionsHref                                                       string
		ListTitle, ListCount, Access, AccessLower, DefaultID               string
		Quiet, Preview, Banner, HasRows, CanSend, Selected, UsageView      bool
		All, Attention                                                     railLinkView
		Projects                                                           []railLinkView
		Groups                                                             []productGroupView
		Rows                                                               []productRowView
		Usage                                                              *ProductUsage
		Launch                                                             *launchView
		ReadFailure                                                        *productReadFailureView
		CanLaunch                                                          bool
	}{Policy: strings.TrimSuffix(ProductPolicyFor(page), "; frame-ancestors 'none'"), Title: page.Title, Current: strings.ToLower(page.Title), Notice: page.Notice,
		ObservedAt: page.ObservedAt, ObservedLabel: observedLabel(page.ObservedAt), Preview: page.Preview,
		SessionsHref: productSessionsHref,
		ListTitle:    page.ListTitle}
	view.FooterUpdatedAt, view.FooterUpdatedLabel = view.ObservedAt, view.ObservedLabel
	if view.Current == "sessions" {
		view.FooterUpdatedAt, view.FooterUpdatedLabel = "", ""
		if page.Connected && page.ReadFailure == nil {
			if at, err := time.Parse(time.RFC3339Nano, page.ReadCompletedAt); err == nil && !at.IsZero() {
				view.FooterUpdatedAt = at.UTC().Format(time.RFC3339Nano)
				view.FooterUpdatedLabel = observedLabel(view.FooterUpdatedAt)
			}
		}
	}
	view.ReadFailure = productReadFailure(page.ReadFailure, page.Filter)
	view.UsageView = page.Filter.View == "usage" && view.Current == "sessions"
	if view.UsageView {
		view.Title, view.Current = "Usage", "usage"
	}
	// A page that holds a send or launch form is not read-only, and says so.
	view.CanSend = pageSend(page)
	view.Access = "Read-only"
	if launch := pageLaunch(page); launch != nil {
		view.CanLaunch, view.Launch = true, newLaunchView(launch)
		view.Access = "Read and launch"
	}
	if view.CanSend {
		view.Access = "Read and message"
		if view.CanLaunch {
			view.Access = "Read, message and launch"
		}
	}
	view.AccessLower = strings.ToLower(view.Access)
	base := productSessionsHref
	view.Section, view.All.Name = "Agents", "All sessions"
	if view.Current == "tasks" {
		view.Section, base, view.All.Name = "Tasks", productTasksHref, "All tasks"
	}
	if view.ListTitle == "" {
		view.ListTitle = page.Title
	}
	// The rail filters the list by query; its counts cover the whole read.
	view.All.Href, view.All.Current = base, !page.Filter.Attention && page.Filter.Project == ""
	view.Attention = railLinkView{Name: "Needs attention", Href: base + "?show=attention", Current: page.Filter.Attention}
	if page.Connected {
		view.Quiet, view.ListCount, view.Usage = page.Quiet, page.ListCount, page.Usage
		if view.UsageView && view.Usage != nil {
			usage := *view.Usage
			usage.RowsNote = strings.ReplaceAll(usage.RowsNote, "listed on the left", "shown below")
			view.Usage = &usage
		}
		if page.Complete {
			view.All.Count = page.Rail.All
			// An alert-coloured zero says nothing; the count shows only when something waits.
			if page.Rail.Attention != "0" {
				view.Attention.Count = page.Rail.Attention
			}
		}
		if view.Current == "sessions" {
			for _, p := range page.Rail.Projects {
				link := railLinkView{Name: p.Name, Href: productSessionsHref + "?project=" + url.QueryEscape(p.Name), Current: page.Filter.Project == p.Name}
				if page.Complete {
					link.Count = p.Count
				}
				view.Projects = append(view.Projects, link)
			}
		}
		for i, group := range page.Groups {
			g := productGroupView{ID: "g" + strconv.Itoa(i+1), Title: group.Title}
			for _, row := range group.Rows {
				if !productID.MatchString(row.ID) {
					continue
				}
				row.Status.Tone = productTone(row.Status.Tone)
				row.Sections = sectionTones(row.Sections)
				// A header link leaves the portal: https only, or none.
				if !strings.HasPrefix(row.Link, "https://") {
					row.Link, row.LinkLabel = "", ""
				}
				if row.Attention != nil {
					attention := *row.Attention
					// The primary action must be the same registered Claude link as the header.
					if attention.Link != row.Link || !productRemoteControlURL.MatchString(attention.Link) {
						attention.Link = ""
					}
					row.Attention = &attention
					if attention.Link != "" {
						row.Link, row.LinkLabel = "", ""
					}
				}
				if row.Messages != nil {
					m := *row.Messages
					m.Route.Tone = productTone(m.Route.Tone)
					if m.Send != nil && !validSend(&m) {
						m.Send, m.Gate = nil, sendUnavailable
					}
					if !strings.HasPrefix(m.HistoryHref, productSessionsHref+"?session=") {
						m.HistoryHref = ""
					}
					if row.Attention != nil && m.HistoryHref == "" && m.Conversation == nil {
						m.History = "The portal cannot show this session's conversation."
					}
					if m.Conversation != nil {
						m.Conversation = boundConversation(*m.Conversation)
					}
					row.Messages = &m
				}
				if row.Selected && !view.Selected {
					view.Selected, view.DefaultID = true, row.ID
				}
				g.Rows = append(g.Rows, row)
				view.Rows = append(view.Rows, productRowView{ProductRow: row})
			}
			if len(g.Rows) == 0 {
				continue
			}
			if page.Complete {
				g.Count = strconv.Itoa(len(g.Rows))
				if group.Total > len(g.Rows) {
					g.Count += " of " + strconv.Itoa(group.Total)
				}
			}
			view.Groups = append(view.Groups, g)
		}
	}
	view.HasRows = len(view.Rows) > 0
	// The selected row, or else the first, is the detail a URL without a
	// fragment opens. A selected row opens on a phone too, not only the list.
	if view.HasRows && view.DefaultID == "" {
		view.DefaultID = view.Rows[0].ID
	}
	for i := range view.Rows {
		view.Rows[i].Default = view.Rows[i].ID == view.DefaultID
	}
	view.Banner = view.HasRows && !view.Quiet && view.Notice != ""
	var body bytes.Buffer
	if err := viewsTemplate.Execute(&body, view); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// observedLabel is the page time for people; the datetime attribute keeps RFC 3339.
func observedLabel(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "Unknown"
	}
	return t.UTC().Format("2 Jan 2006, 15:04 UTC")
}

// Ago is a short relative time; a zero or future time is Unknown.
func Ago(t, now time.Time) string {
	if t.IsZero() || now.IsZero() || t.After(now) {
		return "Unknown"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

type pageView struct {
	Policy, ObservedAt, Notice, Scope string
	Groups                            []groupView
	// Facts are source rows whose Attention group is not supplied. They are not a fifth group.
	Facts []rowView
}

func attentionFields(item Item, now time.Time) (primary, secondary []field, summary string) {
	primary = []field{
		{"Next action", display(item.NextAction), "next"},
		{"Owner", display(item.Owner), ""},
		{"Next actor", display(item.NextActor), ""},
		{"Canonical source", "OpenProject #" + strconv.Itoa(item.WorkPackage), ""},
		{"Source age", age(item.UpdatedAt, now), ""},
		{"Source updated", evidenceMoment(item.UpdatedAt, now), ""},
		{"Last verified", evidenceMoment(item.VerifiedAt, now), ""},
	}
	outcome := field{"Task outcome", display(item.TaskOutcome), ""}
	liveness := field{"Worker liveness", display(item.WorkerState), ""}
	review := field{"Review", display(item.ReviewState), ""}
	head := field{"Reviewed head", display(item.ReviewHead), ""}
	authority := field{"Authority", display(item.AuthorityState), ""}
	release := field{"Release", display(item.ReleaseState), ""}
	switch item.Group {
	case ReviewReturned:
		// Review and the reviewed head stay visible for this group.
		primary = append(primary, review, head)
		secondary = []field{outcome, liveness, authority, release}
		summary = "Separate outcome, worker, authority and release"
	case HandoffMissing:
		// Worker exit and task outcome stay visible and separate for a missing handoff.
		primary = append(primary, outcome, liveness)
		secondary = []field{review, head, authority, release}
		summary = "Separate review, authority and release"
	default:
		secondary = []field{outcome, liveness, review, head, authority, release}
		summary = "Separate outcome, worker, review, authority and release"
	}
	return primary, secondary, summary
}

func makePage(s Snapshot, now time.Time, freshFor time.Duration) pageView {
	// frame-ancestors is enforced by the HTTP header; browsers ignore it in meta.
	p := pageView{Policy: strings.TrimSuffix(contentPolicy, "; frame-ancestors 'none'"), ObservedAt: moment(now)}
	if s.State == Unknown {
		p.Notice = "Attention unknown. The read did not verify current task state."
		for _, item := range s.Items {
			primary, secondary, summary := attentionFields(item, now)
			p.Facts = append(p.Facts, rowView{
				ID: item.Resource.Task, Title: display(item.Title),
				Freshness: freshness(item.UpdatedAt, item.VerifiedAt, now, freshFor),
				Link:      "https://work.haynesnetwork.com/work_packages/" + strconv.Itoa(item.WorkPackage),
				Summary:   summary, Primary: primary, Secondary: secondary,
			})
		}
		return p
	}
	taskScoped := s.Resource.Task != ""
	if taskScoped {
		p.Scope = "One task. Other Attention groups are outside this page and are not verified empty."
	}
	listingFreshness := freshness(s.UpdatedAt, s.VerifiedAt, now, freshFor)
	listingVerified := listingFreshness == "Verified within snapshot window"
	if !listingVerified {
		p.Notice = listingFreshness + ". Listing counts are not verified as current; current Attention contents are Unknown. Last verified: " + evidenceMoment(s.VerifiedAt, now)
	}
	if len(s.Items) == 0 && listingVerified {
		p.Notice = "Verified empty: no Attention items in this authorized snapshot. Last verified: " + evidenceMoment(s.VerifiedAt, now)
	}
	for _, group := range []struct {
		id    Group
		title string
	}{{NeedsYou, "Needs you"}, {NeedsWorker, "Needs worker"}, {ReviewReturned, "Review returned"}, {HandoffMissing, "Handoff missing"}} {
		g := groupView{ID: string(group.id), Title: group.title}
		for _, item := range s.Items {
			if item.Group != group.id {
				continue
			}
			primary, secondary, summary := attentionFields(item, now)
			g.Rows = append(g.Rows, rowView{
				ID: item.Resource.Task, Title: display(item.Title),
				Freshness: freshness(item.UpdatedAt, item.VerifiedAt, now, freshFor),
				Link:      "https://work.haynesnetwork.com/work_packages/" + strconv.Itoa(item.WorkPackage),
				Summary:   summary, Primary: primary, Secondary: secondary,
			})
		}
		g.Count = "Unknown"
		if listingVerified {
			g.Count = strconv.Itoa(len(g.Rows))
		}
		if taskScoped && len(g.Rows) == 0 {
			continue
		}
		p.Groups = append(p.Groups, g)
	}
	return p
}

func evidenceMoment(t, now time.Time) string {
	if t.After(now) {
		return "Unknown (untrusted future timestamp: " + moment(t) + ")"
	}
	return moment(t)
}

func moment(t time.Time) string {
	if t.IsZero() || t.Year() < 1 || t.Year() > 9999 {
		return "Unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

func age(t, now time.Time) string {
	if t.IsZero() || moment(t) == "Unknown" {
		return "Unknown"
	}
	if t.After(now) {
		return "Unknown (future timestamp)"
	}
	d := now.Sub(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func freshness(updated, verified, now time.Time, freshFor time.Duration) string {
	if moment(updated) == "Unknown" || moment(verified) == "Unknown" || now.IsZero() || updated.After(now) || verified.After(now) {
		return "Freshness unknown"
	}
	if verified.Before(updated) {
		return "Unknown: verification predates source update"
	}
	if now.Sub(verified) > freshFor {
		return "Stale evidence"
	}
	return "Verified within snapshot window"
}

// ProductReceipt is one portal send's receipt as the send route recorded it.
// TransportStatus is Delivered, Refused, Pending or Unknown; any other value
// renders Unknown. Delivered is agentd's transport acceptance for the agent's
// terminal: never a reply, a read or a finished task. ReasonCode is the
// route's fixed code; the page words it, and never shows route text. Preview
// labels fixture data.
type ProductReceipt struct {
	RequestID, Session, SessionUID, ResolvedPodUID string
	TransportStatus, ReasonCode                    string
	ObservedAt                                     time.Time
	Preview                                        bool
}

// receiptReasons words the send route's fixed reason codes.
var receiptReasons = map[string]string{
	"delivered": "agentd accepted the text.",
	"refused":   "The session took no message: its agent was not running, it is a headless task, or delivery was refused.",
	"unknown":   "The delivery call ended without a confirmed outcome.",
}

var receiptSessionUID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

type receiptState struct{ Status, Headline, Tone, Meaning string }

var receiptStates = map[string]receiptState{
	"Delivered": {"Delivered", "Delivered to the agent's terminal", "ok", "agentd accepted this text for the agent's terminal. That is transport acceptance only: it does not mean the agent has read it, answered it or acted on it."},
	"Refused":   {"Refused", "Not delivered", "bad", "The send route refused this message. Nothing was delivered."},
	"Pending":   {"Pending", "Not confirmed yet", "wait", "The outcome is not recorded yet. Check this receipt again; do not send the text again."},
	"Unknown":   {"Unknown", "Outcome Unknown", "wait", "The portal cannot tell whether the agent's terminal received this text. Check the session before sending it again."},
}

// RenderReceipt renders one receipt page. Its policy is ProductPolicy: no
// script and no form. Every value is escaped text; a missing one reads Unknown.
func RenderReceipt(r ProductReceipt) ([]byte, error) {
	state, ok := receiptStates[r.TransportStatus]
	if !ok {
		state = receiptStates["Unknown"]
	}
	value := func(s string) string {
		if !validText(s) {
			return "Unknown"
		}
		return display(s)
	}
	observed := "Unknown"
	if !r.ObservedAt.IsZero() {
		observed = r.ObservedAt.UTC().Format("2 Jan 2006, 15:04:05 UTC")
	}
	view := struct {
		receiptState
		Policy, SessionsHref, Self, Back, Observed, Reason string
		Fields                                             []ProductField
		Preview                                            bool
	}{receiptState: state, Policy: strings.TrimSuffix(ProductPolicy, "; frame-ancestors 'none'"), SessionsHref: productSessionsHref, Observed: observed, Preview: r.Preview}
	if sendRequestID.MatchString(r.RequestID) {
		view.Self = ProductSessionMessagesHref + "/" + r.RequestID
	}
	view.Back = productSessionsHref
	if receiptSessionUID.MatchString(r.SessionUID) {
		view.Back += "?session=" + r.SessionUID
	}
	if r.ReasonCode != "" {
		view.Reason = receiptReasons[r.ReasonCode]
		if view.Reason == "" {
			view.Reason = "Reason not recorded."
		}
	}
	view.Fields = []ProductField{
		{Label: "Request ID", Value: value(r.RequestID), Mono: true},
		{Label: "Session", Value: value(r.Session), Mono: true},
		{Label: "Session UID", Value: value(r.SessionUID), Mono: true},
		{Label: "Pod UID at send", Value: value(r.ResolvedPodUID), Mono: true},
		{Label: "Observed", Value: observed},
	}
	var body bytes.Buffer
	if err := receiptTemplate.Execute(&body, view); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}

// launchModeOptions are the launch form's modes and their plain names.
var launchModeOptions = []struct{ Value, Label string }{
	{"task", "Headless task"},
	{"remote", "Interactive with Remote Control"},
	{"local", "Local terminal"},
}

type launchView struct {
	Action, CSRF, RequestID string
	Repos, Models           []string
	Efforts                 []string
	Modes                   []struct{ Value, Label string }
	Sizes                   []string
}

func newLaunchView(l *ProductLaunch) *launchView {
	return &launchView{Action: l.Action, CSRF: l.CSRF, RequestID: l.RequestID, Repos: l.Repos, Models: l.Models, Efforts: l.Efforts, Modes: launchModeOptions, Sizes: []string{"S", "M"}}
}

// ProductLaunchResult is a launch the portal did not turn into a redirect: the
// operator refused it, was unavailable, or the outcome is not known. Outcome is
// limit, unavailable, invalid, conflict or unknown; any other value reads unknown.
type ProductLaunchResult struct {
	Outcome string
	Fields  []ProductField
	Back    string
	Preview bool
}

type launchResultState struct{ Status, Headline, Tone, Meaning string }

// launchResultStates is the fixed wording for each outcome. None echoes a
// prompt or an operator error.
var launchResultStates = map[string]launchResultState{
	"limit":       {"Not started", "Too many launches are running", "wait", "The operator allows only a few unfinished portal-launched sessions at once. Nothing was started. Try again when one has finished or been suspended."},
	"unavailable": {"Not started", "Launching is unavailable", "bad", "The operator did not accept this launch. Nothing was started."},
	"invalid":     {"Not started", "The launch was not valid", "bad", "One or more fields were not accepted. Nothing was started. Fix them and start the agent again."},
	"conflict":    {"Not started", "This form already started a session", "wait", "This request already started a session with different details. Nothing new was started. Find that session in the Agents list, or load a fresh form to start another."},
	"unknown":     {"Unknown", "Outcome Unknown", "wait", "The portal cannot tell whether a session was started. Check the Agents list before starting another. Sending this same form again returns the same session while it is unfinished; a freshly loaded form starts a new one."},
}

// RenderLaunchResult renders a launch result page. Its policy is ProductPolicy:
// no script and no form. Every value is escaped text.
func RenderLaunchResult(r ProductLaunchResult) ([]byte, error) {
	state, ok := launchResultStates[r.Outcome]
	if !ok {
		state = launchResultStates["unknown"]
	}
	back := productSessionsHref
	if r.Back == productSessionsHref+"#launch" {
		back = r.Back
	}
	view := struct {
		launchResultState
		Policy, SessionsHref, Back string
		Fields                     []ProductField
		Preview                    bool
	}{launchResultState: state, Policy: strings.TrimSuffix(ProductPolicy, "; frame-ancestors 'none'"), SessionsHref: productSessionsHref, Back: back, Preview: r.Preview}
	for i, f := range r.Fields {
		if i == 8 {
			break
		}
		if validText(f.Label) && validText(f.Value) {
			view.Fields = append(view.Fields, ProductField{Label: display(f.Label), Value: display(f.Value), Mono: f.Mono})
		}
	}
	var body bytes.Buffer
	if err := launchResultTemplate.Execute(&body, view); err != nil {
		return nil, err
	}
	return body.Bytes(), nil
}
