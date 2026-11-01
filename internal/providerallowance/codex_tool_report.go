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
	"unicode"
	"unicode/utf8"
)

const codexToolReportMaxBytes = 16 << 10
const codexToolProvider = "codex"
const codexToolSource = "Codex get_usage_limits tool"
const codexToolAccount = "Unavailable"
const codexToolBinding = "Unconfigured"
const codexToolCredits = "Unknown"
const codexToolMaxAge = 24 * time.Hour
const codexToolIncarnationMax = 256

// errCodexToolReport is the only error CodexToolReport returns. It contains
// no input bytes, path, account, or provider payload.
var errCodexToolReport = errors.New("codex tool report is invalid")

// CodexToolReport sanitizes one retained Codex get_usage_limits observation
// into a current-source report. Publisher, incarnation, and generation come
// from selected. RetrievedAt keeps the original observedAt spelling.
// ProviderUpdatedAt stays unset. Optional ceiling, points, and banked-reset
// fields are checked only so they can be discarded. The call does not prove
// publisher identity, entitlement, or that the bytes were read from Codex.
func CodexToolReport(raw []byte, selected CurrentSourceSelection, now time.Time) (CurrentSourceReport, error) {
	obs, err := decodeCodexToolObservation(raw)
	if err != nil {
		return CurrentSourceReport{}, errCodexToolReport
	}
	var report CurrentSourceReport
	report.Schema = CurrentSourceSchema
	report.PublisherRef = selected.PublisherRef
	report.ProducerIncarnation = selected.ProducerIncarnation
	report.SourceGeneration = selected.SourceGeneration
	report.RetrievedAt = obs.observedAt
	report.UsedPercent = obs.used
	report.RemainingPercent = obs.remaining
	report.RemainingPercentBasis = CurrentSourceRemainingBasis
	report.WindowDurationMinutes = obs.window
	report.ResetsAt = obs.resetsAt
	if !codexSelectionCurrent(report, selected, obs.observed, obs.reset, now) {
		return CurrentSourceReport{}, errCodexToolReport
	}
	if err := ValidateCurrentSource(report, selected, now); err != nil {
		return CurrentSourceReport{}, errCodexToolReport
	}
	encoded, err := json.Marshal(report)
	if err != nil || len(encoded) > currentSourceMaxBytes {
		return CurrentSourceReport{}, errCodexToolReport
	}
	return report, nil
}

type codexToolObservation struct {
	observedAt   string
	observed     time.Time
	hasRetrieved bool
	retrieved    time.Time
	resetsAt     string
	reset        time.Time
	used         string
	remaining    string
	window       int64
	windowSet    bool
	providerOK   bool
	sourceOK     bool
	accountOK    bool
	bindingOK    bool
}

func decodeCodexToolObservation(raw []byte) (codexToolObservation, error) {
	if len(raw) == 0 || len(raw) > codexToolReportMaxBytes || bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(raw) {
		return codexToolObservation{}, errCodexToolReport
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return codexToolObservation{}, errCodexToolReport
	}
	var obs codexToolObservation
	seen := make(map[string]struct{}, 14)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return codexToolObservation{}, errCodexToolReport
		}
		key, ok := keyTok.(string)
		if !ok {
			return codexToolObservation{}, errCodexToolReport
		}
		if _, dup := seen[key]; dup {
			if err := skipValue(dec); err != nil {
				return codexToolObservation{}, errCodexToolReport
			}
			return codexToolObservation{}, errCodexToolReport
		}
		seen[key] = struct{}{}
		if err := codexToolField(&obs, dec, key); err != nil {
			return codexToolObservation{}, errCodexToolReport
		}
	}
	end, err := dec.Token()
	if err != nil || end != json.Delim('}') || dec.More() {
		return codexToolObservation{}, errCodexToolReport
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return codexToolObservation{}, errCodexToolReport
	}
	for _, key := range []string{"provider", "observedAt", "usedPercent", "resetsAt", "source", "accountIdentity", "binding"} {
		if _, ok := seen[key]; !ok {
			return codexToolObservation{}, errCodexToolReport
		}
	}
	if err := obs.complete(); err != nil {
		return codexToolObservation{}, errCodexToolReport
	}
	return obs, nil
}

func codexToolField(obs *codexToolObservation, dec *json.Decoder, key string) error {
	switch key {
	case "provider", "observedAt", "resetsAt", "source", "accountIdentity", "binding", "retrievedAt", "bankedResetCredits", "remainingPercentBasis":
		return obs.readString(dec, key)
	case "usedPercent", "remainingPercent", "automationCeilingPercent", "remainingToCeilingPercentagePoints":
		return obs.readPercent(dec, key)
	case "windowDurationMinutes", "windowDurationMins":
		return obs.readWindow(dec)
	case "toolProvidesRemainingPercent":
		tok, err := dec.Token()
		if err != nil || tok != false {
			return errCodexToolReport
		}
		return nil
	case "providerObservedAt":
		return readCodexNull(dec)
	default:
		if err := skipValue(dec); err != nil {
			return errCodexToolReport
		}
		return errCodexToolReport
	}
}

func (o *codexToolObservation) readString(dec *json.Decoder, key string) error {
	tok, err := dec.Token()
	if err != nil {
		return errCodexToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return errCodexToolReport
	}
	text, ok := tok.(string)
	if !ok || !o.setString(key, text) {
		return errCodexToolReport
	}
	return nil
}

