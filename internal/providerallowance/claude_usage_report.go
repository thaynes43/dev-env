package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"regexp"
	"strconv"
	"time"
	"unicode"
)

const (
	ClaudeSDKCaptureSchema = "claude.sdk.usage-capture.v1"
	ClaudeSDKSourceVersion = "claude-sdk-usage-report-2026.11.02"
)

var ErrClaudeUsageReport = errors.New("claude usage report unavailable")
var claudeMeterKind = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ClaudeUsageCapture discards session costs, tokens, contents and account data.
// Limits nil means unavailable; an empty slice means the server reported none.
// Scope and ordering are retained; no model or surface is treated as an alias.
type ClaudeUsageCapture struct {
	Schema             string             `json:"schema"`
	ReceivedAt         string             `json:"receivedAt"`
	ProviderObservedAt *string            `json:"providerObservedAt"`
	Limits             []ClaudeUsageLimit `json:"limits"`
}
type ClaudeUsageLimit struct {
	Kind     string            `json:"kind"`
	Group    string            `json:"group"`
	Percent  json.Number       `json:"percent"`
	ResetsAt *string           `json:"resets_at"`
	Scope    *ClaudeUsageScope `json:"scope"`
	Severity string            `json:"severity"`
	IsActive bool              `json:"is_active"`
}
type ClaudeUsageScope struct {
	Model   *ClaudeUsageLabel `json:"model"`
	Surface *ClaudeUsageLabel `json:"surface"`
}
type ClaudeUsageLabel struct {
	DisplayName string `json:"display_name"`
}

