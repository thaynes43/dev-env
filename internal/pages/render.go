package pages

import (
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // the zone must resolve in an image with no zoneinfo
)

// DisplayZone is the zone pages are shown in beside UTC: Tom's.
const DisplayZone = "America/New_York"

// NotCovered lists every Pushover sender dev-env knows about that does not
// record here (DESIGN-031 section 3). Keep it in step with that table.
var NotCovered = []string{
	"Alertmanager (the critical and Plex canary routes)",
	"Gatus",
	"upgrade-gate (health-gate)",
	"alert-responder",
	"dev-env-ops",
	"the v1 dev-env pod (auth-watch)",
	"haynesnetwork outbox",
	"scribe-notes",
	"Home Assistant (not inventoried)",
	"keeper notify-test (a manual test notice)",
}

// Banner is the first line of every lookup: what this history is, and is not.
const Banner = "KEEPER PAGES ONLY - not all Pushover history. Recorded or sent (accepted by Pushover) does not mean delivered or read."

// Coverage is the statement every lookup prints, so a missing page is never
// read as "no page". Its first line is the Banner.
func Coverage(d *Document) []string {
	since := "the first keeper image that has this code"
	if d != nil && d.RecordingSince != nil {
		since = FormatTime(*d.RecordingSince)
	}
	lines := []string{
		Banner,
		"Coverage: dev-env-keeper pages only (login-lapse warnings, NeedsLogin and resolved notices), recorded since " + since + ".",
		"NOT covered: " + strings.Join(NotCovered, "; ") + ".",
		"Pages before recording began are Unknown. A page missing here is Unknown, not \"no page\".",
	}
	if d != nil && d.DroppedCount > 0 {
		old := "?"
		if d.OldestRetainedAt != nil {
			old = FormatTime(*d.OldestRetainedAt)
		}
		lines = append(lines, fmt.Sprintf("Older records were dropped by the bounds (%d dropped); the oldest kept is %s. Pages before that are Unknown here.", d.DroppedCount, old))
	}
	if d != nil && d.ResetAt != nil {
		lines = append(lines, fmt.Sprintf("The record was damaged and reset at %s; %d earlier records were lost (best-effort count). Pages before that are Unknown.", FormatTime(*d.ResetAt), d.LostRecords))
	}
	lines = append(lines,
		"States: recorded = written before the send, outcome not stored; sent = Pushover accepted it (HTTP 200, status 1), not proof the phone received it; failed = a definite 4xx, or no request made; ambiguous = timeout, transport error, 5xx or unconfirmed 2xx, it may have reached Tom.")
	return lines
}

// FormatTime shows a time as UTC and as the display zone with its offset, so a
// reader sees both across a DST change.
func FormatTime(t time.Time) string {
	loc, err := time.LoadLocation(DisplayZone)
	if err != nil {
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC") + " (" + t.In(loc).Format("2006-01-02 15:04:05 MST -07:00") + ")"
}

// Filter selects records. Zero fields match everything.
type Filter struct {
	Sender string
	State  State
	Since  time.Time
	Limit  int
}

// Select returns the matching records, newest first, at most Limit (all when 0).
func (d *Document) Select(f Filter) []Record {
	out := make([]Record, 0, len(d.Records))
	for _, r := range d.Records {
		if f.Sender != "" && r.Sender != f.Sender {
			continue
		}
		if f.State != "" && r.State != f.State {
			continue
		}
		if !f.Since.IsZero() && r.RecordedAt.Before(f.Since) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].RecordedAt.After(out[j].RecordedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out
}

// Summary is one line for a list.
func (r Record) Summary() string {
	return fmt.Sprintf("%s  %-9s %s  %s", FormatTime(r.RecordedAt), r.State, r.Sender, oneLine(r.Title))
}

// Detail is the full text of one page.
func (r Record) Detail() string {
	var b strings.Builder
	w := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-14s %s\n", k+":", v)
		}
	}
	w("key", r.Key)
	w("recorded", FormatTime(r.RecordedAt))
	w("last attempt", FormatTime(r.LastAttemptAt))
	w("state", string(r.State))
	w("sender", r.Sender)
	w("source", r.Source)
	w("provenance", r.Provenance)
	w("correlation", r.Correlation)
	w("title", r.Title)
	w("priority", fmt.Sprint(r.Priority))
	w("attempts", fmt.Sprintf("%d (accepted %d, ambiguous %d)", r.Attempts, r.SentCount, r.AmbiguousCount))
	if r.HTTPStatus != 0 {
		w("http status", fmt.Sprint(r.HTTPStatus))
	}
	w("pushover req", r.RequestID)
	if r.URL != "" {
		w("link", r.URL)
		w("link title", r.URLTitle)
	}
	if r.Truncated {
		w("note", "title or body was cut to Pushover's limit when recorded")
	}
	b.WriteString("body:\n")
	for _, line := range strings.Split(r.Body, "\n") {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:99]) + "…"
	}
	return s
}
