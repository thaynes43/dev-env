package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const gatusCoverage = "Current endpoint-check snapshot, not notification, page or delivery history. Retention completeness, incident duration and recurrence are Unknown. Source and observation time are caller supplied."
const gatusMaxResults = 4000

type GatusSnapshot struct {
	Coverage     string          `json:"coverage"`
	Availability string          `json:"availability"`
	ObservedAt   string          `json:"observedAt"`
	Source       string          `json:"source"`
	Endpoints    []GatusEndpoint `json:"endpoints"`
}
type GatusEndpoint struct {
	Key    string     `json:"key"`
	Name   string     `json:"name"`
	Group  string     `json:"group"`
	Latest GatusCheck `json:"latest"`
}
type GatusCheck struct {
	Timestamp     string `json:"timestamp"`
	Success       *bool  `json:"success"`
	Status        *int64 `json:"status"`
	DurationNanos *int64 `json:"durationNanos"`
}

func UnavailableGatus() GatusSnapshot {
	return GatusSnapshot{Coverage: gatusCoverage, Availability: "Unavailable", ObservedAt: "Unknown", Source: "Unknown", Endpoints: []GatusEndpoint{}}
}

// ParseGatus accepts only a caller-supplied endpoint status array. Neither the
// caller's capture time nor its source reference authenticates provenance.
func ParseGatus(raw []byte, observed time.Time, source string) (GatusSnapshot, error) {
	fail := func() (GatusSnapshot, error) {
		return UnavailableGatus(), errors.New("invalid or over-limit Gatus snapshot")
	}
	if len(raw) > AlertmanagerMaxBytes || !utf8.Valid(raw) || observed.IsZero() || len(source) > 512 || strings.TrimSpace(source) == "" {
		return fail()
	}
	source = gatusText(source)
	if strings.TrimSpace(source) == "" {
		return fail()
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	value, err := decodeGatusValue(dec, 0)
	if err != nil || dec.Decode(new(any)) != io.EOF || !boundedAlertValue(value, 0) {
		return fail()
	}
	records, ok := value.([]any)
	if !ok || len(records) > 1000 {
		return fail()
	}
	out := GatusSnapshot{Coverage: gatusCoverage, Availability: "Available", ObservedAt: observed.UTC().Format(time.RFC3339Nano), Source: source, Endpoints: make([]GatusEndpoint, 0, len(records))}
	seen := map[string]bool{}
	total := 0
	for _, record := range records {
		item, ok := record.(map[string]any)
		if !ok || !exactGatusFields(item, []string{"key", "name", "group", "results"}) {
			return fail()
		}
		key, ok := item["key"].(string)
		if !ok || key == "" || strings.TrimSpace(key) != key || cleanAlertText(key) != key || strings.Contains(key, "://") || RedactText(key) != key || seen[key] {
			return fail()
		}
		seen[key] = true
		endpoint := GatusEndpoint{Key: key, Name: "Unknown", Group: "Unknown"}
		for _, field := range []string{"name", "group"} {
			if v, present := item[field]; present {
				text, ok := v.(string)
				if !ok {
					return fail()
				}
				if field == "name" {
					endpoint.Name = gatusText(text)
				} else {
					endpoint.Group = gatusText(text)
				}
			}
		}
		if v, present := item["results"]; present && v != nil {
			results, ok := v.([]any)
			if !ok {
				return fail()
			}
			total += len(results)
			if total > gatusMaxResults {
				return fail()
			}
			var latest time.Time
			seenLatest := false
			byTime := map[time.Time]map[string]any{}
			for _, r := range results {
				result, ok := r.(map[string]any)
				if !ok || !exactGatusFields(result, []string{"success", "timestamp", "status", "duration"}) {
					return fail()
				}
				stamp, ok := result["timestamp"].(string)
				if !ok || stamp == "" {
					return fail()
				}
				when, err := time.Parse(time.RFC3339Nano, stamp)
				if err != nil {
					return fail()
				}
				when = when.UTC()
				success, ok := result["success"].(bool)
				if !ok {
					return fail()
				}
				status, ok := optionalGatusInt(result, "status", 999)
				if !ok {
					return fail()
				}
				duration, ok := optionalGatusInt(result, "duration", int64(^uint64(0)>>1))
				if !ok {
					return fail()
				}
				comparable := make(map[string]any, len(result)-1)
				for k, v := range result {
					if k != "timestamp" {
						comparable[k] = v
					}
				}
				if previous, found := byTime[when]; found && !reflect.DeepEqual(previous, comparable) {
					return fail()
				}
				byTime[when] = comparable
				if !seenLatest || when.After(latest) {
					seenLatest = true
					latest = when
					endpoint.Latest = GatusCheck{Timestamp: when.Format(time.RFC3339Nano), Success: &success, Status: status, DurationNanos: duration}
				}
			}
		}
		out.Endpoints = append(out.Endpoints, endpoint)
	}
	sort.Slice(out.Endpoints, func(i, j int) bool { return out.Endpoints[i].Key < out.Endpoints[j].Key })
	return out, nil
}
func gatusText(s string) string {
	return RedactText(urlInText.ReplaceAllString(cleanAlertText(s), Redacted))
}
func optionalGatusInt(m map[string]any, key string, max int64) (*int64, bool) {
	v, present := m[key]
	if !present {
		return nil, true
	}
	number, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	n, err := number.Int64()
	if err != nil || n < 0 || n > max {
		return nil, false
	}
	return &n, true
}
func exactGatusFields(m map[string]any, fields []string) bool {
	for key := range m {
		for _, canonical := range fields {
			if strings.EqualFold(key, canonical) && key != canonical {
				return false
			}
		}
	}
	return true
}

// decodeGatusValue preserves exact member names and rejects duplicates before
// any type projection. Unknown fields are bounded and discarded deliberately.
func decodeGatusValue(d *json.Decoder, depth int) (any, error) {
	if depth > 16 {
		return nil, errors.New("depth")
	}
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		obj := map[string]any{}
		for d.More() {
			k, e := d.Token()
			if e != nil {
				return nil, e
			}
			key, ok := k.(string)
			if !ok {
				return nil, errors.New("key")
			}
			if _, exists := obj[key]; exists {
				return nil, errors.New("duplicate")
			}
			v, e := decodeGatusValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			obj[key] = v
			if len(obj) > 1000 {
				return nil, errors.New("object limit")
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim('}') {
			return nil, errors.New("object")
		}
		return obj, nil
	case '[':
		arr := make([]any, 0)
		for d.More() {
			v, e := decodeGatusValue(d, depth+1)
			if e != nil {
				return nil, e
			}
			arr = append(arr, v)
			if len(arr) > 4000 {
				return nil, errors.New("list limit")
			}
		}
		end, e := d.Token()
		if e != nil || end != json.Delim(']') {
			return nil, errors.New("array")
		}
		return arr, nil
	}
	return nil, errors.New("delimiter")
}
