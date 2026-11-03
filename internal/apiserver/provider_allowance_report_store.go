package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const (
	// AggregateAllowanceNamespace is the only namespace of the common slot.
	AggregateAllowanceNamespace = "dev-env-system"
	// AggregateAllowanceName is the only ConfigMap of the common slot.
	AggregateAllowanceName = "dev-env-provider-allowances"
	// AggregateAllowanceDataKey is the single document entry in that ConfigMap.
	AggregateAllowanceDataKey = "report.json"
	// AggregateAllowanceFieldManager owns that entry and not the object's metadata.
	AggregateAllowanceFieldManager = "dev-env-aggregate-allowance"
)

// Sentinel errors for the common allowance slot. Text is the code.
// None of them contain report bytes.
var (
	ErrAggregateAbsent      = errors.New("aggregate_allowance_absent")
	ErrAggregateConflict    = errors.New("aggregate_allowance_conflict")
	ErrAggregateRejected    = errors.New("aggregate_allowance_rejected")
	ErrAggregateDigest      = errors.New("aggregate_allowance_digest")
	ErrAggregateUnavailable = errors.New("aggregate_allowance_unavailable")
)

// AggregateAllowanceView is one read of the slot. Raw is set only for a
// canonical document. The read does not write the object.
type AggregateAllowanceView struct {
	Found           bool
	Suppressed      bool
	Raw             []byte
	ResourceVersion string
}

// AggregateAllowanceTrust is the caller-owned input copied for one projection.
type AggregateAllowanceTrust struct {
	Contexts []providerallowance.TrustedContext
	Registry providerallowance.Registry
	MaxAge   time.Duration
}

// AggregateAllowanceReportStore keeps one canonical providerallowance.read.v1
// document in an existing ConfigMap. It does not create or delete the object,
// and it does not store credentials, raw account ids, or provider history.
type AggregateAllowanceReportStore struct {
	Writer    objectUpdater
	Live      objectGetter
	Namespace string
	Name      string
	DataKey   string
	Contexts  []providerallowance.TrustedContext
	Registry  providerallowance.Registry
	MaxAge    time.Duration
}

func (s *AggregateAllowanceReportStore) configured() bool {
	return s.ready() && s.Registry != nil && s.MaxAge > 0 && s.MaxAge <= 1800*time.Second && len(s.Contexts) <= 8
}

func (s *AggregateAllowanceReportStore) ready() bool {
	return s != nil && s.Writer != nil && s.Live != nil && s.Namespace != "" && s.Name != "" && s.DataKey != ""
}

func (s *AggregateAllowanceReportStore) trust() AggregateAllowanceTrust {
	if s == nil {
		return AggregateAllowanceTrust{}
	}
	copied := make([]providerallowance.TrustedContext, len(s.Contexts))
	for i, ctx := range s.Contexts {
		copied[i] = ctx
		copied[i].AccountRef = cloneAggregateString(ctx.AccountRef)
		copied[i].WorkspaceRef = cloneAggregateString(ctx.WorkspaceRef)
	}
	return AggregateAllowanceTrust{Contexts: copied, Registry: s.Registry, MaxAge: s.MaxAge}
}

// Read returns the stored canonical document. A missing object is an empty
// view. Bytes that are not canonical are suppressed and are not rewritten.
func (s *AggregateAllowanceReportStore) Read(ctx context.Context) (AggregateAllowanceView, error) {
	if !s.ready() {
		return AggregateAllowanceView{}, ErrAggregateUnavailable
	}
	cm, err := s.live(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return AggregateAllowanceView{}, nil
		}
		return AggregateAllowanceView{}, ErrAggregateUnavailable
	}
	view := AggregateAllowanceView{ResourceVersion: cm.ResourceVersion}
	raw := slotBytes(cm, s.DataKey)
	if raw == "" {
		return view, nil
	}
	view.Found = true
	if !canonicalAllowance(raw) {
		view.Suppressed = true
		return view, nil
	}
	view.Raw = append([]byte(nil), raw...)
	return view, nil
}

