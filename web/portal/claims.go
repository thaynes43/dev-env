package portal

import (
	_ "embed"
	"html/template"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// The Claims page (DESIGN-045) is read-only: no form, no button that acts and
// no POST route. A claim is not a grant, an approval or permission to act.

//go:embed claims.html
var claimsSource string

// claimsHref is session.ClaimsPath; a session test pins the two together.
const claimsHref = "/portal/projects/dev-env/environments/internal/claims"

const claimsStyle = `
.claims-content{flex:1;min-height:0;overflow:auto;padding:24px;overflow-wrap:anywhere}
.claims-content:focus-visible{outline:3px solid var(--focus);outline-offset:-3px}
.claims-content>section,.claims-heading{max-width:72rem;margin:0 auto 24px}
.claims-heading h1{font-size:24px;letter-spacing:-.5px;margin:4px 0}
.claims-heading>p:not(.eyebrow){color:var(--muted);max-width:48rem}
.claims-heading>p+p{margin-top:6px}
.claims-notice,.claims-list{padding:22px;border:1px solid var(--line);border-radius:10px;background:var(--surface)}
.claims-notice h2,.claims-summary h2{font-size:15px}
.claims-notice h2+p{margin-top:8px}
.claims-notice p+p{margin-top:12px}
.claims-notice p,.claims-summary p{color:var(--muted)}
.claims-summary{display:grid;gap:4px;margin-bottom:16px}
.claims-summary .claims-counts{color:var(--ink)}
.claims-summary .note{margin-top:4px}
.claims-table th,.claims-table td{text-align:left}
.claims-table td{white-space:normal;overflow-wrap:anywhere;min-width:0}
.claims-table td>div{display:grid;gap:3px;justify-items:start;min-width:0}
.claims-table td a{overflow-wrap:anywhere}
.claims-table .badge{white-space:normal;text-align:left}
.claims-table .mono{overflow-wrap:anywhere}
.claims-none{color:var(--muted)}
.claims-pager{display:flex;flex-wrap:wrap;gap:12px;margin-top:16px}
@media(max-width:700px){.claims-content{overflow:visible;padding:20px 16px 96px}.claims-notice,.claims-list{padding:16px}.claims-table tr{padding:12px 0}.claims-table td{text-align:right}.claims-table td>div{justify-items:end;max-width:70%}.claims-table td:first-child{text-align:left}.claims-table td:first-child>div{justify-items:start;max-width:none}.claims-pager .btn{min-height:44px}}
`

// ClaimsPolicy is the Claims page's Content-Security-Policy: no script, no
// form, no external resource, and only the embedded stylesheet.
var ClaimsPolicy = policy(viewsStyle + claimsStyle)

var claimsTemplate = template.Must(template.New("claims").Parse(strings.Replace(claimsSource, "/*PORTAL_STYLE*/", viewsStyle+claimsStyle, 1)))

// The shared pages' Claims-nav variants. Each is its own template set, used
// only when the caller says a Claims source is configured, so an unconfigured
// portal never links a page it does not serve.
var (
	viewsClaimsTemplate = template.Must(template.New("views").Parse(strings.Replace(viewsSource, "/*PORTAL_STYLE*/", viewsStyle, 1)))
	asksClaimsTemplate  = parseAsksTemplate()
	pagesClaimsTemplate = parsePagesTemplate()
)

func init() {
	for _, target := range []*template.Template{claimsTemplate, viewsClaimsTemplate, asksClaimsTemplate, pagesClaimsTemplate} {
		withClaimsNav(target)
	}
}

// withClaimsNav gives one template set its own copies of the icons and of the
// navigation that links Claims, under the shared names.
func withClaimsNav(target *template.Template) {
	for _, pair := range [][2]string{{"product-nav", "product-nav-claims"}, {"product-mobile-nav", "product-mobile-nav-claims"}, {"product-icons", "product-icons"}} {
		// html/template rewrites trees during escaping; each set owns its copy.
		template.Must(target.AddParseTree(pair[0], viewsTemplate.Lookup(pair[1]).Tree.Copy()))
	}
}

// ClaimsState names what the Claims page shows. Only ClaimsEmpty and
// ClaimsAvailable carry a read; every other state shows no claims and says
// that it is not an empty list.
type ClaimsState string

const (
	ClaimsNotConnected ClaimsState = "not-connected"
	ClaimsRefused      ClaimsState = "refused"
	ClaimsNotServed    ClaimsState = "not-served"
	ClaimsUnavailable  ClaimsState = "unavailable"
	ClaimsUnconfigured ClaimsState = "unconfigured"
	ClaimsOutOfScope   ClaimsState = "out-of-scope"
	ClaimsEmpty        ClaimsState = "empty"
	ClaimsAvailable    ClaimsState = "available"
)

// ClaimsPage is one authorized Claims read, or the named state that replaced
// it. Cursor reports that the read carried a cursor: such a page is never
// Empty. Project is the portal's configured project.
type ClaimsPage struct {
	State   ClaimsState
	Project string
	Read    apiv1.PortalTaskClaims
	Cursor  bool
	Preview bool
}

// ClaimsStateOf maps one decoded operator page to the state the portal shows.
// Any contract failure is Unavailable: Unknown, never empty. Empty needs a
// complete first page with no items, and a complete first page with no items
// that is not Empty breaks the contract.
func ClaimsStateOf(read apiv1.PortalTaskClaims, project string, cursor bool) ClaimsState {
	if apiv1.ValidatePortalTaskClaims(read) != nil || read.Project != project {
		return ClaimsUnavailable
	}
	switch read.State {
	case apiv1.PortalTaskClaimsStateUnknown:
		switch read.Reason {
		case apiv1.PortalTaskClaimsReasonUnconfigured:
			return ClaimsUnconfigured
		case apiv1.PortalTaskClaimsReasonScopeUnconfirmed:
			return ClaimsOutOfScope
		}
		return ClaimsUnavailable
	case apiv1.PortalTaskClaimsStateEmpty:
		if cursor {
			return ClaimsUnavailable
		}
		return ClaimsEmpty
	case apiv1.PortalTaskClaimsStateAvailable:
		if !cursor && read.Complete && len(read.Items) == 0 {
			return ClaimsUnavailable
		}
		return ClaimsAvailable
	}
	return ClaimsUnavailable
}

// claimLabel is the owner-state label table. Category is the counted word.
type claimLabel struct{ Label, Category, Tone string }

var (
	claimLabelActive    = claimLabel{"Active", "Active", "ok"}
	claimLabelDraining  = claimLabel{"Draining", "Draining", "busy"}
	claimLabelStarting  = claimLabel{"Starting", "Starting", "busy"}
	claimLabelStale     = claimLabel{"Stale since", "Stale", "wait"}
	claimLabelNoBeat    = claimLabel{"Unknown (no heartbeat)", "Unknown (no heartbeat)", "muted"}
	claimLabelSuspended = claimLabel{"Suspended", "Suspended", "muted"}
	claimLabelEnded     = claimLabel{"Ended", "Ended", "muted"}
	claimLabelNotFound  = claimLabel{"Owner not found", "Owner not found", "bad"}
	claimLabelUnknown   = claimLabel{"Unknown owner", "Unknown owner", "muted"}
	claimCategoryOrder  = []string{"Active", "Draining", "Starting", "Stale", "Unknown (no heartbeat)", "Suspended", "Ended", "Owner not found", "Unknown owner"}
)

// ownerLabel never returns Active unless the owner is Running or Idle with a
// heartbeat at most 15 minutes before observedAt. Any other word is Unknown.
func ownerLabel(owner apiv1.PortalTaskClaimOwner, observedAt time.Time) claimLabel {
	fresh := time.Duration(apiv1.PortalTaskClaimsFreshForSeconds) * time.Second
	beat := owner.HeartbeatAt
	switch owner.State {
	case apiv1.PortalClaimOwnerRunning, apiv1.PortalClaimOwnerIdle, apiv1.PortalClaimOwnerDraining:
		switch owner.Freshness {
		case apiv1.PortalClaimFreshnessObserved:
			if beat == nil || beat.After(observedAt) || observedAt.Sub(*beat) > fresh {
				return claimLabelUnknown
			}
			if owner.State == apiv1.PortalClaimOwnerDraining {
				return claimLabelDraining
			}
			return claimLabelActive
		case apiv1.PortalClaimFreshnessStale:
			if beat == nil || observedAt.Sub(*beat) <= fresh {
				return claimLabelUnknown
			}
			return claimLabelStale
		case apiv1.PortalClaimFreshnessUnknown:
			return claimLabelNoBeat
		}
		return claimLabelUnknown
	case apiv1.PortalClaimOwnerPending:
		return claimLabelStarting
	case apiv1.PortalClaimOwnerSuspended:
		return claimLabelSuspended
	case apiv1.PortalClaimOwnerArchived, apiv1.PortalClaimOwnerFailed, apiv1.PortalClaimOwnerDeleting:
		ended := claimLabelEnded
		ended.Label = "Ended (" + owner.State + ")"
		return ended
	case apiv1.PortalClaimOwnerNotFound, apiv1.PortalClaimOwnerReplaced:
		return claimLabelNotFound
	}
	return claimLabelUnknown
}

type claimRow struct {
	Owner, OwnerHref, TaskKey, Repository           string
	Label, Tone, StaleAt, StaleLabel                string
	CreatedAt, CreatedLabel, Age, TrackerHref, Name string
	TrackerText                                     string
}

type claimsNotice struct{ Title, Detail string }

var claimsNotices = map[ClaimsState]claimsNotice{
	ClaimsNotConnected: {"Claims are not connected.", "This portal has no Claims source. No claims are shown."},
	ClaimsRefused:      {"The operator refused this read.", "No claims are shown. This is not an empty list."},
	ClaimsNotServed:    {"This operator does not serve claims to the portal.", "No claims are shown. This is not an empty list."},
	ClaimsUnavailable:  {"Claims could not be read.", "Unknown, not empty. No claims are shown."},
	ClaimsUnconfigured: {"Claims are not enabled on the operator.", "No claims are shown. This is not an empty list."},
	ClaimsOutOfScope:   {"", "No claims are shown. This is not an empty list."},
}

// RenderClaims returns one complete escaped page and its exact CSP. It refuses
// a page whose state does not follow from its read, so a caller cannot show
// Empty or a table that the contract does not support.
func RenderClaims(page ClaimsPage, limit int) ([]byte, string, error) {
	if limit <= 0 || !apiv1.ValidPortalTaskClaimsProject(page.Project) {
		return nil, "", ErrUnavailable
	}
	view := struct {
		Policy, Project, Notice, Detail, Href       string
		ObservedAt, ObservedLabel, CountLine        string
		NextHref, FirstHref                         string
		Preview, Retry, Empty, Available, Truncated bool
		Later                                       bool
		Rows                                        []claimRow
	}{Policy: strings.TrimSuffix(ClaimsPolicy, "; frame-ancestors 'none'"), Project: page.Project, Href: claimsHref, Preview: page.Preview}
	switch page.State {
	case ClaimsEmpty, ClaimsAvailable:
		if ClaimsStateOf(page.Read, page.Project, page.Cursor) != page.State {
			return nil, "", ErrUnavailable
		}
		view.ObservedAt, view.ObservedLabel = askTime(page.Read.ObservedAt)
		view.Empty, view.Available = page.State == ClaimsEmpty, page.State == ClaimsAvailable
	default:
		notice, ok := claimsNotices[page.State]
		if !ok {
			return nil, "", ErrUnavailable
		}
		view.Notice, view.Detail = notice.Title, notice.Detail
		if page.State == ClaimsOutOfScope {
			view.Notice = page.Project + " is outside the portal's claim scope."
		}
		view.Retry = page.State == ClaimsUnavailable
	}
	if view.Available {
		read := page.Read
		items := append([]apiv1.PortalTaskClaimItem(nil), read.Items...)
		// A complete page is sorted newest first. A truncated page keeps
		// store order, because a sort could not cover the other pages.
		view.Truncated = !read.Complete
		if read.Complete {
			sort.SliceStable(items, func(i, j int) bool {
				if items[i].CreatedAt.Equal(items[j].CreatedAt) {
					return items[i].Name < items[j].Name
				}
				return items[i].CreatedAt.After(items[j].CreatedAt)
			})
		}
		counts := map[string]int{}
		for _, item := range items {
			label := ownerLabel(item.Owner, read.ObservedAt)
			counts[label.Category]++
			row := claimRow{Name: item.Name, Owner: item.Owner.Name, TaskKey: item.TaskKey, Repository: item.Repository, Label: label.Label, Tone: productTone(label.Tone)}
			if productSessionUID.MatchString(item.Owner.UID) {
				row.OwnerHref = productSessionsHref + "?" + url.Values{"session": {item.Owner.UID}}.Encode()
			}
			if label == claimLabelStale {
				row.StaleAt, row.StaleLabel = askTime(*item.Owner.HeartbeatAt)
			}
			row.CreatedAt, row.CreatedLabel = askTime(item.CreatedAt)
			if item.CreatedAt.After(read.ObservedAt) {
				// A creation time after the read is not shown as a time.
				row.CreatedAt, row.CreatedLabel = "", "Unknown"
			}
			row.Age = claimAge(item.CreatedAt, read.ObservedAt)
			if n, ok := apiv1.PortalClaimIssueNumber(item.TaskKey); ok && apiv1.ValidPortalClaimGitHub(item.GitHub) {
				row.TrackerHref, row.TrackerText = "https://github.com/"+item.GitHub+"/issues/"+n, "GitHub issue #"+n
			}
			view.Rows = append(view.Rows, row)
		}
		parts := []string{claimCount(len(items), "claim", "claims")}
		for _, category := range claimCategoryOrder {
			if counts[category] > 0 {
				parts = append(parts, strconv.Itoa(counts[category])+" "+category)
			}
		}
		view.CountLine = strings.Join(parts, " · ")
		if read.NextCursor != "" {
			view.NextHref = claimsHref + "?" + url.Values{apiv1.PortalTaskClaimsQueryCursor: {read.NextCursor}}.Encode()
		}
		if page.Cursor {
			view.Later, view.FirstHref = true, claimsHref
		}
	}
	buffer := &boundedPagesBuffer{limit: limit}
	if err := claimsTemplate.Execute(buffer, view); err != nil {
		return nil, "", ErrUnavailable
	}
	return buffer.Bytes(), ClaimsPolicy, nil
}

func claimCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// claimAge is how long the claim had been held when the operator observed the
// page. It never reads the portal's or the browser's clock. A creation time
// after the observation is Unknown.
func claimAge(created, observed time.Time) string {
	if created.After(observed) {
		return "Held for an unknown time"
	}
	d := observed.Sub(created)
	days, hours, minutes := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case days > 0:
		return "Held " + strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h when observed"
	case hours > 0:
		return "Held " + strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m when observed"
	case minutes > 0:
		return "Held " + strconv.Itoa(minutes) + "m when observed"
	}
	return "Held under a minute when observed"
}
