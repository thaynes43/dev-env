package providerallowance

import (
	"bytes"
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
	// SchemaObservation is the observation document name.
	SchemaObservation = "providerallowance.v1"
	// SchemaRead is the cached-read document name.
	SchemaRead = "providerallowance.read.v1"
	// UsageJSONCommand is the documented future read command. It is not implemented here.
	UsageJSONCommand = "agent-run usage -o json"
	// ServedModelUnknown is the only served-model result this package reports.
	ServedModelUnknown = "unknown"

	UnitPercent = "percent"
	UnitMoney   = "money"
)

type (
	SourceKind      string
	Availability    string
	Completeness    string
	BindingState    string
	ValueState      string
	Direction       string
	Category        string
	ResetPrecision  string
	Formula         string
	OriginalType    string
	DirectClass     string
	CurrentState    string
	FreshnessState  string
	FreshnessBasis  string
	ValidationState string
	AttemptResult   string
)

const (
	SourceNativeRPC     SourceKind = "native_rpc"
	SourceNativeDisplay SourceKind = "native_display"
	SourcePassive       SourceKind = "passive_event"
	SourceSynthetic     SourceKind = "synthetic"
	SourceAdvisory      SourceKind = "advisory"

	AvailabilityAvailable    Availability = "available"
	AvailabilityPartial      Availability = "partial"
	AvailabilityUnavailable  Availability = "unavailable"
	AvailabilityUnconfigured Availability = "unconfigured"
	AvailabilityAuthNeeded   Availability = "auth_needed"
	AvailabilityThrottled    Availability = "throttled"
	AvailabilityUnsupported  Availability = "unsupported"
	AvailabilityConflict     Availability = "conflict"

	CompletenessComplete Completeness = "complete"
	CompletenessPartial  Completeness = "partial"
	CompletenessUnknown  Completeness = "unknown"

	BindingVerified BindingState = "verified"
	BindingUnknown  BindingState = "unknown"
	BindingMismatch BindingState = "mismatch"

	ValueKnown         ValueState = "known"
	ValueUnknown       ValueState = "unknown"
	ValueNotReported   ValueState = "not_reported"
	ValueNotApplicable ValueState = "not_applicable"
	ValueUncapped      ValueState = "uncapped"

	DirectionUsed      Direction = "used"
	DirectionRemaining Direction = "remaining"
	DirectionLimit     Direction = "limit"
	DirectionNone      Direction = "none"
	DirectionUnknown   Direction = "unknown"

	CategoryIncluded  Category = "included_quota"
	CategorySpend     Category = "api_spend"
	CategoryRateLimit Category = "rate_limit"
	CategoryLocal     Category = "local_capacity"
	CategoryAdvisory  Category = "advisory"

	ResetInstant       ResetPrecision = "instant"
	ResetDisplayOnly   ResetPrecision = "display_only"
	ResetUnknown       ResetPrecision = "unknown"
	ResetNotApplicable ResetPrecision = "not_applicable"

	FormulaComplement Formula = "complement_percent"
	FormulaSum        Formula = "sum_compatible_values"

	TypeString  OriginalType = "string"
	TypeDecimal OriginalType = "decimal"
	TypeBoolean OriginalType = "boolean"
	TypeNull    OriginalType = "null"

	ClassDisplay   DirectClass = "display"
	ClassInventory DirectClass = "inventory"
	ClassSignal    DirectClass = "signal"

	CurrentKnown         CurrentState = "known"
	CurrentUnknown       CurrentState = "unknown"
	CurrentExpired       CurrentState = "expired"
	CurrentNotReported   CurrentState = "not_reported"
	CurrentNotApplicable CurrentState = "not_applicable"
	CurrentUncapped      CurrentState = "uncapped"
	CurrentConflict      CurrentState = "conflict"

	FreshFresh   FreshnessState = "fresh"
	FreshStale   FreshnessState = "stale"
	FreshUnknown FreshnessState = "unknown"
	FreshExpired FreshnessState = "expired"

	BasisProvider FreshnessBasis = "provider_observation"
	BasisCapture  FreshnessBasis = "capture_only"
	BasisNone     FreshnessBasis = "none"

	ValidationSourceFields ValidationState = "source_fields_validated"
	ValidationDisplayOnly  ValidationState = "display_only"
	ValidationUnverified   ValidationState = "unverified"
	ValidationPartial      ValidationState = "partial"
	ValidationRejected     ValidationState = "rejected"
	ValidationConflict     ValidationState = "conflict"

	AttemptSuccess     AttemptResult = "success"
	AttemptError       AttemptResult = "error"
	AttemptUnavailable AttemptResult = "unavailable"
	AttemptUnknown     AttemptResult = "unknown"
)

// ParseLimits is the caller-supplied bound for one decode. Zero values are
// refused. These numbers are not a production SLO.
type ParseLimits struct {
	MaxBytes       int
	MaxDepth       int
	MaxArrayItems  int
	MaxObjectKeys  int
	MaxStringBytes int
}

// FixtureParseLimits bounds the reviewed Story189 fixtures.
var FixtureParseLimits = ParseLimits{
	MaxBytes:       256 * 1024,
	MaxDepth:       32,
	MaxArrayItems:  128,
	MaxObjectKeys:  64,
	MaxStringBytes: 8192,
}

// Count is a nonnegative integer. JSON booleans and fractional numbers are refused.
type Count int64

