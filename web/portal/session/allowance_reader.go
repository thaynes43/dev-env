package session

import (
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
	"github.com/thaynes43/dev-env/web/portal"
)

// AllowanceReader projects one caller-supplied cached allowance read.
// Allowances uses now only to degrade age and exact reset instants.
// The snapshot's provider observed time stays as stored.
type AllowanceReader interface {
	Allowances(now time.Time) []portal.ProductAllowance
}

// AllowanceReadPolicy is the caller-supplied max age for one cached read.
// MaxAge applies only when Set is true and MaxAge is zero or positive.
// An unset or negative policy expires a cached fresh or stale label.
type AllowanceReadPolicy struct {
	MaxAge time.Duration
	Set    bool
}

// cachedAllowanceReader is an immutable providerallowance.read.v1 snapshot.
// Construction copies the bytes and trust contexts. Allowances decodes those
// bytes again and projects a copy. It does not fetch or write the snapshot.
type cachedAllowanceReader struct {
	raw      []byte
	limits   providerallowance.ParseLimits
	contexts []providerallowance.TrustedContext
	reg      providerallowance.Registry
	policy   AllowanceReadPolicy
}

// NewCachedAllowanceReader stores one cached read for later projection.
// limits, contexts, reg, and policy are caller inputs. Account, workspace,
// producer, and generation come from contexts. Validation and binding labels
// in raw are not authority. A decode failure still returns a reader; its
// projection uses the fixed unsupported-document allowance.
func NewCachedAllowanceReader(raw []byte, limits providerallowance.ParseLimits, contexts []providerallowance.TrustedContext, reg providerallowance.Registry, policy AllowanceReadPolicy) AllowanceReader {
	copied := make([]providerallowance.TrustedContext, len(contexts))
	for i, ctx := range contexts {
		copied[i] = cloneTrustedContext(ctx)
	}
	return &cachedAllowanceReader{
		raw:      append([]byte(nil), raw...),
		limits:   limits,
		contexts: copied,
		reg:      reg,
		policy:   policy,
	}
}

// SetAllowanceReader installs reader for later SessionSource reads.
// Nil leaves the disconnected allowances from the session projection.
// Set it before Read. Assembly and production config do not call it.
func (s *SessionSource) SetAllowanceReader(reader AllowanceReader) {
	if s == nil {
		return
	}
	s.allowance = reader
}

func applyCachedAllowance(page portal.ProductPage, reader AllowanceReader, now func() time.Time) portal.ProductPage {
	if reader == nil || now == nil {
		return page
	}
	allowances := reader.Allowances(now())
	if allowances == nil {
		return page
	}
	page.Allowance = allowances
	if page.Usage != nil {
		page.Usage.Allowance = allowances
	}
	return page
}

func (r *cachedAllowanceReader) Allowances(now time.Time) []portal.ProductAllowance {
	if r == nil {
		return disconnectedAllowances()
	}
	doc, err := providerallowance.DecodeReadResponse(r.raw, r.limits)
	if err != nil {
		return MapProductAllowance(providerallowance.ReadResponse{})
	}
	projectCachedAllowance(&doc, r.contexts, r.reg, r.policy, now)
	return MapProductAllowance(doc)
}

func projectCachedAllowance(doc *providerallowance.ReadResponse, contexts []providerallowance.TrustedContext, reg providerallowance.Registry, policy AllowanceReadPolicy, now time.Time) {
	if doc.NextCursor != nil {
		return
	}
	untrusted := map[string]bool{}
	freshHold := map[string]providerallowance.Freshness{}
	for i := range doc.Items {
		item := &doc.Items[i]
		provider := item.Observation.Provider
		matched := contextsForProvider(contexts, provider)
		if len(matched) != 1 || !observationTrusted(item.Observation, matched[0], reg) {
			markItemUntrusted(item)
			untrusted[provider] = true
			continue
		}
		alignItemTrust(item, matched[0])
		degradeFreshness(&item.Freshness, item.Observation.ProviderObservedAt, policy, now)
		if freshnessBlocks(item.Freshness) {
			freshHold[provider] = item.Freshness
			continue
		}
		expirePastResets(item, now)
	}
	for i := range doc.SourceStatuses {
		status := &doc.SourceStatuses[i]
		if untrusted[status.Provider] {
			markStatusUntrusted(status)
			continue
		}
		if held, ok := freshHold[status.Provider]; ok {
			releaseUnconfigured(status, held)
		}
		if at, ok := allowanceParsed(&status.Freshness.AsOf); !ok || at.After(now) {
			expireFreshLabel(&status.Freshness)
		}
	}
}

func contextsForProvider(contexts []providerallowance.TrustedContext, provider string) []providerallowance.TrustedContext {
	var matched []providerallowance.TrustedContext
	for _, ctx := range contexts {
		if ctx.Provider == provider {
			matched = append(matched, ctx)
		}
	}
	return matched
}

