package providerallowance

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"
)

// errCodexRawToolReport is the only error CodexRawToolReport returns.
// The text is fixed and contains no input bytes, account, or payload.
var errCodexRawToolReport = errors.New("codex raw tool report is invalid")

// codexRawResetMax is the latest Unix second that formats as an RFC3339
// year through 9999. Larger values are refused rather than rescaled.
const codexRawResetMax int64 = 253402300799

// CodexRawToolReport adapts one nested Codex get_usage_limits tool envelope
// plus the caller-supplied original observation time into the canonical
// current-source report.
//
// primary.usedPercent is usage consumed. Remaining percent is the complement
// derived by CodexToolReport. primary.windowDurationMins is the window length
// in minutes. primary.resetsAt is Unix seconds, written as UTC RFC3339.
// No calendar week is inferred, and ProviderUpdatedAt stays unset.
// observedAt is copied verbatim to RetrievedAt. It is not read from the
// envelope and is not a provider update time.
//
// accountId must be JSON null. That is an unattributed current source.
// Any other account value is refused. This parser does not authenticate the
// bytes, bind an account, or prove that a tool produced them.
//
// The preferred rateLimitsByLimitId.codex object and the legacy rateLimits
// object must each name limitId codex when present. If both are present their
// primary meter, discarded planType, and discarded credit fields must match.
// A different limit id, a non-null secondary window, or a missing primary is
// refused. Credit balances, reset-credit receipts, upsells, and automation
// ceilings are never copied into the allowance percents.
func CodexRawToolReport(raw []byte, observedAt string, selected CurrentSourceSelection, now time.Time) (CurrentSourceReport, error) {
	if _, ok := codexRFC3339(observedAt); !ok {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	limit, err := decodeCodexRawToolReport(raw)
	if err != nil {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	if limit.used == "" || limit.window < 1 || limit.resetAt == "" {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	flat, err := json.Marshal(codexRawFlatObservation{
		Provider:        codexToolProvider,
		ObservedAt:      observedAt,
		UsedPercent:     json.RawMessage(limit.used),
		WindowMinutes:   limit.window,
		ResetsAt:        limit.resetAt,
		Source:          codexToolSource,
		AccountIdentity: codexToolAccount,
		Binding:         codexToolBinding,
	})
	if err != nil {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	report, err := CodexToolReport(flat, selected, now)
	if err != nil {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	if report.ProviderUpdatedAt != nil || report.RetrievedAt != observedAt || report.UsedPercent != limit.used || report.WindowDurationMinutes != limit.window || report.ResetsAt != limit.resetAt || report.RemainingPercentBasis != CurrentSourceRemainingBasis {
		return CurrentSourceReport{}, errCodexRawToolReport
	}
	return report, nil
}

// codexRawFlatObservation is the unattributed input CodexToolReport already
// validates. The source, account, and binding labels are that builder's
// required vocabulary for an unbound snapshot. They are not authentication
// and are not copied onto the canonical report.
type codexRawFlatObservation struct {
	Provider        string          `json:"provider"`
	ObservedAt      string          `json:"observedAt"`
	UsedPercent     json.RawMessage `json:"usedPercent"`
	WindowMinutes   int64           `json:"windowDurationMinutes"`
	ResetsAt        string          `json:"resetsAt"`
	Source          string          `json:"source"`
	AccountIdentity string          `json:"accountIdentity"`
	Binding         string          `json:"binding"`
}

// rawCodexLimit is one codex primary window. Plan and credit fields are kept
// only so preferred and legacy copies can be refused when they disagree.
// They are not allowance.
type rawCodexLimit struct {
	used       string
	window     int64
	reset      int64
	resetAt    string
	plan       string
	hasCredits bool
	unlimited  bool
	balance    string
}

func (l rawCodexLimit) sameMeter(other rawCodexLimit) bool {
	return l.used == other.used && l.window == other.window && l.reset == other.reset && l.plan == other.plan && l.hasCredits == other.hasCredits && l.unlimited == other.unlimited && l.balance == other.balance
}

func decodeCodexRawToolReport(raw []byte) (rawCodexLimit, error) {
	if len(raw) == 0 || len(raw) > codexToolReportMaxBytes || bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) || !utf8.Valid(raw) {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var preferred *rawCodexLimit
	var legacy *rawCodexLimit
	var accountNull bool
	seen, err := forEachRawKey(dec, func(key string) error {
		switch key {
		case "rateLimitsByLimitId":
			limit, limitErr := readPreferredCodexLimit(dec)
			if limitErr != nil {
				return errCodexRawToolReport
			}
			preferred = &limit
			return nil
		case "rateLimits":
			limit, limitErr := parseRawCodexLimit(dec, "")
			if limitErr != nil {
				return errCodexRawToolReport
			}
			legacy = &limit
			return nil
		case "accountId":
			if nullErr := readRawNull(dec); nullErr != nil {
				return errCodexRawToolReport
			}
			accountNull = true
			return nil
		case "ordinaryUsageAllowed", "rateLimitResetCredits", "rateLimitUpsell":
			return readRawNull(dec)
		default:
			if skipErr := skipValue(dec); skipErr != nil {
				return errCodexRawToolReport
			}
			return errCodexRawToolReport
		}
	})
	if err != nil {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if dec.More() {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	var extra json.RawMessage
	if extraErr := dec.Decode(&extra); !errors.Is(extraErr, io.EOF) {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if _, ok := seen["accountId"]; !ok || !accountNull {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if preferred == nil && legacy == nil {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if preferred != nil && legacy != nil && !preferred.sameMeter(*legacy) {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if preferred != nil {
		return *preferred, nil
	}
	return *legacy, nil
}

func readPreferredCodexLimit(dec *json.Decoder) (rawCodexLimit, error) {
	var found *rawCodexLimit
	seen, err := forEachRawKey(dec, func(key string) error {
		if key != codexToolProvider {
			if skipErr := skipValue(dec); skipErr != nil {
				return errCodexRawToolReport
			}
			return errCodexRawToolReport
		}
		limit, limitErr := parseRawCodexLimit(dec, key)
		if limitErr != nil {
			return errCodexRawToolReport
		}
		found = &limit
		return nil
	})
	if err != nil || found == nil {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if _, ok := seen[codexToolProvider]; !ok || len(seen) != 1 {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	return *found, nil
}

func parseRawCodexLimit(dec *json.Decoder, mapKey string) (rawCodexLimit, error) {
	var limit rawCodexLimit
	var limitID string
	var planSet bool
	var primarySet bool
	var creditsSet bool
	seen, err := forEachRawKey(dec, func(key string) error {
		switch key {
		case "limitId":
			value, valueErr := readRawString(dec)
			if valueErr != nil {
				return errCodexRawToolReport
			}
			limitID = value
			return nil
		case "limitName", "normalModelSlug", "secondary", "individualLimit", "spendControlReached", "rateLimitReachedType":
			return readRawNull(dec)
		case "primary":
			if primaryErr := readRawPrimary(dec, &limit); primaryErr != nil {
				return errCodexRawToolReport
			}
			primarySet = true
			return nil
		case "credits":
			if creditErr := readRawCredits(dec, &limit); creditErr != nil {
				return errCodexRawToolReport
			}
			creditsSet = true
			return nil
		case "planType":
			value, valueErr := readRawString(dec)
			if valueErr != nil {
				return errCodexRawToolReport
			}
			limit.plan = value
			planSet = true
			return nil
		default:
			if skipErr := skipValue(dec); skipErr != nil {
				return errCodexRawToolReport
			}
			return errCodexRawToolReport
		}
	})
	if err != nil {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if err := requireRawKeys(seen, "limitId", "limitName", "normalModelSlug", "primary", "secondary", "credits", "individualLimit", "spendControlReached", "planType", "rateLimitReachedType"); err != nil {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	if limitID != codexToolProvider || (mapKey != "" && mapKey != limitID) || !primarySet || !creditsSet || !planSet {
		return rawCodexLimit{}, errCodexRawToolReport
	}
	return limit, nil
}

func readRawPrimary(dec *json.Decoder, limit *rawCodexLimit) error {
	var used string
	var window int64
	var reset int64
	var resetAt string
	seen, err := forEachRawKey(dec, func(key string) error {
		switch key {
		case "usedPercent":
			num, numErr := readRawNumber(dec)
			if numErr != nil {
				return errCodexRawToolReport
			}
			lexical, ok := codexPercentLexical(num)
			if !ok {
				return errCodexRawToolReport
			}
			used = lexical
			return nil
		case "windowDurationMins":
			num, numErr := readRawNumber(dec)
			if numErr != nil {
				return errCodexRawToolReport
			}
			value, ok := codexPositiveInt(num)
			if !ok {
				return errCodexRawToolReport
			}
			window = value
			return nil
		case "resetsAt":
			num, numErr := readRawNumber(dec)
			if numErr != nil {
				return errCodexRawToolReport
			}
			value, text, ok := rawUnixReset(num)
			if !ok {
				return errCodexRawToolReport
			}
			reset = value
			resetAt = text
			return nil
		default:
			if skipErr := skipValue(dec); skipErr != nil {
				return errCodexRawToolReport
			}
			return errCodexRawToolReport
		}
	})
	if err != nil {
		return errCodexRawToolReport
	}
	if err := requireRawKeys(seen, "usedPercent", "windowDurationMins", "resetsAt"); err != nil {
		return errCodexRawToolReport
	}
	limit.used = used
	limit.window = window
	limit.reset = reset
	limit.resetAt = resetAt
	return nil
}

func readRawCredits(dec *json.Decoder, limit *rawCodexLimit) error {
	var hasCredits bool
	var unlimited bool
	var balance string
	seen, err := forEachRawKey(dec, func(key string) error {
		switch key {
		case "hasCredits":
			value, valueErr := readRawBool(dec)
			if valueErr != nil {
				return errCodexRawToolReport
			}
			hasCredits = value
			return nil
		case "unlimited":
			value, valueErr := readRawBool(dec)
			if valueErr != nil {
				return errCodexRawToolReport
			}
			unlimited = value
			return nil
		case "balance":
			value, valueErr := readRawString(dec)
			if valueErr != nil {
				return errCodexRawToolReport
			}
			balance = value
			return nil
		default:
			if skipErr := skipValue(dec); skipErr != nil {
				return errCodexRawToolReport
			}
			return errCodexRawToolReport
		}
	})
	if err != nil {
		return errCodexRawToolReport
	}
	if err := requireRawKeys(seen, "hasCredits", "unlimited", "balance"); err != nil {
		return errCodexRawToolReport
	}
	limit.hasCredits = hasCredits
	limit.unlimited = unlimited
	limit.balance = balance
	return nil
}

func rawUnixReset(num json.Number) (int64, string, bool) {
	seconds, ok := codexPositiveInt(num)
	if !ok || seconds > codexRawResetMax {
		return 0, "", false
	}
	instant := time.Unix(seconds, 0).UTC()
	text := instant.Format(time.RFC3339)
	parsed, ok := codexRFC3339(text)
	if !ok || !parsed.Equal(instant) || parsed.Unix() != seconds {
		return 0, "", false
	}
	return seconds, text, true
}

func forEachRawKey(dec *json.Decoder, visit func(string) error) (map[string]struct{}, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, errCodexRawToolReport
	}
	if tok != json.Delim('{') {
		if delim, ok := tok.(json.Delim); ok {
			_ = consumeRest(dec, delim)
		}
		return nil, errCodexRawToolReport
	}
	seen := make(map[string]struct{})
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return nil, errCodexRawToolReport
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, errCodexRawToolReport
		}
		if _, dup := seen[key]; dup {
			if skipErr := skipValue(dec); skipErr != nil {
				return nil, errCodexRawToolReport
			}
			return nil, errCodexRawToolReport
		}
		seen[key] = struct{}{}
		if visitErr := visit(key); visitErr != nil {
			return nil, errCodexRawToolReport
		}
	}
	end, endErr := dec.Token()
	if endErr != nil || end != json.Delim('}') {
		return nil, errCodexRawToolReport
	}
	return seen, nil
}

func requireRawKeys(seen map[string]struct{}, keys ...string) error {
	for _, key := range keys {
		if _, ok := seen[key]; !ok {
			return errCodexRawToolReport
		}
	}
	return nil
}

func readRawNull(dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return errCodexRawToolReport
	}
	if tok == nil {
		return nil
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
	}
	return errCodexRawToolReport
}

func readRawString(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", errCodexRawToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return "", errCodexRawToolReport
	}
	value, ok := tok.(string)
	if !ok {
		return "", errCodexRawToolReport
	}
	return value, nil
}

func readRawBool(dec *json.Decoder) (bool, error) {
	tok, err := dec.Token()
	if err != nil {
		return false, errCodexRawToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return false, errCodexRawToolReport
	}
	value, ok := tok.(bool)
	if !ok {
		return false, errCodexRawToolReport
	}
	return value, nil
}

func readRawNumber(dec *json.Decoder) (json.Number, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", errCodexRawToolReport
	}
	if delim, ok := tok.(json.Delim); ok {
		_ = consumeRest(dec, delim)
		return "", errCodexRawToolReport
	}
	value, ok := tok.(json.Number)
	if !ok {
		return "", errCodexRawToolReport
	}
	return value, nil
}