// Put stores one newer canonical report, or leaves the object untouched.
// An equal body is a replay and does not write. Trust comes from the store,
// not from payload binding or freshness labels.
func (s *AggregateAllowanceReportStore) Put(ctx context.Context, raw []byte, now time.Time) error {
	if s == nil || !s.configured() || now.IsZero() {
		return ErrAggregateUnavailable
	}
	raw = append([]byte(nil), raw...)
	doc, err := decodeCanonicalAllowance(string(raw))
	if err != nil {
		return err
	}
	if err := s.accept(doc, now); err != nil {
		return err
	}
	cm, err := s.live(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ErrAggregateAbsent
		}
		return ErrAggregateUnavailable
	}
	existing := slotBytes(cm, s.DataKey)
	if existing == string(raw) {
		return nil
	}
	if !cm.DeletionTimestamp.IsZero() || (cm.Immutable != nil && *cm.Immutable) || cm.ResourceVersion == "" {
		return ErrAggregateRejected
	}
	if existing != "" {
		prev, decErr := decodeCanonicalAllowance(existing)
		if decErr != nil {
			return ErrAggregateRejected
		}
		if err := preserveAllowance(prev, doc); err != nil {
			return err
		}
	}
	// Any newly introduced capture must pass caller-owned provenance and clock
	// checks even when its current values are withheld. Repeating an existing
	// capture for a failed attempt keeps the original trusted watermark.
	previous := map[string]providerallowance.ReadItem{}
	if existing != "" {
		prior, _ := decodeCanonicalAllowance(existing)
		previous, _ = indexAllowanceItems(prior)
	}
	for _, item := range doc.Items {
		if before, ok := previous[item.Observation.Provider]; ok && sameAllowanceCapture(before, item) {
			continue
		}
		var matched *providerallowance.TrustedContext
		for i := range s.Contexts {
			if s.Contexts[i].Provider == item.Observation.Provider {
				matched = &s.Contexts[i]
			}
		}
		if matched == nil || acceptCapture(item, *matched, s.Registry, now) != nil {
			return ErrAggregateRejected
		}
	}
	priorRV := cm.ResourceVersion
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[s.DataKey] = string(raw)
	if err := s.Writer.Update(ctx, cm, client.FieldOwner(AggregateAllowanceFieldManager)); err != nil {
		if apierrors.IsConflict(err) {
			return ErrAggregateConflict
		}
		return s.confirm(ctx, string(raw), priorRV)
	}
	if slotBytes(cm, s.DataKey) != string(raw) {
		return s.confirm(ctx, string(raw), priorRV)
	}
	return nil
}

