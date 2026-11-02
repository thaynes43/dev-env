package providerallowance

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const CaptureBundleSchema = "providerallowance.captures.v1"

const (
	ClaudeCaptureSourceVersion    = "claude-statusline-sanitized-2026.11.02"
	ClaudeCaptureValidationPrefix = "claude-statusline:"
	CodexCaptureSourceVersion     = "codex-usage-limits-sanitized-2026.11.02"
	CodexCaptureCapability        = "codex-native:get-usage-limits"
	CodexCaptureMeasure           = "codex-primary-used"
	CodexCaptureValidation        = "codex-native:primary-used-percent"
)

var ErrCaptureBundle = errors.New("provider_capture_bundle_refused")

type captureEntry struct {
	Provider            string          `json:"provider"`
	ProducerRef         string          `json:"producerRef"`
	ProducerIncarnation string          `json:"producerIncarnation"`
	Sequence            int64           `json:"sequence"`
	CapturedAt          string          `json:"capturedAt"`
	ReceivedAt          string          `json:"receivedAt"`
	Payload             json.RawMessage `json:"payload"`
}

// AssembleCaptureBundle normalizes explicitly supplied captures. It performs
// no acquisition, IO, provider call, schedule or publication. Trust is a
// separate caller-owned selection, never inferred from provider payloads.
func AssembleCaptureBundle(raw []byte, trust AggregateTrust, now time.Time) (ReadResponse, error) {
	fail := func() (ReadResponse, error) { return ReadResponse{}, ErrCaptureBundle }
	if now.IsZero() || checkDocument(raw, FixtureParseLimits) != nil || !ExactObjectKeys(raw, []string{"schema", "captures"}) || trust.MaxAge < 1 || trust.MaxAge > 1800 {
		return fail()
	}
	var bundle struct {
		Schema   string            `json:"schema"`
		Captures []json.RawMessage `json:"captures"`
	}
	if strictDecode(raw, &bundle) != nil || bundle.Schema != CaptureBundleSchema || bundle.Captures == nil || len(bundle.Captures) > 6 {
		return fail()
	}
	contexts := map[string]TrustedContext{}
	for _, ctx := range trust.Contexts {
		if _, ok := contexts[ctx.Provider]; ok {
			return fail()
		}
		contexts[ctx.Provider] = ctx
	}
	base := now.UTC().Format(time.RFC3339Nano)
	c := &Collection{bounds: Bounds{MaxAge: time.Duration(trust.MaxAge) * time.Second, Parse: FixtureParseLimits}, reg: trust.Registry, baseline: base}
	counts := map[string]int{}
	last := map[string]captureEntry{}
	for _, entryRaw := range bundle.Captures {
		if !ExactObjectKeys(entryRaw, []string{"provider", "producerRef", "producerIncarnation", "sequence", "capturedAt", "receivedAt", "payload"}) {
			return fail()
		}
		var e captureEntry
		if strictDecode(entryRaw, &e) != nil || !collectionToken.MatchString(e.ProducerRef) || !collectionToken.MatchString(e.ProducerIncarnation) || e.Sequence < 1 || e.Sequence > 1_000_000 {
			return fail()
		}
		counts[e.Provider]++
		if counts[e.Provider] > 2 {
			return fail()
		}
		captured, err := time.Parse(time.RFC3339Nano, e.CapturedAt)
		if err != nil {
			return fail()
		}
		received, err := time.Parse(time.RFC3339Nano, e.ReceivedAt)
		if err != nil || captured.After(received) || received.After(now) {
			return fail()
		}
		if received.Before(mustCaptureTime(base)) {
			base = e.ReceivedAt
			c.baseline = base
		}
		selected, has := contexts[e.Provider]
		obs, err := projectCapture(e, selected, has, received)
		if err != nil {
			return fail()
		}
		spec := SourceSpec{Provider: e.Provider, ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation, BindingState: BindingUnknown, SourceKind: obs.Source.Kind, SourceVersion: obs.Source.Version, AccountRef: cloneString(obs.Scope.AccountRef), WorkspaceRef: cloneString(obs.Scope.WorkspaceRef)}
		if has {
			if selected.ProducerRef != e.ProducerRef || selected.ProducerIncarnation != e.ProducerIncarnation || selected.SourceKind != obs.Source.Kind || selected.SourceVersion != obs.Source.Version {
				return fail()
			}
			spec.BindingState, spec.BindingGeneration = selected.BindingState, selected.BindingGeneration
			spec.AccountRef, spec.WorkspaceRef = cloneString(selected.AccountRef), cloneString(selected.WorkspaceRef)
		}
		if _, err := validateSourceSpecs([]SourceSpec{spec}, 1); err != nil {
			return fail()
		}
		slot := slotByIdentity(c.slots, e.Provider, e.ProducerRef)
		if prev, ok := last[e.Provider]; ok {
			if e.Sequence > prev.Sequence && (!captured.After(mustCaptureTime(prev.CapturedAt)) || !received.After(mustCaptureTime(prev.ReceivedAt))) {
				return fail()
			}
			if prev.ProducerRef != e.ProducerRef || prev.ProducerIncarnation != e.ProducerIncarnation || e.Sequence < prev.Sequence || captured.Before(mustCaptureTime(prev.CapturedAt)) || received.Before(mustCaptureTime(prev.ReceivedAt)) {
				return fail()
			}
		}
		if slot == nil {
			c.slots = append(c.slots, collectionSlot{spec: spec})
			slot = &c.slots[len(c.slots)-1]
		}
		priorHold := slot.hold
		c.accept(slot, obs, captured, received)
		if slot.hold != nil && slot.hold != priorHold {
			// Preserve the original receipt spelling as well as its instant.
			// An exact replay keeps the first receipt, including across offsets.
			slot.hold.receivedAt = e.ReceivedAt
		}
		last[e.Provider] = e
	}
	doc := c.view().build(now.UTC().Format(time.RFC3339Nano), now)
	encoded, err := json.Marshal(doc)
	if err != nil {
		return fail()
	}
	if _, err := DecodeReadResponse(encoded, FixtureParseLimits); err != nil {
		return fail()
	}
	return doc, nil
}

