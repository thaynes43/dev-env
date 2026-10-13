package main

import (
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// Reject duplicate keys, excessive nesting, trailing JSON and malformed UTF-8.
// Unknown PV fields may be present in memory but never reach any report/error.
func strictObject(b []byte) (map[string]any, bool) {
	if !utf8.Valid(b) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	value, ok := strictValue(d, 0)
	if !ok {
		return nil, false
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, false
	}
	return obj, true
}

func strictValue(d *json.Decoder, depth int) (any, bool) {
	if depth > 32 {
		return nil, false
	}
	t, err := d.Token()
	if err != nil {
		return nil, false
	}
	if delimiter, ok := t.(json.Delim); ok {
		switch delimiter {
		case '{':
			obj := map[string]any{}
			for d.More() {
				t, err := d.Token()
				key, ok := t.(string)
				if err != nil || !ok {
					return nil, false
				}
				if _, found := obj[key]; found {
					return nil, false
				}
				v, ok := strictValue(d, depth+1)
				if !ok {
					return nil, false
				}
				obj[key] = v
			}
			end, err := d.Token()
			return obj, err == nil && end == json.Delim('}')
		case '[':
			values := []any{}
			for d.More() {
				v, ok := strictValue(d, depth+1)
				if !ok {
					return nil, false
				}
				values = append(values, v)
			}
			end, err := d.Token()
			return values, err == nil && end == json.Delim(']')
		}
		return nil, false
	}
	return t, true
}
