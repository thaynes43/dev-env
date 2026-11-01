package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// CurrentSourceSchema is the closed document name for one Codex
	// get_usage_limits current-source snapshot.
	CurrentSourceSchema = "codex.get_usage_limits.current_source.v1"
	// CurrentSourcePublisher is the only publisher a current-source snapshot may name.
	CurrentSourcePublisher = "codex-get-usage-limits-observation-publisher"
)

const currentSourceMaxBytes = 16 * 1024

const currentSourceMaxAge = 24 * time.Hour

const currentSourceMaxWindowMinutes int64 = 525600

const currentSourceMaxIdentifier = 128

// Fixed codes for DecodeCurrentSource and ValidateCurrentSource.
// The error text is the code alone and does not contain document bytes.
var ErrCurrentSourceSize = errors.New("current_source_size")

var ErrCurrentSourceJSON = errors.New("current_source_json")

var ErrCurrentSourceDuplicate = errors.New("current_source_duplicate_key")

var ErrCurrentSourceUnknown = errors.New("current_source_unknown_field")

var ErrCurrentSourceMissing = errors.New("current_source_missing_field")

var ErrCurrentSourceNull = errors.New("current_source_null")

var ErrCurrentSourceType = errors.New("current_source_type")

var ErrCurrentSourceTrailing = errors.New("current_source_trailing")

var ErrCurrentSourceNow = errors.New("current_source_now")

var ErrCurrentSourceSchema = errors.New("current_source_schema")

var ErrCurrentSourcePublisher = errors.New("current_source_publisher")

var ErrCurrentSourceIdentifier = errors.New("current_source_identifier")

var ErrCurrentSourceGeneration = errors.New("current_source_generation")

var ErrCurrentSourceSelection = errors.New("current_source_selection")

var ErrCurrentSourceMaxAge = errors.New("current_source_max_age")

var ErrCurrentSourcePercentGrammar = errors.New("current_source_percent_grammar")

var ErrCurrentSourcePercentRange = errors.New("current_source_percent_range")

var ErrCurrentSourcePercentSum = errors.New("current_source_percent_sum")

var ErrCurrentSourceTime = errors.New("current_source_time")

var ErrCurrentSourceFutureRetrieval = errors.New("current_source_future_retrieval")

var ErrCurrentSourceFutureProvider = errors.New("current_source_future_provider")

var ErrCurrentSourceProviderAfterRetrieval = errors.New("current_source_provider_after_retrieval")

var ErrCurrentSourceResetOrder = errors.New("current_source_reset_order")

var ErrCurrentSourceReset = errors.New("current_source_reset")

var ErrCurrentSourceDuration = errors.New("current_source_duration")

var ErrCurrentSourceExpired = errors.New("current_source_expired")

var currentSourceKeys = []string{
	"schema",
	"publisherRef",
	"producerIncarnation",
	"sourceGeneration",
	"retrievedAt",
	"providerUpdatedAt",
	"usedPercent",
	"remainingPercent",
	"windowDurationMinutes",
	"resetsAt",
}

// CurrentSourceReport is one sanitized Codex get_usage_limits snapshot.
// Stable account identity is absent. This value is not an Observation,
// not a ReadItem, and not a bound entitlement.
type CurrentSourceReport struct {
	Schema                string  `json:"schema"`
	PublisherRef          string  `json:"publisherRef"`
	ProducerIncarnation   string  `json:"producerIncarnation"`
	SourceGeneration      int64   `json:"sourceGeneration"`
	RetrievedAt           string  `json:"retrievedAt"`
	ProviderUpdatedAt     *string `json:"providerUpdatedAt"`
	UsedPercent           string  `json:"usedPercent"`
	RemainingPercent      string  `json:"remainingPercent"`
	WindowDurationMinutes int64   `json:"windowDurationMinutes"`
	ResetsAt              string  `json:"resetsAt"`
}

// CurrentSourceSelection is the caller-supplied publisher, incarnation,
// generation, and maximum retrieval age. It is not read from the document.
type CurrentSourceSelection struct {
	PublisherRef        string
	ProducerIncarnation string
	SourceGeneration    int64
	MaxAge              time.Duration
}

