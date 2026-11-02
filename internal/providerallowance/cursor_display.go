package providerallowance

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// CursorDisplayInputSchema is the closed sanitized capture this adapter reads.
	// The projected document is providerallowance.v1 inside providerallowance.read.v1.
	CursorDisplayInputSchema = "cursor.native_display.sanitized.v1"
	// CursorDisplaySourceVersion is the display source version these projections use.
	// It is not the historical display-only version 2026.10.22-27dcf5f.
	CursorDisplaySourceVersion = "cursor-native-display-sanitized-2026.11.02"
	// CursorDisplayCapability is the Cursor Plan & Usage display capability.
	CursorDisplayCapability = "cursor-native-display:plan-usage"
	// CursorDisplayXAICapability marks a direct xAI reading as outside the Cursor pool.
	CursorDisplayXAICapability = "cursor-native-display:xai-direct-unknown"
	// CursorDisplayIncludedWindow is the included-quota window ref. A weekly label
	// does not fill its duration.
	CursorDisplayIncludedWindow = "cursor-included-display"
	// CursorDisplaySpendWindow is the additional-credit spend window. It is not quota.
	CursorDisplaySpendWindow = "cursor-additional-credits"
	// CursorDisplayIncludedMeasure is the used-percent measure id.
	CursorDisplayIncludedMeasure = "cursor-included-used"
	// CursorDisplaySpendMeasure is the additional-credit measure id.
	CursorDisplaySpendMeasure = "cursor-additional-credits"
	// CursorDisplayRemainingDerivation is the declared complement of the used percent.
	CursorDisplayRemainingDerivation = "cursor-included-remaining"
	// CursorDisplayIncludedValidation is the caller-registry ref for the used percent.
	CursorDisplayIncludedValidation = "cursor-native-display:included-used-percent"
	// CursorDisplaySpendValidation is the caller-registry ref for additional credits.
	CursorDisplaySpendValidation = "cursor-native-display:additional-credits"
	// CursorDisplayAcquisitionEnabled reports that recurring acquisition is off.
	CursorDisplayAcquisitionEnabled = false
)

const (
	cursorDisplayMaxBytes       = 4096
	cursorDisplayMaxString      = 256
	cursorDisplayMaxItems       = 8
	cursorDisplayMaxMinutes     = 525600
	cursorDisplayMaxSequence    = 1_000_000
	cursorDisplayRouteCursor    = "cursor_plan_usage"
	cursorDisplayRouteXAI       = "xai_direct"
	cursorDisplayXAICode        = "direct_xai_unknown"
	cursorDisplayWeeklyLabel    = "weekly"
	cursorDisplayReasonCapture  = "native display capture is not a provider refresh"
	cursorDisplayReasonStale    = "native display capture is older than the selected maximum age"
	cursorDisplayReasonMarked   = "native display capture was marked stale"
	cursorDisplayReasonXAI      = "direct xAI is not the Cursor shared entitlement"
	cursorDisplayEvidenceCursor = "cursor-native-display:capture"
	cursorDisplayEvidenceXAI    = "cursor-native-display:xai-direct-unknown"
)

// CursorDisplayResetExtension is the deferred window and direct-field change
// that would let a calendar date or the exact month/day label survive the
// portal projection. It is not applied here. Window.ResetAt stays nil.
const CursorDisplayResetExtension = "Window needs optional resetDisplayDate and resetDisplayText strings, and those values are mutually exclusive. canonicalKeys must treat both as absent-tolerant so existing windows still decode. A non-null resetDisplayDate is a YYYY-MM-DD calendar date, accepted only when resetPrecision is display_only and resetAt is null. Do not expand it to an RFC3339 midnight instant. A non-null resetDisplayText is the exact English month and day label. The capture has no known year or timezone, so the adapter must not invent a year or a midnight. Date-only ISO does not solve the yearless sample. web/portal/session projectMeasure must render the exact text or the calendar date with a display-only suffix and leave ResetAt empty. Until that ACK, this adapter stores the original label in directFields.resetDisplayDate or directFields.resetDisplayText, sets resetAt null and resetPrecision display_only. projectMeasure reads ResetAt only, so the portal reset stays Unknown."

// CursorDisplayRegistryExtension is the caller-trusted registry addition required
// before these measures validate. Payload product names do not create a pool.
const CursorDisplayRegistryExtension = "Do not reuse cursor native_display version 2026.10.22-27dcf5f. That source sets AllowNormalizedMeasures false and rejects these measures as display promotion. Add a caller-trusted SourceRule provider cursor, kind native_display, version cursor-native-display-sanitized-2026.11.02, capability cursor-native-display:plan-usage, AllowNormalizedMeasures true, AllowDerivations true, and string display direct fields plan, windowLabel, resetDisplayDate, resetDisplayText, and accountLabel. resetDisplayText is the exact yearless month and day label, not an ISO date. Add historical-only metric rules: included used percent on window cursor-included-display with complement bound 100, and USD api_spend on window cursor-additional-credits. EntitlementRule remains caller-owned. A payload product list cannot establish the shared pool."

// Fixed redacted errors. None of them contain captured bytes.
var (
	ErrCursorDisplayJSON                = errors.New("cursor_display_json")
	ErrCursorDisplayDuplicate           = errors.New("cursor_display_duplicate")
	ErrCursorDisplayUnknown             = errors.New("cursor_display_unknown_field")
	ErrCursorDisplayMissing             = errors.New("cursor_display_missing_field")
	ErrCursorDisplayType                = errors.New("cursor_display_type")
	ErrCursorDisplayCredential          = errors.New("cursor_display_credential")
	ErrCursorDisplayLimit               = errors.New("cursor_display_limit")
	ErrCursorDisplayTime                = errors.New("cursor_display_time")
	ErrCursorDisplayFuture              = errors.New("cursor_display_future_capture")
	ErrCursorDisplayPercent             = errors.New("cursor_display_percent")
	ErrCursorDisplayReset               = errors.New("cursor_display_reset")
	ErrCursorDisplaySpend               = errors.New("cursor_display_spend")
	ErrCursorDisplaySelection           = errors.New("cursor_display_selection")
	ErrCursorDisplayBinding             = errors.New("cursor_display_binding")
	ErrCursorDisplayConflict            = errors.New("cursor_display_conflict")
	ErrCursorDisplayAcquisitionDisabled = errors.New("cursor_display_acquisition_disabled")
)