// DecodeError is a closed-document failure.
type DecodeError struct {
	Code string
	Path string
	Msg  string
}

func (e *DecodeError) Error() string {
	if e.Path != "" {
		return e.Code + " at " + e.Path + ": " + e.Msg
	}
	return e.Code + ": " + e.Msg
}

// Observation is one immutable providerallowance.v1 document.
type Observation struct {
	Schema              string        `json:"schema"`
	ObservationID       string        `json:"observationId"`
	ProducerRef         string        `json:"producerRef"`
	ProducerIncarnation string        `json:"producerIncarnation"`
	Sequence            Count         `json:"sequence"`
	BindingGeneration   Count         `json:"bindingGeneration"`
	Provider            string        `json:"provider"`
	Source              Source        `json:"source"`
	Scope               Scope         `json:"scope"`
	CapturedAt          string        `json:"capturedAt"`
	ProviderObservedAt  *string       `json:"providerObservedAt"`
	Availability        Availability  `json:"availability"`
	Completeness        Completeness  `json:"completeness"`
	DirectFields        []DirectField `json:"directFields"`
	Measures            []Measure     `json:"measures"`
	Derivations         []Derivation  `json:"derivations"`
	EvidenceRefs        []string      `json:"evidenceRefs"`
	ErrorCode           *string       `json:"errorCode"`
}

// Scope is the opaque account binding carried in the document.
type Scope struct {
	AccountRef     *string      `json:"accountRef"`
	WorkspaceRef   *string      `json:"workspaceRef"`
	EntitlementRef *string      `json:"entitlementRef"`
	Products       []string     `json:"products"`
	BindingState   BindingState `json:"bindingState"`
}

// Source names the producer kind and version.
type Source struct {
	Kind          SourceKind `json:"kind"`
	Version       string     `json:"version"`
	CapabilityRef string     `json:"capabilityRef"`
}

// DirectField is one allowlisted raw value. LexicalValue keeps the original spelling.
type DirectField struct {
	Field        string       `json:"field"`
	OriginalType OriginalType `json:"originalType"`
	LexicalValue *string      `json:"lexicalValue"`
}

// Window is one independent allowance window.
type Window struct {
	Ref             string         `json:"ref"`
	DurationMinutes *Count         `json:"durationMinutes"`
	StartAt         *string        `json:"startAt"`
	ResetAt         *string        `json:"resetAt"`
	ResetPrecision  ResetPrecision `json:"resetPrecision"`
}

// MeasureValue keeps a state and, only when known, a lexical decimal.
type MeasureValue struct {
	State   ValueState `json:"state"`
	Decimal *string    `json:"decimal"`
}

// Measure is one normalized meter inside an observation.
type Measure struct {
	ID             string       `json:"id"`
	Category       Category     `json:"category"`
	Window         Window       `json:"window"`
	Unit           string       `json:"unit"`
	Currency       *string      `json:"currency"`
	Direction      Direction    `json:"direction"`
	Value          MeasureValue `json:"value"`
	ValidationRef  *string      `json:"validationRef"`
	EvidenceFields []string     `json:"evidenceFields"`
}

// Derivation is a declared formula result. It is provider-reported only as a claim.
type Derivation struct {
	ID        string       `json:"id"`
	Formula   Formula      `json:"formula"`
	InputRefs []string     `json:"inputRefs"`
	Unit      string       `json:"unit"`
	Precision string       `json:"precision"`
	Value     MeasureValue `json:"value"`
}

// ReadResponse is one providerallowance.read.v1 document.
type ReadResponse struct {
	Schema           string         `json:"schema"`
	SnapshotRevision string         `json:"snapshotRevision"`
	ServedAt         string         `json:"servedAt"`
	Items            []ReadItem     `json:"items"`
	SourceStatuses   []SourceStatus `json:"sourceStatuses"`
	NextCursor       *string        `json:"nextCursor"`
}

// ReadItem keeps the original observation beside collector-owned fields.
type ReadItem struct {
	Observation     Observation      `json:"observation"`
	ReceivedAt      string           `json:"receivedAt"`
	Validation      ValidationRecord `json:"validation"`
	Binding         BindingRecord    `json:"binding"`
	Freshness       Freshness        `json:"freshness"`
	CurrentMeasures []CurrentMeasure `json:"currentMeasures"`
}

// ValidationRecord is data on a read document. It is not a trusted registry.
type ValidationRecord struct {
	State          ValidationState `json:"state"`
	ValidationRefs []string        `json:"validationRefs"`
}

// BindingRecord is data on a read document. It does not create trust.
type BindingRecord struct {
	State        BindingState `json:"state"`
	Generation   Count        `json:"generation"`
	AccountRef   *string      `json:"accountRef"`
	WorkspaceRef *string      `json:"workspaceRef"`
}

// Freshness is data on a read document. Decode does not recompute it.
type Freshness struct {
	State     FreshnessState `json:"state"`
	AsOf      string         `json:"asOf"`
	Basis     FreshnessBasis `json:"basis"`
	PolicyRef *string        `json:"policyRef"`
	Reason    string         `json:"reason"`
}

// CurrentMeasure is the read document's current view. Expired current values
// use an unknown decimal while the observation keeps its historical decimal.
type CurrentMeasure struct {
	MeasureID string       `json:"measureId"`
	State     CurrentState `json:"state"`
	Value     MeasureValue `json:"value"`
	Reason    string       `json:"reason"`
}

