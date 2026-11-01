package portal

import (
	"strings"
	"testing"
	"time"
)

func TestCurrentSourceRenderSeparatesRetrievalAndProviderUpdate(t *testing.T) {
	now := time.Date(2026, 1, 8, 12, 5, 0, 0, time.UTC)
	allowance := ProductAllowance{
		Provider: "Codex", Value: AllowanceCurrentSource,
		Windows: []ProductAllowanceWindow{{
			PercentValue: 40, PercentKnown: true, Direction: "used",
			RemainingDerived: true, Window: "7 days",
			RetrievedAt: "2026-01-08T12:00:00Z",
			ResetAt:     "2026-01-15T12:00:00Z", ResetPrecision: "instant", AsOf: now,
			Provenance: "Retained Codex tool report",
		}},
	}
	body, err := RenderProduct(ProductPage{
		Title: "Sessions", Connected: true, Filter: ProductFilter{View: "usage"}, AsOf: now, Allowance: []ProductAllowance{allowance},
		Usage: &ProductUsage{Allowance: []ProductAllowance{allowance}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{
		"Current source", "Stable account identity is unavailable",
		"40% used", "60% remaining (derived)",
		`Retrieved at <time datetime="2026-01-08T12:00:00Z">`,
		"Retrieval does not establish provider update time",
		"Exact observation time not reported", "Window: 7 days",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, forbidden := range []string{"This is a measured provider report", "Observed at <time", "calendar week"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("invented provenance: %s", forbidden)
		}
	}
}