// SanitizeClaudeUsageReport accepts one documented SDK assistant envelope.
// The envelope's display timestamp is never used as a provider data clock.
func SanitizeClaudeUsageReport(raw []byte, received time.Time) (ClaudeUsageCapture, error) {
	fail := func() (ClaudeUsageCapture, error) { return ClaudeUsageCapture{}, ErrClaudeUsageReport }
	if received.IsZero() || checkDocument(raw, FixtureParseLimits) != nil {
		return fail()
	}
	var event struct {
		Type    string          `json:"type"`
		Error   json.RawMessage `json:"error"`
		Aborted bool            `json:"aborted"`
		Report  json.RawMessage `json:"usage_report"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != "assistant" || event.Aborted || len(event.Error) > 0 && !bytes.Equal(event.Error, []byte("null")) || len(event.Report) == 0 || bytes.Equal(event.Report, []byte("null")) {
		return fail()
	}
	var report map[string]json.RawMessage
	if json.Unmarshal(event.Report, &report) != nil || report == nil {
		return fail()
	}
	rate, ok := report["rate_limits"]
	if !ok {
		return fail()
	}
	capture := ClaudeUsageCapture{Schema: ClaudeSDKCaptureSchema, ReceivedAt: received.UTC().Format(time.RFC3339Nano)}
	if bytes.Equal(bytes.TrimSpace(rate), []byte("null")) {
		return capture, nil
	}
	var limits map[string]json.RawMessage
	if json.Unmarshal(rate, &limits) != nil || limits == nil {
		return fail()
	}
	rows, ok := limits["limits"]
	if !ok {
		return fail()
	}
	if json.Unmarshal(rows, &capture.Limits) != nil || len(capture.Limits) > 16 {
		return fail()
	}
	// Presence/type validation before struct decoding avoids missing numbers or
	// booleans being silently changed into zero/false by encoding/json.
	var rawRows []json.RawMessage
	if !bytes.Equal(bytes.TrimSpace(rows), []byte("null")) && !jsonArray(rows) {
		return fail()
	}
	_ = json.Unmarshal(rows, &rawRows)
	for i, row := range rawRows {
		var fields map[string]json.RawMessage
		if json.Unmarshal(row, &fields) != nil || fields == nil {
			return fail()
		}
		for _, key := range []string{"kind", "group", "percent", "resets_at", "severity", "is_active"} {
			v, ok := fields[key]
			if !ok || key != "resets_at" && bytes.Equal(v, []byte("null")) {
				return fail()
			}
		}
		if !claudeDecimal.Match(fields["percent"]) {
			return fail()
		}
		active := string(fields["is_active"])
		if active != "true" && active != "false" {
			return fail()
		}
		if !validClaudeUsageLimit(capture.Limits[i]) {
			return fail()
		}
	}
	return capture, nil
}
func validClaudeUsageLimit(row ClaudeUsageLimit) bool {
	n, ok := new(big.Rat).SetString(string(row.Percent))
	if !claudeMeterKind.MatchString(row.Kind) || !claudeMeterKind.MatchString(row.Group) || !claudeDecimal.MatchString(string(row.Percent)) || !ok || n.Sign() < 0 || n.Cmp(big.NewRat(100, 1)) > 0 || !safeUsageLabel(row.Severity, 32) {
		return false
	}
	if row.ResetsAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *row.ResetsAt); err != nil {
			return false
		}
	}
	if row.Scope != nil {
		for _, label := range []*ClaudeUsageLabel{row.Scope.Model, row.Scope.Surface} {
			if label != nil && !safeUsageLabel(label.DisplayName, 128) {
				return false
			}
		}
	}
	return true
}
func safeUsageLabel(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func projectClaudeUsageCapture(raw []byte, captured string) (Observation, error) {
	var report ClaudeUsageCapture
	if checkDocument(raw, FixtureParseLimits) != nil || !ExactObjectKeys(raw, []string{"schema", "receivedAt", "providerObservedAt", "limits"}) || strictDecode(raw, &report) != nil || report.Schema != ClaudeSDKCaptureSchema || report.ReceivedAt != captured || report.ProviderObservedAt != nil || len(report.Limits) > 16 {
		return Observation{}, ErrClaudeUsageReport
	}
	var root map[string]json.RawMessage
	_ = json.Unmarshal(raw, &root)
	var rows []json.RawMessage
	_ = json.Unmarshal(root["limits"], &rows)
	obs := Observation{Schema: SchemaObservation, Provider: "anthropic", CapturedAt: captured, Source: Source{Kind: SourceNativeRPC, Version: ClaudeSDKSourceVersion, CapabilityRef: "claude-sdk:usage-report"}, Scope: Scope{Products: []string{"claude"}, BindingState: BindingUnknown}, Availability: AvailabilityPartial, Completeness: CompletenessPartial, DirectFields: []DirectField{}, Measures: []Measure{}, Derivations: []Derivation{}, EvidenceRefs: []string{}}
	if report.Limits == nil {
		obs.Availability = AvailabilityUnavailable
		return obs, nil
	}
	for i, row := range report.Limits {
		var rawFields map[string]json.RawMessage
		_ = json.Unmarshal(rows[i], &rawFields)
		activeRaw := string(rawFields["is_active"])
		if !claudeDecimal.Match(rawFields["percent"]) || (activeRaw != "true" && activeRaw != "false") {
			return Observation{}, ErrClaudeUsageReport
		}
		if !ExactObjectKeys(rows[i], []string{"kind", "group", "percent", "resets_at", "scope", "severity", "is_active"}) || !validClaudeUsageLimit(row) {
			return Observation{}, ErrClaudeUsageReport
		}
		id := "claude-sdk-" + strconv.Itoa(i)
		// Index and exact server kind identify a row; no scope, group or display
		// label is inferred to be the same pool as another row or provider.
		window := Window{Ref: id + "-" + row.Kind, ResetAt: row.ResetsAt, ResetPrecision: ResetUnknown}
		if row.ResetsAt != nil {
			window.ResetPrecision = ResetInstant
		}
		fields := []DirectField{}
		for _, meta := range []struct{ name, value string }{{"kind", row.Kind}, {"group", row.Group}, {"severity", row.Severity}} {
			value := meta.value
			fields = append(fields, DirectField{Field: id + "-" + meta.name, OriginalType: TypeString, LexicalValue: &value})
		}
		active := strconv.FormatBool(row.IsActive)
		fields = append(fields, DirectField{Field: id + "-active", OriginalType: TypeBoolean, LexicalValue: &active})
		if row.Scope != nil {
			for _, meta := range []struct {
				name  string
				value *ClaudeUsageLabel
			}{{"model", row.Scope.Model}, {"surface", row.Scope.Surface}} {
				if meta.value != nil {
					value := meta.value.DisplayName
					fields = append(fields, DirectField{Field: id + "-" + meta.name, OriginalType: TypeString, LexicalValue: &value})
				}
			}
		}
		evidence := []string{}
		for _, f := range fields {
			obs.DirectFields = append(obs.DirectFields, f)
			evidence = append(evidence, f.Field)
		}
		percent := string(row.Percent)
		obs.Measures = append(obs.Measures, Measure{ID: id, Category: CategoryIncluded, Window: window, Unit: UnitPercent, Direction: DirectionUsed, Value: MeasureValue{State: ValueKnown, Decimal: &percent}, ValidationRef: stringRef("claude-sdk:" + id + "-used-percent"), EvidenceFields: evidence})
	}
	return obs, nil
}