// SourceStatus separates the last accepted observation from a later attempt.
type SourceStatus struct {
	Provider     string           `json:"provider"`
	ProducerRef  string           `json:"producerRef"`
	Availability Availability     `json:"availability"`
	LastGood     *LastGood        `json:"lastGood"`
	LastAttempt  LastAttempt      `json:"lastAttempt"`
	Freshness    Freshness        `json:"freshness"`
	Validation   ValidationRecord `json:"validation"`
	Binding      BindingRecord    `json:"binding"`
}

// LastGood identifies the last accepted observation and its original times.
type LastGood struct {
	ObservationID      string  `json:"observationId"`
	CapturedAt         string  `json:"capturedAt"`
	ProviderObservedAt *string `json:"providerObservedAt"`
	ReceivedAt         string  `json:"receivedAt"`
}

// LastAttempt is a later collection attempt. It does not replace LastGood.
type LastAttempt struct {
	AttemptedAt string        `json:"attemptedAt"`
	CompletedAt *string       `json:"completedAt"`
	Result      AttemptResult `json:"result"`
	Code        *string       `json:"code"`
}

// TrustedContext is supplied by the caller. It is not read from the observation.
type TrustedContext struct {
	ProducerRef         string
	ProducerIncarnation string
	BindingGeneration   int64
	AccountRef          *string
	WorkspaceRef        *string
	BindingState        BindingState
	Provider            string
	SourceKind          SourceKind
	SourceVersion       string
}

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// DecodeObservation parses one observation under limits.
func DecodeObservation(data []byte, limits ParseLimits) (Observation, error) {
	if err := checkDocument(data, limits); err != nil {
		return Observation{}, err
	}
	var obs Observation
	if err := json.Unmarshal(data, &obs); err != nil {
		return Observation{}, err
	}
	return obs, nil
}

// DecodeReadResponse parses one cached-read document under limits.
func DecodeReadResponse(data []byte, limits ParseLimits) (ReadResponse, error) {
	if err := checkDocument(data, limits); err != nil {
		return ReadResponse{}, err
	}
	var doc ReadResponse
	if err := json.Unmarshal(data, &doc); err != nil {
		return ReadResponse{}, err
	}
	return doc, nil
}

func (c *Count) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("true")) || bytes.Equal(b, []byte("false")) {
		return &DecodeError{Code: "invalid_type", Msg: "integer cannot be a boolean"}
	}
	if len(b) == 0 || b[0] == '"' || b[0] == '{' || b[0] == '[' || bytes.Equal(b, []byte("null")) {
		return &DecodeError{Code: "invalid_type", Msg: "integer has the wrong JSON type"}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var num json.Number
	if err := dec.Decode(&num); err != nil {
		return &DecodeError{Code: "invalid_type", Msg: "integer is invalid"}
	}
	s := num.String()
	if strings.ContainsAny(s, "eE") {
		return &DecodeError{Code: "invalid_type", Msg: "integer cannot use an exponent"}
	}
	if strings.Contains(s, ".") {
		rat, ok := new(big.Rat).SetString(s)
		if !ok || !rat.IsInt() {
			return &DecodeError{Code: "invalid_type", Msg: "integer has a fractional part"}
		}
	}
	whole := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole = s[:i]
	}
	n, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return &DecodeError{Code: "invalid_type", Msg: "integer is out of range"}
	}
	if n < 0 {
		return &DecodeError{Code: "invalid_type", Msg: "integer is negative"}
	}
	*c = Count(n)
	return nil
}

func (o *Observation) UnmarshalJSON(b []byte) error {
	var w observationWire
	if err := strictInto(b, observationKeys, &w); err != nil {
		return err
	}
	next := Observation(w)
	if next.Schema != SchemaObservation {
		return &DecodeError{Code: "invalid_enum", Path: "schema", Msg: "observation schema is not providerallowance.v1"}
	}
	if err := validateRequiredDate(next.CapturedAt, "capturedAt"); err != nil {
		return err
	}
	if err := validateOptionalDate(next.ProviderObservedAt, "providerObservedAt"); err != nil {
		return err
	}
	if !knownAvailability(next.Availability) || !knownCompleteness(next.Completeness) {
		return &DecodeError{Code: "invalid_enum", Path: "availability", Msg: "invalid availability or completeness"}
	}
	*o = next
	return nil
}

func (s *Scope) UnmarshalJSON(b []byte) error {
	var w scopeWire
	if err := strictInto(b, scopeKeys, &w); err != nil {
		return err
	}
	next := Scope(w)
	if !knownBinding(next.BindingState) {
		return &DecodeError{Code: "invalid_enum", Path: "bindingState", Msg: "invalid binding state"}
	}
	*s = next
	return nil
}

func (s *Source) UnmarshalJSON(b []byte) error {
	var w sourceWire
	if err := strictInto(b, sourceKeys, &w); err != nil {
		return err
	}
	next := Source(w)
	if !knownSourceKind(next.Kind) {
		return &DecodeError{Code: "invalid_enum", Path: "kind", Msg: "invalid source kind"}
	}
	*s = next
	return nil
}

func (f *DirectField) UnmarshalJSON(b []byte) error {
	var w directWire
	if err := strictInto(b, directKeys, &w); err != nil {
		return err
	}
	next := DirectField(w)
	if !knownOriginalType(next.OriginalType) {
		return &DecodeError{Code: "invalid_enum", Path: "originalType", Msg: "invalid original type"}
	}
	*f = next
	return nil
}

