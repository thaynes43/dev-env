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
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed attention.html
var pageSource string

//go:embed views.html
var viewsSource string

//go:embed attention.css
var pageStyle string

//go:embed views.css
var viewsStyle string

var (
	exactHead     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	contentPolicy = policy(pageStyle)
	// ProductPolicy is the Sessions and Tasks pages' Content-Security-Policy: no
	// script, no form, no external resource, and only the embedded views stylesheet.
	ProductPolicy = policy(viewsStyle)
	// Only the embedded, repository-owned CSS enters this template source.
	pageTemplate  = template.Must(template.New("attention").Parse(strings.Replace(pageSource, "/*PORTAL_STYLE*/", pageStyle, 1)))
	viewsTemplate = template.Must(template.New("views").Parse(strings.Replace(viewsSource, "/*PORTAL_STYLE*/", viewsStyle, 1)))
)

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

// ProductSection is one panel of a row's detail: optional tiles, key-value
// rows and one short note.
type ProductSection struct {
	Title           string
	Metrics, Fields []ProductField
	Note            string
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

// ProductUsageRow is one session's usage row, linked to its detail by ID.
type ProductUsageRow struct {
	ID, Name, Sub string
	Cells         []ProductUsageCell
}

// ProductUsageTable is one titled breakdown table. A row with an empty ID is
// a group, not a session, and is not linked.
type ProductUsageTable struct {
	Title, Note string
	Columns     []string
	Rows        []ProductUsageRow
}

// ProductUsage is the Sessions page's Usage view: headline counts, account
// allowance kept apart from token observations, breakdowns that sum reported
// values only, the per-session table and what the numbers cover.
type ProductUsage struct {
	Stats            []ProductStat
	Scope, ScopeNote string
	Allowance        []ProductField
	Breakdowns       []ProductUsageTable
	Columns          []string
	Rows             []ProductUsageRow
	RowsNote         string
	Coverage         []ProductField
}

// ProductFilter narrows a product list: to its Needs attention group, or, on
// Sessions, to one project (a repository). The zero value lists everything.
type ProductFilter struct {
	Attention bool
	Project   string
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
}

const (
	productSessionsHref = "/portal/projects/dev-env/environments/internal/sessions"
	productTasksHref    = "/portal/projects/dev-env/environments/internal/tasks"
)

var (
	productTones = map[string]bool{"ok": true, "busy": true, "wait": true, "bad": true, "info": true, "muted": true}
	productID    = regexp.MustCompile(`^[st][0-9]{1,4}$`)
)

func productTone(tone string) string {
	if productTones[tone] {
		return tone
	}
	return "muted"
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
// the Usage breakdowns still cover everything read.
func RenderProductWithin(page ProductPage, limit int) ([]byte, error) {
	body, err := RenderProduct(page)
	for keep := countRows(page); err == nil && len(body) > limit && keep > 0; {
		keep = keep * 3 / 4
		body, err = RenderProduct(keepRows(page, keep))
	}
	if err == nil && len(body) > limit {
		return nil, fmt.Errorf("product page needs %d bytes, over %d", len(body), limit)
	}
	return body, err
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
		n := min(len(g.Rows), keep)
		g.Rows = append([]ProductRow(nil), g.Rows[:n]...)
		keep -= n
		for _, r := range g.Rows {
			kept[r.ID] = true
		}
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
	view := struct {
		Policy, Title, Current, Section, Notice, ObservedAt, ObservedLabel string
		SessionsHref, TasksHref, UsageHref                                 string
		ListTitle, ListCount                                               string
		Quiet, Preview, Banner, HasRows                                    bool
		All, Attention                                                     railLinkView
		Projects                                                           []railLinkView
		Groups                                                             []productGroupView
		Rows                                                               []productRowView
		Usage                                                              *ProductUsage
	}{Policy: strings.TrimSuffix(ProductPolicy, "; frame-ancestors 'none'"), Title: page.Title, Current: strings.ToLower(page.Title), Notice: page.Notice,
		ObservedAt: page.ObservedAt, ObservedLabel: observedLabel(page.ObservedAt), Preview: page.Preview,
		SessionsHref: productSessionsHref, TasksHref: productTasksHref, UsageHref: productSessionsHref + "#usage",
		ListTitle: page.ListTitle}
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
		if view.Current == "sessions" && page.Usage != nil {
			view.UsageHref = "#usage"
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
				g.Rows = append(g.Rows, row)
				view.Rows = append(view.Rows, productRowView{ProductRow: row, Default: len(view.Rows) == 0})
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