func (s *AggregateAllowanceReportStore) accept(doc providerallowance.ReadResponse, now time.Time) error {
	if doc.NextCursor != nil || doc.Schema != providerallowance.SchemaRead {
		return ErrAggregateRejected
	}
	if _, err := time.Parse(time.RFC3339Nano, doc.ServedAt); err != nil {
		return ErrAggregateRejected
	}
	items, err := indexAllowanceItems(doc)
	if err != nil {
		return err
	}
	statuses, err := indexAllowanceStatuses(doc)
	if err != nil {
		return err
	}
	contexts := map[string]providerallowance.TrustedContext{}
	for _, ctx := range s.Contexts {
		if ctx.Provider == "" {
			return ErrAggregateRejected
		}
		if _, dup := contexts[ctx.Provider]; dup {
			return ErrAggregateRejected
		}
		contexts[ctx.Provider] = ctx
	}
	for _, provider := range aggregateProviders {
		item, hasItem := items[provider]
		status := statuses[provider]
		if !hasItem {
			if !gapStatus(status) || status.Binding.State == providerallowance.BindingVerified {
				return ErrAggregateRejected
			}
			continue
		}
		if err := s.acceptItem(item, status, contexts[provider], contexts, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *AggregateAllowanceReportStore) acceptItem(item providerallowance.ReadItem, status providerallowance.SourceStatus, matched providerallowance.TrustedContext, contexts map[string]providerallowance.TrustedContext, now time.Time) error {
	_, hasContext := contexts[item.Observation.Provider]
	current := allowanceClaimsCurrent(item)
	if !hasContext || matched.Provider != item.Observation.Provider {
		if current || statusClaimsCurrent(status) {
			return ErrAggregateRejected
		}
		return nil
	}
	if current {
		if err := acceptCurrentItem(item, matched, s.Registry, now); err != nil {
			return err
		}
	}
	if statusClaimsCurrent(status) && !current {
		return ErrAggregateRejected
	}
	if freshRank(status.Freshness) > freshRank(item.Freshness) {
		return ErrAggregateRejected
	}
	if current && !lastGoodMatches(status.LastGood, item) {
		return ErrAggregateRejected
	}
	if err := rejectPayloadStatusBinding(status, matched, item); err != nil {
		return err
	}
	return nil
}

func rejectPayloadStatusBinding(status providerallowance.SourceStatus, trusted providerallowance.TrustedContext, item providerallowance.ReadItem) error {
	if status.Binding.State != providerallowance.BindingVerified {
		return nil
	}
	if status.ProducerRef != trusted.ProducerRef || item.Observation.ProducerRef != trusted.ProducerRef || item.Observation.ProducerIncarnation != trusted.ProducerIncarnation {
		return ErrAggregateRejected
	}
	if status.Binding.Generation != providerallowance.Count(trusted.BindingGeneration) {
		return ErrAggregateRejected
	}
	if !aggregateStringEqual(status.Binding.AccountRef, trusted.AccountRef) || !aggregateStringEqual(status.Binding.WorkspaceRef, trusted.WorkspaceRef) {
		return ErrAggregateRejected
	}
	return nil
}

func acceptCapture(item providerallowance.ReadItem, trusted providerallowance.TrustedContext, reg providerallowance.Registry, now time.Time) error {
	obs := item.Observation
	if trusted.BindingState != providerallowance.BindingVerified {
		return ErrAggregateRejected
	}
	if obs.ProducerRef != trusted.ProducerRef || obs.ProducerIncarnation != trusted.ProducerIncarnation || obs.Source.Kind != trusted.SourceKind || obs.Source.Version != trusted.SourceVersion {
		return ErrAggregateRejected
	}
	if obs.BindingGeneration != providerallowance.Count(trusted.BindingGeneration) || !aggregateStringEqual(obs.Scope.AccountRef, trusted.AccountRef) || !aggregateStringEqual(obs.Scope.WorkspaceRef, trusted.WorkspaceRef) {
		return ErrAggregateRejected
	}
	assessment := providerallowance.ValidateObservation(obs, trusted, reg)
	if len(assessment.Issues) != 0 || assessment.TrustedBinding != providerallowance.BindingVerified || assessment.ObservationBinding != providerallowance.BindingVerified {
		return ErrAggregateRejected
	}
	receipt, err := time.Parse(time.RFC3339Nano, item.ReceivedAt)
	if err != nil || receipt.After(now) {
		return ErrAggregateRejected
	}
	captured, err := time.Parse(time.RFC3339Nano, obs.CapturedAt)
	if err != nil || captured.After(receipt) {
		return ErrAggregateRejected
	}
	if obs.ProviderObservedAt != nil {
		observed, err := time.Parse(time.RFC3339Nano, *obs.ProviderObservedAt)
		if err != nil || observed.After(receipt) {
			return ErrAggregateRejected
		}
	}
	return nil
}

func acceptCurrentItem(item providerallowance.ReadItem, trusted providerallowance.TrustedContext, reg providerallowance.Registry, now time.Time) error {
	if err := acceptCapture(item, trusted, reg, now); err != nil {
		return err
	}
	obs := item.Observation
	if obs.ProviderObservedAt == nil || !providerallowance.CurrentMeasuresSupported(item) {
		return ErrAggregateRejected
	}
	assessment := providerallowance.ValidateObservation(obs, trusted, reg)
	for _, current := range item.CurrentMeasures {
		if current.State != providerallowance.CurrentKnown && current.State != providerallowance.CurrentUncapped {
			continue
		}
		for i, measure := range obs.Measures {
			if measure.ID == current.MeasureID && (i >= len(assessment.Measures) || !assessment.Measures[i].Actionable || assessment.Measures[i].HistoricalOnly) {
				return ErrAggregateRejected
			}
		}
	}
	receipt, _ := time.Parse(time.RFC3339Nano, item.ReceivedAt)
	for _, measure := range obs.Measures {
		if measure.Window.ResetPrecision != providerallowance.ResetInstant {
			continue
		}
		known := false
		for _, current := range item.CurrentMeasures {
			if current.MeasureID == measure.ID && (current.State == providerallowance.CurrentKnown || current.State == providerallowance.CurrentUncapped) {
				known = true
			}
		}
		if !known {
			continue
		}
		if measure.Window.ResetAt == nil {
			return ErrAggregateRejected
		}
		reset, resetErr := time.Parse(time.RFC3339Nano, *measure.Window.ResetAt)
		if resetErr != nil || !reset.After(receipt) || !reset.After(now) {
			return ErrAggregateRejected
		}
	}
	return nil
}

func preserveAllowance(prev, next providerallowance.ReadResponse) error {
	prevItems, err := indexAllowanceItems(prev)
	if err != nil {
		return err
	}
	nextItems, err := indexAllowanceItems(next)
	if err != nil {
		return err
	}
	prevStatus, err := indexAllowanceStatuses(prev)
	if err != nil {
		return err
	}
	nextStatus, err := indexAllowanceStatuses(next)
	if err != nil {
		return err
	}
	for _, provider := range aggregateProviders {
		before, hasBefore := prevItems[provider]
		after, hasAfter := nextItems[provider]
		if hasBefore && !hasAfter {
			return ErrAggregateRejected
		}
		if !hasBefore {
			continue
		}
		emptyCorrection := emptyClaudeLastGoodCorrection(before, after, prevStatus[provider], nextStatus[provider])
		if sameAllowanceCapture(before, after) {
			if before.ReceivedAt != after.ReceivedAt || !observationsEqual(before.Observation, after.Observation) {
				return ErrAggregateRejected
			}
			if !lastGoodEqual(prevStatus[provider].LastGood, nextStatus[provider].LastGood) && !emptyCorrection {
				return ErrAggregateRejected
			}
			if freshRank(after.Freshness) > freshRank(before.Freshness) || currentPromoted(before, after) {
				return ErrAggregateRejected
			}
			continue
		}
		if !allowanceClaimsCurrent(after) && !lastGoodEqual(prevStatus[provider].LastGood, nextStatus[provider].LastGood) && !lastGoodMatches(nextStatus[provider].LastGood, after) && !emptyCorrection {
			return ErrAggregateRejected
		}
		rebound := !aggregateStringEqual(before.Observation.Scope.AccountRef, after.Observation.Scope.AccountRef) ||
			!aggregateStringEqual(before.Observation.Scope.WorkspaceRef, after.Observation.Scope.WorkspaceRef)
		if after.Observation.BindingGeneration < before.Observation.BindingGeneration ||
			(rebound && after.Observation.BindingGeneration <= before.Observation.BindingGeneration) {
			return ErrAggregateRejected
		}
		sameProducer := before.Observation.ProducerRef == after.Observation.ProducerRef && before.Observation.ProducerIncarnation == after.Observation.ProducerIncarnation
		if sameProducer {
			if after.Observation.Sequence <= before.Observation.Sequence {
				return ErrAggregateRejected
			}
			if !rebound {
				continue
			}
		}
		// The new report has already passed the store's caller-owned trust.
		// A newly selected producer incarnation may restart its counter. An
		// explicit higher-generation scope rebind may replace the old account.
		// Neither can replay an older capture or refresh its original receipt.
		for _, stamps := range [][2]string{
			{before.Observation.CapturedAt, after.Observation.CapturedAt},
			{before.ReceivedAt, after.ReceivedAt},
		} {
			oldAt, oldErr := time.Parse(time.RFC3339Nano, stamps[0])
			newAt, newErr := time.Parse(time.RFC3339Nano, stamps[1])
			if oldErr != nil || newErr != nil || !newAt.After(oldAt) {
				return ErrAggregateRejected
			}
		}
	}
	return nil
}

func (s *AggregateAllowanceReportStore) confirm(ctx context.Context, raw, priorRV string) error {
	cm, err := s.live(ctx)
	if err != nil {
		return ErrAggregateUnavailable
	}
	if cm.ResourceVersion == "" || cm.ResourceVersion == priorRV || slotBytes(cm, s.DataKey) != raw {
		return ErrAggregateUnavailable
	}
	return nil
}

func (s *AggregateAllowanceReportStore) live(ctx context.Context) (*corev1.ConfigMap, error) {
	var cm corev1.ConfigMap
	if err := s.Live.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Name}, &cm); err != nil {
		return nil, err
	}
	return &cm, nil
}

