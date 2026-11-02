//go:build linux

package usagecollector

import (
	"strings"
	"testing"
	"time"
)

func TestClaudeBuiltinRequiresQuotaAndExplicitZeroTurnSuccess(t *testing.T) {
	quota := `{"type":"assistant","usage_report":{"rate_limits":{"limits":[{"kind":"weekly_all","group":"weekly","percent":5,"resets_at":null,"scope":null,"severity":"normal","is_active":true}]}}}`
	result := `{"type":"result","subtype":"success","is_error":false,"num_turns":0,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`
	stream := quota + "\n" + result + "\n"
	at := time.Now()
	got, err := readClaudeBuiltin(strings.NewReader(stream), func() time.Time { return at })
	if err != nil || len(got.Limits) != 1 || got.ReceivedAt != at.UTC().Format(time.RFC3339Nano) {
		t.Fatal("valid builtin", err)
	}
	for _, bad := range []string{quota + "\n", result + "\n", stream + quota + "\n", strings.Replace(stream, `"num_turns":0`, `"num_turns":1`, 1), strings.Replace(stream, `"input_tokens":0`, `"input_tokens":1e-400`, 1), strings.Replace(stream, `"cache_read_input_tokens":0`, `"cache_read_input_tokens":null`, 1), strings.Replace(stream, `"is_error":false`, `"is_error":true`, 1), strings.Replace(stream, `"total_cost_usd":0`, `"total_cost_usd":null`, 1)} {
		if _, err = readClaudeBuiltin(strings.NewReader(bad), func() time.Time { return at }); err == nil {
			t.Fatal("unqualified model/builtin result accepted")
		}
	}
}