// DecodeCurrentSource parses one closed current-source document.
// Input larger than 16 KiB is refused. Object keys must match the canonical
// names exactly, including case. providerUpdatedAt is required and may be
// null. Every other value is required and non-null. Trailing values are
// refused. The returned error is a fixed code.
func DecodeCurrentSource(raw []byte) (CurrentSourceReport, error) {
	if len(raw) > currentSourceMaxBytes {
		return CurrentSourceReport{}, ErrCurrentSourceSize
	}
	if len(raw) == 0 || bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(raw) {
		return CurrentSourceReport{}, ErrCurrentSourceJSON
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return CurrentSourceReport{}, ErrCurrentSourceJSON
	}
	var report CurrentSourceReport
	seen := make(map[string]struct{}, len(currentSourceKeys))
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return CurrentSourceReport{}, ErrCurrentSourceJSON
		}
		key, ok := keyTok.(string)
		if !ok {
			return CurrentSourceReport{}, ErrCurrentSourceJSON
		}
		if _, dup := seen[key]; dup {
			return CurrentSourceReport{}, ErrCurrentSourceDuplicate
		}
		seen[key] = struct{}{}
		if !currentSourceKnown(key) {
			return CurrentSourceReport{}, ErrCurrentSourceUnknown
		}
		if fieldErr := applyCurrentSourceField(dec, key, &report); fieldErr != nil {
			return CurrentSourceReport{}, fieldErr
		}
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') {
		return CurrentSourceReport{}, ErrCurrentSourceJSON
	}
	if dec.More() {
		return CurrentSourceReport{}, ErrCurrentSourceTrailing
	}
	var extra json.RawMessage
	if extraErr := dec.Decode(&extra); !errors.Is(extraErr, io.EOF) {
		return CurrentSourceReport{}, ErrCurrentSourceTrailing
	}
	for _, key := range currentSourceKeys {
		if _, ok := seen[key]; !ok {
			return CurrentSourceReport{}, ErrCurrentSourceMissing
		}
	}
	return report, nil
}

// UnmarshalJSON decodes with DecodeCurrentSource so encoding/json cannot
// case-fold keys or keep unknown fields.
func (r *CurrentSourceReport) UnmarshalJSON(raw []byte) error {
	decoded, err := DecodeCurrentSource(raw)
	if err != nil {
		return err
	}
	*r = decoded
	return nil
}

// ValidateCurrentSource checks one snapshot against the selected source and now.
// Schema and publisher are fixed. Incarnation and generation must match the
// selection exactly. A zero now, a future retrieval, a reset at or before now,
// or a retrieval older than MaxAge invalidates the current meter. Historical
// percent strings are left unchanged and are not rewritten to a full window.
func ValidateCurrentSource(report CurrentSourceReport, selected CurrentSourceSelection, now time.Time) error {
	if now.IsZero() {
		return ErrCurrentSourceNow
	}
	if report.Schema != CurrentSourceSchema {
		return ErrCurrentSourceSchema
	}
	if report.PublisherRef != CurrentSourcePublisher {
		return ErrCurrentSourcePublisher
	}
	if err := currentSourceIdentifier(report.ProducerIncarnation); err != nil {
		return err
	}
	if err := currentSourceIdentifier(selected.PublisherRef); err != nil {
		return err
	}
	if err := currentSourceIdentifier(selected.ProducerIncarnation); err != nil {
		return err
	}
	if report.SourceGeneration <= 0 || selected.SourceGeneration <= 0 {
		return ErrCurrentSourceGeneration
	}
	if selected.PublisherRef != report.PublisherRef || selected.ProducerIncarnation != report.ProducerIncarnation || selected.SourceGeneration != report.SourceGeneration {
		return ErrCurrentSourceSelection
	}
	if selected.MaxAge <= 0 || selected.MaxAge > currentSourceMaxAge {
		return ErrCurrentSourceMaxAge
	}
	used, err := currentSourcePercent(report.UsedPercent)
	if err != nil {
		return err
	}
	remaining, err := currentSourcePercent(report.RemainingPercent)
	if err != nil {
		return err
	}
	if new(big.Rat).Add(used, remaining).Cmp(big.NewRat(100, 1)) != 0 {
		return ErrCurrentSourcePercentSum
	}
	retrieved, err := parseCurrentSourceTime(report.RetrievedAt)
	if err != nil {
		return err
	}
	var provider *time.Time
	if report.ProviderUpdatedAt != nil {
		parsed, providerErr := parseCurrentSourceTime(*report.ProviderUpdatedAt)
		if providerErr != nil {
			return providerErr
		}
		provider = &parsed
	}
	resets, err := parseCurrentSourceTime(report.ResetsAt)
	if err != nil {
		return err
	}
	if retrieved.After(now) {
		return ErrCurrentSourceFutureRetrieval
	}
	if provider != nil && provider.After(now) {
		return ErrCurrentSourceFutureProvider
	}
	if provider != nil && provider.After(retrieved) {
		return ErrCurrentSourceProviderAfterRetrieval
	}
	if !resets.After(retrieved) {
		return ErrCurrentSourceResetOrder
	}
	if !resets.After(now) {
		return ErrCurrentSourceReset
	}
	if report.WindowDurationMinutes <= 0 || report.WindowDurationMinutes > currentSourceMaxWindowMinutes {
		return ErrCurrentSourceDuration
	}
	if retrieved.Add(selected.MaxAge).Before(now) {
		return ErrCurrentSourceExpired
	}
	return nil
}