var cursorDisplayDecimal = regexp.MustCompile(`^(0|[1-9][0-9]{0,6})(\.[0-9]{1,6})?$`)

var cursorDisplayKeys = []string{
	"schema", "capturedAt", "route", "product", "plan", "accountRef",
	"availability", "errorCode", "windowLabel", "durationMinutes",
	"usedPercent", "resetDisplayDate", "resetDisplayText", "spendAmount", "spendCurrency",
}

// CursorDisplaySelection is the caller-supplied producer, capture sequence, and binding.
// It is not read from the capture. Sequence is the positive capture sequence owned by
// the caller for one producer incarnation. A reread keeps that incarnation and does not
// read a sequence from the provider payload. Binding stays unknown unless the caller
// sets BindingVerified and supplies the account and workspace. EntitlementRef is copied
// only from this selection, never from a payload product.
type CursorDisplaySelection struct {
	ProducerRef         string
	ProducerIncarnation string
	Sequence            int64
	BindingGeneration   int64
	BindingState        BindingState
	AccountRef          *string
	WorkspaceRef        *string
	EntitlementRef      *string
	MaxAge              time.Duration
}

// CursorDisplayAcquisition is the recurring acquisition boundary.
// It has no clock, network, credential, or schedule fields. Run is disabled.
type CursorDisplayAcquisition struct{}

// Run refuses acquisition. It does not read a provider, a credential, or a timer.
func (CursorDisplayAcquisition) Run() error {
	return ErrCursorDisplayAcquisitionDisabled
}

type cursorDisplayInput struct {
	schema        string
	capturedAt    string
	route         string
	product       string
	plan          *string
	accountRef    *string
	availability  string
	errorCode     *string
	windowLabel   *string
	duration      *int64
	used          *string
	reset         *string
	resetText     *string
	spendAmount   *string
	spendCurrency *string
	stale         bool
	staleReason   string
}

// CursorDisplayProject maps one sanitized native-display capture into a read item.
// Capture time stays the original spelling. ProviderObservedAt stays nil.
// A date-only reset or an exact month/day label is a direct field with ResetAt
// nil and display_only precision. The provider observation time stays unknown.
// now is the local receipt clock, not a provider refresh.
func CursorDisplayProject(raw []byte, selected CursorDisplaySelection, now time.Time) (ReadItem, error) {
	if err := selected.validate(); err != nil {
		return ReadItem{}, err
	}
	if now.IsZero() {
		return ReadItem{}, ErrCursorDisplayTime
	}
	in, err := decodeCursorDisplay(raw)
	if err != nil {
		return ReadItem{}, err
	}
	if err := in.finish(selected, now); err != nil {
		return ReadItem{}, err
	}
	item, err := in.item(selected, now.UTC())
	if err != nil {
		return ReadItem{}, err
	}
	encoded, err := json.Marshal(item.Observation)
	if err != nil {
		return ReadItem{}, ErrCursorDisplayType
	}
	decoded, err := DecodeObservation(encoded, FixtureParseLimits)
	if err != nil || !cursorDisplaySameObservation(item.Observation, decoded) {
		return ReadItem{}, ErrCursorDisplayType
	}
	return item, nil
}

// CursorDisplayDedupe keeps the original receipt of each source capture.
// The same observation id with a different body is a conflict. A later
// receipt of that same capture is not a conflict when only derived freshness
// has aged. The retained item is the earlier receipt, unchanged: its identity
// and freshness are not rewritten to the later clock. Different accounts,
// windows, and products are not merged.
func CursorDisplayDedupe(items []ReadItem) ([]ReadItem, error) {
	if len(items) == 0 || len(items) > cursorDisplayMaxItems {
		return nil, ErrCursorDisplayLimit
	}
	out := make([]ReadItem, 0, len(items))
	index := make(map[string]int, len(items))
	for _, item := range items {
		if err := cursorDisplayOwnItem(item); err != nil {
			return nil, err
		}
		id := item.Observation.ObservationID
		if pos, ok := index[id]; ok {
			if !cursorDisplaySameCapture(out[pos], item) {
				return nil, ErrCursorDisplayConflict
			}
			if cursorDisplayEarlierReceipt(item, out[pos]) {
				out[pos] = item
			}
			continue
		}
		index[id] = len(out)
		out = append(out, item)
	}
	return out, nil
}

// CursorDisplaySnapshot dedupes items into one providerallowance.read.v1 document.
// It does not schedule a reread and does not contact a provider.
func CursorDisplaySnapshot(items []ReadItem, now time.Time) (ReadResponse, error) {
	if now.IsZero() {
		return ReadResponse{}, ErrCursorDisplayTime
	}
	kept, err := CursorDisplayDedupe(items)
	if err != nil {
		return ReadResponse{}, err
	}
	response := ReadResponse{
		Schema:           SchemaRead,
		SnapshotRevision: cursorDisplayRevision(kept),
		ServedAt:         now.UTC().Format(time.RFC3339Nano),
		Items:            kept,
		SourceStatuses:   cursorDisplayStatuses(kept),
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return ReadResponse{}, ErrCursorDisplayType
	}
	if _, err := DecodeReadResponse(encoded, FixtureParseLimits); err != nil {
		return ReadResponse{}, ErrCursorDisplayType
	}
	return response, nil
}

// CursorDisplayProviderRefresh reports whether two readings are independent
// provider refreshes. Two native-display projections are not, including two
// reads of the same screen and a replay with a later receipt time.
func CursorDisplayProviderRefresh(a, b ReadItem) bool {
	if a.Observation.Source.Kind == SourceNativeDisplay || b.Observation.Source.Kind == SourceNativeDisplay {
		return false
	}
	if a.Observation.ProviderObservedAt == nil || b.Observation.ProviderObservedAt == nil {
		return false
	}
	return false
}

// CursorDisplayPool is the existing shared-pool count. Payload products do not
// create an entitlement, and this function does not add window values.
func CursorDisplayPool(observations []Observation, trusted []TrustedContext, reg Registry) PoolAssessment {
	return SharedPools(observations, trusted, reg)
}