func (o *codexToolObservation) readPercent(dec *json.Decoder, key string) error {
	tok, err := dec.Token()
	if err != nil {
		return errCodexToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return errCodexToolReport
	}
	num, ok := tok.(json.Number)
	if !ok || !o.setPercent(key, num) {
		return errCodexToolReport
	}
	return nil
}

func (o *codexToolObservation) readWindow(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errCodexToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return errCodexToolReport
	}
	num, ok := tok.(json.Number)
	if !ok || !o.setWindow(num) {
		return errCodexToolReport
	}
	return nil
}

func readCodexNull(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil || tok != nil {
		if delim, ok := tok.(json.Delim); ok {
			_ = consumeRest(dec, delim)
		}
		return errCodexToolReport
	}
	return nil
}

func (o *codexToolObservation) setString(key, text string) bool {
	switch key {
	case "provider":
		if text != codexToolProvider {
			return false
		}
		o.providerOK = true
	case "source":
		if text != codexToolSource {
			return false
		}
		o.sourceOK = true
	case "accountIdentity":
		if text != codexToolAccount {
			return false
		}
		o.accountOK = true
	case "binding":
		if text != codexToolBinding {
			return false
		}
		o.bindingOK = true
	case "remainingPercentBasis":
		if text != CurrentSourceRemainingBasis {
			return false
		}
	case "bankedResetCredits":
		if text != codexToolCredits {
			return false
		}
	case "observedAt":
		parsed, ok := codexRFC3339(text)
		if !ok {
			return false
		}
		o.observedAt = text
		o.observed = parsed
	case "resetsAt":
		parsed, ok := codexRFC3339(text)
		if !ok {
			return false
		}
		o.resetsAt = text
		o.reset = parsed
	case "retrievedAt":
		parsed, ok := codexRFC3339(text)
		if !ok {
			return false
		}
		o.hasRetrieved = true
		o.retrieved = parsed
	default:
		return false
	}
	return true
}

func (o *codexToolObservation) setPercent(key string, num json.Number) bool {
	lexical, ok := codexPercentLexical(num)
	if !ok {
		return false
	}
	switch key {
	case "usedPercent":
		o.used = lexical
	case "remainingPercent":
		o.remaining = lexical
	case "automationCeilingPercent", "remainingToCeilingPercentagePoints":
	default:
		return false
	}
	return true
}

func (o *codexToolObservation) setWindow(num json.Number) bool {
	n, ok := codexPositiveInt(num)
	if !ok {
		return false
	}
	if o.windowSet && o.window != n {
		return false
	}
	o.window = n
	o.windowSet = true
	return true
}

func (o *codexToolObservation) complete() error {
	if !o.providerOK || !o.sourceOK || !o.accountOK || !o.bindingOK || !o.windowSet || o.observedAt == "" || o.resetsAt == "" || o.used == "" {
		return errCodexToolReport
	}
	if o.hasRetrieved && !o.retrieved.Equal(o.observed) {
		return errCodexToolReport
	}
	if !o.reset.After(o.observed) {
		return errCodexToolReport
	}
	used, okUsed := new(big.Rat).SetString(o.used)
	if !okUsed {
		return errCodexToolReport
	}
	if o.remaining == "" {
		scale := 0
		if dot := strings.IndexByte(o.used, '.'); dot >= 0 {
			scale = len(o.used) - dot - 1
		}
		complement := new(big.Rat).Sub(big.NewRat(100, 1), used)
		o.remaining = complement.FloatString(scale)
		if strings.Contains(o.remaining, ".") {
			o.remaining = strings.TrimRight(strings.TrimRight(o.remaining, "0"), ".")
		}
	}
	remaining, okRemaining := new(big.Rat).SetString(o.remaining)
	if !okUsed || !okRemaining || new(big.Rat).Add(used, remaining).Cmp(big.NewRat(100, 1)) != 0 {
		return errCodexToolReport
	}
	return nil
}

func codexSelectionCurrent(report CurrentSourceReport, selected CurrentSourceSelection, retrieved, reset, now time.Time) bool {
	if report.Schema != CurrentSourceSchema || selected.PublisherRef != CurrentSourcePublisher || report.PublisherRef != selected.PublisherRef {
		return false
	}
	if report.ProducerIncarnation != selected.ProducerIncarnation || !codexIncarnation(selected.ProducerIncarnation) {
		return false
	}
	if report.SourceGeneration != selected.SourceGeneration || selected.SourceGeneration < 1 {
		return false
	}
	if report.ProviderUpdatedAt != nil || report.RetrievedAt == "" || report.ResetsAt == "" || report.WindowDurationMinutes < 1 {
		return false
	}
	if selected.MaxAge <= 0 || selected.MaxAge > codexToolMaxAge || now.IsZero() {
		return false
	}
	if retrieved.After(now) || now.Sub(retrieved) > selected.MaxAge {
		return false
	}
	if !reset.After(retrieved) || !reset.After(now) {
		return false
	}
	return true
}

func codexIncarnation(s string) bool {
	if len(s) == 0 || len(s) > codexToolIncarnationMax || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func codexRFC3339(text string) (time.Time, bool) {
	if validateDateTime(text) != nil {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func codexPercentLexical(num json.Number) (string, bool) {
	text := num.String()
	if strings.HasPrefix(text, "-") || !decimalPattern.MatchString(text) {
		return "", false
	}
	rat, ok := new(big.Rat).SetString(text)
	if !ok || rat.Cmp(big.NewRat(100, 1)) > 0 {
		return "", false
	}
	return text, true
}

func codexPositiveInt(num json.Number) (int64, bool) {
	text := num.String()
	if text == "" || text[0] < '1' || text[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}
