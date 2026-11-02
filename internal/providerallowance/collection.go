package providerallowance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"
)

// Snapshot bytes are a providerallowance.read.v1 document. Callers can pass
// them to web/portal/session.NewCachedAllowanceReader or to
// `agent-run usage -o json --from-file`. This package does not register a
// route, start a schedule, or claim that recurring acquisition is running.
// internal/apiserver/usage.go still serves invocation usage only.
// `agent-run usage -o json` without `--from-file` remains the local
// unconfigured preflight. Production session assembly does not call
// SessionSource.SetAllowanceReader.

const (
	collectionCycleLimit    = 8
	collectionSourceLimit   = 8
	collectionByteLimit     = 64 << 10
	collectionTimeoutLimit  = 30 * time.Second
	collectionIntervalLimit = time.Hour
	collectionAgeLimit      = 24 * time.Hour
)

const (
	codeAcquisitionError        = "acquisition_error"
	codeAcquisitionTimeout      = "acquisition_timeout"
	codeAcquisitionCancelled    = "acquisition_cancelled"
	codeAcquisitionUnavailable  = "acquisition_unavailable"
	codeAcquisitionAuth         = "acquisition_auth_needed"
	codeAcquisitionThrottled    = "acquisition_throttled"
	codeAcquisitionUnconfigured = "acquisition_unconfigured"
	codeAcquisitionUnsupported  = "acquisition_unsupported"
	codeAcquisitionRejected     = "acquisition_rejected"
	codeAcquisitionConflict     = "acquisition_conflict"
	codeAcquisitionBounds       = "acquisition_bounds"
	codeSourceUnconfigured      = "source_unconfigured"
)

const (
	reasonBinding       = "binding not actionable"
	reasonTimeMissing   = "provider time missing"
	reasonTimeFuture    = "provider time future"
	reasonCaptureFuture = "capture time future"
	reasonReceiptFuture = "receipt time future"
	reasonTimeStale     = "provider time stale"
	reasonReset         = "reset passed"
	reasonHistorical    = "historical only"
	reasonAlias         = "shared pool alias"
	reasonProjection    = "projection alias"
	reasonConflict      = "conflict withheld"
	reasonUnconfigured  = "source not configured"
	reasonAttempt       = "acquisition failed"
)

var (
	// ErrCollectionBounds means a supplied finite bound is missing or too large.
	ErrCollectionBounds = errors.New("collection_bounds")
	// ErrCollectionSources means the caller-supplied source list is unusable.
	ErrCollectionSources = errors.New("collection_sources")
	// ErrCollectionRegistry means a configured source has no trusted registry.
	ErrCollectionRegistry = errors.New("collection_registry")
	// ErrCollectionClock means the injected clock is missing or returned the zero time.
	ErrCollectionClock = errors.New("collection_clock")
	// ErrCollectionWait means the injected wait is missing.
	ErrCollectionWait = errors.New("collection_wait")
	// ErrCollectionAcquire means an enabled collection has no callback.
	ErrCollectionAcquire = errors.New("collection_acquire")
	// ErrCollectionCancelled means the caller context ended. It is not a callback error.
	ErrCollectionCancelled = errors.New("collection_cancelled")
	// ErrCollectionTimeout means one injected call passed CallTimeout.
	// The context passed to Acquirer reports context.DeadlineExceeded.
	// It does not carry this sentinel. Advancing Clock does not cancel that context.
	// Run records acquisition_timeout and continues.
	ErrCollectionTimeout = errors.New("collection_timeout")
	// ErrCollectionTime means a serving time cannot be represented as a date-time.
	ErrCollectionTime = errors.New("collection_time")
	// ErrCollectionSnapshot means the cache could not emit a valid read document.
	ErrCollectionSnapshot = errors.New("collection_snapshot")
)

var collectionToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

// Clock is the injected time source. Collection does not call time.Now.
type Clock interface {
	Now() time.Time
}

// Waiter pauses between finite cycles. Tests supply a wait that does not sleep.
type Waiter interface {
	Wait(ctx context.Context, d time.Duration) error
}

// Bounds is the caller-supplied finite budget. The zero Enabled value acquires nothing.
type Bounds struct {
	Enabled     bool
	MaxCycles   int
	MaxSources  int
	MaxBytes    int
	CallTimeout time.Duration
	Cadence     time.Duration
	Backoff     time.Duration
	MaxAge      time.Duration
	Parse       ParseLimits
}

// SourceSpec selects one provider source and the trusted account generation.
// Those fields come from the caller. An observation payload cannot replace them.
type SourceSpec struct {
	Provider            string
	ProducerRef         string
	ProducerIncarnation string
	BindingGeneration   int64
	AccountRef          *string
	WorkspaceRef        *string
	BindingState        BindingState
	SourceKind          SourceKind
	SourceVersion       string
}

// Trusted returns the caller-supplied trust input for this source.
func (s SourceSpec) Trusted() TrustedContext {
	return TrustedContext{
		ProducerRef:         s.ProducerRef,
		ProducerIncarnation: s.ProducerIncarnation,
		BindingGeneration:   s.BindingGeneration,
		AccountRef:          cloneString(s.AccountRef),
		WorkspaceRef:        cloneString(s.WorkspaceRef),
		BindingState:        s.BindingState,
		Provider:            s.Provider,
		SourceKind:          s.SourceKind,
		SourceVersion:       s.SourceVersion,
	}
}

