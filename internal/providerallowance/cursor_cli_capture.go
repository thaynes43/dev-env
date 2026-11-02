package providerallowance

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"
)

const (
	CursorCLICaptureSchema    = "cursor.cli.usage-capture.v1"
	CursorCLIQualifiedVersion = "2026.10.22-27dcf5f"
	CursorCLISourceVersion    = "cursor-cli-usage-sanitized-2026.11.02"
	CursorCLICapability       = "cursor-cli:usage-display"
)

var ErrCursorCLICapture = errors.New("cursor CLI usage capture unavailable")
var cursorEmailHash = regexp.MustCompile(`^cursor-email-sha256:[a-f0-9]{64}$`)

// CursorCLIUsageCapture is a sanitized observation of the qualified normal
// interactive builtin Usage display, not a raw Dashboard RPC. The local
// acquisition interval is retained because no exact display/provider timestamp
// is supplied. Every scalar is display precision; no generic Monthly caption,
// omitted API percentage or matching email proves a window/shared entitlement.
type CursorCLIUsageCapture struct {
	Schema                 string  `json:"schema"`
	RendererVersion        string  `json:"rendererVersion"`
	CollectionStartedAt    string  `json:"collectionStartedAt"`
	ReceivedAt             string  `json:"receivedAt"`
	ProviderObservedAt     *string `json:"providerObservedAt"`
	AccountRef             *string `json:"accountRef"`
	Plan                   string  `json:"plan"`
	IncludedPercentDisplay *int    `json:"includedPercentDisplay"`
	PercentPrecision       string  `json:"percentPrecision"`
	ResetMonthDay          *string `json:"resetMonthDay"`
	Period                 string  `json:"period"`
}

func projectCursorCLIUsage(raw []byte, selected CursorDisplaySelection) (ReadItem, error) {
	fail := func() (ReadItem, error) { return ReadItem{}, ErrCursorCLICapture }
	keys := []string{"schema", "rendererVersion", "collectionStartedAt", "receivedAt", "providerObservedAt", "accountRef", "plan", "includedPercentDisplay", "percentPrecision", "resetMonthDay", "period"}
	var report CursorCLIUsageCapture
	if checkDocument(raw, FixtureParseLimits) != nil || !ExactObjectKeys(raw, keys) || strictDecode(raw, &report) != nil {
		return fail()
	}
	if report.Schema != CursorCLICaptureSchema || report.RendererVersion != CursorCLIQualifiedVersion || report.PercentPrecision != "rounded_integer_display" || report.Period != "not_exposed" || report.ProviderObservedAt != nil || !cursorDisplayToken(report.Plan) || report.AccountRef != nil && !cursorEmailHash.MatchString(*report.AccountRef) {
		return fail()
	}
	start, e := time.Parse(time.RFC3339Nano, report.CollectionStartedAt)
	received, err := time.Parse(time.RFC3339Nano, report.ReceivedAt)
	if e != nil || err != nil || start.After(received) || received.Sub(start) > 5*time.Minute {
		return fail()
	}
	if report.IncludedPercentDisplay != nil && (*report.IncludedPercentDisplay < 0 || *report.IncludedPercentDisplay > 100) {
		return fail()
	}
	// The qualified UI can fall back absent percentages to zero. Retain its
	// literal display as evidence, but zero is not a known quota observation.
	var used *string
	if report.IncludedPercentDisplay != nil && *report.IncludedPercentDisplay > 0 {
		v := strconv.Itoa(*report.IncludedPercentDisplay)
		used = &v
	}
	if report.ResetMonthDay != nil && !cursorDisplayMonthDay(*report.ResetMonthDay) {
		return fail()
	}
	availability := "partial"
	var code *string
	if used == nil {
		v := "display_unknown"
		code = &v
	}
	body, err := json.Marshal(map[string]any{"schema": CursorDisplayInputSchema, "capturedAt": report.CollectionStartedAt, "route": "cursor_plan_usage", "product": "cursor", "plan": report.Plan, "accountRef": report.AccountRef, "availability": availability, "errorCode": code, "windowLabel": nil, "durationMinutes": nil, "usedPercent": used, "resetDisplayDate": nil, "resetDisplayText": report.ResetMonthDay, "spendAmount": nil, "spendCurrency": nil})
	if err != nil {
		return fail()
	}
	item, err := projectCursorDisplay(body, selected, received, true)
	if err != nil {
		return fail()
	}
	item.Observation.Source.Version = CursorCLISourceVersion
	item.Observation.Source.CapabilityRef = CursorCLICapability
	// Record rounding and interval semantics for readers; this is display-only
	// evidence, not an additional meter or a current provider freshness claim.
	for _, field := range []struct{ name, value string }{{"rendererVersion", report.RendererVersion}, {"percentPrecision", report.PercentPrecision}, {"captureTimeBasis", "local_acquisition_start"}, {"period", "not_exposed"}} {
		value := field.value
		item.Observation.DirectFields = append(item.Observation.DirectFields, DirectField{Field: field.name, OriginalType: TypeString, LexicalValue: &value})
	}
	if report.IncludedPercentDisplay != nil {
		value := strconv.Itoa(*report.IncludedPercentDisplay) + "% used"
		item.Observation.DirectFields = append(item.Observation.DirectFields, DirectField{Field: "includedPercentDisplay", OriginalType: TypeString, LexicalValue: &value})
	}
	// Stable IDs include these typed precision facts; a later serving clock
	// cannot create a different original observation or a provider refresh.
	item.Observation.ObservationID = "cursor-cli-pending"
	identity, _ := json.Marshal(item.Observation)
	item.Observation.ObservationID = captureDigestID("cursor-cli", identity)
	return item, nil
}

// MarshalCursorCLIUsage validates a caller-sanitized display capture without
// acquiring a provider or inferring missing precision. It preserves zero/null
// as source evidence; the normalized quota for ambiguous zero remains unknown.
func MarshalCursorCLIUsage(report CursorCLIUsageCapture) ([]byte, error) {
	raw, err := json.Marshal(report)
	if err != nil {
		return nil, ErrCursorCLICapture
	}
	selection := CursorDisplaySelection{ProducerRef: "cursor-cli-validation", ProducerIncarnation: "capture-validation", Sequence: 1, BindingState: BindingUnknown, MaxAge: 30 * time.Minute}
	if _, err = projectCursorCLIUsage(raw, selection); err != nil {
		return nil, ErrCursorCLICapture
	}
	return raw, nil
}
