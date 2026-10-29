package portal

import (
	"os"
	"strings"
	"testing"
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
		"min-width:1280px",
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
		`tabindex="0"`,
		`<fieldset disabled>`,
		`<select id="pages-escalate" disabled`,
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
	if strings.Contains(text, "<form") || strings.Contains(text, "policy applied") || strings.Contains(text, "Saved preferences are loaded.") || strings.Contains(text, ">Runbook</a>") {
		t.Fatal("control or absent link over-claimed")
	}
	partial, err := RenderPages(view, len(body)-1)
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
	partial, err := RenderPages(view, len(body)-1)
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