// Acquirer is the injected read-only callback. Collection calls it at most
// once per configured source per cycle. The context uses the standard
// cancellation errors: context.Canceled when the caller context ends, and
// context.DeadlineExceeded when CallTimeout elapses on the process clock.
// Cancellation is cooperative. Acquire must return after ctx.Done is closed.
// A callback that never observes the context runs to completion; Collection
// does not abandon it, interrupt it, or start another goroutine to do so.
// Clock is provider time. Moving it does not close Done.
// Collection does not run a provider, shell, credential file, or network exchange itself.
type Acquirer interface {
	Acquire(ctx context.Context, source SourceSpec) (Acquisition, error)
}

// Acquisition is either bounded observation bytes or a typed unavailable outcome.
// A callback error is discarded and is never copied into a status or a returned error.
type Acquisition struct {
	Observation []byte
	Unavailable *Unavailable
}

// Unavailable is a closed outcome. Availability is the only field, so a
// callback cannot attach a message, token, or cookie.
type Unavailable struct {
	Availability Availability
}

// Collection is an in-memory finite cache. Run performs the cycles it was
// given and returns. It does not start a goroutine, open a file, or register
// an endpoint. A disabled collection performs no acquisition and no wait.
//
// mu guards slots and nextEpoch only. bounds, reg, clock, wait, acquire, and
// baseline are fixed once NewCollection returns. Callbacks and waits run
// outside the lock. Snapshot copies slot headers under the lock and builds
// the document after releasing it. A hold or attempt is replaced, not mutated.
type Collection struct {
	mu        sync.Mutex
	bounds    Bounds
	reg       Registry
	clock     Clock
	wait      Waiter
	acquire   Acquirer
	baseline  string
	nextEpoch uint64
	slots     []collectionSlot
}

type collectionSlot struct {
	spec     SourceSpec
	hold     *collectionHold
	conflict bool
	attempt  *storedAttempt
	epoch    uint64
}

// collectionView is the slot header copy Snapshot builds from after releasing mu.
// The copied headers share holds that are no longer written.
type collectionView struct {
	bounds   Bounds
	reg      Registry
	baseline string
	slots    []collectionSlot
}

type collectionHold struct {
	obs        Observation
	receivedAt string
	accepted   bool
}

type storedAttempt struct {
	attemptedAt  string
	completedAt  string
	result       AttemptResult
	availability Availability
	code         string
}

// NewCollection copies bounds, sources, and trust inputs.
// Enabled false is valid and acquires nothing. Every bound is still checked.
func NewCollection(bounds Bounds, sources []SourceSpec, reg Registry, clock Clock, wait Waiter, acquire Acquirer) (*Collection, error) {
	if err := validateBounds(bounds); err != nil {
		return nil, err
	}
	if clock == nil {
		return nil, ErrCollectionClock
	}
	if wait == nil {
		return nil, ErrCollectionWait
	}
	if bounds.Enabled && acquire == nil {
		return nil, ErrCollectionAcquire
	}
	if len(sources) > 0 && reg == nil {
		return nil, ErrCollectionRegistry
	}
	baseline, ok := collectionTime(clock.Now())
	if !ok {
		return nil, ErrCollectionTime
	}
	specs, err := validateSourceSpecs(sources, bounds.MaxSources)
	if err != nil {
		return nil, err
	}
	c := &Collection{
		bounds:   bounds,
		reg:      reg,
		clock:    clock,
		wait:     wait,
		acquire:  acquire,
		baseline: baseline,
		slots:    make([]collectionSlot, len(specs)),
	}
	for i, spec := range specs {
		c.nextEpoch++
		c.slots[i].spec = spec
		c.slots[i].epoch = c.nextEpoch
	}
	return c, nil
}