func decodeCanonicalAllowance(raw string) (providerallowance.ReadResponse, error) {
	if !canonicalAllowance(raw) {
		if len(raw) == 0 || len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
			return providerallowance.ReadResponse{}, ErrAggregateRejected
		}
		if _, err := providerallowance.DecodeReadResponse([]byte(raw), providerallowance.FixtureParseLimits); err != nil {
			return providerallowance.ReadResponse{}, ErrAggregateRejected
		}
		return providerallowance.ReadResponse{}, ErrAggregateDigest
	}
	doc, err := providerallowance.DecodeReadResponse([]byte(raw), providerallowance.FixtureParseLimits)
	if err != nil {
		return providerallowance.ReadResponse{}, ErrAggregateRejected
	}
	return doc, nil
}

func canonicalAllowance(raw string) bool {
	if raw == "" || len(raw) > providerallowance.FixtureParseLimits.MaxBytes {
		return false
	}
	doc, err := providerallowance.DecodeReadResponse([]byte(raw), providerallowance.FixtureParseLimits)
	if err != nil {
		return false
	}
	canonical, err := json.Marshal(doc)
	return err == nil && bytes.Equal([]byte(raw), canonical)
}

var aggregateProviders = []string{"cursor", "anthropic", "openai"}

func indexAllowanceItems(doc providerallowance.ReadResponse) (map[string]providerallowance.ReadItem, error) {
	out := map[string]providerallowance.ReadItem{}
	if len(doc.Items) > 8 {
		return nil, ErrAggregateRejected
	}
	for _, item := range doc.Items {
		if !knownAggregateProvider(item.Observation.Provider) {
			return nil, ErrAggregateRejected
		}
		if _, dup := out[item.Observation.Provider]; dup {
			return nil, ErrAggregateRejected
		}
		out[item.Observation.Provider] = item
	}
	return out, nil
}

