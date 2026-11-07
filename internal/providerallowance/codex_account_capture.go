package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"time"
)

const (
	CodexRPCSchema        = "codex.app-server.account-capture.v1"
	CodexRPCSourceVersion = "codex-app-server-sanitized-2026.11.02"
	CodexRPCCapability    = "codex-app-server:account-rate-limits-read"
)

var ErrCodexRPCReport = errors.New("codex account capture unavailable")
var codexAccountHash = regexp.MustCompile(`^openai-email-sha256:[a-f0-9]{64}$`)

const codexOrdinaryUsageField = "ordinaryUsageAllowed"

var codexAccountCaptureKeys = []string{"schema", "accountRef", "identityBasis", "planType", "receivedAt", "providerObservedAt", "primary", "secondary"}
var codexAccountCaptureExtendedKeys = []string{"schema", "accountRef", "identityBasis", "planType", "receivedAt", "providerObservedAt", "primary", "secondary", "ordinaryUsageAllowed"}

// CodexAccountCapture is the sanitized result of a successful official quota
// RPC bracketed by matching personal account reads. ReceivedAt is collection
// time; ProviderObservedAt is unavailable. OrdinaryUsageAllowed is the optional
// top-level scalar: omitted when the source omitted it, JSON null when the
// provider had no boolean, and JSON true or false only for those booleans.
// Null is not false. False means ordinary included usage was reported
// unavailable at that read. True is not execution permission, recovery, paid
// capacity, or a provider clock. This document does not create trust.
type CodexAccountCapture struct {
	Schema               string          `json:"schema"`
	AccountRef           string          `json:"accountRef"`
	IdentityBasis        string          `json:"identityBasis"`
	PlanType             string          `json:"planType"`
	ReceivedAt           string          `json:"receivedAt"`
	ProviderObservedAt   *string         `json:"providerObservedAt"`
	Primary              *CodexWindow    `json:"primary"`
	Secondary            *CodexWindow    `json:"secondary"`
	OrdinaryUsageAllowed json.RawMessage `json:"ordinaryUsageAllowed,omitempty"`
}
type CodexWindow struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int64 `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

// UnmarshalJSON requires an actual integer usedPercent. Missing/null is not zero.
func (w *CodexWindow) UnmarshalJSON(raw []byte) error {
	if !CanonicalFieldNames(raw, "usedPercent", "windowDurationMins", "resetsAt") {
		return ErrCodexRPCReport
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return ErrCodexRPCReport
	}
	value, ok := fields["usedPercent"]
	if !ok {
		return ErrCodexRPCReport
	}
	used, err := strconv.Atoi(string(value))
	if err != nil || used < 0 || used > 100 {
		return ErrCodexRPCReport
	}
	parsed := CodexWindow{UsedPercent: used}
	if value, ok = fields["windowDurationMins"]; ok {
		if json.Unmarshal(value, &parsed.WindowDurationMins) != nil {
			return ErrCodexRPCReport
		}
	}
	if value, ok = fields["resetsAt"]; ok {
		if json.Unmarshal(value, &parsed.ResetsAt) != nil {
			return ErrCodexRPCReport
		}
	}
	*w = parsed
	return nil
}

func DecodeCodexAccountCapture(raw []byte) (CodexAccountCapture, error) {
	var report CodexAccountCapture
	legacy := ExactObjectKeys(raw, codexAccountCaptureKeys)
	extended := ExactObjectKeys(raw, codexAccountCaptureExtendedKeys)
	if checkDocument(raw, FixtureParseLimits) != nil || (!legacy && !extended) || strictDecode(raw, &report) != nil {
		return report, ErrCodexRPCReport
	}
	usage, err := CanonicalCodexOrdinaryUsage(report.OrdinaryUsageAllowed)
	if err != nil {
		return CodexAccountCapture{}, ErrCodexRPCReport
	}
	report.OrdinaryUsageAllowed = usage
	if report.Schema != CodexRPCSchema || !codexAccountHash.MatchString(report.AccountRef) || report.IdentityBasis != "provider_email_sha256" || report.ProviderObservedAt != nil || !PersonalCodexPlan(report.PlanType) {
		return report, ErrCodexRPCReport
	}
	if _, err := time.Parse(time.RFC3339Nano, report.ReceivedAt); err != nil {
		return report, ErrCodexRPCReport
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, name := range []string{"primary", "secondary"} {
		if string(fields[name]) != "null" && !ExactObjectKeys(fields[name], []string{"usedPercent", "windowDurationMins", "resetsAt"}) {
			return report, ErrCodexRPCReport
		}
	}
	for _, w := range []*CodexWindow{report.Primary, report.Secondary} {
		if w == nil {
			continue
		}
		if w.WindowDurationMins != nil && (*w.WindowDurationMins < 1 || *w.WindowDurationMins > 525600) || w.ResetsAt != nil && (*w.ResetsAt < 0 || *w.ResetsAt > 253402300799) {
			return report, ErrCodexRPCReport
		}
	}
	return report, nil
}

// PersonalCodexPlan enumerates the documented personal plans. Organization
// plans require independent workspace qualification and are not account-only.
func PersonalCodexPlan(plan string) bool {
	switch plan {
	case "free", "go", "plus", "pro", "prolite", "promax":
		return true
	default:
		return false
	}
}

func projectCodexAccountCapture(raw []byte, captured string) (Observation, error) {
	report, err := DecodeCodexAccountCapture(raw)
	if err != nil || report.ReceivedAt != captured {
		return Observation{}, ErrCodexRPCReport
	}
	obs := Observation{Schema: SchemaObservation, Provider: "openai", CapturedAt: captured, Source: Source{Kind: SourceNativeRPC, Version: CodexRPCSourceVersion, CapabilityRef: CodexRPCCapability}, Scope: Scope{AccountRef: &report.AccountRef, Products: []string{"codex"}, BindingState: BindingUnknown}, Availability: AvailabilityPartial, Completeness: CompletenessPartial, DirectFields: []DirectField{}, EvidenceRefs: []string{}, Measures: []Measure{}, Derivations: []Derivation{}}
	for _, window := range []struct {
		name string
		data *CodexWindow
	}{{"primary", report.Primary}, {"secondary", report.Secondary}} {
		m := Measure{ID: "codex-" + window.name + "-used", Category: CategoryIncluded, Unit: UnitPercent, Direction: DirectionUsed, Window: Window{Ref: "codex-" + window.name, ResetPrecision: ResetUnknown}, Value: MeasureValue{State: ValueUnknown}, ValidationRef: stringRef("codex-app-server:" + window.name + "-used-percent"), EvidenceFields: []string{}}
		if w := window.data; w != nil {
			used := strconv.Itoa(w.UsedPercent)
			m.Value = MeasureValue{State: ValueKnown, Decimal: &used}
			if w.WindowDurationMins != nil {
				duration := Count(*w.WindowDurationMins)
				m.Window.DurationMinutes = &duration
			}
			if w.ResetsAt != nil {
				at := time.Unix(*w.ResetsAt, 0).UTC().Format(time.RFC3339)
				m.Window.ResetAt = &at
				m.Window.ResetPrecision = ResetInstant
			}
			remaining := strconv.Itoa(100 - w.UsedPercent)
			obs.Derivations = append(obs.Derivations, Derivation{ID: "codex-" + window.name + "-remaining", Formula: FormulaComplement, InputRefs: []string{m.ID}, Unit: UnitPercent, Precision: "exact", Value: MeasureValue{State: ValueKnown, Decimal: &remaining}})
		}
		obs.Measures = append(obs.Measures, m)
	}
	return obs, nil
}

// CanonicalCodexOrdinaryUsage canonicalizes one ordinaryUsageAllowed JSON
// scalar. Empty raw is an omitted field. JSON null stays null. A JSON boolean
// stays true or false. Strings, numbers, objects, and arrays are refused, so
// null and 0 are never coerced to false or true.
func CanonicalCodexOrdinaryUsage(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, ErrCodexRPCReport
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrCodexRPCReport
	}
	switch tok {
	case nil:
		return json.RawMessage("null"), nil
	case true:
		return json.RawMessage("true"), nil
	case false:
		return json.RawMessage("false"), nil
	default:
		return nil, ErrCodexRPCReport
	}
}

// projectCodexOrdinaryUsage appends ordinaryUsageAllowed only when reg has
// one exact openai/native_rpc/CodexRPCSourceVersion/CodexRPCCapability rule
// whose ordinaryUsageAllowed field is boolean signal, and the capture has an
// explicit JSON boolean. Missing and null omit the normalized field, so the
// legacy observation and its ID stay the same. TrustedContext is not consulted.
// An absent, wrong, or duplicate rule does not inject the field or relax
// ValidateObservation. The boolean authorizes no execution and proves no recovery.
func projectCodexOrdinaryUsage(obs Observation, payload []byte, reg Registry) Observation {
	if !codexOrdinaryUsageQualified(reg) || obs.Provider != "openai" || obs.Source.Kind != SourceNativeRPC || obs.Source.Version != CodexRPCSourceVersion || obs.Source.CapabilityRef != CodexRPCCapability {
		return obs
	}
	report, err := DecodeCodexAccountCapture(payload)
	if err != nil {
		return obs
	}
	value, ok := codexOrdinaryUsageBool(report.OrdinaryUsageAllowed)
	if !ok {
		return obs
	}
	lexical := "false"
	if value {
		lexical = "true"
	}
	obs.DirectFields = append(obs.DirectFields, DirectField{Field: codexOrdinaryUsageField, OriginalType: TypeBoolean, LexicalValue: stringRef(lexical)})
	return obs
}

func codexOrdinaryUsageQualified(reg Registry) bool {
	parts := snapshotRegistry(reg)
	var found SourceRule
	matched := 0
	for _, src := range parts.Sources {
		if src.Provider != "openai" || src.Kind != SourceNativeRPC || src.Version != CodexRPCSourceVersion {
			continue
		}
		matched++
		found = src
	}
	if matched != 1 || found.CapabilityRef != CodexRPCCapability || found.DirectFields == nil {
		return false
	}
	rule, ok := found.DirectFields[codexOrdinaryUsageField]
	return ok && rule.OriginalType == TypeBoolean && rule.Class == ClassSignal
}

func codexOrdinaryUsageBool(raw json.RawMessage) (bool, bool) {
	switch string(raw) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// A successful official RPC can be recently checked while its underlying
// provider-data timestamp remains unknown. Neither field replaces the other.
func captureCollectionFreshness(obs Observation, received string, maxAge time.Duration, now time.Time) *Freshness {
	if !accountScopeObservation(obs) {
		return nil
	}
	fresh := &Freshness{State: FreshUnknown, AsOf: obs.CapturedAt, Basis: BasisCapture, Reason: "local successful quota read; provider data time unknown"}
	if obs.Provider == "anthropic" {
		fresh.Reason = "local official usage report reception; provider data time unknown"
	}
	if obs.Provider == "cursor" {
		fresh.Reason = "local CLI display acquisition interval; provider data time unknown"
	}
	captured, err := time.Parse(time.RFC3339Nano, obs.CapturedAt)
	receipt, e := time.Parse(time.RFC3339Nano, received)
	if err != nil || e != nil || now.IsZero() || maxAge <= 0 || captured.After(receipt) || receipt.After(now) {
		return fresh
	}
	fresh.State = FreshFresh
	if now.Sub(captured) > maxAge {
		fresh.State = FreshStale
	}
	return fresh
}
