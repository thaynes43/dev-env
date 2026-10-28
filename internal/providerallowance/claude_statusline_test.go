package providerallowance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestClaudeStatuslineProjection(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{"cwd":"SECRET-CANARY","cost":{"total_cost_usd":123},"rate_limits":{"five_hour":{"used_percentage":0,"resets_at":1800000000},"seven_day":{"used_percentage":71.5,"resets_at":1000000000}}}`)
	item, err := ClaudeStatuslineRead(raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Observation.Measures) != 2 || *item.Observation.Measures[0].Value.Decimal != "0" || *item.Observation.Measures[1].Value.Decimal != "71.5" || *item.Observation.Measures[0].Window.ResetAt != time.Unix(1800000000, 0).UTC().Format(time.RFC3339) || item.CurrentMeasures[0].State != CurrentUnknown || item.CurrentMeasures[1].State != CurrentExpired || item.Observation.ProviderObservedAt != nil || item.Freshness.State != FreshUnknown || item.Binding.State != BindingUnknown {
		t.Fatal("incorrect projection")
	}
	out, _ := json.Marshal(item)
	if strings.Contains(string(out), "SECRET-CANARY") || strings.Contains(string(out), "total_cost") {
		t.Fatal("private field leaked")
	}
	later, err := ClaudeStatuslineRead(raw, now.Add(time.Minute))
	if err != nil || later.Freshness.State != FreshUnknown || later.CurrentMeasures[0].State != CurrentUnknown {
		t.Fatal("replay freshened")
	}
	empty, err := ClaudeStatuslineRead([]byte(`{"rate_limits":{"five_hour":null,"seven_day":{"used_percentage":null,"resets_at":null}}}`), now)
	if err != nil || empty.Observation.Measures[0].Value.State != ValueUnknown || empty.Observation.Measures[1].Value.State != ValueUnknown {
		t.Fatal("missing windows")
	}
}
func TestClaudeStatuslineRejectsMalformed(t *testing.T) {
	for _, raw := range []string{`{"rate_limits":{"five_hour":{"used_percentage":101}}}`, `{"rate_limits":{"five_hour":{"used_percentage":true}}}`, `{"rate_limits":{"five_hour":{"resets_at":1.2}}}`, `{"rate_limits":{"five_hour":{"resets_at":999999999999999999999}}}`, `{"rate_limits":{"five_hour":[],"five_hour":{}}}`, `{"rate_limits":{"five_hour":{"uſed_percentage":1}}}`, `{"Rate_limits":{}}`, "{", strings.Repeat("x", FixtureParseLimits.MaxBytes+1), string([]byte{0xff})} {
		if _, err := ClaudeStatuslineRead([]byte(raw), time.Now()); err == nil {
			t.Fatalf("accepted %q", raw[:min(len(raw), 80)])
		}
	}
}
