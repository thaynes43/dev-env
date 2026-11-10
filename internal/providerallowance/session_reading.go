package providerallowance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"slices"
	"strconv"
	"time"
)

// Session readings (DESIGN-059): passive allowance readings that running
// sessions report on their heartbeats. The operator serves the newest per
// provider as one passive_event item of source version session-v1. They are
// about the one shared login only because every managed pod holds the
// keeper's projection of it; no account identifier is compared, so binding
// stays Unknown, and no value is ever current.
const (
	// SessionSourceVersion is the source version of a served session reading.
	SessionSourceVersion = "session-v1"
	// SessionProducerRef is the producer of every served session reading and
	// of its source status.
	SessionProducerRef = "dev-env-session-allowances"
)

// The operator's bounds on a session reading (internal/agentd/protocol), kept
// here so the rule refuses what the operator would never store: a window of
// at most 31 days, and a reset at most one window length (plus one hour for a
// reset rounded up to the hour) after the reading.
const (
	sessionMaxWindowMinutes = 31 * 24 * 60
	sessionResetSlack       = time.Hour
)

// sessionReason is the fixed freshness and current-measure reason.
const sessionReason = "session reading: account not verified; provider freshness unknown"

// sessionReadingProviders maps a contract provider to its session
// capability and product.
var sessionReadingProviders = map[string]struct{ capability, product string }{
	"anthropic": {"claude-statusline:session-reading", "claude"},
	"openai":    {"codex-rollout:session-reading", "codex"},
}

// SessionWindow is one window of a session reading.
type SessionWindow struct {
	Minutes     int64
	UsedPercent string
	ResetAt     time.Time
}

var errSessionReading = errors.New("invalid session reading")

// SessionReadingItem builds the served item and source status for one
// session reading of provider ("anthropic" or "openai"). readAt is when the
// session's CLI received the numbers; it becomes both the capture and the
// receipt time, and no provider observation time is claimed. Windows are
// ordered by length. The result passes the built-in rule ProjectCachedRead
// applies to session-v1 items.
func SessionReadingItem(provider string, readAt time.Time, windows []SessionWindow) (ReadItem, SourceStatus, error) {
	spec, ok := sessionReadingProviders[provider]
	if !ok || readAt.IsZero() || len(windows) == 0 || len(windows) > 2 {
		return ReadItem{}, SourceStatus{}, errSessionReading
	}
	at := readAt.UTC().Format(time.RFC3339Nano)
	sorted := append([]SessionWindow(nil), windows...)
	slices.SortFunc(sorted, func(a, b SessionWindow) int { return int(a.Minutes - b.Minutes) })
	obs := Observation{
		Schema: SchemaObservation, ProducerRef: SessionProducerRef, ProducerIncarnation: SessionSourceVersion, Sequence: 1,
		Provider: provider, Source: Source{Kind: SourcePassive, Version: SessionSourceVersion, CapabilityRef: spec.capability},
		Scope:      Scope{Products: []string{spec.product}, BindingState: BindingUnknown},
		CapturedAt: at, Availability: AvailabilityPartial, Completeness: CompletenessPartial,
		DirectFields: []DirectField{}, Measures: []Measure{}, Derivations: []Derivation{}, EvidenceRefs: []string{},
	}
	identity := provider + "|" + at
	for _, w := range sorted {
		if w.Minutes <= 0 || w.ResetAt.IsZero() || !sessionPercentFits(w.UsedPercent) {
			return ReadItem{}, SourceStatus{}, errSessionReading
		}
		minutes := Count(w.Minutes)
		reset := w.ResetAt.UTC().Format(time.RFC3339)
		used := w.UsedPercent
		id := sessionMeasureID(w.Minutes)
		obs.Measures = append(obs.Measures, Measure{ID: id, Category: CategoryIncluded,
			Window: Window{Ref: id, DurationMinutes: &minutes, ResetAt: &reset, ResetPrecision: ResetInstant},
			Unit:   UnitPercent, Direction: DirectionUsed, Value: MeasureValue{State: ValueKnown, Decimal: &used}, EvidenceFields: []string{}})
		identity += "|" + strconv.FormatInt(w.Minutes, 10) + "=" + used + "@" + reset
	}
	sum := sha256.Sum256([]byte(identity))
	obs.ObservationID = "session-" + spec.product + "-" + hex.EncodeToString(sum[:12])
	item := ReadItem{Observation: obs, ReceivedAt: at}
	if !projectSessionItem(&item, readAt) {
		return ReadItem{}, SourceStatus{}, errSessionReading
	}
	status := SourceStatus{Provider: provider, ProducerRef: SessionProducerRef, Availability: AvailabilityPartial,
		LastGood:    &LastGood{ObservationID: obs.ObservationID, CapturedAt: at, ReceivedAt: at},
		LastAttempt: LastAttempt{AttemptedAt: at, CompletedAt: &at, Result: AttemptSuccess},
	}
	projectSessionStatus(&status)
	return item, status, nil
}

// sessionMeasureID names a window by its length, as the statusline does.
func sessionMeasureID(minutes int64) string {
	switch minutes {
	case 300:
		return "five_hour"
	case 7 * 24 * 60:
		return "seven_day"
	default:
		return "window_" + strconv.FormatInt(minutes, 10) + "m"
	}
}