func indexAllowanceStatuses(doc providerallowance.ReadResponse) (map[string]providerallowance.SourceStatus, error) {
	out := map[string]providerallowance.SourceStatus{}
	for _, status := range doc.SourceStatuses {
		if !knownAggregateProvider(status.Provider) {
			return nil, ErrAggregateRejected
		}
		if _, dup := out[status.Provider]; dup {
			return nil, ErrAggregateRejected
		}
		out[status.Provider] = status
	}
	for _, provider := range aggregateProviders {
		if _, ok := out[provider]; !ok {
			return nil, ErrAggregateRejected
		}
	}
	if len(out) != len(aggregateProviders) {
		return nil, ErrAggregateRejected
	}
	return out, nil
}

func knownAggregateProvider(provider string) bool {
	for _, name := range aggregateProviders {
		if name == provider {
			return true
		}
	}
	return false
}

func gapStatus(status providerallowance.SourceStatus) bool {
	switch status.Availability {
	case providerallowance.AvailabilityUnconfigured, providerallowance.AvailabilityUnavailable, providerallowance.AvailabilityUnsupported, providerallowance.AvailabilityAuthNeeded, providerallowance.AvailabilityThrottled:
	default:
		return false
	}
	return status.LastGood == nil && freshRank(status.Freshness) == 0
}

func allowanceClaimsCurrent(item providerallowance.ReadItem) bool {
	if item.Freshness.State == providerallowance.FreshFresh || item.Freshness.State == providerallowance.FreshStale {
		return true
	}
	for _, measure := range item.CurrentMeasures {
		if measure.State == providerallowance.CurrentKnown || measure.State == providerallowance.CurrentUncapped {
			return true
		}
	}
	return false
}

func statusClaimsCurrent(status providerallowance.SourceStatus) bool {
	return status.Freshness.State == providerallowance.FreshFresh || status.Freshness.State == providerallowance.FreshStale
}

func lastGoodMatches(good *providerallowance.LastGood, item providerallowance.ReadItem) bool {
	if good == nil {
		return false
	}
	return good.ObservationID == item.Observation.ObservationID && good.CapturedAt == item.Observation.CapturedAt && good.ReceivedAt == item.ReceivedAt && aggregateStringEqual(good.ProviderObservedAt, item.Observation.ProviderObservedAt)
}