// CursorDisplayCallerSource is the caller-asserted source identity.
// It is not read from a capture, an observation, or a payload.
type CursorDisplayCallerSource struct {
	Provider string
	Kind     SourceKind
	Version  string
}

// CursorDisplayTrustedContext copies caller-supplied selection and source
// identity into trust. It does not read an observation, a binding record,
// or a payload, and it does not establish a shared entitlement.
func CursorDisplayTrustedContext(selected CursorDisplaySelection, source CursorDisplayCallerSource) TrustedContext {
	return TrustedContext{
		ProducerRef:         selected.ProducerRef,
		ProducerIncarnation: selected.ProducerIncarnation,
		BindingGeneration:   selected.BindingGeneration,
		AccountRef:          cloneString(selected.AccountRef),
		WorkspaceRef:        cloneString(selected.WorkspaceRef),
		BindingState:        selected.BindingState,
		Provider:            source.Provider,
		SourceKind:          source.Kind,
		SourceVersion:       source.Version,
	}
}

func (s CursorDisplaySelection) validate() error {
	if !cursorDisplayToken(s.ProducerRef) || !cursorDisplayToken(s.ProducerIncarnation) {
		return ErrCursorDisplaySelection
	}
	if s.Sequence < 1 || s.Sequence > cursorDisplayMaxSequence {
		return ErrCursorDisplaySelection
	}
	if s.MaxAge <= 0 || s.MaxAge > 24*time.Hour {
		return ErrCursorDisplaySelection
	}
	if s.BindingGeneration < 0 || s.BindingGeneration > 1_000_000 {
		return ErrCursorDisplaySelection
	}
	switch s.BindingState {
	case BindingUnknown:
		if s.BindingGeneration != 0 || s.AccountRef != nil || s.WorkspaceRef != nil || s.EntitlementRef != nil {
			return ErrCursorDisplayBinding
		}
	case BindingVerified:
		if s.AccountRef == nil || s.WorkspaceRef == nil || !cursorDisplayToken(*s.AccountRef) || !cursorDisplayToken(*s.WorkspaceRef) {
			return ErrCursorDisplayBinding
		}
		if s.EntitlementRef != nil && !cursorDisplayToken(*s.EntitlementRef) {
			return ErrCursorDisplayBinding
		}
	default:
		return ErrCursorDisplayBinding
	}
	return nil
}

func decodeCursorDisplay(raw []byte) (cursorDisplayInput, error) {
	if len(raw) == 0 || bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(raw) {
		return cursorDisplayInput{}, ErrCursorDisplayJSON
	}
	if len(raw) > cursorDisplayMaxBytes {
		return cursorDisplayInput{}, ErrCursorDisplayLimit
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return cursorDisplayInput{}, ErrCursorDisplayJSON
	}
	var in cursorDisplayInput
	seen := make(map[string]struct{}, len(cursorDisplayKeys))
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return cursorDisplayInput{}, ErrCursorDisplayJSON
		}
		key, ok := keyTok.(string)
		if !ok {
			return cursorDisplayInput{}, ErrCursorDisplayJSON
		}
		if cursorDisplayCredentialText(key) {
			if scanErr := cursorDisplayScanValue(dec); scanErr != nil && errors.Is(scanErr, ErrCursorDisplayJSON) {
				return cursorDisplayInput{}, scanErr
			}
			return cursorDisplayInput{}, ErrCursorDisplayCredential
		}
		if len(key) > 64 {
			if scanErr := cursorDisplayScanValue(dec); scanErr != nil && errors.Is(scanErr, ErrCursorDisplayJSON) {
				return cursorDisplayInput{}, scanErr
			}
			return cursorDisplayInput{}, ErrCursorDisplayLimit
		}
		if _, dup := seen[key]; dup {
			return cursorDisplayInput{}, ErrCursorDisplayDuplicate
		}
		if !cursorDisplayKnownKey(key) {
			if scanErr := cursorDisplayScanValue(dec); scanErr != nil {
				return cursorDisplayInput{}, scanErr
			}
			return cursorDisplayInput{}, ErrCursorDisplayUnknown
		}
		seen[key] = struct{}{}
		if fieldErr := in.apply(dec, key); fieldErr != nil {
			return cursorDisplayInput{}, fieldErr
		}
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') || dec.More() {
		return cursorDisplayInput{}, ErrCursorDisplayJSON
	}
	var extra json.RawMessage
	if extraErr := dec.Decode(&extra); !errors.Is(extraErr, io.EOF) {
		return cursorDisplayInput{}, ErrCursorDisplayJSON
	}
	for _, key := range cursorDisplayKeys {
		if _, ok := seen[key]; !ok {
			return cursorDisplayInput{}, ErrCursorDisplayMissing
		}
	}
	return in, nil
}

func (in *cursorDisplayInput) apply(dec *json.Decoder, key string) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrCursorDisplayJSON
	}
	if delim, ok := tok.(json.Delim); ok {
		if restErr := consumeRest(dec, delim); restErr != nil {
			return ErrCursorDisplayType
		}
		return ErrCursorDisplayType
	}
	switch key {
	case "schema", "capturedAt", "route", "product", "availability":
		text, ok := tok.(string)
		if !ok || cursorDisplayCredentialText(text) {
			if ok && cursorDisplayCredentialText(text) {
				return ErrCursorDisplayCredential
			}
			return ErrCursorDisplayType
		}
		return in.setRequired(key, text)
	case "plan", "accountRef", "errorCode", "windowLabel", "usedPercent", "resetDisplayDate", "resetDisplayText", "spendAmount", "spendCurrency":
		return in.setNullableString(key, tok)
	case "durationMinutes":
		return in.setDuration(tok)
	default:
		return ErrCursorDisplayUnknown
	}
}

func (in *cursorDisplayInput) setRequired(key, text string) error {
	if len(text) > cursorDisplayMaxString {
		return ErrCursorDisplayLimit
	}
	switch key {
	case "schema":
		in.schema = text
	case "capturedAt":
		in.capturedAt = text
	case "route":
		in.route = text
	case "product":
		in.product = text
	case "availability":
		in.availability = text
	default:
		return ErrCursorDisplayUnknown
	}
	return nil
}

