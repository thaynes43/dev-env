package providerallowance_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransitionFixturesRemainB1Specifications(t *testing.T) {
	var doc struct {
		Status    string `json:"status"`
		Scenarios []struct {
			ID       string `json:"id"`
			Input    string `json:"input"`
			Expected string `json:"expected"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(loadJSON(t, "transition-fixtures.json"), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Status, "NOT executed") {
		t.Fatal(doc.Status)
	}
	want := []string{
		"last-good-failed-attempt", "passed-reset", "sparse-event", "exact-replay",
		"conflicting-replay", "out-of-order", "source-order-unknown", "account-change",
		"shared-pool", "legacy-and-bucket", "reset-credit-details", "restart",
		"read-scope", "retention-and-page", "provider-outage", "ui-outage", "denied-source",
	}
	if len(doc.Scenarios) != len(want) {
		t.Fatalf("scenarios = %d", len(doc.Scenarios))
	}
	for i, scenario := range doc.Scenarios {
		if scenario.ID != want[i] || scenario.Input == "" || scenario.Expected == "" {
			t.Fatalf("%d = %#v", i, scenario)
		}
	}
}
