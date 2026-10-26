package portal

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// html/template drops CSS comments from template text, so the policy must hash
// what is actually rendered, not the file. This pins the two together.
func TestProductPolicyHashesTheRenderedStylesheet(t *testing.T) {
	body, err := RenderProduct(ProductPage{Title: "Sessions", Notice: "Sessions not connected"})
	if err != nil {
		t.Fatal(err)
	}
	style := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindSubmatch(body)
	if style == nil {
		t.Fatal("no stylesheet rendered")
	}
	digest := sha256.Sum256(style[1])
	hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
	if !strings.Contains(ProductPolicy, "style-src "+hash+";") {
		t.Fatalf("policy %q does not allow the rendered stylesheet %s", ProductPolicy, hash)
	}
	for _, want := range []string{"default-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(ProductPolicy, want) {
			t.Errorf("product policy lacks %q", want)
		}
	}
	for _, unsafe := range []string{"unsafe-inline", "unsafe-eval", "script-src", "unsafe-hashes"} {
		if strings.Contains(ProductPolicy, unsafe) {
			t.Errorf("product policy contains %q", unsafe)
		}
	}
	if strings.Contains(string(body), "<script") || regexp.MustCompile(`\sstyle="`).Match(body) || regexp.MustCompile(`\son[a-z]+=`).Match(body) {
		t.Fatal("product page carries script, an inline style attribute or an event handler")
	}
}

func TestRenderProductKeepsAnchorsTonesAndCountsSafe(t *testing.T) {
	page := ProductPage{Title: "Sessions", Notice: "Connected session summaries.", Connected: true, Complete: true, Quiet: true, ObservedAt: "2026-10-26T15:00:00Z",
		Groups: []ProductGroup{{Title: "Needs attention", Attention: true, Rows: []ProductRow{
			{ID: "s1", Title: "kept-row", Status: ProductStatus{Text: "Waiting", Tone: "wait"}},
			{ID: `s2" onclick="x`, Title: "dropped-row", Status: ProductStatus{Text: "Busy", Tone: "busy"}},
			{ID: "s3", Title: "toned-row", Status: ProductStatus{Text: "Odd", Tone: "evil"}},
		}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html := string(body)
	if strings.Contains(html, "dropped-row") || strings.Contains(html, "onclick") || strings.Contains(html, "tone-evil") || !strings.Contains(html, `class="badge tone-muted">Odd`) {
		t.Fatalf("unsafe id or tone rendered: %s", html)
	}
	if !strings.Contains(html, `href="#s1"`) || !strings.Contains(html, `id="s1"`) || !strings.Contains(html, `class="detail default" id="s1"`) || !strings.Contains(html, `<span class="attention-count">2</span>`) {
		t.Fatalf("row anchors or counts missing: %s", html)
	}
	if strings.Contains(html, "Fixture preview") || !strings.Contains(html, `<span role="status">Connected session summaries.</span>`) || strings.Contains(html, `class="banner"`) {
		t.Fatalf("notice placement or preview label wrong: %s", html)
	}
	page.Complete, page.Preview, page.Quiet, page.Notice = false, true, false, "Sessions read is incomplete. This is not the full inventory."
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	html = string(body)
	if strings.Contains(html, `<span class="attention-count">`) || strings.Contains(html, `<span class="count">`) || !strings.Contains(html, "Fixture preview") || !strings.Contains(html, `<p class="banner" role="status">Sessions read is incomplete.`) {
		t.Fatalf("incomplete read was counted or preview/banner missing: %s", html)
	}
	for _, planned := range []string{`Automations <span class="soon">Planned</span>`, `Watchdog <span class="soon">Planned</span>`, `id="planned"`} {
		if !strings.Contains(html, planned) {
			t.Errorf("planned feature %q is not visibly planned", planned)
		}
	}
}