func (w *Window) UnmarshalJSON(b []byte) error {
	var wire windowWire
	if err := strictInto(b, windowKeys, &wire); err != nil {
		return err
	}
	next := Window(wire)
	if !knownReset(next.ResetPrecision) {
		return &DecodeError{Code: "invalid_enum", Path: "resetPrecision", Msg: "invalid reset precision"}
	}
	if err := validateOptionalDate(next.StartAt, "startAt"); err != nil {
		return err
	}
	if err := validateOptionalDate(next.ResetAt, "resetAt"); err != nil {
		return err
	}
	*w = next
	return nil
}

func (v *MeasureValue) UnmarshalJSON(b []byte) error {
	var w valueWire
	if err := strictInto(b, valueKeys, &w); err != nil {
		return err
	}
	next := MeasureValue(w)
	if !knownValueState(next.State) {
		return &DecodeError{Code: "invalid_enum", Path: "state", Msg: "invalid value state"}
	}
	if next.Decimal != nil && !decimalPattern.MatchString(*next.Decimal) {
		return &DecodeError{Code: "invalid_decimal", Path: "decimal", Msg: "decimal is not a canonical decimal string"}
	}
	*v = next
	return nil
}

func (m *Measure) UnmarshalJSON(b []byte) error {
	var w measureWire
	if err := strictInto(b, measureKeys, &w); err != nil {
		return err
	}
	next := Measure(w)
	if !knownCategory(next.Category) || !knownDirection(next.Direction) {
		return &DecodeError{Code: "invalid_enum", Path: "category", Msg: "invalid measure category or direction"}
	}
	*m = next
	return nil
}

func (d *Derivation) UnmarshalJSON(b []byte) error {
	var w derivationWire
	if err := strictInto(b, derivationKeys, &w); err != nil {
		return err
	}
	next := Derivation(w)
	if !knownFormula(next.Formula) {
		return &DecodeError{Code: "invalid_enum", Path: "formula", Msg: "invalid derivation formula"}
	}
	*d = next
	return nil
}

func (r *ReadResponse) UnmarshalJSON(b []byte) error {
	var w readWire
	if err := strictInto(b, readKeys, &w); err != nil {
		return err
	}
	next := ReadResponse(w)
	if next.Schema != SchemaRead {
		return &DecodeError{Code: "invalid_enum", Path: "schema", Msg: "read schema is not providerallowance.read.v1"}
	}
	if err := validateRequiredDate(next.ServedAt, "servedAt"); err != nil {
		return err
	}
	*r = next
	return nil
}

func (item *ReadItem) UnmarshalJSON(b []byte) error {
	var w itemWire
	if err := strictInto(b, itemKeys, &w); err != nil {
		return err
	}
	next := ReadItem(w)
	if err := validateRequiredDate(next.ReceivedAt, "receivedAt"); err != nil {
		return err
	}
	*item = next
	return nil
}

func (v *ValidationRecord) UnmarshalJSON(b []byte) error {
	var w validationWire
	if err := strictInto(b, validationKeys, &w); err != nil {
		return err
	}
	next := ValidationRecord(w)
	if !knownValidationState(next.State) {
		return &DecodeError{Code: "invalid_enum", Path: "state", Msg: "invalid validation state"}
	}
	*v = next
	return nil
}

func (brec *BindingRecord) UnmarshalJSON(b []byte) error {
	var w bindingWire
	if err := strictInto(b, bindingKeys, &w); err != nil {
		return err
	}
	next := BindingRecord(w)
	if !knownBinding(next.State) {
		return &DecodeError{Code: "invalid_enum", Path: "state", Msg: "invalid binding state"}
	}
	*brec = next
	return nil
}

func (f *Freshness) UnmarshalJSON(b []byte) error {
	var w freshnessWire
	if err := strictInto(b, freshnessKeys, &w); err != nil {
		return err
	}
	next := Freshness(w)
	if !knownFreshness(next.State) || !knownBasis(next.Basis) {
		return &DecodeError{Code: "invalid_enum", Path: "state", Msg: "invalid freshness"}
	}
	if err := validateRequiredDate(next.AsOf, "asOf"); err != nil {
		return err
	}
	*f = next
	return nil
}

func (m *CurrentMeasure) UnmarshalJSON(b []byte) error {
	var w currentWire
	if err := strictInto(b, currentKeys, &w); err != nil {
		return err
	}
	next := CurrentMeasure(w)
	if !knownCurrent(next.State) {
		return &DecodeError{Code: "invalid_enum", Path: "state", Msg: "invalid current measure state"}
	}
	*m = next
	return nil
}

func (s *SourceStatus) UnmarshalJSON(b []byte) error {
	var w statusWire
	if err := strictInto(b, statusKeys, &w); err != nil {
		return err
	}
	next := SourceStatus(w)
	if !knownAvailability(next.Availability) {
		return &DecodeError{Code: "invalid_enum", Path: "availability", Msg: "invalid availability"}
	}
	*s = next
	return nil
}

func (g *LastGood) UnmarshalJSON(b []byte) error {
	var w lastGoodWire
	if err := strictInto(b, lastGoodKeys, &w); err != nil {
		return err
	}
	next := LastGood(w)
	if err := validateRequiredDate(next.CapturedAt, "capturedAt"); err != nil {
		return err
	}
	if err := validateOptionalDate(next.ProviderObservedAt, "providerObservedAt"); err != nil {
		return err
	}
	if err := validateRequiredDate(next.ReceivedAt, "receivedAt"); err != nil {
		return err
	}
	*g = next
	return nil
}

