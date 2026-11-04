package providerallowance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// This file characterizes how a collection slot is projected into a
// SourceStatus (and, when a hold exists, a ReadItem). It pins what the code
// does at this base. It does not claim every row is the desired product
// behaviour.

type matrixWant struct {
	availability providerallowance.Availability
	producerRef  string
	attempt      providerallowance.AttemptResult
	code         string // empty means a nil code
	freshState   providerallowance.FreshnessState
	freshBasis   providerallowance.FreshnessBasis
	freshReason  string
	freshAsOf    string
	validation   providerallowance.ValidationState
	binding      providerallowance.BindingState
	lastGood     bool
	hasItem      bool
	currentState providerallowance.CurrentState
	currentWhy   string
}

type matrixCase struct {
	name   string
	build  func(t *testing.T, clock *fakeClock) (sources []providerallowance.SourceSpec, steps []acquireStep, run bool, cycles int)
	served time.Time
	want   matrixWant
}

const (
	matrixBaseline   = "2026-11-02T03:05:00Z"
	matrixReceived   = "2026-11-02T03:05:01Z"
	matrixObserved   = "2026-11-02T02:59:00Z"
	matrixProducer   = "fixture-producer"
	matrixNoSlotName = "collection-unconfigured"
)

func matrixError(clock *fakeClock) acquireStep {
	return func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
		clock.Advance(collectionSuccessLatency)
		return providerallowance.Acquisition{}, errors.New("synthetic failure")
	}
}

func matrixUnavailable(clock *fakeClock, availability providerallowance.Availability) acquireStep {
	return func(context.Context, providerallowance.SourceSpec) (providerallowance.Acquisition, error) {
		clock.Advance(collectionSuccessLatency)
		return providerallowance.Acquisition{Unavailable: &providerallowance.Unavailable{Availability: availability}}, nil
	}
}

