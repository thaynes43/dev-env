package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/pages"
)

func TestPagesRenderEscapingAndPolicy(t *testing.T) {
	view := PagesWorkspace{
		Condition:    "available",
		Availability: "Available",
		ObservedAt:   "Unknown",
		Source:       "Unknown",
		Coverage:     pages.AggregateCoverage,
		Groups: []PagesGroup{{
			ID: "pages-group-1", Default: true,
			AlertName: "<script>x</script>", AlertNameKnown: true, AlertNameDisplay: "<script>x</script>",
			Severity: "warning", SeverityKnown: true, SeverityDisplay: "warning",
			Count: 1, EarliestStart: "Unknown", LatestUpdate: "Unknown",
			Alerts: []PagesAlert{{
				Fingerprint: "f", Name: "<script>x</script>", NameKnown: true, NameDisplay: "<script>x</script>",
				Status: "unprocessed", StartsAt: "Unknown", EndsAt: "Unknown", UpdatedAt: "Unknown",
				Summary: "<img src=x>", Disposition: "Unknown", Recurrence: "Unknown",
				Labels: []PagesPair{{Key: "Name", Value: "A"}, {Key: "name", Value: "B"}, {Key: "alertname", Value: "<script>x</script>"}},
			}},
		}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"&lt;script&gt;", "&lt;img", ">Name<", ">name<", "Unknown", "unprocessed", "viewport", "Pages · Alertmanager", PagesControlNotice} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(text, "<script>") || strings.Contains(text, "javascript:alert") || strings.Contains(text, "<form") {
		t.Fatal("unsafe content")
	}
	sum := sha256.Sum256([]byte(viewsStyle + pagesStyle))
	if !strings.Contains(PagesPolicy, base64.StdEncoding.EncodeToString(sum[:])) {
		t.Fatal("CSP mismatch")
	}
	if partial, err := RenderPages(view, 100); err == nil || partial != nil {
		t.Fatal("render limit ignored")
	}
}

func TestExistingNavigationLinksToPages(t *testing.T) {
	body, err := RenderProduct(ProductPage{Title: "Sessions", Notice: "Unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), `href="/portal/projects/dev-env/environments/internal/pages"`) != 2 {
		t.Fatal("top and mobile Pages navigation missing")
	}
}

func TestPagesTimestampPresence(t *testing.T) {
	view := PagesWorkspace{
		Condition: "available", Availability: "Available", ObservedAt: "Unknown", Source: "Unknown",
		Coverage: pages.AggregateCoverage,
		Groups: []PagesGroup{{
			ID: "pages-group-1", Default: true,
			AlertName: "Present", AlertNameKnown: true, AlertNameDisplay: "Present",
			Severity: "warning", SeverityKnown: true, SeverityDisplay: "warning",
			Count: 1, EarliestStart: "2026-10-28T12:00:00Z", LatestUpdate: "Unknown",
			Alerts: []PagesAlert{
				{Fingerprint: "missing", NameDisplay: "Missing", NameKnown: true, Status: "active", StartsAt: "Unknown", EndsAt: "Unknown", UpdatedAt: "Unknown", Disposition: "Unknown", Recurrence: "Unknown"},
				{Fingerprint: "present", NameDisplay: "Present", NameKnown: true, Status: "active", StartsAt: "2026-10-28T12:00:00Z", EndsAt: "2026-10-28T13:00:00Z", UpdatedAt: "2026-10-28T12:01:00Z", Disposition: "Unknown", Recurrence: "Unknown"},
			},
		}},
		Controls: DisconnectedPagesControls(nil),
	}
	body, err := RenderPages(view, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`<dt>Starts at</dt><dd>Unknown</dd>`,
		`<dd>28 Oct 2026, 12:00 UTC</dd>`,
		`<dd>28 Oct 2026, 13:00 UTC</dd>`,
		`<dd>28 Oct 2026, 12:01 UTC</dd>`,
		`datetime="2026-10-28T12:00:00Z"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("timestamp rendering missing %s", want)
		}
	}
	if strings.Count(text, "<h1") != 1 {
		t.Fatal("page does not have one h1")
	}
}

func TestPagesHasSharedProductNavigation(t *testing.T) {
	body, err := RenderPages(PagesWorkspace{}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if strings.Count(text, `href="`+productSessionsHref+`?view=usage"`) != 2 {
		t.Fatal("Pages lacks top and mobile Usage navigation")
	}
	if strings.Count(text, `href="/portal/projects/dev-env/environments/internal/pages" aria-current="page"`) != 2 {
		t.Fatal("Pages navigation does not mark Pages current")
	}
}

func TestSessionsAndPagesRenderConcurrently(t *testing.T) {
	for _, name := range []string{"product-nav", "product-mobile-nav", "product-icons"} {
		if viewsTemplate.Lookup(name).Tree == pagesTemplate.Lookup(name).Tree {
			t.Fatalf("%s shares a mutable parse tree across template sets", name)
		}
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := RenderProduct(ProductPage{Title: "Sessions", Notice: "Sessions not connected"})
		results <- err
	}()
	go func() {
		<-start
		_, err := RenderPages(PagesWorkspace{}, 1<<20)
		results <- err
	}()
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}
