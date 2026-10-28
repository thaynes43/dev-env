package providerallowance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var claudeDecimal = regexp.MustCompile(`^(0|[1-9][0-9]*)(\.[0-9]+)?$`)
var errClaudeStatusline = errors.New("invalid Claude statusline")

// ClaudeStatuslineRead projects only documented subscription windows from an
// untrusted callback. Capture time is not provider observation or freshness.
func ClaudeStatuslineRead(raw []byte, now time.Time) (ReadItem, error) {
	if now.IsZero() || checkDocument(raw, FixtureParseLimits) != nil {
		return ReadItem{}, errClaudeStatusline
	}
	object := func(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, error) {
		var m map[string]json.RawMessage
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &m) != nil || m == nil {
			return nil, errClaudeStatusline
		}
		for k := range m {
			for _, want := range keys {
				if k != want && strings.EqualFold(k, want) {
					return nil, errClaudeStatusline
				}
			}
		}
		return m, nil
	}
	root, err := object(raw, "rate_limits")
	if err != nil {
		return ReadItem{}, errClaudeStatusline
	}
	var limits map[string]json.RawMessage
	if v, ok := root["rate_limits"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
		limits, err = object(v, "five_hour", "seven_day")
		if err != nil {
			return ReadItem{}, errClaudeStatusline
		}
	}
	at := now.UTC().Format(time.RFC3339Nano)
	id := "claude-input-pending"
	obs := Observation{Schema: SchemaObservation, ObservationID: id, ProducerRef: "agent-run-claude-statusline", ProducerIncarnation: id, Sequence: 1, Provider: "anthropic", Source: Source{Kind: SourcePassive, Version: "unknown", CapabilityRef: "claude-statusline:documented-windows"}, Scope: Scope{Products: []string{"claude"}, BindingState: BindingUnknown}, CapturedAt: at, Availability: AvailabilityPartial, Completeness: CompletenessPartial, DirectFields: []DirectField{}, Measures: []Measure{}, Derivations: []Derivation{}, EvidenceRefs: []string{}}
	currents := []CurrentMeasure{}
	for _, spec := range []struct {
		name    string
		minutes Count
	}{{"five_hour", 300}, {"seven_day", 10080}} {
		m := Measure{ID: spec.name, Category: CategoryIncluded, Window: Window{Ref: spec.name, DurationMinutes: &spec.minutes, ResetPrecision: ResetUnknown}, Unit: UnitPercent, Direction: DirectionUsed, Value: MeasureValue{State: ValueUnknown}, EvidenceFields: []string{}}
		if w, ok := limits[spec.name]; ok && !bytes.Equal(bytes.TrimSpace(w), []byte("null")) {
			fields, e := object(w, "used_percentage", "resets_at")
			if e != nil {
				return ReadItem{}, errClaudeStatusline
			}
			if v, ok := fields["used_percentage"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				text := string(v)
				n, ok := new(big.Rat).SetString(text)
				if !claudeDecimal.MatchString(text) || !ok || n.Sign() < 0 || n.Cmp(big.NewRat(100, 1)) > 0 {
					return ReadItem{}, errClaudeStatusline
				}
				m.Value = MeasureValue{State: ValueKnown, Decimal: &text}
			}
			if v, ok := fields["resets_at"]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
				text := string(v)
				n, e := strconv.ParseInt(text, 10, 64)
				if e != nil || n < 0 || n > 253402300799 || strconv.FormatInt(n, 10) != text {
					return ReadItem{}, errClaudeStatusline
				}
				reset := time.Unix(n, 0).UTC().Format(time.RFC3339)
				m.Window.ResetAt = &reset
				m.Window.ResetPrecision = ResetInstant
			}
		}
		state := CurrentUnknown
		reason := "provider freshness and account binding unknown"
		if m.Window.ResetAt != nil {
			reset, _ := time.Parse(time.RFC3339, *m.Window.ResetAt)
			if !reset.After(now) {
				state = CurrentExpired
				reason = "reported reset passed; replenishment unknown"
			}
		}
		obs.Measures = append(obs.Measures, m)
		currents = append(currents, CurrentMeasure{MeasureID: m.ID, State: state, Value: MeasureValue{State: ValueUnknown}, Reason: reason})
	}
	identity, _ := json.Marshal(obs.Measures)
	sum := sha256.Sum256(append(identity, []byte(at)...))
	obs.ObservationID = fmt.Sprintf("claude-input-%x", sum[:12])
	obs.ProducerIncarnation = obs.ObservationID
	item := ReadItem{Observation: obs, ReceivedAt: at, Validation: ValidationRecord{State: ValidationUnverified, ValidationRefs: []string{}}, Binding: BindingRecord{State: BindingUnknown}, Freshness: Freshness{State: FreshUnknown, AsOf: at, Basis: BasisNone, Reason: "local callback does not establish provider freshness"}, CurrentMeasures: currents}
	if !ValidateObservation(obs, TrustedContext{}, nil).ShapeOK {
		return ReadItem{}, errClaudeStatusline
	}
	return item, nil
}
