// Package delegatedresult defines an inert delegated-result contract. It has no
// routes, production adapters, feature activation, provider calls or persistence.
package delegatedresult

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"strconv"
	"unicode/utf8"
)

const (
	MaxRawBytes    = 400 * 1024
	MaxAnswerBytes = 64 * 1024
)

var (
	ErrInvalid     = errors.New("invalid delegated result")
	ErrDisabled    = errors.New("delegated results disabled")
	ErrUnconfirmed = errors.New("delegated result unconfirmed")
)

type Request struct {
	SchemaVersion int    `json:"schemaVersion"`
	Answer        string `json:"answer"`
}

// DecodeRequest consumes at most MaxRawBytes+1 bytes. The extra byte detects an
// oversized body; it is never decoded. No caller-owned transport error is exposed.
// Transport adapters must supply the actual Content-Type/Content-Encoding and
// impose their own read deadline. No compressed encoding (even identity) is used.
func DecodeRequest(r io.Reader, contentType, contentEncoding string) (Request, error) {
	var out Request
	media, params, err := mime.ParseMediaType(contentType)
	if err != nil || media != "application/json" || len(params) != 0 || contentEncoding != "" || r == nil {
		return out, ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxRawBytes+1))
	if err != nil || !strictJSON(data) {
		return out, ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || string(fields["schemaVersion"]) != "1" {
		return out, ErrInvalid
	}
	answer := bytes.TrimSpace(fields["answer"])
	if len(answer) == 0 || answer[0] != '"' {
		return out, ErrInvalid
	}
	if json.Unmarshal(data, &out) != nil || !validAnswer(out.Answer) {
		return Request{}, ErrInvalid
	}
	return out, nil
}

func validAnswer(s string) bool {
	if len(s) < 1 || len(s) > MaxAnswerBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == 127 || (r < 32 && r != '\t' && r != '\r' && r != '\n') {
			return false
		}
	}
	return true
}

// encoding/json replaces invalid UTF-8 and unpaired UTF-16 escapes. Validate
// both before decoding, retaining legitimate U+FFFD and paired surrogate escapes.
func strictJSON(data []byte) bool {
	if len(data) == 0 || len(data) > MaxRawBytes || !utf8.Valid(data) || !json.Valid(data) {
		return false
	}
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		for i++; i < len(data) && data[i] != '"'; i++ {
			if data[i] != '\\' {
				continue
			}
			i++
			if data[i] != 'u' {
				continue
			}
			n, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
			i += 4
			if n >= 0xdc00 && n <= 0xdfff {
				return false
			}
			if n >= 0xd800 && n <= 0xdbff {
				if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
					return false
				}
				low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return uniqueValue(d, 0) && d.Decode(new(any)) == io.EOF
}

// A small depth cap bounds recursive traversal; this schema needs only 3 levels.
func uniqueValue(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
			if !uniqueValue(d, depth+1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim('}')
	case '[':
		for d.More() {
			if !uniqueValue(d, depth+1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && end == json.Delim(']')
	default:
		return false
	}
}
