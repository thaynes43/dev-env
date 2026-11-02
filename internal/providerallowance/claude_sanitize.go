package providerallowance

import (
	"bytes"
	"encoding/json"
	"time"
)

// SanitizeClaudeStatusline discards every field except the two documented
// subscription windows. It does not retain transcript, workspace or identity
// data. Null/absent windows remain null; they never become zero usage.
func SanitizeClaudeStatusline(raw []byte) ([]byte, error) {
	if _, err := ClaudeStatuslineRead(raw, time.Unix(1, 0)); err != nil {
		return nil, errClaudeStatusline
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(raw, &root) != nil {
		return nil, errClaudeStatusline
	}
	var limits map[string]json.RawMessage
	if v := root["rate_limits"]; len(v) > 0 && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		if json.Unmarshal(v, &limits) != nil {
			return nil, errClaudeStatusline
		}
	}
	windows := map[string]any{}
	for _, name := range []string{"five_hour", "seven_day"} {
		windows[name] = nil
		v := limits[name]
		if len(v) == 0 || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(v, &fields) != nil {
			return nil, errClaudeStatusline
		}
		w := map[string]any{"used_percentage": nil, "resets_at": nil}
		for _, key := range []string{"used_percentage", "resets_at"} {
			if value, ok := fields[key]; ok {
				w[key] = value
			}
		}
		windows[name] = w
	}
	return json.Marshal(map[string]any{"rate_limits": windows})
}

// ValidJSONDocument checks bounded duplicate-free JSON without retaining it.
// It supplies the same parser limits used for provider observations.
func ValidJSONDocument(raw []byte, maxBytes int) bool {
	limits := FixtureParseLimits
	if maxBytes <= 0 || maxBytes > limits.MaxBytes {
		return false
	}
	limits.MaxBytes = maxBytes
	return checkDocument(raw, limits) == nil
}
