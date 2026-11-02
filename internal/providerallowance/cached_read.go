package providerallowance

import (
	"strings"
	"time"
)

// ProjectCachedRead degrades one cached read for a serving clock.
// It changes freshness, current-measure state, and derived status binding.
// Observation timestamps, receipts, provider observed times, and raw
// observation decimals stay as stored. A receipt ahead of the serving clock
// expires freshness without moving those originals. Payload binding and
// freshness labels are not authority: a provider is current only when exactly
// one caller-owned context validates it. Current known and uncapped values are
// withheld when trust, freshness, or a passed reset blocks them. A nil
// document or a paged document is left unchanged.
func ProjectCachedRead(doc *ReadResponse, contexts []TrustedContext, reg Registry, maxAge time.Duration, maxAgeSet bool, now time.Time) {
	if doc == nil || doc.NextCursor != nil {
		return
	}
	untrusted := map[string]bool{}
	trustedStatus := map[string]TrustedContext{}
	freshHold := map[string]Freshness{}
	for i := range doc.Items {
		item := &doc.Items[i]
		provider := item.Observation.Provider
		matched := cachedContexts(contexts, provider)
		if len(matched) != 1 || !cachedObservationTrusted(item.Observation, matched[0], reg) {
			item.CollectionFreshness = nil
			cachedMarkItem(item)
			cachedWithholdCurrent(item)
			untrusted[provider] = true
			continue
		}
		item.CollectionFreshness = officialCollectionFreshness(item.Observation, item.ReceivedAt, maxAge, now)
		if !maxAgeSet {
			item.CollectionFreshness = nil
		}
		trustedStatus[provider] = matched[0]
		cachedAlignTrust(item, matched[0])
		cachedPreserveKnownConflicts(item)
		cachedWithholdHistorical(item, matched[0], reg)
		cachedDegrade(&item.Freshness, item.Observation.ProviderObservedAt, item.ReceivedAt, maxAge, maxAgeSet, now)
		freshHold[provider] = item.Freshness
		if cachedFreshnessBlocks(item.Freshness) {
			cachedWithholdCurrent(item)
			continue
		}
		cachedExpireResets(item, now)
		cachedPreserveConflicts(item)
	}
	for i := range doc.SourceStatuses {
		status := &doc.SourceStatuses[i]
		if untrusted[status.Provider] {
			cachedMarkStatus(status)
			continue
		}
		if ctx, ok := trustedStatus[status.Provider]; ok {
			cachedAlignStatus(status, ctx)
		} else if status.Binding.State == BindingVerified {
			status.Binding = BindingRecord{State: BindingUnknown}
		}
		if held, ok := freshHold[status.Provider]; ok {
			if cachedFreshnessBlocks(held) {
				cachedReleaseUnconfigured(status, held)
				cachedExpireLabel(&status.Freshness)
			} else if held.State == FreshStale && status.Freshness.State == FreshFresh {
				status.Freshness.State = FreshStale
			}
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
			if i >= len(assessment.Measures) || !assessment.Measures[i].SemanticsEvidenced || !assessment.Measures[i].InRange {
				return false
			}
		}
	}
	return true
}

// A validated historical measure is trusted history, not current capacity.
// Keep its raw observation/binding for last-reported display while refusing
// a payload that labels that same measure current, even with provider time.
func cachedWithholdHistorical(item *ReadItem, trusted TrustedContext, reg Registry) {
	assessment := ValidateObservation(item.Observation, trusted, reg)
	for i, measure := range item.Observation.Measures {
		if i >= len(assessment.Measures) || assessment.Measures[i].Actionable {
			continue
		}
		for j := range item.CurrentMeasures {
			current := &item.CurrentMeasures[j]
			if current.MeasureID == measure.ID && (current.State == CurrentKnown || current.State == CurrentUncapped) {
				cachedClearCurrent(current)
			}
		}
	}
}