func mustCaptureTime(v string) time.Time { t, _ := time.Parse(time.RFC3339Nano, v); return t }

func projectCapture(e captureEntry, selected TrustedContext, has bool, received time.Time) (Observation, error) {
	var obs Observation
	switch e.Provider {
	case "cursor":
		s := CursorDisplaySelection{ProducerRef: e.ProducerRef, ProducerIncarnation: e.ProducerIncarnation, Sequence: e.Sequence, BindingState: BindingUnknown, MaxAge: 30 * time.Minute}
		if has {
			s.BindingState = selected.BindingState
			s.BindingGeneration = selected.BindingGeneration
			s.AccountRef = cloneString(selected.AccountRef)
			s.WorkspaceRef = cloneString(selected.WorkspaceRef)
		}
		item, err := CursorDisplayProject(e.Payload, s, received)
		if err != nil || item.Observation.CapturedAt != e.CapturedAt {
			return obs, ErrCaptureBundle
		}
		obs = item.Observation
	case "anthropic":
		var schema struct {
			Schema string `json:"schema"`
		}
		if json.Unmarshal(e.Payload, &schema) != nil {
			return obs, ErrCaptureBundle
		}
		if schema.Schema == ClaudeSDKCaptureSchema {
			var err error
			obs, err = projectClaudeUsageCapture(e.Payload, e.CapturedAt)
			if err != nil {
				return obs, ErrCaptureBundle
			}
			break
		}
		if !sanitizedClaudeCapture(e.Payload) {
			return obs, ErrCaptureBundle
		}
		item, err := ClaudeStatuslineRead(e.Payload, mustCaptureTime(e.CapturedAt))
		if err != nil {
			return obs, ErrCaptureBundle
		}
		obs = item.Observation
		obs.CapturedAt = e.CapturedAt
		obs.Source.Version = ClaudeCaptureSourceVersion
		for i := range obs.Measures {
			obs.Measures[i].ValidationRef = stringRef(ClaudeCaptureValidationPrefix + obs.Measures[i].ID + "-used-percent")
		}
	case "openai":
		var schema struct {
			Schema string `json:"schema"`
		}
		if json.Unmarshal(e.Payload, &schema) != nil {
			return obs, ErrCaptureBundle
		}
		if schema.Schema == CodexRPCSchema {
			var err error
			obs, err = projectCodexAccountCapture(e.Payload, e.CapturedAt)
			if err != nil {
				return obs, ErrCaptureBundle
			}
			if has && (!stringPtrEqual(obs.Scope.AccountRef, selected.AccountRef) || selected.WorkspaceRef != nil) {
				return obs, ErrCaptureBundle
			}
			break
		}
		report, err := DecodeCurrentSource(e.Payload)
		if err != nil || report.RetrievedAt != e.CapturedAt || report.ProviderUpdatedAt != nil || ValidateCurrentSource(report, CurrentSourceSelection{PublisherRef: report.PublisherRef, ProducerIncarnation: report.ProducerIncarnation, SourceGeneration: report.SourceGeneration, MaxAge: 24 * time.Hour}, mustCaptureTime(e.CapturedAt)) != nil {
			return obs, ErrCaptureBundle
		}
		window := Count(report.WindowDurationMinutes)
		obs = Observation{Schema: SchemaObservation, Provider: "openai", CapturedAt: e.CapturedAt, Availability: AvailabilityPartial, Completeness: CompletenessPartial,
			Source: Source{Kind: SourceNativeRPC, Version: CodexCaptureSourceVersion, CapabilityRef: CodexCaptureCapability}, Scope: Scope{Products: []string{"codex"}, BindingState: BindingUnknown},
			DirectFields: []DirectField{}, EvidenceRefs: []string{}, Measures: []Measure{{ID: CodexCaptureMeasure, Category: CategoryIncluded, Window: Window{Ref: "codex-primary", DurationMinutes: &window, ResetAt: &report.ResetsAt, ResetPrecision: ResetInstant}, Unit: UnitPercent, Direction: DirectionUsed, Value: MeasureValue{State: ValueKnown, Decimal: &report.UsedPercent}, ValidationRef: stringRef(CodexCaptureValidation), EvidenceFields: []string{}}},
			Derivations: []Derivation{{ID: "codex-primary-remaining", Formula: FormulaComplement, InputRefs: []string{CodexCaptureMeasure}, Unit: UnitPercent, Precision: "exact", Value: MeasureValue{State: ValueKnown, Decimal: &report.RemainingPercent}}}}
	default:
		return obs, ErrCaptureBundle
	}
	obs.ProducerRef, obs.ProducerIncarnation, obs.Sequence = e.ProducerRef, e.ProducerIncarnation, Count(e.Sequence)
	obs.ProviderObservedAt = nil
	if has {
		obs.BindingGeneration = Count(selected.BindingGeneration)
		obs.Scope.BindingState = selected.BindingState
		obs.Scope.AccountRef, obs.Scope.WorkspaceRef = cloneString(selected.AccountRef), cloneString(selected.WorkspaceRef)
	}
	// Stable ID follows the canonical normalized capture, never the serving clock.
	obs.ObservationID = "capture-pending"
	identity, err := json.Marshal(obs)
	if err != nil {
		return Observation{}, ErrCaptureBundle
	}
	obs.ObservationID = fmt.Sprintf("capture-%x", sha256.Sum256(identity))
	return obs, nil
}

func stringRef(s string) *string { return &s }

func sanitizedClaudeCapture(raw []byte) bool {
	if !ExactObjectKeys(raw, []string{"rate_limits"}) {
		return false
	}
	var root struct {
		Limits json.RawMessage `json:"rate_limits"`
	}
	if strictDecode(raw, &root) != nil || !ExactObjectKeys(root.Limits, []string{"five_hour", "seven_day"}) {
		return false
	}
	var windows map[string]json.RawMessage
	if json.Unmarshal(root.Limits, &windows) != nil {
		return false
	}
	for _, window := range windows {
		if string(window) != "null" && !ExactObjectKeys(window, []string{"used_percentage", "resets_at"}) {
			return false
		}
	}
	return true
}
