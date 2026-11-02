package providerallowance

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const sdkUsageFixture = `{"type":"assistant","timestamp":"2099-01-01T00:00:00Z","message":{"content":[{"type":"text","text":"SECRET-MESSAGE"}],"usage":{"input_tokens":999}},"usage_report":{"session":{"total_cost_usd":999,"model_usage":{"SECRET-MODEL":{}}},"rate_limits":{"limits":[{"kind":"session","group":"session","percent":0,"resets_at":"2026-11-03T00:00:00Z","scope":null,"severity":"normal","is_active":true},{"kind":"weekly_scoped","group":"weekly","percent":12.5,"resets_at":null,"scope":{"model":{"display_name":"Model fixture"},"surface":{"display_name":"Surface fixture"}},"severity":"warning","is_active":false}],"extra_usage":{"used_credits":123,"currency":"USD"}}}}`

func TestClaudeSDKUsageSanitizesStructuredRowsWithoutInventingScopeOrTime(t *testing.T) {
	at := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	report, err := SanitizeClaudeUsageReport([]byte(sdkUsageFixture), at)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(report)
	if bytes.Contains(body, []byte("SECRET")) || bytes.Contains(body, []byte("999")) || bytes.Contains(body, []byte("credits")) || bytes.Contains(body, []byte("2099")) {
		t.Fatal("private session data retained")
	}
	if len(report.Limits) != 2 || report.Limits[0].Percent != "0" || report.Limits[1].Percent != "12.5" || report.Limits[1].Scope.Model.DisplayName != "Model fixture" || report.ProviderObservedAt != nil || report.ReceivedAt != at.Format(time.RFC3339Nano) {
		t.Fatal("quota/scope/clock lost")
	}
	obs, err := projectClaudeUsageCapture(body, report.ReceivedAt)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Measures) != 2 || obs.Measures[0].Window.DurationMinutes != nil || obs.Measures[1].Window.DurationMinutes != nil || obs.Measures[1].Window.ResetAt != nil || obs.ProviderObservedAt != nil || obs.Scope.EntitlementRef != nil || obs.Scope.BindingState != BindingUnknown {
		t.Fatal("scope/period/provider clock invented")
	}
	if obs.Measures[0].Category != CategoryIncluded || obs.Measures[1].Category != CategoryAdvisory {
		t.Fatal("scoped zero promoted into global quota")
	}
	if obs.Measures[0].Window.Ref == obs.Measures[1].Window.Ref || len(obs.DirectFields) != 10 {
		t.Fatal("server row identity/metadata collapsed")
	}
	for _, variant := range []struct {
		old, new  string
		nilLimits bool
		count     int
	}{{`"limits":[` + strings.Split(strings.Split(sdkUsageFixture, `"limits":[`)[1], `],"extra_usage"`)[0] + `]`, `"limits":null`, true, 0}, {`"limits":[` + strings.Split(strings.Split(sdkUsageFixture, `"limits":[`)[1], `],"extra_usage"`)[0] + `]`, `"limits":[]`, false, 0}} {
		got, err := SanitizeClaudeUsageReport([]byte(strings.Replace(sdkUsageFixture, variant.old, variant.new, 1)), at)
		if err != nil || (got.Limits == nil) != variant.nilLimits || len(got.Limits) != variant.count {
			t.Fatal("null/no meters conflated", err)
		}
	}
	unavailable, err := SanitizeClaudeUsageReport([]byte(`{"type":"assistant","usage_report":{"session":{},"rate_limits":null}}`), at)
	if err != nil || unavailable.Limits != nil {
		t.Fatal("profile-unavailable report not preserved")
	}
}
func TestClaudeSDKUsageRefusesFalseQuotaAndMissingFields(t *testing.T) {
	at := time.Date(2026, 11, 2, 19, 0, 0, 0, time.UTC)
	for _, raw := range []string{
		strings.Replace(sdkUsageFixture, `"type":"assistant"`, `"type":"result"`, 1),
		strings.Replace(sdkUsageFixture, `"percent":0`, `"percent":"0"`, 1),
		strings.Replace(sdkUsageFixture, `"percent":0`, `"percent":0,"Percent":99`, 1),
		strings.Replace(sdkUsageFixture, `"is_active":true`, `"is_active":true,"Is_active":false`, 1),
		strings.Replace(sdkUsageFixture, `"display_name":"Model fixture"`, `"display_name":"Model fixture","Display_name":"Overwritten"`, 1),
		strings.Replace(sdkUsageFixture, `"percent":0`, `"percent":null`, 1),
		strings.Replace(sdkUsageFixture, `"percent":0,`, "", 1),
		strings.Replace(sdkUsageFixture, `"percent":0`, `"percent":101`, 1),
		strings.Replace(sdkUsageFixture, `"percent":0`, `"percent":0,"percent":99`, 1),
		strings.Replace(sdkUsageFixture, `"is_active":true`, `"is_active":null`, 1),
		strings.Replace(sdkUsageFixture, `"resets_at":"2026-11-03T00:00:00Z"`, `"resets_at":"Nov 3"`, 1),
		strings.Replace(sdkUsageFixture, `"severity":"normal"`, `"severity":"bad\u001b[1m"`, 1),
		`{"type":"assistant","aborted":true,"usage_report":{"rate_limits":null}}`,
		`{"type":"assistant","error":"authentication_failed","usage_report":{"rate_limits":null}}`,
		`{"type":"assistant","usage_report":{"session":{"total_cost_usd":0}}}`,
	} {
		if _, err := SanitizeClaudeUsageReport([]byte(raw), at); err == nil {
			t.Fatal("malformed or session-only quota accepted")
		}
	}
}
