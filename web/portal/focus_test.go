package portal

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestViewSkipTargetsAndMainRegionsHaveVisibleFocus(t *testing.T) {
	product := func(page ProductPage) func() ([]byte, error) {
		return func() ([]byte, error) { return RenderProduct(page) }
	}
	for _, view := range []struct {
		name   string
		render func() ([]byte, error)
	}{
		{"agents", product(ProductPage{Title: "Sessions", Connected: true, Complete: true})},
		{"tasks", product(ProductPage{Title: "Tasks", Connected: true, Complete: true})},
		{"usage", product(ProductPage{Title: "Sessions", Filter: ProductFilter{View: "usage"}, Usage: &ProductUsage{}, Connected: true, Complete: true})},
		{"read-failure", product(ProductPage{Title: "Sessions", ReadFailure: &ProductReadFailure{ReasonCode: "operator_unreachable"}})},
		{"receipt", func() ([]byte, error) { return RenderReceipt(ProductReceipt{TransportStatus: "Delivered"}) }},
		{"launch-result", func() ([]byte, error) { return RenderLaunchResult(ProductLaunchResult{Outcome: "unavailable"}) }},
		{"pages", func() ([]byte, error) {
			return RenderPages(PagesWorkspace{Controls: DisconnectedPagesControls(nil)}, 1<<20)
		}},
		{"asks", func() ([]byte, error) {
			body, _, err := RenderAsks(AsksPage{}, 1<<20)
			return body, err
		}},
	} {
		t.Run(view.name, func(t *testing.T) {
			body, err := view.render()
			if err != nil {
				t.Fatal(err)
			}
			doc, err := html.Parse(bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			styles := pagesTestNodes(doc, func(n *html.Node) bool { return n.Data == "style" })
			if len(styles) != 1 {
				t.Fatal("view must carry one embedded stylesheet")
			}
			style := pagesTestText(styles[0])
			// Check the rendered bytes so a CSS change cannot silently break CSP.
			digest := sha256.Sum256([]byte(style))
			hash := "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
			policies := pagesTestNodes(doc, func(n *html.Node) bool {
				return n.Data == "meta" && pagesTestAttr(n, "http-equiv") == "Content-Security-Policy"
			})
			if len(policies) != 1 || !strings.Contains(pagesTestAttr(policies[0], "content"), "style-src "+hash+";") {
				t.Fatal("CSP does not allow the rendered stylesheet")
			}
			rules := focusTestRules(style)
			skips := pagesTestNodes(doc, func(n *html.Node) bool {
				return n.Data == "a" && strings.Contains(" "+pagesTestAttr(n, "class")+" ", " skip ")
			})
			if len(skips) != 1 {
				t.Fatal("view must carry one skip link")
			}
			href := pagesTestAttr(skips[0], "href")
			if !strings.HasPrefix(href, "#") || len(href) < 2 {
				t.Fatalf("skip link must point to a region in this view: %q", href)
			}
			targets := pagesTestNodes(doc, func(n *html.Node) bool { return pagesTestAttr(n, "id") == href[1:] })
			if len(targets) != 1 {
				t.Fatalf("skip link target %s must exist exactly once", href)
			}
			mains := pagesTestNodes(doc, func(n *html.Node) bool { return n.Data == "main" })
			if len(mains) != 1 {
				t.Fatal("view must carry one main region")
			}
			for _, region := range append(targets, mains...) {
				if pagesTestAttr(region, "tabindex") != "-1" {
					t.Errorf("region #%s cannot receive skip-link focus", pagesTestAttr(region, "id"))
				}
				var declarations string
				for _, class := range strings.Fields(pagesTestAttr(region, "class")) {
					declarations += rules["."+class+":focus"] + rules["."+class+":focus-visible"]
				}
				focusTestOutline(t, declarations, "-3px")
			}
			for _, selector := range []string{"a:focus-visible", "button:focus-visible", "summary:focus-visible"} {
				focusTestOutline(t, rules[selector], "3px")
			}
		})
	}
}

// Only flat selector/declaration blocks are needed for the focus rules. Match
// their braces independently of the surrounding light/dark and width media rules.
func focusTestRules(style string) map[string]string {
	rules := map[string]string{}
	for _, block := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(style, -1) {
		for _, selector := range strings.Split(block[1], ",") {
			rules[strings.TrimSpace(selector)] += block[2] + ";"
		}
	}
	return rules
}

func focusTestOutline(t *testing.T, declarations, offset string) {
	t.Helper()
	properties := map[string]string{}
	for _, declaration := range strings.Split(declarations, ";") {
		if key, value, ok := strings.Cut(declaration, ":"); ok {
			properties[strings.TrimSpace(key)] = strings.Join(strings.Fields(value), " ")
		}
	}
	if properties["outline"] != "3px solid var(--focus)" || properties["outline-offset"] != offset {
		t.Errorf("focus must use the shared outline with offset %s; got %q", offset, declarations)
	}
}

func TestSharedFocusRulesDoNotNameViewSpecificClasses(t *testing.T) {
	for selector := range focusTestRules(viewsStyle) {
		if strings.Contains(selector, ":focus") && (strings.Contains(selector, ".pages-") || strings.Contains(selector, ".asks-")) {
			t.Errorf("view-specific focus rule belongs in its own stylesheet: %s", selector)
		}
	}
}
