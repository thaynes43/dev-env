package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/thaynes43/dev-env/internal/pages"
)

func TestPagesRenderEscapingAndPolicy(t *testing.T) {
	snapshot, err := pages.ParseAlertmanager([]byte(`[{"fingerprint":"f","labels":{"alertname":"<script>x</script>","Name":"A","name":"B"},"annotations":{"summary":"<img src=x>","runbook_url":"javascript:alert(1)"},"status":{"state":"unprocessed"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderPages(snapshot, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"&lt;script&gt;", "&lt;img", ">Name<", ">name<", "Unknown", "unprocessed", "viewport", "Pages · Alertmanager"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(text, "<script>") || strings.Contains(text, "javascript:alert") {
		t.Fatal("unsafe content")
	}
	sum := sha256.Sum256([]byte(viewsStyle + pagesStyle))
	if !strings.Contains(PagesPolicy, base64.StdEncoding.EncodeToString(sum[:])) {
		t.Fatal("CSP mismatch")
	}
	if _, err := RenderPages(snapshot, false, 100); err == nil {
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
	snapshot, err := pages.ParseAlertmanager([]byte(`[{"fingerprint":"missing","labels":{"alertname":"Missing"},"status":{"state":"active"}},{"fingerprint":"present","labels":{"alertname":"Present"},"status":{"state":"active"},"startsAt":"2026-10-28T12:00:00Z","endsAt":"2026-10-28T13:00:00Z","updatedAt":"2026-10-28T12:01:00Z"}]`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := RenderPages(snapshot, false, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		`<dt>Starts at</dt><dd>Unknown</dd><dt>Ends at</dt><dd>Unknown</dd><dt>Updated at</dt><dd>Unknown</dd>`,
		`<dt>Starts at</dt><dd>2026-10-28T12:00:00Z</dd><dt>Ends at</dt><dd>2026-10-28T13:00:00Z</dd><dt>Updated at</dt><dd>2026-10-28T12:01:00Z</dd>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("timestamp rendering missing %s", want)
		}
	}
}

func TestPagesHasSharedProductNavigation(t *testing.T) {
	body, err := RenderPages(pages.AlertmanagerSnapshot{}, false, 1<<20)
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
