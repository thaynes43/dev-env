package session

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

var allowancePercent = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6})?$`)

func disconnectedAllowances() []portal.ProductAllowance {
	return []portal.ProductAllowance{
		{Provider: "Claude", Value: portal.AllowanceNotConnected},
		{Provider: "Codex", Value: portal.AllowanceNotConnected},
	}
}

// MapProductAllowance projects a decoded cached-read contract into account
// allowance views. It performs no I/O, uses no clock or session usage, and never
// evaluates derivations. Production has no allowance reader and does not call it.
func MapProductAllowance(doc providerallowance.ReadResponse) []portal.ProductAllowance {
	out := disconnectedAllowances()
	for i, provider := range []string{"anthropic", "openai"} {
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
			if status.Provider == provider {
				conflict = conflict || status.Availability == providerallowance.AvailabilityConflict
				unconfigured = unconfigured || status.Availability == providerallowance.AvailabilityUnconfigured
			}
		}
		for _, item := range doc.Items {
			if item.Observation.Provider != provider {
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
			if status.Provider != provider {
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
			if item.Observation.Provider != provider {
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
	var windows []portal.ProductAllowanceWindow
	for _, m := range o.Measures {
		if m.Unit != providerallowance.UnitPercent || (m.Category != providerallowance.CategoryIncluded && m.Category != providerallowance.CategoryRateLimit) {
			continue
		}
		current, ok := currents[m.ID]
		if current.State == providerallowance.CurrentConflict {
			return nil, "Conflicting allowance values"
		}
		if m.Value.Decimal != nil && current.Value.Decimal != nil && *m.Value.Decimal != *current.Value.Decimal {
			return nil, "Conflicting allowance values"
		}
		suffix := ""
		switch m.Direction {
		case providerallowance.DirectionRemaining:
			suffix = "% left"
		case providerallowance.DirectionUsed:
			suffix = "% used"
		}
		percent := "Unknown"
		if ok && current.State == providerallowance.CurrentKnown && current.Value.State == providerallowance.ValueKnown && current.Value.Decimal != nil && allowancePercent.MatchString(*current.Value.Decimal) && m.Value.State == providerallowance.ValueKnown && m.Value.Decimal != nil && allowancePercent.MatchString(*m.Value.Decimal) && suffix != "" {
			percent = *current.Value.Decimal + suffix
		}
		reset := "Unknown"
		switch m.Window.ResetPrecision {
		case providerallowance.ResetInstant:
			reset = allowanceTime(m.Window.ResetAt)
		case providerallowance.ResetDisplayOnly:
			reset = allowanceTime(m.Window.ResetAt)
			if reset != "Unknown" {
				reset += " (display only)"
			}
		}
		windows = append(windows, portal.ProductAllowanceWindow{
			Percent: percent, Window: allowanceWindow(m.Window),
			Reset: reset, Observed: allowanceTime(o.ProviderObservedAt), Provenance: source,
		})
	}
	return windows, ""
}

func allowanceTime(raw *string) string {
	if raw == nil {
		return "Unknown"
	}
	at, err := time.Parse(time.RFC3339Nano, strings.ToUpper(*raw))
	if err != nil || at.IsZero() {
		return "Unknown"
	}
	return at.UTC().Format("2 Jan 15:04 UTC")
}

func allowanceWindow(w providerallowance.Window) string {
	if w.DurationMinutes == nil || *w.DurationMinutes <= 0 {
		return "Unknown"
	}
	minutes := int64(*w.DurationMinutes)
	amount, unit := minutes, "minute"
	if minutes%1440 == 0 {
		amount, unit = minutes/1440, "day"
	} else if minutes%60 == 0 {
		amount, unit = minutes/60, "hour"
	}
	if amount != 1 {
		unit += "s"
	}
	return strconv.FormatInt(amount, 10) + " " + unit
}