func (in *cursorDisplayInput) setNullableString(key string, tok json.Token) error {
	if tok == nil {
		return nil
	}
	text, ok := tok.(string)
	if !ok {
		return ErrCursorDisplayType
	}
	if cursorDisplayCredentialText(text) {
		return ErrCursorDisplayCredential
	}
	if len(text) > cursorDisplayMaxString {
		return ErrCursorDisplayLimit
	}
	switch key {
	case "plan":
		if !cursorDisplayToken(text) {
			return ErrCursorDisplayType
		}
		in.plan = cursorDisplayPtr(text)
	case "accountRef":
		if !cursorDisplayToken(text) {
			return ErrCursorDisplayType
		}
		in.accountRef = cursorDisplayPtr(text)
	case "errorCode":
		if !cursorDisplayErrorCode(text) {
			return ErrCursorDisplayType
		}
		in.errorCode = cursorDisplayPtr(text)
	case "windowLabel":
		if text != cursorDisplayWeeklyLabel {
			return ErrCursorDisplayType
		}
		in.windowLabel = cursorDisplayPtr(text)
	case "usedPercent":
		if err := cursorDisplayPercent(text); err != nil {
			return err
		}
		in.used = cursorDisplayPtr(text)
	case "resetDisplayDate":
		if !rfc3339Date(text) {
			return ErrCursorDisplayReset
		}
		in.reset = cursorDisplayPtr(text)
	case "resetDisplayText":
		if !cursorDisplayMonthDay(text) {
			return ErrCursorDisplayReset
		}
		in.resetText = cursorDisplayPtr(text)
	case "spendAmount":
		if err := cursorDisplayMoney(text); err != nil {
			return err
		}
		in.spendAmount = cursorDisplayPtr(text)
	case "spendCurrency":
		if text != "USD" {
			return ErrCursorDisplaySpend
		}
		in.spendCurrency = cursorDisplayPtr(text)
	default:
		return ErrCursorDisplayUnknown
	}
	return nil
}