// SetSources replaces caller trust. An account, workspace, generation,
// incarnation, or binding change drops that source's previous quota before
// the next acquisition. A failed later call cannot restore it.
func (c *Collection) SetSources(sources []SourceSpec) error {
	if c == nil {
		return ErrCollectionBounds
	}
	specs, err := validateSourceSpecs(sources, c.bounds.MaxSources)
	if err != nil {
		return err
	}
	if len(specs) > 0 && c.reg == nil {
		return ErrCollectionRegistry
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	next := make([]collectionSlot, len(specs))
	for i, spec := range specs {
		next[i].spec = spec
		prev, ok := findSlot(c.slots, spec.Provider, spec.ProducerRef)
		if ok && trustSame(prev.spec, spec) {
			next[i].hold = prev.hold
			next[i].conflict = prev.conflict
			next[i].attempt = prev.attempt
			next[i].epoch = prev.epoch
			continue
		}
		c.nextEpoch++
		next[i].epoch = c.nextEpoch
	}
	c.slots = next
	return nil
}

// Run executes at most MaxCycles cycles and then returns.
// A disabled collection returns before any callback, wait, or clock read.
func (c *Collection) Run(ctx context.Context) error {
	if c == nil || !c.bounds.Enabled {
		return nil
	}
	if ctx == nil || ctx.Err() != nil {
		return ErrCollectionCancelled
	}
	for cycle := 0; cycle < c.bounds.MaxCycles; cycle++ {
		failed := false
		for _, target := range c.cycleTargets() {
			if ctx.Err() != nil {
				return ErrCollectionCancelled
			}
			if c.acquireTarget(ctx, target) {
				failed = true
			}
			if ctx.Err() != nil {
				return ErrCollectionCancelled
			}
		}
		if cycle+1 == c.bounds.MaxCycles {
			break
		}
		delay := c.bounds.Cadence
		if failed {
			delay = c.bounds.Backoff
		}
		if waitErr := c.wait.Wait(ctx, delay); waitErr != nil || ctx.Err() != nil {
			return ErrCollectionCancelled
		}
	}
	return nil
}

// Snapshot builds a deep copy of the cache at the caller-supplied serving time.
// Serving time is not an acquisition time and not a provider observation time.
// The bytes are a providerallowance.read.v1 document.
func (c *Collection) Snapshot(servedAt time.Time) (ReadResponse, []byte, error) {
	if c == nil || validateBounds(c.bounds) != nil {
		return ReadResponse{}, nil, ErrCollectionSnapshot
	}
	served, ok := collectionTime(servedAt)
	if !ok {
		return ReadResponse{}, nil, ErrCollectionTime
	}
	doc := c.view().build(served, servedAt.UTC())
	raw, err := json.Marshal(doc)
	if err != nil {
		return ReadResponse{}, nil, ErrCollectionSnapshot
	}
	decoded, err := DecodeReadResponse(append([]byte(nil), raw...), c.bounds.Parse)
	if err != nil {
		return ReadResponse{}, nil, ErrCollectionSnapshot
	}
	if _, err = DecodeReadResponse(raw, FixtureParseLimits); err != nil {
		return ReadResponse{}, nil, ErrCollectionSnapshot
	}
	return decoded, append([]byte(nil), raw...), nil
}

type cycleTarget struct {
	spec  SourceSpec
	epoch uint64
}

func (c *Collection) cycleTargets() []cycleTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	targets := make([]cycleTarget, len(c.slots))
	for i := range c.slots {
		targets[i] = cycleTarget{spec: cloneSpec(c.slots[i].spec), epoch: c.slots[i].epoch}
	}
	return targets
}

func (c *Collection) view() collectionView {
	c.mu.Lock()
	defer c.mu.Unlock()
	slots := make([]collectionSlot, len(c.slots))
	copy(slots, c.slots)
	return collectionView{bounds: c.bounds, reg: c.reg, baseline: c.baseline, slots: slots}
}

func (c *Collection) acquireTarget(ctx context.Context, target cycleTarget) bool {
	started := c.clock.Now()
	callCtx, cancel := context.WithTimeout(ctx, c.bounds.CallTimeout)
	defer cancel()
	result, callErr := c.acquire.Acquire(callCtx, cloneSpec(target.spec))
	callState := callCtx.Err()
	finished := c.clock.Now()
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	slot := slotByIdentity(c.slots, target.spec.Provider, target.spec.ProducerRef)
	if slot == nil || slot.epoch != target.epoch || !trustSame(slot.spec, target.spec) {
		return true
	}
	if ctx.Err() != nil {
		c.record(slot, started, finished, AttemptUnavailable, AvailabilityUnavailable, codeAcquisitionCancelled)
		return true
	}
	if errors.Is(callState, context.DeadlineExceeded) || !finished.Before(started.Add(c.bounds.CallTimeout)) {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionTimeout)
		return true
	}
	if callErr != nil {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionError)
		return true
	}
	if result.Unavailable != nil && len(result.Observation) > 0 {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	if result.Unavailable != nil {
		availability, code := mapUnavailable(result.Unavailable.Availability)
		c.record(slot, started, finished, AttemptUnavailable, availability, code)
		return true
	}
	raw := append([]byte(nil), result.Observation...)
	if len(raw) == 0 || len(raw) > c.bounds.MaxBytes {
		code := codeAcquisitionRejected
		if len(raw) > c.bounds.MaxBytes {
			code = codeAcquisitionBounds
		}
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, code)
		return true
	}
	obs, err := DecodeObservation(raw, c.bounds.Parse)
	if err != nil {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	return c.accept(slot, obs, started, finished)
}