func (a *LastAttempt) UnmarshalJSON(b []byte) error {
	var w attemptWire
	if err := strictInto(b, attemptKeys, &w); err != nil {
		return err
	}
	next := LastAttempt(w)
	if !knownAttempt(next.Result) {
		return &DecodeError{Code: "invalid_enum", Path: "result", Msg: "invalid attempt result"}
	}
	if err := validateRequiredDate(next.AttemptedAt, "attemptedAt"); err != nil {
		return err
	}
	if err := validateOptionalDate(next.CompletedAt, "completedAt"); err != nil {
		return err
	}
	*a = next
	return nil
}

func (o Observation) MarshalJSON() ([]byte, error) {
	w := observationWire(o)
	w.DirectFields = nonNilSlice(w.DirectFields)
	w.Measures = nonNilSlice(w.Measures)
	w.Derivations = nonNilSlice(w.Derivations)
	w.EvidenceRefs = nonNilSlice(w.EvidenceRefs)
	return json.Marshal(w)
}

func (s Scope) MarshalJSON() ([]byte, error) {
	w := scopeWire(s)
	w.Products = nonNilSlice(w.Products)
	return json.Marshal(w)
}

func (m Measure) MarshalJSON() ([]byte, error) {
	w := measureWire(m)
	w.EvidenceFields = nonNilSlice(w.EvidenceFields)
	return json.Marshal(w)
}

func (d Derivation) MarshalJSON() ([]byte, error) {
	w := derivationWire(d)
	w.InputRefs = nonNilSlice(w.InputRefs)
	return json.Marshal(w)
}

func (r ReadResponse) MarshalJSON() ([]byte, error) {
	w := readWire(r)
	w.Items = nonNilSlice(w.Items)
	w.SourceStatuses = nonNilSlice(w.SourceStatuses)
	return json.Marshal(w)
}

func (item ReadItem) MarshalJSON() ([]byte, error) {
	w := itemWire(item)
	w.CurrentMeasures = nonNilSlice(w.CurrentMeasures)
	return json.Marshal(w)
}

func (v ValidationRecord) MarshalJSON() ([]byte, error) {
	w := validationWire(v)
	w.ValidationRefs = nonNilSlice(w.ValidationRefs)
	return json.Marshal(w)
}

func nonNilSlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

type (
	observationWire Observation
	scopeWire       Scope
	sourceWire      Source
	directWire      DirectField
	windowWire      Window
	valueWire       MeasureValue
	measureWire     Measure
	derivationWire  Derivation
	readWire        ReadResponse
	itemWire        ReadItem
	validationWire  ValidationRecord
	bindingWire     BindingRecord
	freshnessWire   Freshness
	currentWire     CurrentMeasure
	statusWire      SourceStatus
	lastGoodWire    LastGood
	attemptWire     LastAttempt
)

var (
	observationKeys = []string{"schema", "observationId", "producerRef", "producerIncarnation", "sequence", "bindingGeneration", "provider", "source", "scope", "capturedAt", "providerObservedAt", "availability", "completeness", "directFields", "measures", "derivations", "evidenceRefs", "errorCode"}
	scopeKeys       = []string{"accountRef", "workspaceRef", "entitlementRef", "products", "bindingState"}
	sourceKeys      = []string{"kind", "version", "capabilityRef"}
	directKeys      = []string{"field", "originalType", "lexicalValue"}
	windowKeys      = []string{"ref", "durationMinutes", "startAt", "resetAt", "resetPrecision"}
	valueKeys       = []string{"state", "decimal"}
	measureKeys     = []string{"id", "category", "window", "unit", "currency", "direction", "value", "validationRef", "evidenceFields"}
	derivationKeys  = []string{"id", "formula", "inputRefs", "unit", "precision", "value"}
	readKeys        = []string{"schema", "snapshotRevision", "servedAt", "items", "sourceStatuses", "nextCursor"}
	itemKeys        = []string{"observation", "receivedAt", "validation", "binding", "freshness", "currentMeasures"}
	validationKeys  = []string{"state", "validationRefs"}
	bindingKeys     = []string{"state", "generation", "accountRef", "workspaceRef"}
	freshnessKeys   = []string{"state", "asOf", "basis", "policyRef", "reason"}
	currentKeys     = []string{"measureId", "state", "value", "reason"}
	statusKeys      = []string{"provider", "producerRef", "availability", "lastGood", "lastAttempt", "freshness", "validation", "binding"}
	lastGoodKeys    = []string{"observationId", "capturedAt", "providerObservedAt", "receivedAt"}
	attemptKeys     = []string{"attemptedAt", "completedAt", "result", "code"}
)

func (l ParseLimits) valid() error {
	if l.MaxBytes < 1 || l.MaxDepth < 1 || l.MaxArrayItems < 1 || l.MaxObjectKeys < 1 || l.MaxStringBytes < 1 {
		return &DecodeError{Code: "limit", Msg: "parse limits must be supplied and positive"}
	}
	return nil
}