func (in *cursorDisplayInput) setDuration(tok json.Token) error {
	if tok == nil {
		return nil
	}
	num, ok := tok.(json.Number)
	if !ok {
		return ErrCursorDisplayType
	}
	text := num.String()
	if text == "" || strings.ContainsAny(text, ".eE+") || strings.HasPrefix(text, "-") {
		return ErrCursorDisplayType
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != text || n < 1 || n > cursorDisplayMaxMinutes {
		return ErrCursorDisplayType
	}
	in.duration = &n
	return nil
}

func (in *cursorDisplayInput) finish(selected CursorDisplaySelection, now time.Time) error {
	if in.schema != CursorDisplayInputSchema {
		return ErrCursorDisplayUnknown
	}
	captured, ok := cursorDisplayTime(in.capturedAt)
	if !ok {
		return ErrCursorDisplayTime
	}
	if captured.After(now) {
		return ErrCursorDisplayFuture
	}
	in.stale = now.Sub(captured) > selected.MaxAge
	in.staleReason = cursorDisplayReasonCapture
	if in.stale {
		in.staleReason = cursorDisplayReasonStale
	}
	if in.errorCode != nil && *in.errorCode == "display_stale" {
		in.stale = true
		if in.staleReason == cursorDisplayReasonCapture {
			in.staleReason = cursorDisplayReasonMarked
		}
	}
	if (in.spendAmount == nil) != (in.spendCurrency == nil) {
		return ErrCursorDisplaySpend
	}
	if in.reset != nil && in.resetText != nil {
		return ErrCursorDisplayReset
	}
	switch in.route {
	case cursorDisplayRouteXAI:
		return in.finishXAI(selected)
	case cursorDisplayRouteCursor:
		return in.finishCursor(selected)
	default:
		return ErrCursorDisplayType
	}
}

func (in *cursorDisplayInput) finishXAI(selected CursorDisplaySelection) error {
	if selected.BindingState != BindingUnknown {
		return ErrCursorDisplayBinding
	}
	if in.product != "grok" || in.availability != string(AvailabilityUnsupported) || in.errorCode != nil {
		return ErrCursorDisplayType
	}
	if in.plan != nil || in.accountRef != nil || in.windowLabel != nil || in.duration != nil || in.used != nil || in.reset != nil || in.resetText != nil || in.spendAmount != nil {
		return ErrCursorDisplayType
	}
	return nil
}

func (in *cursorDisplayInput) finishCursor(selected CursorDisplaySelection) error {
	if in.product != "grok" && in.product != "cursor" {
		return ErrCursorDisplayType
	}
	availability := cursorDisplayAvailability(in.availability)
	if availability == "" || availability == AvailabilityUnsupported {
		return ErrCursorDisplayType
	}
	if err := cursorDisplayErrorMatches(availability, in.errorCode, in.used); err != nil {
		return err
	}
	if selected.BindingState == BindingVerified {
		if in.accountRef == nil || *in.accountRef != *selected.AccountRef {
			return ErrCursorDisplayBinding
		}
	}
	return nil
}

func (in cursorDisplayInput) item(selected CursorDisplaySelection, now time.Time) (ReadItem, error) {
	if in.route == cursorDisplayRouteXAI {
		return in.xaiItem(selected, now), nil
	}
	return in.cursorItem(selected, now)
}

func (in cursorDisplayInput) cursorItem(selected CursorDisplaySelection, now time.Time) (ReadItem, error) {
	received := now.Format(time.RFC3339Nano)
	state := FreshUnknown
	if in.stale {
		state = FreshStale
	}
	accountLabel := ""
	if in.accountRef != nil && selected.BindingState != BindingVerified {
		accountLabel = *in.accountRef
	}
	obs := Observation{
		Schema:              SchemaObservation,
		ObservationID:       cursorDisplayID("cursor-display-", selected, in, accountLabel),
		ProducerRef:         selected.ProducerRef,
		ProducerIncarnation: selected.ProducerIncarnation,
		Sequence:            Count(selected.Sequence),
		BindingGeneration:   Count(selected.BindingGeneration),
		Provider:            "cursor",
		Source: Source{
			Kind:          SourceNativeDisplay,
			Version:       CursorDisplaySourceVersion,
			CapabilityRef: CursorDisplayCapability,
		},
		Scope: Scope{
			Products:     []string{in.product},
			BindingState: selected.BindingState,
		},
		CapturedAt:   in.capturedAt,
		Availability: cursorDisplayAvailability(in.availability),
		Completeness: CompletenessPartial,
		DirectFields: cursorDisplayFields(in, accountLabel),
		Measures:     []Measure{},
		Derivations:  []Derivation{},
		EvidenceRefs: []string{cursorDisplayEvidenceCursor},
		ErrorCode:    cloneString(in.errorCode),
	}
	if selected.BindingState == BindingVerified {
		obs.Scope.AccountRef = cloneString(selected.AccountRef)
		obs.Scope.WorkspaceRef = cloneString(selected.WorkspaceRef)
		obs.Scope.EntitlementRef = cloneString(selected.EntitlementRef)
	}
	window := Window{Ref: CursorDisplayIncludedWindow, ResetPrecision: ResetUnknown}
	if in.duration != nil {
		minutes := Count(*in.duration)
		window.DurationMinutes = &minutes
	}
	if in.reset != nil || in.resetText != nil {
		window.ResetPrecision = ResetDisplayOnly
	}
	measure := Measure{
		ID:             CursorDisplayIncludedMeasure,
		Category:       CategoryIncluded,
		Window:         window,
		Unit:           UnitPercent,
		Direction:      DirectionUsed,
		Value:          MeasureValue{State: ValueUnknown},
		EvidenceFields: []string{},
	}
	if in.used != nil {
		used := *in.used
		ref := CursorDisplayIncludedValidation
		measure.Value = MeasureValue{State: ValueKnown, Decimal: &used}
		measure.ValidationRef = &ref
		complement, ok := cursorDisplayComplement(used)
		if !ok {
			return ReadItem{}, ErrCursorDisplayPercent
		}
		obs.Derivations = []Derivation{{
			ID:        CursorDisplayRemainingDerivation,
			Formula:   FormulaComplement,
			InputRefs: []string{CursorDisplayIncludedMeasure},
			Unit:      UnitPercent,
			Precision: "exact",
			Value:     MeasureValue{State: ValueKnown, Decimal: cursorDisplayPtr(complement)},
		}}
	}
	obs.Measures = append(obs.Measures, measure)
	if in.spendAmount != nil {
		amount := *in.spendAmount
		currency := *in.spendCurrency
		ref := CursorDisplaySpendValidation
		obs.Measures = append(obs.Measures, Measure{
			ID:             CursorDisplaySpendMeasure,
			Category:       CategorySpend,
			Window:         Window{Ref: CursorDisplaySpendWindow, ResetPrecision: ResetNotApplicable},
			Unit:           UnitMoney,
			Currency:       &currency,
			Direction:      DirectionUsed,
			Value:          MeasureValue{State: ValueKnown, Decimal: &amount},
			ValidationRef:  &ref,
			EvidenceFields: []string{},
		})
	}
	currents := make([]CurrentMeasure, 0, len(obs.Measures))
	for _, measure := range obs.Measures {
		currents = append(currents, CurrentMeasure{
			MeasureID: measure.ID,
			State:     CurrentUnknown,
			Value:     MeasureValue{State: ValueUnknown},
			Reason:    in.staleReason,
		})
	}
	return ReadItem{
		Observation: obs,
		ReceivedAt:  received,
		Validation:  ValidationRecord{State: ValidationDisplayOnly, ValidationRefs: []string{}},
		Binding: BindingRecord{
			State:        obs.Scope.BindingState,
			Generation:   obs.BindingGeneration,
			AccountRef:   cloneString(obs.Scope.AccountRef),
			WorkspaceRef: cloneString(obs.Scope.WorkspaceRef),
		},
		Freshness: Freshness{
			State:  state,
			AsOf:   received,
			Basis:  BasisCapture,
			Reason: in.staleReason,
		},
		CurrentMeasures: currents,
	}, nil
}

func (in cursorDisplayInput) xaiItem(selected CursorDisplaySelection, now time.Time) ReadItem {
	received := now.Format(time.RFC3339Nano)
	state := FreshUnknown
	if in.stale {
		state = FreshStale
	}
	code := cursorDisplayXAICode
	obs := Observation{
		Schema:              SchemaObservation,
		ObservationID:       cursorDisplayID("xai-direct-", selected, in, ""),
		ProducerRef:         selected.ProducerRef,
		ProducerIncarnation: selected.ProducerIncarnation,
		Sequence:            Count(selected.Sequence),
		Provider:            "xai",
		Source: Source{
			Kind:          SourceNativeDisplay,
			Version:       CursorDisplaySourceVersion,
			CapabilityRef: CursorDisplayXAICapability,
		},
		Scope:        Scope{Products: []string{"grok"}, BindingState: BindingUnknown},
		CapturedAt:   in.capturedAt,
		Availability: AvailabilityUnsupported,
		Completeness: CompletenessUnknown,
		DirectFields: []DirectField{},
		Measures:     []Measure{},
		Derivations:  []Derivation{},
		EvidenceRefs: []string{cursorDisplayEvidenceXAI},
		ErrorCode:    &code,
	}
	return ReadItem{
		Observation: obs,
		ReceivedAt:  received,
		Validation:  ValidationRecord{State: ValidationUnverified, ValidationRefs: []string{}},
		Binding:     BindingRecord{State: BindingUnknown},
		Freshness: Freshness{
			State:  state,
			AsOf:   received,
			Basis:  BasisNone,
			Reason: cursorDisplayReasonXAI,
		},
		CurrentMeasures: []CurrentMeasure{},
	}
}

func cursorDisplayFields(in cursorDisplayInput, accountLabel string) []DirectField {
	fields := make([]DirectField, 0, 4)
	if in.plan != nil {
		fields = append(fields, DirectField{Field: "plan", OriginalType: TypeString, LexicalValue: cloneString(in.plan)})
	}
	if in.windowLabel != nil {
		fields = append(fields, DirectField{Field: "windowLabel", OriginalType: TypeString, LexicalValue: cloneString(in.windowLabel)})
	}
	if in.reset != nil {
		fields = append(fields, DirectField{Field: "resetDisplayDate", OriginalType: TypeString, LexicalValue: cloneString(in.reset)})
	}
	if in.resetText != nil {
		fields = append(fields, DirectField{Field: "resetDisplayText", OriginalType: TypeString, LexicalValue: cloneString(in.resetText)})
	}
	if accountLabel != "" {
		fields = append(fields, DirectField{Field: "accountLabel", OriginalType: TypeString, LexicalValue: cursorDisplayPtr(accountLabel)})
	}
	return fields
}

// cursorDisplayID binds the caller sequence into the capture id. The same
// selection and payload keep the same id when the receipt clock moves. A later
// sequence is a different capture even when the sanitized screen repeats.
func cursorDisplayID(prefix string, selected CursorDisplaySelection, in cursorDisplayInput, accountLabel string) string {
	identity := struct {
		Producer      string `json:"producer"`
		Incarnation   string `json:"incarnation"`
		Sequence      int64  `json:"sequence"`
		Binding       string `json:"binding"`
		Generation    int64  `json:"generation"`
		Account       string `json:"account"`
		Workspace     string `json:"workspace"`
		Entitlement   string `json:"entitlement"`
		Route         string `json:"route"`
		Product       string `json:"product"`
		CapturedAt    string `json:"capturedAt"`
		Plan          string `json:"plan"`
		AccountLabel  string `json:"accountLabel"`
		WindowLabel   string `json:"windowLabel"`
		Duration      string `json:"duration"`
		Used          string `json:"used"`
		Reset         string `json:"reset"`
		ResetText     string `json:"resetText"`
		SpendAmount   string `json:"spendAmount"`
		SpendCurrency string `json:"spendCurrency"`
		Availability  string `json:"availability"`
		ErrorCode     string `json:"errorCode"`
		SourceVersion string `json:"sourceVersion"`
	}{
		Producer:      selected.ProducerRef,
		Incarnation:   selected.ProducerIncarnation,
		Sequence:      selected.Sequence,
		Binding:       string(selected.BindingState),
		Generation:    selected.BindingGeneration,
		Account:       cursorDisplayText(selected.AccountRef),
		Workspace:     cursorDisplayText(selected.WorkspaceRef),
		Entitlement:   cursorDisplayText(selected.EntitlementRef),
		Route:         in.route,
		Product:       in.product,
		CapturedAt:    in.capturedAt,
		Plan:          cursorDisplayText(in.plan),
		AccountLabel:  accountLabel,
		WindowLabel:   cursorDisplayText(in.windowLabel),
		Duration:      cursorDisplayDuration(in.duration),
		Used:          cursorDisplayText(in.used),
		Reset:         cursorDisplayText(in.reset),
		ResetText:     cursorDisplayText(in.resetText),
		SpendAmount:   cursorDisplayText(in.spendAmount),
		SpendCurrency: cursorDisplayText(in.spendCurrency),
		Availability:  in.availability,
		ErrorCode:     cursorDisplayText(in.errorCode),
		SourceVersion: CursorDisplaySourceVersion,
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		encoded = []byte(prefix)
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("%s%x", prefix, sum[:12])
}

func cursorDisplayStatuses(items []ReadItem) []SourceStatus {
	statuses := make([]SourceStatus, 0, len(items))
	prior := make(map[string]LastGood, len(items))
	for _, item := range items {
		key := cursorDisplayStatusKey(item)
		var previous *LastGood
		if good, ok := prior[key]; ok {
			previous = cloneLastGood(&good)
		}
		status := cursorDisplayStatus(item, previous)
		if cursorDisplayGoodSample(item) && status.LastGood != nil {
			prior[key] = *status.LastGood
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func cursorDisplayStatus(item ReadItem, previous *LastGood) SourceStatus {
	completed := item.ReceivedAt
	status := SourceStatus{
		Provider:     item.Observation.Provider,
		ProducerRef:  item.Observation.ProducerRef,
		Availability: item.Observation.Availability,
		Freshness:    item.Freshness,
		Validation:   item.Validation,
		Binding:      item.Binding,
		LastAttempt: LastAttempt{
			AttemptedAt: item.ReceivedAt,
			CompletedAt: &completed,
			Result:      AttemptSuccess,
			Code:        cloneString(item.Observation.ErrorCode),
		},
	}
	switch item.Observation.Availability {
	case AvailabilityUnavailable, AvailabilityAuthNeeded:
		status.LastAttempt.Result = AttemptError
	case AvailabilityConflict, AvailabilityUnsupported:
		status.LastAttempt.Result = AttemptUnknown
	}
	if cursorDisplayGoodSample(item) {
		status.LastGood = cursorDisplayLastGood(item)
		return status
	}
	if previous != nil {
		status.LastGood = cloneLastGood(previous)
	}
	return status
}

// cursorDisplayGoodSample is an accepted Cursor display sample.
// Unavailable, auth-needed, and conflict captures are attempts, not LastGood.
func cursorDisplayGoodSample(item ReadItem) bool {
	if item.Observation.Provider != "cursor" {
		return false
	}
	switch item.Observation.Availability {
	case AvailabilityUnavailable, AvailabilityAuthNeeded, AvailabilityConflict:
		return false
	default:
		return true
	}
}

func cursorDisplayLastGood(item ReadItem) *LastGood {
	return &LastGood{
		ObservationID:      item.Observation.ObservationID,
		CapturedAt:         item.Observation.CapturedAt,
		ProviderObservedAt: cloneString(item.Observation.ProviderObservedAt),
		ReceivedAt:         item.ReceivedAt,
	}
}

func cloneLastGood(in *LastGood) *LastGood {
	if in == nil {
		return nil
	}
	return &LastGood{
		ObservationID:      in.ObservationID,
		CapturedAt:         in.CapturedAt,
		ProviderObservedAt: cloneString(in.ProviderObservedAt),
		ReceivedAt:         in.ReceivedAt,
	}
}

func cursorDisplayStatusKey(item ReadItem) string {
	return strings.Join([]string{
		item.Observation.Provider,
		item.Observation.ProducerRef,
		item.Observation.ProducerIncarnation,
		string(item.Binding.State),
		strconv.FormatInt(int64(item.Binding.Generation), 10),
		cursorDisplayText(item.Binding.AccountRef),
		cursorDisplayText(item.Binding.WorkspaceRef),
	}, "\x00")
}

func cursorDisplayRevision(items []ReadItem) string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.Observation.ObservationID
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return fmt.Sprintf("cursor-display-%x", sum[:8])
}

func cursorDisplayOwnItem(item ReadItem) error {
	obs := item.Observation
	if obs.Schema != SchemaObservation || obs.Source.Kind != SourceNativeDisplay || obs.Source.Version != CursorDisplaySourceVersion {
		return ErrCursorDisplaySelection
	}
	switch obs.Source.CapabilityRef {
	case CursorDisplayCapability:
		if obs.Provider != "cursor" {
			return ErrCursorDisplaySelection
		}
	case CursorDisplayXAICapability:
		if obs.Provider != "xai" {
			return ErrCursorDisplaySelection
		}
	default:
		return ErrCursorDisplaySelection
	}
	if obs.ProviderObservedAt != nil {
		return ErrCursorDisplaySelection
	}
	return nil
}

func cursorDisplaySameCapture(a, b ReadItem) bool {
	if !cursorDisplaySameObservation(a.Observation, b.Observation) {
		return false
	}
	if cursorDisplayViewEqual(a, b) {
		return true
	}
	earlier, later, ok := cursorDisplayReceiptOrder(a, b)
	if !ok {
		return false
	}
	return cursorDisplayOnlyAged(earlier, later)
}

func cursorDisplayViewEqual(a, b ReadItem) bool {
	left, errA := json.Marshal(cursorDisplayCaptureView(a))
	right, errB := json.Marshal(cursorDisplayCaptureView(b))
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func cursorDisplayReceiptOrder(a, b ReadItem) (ReadItem, ReadItem, bool) {
	at, aOK := cursorDisplayTime(a.ReceivedAt)
	bt, bOK := cursorDisplayTime(b.ReceivedAt)
	if !aOK || !bOK || (!at.Before(bt) && !bt.Before(at)) {
		return ReadItem{}, ReadItem{}, false
	}
	if bt.Before(at) {
		return b, a, true
	}
	return a, b, true
}

func cursorDisplayEarlierReceipt(candidate, kept ReadItem) bool {
	at, aOK := cursorDisplayTime(candidate.ReceivedAt)
	bt, bOK := cursorDisplayTime(kept.ReceivedAt)
	if !aOK || !bOK {
		return false
	}
	return at.Before(bt)
}

// cursorDisplayOnlyAged reports that later is the same capture as earlier
// after derived freshness aged. The later state must be staler. A fresher
// later label is not this transition.
func cursorDisplayOnlyAged(earlier, later ReadItem) bool {
	if !cursorDisplayReceiptIsAsOf(earlier) || !cursorDisplayReceiptIsAsOf(later) {
		return false
	}
	if !cursorDisplayAgedTransition(earlier.Freshness, later.Freshness) {
		return false
	}
	if !cursorDisplayStableEqual(earlier, later) {
		return false
	}
	return cursorDisplayCurrentsFollowReason(earlier) && cursorDisplayCurrentsFollowReason(later)
}

func cursorDisplayReceiptIsAsOf(item ReadItem) bool {
	return item.ReceivedAt != "" && item.Freshness.AsOf == item.ReceivedAt
}

func cursorDisplayAgedTransition(before, after Freshness) bool {
	if before.Basis != after.Basis {
		return false
	}
	switch {
	case before.State == FreshUnknown && before.Reason == cursorDisplayReasonCapture &&
		after.State == FreshStale && after.Reason == cursorDisplayReasonStale:
		return true
	case before.State == FreshStale && before.Reason == cursorDisplayReasonMarked &&
		after.State == FreshStale && after.Reason == cursorDisplayReasonStale:
		return true
	case before.State == FreshUnknown && after.State == FreshStale &&
		before.Reason == cursorDisplayReasonXAI && after.Reason == cursorDisplayReasonXAI &&
		before.Basis == BasisNone:
		return true
	default:
		return false
	}
}

func cursorDisplayStableEqual(a, b ReadItem) bool {
	if a.Freshness.Basis != b.Freshness.Basis || !cursorDisplaySameOptional(a.Freshness.PolicyRef, b.Freshness.PolicyRef) {
		return false
	}
	if !cursorDisplayJSONEqual(a.Validation, b.Validation) || !cursorDisplayJSONEqual(a.Binding, b.Binding) {
		return false
	}
	return cursorDisplayCurrentsEqualIgnoringReason(a.CurrentMeasures, b.CurrentMeasures)
}

func cursorDisplayCurrentsFollowReason(item ReadItem) bool {
	for _, current := range item.CurrentMeasures {
		if current.Reason != item.Freshness.Reason {
			return false
		}
	}
	return true
}

func cursorDisplayCurrentsEqualIgnoringReason(a, b []CurrentMeasure) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		left := a[i]
		right := b[i]
		left.Reason = ""
		right.Reason = ""
		if !cursorDisplayJSONEqual(left, right) {
			return false
		}
	}
	return true
}

func cursorDisplaySameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cursorDisplayJSONEqual(a, b any) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func cursorDisplayCaptureView(item ReadItem) struct {
	Validation ValidationRecord `json:"validation"`
	Binding    BindingRecord    `json:"binding"`
	State      FreshnessState   `json:"state"`
	Basis      FreshnessBasis   `json:"basis"`
	Reason     string           `json:"reason"`
	Current    []CurrentMeasure `json:"current"`
} {
	return struct {
		Validation ValidationRecord `json:"validation"`
		Binding    BindingRecord    `json:"binding"`
		State      FreshnessState   `json:"state"`
		Basis      FreshnessBasis   `json:"basis"`
		Reason     string           `json:"reason"`
		Current    []CurrentMeasure `json:"current"`
	}{
		Validation: item.Validation,
		Binding:    item.Binding,
		State:      item.Freshness.State,
		Basis:      item.Freshness.Basis,
		Reason:     item.Freshness.Reason,
		Current:    item.CurrentMeasures,
	}
}

func cursorDisplaySameObservation(a, b Observation) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func cursorDisplayErrorMatches(availability Availability, code, used *string) error {
	if availability == AvailabilityUnavailable || availability == AvailabilityAuthNeeded || availability == AvailabilityConflict {
		if used != nil {
			return ErrCursorDisplayType
		}
	}
	if code == nil {
		return nil
	}
	switch *code {
	case "display_unavailable":
		if availability != AvailabilityUnavailable {
			return ErrCursorDisplayType
		}
	case "display_auth_needed":
		if availability != AvailabilityAuthNeeded {
			return ErrCursorDisplayType
		}
	case "display_unknown":
		if availability != AvailabilityPartial || used != nil {
			return ErrCursorDisplayType
		}
	case "display_stale":
		if availability != AvailabilityPartial {
			return ErrCursorDisplayType
		}
	default:
		return ErrCursorDisplayType
	}
	return nil
}

func cursorDisplayAvailability(text string) Availability {
	switch Availability(text) {
	case AvailabilityPartial, AvailabilityUnavailable, AvailabilityAuthNeeded, AvailabilityUnsupported, AvailabilityConflict:
		return Availability(text)
	default:
		return ""
	}
}

func cursorDisplayErrorCode(text string) bool {
	switch text {
	case "display_unavailable", "display_stale", "display_unknown", "display_auth_needed":
		return true
	default:
		return false
	}
}

func cursorDisplayPercent(text string) error {
	if !cursorDisplayDecimal.MatchString(text) {
		return ErrCursorDisplayPercent
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok || n.Sign() < 0 || n.Cmp(big.NewRat(100, 1)) > 0 {
		return ErrCursorDisplayPercent
	}
	if _, ok := cursorDisplayComplement(text); !ok {
		return ErrCursorDisplayPercent
	}
	return nil
}

func cursorDisplayMoney(text string) error {
	if !cursorDisplayDecimal.MatchString(text) {
		return ErrCursorDisplaySpend
	}
	n, ok := new(big.Rat).SetString(text)
	if !ok || n.Sign() < 0 || n.Cmp(big.NewRat(1000000, 1)) > 0 {
		return ErrCursorDisplaySpend
	}
	return nil
}

func cursorDisplayComplement(used string) (string, bool) {
	n, ok := new(big.Rat).SetString(used)
	if !ok {
		return "", false
	}
	hundred := big.NewRat(100, 1)
	if n.Cmp(hundred) > 0 {
		return "", false
	}
	scale := 0
	if dot := strings.IndexByte(used, '.'); dot >= 0 {
		scale = len(used) - dot - 1
	}
	got := new(big.Rat).Sub(hundred, n)
	text := got.FloatString(scale)
	back, ok := new(big.Rat).SetString(text)
	if !ok || back.Cmp(got) != 0 || !cursorDisplayDecimal.MatchString(text) {
		return "", false
	}
	return text, true
}

// cursorDisplayMonthDay accepts one exact English month and day label.
// February 29 stays a display label. No year, timezone, or midnight is chosen.
func cursorDisplayMonthDay(text string) bool {
	if text == "" || len(text) > 32 || strings.TrimSpace(text) != text {
		return false
	}
	month, dayText, ok := strings.Cut(text, " ")
	if !ok || month == "" || strings.Contains(dayText, " ") {
		return false
	}
	maxDay, known := cursorDisplayMonthDays(month)
	if !known || dayText == "" || len(dayText) > 2 || dayText[0] == '0' {
		return false
	}
	for i := 0; i < len(dayText); i++ {
		if dayText[i] < '0' || dayText[i] > '9' {
			return false
		}
	}
	day, err := strconv.Atoi(dayText)
	if err != nil || strconv.Itoa(day) != dayText {
		return false
	}
	return day >= 1 && day <= maxDay
}

func cursorDisplayMonthDays(month string) (int, bool) {
	switch month {
	case "Jan", "January", "Mar", "March", "May", "Jul", "July", "Aug", "August", "Oct", "October", "Dec", "December":
		return 31, true
	case "Apr", "April", "Jun", "June", "Sep", "September", "Nov", "November":
		return 30, true
	case "Feb", "February":
		return 29, true
	default:
		return 0, false
	}
}

func cursorDisplayTime(text string) (time.Time, bool) {
	if len(text) > 64 || validateDateTime(text) != nil {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func cursorDisplayKnownKey(key string) bool {
	for _, candidate := range cursorDisplayKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

func cursorDisplayToken(text string) bool {
	if text == "" || len(text) > 128 || cursorDisplayCredentialText(text) {
		return false
	}
	for i := 0; i < len(text); i++ {
		if !cursorDisplayRune(text[i]) {
			return false
		}
	}
	return true
}

func cursorDisplayRune(c byte) bool {
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
		return true
	}
	switch c {
	case '.', '_', ':', '-':
		return true
	default:
		return false
	}
}

func cursorDisplayCredentialText(text string) bool {
	folded := strings.ToLower(text)
	for _, needle := range []string{
		"cookie", "token", "password", "secret", "authorization", "api_key", "apikey",
		"bearer", "credential", "private_key", "sessionid", "sk-", "ghp_", "eyj",
	} {
		if strings.Contains(folded, needle) {
			return true
		}
	}
	return false
}

func cursorDisplayScanValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrCursorDisplayJSON
	}
	return cursorDisplayScanToken(dec, tok)
}

func cursorDisplayScanToken(dec *json.Decoder, tok json.Token) error {
	switch value := tok.(type) {
	case string:
		if cursorDisplayCredentialText(value) {
			return ErrCursorDisplayCredential
		}
		if len(value) > cursorDisplayMaxString {
			return ErrCursorDisplayLimit
		}
		return nil
	case json.Delim:
		return cursorDisplayScanContainer(dec, value)
	case nil, json.Number, bool:
		return nil
	default:
		return ErrCursorDisplayJSON
	}
}

func cursorDisplayScanContainer(dec *json.Decoder, open json.Delim) error {
	closeDelim := json.Delim('}')
	if open == '[' {
		closeDelim = ']'
	} else if open != '{' {
		return ErrCursorDisplayJSON
	}
	for dec.More() {
		if open == '{' {
			keyTok, err := dec.Token()
			if err != nil {
				return ErrCursorDisplayJSON
			}
			key, ok := keyTok.(string)
			if !ok {
				return ErrCursorDisplayJSON
			}
			if cursorDisplayCredentialText(key) {
				return ErrCursorDisplayCredential
			}
		}
		if err := cursorDisplayScanValue(dec); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil || end != closeDelim {
		return ErrCursorDisplayJSON
	}
	return nil
}

func cursorDisplayPtr(text string) *string {
	value := text
	return &value
}

func cursorDisplayText(text *string) string {
	if text == nil {
		return ""
	}
	return *text
}

func cursorDisplayDuration(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}