func (c *Collection) accept(slot *collectionSlot, obs Observation, started, finished time.Time) bool {
	if !sourceSelected(obs, slot.spec) {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	assessment := ValidateObservation(obs, slot.spec.Trusted(), c.reg)
	if !assessment.ShapeOK {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	canon, ok := canonicalObservation(obs)
	if !ok {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	if slot.hold != nil {
		prev, prevOK := canonicalObservation(slot.hold.obs)
		if prevOK && prev == canon {
			if slot.conflict {
				c.record(slot, started, finished, AttemptError, AvailabilityConflict, codeAcquisitionConflict)
				return true
			}
			// Canonical equality does not accept a capture the trusted caller
			// binding already rejected. The original receipt stays in place,
			// and the payload availability is not copied onto the attempt.
			if !storedCaptureAccepted(slot.hold.accepted, ValidateObservation(obs, slot.spec.Trusted(), c.reg)) {
				c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
				return true
			}
			c.record(slot, started, finished, AttemptSuccess, slot.hold.obs.Availability, "")
			return false
		}
		if prevOK && sameCaptureIdentity(slot.hold.obs, obs) {
			slot.conflict = true
			c.record(slot, started, finished, AttemptError, AvailabilityConflict, codeAcquisitionConflict)
			return true
		}
	}
	blocked := hasIdentityBlock(assessment.Issues) || hasPolicyBlock(assessment.Issues)
	if blocked && slot.hold != nil && slot.hold.accepted {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	cloned, ok := cloneObservationValue(obs, c.bounds.Parse)
	if !ok {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	received, ok := collectionTime(finished)
	if !ok {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	slot.hold = &collectionHold{obs: cloned, receivedAt: received, accepted: !blocked}
	slot.conflict = false
	if blocked {
		c.record(slot, started, finished, AttemptError, AvailabilityUnavailable, codeAcquisitionRejected)
		return true
	}
	c.record(slot, started, finished, AttemptSuccess, cloned.Availability, "")
	return false
}

func (c *Collection) record(slot *collectionSlot, started, finished time.Time, result AttemptResult, availability Availability, code string) {
	attempted, ok := collectionTime(started)
	completed, ok2 := collectionTime(finished)
	if !ok || !ok2 {
		attempted = c.baseline
		completed = c.baseline
		result = AttemptError
		availability = AvailabilityUnavailable
		code = codeAcquisitionRejected
	}
	slot.attempt = &storedAttempt{
		attemptedAt:  attempted,
		completedAt:  completed,
		result:       result,
		availability: availability,
		code:         fixedCode(code),
	}
}

func (c collectionView) build(served string, servedAt time.Time) ReadResponse {
	rows := make([]sharedInput, 0, len(c.slots))
	for i := range c.slots {
		slot := &c.slots[i]
		if slot.hold == nil {
			continue
		}
		item := c.itemFor(slot, servedAt)
		rows = append(rows, sharedInput{slot: slot, item: item, trust: slot.spec.Trusted()})
	}
	shared := cursorGrokShared(rows, c.reg)
	items := make([]ReadItem, 0, len(rows))
	for _, row := range rows {
		item := row.item
		if shared && item.Observation.Provider == "grok" {
			demoteAlias(&item)
		}
		if row.slot.conflict {
			markConflict(&item)
		}
		items = append(items, item)
	}
	statuses := make([]SourceStatus, 0, len(c.slots)+3)
	seen := map[string]struct{}{}
	for i := range c.slots {
		statuses = append(statuses, c.statusFor(&c.slots[i], served, servedAt))
		seen[c.slots[i].spec.Provider] = struct{}{}
	}
	for _, provider := range []string{"cursor", "anthropic", "openai"} {
		if _, ok := seen[provider]; ok {
			continue
		}
		statuses = append(statuses, unconfiguredStatus(provider, c.baseline, served))
	}
	doc := ReadResponse{
		Schema:           SchemaRead,
		SnapshotRevision: collectionRevision(items, statuses),
		ServedAt:         served,
		Items:            items,
		SourceStatuses:   statuses,
	}
	return doc
}

func (c collectionView) itemFor(slot *collectionSlot, servedAt time.Time) ReadItem {
	obs := slot.hold.obs
	trust := slot.spec.Trusted()
	assessment := ValidateObservation(obs, trust, c.reg)
	derived := Derive(obs, trust, c.reg)
	// Confirmed sums and complements are not appended as extra current meters.
	formulaIDs := map[string]struct{}{}
	if derived.Confirmed {
		for _, value := range derived.Values {
			if value.Formula == FormulaSum || value.Formula == FormulaComplement {
				formulaIDs[value.ID] = struct{}{}
			}
		}
	}
	fresh := viewFreshness(obs, slot.hold.receivedAt, servedAt, c.bounds.MaxAge, slot.hold.accepted, assessment)
	drops := projectionDrops(obs, c.reg)
	currents := make([]CurrentMeasure, 0, len(obs.Measures))
	for i, measure := range obs.Measures {
		var judged MeasureAssessment
		if i < len(assessment.Measures) {
			judged = assessment.Measures[i]
		}
		current := currentFor(measure, judged, fresh, servedAt)
		if drops[measure.ID] {
			current = CurrentMeasure{MeasureID: measure.ID, State: CurrentNotApplicable, Value: MeasureValue{State: ValueUnknown}, Reason: reasonProjection}
		}
		currents = append(currents, current)
	}
	if len(formulaIDs) > len(obs.Measures)+len(obs.Derivations) {
		currents = nil
	}
	validation := ValidationRecord{State: validationFor(assessment, slot.hold.accepted), ValidationRefs: validationRefs(obs)}
	return ReadItem{
		Observation:     obs,
		ReceivedAt:      slot.hold.receivedAt,
		Validation:      validation,
		Binding:         servedBinding(slot.spec, obs, slot.hold.accepted, assessment),
		Freshness:       fresh,
		CurrentMeasures: currents,
	}
}

func (c collectionView) statusFor(slot *collectionSlot, served string, servedAt time.Time) SourceStatus {
	status := SourceStatus{
		Provider:    slot.spec.Provider,
		ProducerRef: slot.spec.ProducerRef,
		Validation:  ValidationRecord{State: ValidationUnverified, ValidationRefs: []string{}},
		Binding:     BindingRecord{State: BindingUnknown},
		Freshness:   Freshness{State: FreshUnknown, AsOf: c.baseline, Basis: BasisNone, Reason: reasonUnconfigured},
		LastAttempt: LastAttempt{AttemptedAt: c.baseline, CompletedAt: cloneString(&c.baseline), Result: AttemptUnknown, Code: codePtr(codeSourceUnconfigured)},
	}
	if slot.attempt != nil {
		completed := slot.attempt.completedAt
		status.Availability = slot.attempt.availability
		status.LastAttempt = LastAttempt{
			AttemptedAt: slot.attempt.attemptedAt,
			CompletedAt: cloneString(&completed),
			Result:      slot.attempt.result,
			Code:        codePtr(slot.attempt.code),
		}
	} else {
		status.Availability = AvailabilityUnconfigured
	}
	if slot.hold == nil {
		if slot.attempt != nil {
			status.Freshness = Freshness{State: FreshUnknown, AsOf: slot.attempt.completedAt, Basis: BasisNone, Reason: reasonAttempt}
			status.Binding = BindingRecord{State: BindingUnknown}
		}
		return status
	}
	item := c.itemFor(slot, servedAt)
	if slot.conflict {
		markConflict(&item)
	}
	status.Freshness = item.Freshness
	status.Validation = item.Validation
	status.Binding = item.Binding
	if slot.conflict {
		status.Availability = AvailabilityConflict
		status.Validation.State = ValidationConflict
	}
	if slot.hold.accepted {
		status.LastGood = &LastGood{
			ObservationID:      slot.hold.obs.ObservationID,
			CapturedAt:         slot.hold.obs.CapturedAt,
			ProviderObservedAt: cloneString(slot.hold.obs.ProviderObservedAt),
			ReceivedAt:         slot.hold.receivedAt,
		}
	}
	if served == "" {
		status.Freshness.AsOf = c.baseline
	}
	return status
}

func unconfiguredStatus(provider, baseline, served string) SourceStatus {
	asOf := baseline
	if asOf == "" {
		asOf = served
	}
	return SourceStatus{
		Provider:     provider,
		ProducerRef:  "collection-unconfigured",
		Availability: AvailabilityUnconfigured,
		LastAttempt: LastAttempt{
			AttemptedAt: asOf,
			CompletedAt: cloneString(&asOf),
			Result:      AttemptUnknown,
			Code:        codePtr(codeSourceUnconfigured),
		},
		Freshness:  Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisNone, Reason: reasonUnconfigured},
		Validation: ValidationRecord{State: ValidationUnverified, ValidationRefs: []string{}},
		Binding:    BindingRecord{State: BindingUnknown},
	}
}

func currentFor(measure Measure, judged MeasureAssessment, fresh Freshness, servedAt time.Time) CurrentMeasure {
	unknown := CurrentMeasure{MeasureID: measure.ID, State: CurrentUnknown, Value: MeasureValue{State: ValueUnknown}, Reason: reasonBinding}
	if fresh.State != FreshFresh && fresh.State != FreshStale {
		unknown.Reason = fresh.Reason
		if unknown.Reason == "" {
			unknown.Reason = reasonTimeMissing
		}
		return unknown
	}
	if !judged.Actionable || judged.HistoricalOnly {
		unknown.Reason = reasonHistorical
		return unknown
	}
	if measure.Window.ResetPrecision == ResetInstant && resetPassed(measure.Window.ResetAt, servedAt) {
		return CurrentMeasure{MeasureID: measure.ID, State: CurrentExpired, Value: MeasureValue{State: ValueUnknown}, Reason: reasonReset}
	}
	if measure.Value.State == ValueKnown && measure.Value.Decimal != nil {
		return CurrentMeasure{
			MeasureID: measure.ID,
			State:     CurrentKnown,
			Value:     MeasureValue{State: ValueKnown, Decimal: cloneString(measure.Value.Decimal)},
		}
	}
	if measure.Value.State == ValueUncapped {
		return CurrentMeasure{MeasureID: measure.ID, State: CurrentUncapped, Value: MeasureValue{State: ValueUncapped}}
	}
	return unknown
}

func viewFreshness(obs Observation, received string, servedAt time.Time, maxAge time.Duration, accepted bool, assessment Assessment) Freshness {
	asOf := obs.CapturedAt
	if received != "" {
		asOf = received
	}
	if !servingChronologyAllowed(accepted, assessment, obs) {
		return Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisCapture, Reason: reasonBinding}
	}
	if obs.ProviderObservedAt == nil {
		return Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisCapture, Reason: reasonTimeMissing}
	}
	observed, err := time.Parse(time.RFC3339Nano, *obs.ProviderObservedAt)
	if err != nil {
		return Freshness{State: FreshUnknown, AsOf: asOf, Basis: BasisCapture, Reason: reasonTimeMissing}
	}
	// received is the original receipt. A provider or capture timestamp that
	// was already after that receipt stays non-current when servedAt advances.
	// A later acquisition stores its own receipt and can become current.
	// A receipt still ahead of servedAt withholds the current value even when
	// the provider and capture timestamps are not after servedAt.
	if future, ok := receiptFuture(obs, received, observed); ok {
		return future
	}
	if ahead, ok := receiptAheadOfServing(received, servedAt); ok {
		return ahead
	}
	if observed.After(servedAt) {
		return Freshness{State: FreshExpired, AsOf: *obs.ProviderObservedAt, Basis: BasisProvider, Reason: reasonTimeFuture}
	}
	state := FreshFresh
	reason := ""
	if servedAt.Sub(observed) > maxAge {
		state = FreshStale
		reason = reasonTimeStale
	}
	return Freshness{State: state, AsOf: *obs.ProviderObservedAt, Basis: BasisProvider, Reason: reason}
}

func receiptAheadOfServing(received string, servedAt time.Time) (Freshness, bool) {
	receipt, err := time.Parse(time.RFC3339Nano, received)
	if err != nil || !receipt.After(servedAt) {
		return Freshness{}, false
	}
	return Freshness{State: FreshExpired, AsOf: received, Basis: BasisCapture, Reason: reasonReceiptFuture}, true
}

func receiptFuture(obs Observation, received string, observed time.Time) (Freshness, bool) {
	receipt, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return Freshness{}, false
	}
	if obs.ProviderObservedAt != nil && observed.After(receipt) {
		return Freshness{State: FreshExpired, AsOf: *obs.ProviderObservedAt, Basis: BasisProvider, Reason: reasonTimeFuture}, true
	}
	captured, err := time.Parse(time.RFC3339Nano, obs.CapturedAt)
	if err == nil && captured.After(receipt) {
		return Freshness{State: FreshExpired, AsOf: obs.CapturedAt, Basis: BasisCapture, Reason: reasonCaptureFuture}, true
	}
	return Freshness{}, false
}

func validationFor(assessment Assessment, accepted bool) ValidationState {
	if !accepted || !assessment.ShapeOK || hasIdentityBlock(assessment.Issues) || hasPolicyBlock(assessment.Issues) {
		return ValidationRejected
	}
	if assessment.TrustedBinding != BindingVerified || !assessment.ProviderTimeKnown {
		return ValidationUnverified
	}
	if len(assessment.Measures) == 0 && len(assessment.Issues) == 0 {
		return ValidationDisplayOnly
	}
	actionable := 0
	for _, measure := range assessment.Measures {
		if measure.Actionable {
			actionable++
		}
	}
	if len(assessment.Issues) == 0 && len(assessment.Measures) > 0 && actionable == len(assessment.Measures) {
		return ValidationSourceFields
	}
	if actionable > 0 {
		return ValidationPartial
	}
	return ValidationUnverified
}

func validationRefs(obs Observation) []string {
	refs := make([]string, 0, len(obs.Measures))
	seen := map[string]struct{}{}
	for _, measure := range obs.Measures {
		if measure.ValidationRef == nil || *measure.ValidationRef == "" {
			continue
		}
		if _, ok := seen[*measure.ValidationRef]; ok {
			continue
		}
		seen[*measure.ValidationRef] = struct{}{}
		refs = append(refs, *measure.ValidationRef)
		if len(refs) == 8 {
			break
		}
	}
	return refs
}

func demoteAlias(item *ReadItem) {
	for i := range item.CurrentMeasures {
		measureID := item.CurrentMeasures[i].MeasureID
		for _, measure := range item.Observation.Measures {
			if measure.ID == measureID && measure.Category == CategoryIncluded && measure.Unit == UnitPercent {
				item.CurrentMeasures[i] = CurrentMeasure{
					MeasureID: measureID,
					State:     CurrentNotApplicable,
					Value:     MeasureValue{State: ValueUnknown},
					Reason:    reasonAlias,
				}
			}
		}
	}
}

func markConflict(item *ReadItem) {
	item.Validation.State = ValidationConflict
	for i := range item.CurrentMeasures {
		item.CurrentMeasures[i] = CurrentMeasure{
			MeasureID: item.CurrentMeasures[i].MeasureID,
			State:     CurrentConflict,
			Value:     MeasureValue{State: ValueUnknown},
			Reason:    reasonConflict,
		}
	}
}

type sharedInput struct {
	slot  *collectionSlot
	item  ReadItem
	trust TrustedContext
}

func cursorGrokShared(items []sharedInput, reg Registry) bool {
	obs := make([]Observation, 0, 2)
	trusts := make([]TrustedContext, 0, 2)
	for _, item := range items {
		provider := item.item.Observation.Provider
		if provider != "cursor" && provider != "grok" {
			continue
		}
		if item.slot.conflict || item.slot.hold == nil || !item.slot.hold.accepted {
			continue
		}
		obs = append(obs, item.item.Observation)
		trusts = append(trusts, item.trust)
	}
	if len(obs) < 2 || reg == nil {
		return false
	}
	pools := SharedPools(obs, trusts, reg)
	return pools.Resolved && pools.Count == 1 && pools.EntitlementRef != "" && len(pools.Issues) == 0
}

func projectionDrops(obs Observation, reg Registry) map[string]bool {
	drops := map[string]bool{}
	if reg == nil {
		return drops
	}
	parts := snapshotRegistry(reg)
	if len(projectionAmbiguities(parts.Projections)) > 0 {
		return drops
	}
	for _, rule := range parts.Projections {
		if rule.Provider != obs.Provider || rule.SourceVersion != obs.Source.Version {
			continue
		}
		present := presentMeasureIDs(obs, rule.MeasureIDs)
		if len(present) < 2 {
			continue
		}
		for _, id := range present[1:] {
			drops[id] = true
		}
	}
	return drops
}

func mapUnavailable(availability Availability) (Availability, string) {
	switch availability {
	case AvailabilityUnavailable:
		return AvailabilityUnavailable, codeAcquisitionUnavailable
	case AvailabilityAuthNeeded:
		return AvailabilityAuthNeeded, codeAcquisitionAuth
	case AvailabilityThrottled:
		return AvailabilityThrottled, codeAcquisitionThrottled
	case AvailabilityUnconfigured:
		return AvailabilityUnconfigured, codeAcquisitionUnconfigured
	case AvailabilityUnsupported:
		return AvailabilityUnsupported, codeAcquisitionUnsupported
	default:
		return AvailabilityUnavailable, codeAcquisitionUnavailable
	}
}

func fixedCode(code string) string {
	switch code {
	case codeAcquisitionError, codeAcquisitionTimeout, codeAcquisitionCancelled, codeAcquisitionUnavailable,
		codeAcquisitionAuth, codeAcquisitionThrottled, codeAcquisitionUnconfigured, codeAcquisitionUnsupported,
		codeAcquisitionRejected, codeAcquisitionConflict, codeAcquisitionBounds, codeSourceUnconfigured, "":
		return code
	default:
		return codeAcquisitionError
	}
}

func bindingFrom(spec SourceSpec) BindingRecord {
	return BindingRecord{
		State:        spec.BindingState,
		Generation:   Count(spec.BindingGeneration),
		AccountRef:   cloneString(spec.AccountRef),
		WorkspaceRef: cloneString(spec.WorkspaceRef),
	}
}

func storedCaptureAccepted(accepted bool, assessment Assessment) bool {
	return accepted && assessment.ShapeOK && !hasIdentityBlock(assessment.Issues) && !hasPolicyBlock(assessment.Issues)
}

func servingChronologyAllowed(accepted bool, assessment Assessment, obs Observation) bool {
	if !storedCaptureAccepted(accepted, assessment) {
		return false
	}
	return assessment.TrustedBinding == BindingVerified && obs.Scope.BindingState == BindingVerified
}

func servedBinding(spec SourceSpec, obs Observation, accepted bool, assessment Assessment) BindingRecord {
	if storedCaptureAccepted(accepted, assessment) {
		return bindingFrom(spec)
	}
	if hasIdentityBlock(assessment.Issues) && (spec.BindingState == BindingVerified || !trustedRefsMatch(spec, obs)) {
		return BindingRecord{State: BindingMismatch, Generation: Count(spec.BindingGeneration)}
	}
	return bindingFrom(spec)
}

func trustedRefsMatch(spec SourceSpec, obs Observation) bool {
	return stringPtrEqual(spec.AccountRef, obs.Scope.AccountRef) && stringPtrEqual(spec.WorkspaceRef, obs.Scope.WorkspaceRef)
}

func sourceSelected(obs Observation, spec SourceSpec) bool {
	return obs.Provider == spec.Provider &&
		obs.ProducerRef == spec.ProducerRef &&
		obs.ProducerIncarnation == spec.ProducerIncarnation &&
		obs.Source.Kind == spec.SourceKind &&
		obs.Source.Version == spec.SourceVersion
}

func sameCaptureIdentity(a, b Observation) bool {
	if a.ObservationID != "" && a.ObservationID == b.ObservationID {
		return true
	}
	return a.Provider == b.Provider && a.ProducerRef == b.ProducerRef && a.ProducerIncarnation == b.ProducerIncarnation && a.Sequence == b.Sequence
}

func trustSame(a, b SourceSpec) bool {
	return a.ProducerIncarnation == b.ProducerIncarnation &&
		a.BindingGeneration == b.BindingGeneration &&
		a.BindingState == b.BindingState &&
		a.SourceKind == b.SourceKind &&
		a.SourceVersion == b.SourceVersion &&
		stringPtrEqual(a.AccountRef, b.AccountRef) &&
		stringPtrEqual(a.WorkspaceRef, b.WorkspaceRef)
}

func slotByIdentity(slots []collectionSlot, provider, producer string) *collectionSlot {
	for i := range slots {
		if slots[i].spec.Provider == provider && slots[i].spec.ProducerRef == producer {
			return &slots[i]
		}
	}
	return nil
}

func findSlot(slots []collectionSlot, provider, producer string) (collectionSlot, bool) {
	for _, slot := range slots {
		if slot.spec.Provider == provider && slot.spec.ProducerRef == producer {
			return slot, true
		}
	}
	return collectionSlot{}, false
}

func resetPassed(resetAt *string, servedAt time.Time) bool {
	if resetAt == nil {
		return false
	}
	reset, err := time.Parse(time.RFC3339Nano, *resetAt)
	if err != nil {
		return false
	}
	return !reset.After(servedAt)
}

func collectionTime(t time.Time) (string, bool) {
	if t.IsZero() {
		return "", false
	}
	formatted := t.UTC().Format(time.RFC3339Nano)
	if validateDateTime(formatted) != nil {
		return "", false
	}
	return formatted, true
}

func collectionRevision(items []ReadItem, statuses []SourceStatus) string {
	sum := sha256.New()
	for _, item := range items {
		_, _ = sum.Write([]byte(item.Observation.Provider))
		_, _ = sum.Write([]byte{0})
		_, _ = sum.Write([]byte(item.Observation.ObservationID))
		_, _ = sum.Write([]byte{0})
		_, _ = sum.Write([]byte(item.Observation.CapturedAt))
		_, _ = sum.Write([]byte{0})
	}
	for _, status := range statuses {
		_, _ = sum.Write([]byte(status.Provider))
		_, _ = sum.Write([]byte{0})
		_, _ = sum.Write([]byte(status.Availability))
		_, _ = sum.Write([]byte{0})
		if status.LastAttempt.Code != nil {
			_, _ = sum.Write([]byte(*status.LastAttempt.Code))
		}
		_, _ = sum.Write([]byte{0})
	}
	return "collection-" + hex.EncodeToString(sum.Sum(nil)[:8])
}

func canonicalObservation(obs Observation) (string, bool) {
	raw, err := json.Marshal(obs)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func cloneObservationValue(obs Observation, limits ParseLimits) (Observation, bool) {
	raw, err := json.Marshal(obs)
	if err != nil {
		return Observation{}, false
	}
	cloned, err := DecodeObservation(raw, limits)
	if err != nil {
		return Observation{}, false
	}
	return cloned, true
}

func cloneSpec(in SourceSpec) SourceSpec {
	out := in
	out.AccountRef = cloneString(in.AccountRef)
	out.WorkspaceRef = cloneString(in.WorkspaceRef)
	return out
}

func codePtr(code string) *string {
	if code == "" {
		return nil
	}
	value := code
	return &value
}

func validateBounds(bounds Bounds) error {
	if bounds.MaxCycles < 1 || bounds.MaxCycles > collectionCycleLimit {
		return ErrCollectionBounds
	}
	if bounds.MaxSources < 1 || bounds.MaxSources > collectionSourceLimit {
		return ErrCollectionBounds
	}
	if bounds.MaxBytes < 1 || bounds.MaxBytes > collectionByteLimit {
		return ErrCollectionBounds
	}
	if bounds.CallTimeout < time.Millisecond || bounds.CallTimeout > collectionTimeoutLimit {
		return ErrCollectionBounds
	}
	if bounds.Cadence < time.Millisecond || bounds.Cadence > collectionIntervalLimit {
		return ErrCollectionBounds
	}
	if bounds.Backoff < time.Millisecond || bounds.Backoff > collectionIntervalLimit {
		return ErrCollectionBounds
	}
	if bounds.MaxAge < time.Millisecond || bounds.MaxAge > collectionAgeLimit {
		return ErrCollectionBounds
	}
	if bounds.Parse.valid() != nil || bounds.MaxBytes > bounds.Parse.MaxBytes || !parseWithinFixture(bounds.Parse) {
		return ErrCollectionBounds
	}
	return nil
}

func parseWithinFixture(limits ParseLimits) bool {
	fixture := FixtureParseLimits
	return limits.MaxBytes <= fixture.MaxBytes &&
		limits.MaxDepth <= fixture.MaxDepth &&
		limits.MaxArrayItems <= fixture.MaxArrayItems &&
		limits.MaxObjectKeys <= fixture.MaxObjectKeys &&
		limits.MaxStringBytes <= fixture.MaxStringBytes
}

func validateSourceSpecs(sources []SourceSpec, maxSources int) ([]SourceSpec, error) {
	if len(sources) > maxSources {
		return nil, ErrCollectionSources
	}
	out := make([]SourceSpec, len(sources))
	seen := map[string]struct{}{}
	for i, source := range sources {
		if !collectionToken.MatchString(source.Provider) || !collectionToken.MatchString(source.ProducerRef) || !collectionToken.MatchString(source.ProducerIncarnation) || !collectionToken.MatchString(source.SourceVersion) {
			return nil, ErrCollectionSources
		}
		if source.BindingGeneration < 0 || !knownBinding(source.BindingState) || !knownSourceKind(source.SourceKind) {
			return nil, ErrCollectionSources
		}
		if source.AccountRef != nil && !collectionToken.MatchString(*source.AccountRef) {
			return nil, ErrCollectionSources
		}
		if source.WorkspaceRef != nil && !collectionToken.MatchString(*source.WorkspaceRef) {
			return nil, ErrCollectionSources
		}
		key := source.Provider + "\x00" + source.ProducerRef
		if _, dup := seen[key]; dup {
			return nil, ErrCollectionSources
		}
		seen[key] = struct{}{}
		out[i] = cloneSpec(source)
	}
	return out, nil
}