func checkDocument(data []byte, limits ParseLimits) error {
	if err := limits.valid(); err != nil {
		return err
	}
	if len(data) == 0 {
		return &DecodeError{Code: "invalid_json", Msg: "document is empty"}
	}
	if len(data) > limits.MaxBytes {
		return &DecodeError{Code: "limit", Msg: fmt.Sprintf("document is %d bytes; the supplied limit is %d", len(data), limits.MaxBytes)}
	}
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(data) {
		return &DecodeError{Code: "invalid_json", Msg: "document must be UTF-8 without a byte order mark"}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walkValue(dec, 0, limits, "$"); err != nil {
		var de *DecodeError
		if errors.As(err, &de) {
			return err
		}
		return &DecodeError{Code: "invalid_json", Msg: err.Error()}
	}
	if dec.More() {
		return &DecodeError{Code: "trailing_data", Msg: "trailing JSON after the first document"}
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return &DecodeError{Code: "trailing_data", Msg: "trailing JSON after the first document"}
	}
	return nil
}

func walkValue(dec *json.Decoder, depth int, lim ParseLimits, path string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		if depth+1 > lim.MaxDepth {
			return &DecodeError{Code: "limit", Path: path, Msg: "max depth exceeded"}
		}
		switch t {
		case '{':
			return walkObject(dec, depth+1, lim, path)
		case '[':
			return walkArray(dec, depth+1, lim, path)
		default:
			return fmt.Errorf("%s has an unexpected delimiter", path)
		}
	case string:
		if len(t) > lim.MaxStringBytes {
			return &DecodeError{Code: "limit", Path: path, Msg: "string exceeds the supplied limit"}
		}
	case json.Number:
		if len(t.String()) > lim.MaxStringBytes {
			return &DecodeError{Code: "limit", Path: path, Msg: "number exceeds the supplied limit"}
		}
	}
	return nil
}

func walkObject(dec *json.Decoder, depth int, lim ParseLimits, path string) error {
	seen := map[string]struct{}{}
	n := 0
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return &DecodeError{Code: "invalid_json", Path: path, Msg: "object key is not a string"}
		}
		if len(key) > lim.MaxStringBytes {
			return &DecodeError{Code: "limit", Path: path, Msg: "object key exceeds the supplied limit"}
		}
		child := path + "." + key
		if _, dup := seen[key]; dup {
			return &DecodeError{Code: "duplicate_key", Path: child, Msg: "duplicate key"}
		}
		seen[key] = struct{}{}
		n++
		if n > lim.MaxObjectKeys {
			return &DecodeError{Code: "limit", Path: path, Msg: "object has too many keys"}
		}
		if err := walkValue(dec, depth, lim, child); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return fmt.Errorf("%s is not closed", path)
	}
	return nil
}

func walkArray(dec *json.Decoder, depth int, lim ParseLimits, path string) error {
	i := 0
	for dec.More() {
		i++
		if i > lim.MaxArrayItems {
			return &DecodeError{Code: "limit", Path: path, Msg: "array exceeds the supplied limit"}
		}
		if err := walkValue(dec, depth, lim, fmt.Sprintf("%s[%d]", path, i-1)); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return err
	}
	if end != json.Delim(']') {
		return fmt.Errorf("%s is not closed", path)
	}
	return nil
}

func strictInto(b []byte, keys []string, dest any) error {
	if err := canonicalKeys(b, keys); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		var de *DecodeError
		if errors.As(err, &de) {
			return err
		}
		msg := err.Error()
		switch {
		case strings.Contains(msg, "unknown field"):
			return &DecodeError{Code: "unknown_field", Msg: msg}
		case strings.Contains(msg, "cannot unmarshal"):
			return &DecodeError{Code: "invalid_type", Msg: msg}
		default:
			return &DecodeError{Code: "invalid_json", Msg: msg}
		}
	}
	return nil
}

// canonicalKeys compares property names with the canonical field list before
// typed decode. encoding/json folds case, including Unicode folds such as
// long s, and DisallowUnknownFields still accepts that alias beside the
// canonical name. Exact duplicate keys are rejected here as well. JSON null
// is rejected for every field the schema does not list as nullable, including
// string-array elements, before encoding/json turns null into an empty string
// or a nil slice. String values are not compared or rewritten.
func canonicalKeys(b []byte, keys []string) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return &DecodeError{Code: "invalid_json", Msg: "expected a JSON object"}
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	nullable, stringArrays := policyFor(keys)
	seen := make(map[string]struct{}, len(keys))
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return &DecodeError{Code: "invalid_json", Msg: err.Error()}
		}
		key, ok := keyTok.(string)
		if !ok {
			return &DecodeError{Code: "invalid_json", Msg: "object key is not a string"}
		}
		if _, dup := seen[key]; dup {
			return &DecodeError{Code: "duplicate_key", Path: key, Msg: "duplicate key"}
		}
		seen[key] = struct{}{}
		if _, ok := allowed[key]; !ok {
			return &DecodeError{Code: "unknown_field", Path: key, Msg: "field is not a canonical name"}
		}
		_, allowNull := nullable[key]
		_, stringArray := stringArrays[key]
		if err := consumeChecked(dec, key, allowNull, stringArray); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Msg: err.Error()}
	}
	if end != json.Delim('}') {
		return &DecodeError{Code: "invalid_json", Msg: "object is not closed"}
	}
	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			return &DecodeError{Code: "missing_field", Path: key, Msg: "required field is missing"}
		}
	}
	return nil
}

