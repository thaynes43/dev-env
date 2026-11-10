package portal

import (
	"regexp"
	"strings"
	"testing"
)

// OP192 S1 (research R-07): the portal renders a fixed Grok row after Claude,
// Codex and Cursor. It is only Not connected or Unknown, never draws a value,
// and the Cursor and Grok sections say their shared pool is unknown.

const (
	op192GrokMissing  = "No Grok account view is connected. Nothing here is estimated."
	op192PoolRelation = "Grok used in Cursor counts toward Cursor&#39;s plan usage. Whether Cursor and Grok share one pool is unknown."
)

// op192Section returns one provider's account section on the Usage page.
func op192Section(t *testing.T, section, provider string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<section class="allowance-account"><h3>` + provider + `(?: |</h3>).*?</section>`).FindString(section)
	if m == "" {
		t.Fatalf("no %s account section in %s", provider, section)
	}
	return m
}

func TestOP192RenderFillsGrokRowNotConnected(t *testing.T) {
	// Older session code passes three rows; the renderer still shows Grok.
	three := []ProductAllowance{{Provider: "Claude", Value: AllowanceNotConnected}, {Provider: "Codex", Value: AllowanceNotConnected}, {Provider: "Cursor", Value: AllowanceNotConnected}}
	page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Allowance: three, Usage: &ProductUsage{Allowance: three},
		Groups: []ProductGroup{{Title: "In progress", Rows: []ProductRow{{ID: "s1", Title: "synthetic-agent"}}}}}
	body, err := RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	strip := allowanceBlock(t, body, false)
	names := regexp.MustCompile(`<span class="allowance-name">([^<]+)</span>`).FindAllStringSubmatch(strip, -1)
	if len(names) != 4 || names[3][1] != "Grok" || strings.Count(strip, "Not connected") != 4 || !strings.Contains(strip, op192GrokMissing) || strings.Contains(strip, "%") {
		t.Fatalf("strip: %s", strip)
	}
	page.Filter.View = "usage"
	body, err = RenderProduct(page)
	if err != nil {
		t.Fatal(err)
	}
	section := allowanceBlock(t, body, true)
	grok := op192Section(t, section, "Grok")
	if !strings.Contains(grok, "Not connected") || !strings.Contains(grok, op192GrokMissing) || !strings.Contains(grok, op192PoolRelation) || strings.Contains(grok, "%") || strings.Contains(grok, "meter") {
		t.Fatalf("grok section: %s", grok)
	}
	if cursor := op192Section(t, section, "Cursor"); !strings.Contains(cursor, op192PoolRelation) {
		t.Fatalf("cursor section lacks the pool relation: %s", cursor)
	}
	for _, provider := range []string{"Claude", "Codex"} {
		if strings.Contains(op192Section(t, section, provider), op192PoolRelation) {
			t.Fatalf("%s carries the Cursor/Grok relation", provider)
		}
	}
	order := regexp.MustCompile(`<h3 class="glance-name">([^<]+)</h3>`).FindAllStringSubmatch(glanceSection(t, string(body)), -1)
	if len(order) != 4 || order[3][1] != "Grok" {
		t.Fatalf("glance order %v", order)
	}
}

func TestOP192RenderNeverDrawsAGrokValue(t *testing.T) {
	window := ProductAllowanceWindow{Percent: "41.7% used", Window: "7 days", Reset: "2 Nov 14:00 UTC", Observed: "29 Oct 13:59 UTC", Provenance: "Provider API", PercentKnown: true, PercentValue: 41.7, Direction: "used"}
	last := ProductAllowanceLastReported{Percent: "41.7% used", Window: "7 days", ReadAt: "29 Oct 13:59 UTC", ReadAtExact: "2026-10-29T13:59:00Z", Source: "Provider API", Account: "Account verified", Why: "synthetic"}
	for _, value := range []ProductAllowanceState{AllowanceMeasured, AllowanceStale, AllowanceCurrentSource, AllowanceUnknown} {
		t.Run(string(value), func(t *testing.T) {
			grok := ProductAllowance{Provider: "Grok", Value: value, Reason: "Synthetic", Windows: []ProductAllowanceWindow{window}, LastReported: []ProductAllowanceLastReported{last}}
			rows := allowanceViews([]ProductAllowance{grok})
			if len(rows) != 4 || rows[3].Provider != "Grok" {
				t.Fatalf("rows %+v", rows)
			}
			got := rows[3]
			if got.Value != AllowanceUnknown || got.Reason != "No qualified Grok source" || len(got.Windows) != 0 || len(got.LastReported) != 0 || got.LastReportedMore != 0 {
				t.Fatalf("grok row drew a value: %+v", got)
			}
			page := ProductPage{Title: "Sessions", Connected: true, Complete: true, Filter: ProductFilter{View: "usage"}, Usage: &ProductUsage{Allowance: []ProductAllowance{grok}}}
			body, err := RenderProduct(page)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "41.7") {
				t.Fatal("a Grok value reached the page")
			}
		})
	}
}
