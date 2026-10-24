package portal

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

//go:embed attention.html
var pageSource string

//go:embed attention.css
var pageStyle string

var (
	exactHead     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	contentPolicy = policy()
	// Only the embedded, repository-owned CSS enters this template source.
	pageTemplate = template.Must(template.New("attention").Parse(strings.Replace(pageSource, "/*PORTAL_STYLE*/", pageStyle, 1)))
)

func policy() string {
	digest := sha256.Sum256([]byte(pageStyle))
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
type pageView struct {
	Policy, ObservedAt, Notice, Scope string
	Groups                            []groupView
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