func policyFor(keys []string) (map[string]struct{}, map[string]struct{}) {
	switch {
	case sameKeys(keys, observationKeys):
		return keySet("providerObservedAt", "errorCode"), keySet("evidenceRefs")
	case sameKeys(keys, scopeKeys):
		return keySet("accountRef", "workspaceRef", "entitlementRef"), keySet("products")
	case sameKeys(keys, directKeys):
		return keySet("lexicalValue"), nil
	case sameKeys(keys, windowKeys):
		return keySet("durationMinutes", "startAt", "resetAt"), nil
	case sameKeys(keys, valueKeys):
		return keySet("decimal"), nil
	case sameKeys(keys, measureKeys):
		return keySet("currency", "validationRef"), keySet("evidenceFields")
	case sameKeys(keys, derivationKeys):
		return nil, keySet("inputRefs")
	case sameKeys(keys, readKeys):
		return keySet("nextCursor"), nil
	case sameKeys(keys, validationKeys):
		return nil, keySet("validationRefs")
	case sameKeys(keys, bindingKeys):
		return keySet("accountRef", "workspaceRef"), nil
	case sameKeys(keys, freshnessKeys):
		return keySet("policyRef"), nil
	case sameKeys(keys, statusKeys):
		return keySet("lastGood"), nil
	case sameKeys(keys, lastGoodKeys):
		return keySet("providerObservedAt"), nil
	case sameKeys(keys, attemptKeys):
		return keySet("completedAt", "code"), nil
	default:
		return nil, nil
	}
}

func sameKeys(a, b []string) bool {
	return len(a) > 0 && len(a) == len(b) && &a[0] == &b[0]
}

func keySet(keys ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
}

func consumeChecked(dec *json.Decoder, key string, allowNull, stringArray bool) error {
	tok, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Path: key, Msg: err.Error()}
	}
	if tok == nil {
		if !allowNull {
			return &DecodeError{Code: "invalid_type", Path: key, Msg: "null is not allowed"}
		}
		return nil
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		return nil
	}
	if delim == '[' {
		return consumeArray(dec, key, stringArray)
	}
	if delim == '{' {
		return consumeRest(dec, '{')
	}
	return &DecodeError{Code: "invalid_json", Path: key, Msg: "unexpected delimiter"}
}

func consumeArray(dec *json.Decoder, key string, stringsOnly bool) error {
	i := 0
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return &DecodeError{Code: "invalid_json", Path: key, Msg: err.Error()}
		}
		path := fmt.Sprintf("%s[%d]", key, i)
		if tok == nil {
			return &DecodeError{Code: "invalid_type", Path: path, Msg: "null is not allowed"}
		}
		if stringsOnly {
			if _, ok := tok.(string); !ok {
				if delim, ok := tok.(json.Delim); ok {
					if err := consumeRest(dec, delim); err != nil {
						return err
					}
				}
				return &DecodeError{Code: "invalid_type", Path: path, Msg: "array element must be a string"}
			}
		} else if delim, ok := tok.(json.Delim); ok {
			if err := consumeRest(dec, delim); err != nil {
				return err
			}
		}
		i++
	}
	end, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Path: key, Msg: err.Error()}
	}
	if end != json.Delim(']') {
		return &DecodeError{Code: "invalid_json", Path: key, Msg: "array is not closed"}
	}
	return nil
}

func consumeRest(dec *json.Decoder, open json.Delim) error {
	closeDelim := json.Delim('}')
	if open == '[' {
		closeDelim = ']'
	}
	for dec.More() {
		if open == '{' {
			if _, err := dec.Token(); err != nil {
				return &DecodeError{Code: "invalid_json", Msg: err.Error()}
			}
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Msg: err.Error()}
	}
	if end != closeDelim {
		return &DecodeError{Code: "invalid_json", Msg: "object or array is not closed"}
	}
	return nil
}

func skipValue(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Msg: err.Error()}
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return &DecodeError{Code: "invalid_json", Msg: "unexpected delimiter"}
	}
	for dec.More() {
		if delim == '{' {
			if _, err := dec.Token(); err != nil {
				return &DecodeError{Code: "invalid_json", Msg: err.Error()}
			}
		}
		if err := skipValue(dec); err != nil {
			return err
		}
	}
	end, err := dec.Token()
	if err != nil {
		return &DecodeError{Code: "invalid_json", Msg: err.Error()}
	}
	want := json.Delim('}')
	if delim == '[' {
		want = ']'
	}
	if end != want {
		return &DecodeError{Code: "invalid_json", Msg: "object or array is not closed"}
	}
	return nil
}

func validateDateTime(s string) error {
	if s == "" || strings.TrimSpace(s) != s || !rfc3339DateTime(s) {
		return errors.New("invalid date-time")
	}
	return nil
}

// rfc3339DateTime matches the Draft 2020-12 date-time format: RFC 3339
// section 5.6, with T/t and Z/z, a dot fraction of any positive length, and
// second 60 only when the offset places it at UTC 23:59. The input string
// is not rewritten.
func rfc3339DateTime(s string) bool {
	if len(s) < 20 || (s[10] != 'T' && s[10] != 't') {
		return false
	}
	return rfc3339Date(s[:10]) && rfc3339Time(s[11:])
}

