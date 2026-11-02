package providerallowance

import (
	"strings"
	"time"
)

// ProjectCachedRead degrades one cached read for a serving clock.
// It changes freshness and current-measure state only. Observation timestamps,
// receipts, and provider observed times stay as stored. Payload binding and
// freshness labels are not authority: a provider is current only when exactly
// one caller-owned context validates it. A nil document or a paged document is
// left unchanged.
func ProjectCachedRead(doc *ReadResponse, contexts []TrustedContext, reg Registry, maxAge time.Duration, maxAgeSet bool, now time.Time) {
	if doc == nil || doc.NextCursor != nil {
		return
	}
	untrusted := map[string]bool{}
	freshHold := map[string]Freshness{}
	for i := range doc.Items {
		item := &doc.Items[i]
		provider := item.Observation.Provider
		matched := cachedContexts(contexts, provider)
		if len(matched) != 1 || !cachedObservationTrusted(item.Observation, matched[0], reg) {
			cachedMarkItem(item)
			untrusted[provider] = true
			continue
		}
		cachedAlignTrust(item, matched[0])
		cachedPreserveConflicts(item)
		cachedDegrade(&item.Freshness, item.Observation.ProviderObservedAt, maxAge, maxAgeSet, now)
		if cachedFreshnessBlocks(item.Freshness) {
			freshHold[provider] = item.Freshness
			continue
		}
		cachedExpireResets(item, now)
	}
	for i := range doc.SourceStatuses {
		status := &doc.SourceStatuses[i]
		if untrusted[status.Provider] {
			cachedMarkStatus(status)
			continue
		}
		if held, ok := freshHold[status.Provider]; ok {
			cachedReleaseUnconfigured(status, held)
		}
		if at, ok := cachedTime(&status.Freshness.AsOf); !ok || at.After(now) {
			cachedExpireLabel(&status.Freshness)
		}
	}
}

func cachedContexts(contexts []TrustedContext, provider string) []TrustedContext {
	var matched []TrustedContext
	for _, ctx := range contexts {
		if ctx.Provider == provider {
			matched = append(matched, ctx)
		}
	}
	return matched
}

func cachedObservationTrusted(obs Observation, trusted TrustedContext, reg Registry) bool {
	assessment := ValidateObservation(obs, trusted, reg)
	if len(assessment.Issues) != 0 || assessment.TrustedBinding != BindingVerified || assessment.ObservationBinding != BindingVerified {
		return false
	}
	for i, measure := range obs.Measures {
		if measure.Value.State == ValueKnown || measure.Value.State == ValueUncapped {
			if i >= len(assessment.Measures) || assessment.Measures[i].HistoricalOnly || !assessment.Measures[i].Actionable {
				return false
			}
		}
	}
	return true
}

func cachedPreserveConflicts(item *ReadItem) {
	for i := range item.CurrentMeasures {
		current := &item.CurrentMeasures[i]
		if current.State != CurrentKnown || current.Value.State != ValueKnown || current.Value.Decimal == nil {
			continue
		}
		for _, measure := range item.Observation.Measures {
			if measure.ID == current.MeasureID && measure.Value.State == ValueKnown && measure.Value.Decimal != nil && *measure.Value.Decimal != *current.Value.Decimal {
				current.State = CurrentConflict
				break
			}
		}
	}
}

func cachedAlignTrust(item *ReadItem, trusted TrustedContext) {
	item.Validation.State = ValidationSourceFields
	item.Binding.State = BindingVerified
	item.Binding.Generation = Count(trusted.BindingGeneration)
	item.Binding.AccountRef = cloneString(trusted.AccountRef)
	item.Binding.WorkspaceRef = cloneString(trusted.WorkspaceRef)
	item.Freshness.Reason = ""
}

func cachedMarkItem(item *ReadItem) {
	if item.Observation.Availability == AvailabilityUnconfigured {
		item.Observation.Availability = AvailabilityAvailable
	}
	item.Observation.Scope.BindingState = BindingMismatch
	item.Validation.State = ValidationRejected
	item.Binding.State = BindingMismatch
	item.Freshness.Reason = ""
}

func cachedMarkStatus(status *SourceStatus) {
	if status.Availability == AvailabilityConflict {
		return
	}
	if status.Availability == AvailabilityUnconfigured {
		status.Availability = AvailabilityAvailable
	}
	status.Validation.State = ValidationRejected
	status.Binding.State = BindingMismatch
	status.Freshness.Reason = ""
}

func cachedReleaseUnconfigured(status *SourceStatus, held Freshness) {
	if status.Availability != AvailabilityUnconfigured {
		return
	}
	status.Availability = AvailabilityAvailable
	status.Freshness.State = held.State
	status.Freshness.Basis = held.Basis
	status.Freshness.Reason = ""
}

func cachedDegrade(fresh *Freshness, observed *string, maxAge time.Duration, maxAgeSet bool, now time.Time) {
	fresh.Reason = ""
	if !maxAgeSet || maxAge < 0 || now.IsZero() {
		cachedExpireLabel(fresh)
		return
	}
	at, ok := cachedTime(observed)
	if !ok || at.After(now) {
		cachedExpireLabel(fresh)
		return
	}
	asOf, valid := cachedTime(&fresh.AsOf)
	if !valid || asOf.After(now) || at.After(asOf) {
		cachedExpireLabel(fresh)
		return
	}
	if now.Sub(at) > maxAge && fresh.State == FreshFresh {
		fresh.State = FreshStale
	}
}

func cachedExpireLabel(fresh *Freshness) {
	if fresh.State == FreshFresh || fresh.State == FreshStale {
		fresh.State = FreshExpired
	}
}

func cachedFreshnessBlocks(fresh Freshness) bool {
	if fresh.Basis != BasisProvider {
		return true
	}
	return fresh.State != FreshFresh && fresh.State != FreshStale
}

func cachedExpireResets(item *ReadItem, now time.Time) {
	if now.IsZero() {
		return
	}
	for _, measure := range item.Observation.Measures {
		if measure.Window.ResetPrecision != ResetInstant {
			continue
		}
		at, ok := cachedTime(measure.Window.ResetAt)
		if !ok || now.Before(at) {
			continue
		}
		for j := range item.CurrentMeasures {
			if item.CurrentMeasures[j].MeasureID != measure.ID || item.CurrentMeasures[j].State != CurrentKnown {
				continue
			}
			item.CurrentMeasures[j].State = CurrentUnknown
			item.CurrentMeasures[j].Value = MeasureValue{State: ValueUnknown}
			item.CurrentMeasures[j].Reason = ""
		}
	}
}

func cachedTime(raw *string) (time.Time, bool) {
	if raw == nil {
		return time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, strings.ToUpper(*raw))
	if err != nil || at.IsZero() {
		return time.Time{}, false
	}
	return at.UTC(), true
}