// Older SDK assembly incorrectly called an empty envelope last-good. Permit
// removing only that data-free reference under the same verified identity.
// No usable value, source selection, clock, raw observation or history is reset.
func emptyClaudeLastGoodCorrection(before, after providerallowance.ReadItem, prev, next providerallowance.SourceStatus) bool {
	if next.LastGood != nil || next.Availability != providerallowance.AvailabilityUnavailable || !lastGoodMatches(prev.LastGood, before) {
		return false
	}
	for _, item := range []providerallowance.ReadItem{before, after} {
		obs := item.Observation
		if obs.Provider != "anthropic" || obs.Source.Kind != providerallowance.SourceNativeRPC || obs.Source.Version != providerallowance.ClaudeSDKSourceVersion {
			return false
		}
		if obs.ProviderObservedAt != nil || len(obs.Measures) != 0 || len(obs.Derivations) != 0 || len(obs.DirectFields) != 0 || len(item.Validation.ValidationRefs) != 0 {
			return false
		}
		if item.Validation.State != providerallowance.ValidationSourceFields || item.Binding.State != providerallowance.BindingVerified || obs.Scope.BindingState != providerallowance.BindingVerified {
			return false
		}
		if obs.Availability != providerallowance.AvailabilityUnavailable && obs.Availability != providerallowance.AvailabilityPartial {
			return false
		}
	}
	return before.Observation.ProducerRef == after.Observation.ProducerRef &&
		before.Observation.ProducerIncarnation == after.Observation.ProducerIncarnation &&
		before.Observation.Source.CapabilityRef == after.Observation.Source.CapabilityRef &&
		before.Observation.BindingGeneration == after.Observation.BindingGeneration && before.Binding.Generation == after.Binding.Generation &&
		aggregateStringEqual(before.Observation.Scope.AccountRef, after.Observation.Scope.AccountRef) &&
		aggregateStringEqual(before.Observation.Scope.WorkspaceRef, after.Observation.Scope.WorkspaceRef)
}

func lastGoodEqual(a, b *providerallowance.LastGood) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.ObservationID == b.ObservationID && a.CapturedAt == b.CapturedAt && a.ReceivedAt == b.ReceivedAt && aggregateStringEqual(a.ProviderObservedAt, b.ProviderObservedAt)
}

func sameAllowanceCapture(a, b providerallowance.ReadItem) bool {
	if a.Observation.ObservationID != "" && a.Observation.ObservationID == b.Observation.ObservationID {
		return true
	}
	return a.Observation.Provider == b.Observation.Provider && a.Observation.ProducerRef == b.Observation.ProducerRef && a.Observation.ProducerIncarnation == b.Observation.ProducerIncarnation && a.Observation.Sequence == b.Observation.Sequence
}

func observationsEqual(a, b providerallowance.Observation) bool {
	left, errA := json.Marshal(a)
	right, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(left, right)
}

func currentPromoted(prev, next providerallowance.ReadItem) bool {
	before := map[string]int{}
	for _, measure := range prev.CurrentMeasures {
		before[measure.MeasureID] = currentRank(measure.State)
	}
	for _, measure := range next.CurrentMeasures {
		if currentRank(measure.State) > before[measure.MeasureID] {
			return true
		}
	}
	return false
}

func freshRank(fresh providerallowance.Freshness) int {
	switch fresh.State {
	case providerallowance.FreshFresh:
		return 3
	case providerallowance.FreshStale:
		return 2
	case providerallowance.FreshExpired:
		return 1
	default:
		return 0
	}
}

func currentRank(state providerallowance.CurrentState) int {
	switch state {
	case providerallowance.CurrentKnown, providerallowance.CurrentUncapped:
		return 3
	case providerallowance.CurrentExpired, providerallowance.CurrentConflict:
		return 1
	default:
		return 0
	}
}

func aggregateStringEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func cloneAggregateString(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

// UnconfiguredAggregateDocument is the empty slot. All three providers are
// unknown and unconfigured. It does not use a serving clock or a zero quota.
func UnconfiguredAggregateDocument() providerallowance.ReadResponse {
	at := "1970-01-01T00:00:00Z"
	code := "source_unconfigured"
	completed := at
	statuses := make([]providerallowance.SourceStatus, 0, len(aggregateProviders))
	for _, provider := range aggregateProviders {
		statuses = append(statuses, providerallowance.SourceStatus{
			Provider: provider, ProducerRef: "aggregate-unconfigured", Availability: providerallowance.AvailabilityUnconfigured,
			LastAttempt: providerallowance.LastAttempt{AttemptedAt: at, CompletedAt: &completed, Result: providerallowance.AttemptUnknown, Code: &code},
			Freshness:   providerallowance.Freshness{State: providerallowance.FreshUnknown, AsOf: at, Basis: providerallowance.BasisNone, Reason: "unconfigured"},
			Validation:  providerallowance.ValidationRecord{State: providerallowance.ValidationUnverified, ValidationRefs: []string{}},
			Binding:     providerallowance.BindingRecord{State: providerallowance.BindingUnknown},
		})
	}
	return providerallowance.ReadResponse{
		Schema: providerallowance.SchemaRead, SnapshotRevision: "aggregate-unconfigured", ServedAt: at,
		Items: []providerallowance.ReadItem{}, SourceStatuses: statuses,
	}
}
