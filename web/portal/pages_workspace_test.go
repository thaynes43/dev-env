package portal

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

const pagesSharedHeader = `<header class="topbar">{{template "product-icons"}}<a class="brand" href="/portal/projects/dev-env/environments/internal/sessions"><span class="brand-mark" aria-hidden="true">/&gt;</span>dev-env<sup>V2</sup></a>{{template "product-nav" "pages"}}</header>`

const pagesSharedMobileNav = `{{template "product-mobile-nav" "pages"}}`

func TestPagesSharedNavBytesMatchBaseline(t *testing.T) {
	body, err := os.ReadFile("pages.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, pagesSharedHeader) != 1 || strings.Count(text, pagesSharedMobileNav) != 1 {
		t.Fatal("shared nav bytes changed")
	}
}

func TestPagesWorkspaceFocusAndWidths(t *testing.T) {
	for _, want := range []string{
		"focus-visible",
		"var(--focus)",
		"overflow-wrap:anywhere",
		"minmax(0,22rem) minmax(0,1fr)",
		"max-width:700px",
		"max-width:390px",
		"max-width:320px",
	} {
		if !strings.Contains(pagesStyle, want) {
			t.Fatalf("pages style missing %s", want)
		}
	}
	if strings.Contains(pagesStyle, "overflow-x:auto") {
		t.Fatal("pages style allows a horizontal scrollbar")
	}
	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "2026-10-28T12:02:00Z", Source: "Synthetic fixture",
		Coverage: "Current snapshot only.",
		Groups: []PagesGroup{{
			ID: "pages-group-1", Default: true,
			AlertName: "DiskFull", AlertNameKnown: true, AlertNameDisplay: "DiskFull",
			Severity: "critical", SeverityKnown: true, SeverityDisplay: "critical",
			Count: 2, EarliestStart: "2026-10-28T15:00:00Z", LatestUpdate: "2026-10-28T16:00:00Z",
			Alerts: []PagesAlert{{
				Fingerprint: "selected-fingerprint", NameDisplay: "DiskFull", NameKnown: true,
				Status: "active", StartsAt: "2026-10-28T15:00:00Z", EndsAt: "Unknown", UpdatedAt: "2026-10-28T16:00:00Z",
				Summary: "selected summary", Description: "selected description",
				Disposition: "Unknown", Recurrence: "Unknown",
				Labels:       []PagesPair{{Key: "alertname", Value: "DiskFull"}, {Key: "severity", Value: "critical"}},
				GeneratorURL: "https://example.test/graph",
			}},
		}},
		Controls: DisconnectedPagesControls([]PagesTypeControl{{
			AlertName: "DiskFull", AlertNameKnown: true, Label: "DiskFull",
			Desired: PagesDesiredUnset, Applied: PagesAppliedState,
		}}),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, "<h1") != 1 {
		t.Fatalf("h1 count %d", strings.Count(text, "<h1"))
	}
	for _, want := range []string{
		`id="pages-group-1" class="pages-detail is-default"`,
		"selected-fingerprint",
		"selected summary",
		`datetime="2026-10-28T15:00:00Z"`,
		"Current snapshot only",
		`class="pages-count">2<`,
		`href="https://example.test/graph" rel="noreferrer">Generator</a>`,
		`<fieldset disabled>`,
		`<select disabled aria-label="Escalate for DiskFull · selected…rint"`,
		PagesControlNotice,
		PagesAppliedState,
		PagesDesiredUnset,
		PagesDesiredOn,
		PagesDesiredOff,
		"No preference document is loaded. This is not a loaded empty store.",
		`href="#pages-group-1"`,
		`href="#pages-configuration"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(text, `tabindex="0"`) || strings.Contains(text, "<form") || strings.Contains(text, "policy applied") || strings.Contains(text, "Saved preferences are loaded.") || strings.Contains(text, ">Runbook</a>") {
		t.Fatal("control or absent link over-claimed")
	}
	partial, err := RenderPages(view, 100)
	if err == nil || partial != nil {
		t.Fatal("short limit returned a partial page")
	}
}

func TestPagesWorkspaceStatesAndLongLabels(t *testing.T) {
	long := strings.Repeat("<script>", 40)
	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Groups: []PagesGroup{{
			ID: "pages-group-1", Default: true, AlertNameDisplay: long, AlertNameKnown: true, AlertName: long,
			SeverityDisplay: "warning", SeverityKnown: true, Count: 1, EarliestStart: "Unknown", LatestUpdate: "Unknown",
			Alerts: []PagesAlert{{
				Fingerprint: "long", NameDisplay: long, NameKnown: true, Status: "active",
				StartsAt: "Unknown", EndsAt: "Unknown", UpdatedAt: "Unknown",
				Summary: long, Disposition: "Unknown", Recurrence: "Unknown",
				Labels: []PagesPair{{Key: "note", Value: long}},
			}},
		}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Contains(text, "<script>") || strings.Count(text, "&lt;script&gt;") < 40 {
		t.Fatalf("long label was not escaped, count %d", strings.Count(text, "&lt;script&gt;"))
	}
	partial, err := RenderPages(view, 100)
	if err == nil || partial != nil {
		t.Fatal("long label render was not atomic")
	}
	for _, tc := range []struct {
		name      string
		view      PagesWorkspace
		want      string
		forbidden []string
	}{
		{"empty", PagesWorkspace{Condition: "empty", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown", Controls: DisconnectedPagesControls(nil)}, "Observed zero current alerts.", []string{"Alertmanager is unavailable.", "Pages not enabled.", "Current snapshot is invalid.", `class="pages-count"`}},
		{"unavailable", PagesWorkspace{Condition: "unavailable", Availability: "Unavailable", ObservedAt: "Unknown", Source: "Unknown", Controls: DisconnectedPagesControls(nil)}, "Alertmanager is unavailable.", []string{"Pages not enabled.", "Observed zero current alerts.", "Current snapshot is invalid."}},
		{"disabled", PagesWorkspace{Condition: "disabled", Availability: "Unavailable", ObservedAt: "Unknown", Source: "Unknown", Controls: DisconnectedPagesControls(nil)}, "Pages not enabled.", []string{"Alertmanager is unavailable.", "Observed zero current alerts."}},
		{"invalid", PagesWorkspace{Condition: "invalid", Availability: "Unavailable", ObservedAt: "Unknown", Source: "Unknown", Controls: DisconnectedPagesControls(nil)}, "Current snapshot is invalid.", []string{"Alertmanager is unavailable.", "Observed zero current alerts.", "Pages not enabled."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RenderPages(tc.view, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			page := string(got)
			if !strings.Contains(page, tc.want) || strings.Count(page, "<h1") != 1 {
				t.Fatalf("missing %s or h1", tc.want)
			}
			for _, bad := range tc.forbidden {
				if strings.Contains(page, bad) {
					t.Fatalf("state contains %s", bad)
				}
			}
		})
	}
}

func TestPagesControlsBelongToEachAlert(t *testing.T) {
	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Groups: []PagesGroup{{ID: "pages-group-1", Default: true, AlertNameDisplay: "DiskFull", Alerts: []PagesAlert{
			{Fingerprint: "aabbccdd11223344", NameDisplay: "DiskFull", NameKnown: true},
			{Fingerprint: "eeff001155667788", NameDisplay: "DiskFull", NameKnown: true},
		}}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	alerts := pagesTestNodes(doc, func(n *html.Node) bool { return pagesTestAttr(n, "data-fingerprint") != "" })
	if len(alerts) != 2 {
		t.Fatalf("alert articles: %d", len(alerts))
	}
	for i, article := range alerts {
		fingerprint := view.Groups[0].Alerts[i].Fingerprint
		if pagesTestAttr(article, "data-fingerprint") != fingerprint {
			t.Fatalf("alert identity: %s", pagesTestAttr(article, "data-fingerprint"))
		}
		selects := pagesTestNodes(article, func(n *html.Node) bool { return n.Data == "select" })
		if len(selects) != 2 {
			t.Fatalf("%s has %d controls", fingerprint, len(selects))
		}
		for j, selectNode := range selects {
			name := []string{"Escalate", "Mute"}[j] + " for DiskFull · " + shortFingerprint(fingerprint)
			if pagesTestAttr(selectNode, "aria-label") != name {
				t.Fatalf("control identity: %s", pagesTestAttr(selectNode, "aria-label"))
			}
			disabled := false
			for _, attr := range selectNode.Attr {
				if attr.Key == "disabled" {
					disabled = true
				}
			}
			if !disabled {
				t.Fatalf("enabled control for %s", fingerprint)
			}
			selected := pagesTestNodes(selectNode, func(n *html.Node) bool {
				for _, attr := range n.Attr {
					if n.Data == "option" && attr.Key == "selected" {
						return true
					}
				}
				return false
			})
			if len(selected) != 1 || pagesTestAttr(selected[0], "value") != "unset" {
				t.Fatal("desired control is not Unset")
			}
		}
		if !strings.Contains(pagesTestText(article), "Desired Unset") || !strings.Contains(pagesTestText(article), "Applied "+PagesAppliedState) {
			t.Fatal("alert control omitted desired or applied state")
		}
	}
	configuration := pagesTestNodes(doc, func(n *html.Node) bool { return pagesTestAttr(n, "id") == "pages-configuration" })
	if len(configuration) != 1 || strings.Contains(pagesTestText(configuration[0]), "Escalate") || strings.Contains(pagesTestText(configuration[0]), "Mute") {
		t.Fatal("page-wide escalate/mute control remains")
	}
	current := pagesTestNodes(doc, func(n *html.Node) bool { return pagesTestAttr(n, "aria-current") == "location" })
	if len(current) != 1 || pagesTestAttr(current[0], "href") != "#pages-group-1" {
		t.Fatal("current-group marker is not in the matching detail")
	}
}

// TestPagesLabelTablePhoneStyle pins the phone layout of the per-alert label
// table. Below 700px views.css makes tables blocks; without these rules the
// caption and the row headers stay table boxes sized to one character.
func TestPagesLabelTablePhoneStyle(t *testing.T) {
	const start = "@media (max-width:700px){"
	from := strings.Index(pagesStyle, start)
	if from < 0 {
		t.Fatal("pages style has no 700px block")
	}
	block := pagesStyle[from+len(start):]
	end := strings.Index(block, "\n}\n")
	if end < 0 {
		t.Fatal("700px block is not closed")
	}
	rules := pagesTestRules(block[:end])
	for selector, wants := range map[string][]string{
		// The column header row is clipped, not removed: screen readers keep it.
		".pages-content .pages-labels thead": {"position:absolute", "width:1px", "height:1px", "margin:-1px", "overflow:hidden", "clip:rect(0 0 0 0)", "clip-path:inset(50%)", "white-space:nowrap"},
		// A block caption takes the full width, so "Labels" stays on one line.
		".pages-content .pages-labels caption":    {"display:block", "width:auto", "overflow-wrap:normal", "word-break:normal"},
		".pages-content .pages-labels":            {"display:block"},
		".pages-content .pages-labels tbody":      {"display:block"},
		".pages-content .pages-labels tr":         {"display:block"},
		".pages-content .pages-labels th":         {"display:block", "text-align:left"},
		".pages-content .pages-labels td":         {"display:block", "text-align:left"},
		".pages-content .pages-labels td::before": {"content:none"},
	} {
		got, ok := rules[selector]
		if !ok {
			t.Fatalf("700px block has no rule for %s", selector)
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Fatalf("%s lacks %s: %s", selector, want, got)
			}
		}
	}
	for _, hidden := range []string{"display:none", "visibility:hidden"} {
		if strings.Contains(rules[".pages-content .pages-labels thead"], hidden) {
			t.Fatalf("label header row is removed from the accessibility tree with %s", hidden)
		}
	}

	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Groups: []PagesGroup{{ID: "pages-group-1", Default: true, AlertNameDisplay: "DiskFull", Alerts: []PagesAlert{{
			Fingerprint: "aabbccdd11223344", NameDisplay: "DiskFull", NameKnown: true,
			Labels: []PagesPair{{Key: "alertname", Value: "DiskFull"}, {Key: "team", Value: ""}},
		}}}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	tables := pagesTestNodes(doc, func(n *html.Node) bool { return n.Data == "table" && pagesTestAttr(n, "class") == "pages-labels" })
	if len(tables) != 1 {
		t.Fatalf("label tables: %d", len(tables))
	}
	captions := pagesTestNodes(tables[0], func(n *html.Node) bool { return n.Data == "caption" })
	if len(captions) != 1 || pagesTestText(captions[0]) != "Labels" {
		t.Fatal("label table caption is not Labels")
	}
	var columns []string
	for _, th := range pagesTestNodes(tables[0], func(n *html.Node) bool { return n.Data == "th" && pagesTestAttr(n, "scope") == "col" }) {
		columns = append(columns, pagesTestText(th))
	}
	if strings.Join(columns, ",") != "Label,Value" {
		t.Fatalf("label column headers: %v", columns)
	}
	rows := pagesTestNodes(tables[0], func(n *html.Node) bool { return n.Data == "th" && pagesTestAttr(n, "scope") == "row" })
	if len(rows) != 2 || pagesTestText(rows[0]) != "alertname" || pagesTestText(rows[1]) != "team" {
		t.Fatal("label row headers changed")
	}
	if !strings.Contains(string(body), `<th scope="row">team</th><td class="pages-empty-value">Empty (value)</td>`) {
		t.Fatal("empty label value has no muted Empty (value) cue")
	}
}

func TestPagesAlertTimesAndEmptyCues(t *testing.T) {
	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Groups: []PagesGroup{{ID: "pages-group-1", Default: true, AlertNameDisplay: "DiskFull", Alerts: []PagesAlert{
			{
				Fingerprint: "timed", NameDisplay: "DiskFull", NameKnown: true,
				StartsAt: "2026-10-28T15:00:00Z", EndsAt: "Unknown", UpdatedAt: "2026-10-28T16:30:00Z",
				Summary: "present summary", Description: "present description",
			},
			{
				Fingerprint: "untimed", NameDisplay: "DiskFull", NameKnown: true,
				StartsAt: "Unknown", EndsAt: "not-a-time", UpdatedAt: "",
			},
		}}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	alerts := pagesTestNodes(doc, func(n *html.Node) bool { return pagesTestAttr(n, "data-fingerprint") != "" })
	if len(alerts) != 2 {
		t.Fatalf("alert articles: %d", len(alerts))
	}
	type stamp struct{ datetime, text string }
	times := func(article *html.Node) []stamp {
		var out []stamp
		for _, node := range pagesTestNodes(article, func(n *html.Node) bool { return n.Data == "time" }) {
			out = append(out, stamp{pagesTestAttr(node, "datetime"), pagesTestText(node)})
		}
		return out
	}
	got := times(alerts[0])
	want := []stamp{{"2026-10-28T15:00:00Z", "28 Oct 2026, 15:00 UTC"}, {"2026-10-28T16:30:00Z", "28 Oct 2026, 16:30 UTC"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("per-alert time elements: %v", got)
	}
	text := string(body)
	for _, wantText := range []string{
		`<dt>Starts at</dt><dd><time datetime="2026-10-28T15:00:00Z">28 Oct 2026, 15:00 UTC</time></dd>`,
		`<dt>Ends at</dt><dd>Unknown</dd>`,
		`<dt>Updated at</dt><dd><time datetime="2026-10-28T16:30:00Z">28 Oct 2026, 16:30 UTC</time></dd>`,
		`<p class="turn-text">present summary</p>`,
		`<p class="turn-text">present description</p>`,
		`<p class="turn-text pages-empty-value">No summary</p>`,
		`<p class="turn-text pages-empty-value">No description</p>`,
	} {
		if !strings.Contains(text, wantText) {
			t.Fatalf("missing %s", wantText)
		}
	}
	if untimed := times(alerts[1]); len(untimed) != 0 {
		t.Fatalf("unknown alert times rendered time elements: %v", untimed)
	}
	if strings.Contains(text, "not-a-time") || strings.Count(pagesTestText(alerts[1]), "Unknown") < 3 {
		t.Fatal("unknown alert times are not plain Unknown")
	}
	if strings.Contains(pagesTestText(alerts[0]), "No summary") || strings.Contains(pagesTestText(alerts[0]), "No description") {
		t.Fatal("present summary or description shows an empty cue")
	}
}

// pagesTestRules maps each selector in a flat CSS block to its declarations.
func pagesTestRules(block string) map[string]string {
	rules := map[string]string{}
	for _, rule := range strings.Split(block, "}") {
		selectors, declarations, ok := strings.Cut(rule, "{")
		if !ok {
			continue
		}
		for _, selector := range strings.Split(selectors, ",") {
			selector = strings.TrimSpace(selector)
			rules[selector] += declarations + ";"
		}
	}
	return rules
}

func pagesTestAttr(n *html.Node, key string) string {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func pagesTestNodes(root *html.Node, match func(*html.Node) bool) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && match(n) {
			found = append(found, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return found
}

func pagesTestText(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var text strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		text.WriteString(pagesTestText(c))
	}
	return text.String()
}
