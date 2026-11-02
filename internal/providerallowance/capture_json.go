package providerallowance

import (
	"encoding/json"
	"strings"
)

// ValidJSONDocument checks bounded duplicate-free JSON without retaining it.
func ValidJSONDocument(raw []byte, maxBytes int) bool {
	limits := FixtureParseLimits
	if maxBytes <= 0 || maxBytes > limits.MaxBytes {
		return false
	}
	limits.MaxBytes = maxBytes
	return checkDocument(raw, limits) == nil
}

// CanonicalFieldNames refuses case-folded aliases of every selected JSON key.
// Unknown additive fields may be ignored, but cannot overwrite a known value
// through encoding/json's case-insensitive struct matching. Missing keys are
// validated separately by the particular source contract.
func CanonicalFieldNames(raw []byte, selected ...string) bool {
	if len(raw) == 0 || len(raw) > FixtureParseLimits.MaxBytes {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for key := range fields {
		for _, known := range selected {
			if key != known && strings.EqualFold(key, known) {
				return false
			}
		}
	}
	return true
}