func observationTrusted(obs providerallowance.Observation, trusted providerallowance.TrustedContext, reg providerallowance.Registry) bool {
	assessment := providerallowance.ValidateObservation(obs, trusted, reg)
	return len(assessment.Issues) == 0 &&
		assessment.TrustedBinding == providerallowance.BindingVerified &&
		assessment.ObservationBinding == providerallowance.BindingVerified
}

func alignItemTrust(item *providerallowance.ReadItem, trusted providerallowance.TrustedContext) {
	item.Validation.State = providerallowance.ValidationSourceFields
	item.Binding.State = providerallowance.BindingVerified
	item.Binding.Generation = providerallowance.Count(trusted.BindingGeneration)
	item.Binding.AccountRef = cloneAllowanceString(trusted.AccountRef)
	item.Binding.WorkspaceRef = cloneAllowanceString(trusted.WorkspaceRef)
	item.Freshness.Reason = ""
}

func markItemUntrusted(item *providerallowance.ReadItem) {
	if item.Observation.Availability == providerallowance.AvailabilityUnconfigured {
		item.Observation.Availability = providerallowance.AvailabilityAvailable
	}
	item.Observation.Scope.BindingState = providerallowance.BindingMismatch
	item.Validation.State = providerallowance.ValidationRejected
	item.Binding.State = providerallowance.BindingMismatch
	item.Freshness.Reason = ""
}

func markStatusUntrusted(status *providerallowance.SourceStatus) {
	if status.Availability == providerallowance.AvailabilityConflict {
		return
	}
	if status.Availability == providerallowance.AvailabilityUnconfigured {
		status.Availability = providerallowance.AvailabilityAvailable
	}
	status.Validation.State = providerallowance.ValidationRejected
	status.Binding.State = providerallowance.BindingMismatch
	status.Freshness.Reason = ""
}

func releaseUnconfigured(status *providerallowance.SourceStatus, held providerallowance.Freshness) {
	if status.Availability != providerallowance.AvailabilityUnconfigured {
		return
	}
	status.Availability = providerallowance.AvailabilityAvailable
	status.Freshness.State = held.State
	status.Freshness.Basis = held.Basis
	status.Freshness.Reason = ""
}

func degradeFreshness(fresh *providerallowance.Freshness, observed *string, policy AllowanceReadPolicy, now time.Time) {
	fresh.Reason = ""
	if !policyUsable(policy) || now.IsZero() {
		expireFreshLabel(fresh)
		return
	}
	at, ok := allowanceParsed(observed)
	if !ok || at.After(now) {
		expireFreshLabel(fresh)
		return
	}
	asOf, valid := allowanceParsed(&fresh.AsOf)
	if !valid || asOf.After(now) || at.After(asOf) {
		expireFreshLabel(fresh)
		return
	}
	if now.Sub(at) > policy.MaxAge && fresh.State == providerallowance.FreshFresh {
		fresh.State = providerallowance.FreshStale
	}
}

func policyUsable(policy AllowanceReadPolicy) bool {
	return policy.Set && policy.MaxAge >= 0
}

func expireFreshLabel(fresh *providerallowance.Freshness) {
	if fresh.State == providerallowance.FreshFresh || fresh.State == providerallowance.FreshStale {
		fresh.State = providerallowance.FreshExpired
	}
}

func freshnessBlocks(fresh providerallowance.Freshness) bool {
	if fresh.Basis != providerallowance.BasisProvider {
		return true
	}
	return fresh.State != providerallowance.FreshFresh && fresh.State != providerallowance.FreshStale
}

// expirePastResets clears the current value of a measure whose exact reset
// instant is at or before now. The observation decimal stays. Display-only
// reset text is not an instant. Sibling measures are left in place.
func expirePastResets(item *providerallowance.ReadItem, now time.Time) {
	if now.IsZero() {
		return
	}
	for _, measure := range item.Observation.Measures {
		if measure.Window.ResetPrecision != providerallowance.ResetInstant {
			continue
		}
		at, ok := allowanceParsed(measure.Window.ResetAt)
		if !ok || now.Before(at) {
			continue
		}
		for j := range item.CurrentMeasures {
			if item.CurrentMeasures[j].MeasureID != measure.ID || item.CurrentMeasures[j].State != providerallowance.CurrentKnown {
				continue
			}
			item.CurrentMeasures[j].State = providerallowance.CurrentUnknown
			item.CurrentMeasures[j].Value = providerallowance.MeasureValue{State: providerallowance.ValueUnknown}
			item.CurrentMeasures[j].Reason = ""
		}
	}
}

func cloneTrustedContext(in providerallowance.TrustedContext) providerallowance.TrustedContext {
	out := in
	out.AccountRef = cloneAllowanceString(in.AccountRef)
	out.WorkspaceRef = cloneAllowanceString(in.WorkspaceRef)
	return out
}

func cloneAllowanceString(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}