func rfc3339Date(s string) bool {
	if len(s) != 10 || s[4] != '-' || s[7] != '-' {
		return false
	}
	if !asciiDigits(s[0:4]) || !asciiDigits(s[5:7]) || !asciiDigits(s[8:10]) {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func rfc3339Time(str string) bool {
	if len(str) < 9 || str[2] != ':' || str[5] != ':' {
		return false
	}
	hour, ok := asciiRange(str[0:2], 0, 23)
	if !ok {
		return false
	}
	minute, ok := asciiRange(str[3:5], 0, 59)
	if !ok {
		return false
	}
	second, ok := asciiRange(str[6:8], 0, 60)
	if !ok {
		return false
	}
	rest := str[8:]
	if len(rest) == 0 {
		return false
	}
	if rest[0] == '.' {
		rest = rest[1:]
		digits := 0
		for len(rest) > 0 && rest[0] >= '0' && rest[0] <= '9' {
			digits++
			rest = rest[1:]
		}
		if digits == 0 {
			return false
		}
	}
	if len(rest) == 0 {
		return false
	}
	if rest[0] == 'Z' || rest[0] == 'z' {
		if len(rest) != 1 {
			return false
		}
	} else {
		if len(rest) != 6 || rest[3] != ':' {
			return false
		}
		sign := 0
		switch rest[0] {
		case '+':
			sign = -1
		case '-':
			sign = 1
		default:
			return false
		}
		zoneHour, ok := asciiRange(rest[1:3], 0, 23)
		if !ok {
			return false
		}
		zoneMinute, ok := asciiRange(rest[4:6], 0, 59)
		if !ok {
			return false
		}
		utcMinutes := (hour*60 + minute) + sign*(zoneHour*60+zoneMinute)
		if utcMinutes < 0 {
			utcMinutes += 24 * 60
		}
		hour, minute = utcMinutes/60, utcMinutes%60
	}
	return second < 60 || (hour == 23 && minute == 59)
}

func asciiDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func asciiRange(s string, min, max int) (int, bool) {
	if !asciiDigits(s) {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < min || n > max {
		return 0, false
	}
	return n, true
}

func validateRequiredDate(s, path string) error {
	if err := validateDateTime(s); err != nil {
		return &DecodeError{Code: "invalid_time", Path: path, Msg: "invalid date-time"}
	}
	return nil
}

func validateOptionalDate(s *string, path string) error {
	if s == nil {
		return nil
	}
	return validateRequiredDate(*s, path)
}

func knownSourceKind(s SourceKind) bool {
	switch s {
	case SourceNativeRPC, SourceNativeDisplay, SourcePassive, SourceSynthetic, SourceAdvisory:
		return true
	default:
		return false
	}
}

func knownAvailability(s Availability) bool {
	switch s {
	case AvailabilityAvailable, AvailabilityPartial, AvailabilityUnavailable, AvailabilityUnconfigured, AvailabilityAuthNeeded, AvailabilityThrottled, AvailabilityUnsupported, AvailabilityConflict:
		return true
	default:
		return false
	}
}

func knownCompleteness(s Completeness) bool {
	switch s {
	case CompletenessComplete, CompletenessPartial, CompletenessUnknown:
		return true
	default:
		return false
	}
}

func knownBinding(s BindingState) bool {
	switch s {
	case BindingVerified, BindingUnknown, BindingMismatch:
		return true
	default:
		return false
	}
}

func knownValueState(s ValueState) bool {
	switch s {
	case ValueKnown, ValueUnknown, ValueNotReported, ValueNotApplicable, ValueUncapped:
		return true
	default:
		return false
	}
}

func knownDirection(s Direction) bool {
	switch s {
	case DirectionUsed, DirectionRemaining, DirectionLimit, DirectionNone, DirectionUnknown:
		return true
	default:
		return false
	}
}

func knownCategory(s Category) bool {
	switch s {
	case CategoryIncluded, CategorySpend, CategoryRateLimit, CategoryLocal, CategoryAdvisory:
		return true
	default:
		return false
	}
}

func knownReset(s ResetPrecision) bool {
	switch s {
	case ResetInstant, ResetDisplayOnly, ResetUnknown, ResetNotApplicable:
		return true
	default:
		return false
	}
}

func knownFormula(s Formula) bool {
	switch s {
	case FormulaComplement, FormulaSum:
		return true
	default:
		return false
	}
}

func knownOriginalType(s OriginalType) bool {
	switch s {
	case TypeString, TypeDecimal, TypeBoolean, TypeNull:
		return true
	default:
		return false
	}
}

func knownCurrent(s CurrentState) bool {
	switch s {
	case CurrentKnown, CurrentUnknown, CurrentExpired, CurrentNotReported, CurrentNotApplicable, CurrentUncapped, CurrentConflict:
		return true
	default:
		return false
	}
}

func knownFreshness(s FreshnessState) bool {
	switch s {
	case FreshFresh, FreshStale, FreshUnknown, FreshExpired:
		return true
	default:
		return false
	}
}

func knownBasis(s FreshnessBasis) bool {
	switch s {
	case BasisProvider, BasisCapture, BasisNone:
		return true
	default:
		return false
	}
}

func knownValidationState(s ValidationState) bool {
	switch s {
	case ValidationSourceFields, ValidationDisplayOnly, ValidationUnverified, ValidationPartial, ValidationRejected, ValidationConflict:
		return true
	default:
		return false
	}
}

func knownAttempt(s AttemptResult) bool {
	switch s {
	case AttemptSuccess, AttemptError, AttemptUnavailable, AttemptUnknown:
		return true
	default:
		return false
	}
}