func TestCollectionStatusMatrix(t *testing.T) {
	spec := []providerallowance.SourceSpec{openaiSpec(sp("fixture-account"))}
	good := collectionObs(t, obsOpts{id: "obs-good", provider: "openai", decimal: "11"})
	changed := collectionObs(t, obsOpts{id: "obs-good", provider: "openai", decimal: "99"})
	crossAccount := mutateObs(t, collectionObs(t, obsOpts{id: "obs-cross", provider: "openai", decimal: "11"}), func(obs *providerallowance.Observation) {
		obs.Scope.AccountRef = sp("other-account")
	})
	// MaxAge is 30 minutes from the provider observation at 02:59:00.
	insideMaxAge := time.Date(2026, 11, 2, 3, 29, 0, 0, time.UTC)
	beyondMaxAge := insideMaxAge.Add(time.Second)

	cases := []matrixCase{
		{
			name: "1a-configured-slot-never-attempted",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, nil, false, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnconfigured, producerRef: matrixProducer,
				attempt: providerallowance.AttemptUnknown, code: "source_unconfigured",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisNone, freshReason: "source not configured", freshAsOf: matrixBaseline,
				validation: providerallowance.ValidationUnverified, binding: providerallowance.BindingUnknown,
			},
		},
		{
			name: "1b-provider-without-any-slot",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return nil, nil, false, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnconfigured, producerRef: matrixNoSlotName,
				attempt: providerallowance.AttemptUnknown, code: "source_unconfigured",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisNone, freshReason: "source not configured", freshAsOf: matrixBaseline,
				validation: providerallowance.ValidationUnverified, binding: providerallowance.BindingUnknown,
			},
		},
		{
			name: "2a-attempt-error-no-hold",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{matrixError(clock)}, true, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnavailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptError, code: "acquisition_error",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisNone, freshReason: "acquisition failed", freshAsOf: matrixReceived,
				validation: providerallowance.ValidationUnverified, binding: providerallowance.BindingUnknown,
			},
		},
		{
			name: "2b-attempt-unavailable-no-hold",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{matrixUnavailable(clock, providerallowance.AvailabilityUnavailable)}, true, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnavailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptUnavailable, code: "acquisition_unavailable",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisNone, freshReason: "acquisition failed", freshAsOf: matrixReceived,
				validation: providerallowance.ValidationUnverified, binding: providerallowance.BindingUnknown,
			},
		},
		{
			name: "2c-attempt-reports-unconfigured-no-hold",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{matrixUnavailable(clock, providerallowance.AvailabilityUnconfigured)}, true, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnconfigured, producerRef: matrixProducer,
				attempt: providerallowance.AttemptUnavailable, code: "acquisition_unconfigured",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisNone, freshReason: "acquisition failed", freshAsOf: matrixReceived,
				validation: providerallowance.ValidationUnverified, binding: providerallowance.BindingUnknown,
			},
		},
		{
			name: "3a-accepted-hold-just-inside-max-age",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{successStep(clock, good)}, true, 1
			},
			served: insideMaxAge,
			want: matrixWant{
				availability: providerallowance.AvailabilityAvailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptSuccess, code: "",
				freshState: providerallowance.FreshFresh, freshBasis: providerallowance.BasisProvider, freshReason: "", freshAsOf: matrixObserved,
				validation: providerallowance.ValidationSourceFields, binding: providerallowance.BindingVerified,
				lastGood: true, hasItem: true, currentState: providerallowance.CurrentKnown,
			},
		},
		{
			name: "3b-accepted-hold-just-beyond-max-age",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{successStep(clock, good)}, true, 1
			},
			served: beyondMaxAge,
			want: matrixWant{
				availability: providerallowance.AvailabilityAvailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptSuccess, code: "",
				freshState: providerallowance.FreshStale, freshBasis: providerallowance.BasisProvider, freshReason: "provider time stale", freshAsOf: matrixObserved,
				validation: providerallowance.ValidationSourceFields, binding: providerallowance.BindingVerified,
				lastGood: true, hasItem: true, currentState: providerallowance.CurrentKnown,
			},
		},
		{
			name: "3c-accepted-hold-then-failed-attempt",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{successStep(clock, good), matrixError(clock)}, true, 2
			},
			served: insideMaxAge,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnavailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptError, code: "acquisition_error",
				freshState: providerallowance.FreshFresh, freshBasis: providerallowance.BasisProvider, freshReason: "", freshAsOf: matrixObserved,
				validation: providerallowance.ValidationSourceFields, binding: providerallowance.BindingVerified,
				lastGood: true, hasItem: true, currentState: providerallowance.CurrentKnown,
			},
		},
		{
			name: "4-rejected-hold-cross-account",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{successStep(clock, crossAccount)}, true, 1
			},
			served: collectionServed,
			want: matrixWant{
				availability: providerallowance.AvailabilityUnavailable, producerRef: matrixProducer,
				attempt: providerallowance.AttemptError, code: "acquisition_rejected",
				freshState: providerallowance.FreshUnknown, freshBasis: providerallowance.BasisCapture, freshReason: "binding not actionable", freshAsOf: matrixReceived,
				validation: providerallowance.ValidationRejected, binding: providerallowance.BindingMismatch,
				lastGood: false, hasItem: true, currentState: providerallowance.CurrentUnknown, currentWhy: "binding not actionable",
			},
		},
		{
			name: "5-conflict-same-identity-different-body",
			build: func(t *testing.T, clock *fakeClock) ([]providerallowance.SourceSpec, []acquireStep, bool, int) {
				return spec, []acquireStep{successStep(clock, good), successStep(clock, changed)}, true, 2
			},
			served: insideMaxAge,
			want: matrixWant{
				availability: providerallowance.AvailabilityConflict, producerRef: matrixProducer,
				attempt: providerallowance.AttemptError, code: "acquisition_conflict",
				freshState: providerallowance.FreshFresh, freshBasis: providerallowance.BasisProvider, freshReason: "", freshAsOf: matrixObserved,
				validation: providerallowance.ValidationConflict, binding: providerallowance.BindingVerified,
				lastGood: true, hasItem: true, currentState: providerallowance.CurrentConflict, currentWhy: "conflict withheld",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{now: collectionStart}
			sources, steps, run, cycles := tc.build(t, clock)
			acquire := &scriptAcquire{t: t}
			if len(steps) > 0 {
				acquire = &scriptAcquire{t: t, steps: map[string][]acquireStep{"openai": steps}}
			}
			collection := mustCollection(t, collectionBounds(30*time.Minute, cycles), sources, fixtureOnlyRegistry(t), clock, &fakeWait{}, acquire)
			if run {
				if err := collection.Run(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			doc, _, err := collection.Snapshot(tc.served)
			if err != nil {
				t.Fatal(err)
			}
			status := statusBy(t, doc, "openai")
			assertMatrixStatus(t, status, tc.want)
			if !tc.want.hasItem {
				if len(doc.Items) != 0 {
					t.Fatalf("items=%d, want none without a hold", len(doc.Items))
				}
				return
			}
			item := onlyItem(t, doc, "openai")
			// The status reuses the item's freshness, validation and binding.
			if item.Freshness.State != tc.want.freshState || item.Freshness.Basis != tc.want.freshBasis || item.Freshness.Reason != tc.want.freshReason || item.Freshness.AsOf != tc.want.freshAsOf {
				t.Fatalf("item freshness=%+v", item.Freshness)
			}
			if item.Validation.State != tc.want.validation || item.Binding.State != tc.want.binding {
				t.Fatalf("item validation=%+v binding=%+v", item.Validation, item.Binding)
			}
			if len(item.CurrentMeasures) != 1 || item.CurrentMeasures[0].State != tc.want.currentState || item.CurrentMeasures[0].Reason != tc.want.currentWhy {
				t.Fatalf("item current=%+v", item.CurrentMeasures)
			}
		})
	}
}

func assertMatrixStatus(t *testing.T, status providerallowance.SourceStatus, want matrixWant) {
	t.Helper()
	if status.Availability != want.availability || status.ProducerRef != want.producerRef {
		t.Fatalf("availability=%s producer=%s", status.Availability, status.ProducerRef)
	}
	if status.LastAttempt.Result != want.attempt {
		t.Fatalf("attempt result=%s", status.LastAttempt.Result)
	}
	if want.code == "" {
		if status.LastAttempt.Code != nil {
			t.Fatalf("attempt code=%q, want nil", *status.LastAttempt.Code)
		}
	} else if status.LastAttempt.Code == nil || *status.LastAttempt.Code != want.code {
		t.Fatalf("attempt code=%v, want %s", status.LastAttempt.Code, want.code)
	}
	fresh := status.Freshness
	if fresh.State != want.freshState || fresh.Basis != want.freshBasis || fresh.Reason != want.freshReason || fresh.AsOf != want.freshAsOf {
		t.Fatalf("freshness=%+v", fresh)
	}
	if status.Validation.State != want.validation || status.Binding.State != want.binding {
		t.Fatalf("validation=%+v binding=%+v", status.Validation, status.Binding)
	}
	if (status.LastGood != nil) != want.lastGood {
		t.Fatalf("lastGood=%+v, want present=%v", status.LastGood, want.lastGood)
	}
	if status.LastGood != nil && (status.LastGood.ObservationID != "obs-good" || status.LastGood.ReceivedAt != matrixReceived) {
		t.Fatalf("lastGood=%+v", status.LastGood)
	}
}
