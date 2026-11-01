package session

import (
	"math/big"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// allowancePercent is the lexical bound for a displayed percent. The rational
// value must also lie in 0..100. Values outside that range are not clamped.
var allowancePercent = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6})?$`)

// allowanceAccountToken is the only account ref copied onto a window.
var allowanceAccountToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// allowanceProviders is the mapped account set. Ids are contract provider
// names. Labels and order are the portal contract in ALLOWANCE-INTERFACE.md.
var allowanceProviders = []struct{ id, label string }{
	{"anthropic", "Claude"},
	{"openai", "Codex"},
	{"cursor", "Cursor"},
}

func disconnectedAllowances() []portal.ProductAllowance {
	return []portal.ProductAllowance{
		{Provider: "Claude", Value: portal.AllowanceNotConnected},
		{Provider: "Codex", Value: portal.AllowanceNotConnected},
	}
}

func mappedAllowances() []portal.ProductAllowance {
	out := disconnectedAllowances()
	return append(out, portal.ProductAllowance{Provider: "Cursor", Value: portal.AllowanceNotConnected})
}

// allowanceFacts is the typed window projection. Field names match the
// additions lane B adds to portal.ProductAllowanceWindow.
type allowanceFacts struct {
	PercentValue   float64
	PercentKnown   bool
	Direction      string
	ResetAt        string
	ObservedAt     string
	WindowKind     string
	ResetPrecision string
	AccountKey     string
}

// MapProductAllowance projects a decoded cached-read contract into account
// allowance views. It performs no I/O, uses no clock or session usage, and never
// evaluates derivations. It does not run providerallowance.UsageJSONCommand.
// Production has no allowance reader and does not call it.
func MapProductAllowance(doc providerallowance.ReadResponse) []portal.ProductAllowance {
	out := mappedAllowances()
	for i, provider := range allowanceProviders {
		a := &out[i]
		a.Value, a.Reason = portal.AllowanceUnknown, "No trusted allowance reported"
		if doc.NextCursor != nil {
			a.Reason = "Partial allowance read"
			continue
		}
		if doc.Schema != providerallowance.SchemaRead {
			a.Reason = "Unsupported allowance document"
			continue
		}
		// Scan the whole provider before trust checks so availability precedence
		// and measure identity never depend on item or status order.
		conflict, unconfigured, duplicate := false, false, false
		seen := map[string]bool{}
		for _, status := range doc.SourceStatuses {
			if status.Provider == provider.id {
				conflict = conflict || status.Availability == providerallowance.AvailabilityConflict
				unconfigured = unconfigured || status.Availability == providerallowance.AvailabilityUnconfigured
			}
		}
		for _, item := range doc.Items {
			if item.Observation.Provider != provider.id {
				continue
			}
			conflict = conflict || item.Observation.Availability == providerallowance.AvailabilityConflict
			unconfigured = unconfigured || item.Observation.Availability == providerallowance.AvailabilityUnconfigured
			for _, measure := range item.Observation.Measures {
				duplicate = duplicate || seen[measure.ID]
				seen[measure.ID] = true
			}
		}
		if conflict {
			a.Reason = "Conflicting allowance reports"
			continue
		}
		if unconfigured {
			a.Value, a.Reason = portal.AllowanceNotConnected, ""
			continue
		}
		if duplicate {
			a.Reason = "Conflicting allowance values"
			continue
		}
		unavailable := false
		statusStale := false
		for _, status := range doc.SourceStatuses {
			if status.Provider != provider.id {
				continue
			}
			if reason := allowanceAvailability(status.Availability); reason != "" {
				a.Reason, unavailable = reason, true
			} else if reason := allowanceTrust(status.Validation.State, status.Binding.State, status.Freshness); reason != "" {
				a.Reason, unavailable = reason, true
			}
			statusStale = statusStale || status.Freshness.State == providerallowance.FreshStale
		}
		if unavailable {
			continue
		}
		var windows []portal.ProductAllowanceWindow
		state := portal.AllowanceMeasured
		if statusStale {
			state = portal.AllowanceStale
		}
		refused := false
		for _, item := range doc.Items {
			if item.Observation.Provider != provider.id {
				continue
			}
			mapped, reason := allowanceItem(item)
			if reason != "" {
				a.Reason, refused = reason, true
				break
			}
			if item.Freshness.State == providerallowance.FreshStale {
				state = portal.AllowanceStale
			}
			windows = append(windows, mapped...)
		}
		if !refused {
			a.Reason = "No percent allowance reported"
			for _, w := range windows {
				if w.Percent != "Unknown" {
					a.Value, a.Reason, a.Windows = state, "", windows
					break
				}
			}
		}
	}
	return out
}

func allowanceAvailability(a providerallowance.Availability) string {
	switch a {
	case providerallowance.AvailabilityAvailable, providerallowance.AvailabilityPartial:
		return ""
	case providerallowance.AvailabilityConflict:
		return "Conflicting allowance reports"
	default:
		return "Allowance unavailable"
	}
}

// allowanceTrust applies the same conservative gates to an item and any
// source status carried beside it. A later refusal cannot be hidden by a value.
func allowanceTrust(validation providerallowance.ValidationState, binding providerallowance.BindingState, freshness providerallowance.Freshness) string {
	if validation != providerallowance.ValidationSourceFields && validation != providerallowance.ValidationDisplayOnly {
		return "Allowance validation unavailable"
	}
	if binding != providerallowance.BindingVerified {
		return "Account binding unverified"
	}
	if freshness.Basis != providerallowance.BasisProvider || (freshness.State != providerallowance.FreshFresh && freshness.State != providerallowance.FreshStale) {
		return "Allowance freshness unknown or expired"
	}
	return ""
}

func allowanceItem(item providerallowance.ReadItem) ([]portal.ProductAllowanceWindow, string) {
	o := item.Observation
	if reason := allowanceAvailability(o.Availability); reason != "" {
		return nil, reason
	}
	if o.Scope.BindingState != providerallowance.BindingVerified {
		return nil, "Account binding unverified"
	}
	if reason := allowanceTrust(item.Validation.State, item.Binding.State, item.Freshness); reason != "" {
		return nil, reason
	}
	source := ""
	switch o.Source.Kind {
	case providerallowance.SourceNativeRPC:
		source = "Provider API"
	case providerallowance.SourceNativeDisplay:
		source = "Provider display"
	case providerallowance.SourcePassive:
		source = "Passive event"
	default:
		return nil, "Source is not a provider measurement"
	}
	// A current conflict refuses the item even without a matching measure.
	for _, current := range item.CurrentMeasures {
		if current.State == providerallowance.CurrentConflict {
			return nil, "Conflicting allowance values"
		}
	}
	if len(o.Measures) == 0 {
		return nil, "No allowance windows reported"
	}
	currents := map[string]providerallowance.CurrentMeasure{}
	for _, current := range item.CurrentMeasures {
		if _, duplicate := currents[current.MeasureID]; duplicate {
			return nil, "Conflicting allowance values"
		}
		currents[current.MeasureID] = current
	}
	accountKey := allowanceAccountKey(item)
	var windows []portal.ProductAllowanceWindow
	for _, m := range o.Measures {
		current, ok := currents[m.ID]
		measureFits := allowancePercentFits(m.Value.Decimal)
		currentFits := allowancePercentFits(current.Value.Decimal)
		validDecimals := measureFits && currentFits
		// Differing decimals refuse when both fit the percent range or neither
		// does; exactly one out-of-range side shows Unknown on its row instead.
		// The comparison is textual, so 62 and 62.00 refuse: the safe direction.
		if m.Value.Decimal != nil && current.Value.Decimal != nil && measureFits == currentFits && *m.Value.Decimal != *current.Value.Decimal {
			return nil, "Conflicting allowance values"
		}
		if m.Unit != providerallowance.UnitPercent || (m.Category != providerallowance.CategoryIncluded && m.Category != providerallowance.CategoryRateLimit) {
			// A measure that is never shown has no Unknown fallback, so any
			// differing decimals refuse, whatever their size or sign.
			if m.Value.Decimal != nil && current.Value.Decimal != nil && *m.Value.Decimal != *current.Value.Decimal {
				return nil, "Conflicting allowance values"
			}
			continue
		}
		window, _ := projectMeasure(m, current, ok, o.ProviderObservedAt, source, accountKey, validDecimals)
		windows = append(windows, window)
	}
	return windows, ""
}

// projectMeasure reads typed measure fields only. It does not read direct
// fields or format a percent by scanning display text.
func projectMeasure(m providerallowance.Measure, current providerallowance.CurrentMeasure, hasCurrent bool, observed *string, source, accountKey string, validDecimals bool) (portal.ProductAllowanceWindow, allowanceFacts) {
	facts := allowanceFacts{
		ObservedAt: allowanceRFC3339(observed),
		WindowKind: allowanceWindowKind(m.Window),
		AccountKey: accountKey,
	}
	suffix := ""
	switch m.Direction {
	case providerallowance.DirectionRemaining:
		facts.Direction, suffix = "remaining", "% left"
	case providerallowance.DirectionUsed:
		facts.Direction, suffix = "used", "% used"
	}
	percent := "Unknown"
	if hasCurrent && current.State == providerallowance.CurrentKnown && current.Value.State == providerallowance.ValueKnown && m.Value.State == providerallowance.ValueKnown && validDecimals && suffix != "" && current.Value.Decimal != nil {
		percent = *current.Value.Decimal + suffix
		facts.PercentKnown = true
		facts.PercentValue = allowancePercentValue(*current.Value.Decimal)
	}
	reset := "Unknown"
	switch m.Window.ResetPrecision {
	case providerallowance.ResetInstant:
		reset = allowanceTime(m.Window.ResetAt)
		if at := allowanceRFC3339(m.Window.ResetAt); at != "" {
			facts.ResetPrecision = "instant"
			facts.ResetAt = at
		}
	case providerallowance.ResetDisplayOnly:
		reset = allowanceTime(m.Window.ResetAt)
		if reset != "Unknown" {
			reset += " (display only)"
			facts.ResetPrecision = "display_only"
		}
	}
	window := portal.ProductAllowanceWindow{
		Percent: percent, Window: allowanceWindow(m.Window),
		Reset: reset, Observed: allowanceTime(observed), Provenance: source,
	}
	stampAllowanceFacts(&window, facts)
	return window, facts
}

// stampAllowanceFacts copies the projection onto exported fields of the same
// names. Fields render.go does not have yet are skipped.
func stampAllowanceFacts(dest any, facts allowanceFacts) {
	v := reflect.ValueOf(dest)
	if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
		return
	}
	v = v.Elem()
	src := reflect.ValueOf(facts)
	for i := 0; i < src.NumField(); i++ {
		name := src.Type().Field(i).Name
		field := v.FieldByName(name)
		in := src.Field(i)
		if !field.IsValid() || !field.CanSet() || field.Type() != in.Type() {
			continue
		}
		field.Set(in)
	}
}

func allowanceAccountKey(item providerallowance.ReadItem) string {
	scope := item.Observation.Scope.AccountRef
	binding := item.Binding.AccountRef
	if scope == nil || binding == nil || *scope == "" || *scope != *binding || !allowanceAccountToken.MatchString(*scope) {
		return ""
	}
	return *scope
}

func allowancePercentFits(decimal *string) bool {
	if decimal == nil || !allowancePercent.MatchString(*decimal) {
		return false
	}
	rat, ok := allowancePercentRat(*decimal)
	if !ok || rat.Sign() < 0 {
		return false
	}
	return rat.Cmp(big.NewRat(100, 1)) <= 0
}

func allowancePercentValue(decimal string) float64 {
	rat, ok := allowancePercentRat(decimal)
	if !ok {
		return 0
	}
	value, _ := rat.Float64()
	return value
}

// allowancePercentRat parses a lexical percent. Leading zeros stay in the
// display string; the rational value does not depend on them.
func allowancePercentRat(decimal string) (*big.Rat, bool) {
	whole, frac, hasFrac := decimal, "", false
	if i := strings.IndexByte(decimal, '.'); i >= 0 {
		whole, frac, hasFrac = decimal[:i], decimal[i+1:], true
	}
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	canonical := whole
	if hasFrac {
		canonical += "." + frac
	}
	return new(big.Rat).SetString(canonical)
}

func allowanceParsed(raw *string) (time.Time, bool) {
	if raw == nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.ToUpper(*raw))
	if err != nil || at.IsZero() {
		return time.Time{}, false
	}
	return at.UTC(), true
}

func allowanceTime(raw *string) string {
	at, ok := allowanceParsed(raw)
	if !ok {
		return "Unknown"
	}
	return at.Format("2 Jan 15:04 UTC")
}

func allowanceRFC3339(raw *string) string {
	at, ok := allowanceParsed(raw)
	if !ok {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

func allowanceWindow(w providerallowance.Window) string {
	amount, unit, ok := allowanceDuration(w)
	if !ok {
		return "Unknown"
	}
	if amount != 1 {
		unit += "s"
	}
	return strconv.FormatInt(amount, 10) + " " + unit
}

// allowanceWindowKind is the duration unit. Week and month are never invented
// from a ref, a round week of minutes, or display text.
func allowanceWindowKind(w providerallowance.Window) string {
	_, unit, ok := allowanceDuration(w)
	if !ok {
		return ""
	}
	return unit
}

func allowanceDuration(w providerallowance.Window) (amount int64, unit string, ok bool) {
	if w.DurationMinutes == nil || *w.DurationMinutes <= 0 {
		return 0, "", false
	}
	minutes := int64(*w.DurationMinutes)
	amount, unit = minutes, "minute"
	if minutes%1440 == 0 {
		amount, unit = minutes/1440, "day"
	} else if minutes%60 == 0 {
		amount, unit = minutes/60, "hour"
	}
	return amount, unit, true
}
