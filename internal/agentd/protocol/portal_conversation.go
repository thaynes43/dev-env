package protocol

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"time"
	"unicode"
	"unicode/utf8"
)

// PortalConversation is a bounded projection, never a provider transcript.
// Coverage remains partial unless a caller can prove complete source coverage.
type PortalConversation struct {
	State          string                   `json:"state"`
	ReasonCode     string                   `json:"reasonCode"`
	ConversationID string                   `json:"conversationID,omitempty"`
	OldestSeq      uint64                   `json:"oldestSeq,omitempty"`
	NewestSeq      uint64                   `json:"newestSeq,omitempty"`
	NextCursor     string                   `json:"nextCursor,omitempty"`
	ObservedAt     time.Time                `json:"observedAt"`
	Turns          []PortalConversationTurn `json:"turns,omitempty"`
}

type PortalConversationTurn struct {
	Seq       uint64    `json:"seq"`
	At        time.Time `json:"at"`
	Role      string    `json:"role"`
	From      string    `json:"from,omitempty"`
	Text      string    `json:"text"`
	Truncated bool      `json:"truncated,omitempty"`
}

// Valid reports whether a projection can cross the private transport boundary.
// Callers must separately establish the selected Session and Pod binding.
func (p PortalConversation) Valid() bool {
	if p.ObservedAt.IsZero() || len(p.Turns) > 60 || !portalReasonCode(p.ReasonCode) {
		return false
	}
	switch p.State {
	case "unavailable":
		if p.ReasonCode != "unsupported" && p.ReasonCode != "identity_unconfirmed" && p.ReasonCode != "ordering" && p.ReasonCode != "conflicting_record" {
			return false
		}
		return len(p.Turns) == 0 && p.ConversationID == "" && p.OldestSeq == 0 && p.NewestSeq == 0 && p.NextCursor == ""
	case "partial":
		if p.ReasonCode != "coverage_unverified" && p.ReasonCode != "unrecognized_record" {
			return false
		}
	case "truncated":
		if p.ReasonCode != "source_limit" {
			return false
		}
	case "complete":
		if p.ReasonCode != "source_complete" {
			return false
		}
	default:
		return false
	}
	if !portalConversationID.MatchString(p.ConversationID) || p.ReasonCode == "" || (p.NextCursor != "" && !portalCursorPattern.MatchString(p.NextCursor)) {
		return false
	}
	if len(p.Turns) == 0 {
		return p.OldestSeq == 0 && p.NewestSeq == 0
	}
	if p.OldestSeq != p.Turns[0].Seq || p.NewestSeq != p.Turns[len(p.Turns)-1].Seq {
		return false
	}
	var previous uint64
	total := 0
	for _, t := range p.Turns {
		if t.Seq == 0 || t.Seq <= previous || t.At.IsZero() || len(t.Text) > 4096 || !utf8.ValidString(t.Text) || len(t.From) > 128 {
			return false
		}
		if t.Role != "user" && t.Role != "assistant" && t.Role != "delivered-message" {
			return false
		}
		if t.Role != "delivered-message" && t.From != "" {
			return false
		}
		for _, r := range t.Text {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return false
			}
		}
		total += len(t.Text)
		if total > 96<<10 {
			return false
		}
		previous = t.Seq
	}
	return true
}

var portalConversationID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func portalReasonCode(reason string) bool {
	switch reason {
	case "unsupported", "coverage_unverified", "source_limit", "unrecognized_record", "ordering", "conflicting_record", "identity_unconfirmed", "source_complete":
		return true
	default:
		return false
	}
}

// StrictPortalJSON rejects duplicate fields, trailing data and excessive
// nesting before separate typed decoder passes interpret private results.
func StrictPortalJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if !portalJSONValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}
func portalJSONValue(decoder *json.Decoder, depth int) bool {
	if depth > 16 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return true
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] {
				return false
			}
			seen[key] = true
			if !portalJSONValue(decoder, depth+1) {
				return false
			}
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim('}')
	case '[':
		for decoder.More() {
			if !portalJSONValue(decoder, depth+1) {
				return false
			}
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim(']')
	default:
		return false
	}
}

var portalCursorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{54}$`)

// StrictPortalConversationJSON permits only the exact typed transport keys.
// Provider payloads are not normalized: their unrelated map labels may be
// legitimately case-sensitive and are handled separately.
func StrictPortalConversationJSON(raw []byte) bool {
	if !StrictPortalJSON(raw) {
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || !portalExactKeys(object, map[string]bool{"state": true, "reasonCode": true, "conversationID": true, "oldestSeq": true, "newestSeq": true, "nextCursor": true, "observedAt": true, "turns": true}) {
		return false
	}
	if turns, ok := object["turns"]; ok {
		var entries []json.RawMessage
		if json.Unmarshal(turns, &entries) != nil {
			return false
		}
		for _, entry := range entries {
			var fields map[string]json.RawMessage
			if json.Unmarshal(entry, &fields) != nil || !portalExactKeys(fields, map[string]bool{"seq": true, "at": true, "role": true, "from": true, "text": true, "truncated": true}) {
				return false
			}
		}
	}
	return true
}
func portalExactKeys(values map[string]json.RawMessage, allowed map[string]bool) bool {
	if values == nil {
		return false
	}
	for key := range values {
		if !allowed[key] {
			return false
		}
	}
	return true
}