// sessionPercentFits is a plain decimal percent from 0 to 100.
func sessionPercentFits(text string) bool {
	if !claudeDecimal.MatchString(text) || len(text) > 16 {
		return false
	}
	n, err := parseDecimal(text)
	return err == nil && n.Sign() >= 0 && n.Cmp(big.NewRat(100, 1)) <= 0
}

// SessionReadingSource reports whether a source is a session reading's: such
// an item is judged by the built-in rule, never by a private trust context.
func SessionReadingSource(src Source) bool {
	return src.Kind == SourcePassive && src.Version == SessionSourceVersion
}

// RejectSessionReadings labels every session-v1 item in doc rejected. The
// operator calls it on each document it did not build (the stored slot, a
// Source) before it merges its own session readings: anyone who can write
// that document can also write a well-shaped session-v1 item, so such an
// item is shown rejected, as it was before session readings existed, and
// ProjectCachedRead then treats it like any untrusted item. The built-in rule
// never lifts a rejected label, so a reader that projects the served bytes
// again (the portal) keeps it rejected. The Items slice is copied before the
// first change; the caller's backing array is not written.
func RejectSessionReadings(doc *ReadResponse) {
	if doc == nil {
		return
	}
	copied := false
	for i := range doc.Items {
		if !SessionReadingSource(doc.Items[i].Observation.Source) {
			continue
		}
		if !copied {
			doc.Items, copied = append([]ReadItem(nil), doc.Items...), true
		}
		doc.Items[i].Validation.State = ValidationRejected
	}
}

// projectSessionItem is the built-in display rule for one session-v1 item.
// It checks the item's shape, sets its binding Unknown, never marks a value
// current, and marks a measure expired once its reset has passed at now. It
// reports false for an item it does not accept, which the caller rejects.
// An item that arrives labelled rejected is not accepted: a label never
// grants trust, but a rejection by the operator (RejectSessionReadings) or by
// an earlier projection is kept.
func projectSessionItem(item *ReadItem, now time.Time) bool {
	if item.Validation.State == ValidationRejected {
		return false
	}
	obs := &item.Observation
	spec, ok := sessionReadingProviders[obs.Provider]
	if !ok || !SessionReadingSource(obs.Source) || obs.Source.CapabilityRef != spec.capability || obs.ProducerRef != SessionProducerRef {
		return false
	}
	if obs.ProviderObservedAt != nil || obs.Scope.AccountRef != nil || obs.Scope.WorkspaceRef != nil || obs.Scope.EntitlementRef != nil || obs.BindingGeneration != 0 {
		return false
	}
	if len(obs.DirectFields) != 0 || len(obs.Derivations) != 0 || len(obs.Measures) == 0 || len(obs.Measures) > 2 {
		return false
	}
	if obs.Availability != AvailabilityPartial && obs.Availability != AvailabilityAvailable {
		return false
	}
	if !ValidateObservation(*obs, TrustedContext{}, nil).ShapeOK {
		return false
	}
	captured, ok := cachedTimeString(obs.CapturedAt)
	if !ok {
		return false
	}
	currents := make([]CurrentMeasure, 0, len(obs.Measures))
	for _, m := range obs.Measures {
		if m.Category != CategoryIncluded || m.Unit != UnitPercent || m.Direction != DirectionUsed || m.Currency != nil {
			return false
		}
		if m.Window.DurationMinutes == nil || *m.Window.DurationMinutes <= 0 || *m.Window.DurationMinutes > sessionMaxWindowMinutes || m.Window.ResetPrecision != ResetInstant || m.Value.State != ValueKnown || m.Value.Decimal == nil || !sessionPercentFits(*m.Value.Decimal) {
			return false
		}
		reset, ok := cachedTime(m.Window.ResetAt)
		if !ok || !reset.After(captured) || reset.After(captured.Add(time.Duration(*m.Window.DurationMinutes)*time.Minute+sessionResetSlack)) {
			return false
		}
		current := CurrentMeasure{MeasureID: m.ID, State: CurrentUnknown, Value: MeasureValue{State: ValueUnknown}, Reason: sessionReason}
		if !now.IsZero() && !now.Before(reset) {
			current.State, current.Reason = CurrentExpired, "reported reset passed; no reading since"
		}
		currents = append(currents, current)
	}
	obs.Scope.BindingState = BindingUnknown
	item.CollectionFreshness = nil
	item.Validation = ValidationRecord{State: ValidationDisplayOnly, ValidationRefs: []string{}}
	item.Binding = BindingRecord{State: BindingUnknown}
	item.Freshness = Freshness{State: FreshUnknown, AsOf: obs.CapturedAt, Basis: BasisCapture, Reason: sessionReason}
	item.CurrentMeasures = currents
	return true
}

// projectSessionStatus is the built-in rule for a session reading's source
// status: binding Unknown, display-only validation and no freshness claim.
// It reports false for a status it does not accept.
func projectSessionStatus(status *SourceStatus) bool {
	if status.ProducerRef != SessionProducerRef || (status.Availability != AvailabilityPartial && status.Availability != AvailabilityAvailable) {
		return false
	}
	asOf := status.LastAttempt.AttemptedAt
	if status.LastGood != nil {
		asOf = status.LastGood.CapturedAt
	}
	if _, ok := cachedTimeString(asOf); !ok {
		return false
	}
	status.Binding = BindingRecord{State: BindingUnknown}
	status.Validation = ValidationRecord{State: ValidationDisplayOnly, ValidationRefs: []string{}}
	status.Freshness = Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisCapture, Reason: sessionReason}
	return true
}