func currentSourceKnown(key string) bool {
	for _, candidate := range currentSourceKeys {
		if candidate == key {
			return true
		}
	}
	return false
}

func applyCurrentSourceField(dec *json.Decoder, key string, report *CurrentSourceReport) error {
	tok, err := dec.Token()
	if err != nil {
		return ErrCurrentSourceJSON
	}
	if _, isDelim := tok.(json.Delim); isDelim {
		return ErrCurrentSourceType
	}
	switch key {
	case "schema":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.Schema = value
	case "publisherRef":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.PublisherRef = value
	case "producerIncarnation":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.ProducerIncarnation = value
	case "retrievedAt":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.RetrievedAt = value
	case "usedPercent":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.UsedPercent = value
	case "remainingPercent":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.RemainingPercent = value
	case "resetsAt":
		value, valueErr := currentSourceRequiredString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.ResetsAt = value
	case "providerUpdatedAt":
		value, valueErr := currentSourceNullableString(tok)
		if valueErr != nil {
			return valueErr
		}
		report.ProviderUpdatedAt = value
	case "sourceGeneration":
		value, valueErr := currentSourceRequiredInt(tok)
		if valueErr != nil {
			return valueErr
		}
		report.SourceGeneration = value
	case "windowDurationMinutes":
		value, valueErr := currentSourceRequiredInt(tok)
		if valueErr != nil {
			return valueErr
		}
		report.WindowDurationMinutes = value
	default:
		return ErrCurrentSourceUnknown
	}
	return nil
}

func currentSourceRequiredString(tok json.Token) (string, error) {
	if tok == nil {
		return "", ErrCurrentSourceNull
	}
	value, ok := tok.(string)
	if !ok {
		return "", ErrCurrentSourceType
	}
	return value, nil
}

func currentSourceNullableString(tok json.Token) (*string, error) {
	if tok == nil {
		return nil, nil
	}
	value, ok := tok.(string)
	if !ok {
		return nil, ErrCurrentSourceType
	}
	copied := value
	return &copied, nil
}

func currentSourceRequiredInt(tok json.Token) (int64, error) {
	if tok == nil {
		return 0, ErrCurrentSourceNull
	}
	number, ok := tok.(json.Number)
	if !ok || !canonicalJSONInteger(number.String()) {
		return 0, ErrCurrentSourceType
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return 0, ErrCurrentSourceType
	}
	return parsed, nil
}

func canonicalJSONInteger(s string) bool {
	if s == "" || s == "-" {
		return false
	}
	i := 0
	if s[0] == '-' {
		i = 1
	}
	if s[i] == '0' {
		return len(s) == i+1 && i == 0
	}
	if s[i] < '1' || s[i] > '9' {
		return false
	}
	for i++; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func currentSourceIdentifier(s string) error {
	if len(s) == 0 || len(s) > currentSourceMaxIdentifier {
		return ErrCurrentSourceIdentifier
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7E {
			return ErrCurrentSourceIdentifier
		}
	}
	return nil
}

func currentSourcePercent(s string) (*big.Rat, error) {
	if len(s) == 0 || len(s) > currentSourceMaxBytes || !canonicalNonNegativeDecimal(s) {
		return nil, ErrCurrentSourcePercentGrammar
	}
	rat, ok := new(big.Rat).SetString(s)
	if !ok || rat.Sign() < 0 {
		return nil, ErrCurrentSourcePercentGrammar
	}
	if rat.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, ErrCurrentSourcePercentRange
	}
	return rat, nil
}

func canonicalNonNegativeDecimal(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '0' {
		i = 1
	} else if s[0] < '1' || s[0] > '9' {
		return false
	} else {
		i = 1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	}
	if i == len(s) {
		return true
	}
	if s[i] != '.' || i+1 == len(s) {
		return false
	}
	for i++; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseCurrentSourceTime(s string) (time.Time, error) {
	if len(s) < 20 || len(s) > 64 || strings.TrimSpace(s) != s {
		return time.Time{}, ErrCurrentSourceTime
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, ErrCurrentSourceTime
	}
	return parsed, nil
}