// CurrentMeasuresSupported binds every actionable projection to its original
// observation. Withheld measures may remain unknown without inventing capacity.
func CurrentMeasuresSupported(item ReadItem) bool {
	for _, current := range item.CurrentMeasures {
		if !currentMeasureSupported(current, item.Observation.Measures) {
			return false
		}
	}
	return true
}

func currentMeasureSupported(current CurrentMeasure, measures []Measure) bool {
	if current.State != CurrentKnown && current.State != CurrentUncapped {
		return true
	}
	for _, measure := range measures {
		if measure.ID != current.MeasureID {
			continue
		}
		if current.State == CurrentUncapped {
			return current.Value.State == ValueUncapped && current.Value.Decimal == nil && measure.Value.State == ValueUncapped && measure.Value.Decimal == nil
		}
		return current.Value.State == ValueKnown && current.Value.Decimal != nil && measure.Value.State == ValueKnown && measure.Value.Decimal != nil && *current.Value.Decimal == *measure.Value.Decimal
	}
	return false
}

// A known disagreement is evidence of a conflict even after its reset. Keep
// that conflict visible before applying clock-based withholding.
func cachedPreserveKnownConflicts(item *ReadItem) {
	for i := range item.CurrentMeasures {
		current := &item.CurrentMeasures[i]
		if current.State != CurrentKnown || current.Value.State != ValueKnown || current.Value.Decimal == nil {
			continue
		}
		for _, measure := range item.Observation.Measures {
			if measure.ID == current.MeasureID && measure.Value.State == ValueKnown && measure.Value.Decimal != nil && *measure.Value.Decimal != *current.Value.Decimal {
				current.State = CurrentConflict
				current.Value = MeasureValue{State: ValueUnknown}
				break
			}
		}
	}
}

func cachedPreserveConflicts(item *ReadItem) {
	for i := range item.CurrentMeasures {
		current := &item.CurrentMeasures[i]
		if !currentMeasureSupported(*current, item.Observation.Measures) {
			current.State = CurrentConflict
			current.Value = MeasureValue{State: ValueUnknown}
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

func cachedAlignStatus(status *SourceStatus, trusted TrustedContext) {
	status.ProducerRef = trusted.ProducerRef
	status.Binding.State = BindingVerified
	status.Binding.Generation = Count(trusted.BindingGeneration)
	status.Binding.AccountRef = cloneString(trusted.AccountRef)
	status.Binding.WorkspaceRef = cloneString(trusted.WorkspaceRef)
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
		if status.Binding.State == BindingVerified {
			status.Binding = BindingRecord{State: BindingUnknown}
		}
		return
	}
	if status.Availability == AvailabilityUnconfigured {
		status.Availability = AvailabilityAvailable
	}
	status.Validation.State = ValidationRejected
	status.Binding = BindingRecord{State: BindingMismatch}
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

func cachedDegrade(fresh *Freshness, observed *string, received string, maxAge time.Duration, maxAgeSet bool, now time.Time) {
	fresh.Reason = ""
	if !maxAgeSet || maxAge < 0 || now.IsZero() {
		cachedExpireLabel(fresh)
		return
	}
	if receipt, ok := cachedTimeString(received); ok && receipt.After(now) {
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

func cachedWithholdCurrent(item *ReadItem) {
	for j := range item.CurrentMeasures {
		switch item.CurrentMeasures[j].State {
		case CurrentKnown, CurrentUncapped:
			cachedClearCurrent(&item.CurrentMeasures[j])
		}
	}
}

func cachedClearCurrent(current *CurrentMeasure) {
	current.State = CurrentUnknown
	current.Value = MeasureValue{State: ValueUnknown}
	current.Reason = ""
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
			state := item.CurrentMeasures[j].State
			if item.CurrentMeasures[j].MeasureID != measure.ID || (state != CurrentKnown && state != CurrentUncapped) {
				continue
			}
			cachedClearCurrent(&item.CurrentMeasures[j])
		}
	}
}

func cachedTimeString(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	return cachedTime(&raw)
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
